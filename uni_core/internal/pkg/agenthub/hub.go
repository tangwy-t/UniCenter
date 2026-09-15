package agenthub

import (
	"errors"
	"sort"
	"sync"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
	"go.uber.org/zap"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
)

// errNilConn 由 Register 在收到 nil 连接时返回 —— 一个笔误不该打崩整个进程，
// 也不该在注册表里留下 0 号设备的槽位。
var errNilConn = errors.New("agenthub: nil conn")

// Hub 是「设备 ID → 连接」的注册表。
//
// 并发模型：一把 Mutex 保护 map。连接数是**单机千级**（设计上限 500~1000 设备），
// 用不着分片；读多写少，锁竞争不是瓶颈。
//
// 顶替策略：同设备再次注册时**顶掉旧连接**（agent 重连/重装都会走到这里），
// 但**注销必须按连接身份**（by-identity）：否则旧连接的延迟注销会把刚注册的
// 新连接挤掉，表现为「设备莫名其妙离线」。
type Hub struct {
	opts Options
	log  logger.LoggerInterface

	mu    sync.Mutex
	conns map[uint64]*Conn
}

func NewHub(opts Options, log logger.LoggerInterface) *Hub {
	return &Hub{opts: opts.withDefaults(), log: log, conns: make(map[uint64]*Conn)}
}

// Register 注册连接；同设备已存在时顶掉旧连接并返回 nil（不是错误）。
//
// 顶替之所以不是错误：agent 重连/重装/多实例都表现为「同设备第二条连接」，
// 服务端必须接受新的那条（新连接才拿着最新凭据），并把旧的那条用
// `CloseDuplicateInstance` 关掉，让客户端知道该换新 token 重连。
func (h *Hub) Register(c *Conn) error {
	if c == nil {
		return errNilConn
	}
	deviceID := c.deviceID()

	h.mu.Lock()
	old, existed := h.conns[deviceID]
	h.conns[deviceID] = c
	h.mu.Unlock()

	if existed && old != c {
		// 顶替：给旧连接发关闭码并断开（在锁外做，避免持锁写 socket）
		old.CloseWith(agentproto.CloseDuplicateInstance, "")
		h.log.Info("agent connection replaced", zap.Uint64("device_id", deviceID))
	}
	return nil
}

// Unregister 按**连接身份**注销：只有当前登记的就是 c 时才移除。
//
// 为什么不能只比 deviceID：旧连接的关闭/断开会**延迟**触发注销，
// 而那时注册表里已经是新连接了 —— 只比 ID 就会把新连接挤掉，
// 表现为「设备刚重连上线又莫名离线」。c 为 nil 表示调用方无法提供身份
// （例如 handler 在握手失败路径上的兜底清理），此时按设备 ID 注销。
func (h *Hub) Unregister(deviceID uint64, c *Conn) {
	h.mu.Lock()
	cur, ok := h.conns[deviceID]
	if ok && (c == nil || cur == c) {
		delete(h.conns, deviceID)
	}
	h.mu.Unlock()
}

// Get 返回该设备当前的连接。
func (h *Hub) Get(deviceID uint64) (*Conn, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.conns[deviceID]
	return c, ok
}

// OnlineCount 返回当前在线连接数。
func (h *Hub) OnlineCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.conns)
}

// DeviceIDs 返回在线设备 ID 的**升序**快照（flush 需要稳定遍历顺序：
// map 迭代顺序随机，不排序会让每轮的遍历顺序漂移，指标补齐与日志都不可复现）。
func (h *Hub) DeviceIDs() []uint64 {
	h.mu.Lock()
	out := make([]uint64, 0, len(h.conns))
	for id := range h.conns {
		out = append(out, id)
	}
	h.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// CloseDevice 向指定设备的连接下发关闭码；设备不在线返回 false。
//
// reason 只是日志细节：线上 reason 由 CloseWith 取 `agentproto.CloseReason(code)`。
func (h *Hub) CloseDevice(deviceID uint64, code int, reason string) bool {
	h.mu.Lock()
	c, ok := h.conns[deviceID]
	h.mu.Unlock()
	if !ok {
		return false
	}
	c.CloseWith(code, reason)
	return true
}

// DrainAll 向注册表里的每条连接下发「服务端排空」关闭码（停机 drain 相位用）。
// 返回被通知的连接数。
//
// 通知的是**注册表里的每一条**（含已关闭但尚未注销的连接）；关闭幂等保证
// 已关闭的连接不会收到第二帧。reason 只进服务端日志：线上 reason 一律取
// `agentproto.CloseReason(CloseServerShutdown)`（协议规范话术）。
func (h *Hub) DrainAll(reason string) int {
	h.mu.Lock()
	cs := make([]*Conn, 0, len(h.conns))
	for _, c := range h.conns {
		cs = append(cs, c)
	}
	h.mu.Unlock()

	for _, c := range cs {
		c.CloseWith(agentproto.CloseServerShutdown, "")
	}
	h.log.Info("draining agent connections",
		zap.Int("notified", len(cs)),
		zap.String("detail", reason))
	return len(cs)
}
