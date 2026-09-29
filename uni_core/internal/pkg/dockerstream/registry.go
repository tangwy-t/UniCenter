// Package dockerstream 是 core 侧的**流会话注册表**（三期日志 Follow / 终端）。
//
// 它管三件事，全都是「进程内、会话级」的：
//  1. 登记：agent 回 result{session_id} 时建一条会话（谁发起的、哪台设备、哪条 ref）；
//  2. 投递：agent.docker.frame 到达时按 session_id 找到会话，写入它的待消费缓冲；
//  3. 接入：HTTP 流端点（NDJSON / WebSocket）把会话接给**唯一一个**消费者，重复接入 409。
//
// 为什么是内存而不是 Redis（与 dockerstate 的其余键相反）：会话的另一端是**本进程
// 持有的 agent WebSocket 连接**，把注册表放进 Redis 并不能让另一实例把帧投递过来；
// 多实例部署在本域本就是已声明的限制（见 service/docker_cmd.go 的 ref 生成说明）。
// 一次性 ticket 才是 Redis 键（docker:stream_ticket:*，spec §4.3.2 的键清单）——
// 它是授权凭证，必须在任一实例上可校验、可单次消费。
//
// 三条不变式（测试逐条钉住）：
//   - 投递**绝不阻塞**（帧来自 agent 连接的唯一读循环，堵住它会连累指标/心跳）；
//     缓冲满丢最旧并计数，seq 缺口由消费端自行发现。
//   - 接入是**排他**的：一条会话同时只能有一个消费者（第二个 409）。
//   - 清理是**幂等**的：Remove/Release 重复调用安全；空闲超时按「无数据」判定。
package dockerstream

