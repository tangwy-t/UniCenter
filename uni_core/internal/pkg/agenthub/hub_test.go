package agenthub

import (
	"sync"
	"testing"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
)

// idleSink 是「未接管真实 socket」的连接所用的出站记录器：把出站帧收进内存，
// 让测试能断言「到底下发了哪个关闭码、下发了多少次」，而无需起真实 TCP。
type idleSink struct {
	mu     sync.Mutex
	sends  [][]byte
	closes []idleClose
}

type idleClose struct {
	code   int
	reason string
}

func (s *idleSink) send(b []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]byte, len(b))
	copy(cp, b)
	s.sends = append(s.sends, cp)
	return nil
}

func (s *idleSink) close(code int, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closes = append(s.closes, idleClose{code: code, reason: reason})
	return nil
}

func (s *idleSink) closeFrames() []idleClose {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]idleClose, len(s.closes))
	copy(out, s.closes)
	return out
}

func (s *idleSink) closeCodes() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]int, 0, len(s.closes))
	for _, c := range s.closes {
		out = append(out, c.code)
	}
	return out
}

// idleSinks 把 newIdleConn 构造的连接映射到它的记录器，让 newIdleConn 能保持
// `(h, id) *Conn` 这个签名（hub 的注册表测试只用得到连接本身），
// 而需要断言关闭帧的用例通过 idleSinkOf 取回记录器。
var idleSinks sync.Map // *Conn → *idleSink

// newIdleConn 构造一条**未接管真实 socket** 的 *Conn：设备 ID 已绑定、状态为
// StateActive，出站帧写进记录器而不是 socket。
//
// 存在的理由：hub 的顶替/关闭路径必须在「不写 socket」的前提下可被断言 ——
// 若发送路径是硬编码的 `c.ws.WriteMessage(...)`，测试就只能起真实 TCP 才能覆盖。
func newIdleConn(h *Hub, id uint64) *Conn {
	sink := &idleSink{}
	c := &Conn{
		hub:          h,
		opts:         h.cfg,
		log:          logger.NewNop(),
		sendFn:       sink.send,
		writeCloseFn: sink.close,
		done:         make(chan struct{}),
		state:        StateActive,
		devID:        id,
	}
	idleSinks.Store(c, sink)
	return c
}

// newBareConn 构造「连出站函数都是 nil」的连接：最极端的未接管 socket 形态，
// 用来钉住 CloseWith 对 nil socket 短路（不写、不 panic）。
func newBareConn(h *Hub, id uint64) *Conn {
	return &Conn{
		hub:   h,
		opts:  h.cfg,
		log:   logger.NewNop(),
		state: StateActive,
		devID: id,
	}
}

// stateOf 读取连接状态（同一包内直读字段，不必为测试提前暴露 Task 2 的 State()）。
func stateOf(c *Conn) ConnState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// idleSinkOf 取回 newIdleConn 为该连接挂的出站记录器。
func idleSinkOf(t *testing.T, c *Conn) *idleSink {
	t.Helper()
	v, ok := idleSinks.Load(c)
	if !ok {
		t.Fatal("该连接不是 newIdleConn 构造的（没有出站记录器）")
	}
	return v.(*idleSink)
}

func newTestHub(t *testing.T) *Hub {
	t.Helper()
	return NewHub(Options{WriteTimeout: 0, PongWait: 0, PingInterval: 0, MaxMessageBytes: 1 << 20, SendQueue: 8}, logger.NewNop())
}

