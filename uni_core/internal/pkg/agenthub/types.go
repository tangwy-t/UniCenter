// Package agenthub 承载 agent 侧的 WebSocket 长连接：注册、鉴权、心跳、指标入湖。
//
// 为什么不复用 internal/pkg/ws：那个包面向 console（JWT + 通知推送 + 踢人），
// 走 MsgTypeAuth/auth_ok 那套文本协议；agent 侧走 agentproto 的信封与方向校验，
// 鉴权模型也不同（共享 enroll 令牌 + 每设备 token）。两者共用只会互相污染。
package agenthub

import (
	"context"
	"sync"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
)

// 关闭码：**直接使用协议常量，不自造别名**。
//
// 为什么不自造语义别名：协议已经为每一种情形定义了精确的码（见下表），
// 自造别名只会掩盖差异、并在协议演进时悄悄漂移。hub 里一律直接写
// `agentproto.CloseXxx`，让「哪个情形对应哪个码」在代码里一眼可见。
//
// | 情形 | 关闭码 |
// |---|---|
// | 信封 JSON 非法 / Validate 失败 | `CloseMalformedMessage`(4002) |
// | 收到 core.*（**方向错误**）或回环消息 | `CloseUnsupportedType`(4003) |
// | hello 之前发其它消息 / 重复 hello | `CloseProtocolViolation`(4008) |
// | 协议版本不在受支持区间 | `CloseVersionMismatch`(4000) |
// | enroll/agent token 缺失或非法 | `CloseUnauthorized`(4001) |
// | 设备被停用或已删除 | `CloseUnauthorized`(4001) |
// | 同 instance_id 顶号（旧连接被顶替） | `CloseDuplicateInstance`(4004) |
// | 活跃时间超时（读超时） | `CloseHeartbeatTimeout`(4005) |
// | 上报过于频繁 | `CloseRateLimited`(4006) |
// | 服务端停机排空 | `CloseServerShutdown`(4007) |
//
// **reason 一律用 `agentproto.CloseReason(code)`**：它返回 `"code|human"` 形式
// 并按 UTF-8 边界截断到 `MaxCloseReasonBytes`；自造 reason 既会超长截断出错、
// 也丢掉了协议的规范话术。自定义细节（设备 ID、原因）走**服务端日志**而不是线上 reason。

// Conn 是单条 agent 连接的状态与出站通路。
//
// 为什么出站交互收敛在 `sendFn`/`writeCloseFn` 两个函数字段上：hub 的顶替与
// 关闭路径必须能在**不写真实 socket** 的前提下被测试（见 hub_test.go 的 newIdleConn），
// 而 gorilla 的 `*websocket.Conn` 不允许并发写、也不该被测试替身冒充。
// 生产态由 conn.go 的 NewConn 把这两个字段绑到真实 `websocket.Conn` 上；
// 测试态留 nil 即可短路。入站/控制通路是 `ws`（conn.go 的 socket 窄接口），
// 同样在生产态绑真实连接、测试态绑内存替身。
//
// 读循环、心跳、入湖与背压计数不在这里 —— 见 conn.go。
type Conn struct {
	hub  *Hub
	opts Options
	deps Deps
	log  logger.LoggerInterface

	// ws 是入站/控制通路（生产态 = 真实 *websocket.Conn，见 conn.go 的 socket 窄接口）；
	// nil 表示未接管真实 socket（测试态）。
	ws socket

	// sendFn/writeCloseFn 是**唯一**的出站通路；nil 表示未接管真实 socket（测试态）。
	sendFn       func(b []byte) error
	writeCloseFn func(code int, reason string) error

	// send 是数据帧的出站队列（Task 2 的背压面）：满了就丢弃并计数，绝不阻塞读循环。
	// 控制帧（ping / 关闭）不走这里 —— 见 conn.go 的 sendPing 与 finish。
	send chan []byte

	// done 在关闭时被关闭，用于让发送协程退出。
	done chan struct{}
	// once 保证关闭逻辑只执行一次 —— CloseWith 的幂等由它守住。
	once sync.Once

	mu      sync.Mutex
	state   ConnState
	devID   uint64
	dropped int64
	skew    int64
}