import (
	"context"
	"errors"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// Kind 是会话种类：它的差别只在结论句与日志口径（缓冲/丢弃策略由投递侧统一执行）。
type Kind uint8

const (
	// KindLog 是日志流（container:logs{follow:true}）。
	KindLog Kind = iota
	// KindPTY 是终端（container:exec）。
	KindPTY
)

func (k Kind) String() string {
	if k == KindPTY {
		return "pty"
	}
	return "log"
}

// KindForAction 按 action 判定会话种类（日志之外的会话按日志处理：多一条流不致命，
// 少登记一条会让用户对着「会话不存在」发呆）。
func KindForAction(action string) Kind {
	if action == agentproto.DockerActionContainerExec {
		return KindPTY
	}
	return KindLog
}

const (
	// MaxSessionsPerDevice 是每台设备并发的流会话上限，与 agent 侧同值（plan §0）。
	// core 在受理处**先拒**，给出的结论句比 agent 的 errStreamLimit 更贴近用户动作。
	MaxSessionsPerDevice = 3
	// defaultQueueDepth 是未消费帧的缓冲深度。
	//
	// 64 帧 ≈ 满速（15 帧/s）下 4 秒的余量：客户端从「轮询拿到 ticket」到「接上流」
	// 通常 <1s，正常路径远到不了这个深度；给足余量是为了容忍一次网络抖动。
	defaultQueueDepth = 64
	// defaultIdleTimeout 是「无数据」的清理时限（与 agent 的会话空闲超时同值）。
	defaultIdleTimeout = 10 * time.Minute
)

// 注册表的语义错误。handler/service 按 `errors.Is` 映射成 HTTP 结论（403/409/404），
// 不在此处引入 apperror —— 本包不依赖 HTTP 层。
var (
	// ErrNotFound 表示会话不存在或已被清理。
	ErrNotFound = errors.New("dockerstream: 会话不存在或已结束")
	// ErrForbidden 表示接入者不是发起人（spec §10.5 的会话劫持防护）。
	ErrForbidden = errors.New("dockerstream: 会话不属于该用户")
	// ErrBusy 表示会话已有消费者（同一会话重复连接 → 409）。
	ErrBusy = errors.New("dockerstream: 会话已有连接")
	// ErrClosed 表示会话已收尾（eof 已到且缓冲已排空 / 已被取消）。
	ErrClosed = errors.New("dockerstream: 会话已结束")
	// ErrDuplicate 表示同一 session_id 被重复登记（agent 的 id 是 crypto/rand，
	// 撞号意味着串号或伪造）。
	ErrDuplicate = errors.New("dockerstream: 会话 id 重复")
	// errBadMeta 表示登记参数不全（编程错误，不是用户输入问题）。
	errBadMeta = errors.New("dockerstream: 会话元数据不完整")
)

// Meta 是登记一条会话所需的全部元数据（来自指令记录；session_id 来自 agent 的 result）。
type Meta struct {
	SessionID string
	DeviceID  uint64
	UserID    uint64
	Action    string
	Ref       string
	Kind      Kind
	CreatedAt time.Time
}

// Options 是注册表的可注入参数（测试要确定性时钟与短超时）。
type Options struct {
	QueueDepth           int
	IdleTimeout          time.Duration
	MaxSessionsPerDevice int
	Now                  func() time.Time
}

func (o Options) withDefaults() Options {
	if o.QueueDepth <= 0 {
		o.QueueDepth = defaultQueueDepth
	}
	if o.IdleTimeout <= 0 {
		o.IdleTimeout = defaultIdleTimeout
	}
	if o.MaxSessionsPerDevice <= 0 {
		o.MaxSessionsPerDevice = MaxSessionsPerDevice
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// Registry 是 session_id → 会话 的注册表。
type Registry struct {
	opts Options
	log  logger.LoggerInterface

	mu       sync.Mutex
	sessions map[string]*Session
}

// NewRegistry 构造注册表（缺省参数）。
func NewRegistry(log logger.LoggerInterface) *Registry {
	return NewRegistryWithOptions(Options{}, log)
}

// NewRegistryWithOptions 构造注册表（显式参数，缺省值由 withDefaults 补齐）。
func NewRegistryWithOptions(opts Options, log logger.LoggerInterface) *Registry {
	return &Registry{opts: opts.withDefaults(), log: log, sessions: make(map[string]*Session)}
}

// Register 登记一条会话（agent 回 result{session_id} 时调用）。
//
// 每设备上限溢出时**淘汰最旧**的一条而不是拒绝新的：agent 侧同有 3 条上限，
// core 出现第 4 条只可能是旧会话泄漏（agent 重启/断连后 core 没收到 eof），
// 拒绝新会话会让用户「刚建的终端打不开」，而淘汰最旧是自愈且可观测的。
func (r *Registry) Register(meta Meta) error {
	if meta.SessionID == "" || meta.DeviceID == 0 || meta.UserID == 0 {
		return errBadMeta
	}
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = r.opts.Now()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.sessions[meta.SessionID]; exists {
		return ErrDuplicate
	}
	for len(r.deviceSessionsLocked(meta.DeviceID)) >= r.opts.MaxSessionsPerDevice {
		oldest := oldestOf(r.deviceSessionsLocked(meta.DeviceID))
		if oldest == nil {
			break
		}
		delete(r.sessions, oldest.meta.SessionID)
		oldest.markClosed()
		r.warn("docker stream session evicted (per-device limit)",
			zap.String("session", oldest.meta.SessionID),
			zap.Uint64("deviceId", meta.DeviceID),
			zap.String("ref", oldest.meta.Ref))
	}
	s := &Session{
		meta:         meta,
		reg:          r,
		depth:        r.opts.QueueDepth,
		lastActivity: r.opts.Now(),
		wake:         make(chan struct{}, 1),
	}
	r.sessions[meta.SessionID] = s
	return nil
}

// oldestOf 返回集合里创建最早的一条（nil 表示空集合）。
func oldestOf(list []*Session) *Session {
	var out *Session
	for _, s := range list {
		if out == nil || s.meta.CreatedAt.Before(out.meta.CreatedAt) {
			out = s
		}
	}
	return out
}

// deviceSessionsLocked 返回该设备当前**未收尾**的会话（调用方必须持 r.mu）。
func (r *Registry) deviceSessionsLocked(deviceID uint64) []*Session {
	out := make([]*Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		if s.meta.DeviceID == deviceID && !s.isClosed() {
			out = append(out, s)
		}
	}
	return out
}

// Get 按 id 返回会话（不存在返回 nil）。
func (r *Registry) Get(id string) *Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessions[id]
}

// Len 返回当前会话数（测试与日志用）。
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sessions)
}