func TestHubRegisterAndUnregister(t *testing.T) {
	h := newTestHub(t)

	c1 := newIdleConn(h, 1001)
	c2 := newIdleConn(h, 1002)

	if err := h.Register(c1); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := h.Register(c2); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if h.OnlineCount() != 2 {
		t.Fatalf("在线数 = %d, want 2", h.OnlineCount())
	}
	if got, ok := h.Get(1001); !ok || got != c1 {
		t.Fatalf("Get(1001) 未命中或串号")
	}

	// 注销后不再可查
	h.Unregister(1001, c1)
	if _, ok := h.Get(1001); ok {
		t.Fatal("注销后不应还能查到")
	}
	if h.OnlineCount() != 1 {
		t.Fatalf("注销后在线数 = %d, want 1", h.OnlineCount())
	}

	// nil 连接必须是**错误**而不是 panic：调用方一个笔误不该打崩进程。
	if err := h.Register(nil); err == nil {
		t.Fatal("Register(nil) 必须返回错误，不得静默成功")
	}
	if h.OnlineCount() != 1 {
		t.Fatalf("Register(nil) 不得改动注册表, 在线数 = %d, want 1", h.OnlineCount())
	}
	if _, ok := h.Get(0); ok {
		t.Fatal("Register(nil) 不得写入 0 号设备槽位")
	}
}

func TestHubRejectsDuplicateDeviceAndKeepsNewest(t *testing.T) {
	h := newTestHub(t)

	old := newIdleConn(h, 1001)
	dup := newIdleConn(h, 1001)

	if err := h.Register(old); err != nil {
		t.Fatal(err)
	}
	// 同设备第二条连接：注册成功（顶掉旧的），旧的被关闭
	if err := h.Register(dup); err != nil {
		t.Fatalf("同设备重连应成功（顶替策略）: %v", err)
	}
	if h.OnlineCount() != 1 {
		t.Fatalf("同设备只应保留 1 条连接, got %d", h.OnlineCount())
	}
	got, _ := h.Get(1001)
	if got != dup {
		t.Fatal("应保留最新连接")
	}
	// 被顶替的旧连接必须收到「顶号」码（客户端据此换新 token 重连），且只收到一次；
	// 新连接则完全不该被关闭。
	if codes := idleSinkOf(t, old).closeCodes(); len(codes) != 1 || codes[0] != agentproto.CloseDuplicateInstance {
		t.Fatalf("旧连接收到的关闭码 = %v, want [%d]", codes, agentproto.CloseDuplicateInstance)
	}
	if codes := idleSinkOf(t, dup).closeCodes(); len(codes) != 0 {
		t.Fatalf("新连接不得被顶替路径关闭, got %v", codes)
	}
	// 旧连接的注销不得把新连接挤掉（by-identity 注销）
	h.Unregister(1001, old)
	if g, ok := h.Get(1001); !ok || g != dup {
		t.Fatal("旧连接注销后，新连接必须仍在（注销必须按连接身份而非仅设备 ID）")
	}
}

func TestHubUnregisterByIdentityOnly(t *testing.T) {
	h := newTestHub(t)
	c := newIdleConn(h, 1001)
	_ = h.Register(c)

	// 用一个不同的 Conn 实例去注销同一设备：不得生效
	h.Unregister(1001, newIdleConn(h, 1001))
	if _, ok := h.Get(1001); !ok {
		t.Fatal("非同一连接的注销请求不得移除已注册连接")
	}
	// 反向：同一实例注销必须生效（否则「按身份」会退化成「永不注销」）
	h.Unregister(1001, c)
	if _, ok := h.Get(1001); ok {
		t.Fatal("同一连接的注销请求必须生效")
	}
}