// deviceID 返回该连接当前绑定的设备 ID；尚未鉴权时为 0。
func (c *Conn) deviceID() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.devID
}

// CloseWith 关闭连接并下发关闭码 code。
//
// reason 是**服务端日志用的细节**（触发原因、设备 ID 等），**不会**进入线上关闭帧：
// 线上 reason 一律取 `agentproto.CloseReason(code)` —— 协议规范话术，且已按 UTF-8
// 边界截断到 `MaxCloseReasonBytes`。这样既不会因自造 reason 超长而被截断出错，
// 也保证客户端看到的始终是自己认识的话术。
//
// 两条不可退化的语义（conn.go 补完整状态机时**不得**破坏）：
//   - **幂等**：重复调用不 panic，关闭帧只下发一次（sync.Once 守）。
//   - **对 nil socket 短路**：未接管真实 socket 时（出站函数为 nil）不写、不 panic。
func (c *Conn) CloseWith(code int, reason string) {
	// 实现体在 conn.go 的 finish：它与「对端已断开的收尾」（teardown）共用同一个
	// once，才能保证 done 只被关一次 —— 两条收尾路径是并发到达的。
	c.once.Do(func() { c.finish(code, reason, true) })
}

// ConnState 是单连接的状态机状态。**只允许按 StateAwaitHello → StateEnrolling
// → StateActive → StateClosed 前进**（不复用「回到上一状态」的路径，避免半开态的歧义）。
type ConnState uint8

const (
	StateAwaitHello ConnState = iota
	StateEnrolling
	StateActive
	StateDraining
	StateClosed
)

func (s ConnState) String() string {
	switch s {
	case StateAwaitHello:
		return "await_hello"
	case StateEnrolling:
		return "enrolling"
	case StateActive:
		return "active"
	case StateDraining:
		return "draining"
	default:
		return "closed"
	}
}

// Options 是 hub 与连接的运行参数。零值会被 withDefaults 填成安全默认。
type Options struct {
	WriteTimeout time.Duration
	// PongWait 是「多久没收到任何消息即判定死连接」；PingInterval 必须明显小于它。
	PongWait     time.Duration
	PingInterval time.Duration
	// MaxMessageBytes 是单帧上限（默认取协议常量 MaxMessageBytes）。
	MaxMessageBytes int64
	// SendQueue 是每连接的发送队列深度；满时**丢弃并计数**（绝不阻塞读循环）。
	SendQueue int
}

func (o Options) withDefaults() Options {
	if o.WriteTimeout <= 0 {
		o.WriteTimeout = 10 * time.Second
	}
	if o.PongWait <= 0 {
		o.PongWait = 90 * time.Second
	}
	if o.PingInterval <= 0 {
		o.PingInterval = o.PongWait / 3
	}
	if o.MaxMessageBytes <= 0 {
		o.MaxMessageBytes = agentproto.MaxMessageBytes
	}
	if o.SendQueue <= 0 {
		o.SendQueue = 64
	}
	return o
}

// isRegisteredCloseCode 报告 code 是否在协议登记集内（守卫用：hub 里出现的每个
// 关闭码都必须登记过，否则客户端会收到一个它不认识的码）。
func isRegisteredCloseCode(code int) bool {
	for _, c := range agentproto.AllCloseCodes() {
		if c == code {
			return true
		}
	}
	return false
}

// Decider 是 hub 需要的「设备是否可接受上报」能力（消费方窄接口，
// 由 service.AgentIngestService 实现）。
type Decider interface {
	IsAccepting(ctx context.Context, deviceID uint64) (bool, error)
}