// CountByDevice 返回该设备当前未收尾的会话数（受理前的上限判定用）。
func (r *Registry) CountByDevice(deviceID uint64) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.deviceSessionsLocked(deviceID))
}

// All 返回全部会话的快照（设备离线判定用）。
func (r *Registry) All() []*Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		out = append(out, s)
	}
	return out
}

// Expired 返回空闲超过 IdleTimeout 的会话（lastActivity 由投递与控制帧刷新）。
func (r *Registry) Expired(now time.Time) []*Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*Session
	for _, s := range r.sessions {
		if now.Sub(s.lastActivityTime()) >= r.opts.IdleTimeout {
			out = append(out, s)
		}
	}
	return out
}

// DeliverDockerFrame 投递一条 agent.docker.frame（agenthub 的窄接口实现）。
//
// **绝不阻塞**：帧缓冲满时丢最旧并计数（见包注释）。会话不存在是正常时序
// （取消/超时后的迟到帧），返回 ErrNotFound 让调用方按 Debug 记录。
func (r *Registry) DeliverDockerFrame(_ context.Context, deviceID uint64, f *agentproto.DockerFrame) error {
	if f == nil {
		return nil
	}
	r.mu.Lock()
	s := r.sessions[f.SessionID]
	r.mu.Unlock()
	if s == nil {
		return ErrNotFound
	}
	if s.meta.DeviceID != deviceID {
		// 一台设备发来别人的 session_id：伪造或串号，丢弃并告警（与 result 归属校验同向）。
		r.warn("docker frame device mismatch, dropped",
			zap.Uint64("fromDevice", deviceID),
			zap.Uint64("expectDevice", s.meta.DeviceID),
			zap.String("session", f.SessionID))
		return ErrForbidden
	}
	// 解码路径已校验过载荷；这里再校验一次是防御（协议纪律：core 侧不信任任何帧）。
	if err := f.Validate(); err != nil {
		r.warn("docker frame invalid, dropped", zap.String("session", f.SessionID), zap.Error(err))
		return err
	}
	s.push(f, r.opts.Now())
	return nil
}