func TestHubCloseDeviceUsesRegisteredCloseCode(t *testing.T) {
	h := newTestHub(t)
	c := newIdleConn(h, 1001)
	_ = h.Register(c)

	if !h.CloseDevice(1001, agentproto.CloseUnauthorized, "管理员停用") {
		t.Fatal("CloseDevice 应命中在线设备")
	}
	if h.CloseDevice(999999, agentproto.CloseUnauthorized, "x") {
		t.Fatal("对不在线设备应返回 false")
	}

	frames := idleSinkOf(t, c).closeFrames()
	if len(frames) != 1 {
		t.Fatalf("关闭帧应只下发一次, got %d", len(frames))
	}
	if frames[0].code != agentproto.CloseUnauthorized {
		t.Fatalf("下发的关闭码 = %d, want %d", frames[0].code, agentproto.CloseUnauthorized)
	}
	// 线上 reason 必须是协议规范话术（"code|human"），自定义细节只进日志。
	if want := agentproto.CloseReason(agentproto.CloseUnauthorized); frames[0].reason != want {
		t.Fatalf("线上 reason = %q, want %q", frames[0].reason, want)
	}
	if len(frames[0].reason) > agentproto.MaxCloseReasonBytes {
		t.Fatalf("线上 reason 长度 %d 超出上限 %d", len(frames[0].reason), agentproto.MaxCloseReasonBytes)
	}

	// CloseWith 必须幂等：重复关闭不 panic、不再下发关闭帧。
	c.CloseWith(agentproto.CloseUnauthorized, "重复关闭")
	if frames := idleSinkOf(t, c).closeFrames(); len(frames) != 1 {
		t.Fatalf("重复 CloseWith 不得再次下发关闭帧, got %d", len(frames))
	}
	if got := stateOf(c); got != StateClosed {
		t.Fatalf("CloseWith 后状态 = %v, want %v", got, StateClosed)
	}

	// 未接管真实 socket（出站函数为 nil）时也必须短路：不写、不 panic。
	bare := newBareConn(h, 1002)
	if err := h.Register(bare); err != nil {
		t.Fatalf("Register(bare): %v", err)
	}
	if !h.CloseDevice(1002, agentproto.CloseServerShutdown, "停机") {
		t.Fatal("CloseDevice 应命中 bare 连接")
	}
	bare.CloseWith(agentproto.CloseServerShutdown, "重复关闭")
	if got := stateOf(bare); got != StateClosed {
		t.Fatalf("bare 连接 CloseWith 后状态 = %v, want %v", got, StateClosed)
	}
}