// Attach 把会话接给一个消费者（userID 必须是发起人）。错误语义见各 Err* 常量。
func (r *Registry) Attach(id string, userID uint64) (*Session, error) {
	r.mu.Lock()
	s := r.sessions[id]
	r.mu.Unlock()
	if s == nil {
		return nil, ErrNotFound
	}
	if s.meta.UserID != userID {
		// 归属校验的**第二道**（第一道在指令记录的发起人比对）：即使有人拿到了
		// 别处的 ticket/ref，也无法以另一个 userId 接入。
		return nil, ErrForbidden
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attached {
		return nil, ErrBusy
	}
	if s.closed && len(s.buf) == 0 {
		// eof 已被前一个消费者排空：会话已经结束，不能再接（与不存在同归一类）。
		return nil, ErrClosed
	}
	s.attached = true
	return s, nil
}

// Release 解除接入占用（接入后连接建立失败时调用：会话保留，可凭新 ticket 重接）。
func (r *Registry) Release(id string) {
	if s := r.Get(id); s != nil {
		s.mu.Lock()
		s.attached = false
		s.mu.Unlock()
	}
}

// Remove 清理一条会话（幂等）：从注册表删除并唤醒等待中的消费者（它会收到 ErrClosed）。
func (r *Registry) Remove(id string) {
	r.mu.Lock()
	s := r.sessions[id]
	delete(r.sessions, id)
	r.mu.Unlock()
	if s != nil {
		s.markClosed()
	}
}

func (r *Registry) warn(msg string, fields ...zap.Field) {
	if r.log != nil {
		r.log.Warn(msg, fields...)
	}
}

// ── 单条会话 ────────────────────────────────────────────────────────────

// Session 是一条流会话：agent 的帧进缓冲，唯一消费者按序取走。
type Session struct {
	meta  Meta
	reg   *Registry
	depth int

	mu           sync.Mutex
	buf          []*agentproto.DockerFrame
	attached     bool
	closed       bool
	dropped      int
	dropWarned   bool
	lastActivity time.Time
	// wake 是「有数据 / 已收尾」的唤醒信号（容量 1，合并）。
	wake chan struct{}
}

// ID 返回会话句柄。
func (s *Session) ID() string { return s.meta.SessionID }

// DeviceID 返回会话所属设备（下发控制帧的路由依据）。
func (s *Session) DeviceID() uint64 { return s.meta.DeviceID }

// UserID 返回发起人（归属校验）。
func (s *Session) UserID() uint64 { return s.meta.UserID }

// Action 返回建立该会话的 action。
func (s *Session) Action() string { return s.meta.Action }

// Ref 返回建立该会话的指令号。
func (s *Session) Ref() string { return s.meta.Ref }

// Kind 返回会话种类（log / pty）。
func (s *Session) Kind() Kind { return s.meta.Kind }

// Attached 报告是否已有消费者接入。
func (s *Session) Attached() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attached
}

// Closed 报告会话是否已收尾（eof 已到 / 已被取消）。
func (s *Session) Closed() bool { return s.isClosed() }

func (s *Session) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// Buffered 返回当前未消费的帧数。
func (s *Session) Buffered() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.buf)
}

// Dropped 返回因缓冲满被丢弃的帧数。
func (s *Session) Dropped() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}

func (s *Session) lastActivityTime() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastActivity
}

// Touch 刷新活动时刻（下发 input/resize 也算活动：终端上「用户在敲」与「有输出」
// 同等说明会话还活着，10 分钟空闲判定的口径与 agent 侧一致）。
func (s *Session) Touch() {
	s.mu.Lock()
	s.lastActivity = s.reg.opts.Now()
	s.mu.Unlock()
}

// push 写入一帧。**不阻塞**：缓冲满丢最旧并计数。f.EOF 标记会话收尾
// （已缓冲的帧仍会被消费者取走，取空后才返回 ErrClosed —— 保证 eof 之前的数据不丢）。
func (s *Session) push(f *agentproto.DockerFrame, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.lastActivity = now
	if f.EOF {
		s.closed = true
	}
	if !f.EOF && len(s.buf) >= s.depth {
		s.buf = s.buf[1:]
		s.dropped++
		if !s.dropWarned {
			s.dropWarned = true
			s.reg.warn("docker stream buffer overflow, dropped oldest frames",
				zap.String("session", s.meta.SessionID),
				zap.Uint64("deviceId", s.meta.DeviceID),
				zap.String("kind", s.meta.Kind.String()))
		}
	}
	s.buf = append(s.buf, f)
	s.signalLocked()
}

// markClosed 本地收尾（唤醒等待者）。已收尾时是空操作。
func (s *Session) markClosed() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.signal()
}

func (s *Session) signal() {
	s.signalLocked()
}

func (s *Session) signalLocked() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Next 取下一帧：缓冲空且未收尾时等待（ctx 取消 / 有新数据 / 会话收尾）。
// 缓冲取空且已收尾时返回 ErrClosed；ctx 取消时返回 ctx.Err()。
func (s *Session) Next(ctx context.Context) (*agentproto.DockerFrame, error) {
	for {
		s.mu.Lock()
		if len(s.buf) > 0 {
			f := s.buf[0]
			s.buf = s.buf[1:]
			s.mu.Unlock()
			return f, nil
		}
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return nil, ErrClosed
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.wake:
		}
	}
}