func TestHubUsesRegisteredCloseCodes(t *testing.T) {
	// hub 里用到的每一个关闭码都必须在协议登记集内 ——
	// 否则客户端会收到不认识的码，只能当异常处理。
	for _, code := range []int{
		agentproto.CloseDuplicateInstance,
		agentproto.CloseUnauthorized,
		agentproto.CloseServerShutdown,
	} {
		if !isRegisteredCloseCode(code) {
			t.Fatalf("关闭码 %d 未在协议登记（AllCloseCodes=%v）", code, agentproto.AllCloseCodes())
		}
	}
	// 顶替必须用「顶号」语义的码，不是通用的 PolicyViolation
	if agentproto.CloseDuplicateInstance == agentproto.CloseUnauthorized {
		t.Fatal("顶号与鉴权失败必须是不同的码（客户端处理策略不同）")
	}

	// 反向钉住：把 hub 的三条关闭路径（顶替 / CloseDevice / DrainAll）都真跑一遍，
	// 收集**实际下发**的码逐个回查登记集 —— 守卫必须挂在真实路径上，
	// 而不是只检查常量本身（那样实现里写错一个码也照样绿）。
	h := newTestHub(t)
	replaced := newIdleConn(h, 1001)
	dup := newIdleConn(h, 1001)
	stopped := newIdleConn(h, 2001)
	live := newIdleConn(h, 2002)
	for _, c := range []*Conn{replaced, stopped, live} {
		if err := h.Register(c); err != nil {
			t.Fatalf("Register: %v", err)
		}
	}
	if err := h.Register(dup); err != nil { // 顶替 replaced
		t.Fatalf("Register(dup): %v", err)
	}
	if !h.CloseDevice(2001, agentproto.CloseUnauthorized, "管理员停用") {
		t.Fatal("CloseDevice 未命中 2001")
	}
	// DrainAll 通知的是**注册表里的每一条**（含已关闭但尚未注销的 2001）：
	// 关闭幂等保证 2001 不会收到第二帧。
	if n := h.DrainAll("服务端停机"); n != 3 {
		t.Fatalf("DrainAll 通知数 = %d, want 3", n)
	}

	got := map[string][]int{
		"replaced": idleSinkOf(t, replaced).closeCodes(),
		"dup":      idleSinkOf(t, dup).closeCodes(),
		"stopped":  idleSinkOf(t, stopped).closeCodes(),
		"live":     idleSinkOf(t, live).closeCodes(),
	}
	for name, codes := range got {
		for _, code := range codes {
			if !isRegisteredCloseCode(code) {
				t.Fatalf("%s 实际收到的关闭码 %d 未在协议登记集 %v", name, code, agentproto.AllCloseCodes())
			}
		}
	}
	if want := []int{agentproto.CloseDuplicateInstance}; !equalInts(got["replaced"], want) {
		t.Fatalf("被顶替连接收到的码 = %v, want %v", got["replaced"], want)
	}
	if want := []int{agentproto.CloseUnauthorized}; !equalInts(got["stopped"], want) {
		t.Fatalf("被停用连接收到的码 = %v, want %v（CloseDevice 之后 DrainAll 不得重复下发）", got["stopped"], want)
	}
	if want := []int{agentproto.CloseServerShutdown}; !equalInts(got["live"], want) {
		t.Fatalf("在线连接在 DrainAll 后收到的码 = %v, want %v", got["live"], want)
	}
	if want := []int{agentproto.CloseServerShutdown}; !equalInts(got["dup"], want) {
		t.Fatalf("顶替后的新连接在 DrainAll 后收到的码 = %v, want %v", got["dup"], want)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestHubDeviceIDsAreSorted(t *testing.T) {
	h := newTestHub(t)
	for _, id := range []uint64{1003, 1001, 1002} {
		_ = h.Register(newIdleConn(h, id))
	}
	ids := h.DeviceIDs()
	for i := 1; i < len(ids); i++ {
		if ids[i-1] > ids[i] {
			t.Fatalf("DeviceIDs 必须升序（供 flush 稳定遍历）: %v", ids)
		}
	}
	// 升序之外还得**完整**：漏设备会让 flush 静默跳过它的桶。
	if want := []uint64{1001, 1002, 1003}; !equalUints(ids, want) {
		t.Fatalf("DeviceIDs = %v, want %v", ids, want)
	}
	if h.OnlineCount() != len(ids) {
		t.Fatalf("DeviceIDs 长度 %d 与在线数 %d 不一致", len(ids), h.OnlineCount())
	}
}

func equalUints(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestOptionsDefaults(t *testing.T) {
	o := Options{}.withDefaults()
	if o.WriteTimeout <= 0 || o.PongWait <= 0 || o.PingInterval <= 0 || o.MaxMessageBytes <= 0 || o.SendQueue <= 0 {
		t.Fatalf("零值 Options 必须被填成安全默认: %+v", o)
	}
	if o.PingInterval >= o.PongWait {
		t.Fatalf("PingInterval(%v) 必须明显小于 PongWait(%v)，否则会被自己的心跳判超时", o.PingInterval, o.PongWait)
	}
	if o.PingInterval*2 > o.PongWait {
		t.Fatalf("PingInterval(%v) 必须至多为 PongWait(%v) 的一半才算「明显小于」", o.PingInterval, o.PongWait)
	}
	// 帧上限的零值必须取**协议常量**，不得自造一个数（自造值会与 agent 侧不一致）。
	if o.MaxMessageBytes != agentproto.MaxMessageBytes {
		t.Fatalf("MaxMessageBytes 默认值 = %d, want 协议常量 %d", o.MaxMessageBytes, agentproto.MaxMessageBytes)
	}
	// 只给了 PongWait（wireup 按配置推导的情形）时，PingInterval 必须跟着它缩小。
	partial := Options{PongWait: 30 * time.Second}.withDefaults()
	if partial.PingInterval <= 0 || partial.PingInterval >= partial.PongWait {
		t.Fatalf("PongWait=%v 时 PingInterval = %v，必须为正且明显小于 PongWait", partial.PongWait, partial.PingInterval)
	}
	// 显式给了的值不得被覆盖。
	explicit := Options{WriteTimeout: 3 * time.Second, SendQueue: 7}.withDefaults()
	if explicit.WriteTimeout != 3*time.Second || explicit.SendQueue != 7 {
		t.Fatalf("显式配置被 withDefaults 覆盖: %+v", explicit)
	}
}
