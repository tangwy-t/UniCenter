package agenthub

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
)

// ── 内存 socket 替身 ────────────────────────────────────────
//
// 为什么需要一个 socket 替身：本任务要断言的恰恰是**读循环里的分派与关闭码**，
// 而 gorilla 不导出可注入的 *websocket.Conn 构造函数（只有 Upgrade 能拿到真连接）。
// 于是把「读一帧 / 写一帧 / 控制帧 / 读期限 / 读上限」收敛到 conn.go 的 socket
// 窄接口上：生产态绑真实 *websocket.Conn，测试态绑本文件的内存替身，
// 于是本任务的全部守卫都能在**不起真实 TCP** 的前提下被驱动（真实 TCP 的
// 升级与握手由 Task 3 的 handler 测试覆盖）。
type fakeSocket struct {
	mu          sync.Mutex
	in          chan []byte
	deadline    time.Time
	readLimit   int64
	pongHandler func(string) error
	remote      net.Addr
	closed      bool
	dataFrames  [][]byte
	closes      []ctrlFrame
	pings       int
}

// ctrlFrame 是一次控制帧下发（ping / close）的记录。
type ctrlFrame struct {
	kind    int
	payload []byte
}

func newFakeSocket() *fakeSocket {
	return &fakeSocket{
		in: make(chan []byte, 16),
		// 远端地址**故意带端口**：未鉴权限流键必须只含 host 部分，而这条规则唯一的
		// 失效方式就是端口被带进键（每连接一个新键 ⇒ 限流形同虚设）—— 夹具要能暴露它，
		// 所以这里给的就是真实的 host:port 形态（与 net.TCPAddr.String() 同型）。
		remote: &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 54321},
	}
}

// ReadMessage 是入站通路：有帧就返回帧，读期限到了就返回超时错误。
//
// 超时错误特意用 os.ErrDeadlineExceeded —— 它实现了 net.Error 且 Timeout()==true，
// 与 gorilla 在真实连接上超时返回的 net 错误同型；返回一个自造的哨兵就测不出
// 「读循环是否真的按 net.Error 判别超时」。
//
// 期限每 tick 复查一次，而不是在进入时就定一个一次性 timer：真实 net.Conn 的
// 读期限是**可随时更新**的（SetReadDeadline 会打断正在阻塞的读；gorilla 的
// SetReadDeadline 就是 `return c.conn.SetReadDeadline(t)`），一次性 timer
// 假装不出这一点，于是「关闭时把读期限压到当下以解开读循环」这条路径无法被驱动。
func (f *fakeSocket) ReadMessage() (int, []byte, error) {
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()

	for {
		f.mu.Lock()
		dl := f.deadline
		f.mu.Unlock()
		if !dl.IsZero() && !time.Now().Before(dl) {
			return 0, nil, os.ErrDeadlineExceeded
		}
		select {
		case b := <-f.in:
			return websocket.TextMessage, b, nil
		case <-tick.C:
		}
	}
}

func (f *fakeSocket) WriteMessage(kind int, b []byte) error {
	cp := append([]byte(nil), b...)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dataFrames = append(f.dataFrames, cp)
	return nil
}

func (f *fakeSocket) WriteControl(kind int, b []byte, _ time.Time) error {
	cp := append([]byte(nil), b...)
	f.mu.Lock()
	defer f.mu.Unlock()
	if kind == websocket.PingMessage {
		f.pings++
		return nil
	}
	f.closes = append(f.closes, ctrlFrame{kind: kind, payload: cp})
	return nil
}

func (f *fakeSocket) SetReadLimit(limit int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readLimit = limit
}

func (f *fakeSocket) SetReadDeadline(t time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deadline = t
	return nil
}

func (f *fakeSocket) SetPongHandler(h func(string) error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pongHandler = h
}

// RemoteAddr 是 socket 窄接口的远端地址面：**未鉴权阶段的限流键身份只有它**
// （见 ratelimit.go 的 FrameLimitKey）。测试态由 newFakeSocket 给一个带端口的
// TCP 地址，于是「键里不得出现端口」这条规则可以在不起真实 TCP 的前提下被驱动。
func (f *fakeSocket) RemoteAddr() net.Addr {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.remote
}

// setRemoteAddr 换一个远端地址（用于驱动「IP 变了 → 键也变」）。
func (f *fakeSocket) setRemoteAddr(a net.Addr) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.remote = a
}

func (f *fakeSocket) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeSocket) push(b []byte) { f.in <- b }

func (f *fakeSocket) frames() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]byte, len(f.dataFrames))
	copy(out, f.dataFrames)
	return out
}

func (f *fakeSocket) closeFrames() []ctrlFrame {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]ctrlFrame, len(f.closes))
	copy(out, f.closes)
	return out
}

func (f *fakeSocket) pingCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pings
}

func (f *fakeSocket) readLimitOf() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.readLimit
}

func (f *fakeSocket) deadlineOf() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deadline
}

func (f *fakeSocket) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// pong 模拟对端回一个 pong：调用读循环注册的 handler（未注册即夹具失效）。
func (f *fakeSocket) pong(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	h := f.pongHandler
	f.mu.Unlock()
	if h == nil {
		t.Fatal("读循环未注册 pong handler —— ping/pong keepalive 无从生效")
	}
	if err := h(""); err != nil {
		t.Fatalf("pong handler 返回错误: %v", err)
	}
}

// closeCode 解析关闭帧里的码，并**顺带做登记集守卫**：真实下发的每个码都必须在
// agentproto.AllCloseCodes() 内（客户端收到不认识的码只能当异常处理）。
func (fr ctrlFrame) closeCode(t *testing.T) int {
	t.Helper()
	if fr.kind != websocket.CloseMessage {
		t.Fatalf("不是关闭帧: kind=%d", fr.kind)
	}
	if len(fr.payload) < 2 {
		t.Fatalf("关闭帧载荷过短: %v", fr.payload)
	}
	code := int(binary.BigEndian.Uint16(fr.payload[:2]))
	if !isRegisteredCloseCode(code) {
		t.Fatalf("关闭码 %d 未在协议登记集 %v", code, agentproto.AllCloseCodes())
	}
	return code
}

// closeReason 解析关闭帧里的话术（应为 agentproto.CloseReason(code)）。
func (fr ctrlFrame) closeReason() string {
	if len(fr.payload) <= 2 {
		return ""
	}
	return string(fr.payload[2:])
}

// ── Deps 账本替身 ───────────────────────────────────────────
//
// 五个窄接口由同一个结构体实现（Deps 的字段各自指向它），每个调用都记账。
// 记账全部走 mutex：读循环跑在 Serve 的协程里，测试在另一个协程读账本。
type stubDeps struct {
	mu sync.Mutex

	enrollID    uint64
	enrollToken string
	enrollErr   error
	enrollCalls int
	// enrollIPs 记录每次 Enroll 收到的 remoteIP（按调用顺序）：
	// 用于断言「服务端观测到的来源 IP 真的被带到了 enroll 调用点」。
	enrollIPs []string

	authID    uint64
	authErr   error
	authCalls int

	ingested  []*agentproto.MetricsSample
	ingestErr error

	touched  []uint64
	touchErr error

	accepting   bool
	decideErr   error
	decideCalls int

	reportInterval    time.Duration
	heartbeatInterval time.Duration
}

func newStubDeps() *stubDeps {
	return &stubDeps{
		accepting:         true,
		reportInterval:    10 * time.Second,
		heartbeatInterval: 30 * time.Second,
	}
}

func (s *stubDeps) Enroll(_ context.Context, _ *agentproto.Hello, remoteIP string) (uint64, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enrollCalls++
	s.enrollIPs = append(s.enrollIPs, remoteIP)
	if s.enrollErr != nil {
		return 0, "", s.enrollErr
	}
	return s.enrollID, s.enrollToken, nil
}

func (s *stubDeps) Authenticate(_ context.Context, _ *agentproto.Hello) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authCalls++
	if s.authErr != nil {
		return 0, s.authErr
	}
	return s.authID, nil
}

func (s *stubDeps) Ingest(_ context.Context, _ uint64, sample *agentproto.MetricsSample) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ingestErr != nil {
		return s.ingestErr
	}
	s.ingested = append(s.ingested, sample)
	return nil
}

func (s *stubDeps) Touch(_ context.Context, deviceID uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.touched = append(s.touched, deviceID)
	return s.touchErr
}

func (s *stubDeps) IsAccepting(_ context.Context, _ uint64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.decideCalls++
	if s.decideErr != nil {
		return false, s.decideErr
	}
	return s.accepting, nil
}

func (s *stubDeps) ReportInterval() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reportInterval
}

func (s *stubDeps) HeartbeatInterval() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.heartbeatInterval
}

func (s *stubDeps) setEnroll(id uint64, token string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enrollID, s.enrollToken, s.enrollErr = id, token, err
}

func (s *stubDeps) setAuth(id uint64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authID, s.authErr = id, err
}

func (s *stubDeps) setAccepting(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accepting = v
}

func (s *stubDeps) setPolicy(report, heartbeat time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reportInterval, s.heartbeatInterval = report, heartbeat
}

func (s *stubDeps) enrollCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enrollCalls
}

func (s *stubDeps) authCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authCalls
}

func (s *stubDeps) decideCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.decideCalls
}

func (s *stubDeps) ingestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.ingested)
}

func (s *stubDeps) ingestedSamples() []*agentproto.MetricsSample {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*agentproto.MetricsSample, len(s.ingested))
	copy(out, s.ingested)
	return out
}

func (s *stubDeps) touchedIDs() []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]uint64, len(s.touched))
	copy(out, s.touched)
	return out
}

// ─ 限流器替身 ──────────────────────────────────────────────
//
// stubLimiter 是 agenthub.RateLimiter 的账本替身：按调用次序放行前 allowN 次，
// 之后一律拒绝；每次调用都记下 (deviceID, remoteIP) ——「键身份随阶段变化」
// （hello 前按 IP、hello 后按设备）是本任务的核心契约，必须逐次可断言。
type stubLimiter struct {
	mu     sync.Mutex
	allowN int   // 放行前 N 次调用（0 = 全部拒绝）
	err    error // 非 nil → 每次调用都返回错误（驱动 fail-open 的报错路径）
	calls  []limiterCall
}

// limiterCall 是一次 Allow 调用的入参快照。
type limiterCall struct {
	deviceID uint64
	remoteIP string
}

func (s *stubLimiter) Allow(_ context.Context, deviceID uint64, remoteIP string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, limiterCall{deviceID: deviceID, remoteIP: remoteIP})
	if s.err != nil {
		// 报错时 bool 无意义（消费方必须走 fail-open 分支，不得看它）——刻意返回 false，
		// 这样「实现把 err 当成拒绝」就会被断言抓住。
		return false, s.err
	}
	return len(s.calls) <= s.allowN, nil
}

func (s *stubLimiter) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *stubLimiter) callsSnapshot() []limiterCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]limiterCall, len(s.calls))
	copy(out, s.calls)
	return out
}

func (s *stubLimiter) setError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

// ── 夹具 ───────────────────────────────────────────────────

type fixture struct {
	t    *testing.T
	hub  *Hub
	sock *fakeSocket
	conn *Conn
	stub *stubDeps
}

func newFixture(t *testing.T, opts Options) *fixture {
	t.Helper()
	stub := newStubDeps()
	return newFixtureWithDeps(t, opts, Deps{
		Enroller:      stub,
		Authenticator: stub,
		Ingestor:      stub,
		Toucher:       stub,
		Decider:       stub,
		Policy:        stub,
	}, stub)
}

func newFixtureWithDeps(t *testing.T, opts Options, deps Deps, stub *stubDeps) *fixture {
	t.Helper()
	hub := NewHub(opts, logger.NewNop())
	sock := newFakeSocket()
	// 测试态显式给一个来源 IP：生产路径由 handler 的 ClientIP 提供，
	// 这里钉住「它会被原样带到 enroll」。
	conn := newConn(hub, sock, deps, logger.NewNop(), testObservedIP)
	f := &fixture{t: t, hub: hub, sock: sock, conn: conn, stub: stub}
	t.Cleanup(func() {
		// 收尾：把可能还卡在读上的 Serve 协程解开（关闭帧 + 读期限压到当下）。
		conn.CloseWith(agentproto.CloseServerShutdown, "测试收尾")
	})
	return f
}

// serve 启动读循环（与生产一致：跑在独立协程里）；返回「等 Serve 返回」的等待器。
func (f *fixture) serve() func(time.Duration) bool {
	f.t.Helper()
	served := make(chan struct{})
	go func() {
		defer close(served)
		f.conn.Serve(context.Background())
	}()
	return func(d time.Duration) bool {
		select {
		case <-served:
			return true
		case <-time.After(d):
			return false
		}
	}
}

func (f *fixture) push(raw []byte) { f.sock.push(raw) }

// waitClose 等待第一个关闭帧，并断言码与线上 reason（reason 必须是协议规范话术）。
func (f *fixture) waitClose(t *testing.T, want int) ctrlFrame {
	t.Helper()
	waitFor(t, fmt.Sprintf("关闭帧 %d", want), func() bool { return len(f.sock.closeFrames()) > 0 })
	fr := f.sock.closeFrames()[0]
	if got := fr.closeCode(t); got != want {
		t.Fatalf("关闭码 = %d, want %d", got, want)
	}
	if got, wantReason := fr.closeReason(), agentproto.CloseReason(want); got != wantReason {
		t.Fatalf("线上 reason = %q, want %q", got, wantReason)
	}
	return fr
}

// expectNoClose 断言在 d 内**没有**关闭帧 —— 用于「拒绝该条但不断连」的语义。
func (f *fixture) expectNoClose(t *testing.T, d time.Duration) {
	t.Helper()
	time.Sleep(d)
	if frames := f.sock.closeFrames(); len(frames) > 0 {
		t.Fatalf("不应下发关闭帧，实际收到 %d 帧（码 %d）", len(frames), frames[0].closeCode(t))
	}
}

// waitData 等待第一个出站数据帧并解码为信封（解码即校验，顺带钉住「出站也是合法信封」）。
func (f *fixture) waitData(t *testing.T) *agentproto.Message {
	t.Helper()
	waitFor(t, "出站数据帧", func() bool { return len(f.sock.frames()) > 0 })
	raw := f.sock.frames()[0]
	m, err := agentproto.Decode(raw)
	if err != nil {
		t.Fatalf("出站帧不是合法信封: %v", err)
	}
	return m
}

func (f *fixture) waitState(t *testing.T, want ConnState) {
	t.Helper()
	waitFor(t, "状态 "+want.String(), func() bool { return f.conn.State() == want })
}

func (f *fixture) expectState(t *testing.T, want ConnState) {
	t.Helper()
	if got := f.conn.State(); got != want {
		t.Fatalf("状态 = %v, want %v", got, want)
	}
}

const waitTimeout = 2 * time.Second

// testObservedIP 是测试态注入的来源 IP。生产路径由 handler 的 c.ClientIP()
// 提供（代理感知），测试态用固定值即可 —— 目的是断言它被原样透传到 enroll。
const testObservedIP = "203.0.113.7"

// waitFor 轮询等待条件成立 —— 断言异步行为一律用它，不用固定 sleep 断言瞬时值。
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s：%v 内未发生", what, waitTimeout)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// ── 帧构造 ──────────────────────────────────────────────────
//
// 为什么不走 agentproto.NewMessage/Marshal：那两处会**自校验**（版本必须受支持、
// ts 必须为正），而本文件必须能构造非法信封（版本越界、时钟偏移），
// 否则对应的守卫永远不可达。
type wireFrame struct {
	V    int             `json:"v"`
	ID   string          `json:"id"`
	Type string          `json:"type"`
	TS   int64           `json:"ts"`
	Data json.RawMessage `json:"data"`
}

func rawFrame(t *testing.T, v int, typ string, ts int64, data any) []byte {
	t.Helper()
	payload, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("编码载荷: %v", err)
	}
	b, err := json.Marshal(wireFrame{V: v, ID: "1", Type: typ, TS: ts, Data: payload})
	if err != nil {
		t.Fatalf("编码信封: %v", err)
	}
	return b
}

// testHello 构造一份必填字段齐全的 hello（凭据由调用方补）。
func testHello() *agentproto.Hello {
	return &agentproto.Hello{
		InstanceID:   "i-1001",
		Hostname:     "host-1001",
		OS:           "linux",
		Arch:         "amd64",
		AgentVersion: "1.0.0",
	}
}

func helloFrame(t *testing.T, h *agentproto.Hello) []byte {
	t.Helper()
	return rawFrame(t, agentproto.CurrentVersion, agentproto.TypeAgentHello, time.Now().UnixMilli(), h)
}

func heartbeatFrame(t *testing.T) []byte {
	t.Helper()
	return rawFrame(t, agentproto.CurrentVersion, agentproto.TypeAgentHeartbeat, time.Now().UnixMilli(), &agentproto.Heartbeat{})
}

// metricsFrame 造一条合法样本；envelopeTS 可单独指定（时钟偏移用），payload 的 t 用 sampleTS。
func metricsFrame(t *testing.T, envelopeTS, sampleTS int64, mutate func(*agentproto.MetricsSample)) []byte {
	t.Helper()
	s := &agentproto.MetricsSample{
		T:              sampleTS,
		CPUUsedPercent: 12.5,
		Load1:          0.5,
		Load5:          0.4,
		Load15:         0.3,
		MemUsedPercent: 32.0,
		MemUsedMB:      1024,
		MemAvailableMB: 2048,
		TCPTotal:       10,
		TCPEstablished: 4,
		TCPListen:      3,
		UDPTotal:       2,
		ProcCount:      120,
		UptimeSec:      3600,
	}
	if mutate != nil {
		mutate(s)
	}
	return rawFrame(t, agentproto.CurrentVersion, agentproto.TypeAgentReportMetrics, envelopeTS, s)
}

// mustMessage 造一条出站信封（背压用例用）。
func mustMessage(t *testing.T) *agentproto.Message {
	t.Helper()
	m, err := agentproto.NewMessage("1", agentproto.TypeCoreHelloAck, &agentproto.HelloAck{Accepted: true, DeviceID: "1"})
	if err != nil {
		t.Fatalf("构造出站消息: %v", err)
	}
	return m
}

// enroll 走一遍 enroll 分支并等到 StateActive，返回夹具（多个用例的开场动作）。
func enroll(t *testing.T, f *fixture) {
	t.Helper()
	f.stub.setEnroll(1001, "agent-token-1", nil)
	f.serve()
	h := testHello()
	h.EnrollToken = "enroll-token"
	f.push(helloFrame(t, h))
	f.waitState(t, StateActive)
}

// ── 测试 1：newIdleConn / newBareConn 的短路语义 ─────────────

// TestConnIdleConnShortCircuitsWithoutSocket 钉住计划 Step 1 的第 1 条：
// newIdleConn/newBareConn（Task 1 的 hub 测试助手）构造的 *Conn 未接管真实 socket，
// Serve 与 SendMessage 都必须短路 —— 不写、不排队、不 panic。
func TestConnIdleConnShortCircuitsWithoutSocket(t *testing.T) {
	h := newTestHub(t)
	msg := mustMessage(t)

	idle := newIdleConn(h, 1001)
	idle.Serve(context.Background()) // 未接管 socket：立即返回，不得 panic
	if err := idle.SendMessage(msg); err != nil {
		t.Fatalf("nil socket 的 SendMessage 必须短路: %v", err)
	}
	if got := idle.DroppedCount(); got != 0 {
		t.Fatalf("短路的发送不得计入丢弃（它根本没进队列）: %d", got)
	}

	bare := newBareConn(h, 1002) // 连出站函数都是 nil 的最极端形态
	bare.Serve(context.Background())
	if err := bare.SendMessage(msg); err != nil {
		t.Fatalf("出站函数为 nil 时 SendMessage 必须短路: %v", err)
	}
	if err := bare.SendMessage(nil); err == nil {
		t.Fatal("nil 消息必须返回错误，不得静默成功")
	}
	if got := bare.DroppedCount(); got != 0 {
		t.Fatalf("短路路径不得计数: %d", got)
	}
}

// ── 测试 2：首帧的四类拒绝 ──────────────────────────────────

// TestConnFirstFrameGuards 是**方向校验与首帧规则**的主守卫：四种情形必须对应
// **四个不同的**码 —— 复用同一个码会把差异抹平，agent 也就无从知道该修 JSON、
// 该升级客户端，还是该先发 hello。
func TestConnFirstFrameGuards(t *testing.T) {
	now := time.Now().UnixMilli()
	cases := []struct {
		name string
		raw  []byte
		code int
	}{
		{
			"非法 JSON → 4002",
			[]byte("{not-json"),
			agentproto.CloseMalformedMessage,
		},
		{
			"未登记类型 → 4003",
			rawFrame(t, agentproto.CurrentVersion, "agent.unknown", now, map[string]any{}),
			agentproto.CloseUnsupportedType,
		},
		{
			"方向错误（core.hello_ack 出现在 agent 通道上）→ 4003",
			rawFrame(t, agentproto.CurrentVersion, agentproto.TypeCoreHelloAck, now,
				&agentproto.HelloAck{Accepted: true, DeviceID: "1"}),
			agentproto.CloseUnsupportedType,
		},
		{
			"hello 之前的 heartbeat → 4008",
			rawFrame(t, agentproto.CurrentVersion, agentproto.TypeAgentHeartbeat, now, &agentproto.Heartbeat{}),
			agentproto.CloseProtocolViolation,
		},
	}

	// 每个情形都各自断言**精确的**码（上面 f.waitClose(tc.code)），并钉住
	// 「不许把所有情形都塞进同一个码」：那样 agent 就无从区分「修报文 / 升级协议 /
	// 先握手」三种完全不同的处置。
	//
	// 注意：这里**不**要求四类互不相同 —— 按协议（closecode.go 对 CloseUnsupportedType
	// 的定义：「收到 core.*（方向错误）或回环消息」）与计划的关闭码表，
	// 「未登记类型」与「方向错误」本来就共用 4003；强行拆成两个码会脱离登记集。
	seen := map[int]string{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, Options{MaxMessageBytes: 1 << 20, SendQueue: 8})
			f.serve()
			f.push(tc.raw)
			f.waitClose(t, tc.code)
			f.expectState(t, StateClosed)

			seen[tc.code] = tc.name

			// 首帧被拒的连接绝不能进入鉴权/入湖路径。
			if n := f.stub.authCallCount(); n != 0 {
				t.Fatalf("首帧非法仍调用了 Authenticate %d 次", n)
			}
			if n := f.stub.enrollCallCount(); n != 0 {
				t.Fatalf("首帧非法仍调用了 Enroll %d 次", n)
			}
		})
	}
	if len(seen) < 3 {
		t.Fatalf("四类首帧非法情形只用了 %d 个不同的码（%v）—— 客户端无法区分处置策略",
			len(seen), seen)
	}
}

// TestConnVersionMismatchClosesVersionMismatch 钉住「版本不支持 → 4000」：
// 版本校验发生在信封解码层（Message.Validate 用 IsSupported 拦下），
// 读循环必须把 ErrUnsupportedVersion 从其它解码错误里区分出来，
// 否则 agent 收到的是「报文损坏」（4002），会一直重试同一个旧版本客户端。
func TestConnVersionMismatchClosesVersionMismatch(t *testing.T) {
	bad := agentproto.CurrentVersion + 1
	if slices.Contains(agentproto.SupportedVersions(), bad) {
		t.Fatalf("夹具失效：版本 %d 在受支持区间 %v 内", bad, agentproto.SupportedVersions())
	}
	f := newFixture(t, Options{SendQueue: 8})
	f.serve()
	h := testHello()
	h.EnrollToken = "enroll-token"
	f.push(rawFrame(t, bad, agentproto.TypeAgentHello, time.Now().UnixMilli(), h))
	f.waitClose(t, agentproto.CloseVersionMismatch)
	if n := f.stub.enrollCallCount(); n != 0 {
		t.Fatalf("版本不符的连接不得进入 enroll（调用 %d 次）", n)
	}
}

// ── 测试 3：hello 的两个分支 ────────────────────────────────

// TestConnHelloEnrollAckAndRegister 钉住 enroll 分支的全部后果：
// 回 core.hello_ack（含 device_id / agent_token / report_interval / v）、
// 状态转 StateActive、注册进 hub、刷新 last_seen_at。
func TestConnHelloEnrollAckAndRegister(t *testing.T) {
	f := newFixture(t, Options{SendQueue: 8})
	f.stub.setEnroll(1001, "agent-token-1", nil)
	f.serve()

	h := testHello()
	h.EnrollToken = "enroll-token"
	f.push(helloFrame(t, h))

	m := f.waitData(t)
	if m.Type != agentproto.TypeCoreHelloAck {
		t.Fatalf("应答类型 = %q, want %q", m.Type, agentproto.TypeCoreHelloAck)
	}
	if m.ID != "1" {
		t.Fatalf("应答信封 id = %q, want 回显请求 id %q（agent 靠它关联请求）", m.ID, "1")
	}
	ack := &agentproto.HelloAck{}
	if err := m.DecodeData(ack); err != nil {
		t.Fatalf("解码 hello_ack: %v", err)
	}
	if !ack.Accepted {
		t.Fatalf("accepted = false, want true")
	}
	if want := strconv.FormatUint(1001, 10); ack.DeviceID != want {
		t.Fatalf("device_id = %q, want %q（协议用十进制串，避免雪花 id 在 JS 侧丢精度）", ack.DeviceID, want)
	}
	if ack.AgentToken != "agent-token-1" {
		t.Fatalf("enroll 分支必须下发本次签发的 agent_token, got %q", ack.AgentToken)
	}
	if ack.ReportInterval != 10 {
		t.Fatalf("report_interval = %d, want 10（= Policy.ReportInterval 的秒数）", ack.ReportInterval)
	}
	if ack.V != agentproto.CurrentVersion {
		t.Fatalf("ack.v = %d, want %d", ack.V, agentproto.CurrentVersion)
	}
	// 下发的 ack 自身必须过契约校验（否则 agent 侧的 Validate 会拒绝它）。
	if err := ack.Validate(); err != nil {
		t.Fatalf("下发的 hello_ack 不合法: %v", err)
	}

	f.waitState(t, StateActive)
	if got := f.conn.DeviceID(); got != 1001 {
		t.Fatalf("DeviceID = %d, want 1001", got)
	}
	if got, ok := f.hub.Get(1001); !ok || got != f.conn {
		t.Fatal("hello 成功后必须把连接注册进 hub（否则 flush 看不到在线设备）")
	}
	waitFor(t, "hello 后的 Touch", func() bool { return len(f.stub.touchedIDs()) == 1 })
	if got := f.stub.touchedIDs()[0]; got != 1001 {
		t.Fatalf("Touch 的设备 = %d, want 1001", got)
	}
	f.expectNoClose(t, 60*time.Millisecond)
}

// TestConnHelloAuthenticateAckCarriesNoToken 钉住鉴权分支：不回 token
// （明文 token 只在 enroll 时下发一次；重连分支重发只会多一份明文落盘/落日志的机会）。
func TestConnHelloAuthenticateAckCarriesNoToken(t *testing.T) {
	f := newFixture(t, Options{SendQueue: 8})
	f.stub.setAuth(1002, nil)
	f.serve()

	h := testHello()
	h.AgentToken = "agent-token-existing"
	f.push(helloFrame(t, h))

	m := f.waitData(t)
	ack := &agentproto.HelloAck{}
	if err := m.DecodeData(ack); err != nil {
		t.Fatalf("解码 hello_ack: %v", err)
	}
	if ack.AgentToken != "" {
		t.Fatalf("鉴权分支不得下发 agent_token, got %q", ack.AgentToken)
	}
	if want := strconv.FormatUint(1002, 10); ack.DeviceID != want {
		t.Fatalf("device_id = %q, want %q", ack.DeviceID, want)
	}
	if ack.ReportInterval != 10 || ack.V != agentproto.CurrentVersion {
		t.Fatalf("report_interval/v = %d/%d, want 10/%d", ack.ReportInterval, ack.V, agentproto.CurrentVersion)
	}
	if err := ack.Validate(); err != nil {
		t.Fatalf("下发的 hello_ack 不合法: %v", err)
	}
	f.waitState(t, StateActive)
	if n := f.stub.enrollCallCount(); n != 0 {
		t.Fatalf("带 agent_token 的 hello 不得走 enroll（调用 %d 次）", n)
	}
	if n := f.stub.authCallCount(); n != 1 {
		t.Fatalf("Authenticate 调用 %d 次, want 1", n)
	}
	if got, ok := f.hub.Get(1002); !ok || got != f.conn {
		t.Fatal("鉴权成功后必须注册进 hub")
	}
}

// TestConnHelloCredentialGuards 钉住 hello 载荷的两层拒绝：
// 凭据缺失/歧义 → 4001（凭据问题），载荷缺必填字段 → 4002（报文问题）。
// 「两者都空」必须被归到凭据而不是报文：agent 收到 4001 才会去补/换 token，
// 收到 4002 只会去查 JSON 序列化。
func TestConnHelloCredentialGuards(t *testing.T) {
	t.Run("两种 token 都缺失 → 4001", func(t *testing.T) {
		f := newFixture(t, Options{SendQueue: 8})
		f.serve()
		f.push(helloFrame(t, testHello()))
		f.waitClose(t, agentproto.CloseUnauthorized)
		f.expectState(t, StateClosed)
		if n := f.stub.enrollCallCount() + f.stub.authCallCount(); n != 0 {
			t.Fatalf("凭据缺失不得进入鉴权分支（调用了 %d 次）", n)
		}
	})

	t.Run("两种 token 同时存在 → 4001", func(t *testing.T) {
		f := newFixture(t, Options{SendQueue: 8})
		f.serve()
		h := testHello()
		h.EnrollToken, h.AgentToken = "enroll", "agent"
		f.push(helloFrame(t, h))
		f.waitClose(t, agentproto.CloseUnauthorized)
		if n := f.stub.enrollCallCount() + f.stub.authCallCount(); n != 0 {
			t.Fatalf("凭据歧义时不得暗自取优先级（调用了 %d 次）", n)
		}
	})

	t.Run("载荷缺必填字段 → 4002", func(t *testing.T) {
		f := newFixture(t, Options{SendQueue: 8})
		f.serve()
		h := testHello()
		h.InstanceID = "" // 必填
		h.EnrollToken = "enroll"
		f.push(helloFrame(t, h))
		f.waitClose(t, agentproto.CloseMalformedMessage)
		if n := f.stub.enrollCallCount(); n != 0 {
			t.Fatalf("载荷非法不得进入 enroll（调用了 %d 次）", n)
		}
	})
}

// ── 测试 4/5：鉴权失败与停用设备 ────────────────────────────

// TestConnAuthStateFailuresUseUnauthorized 钉住「enroll 失败」「鉴权失败」
// 「设备被停用/删除」三种情形**共用 4001**：
// 用不同码会让旁观者（以及被停用的机器）从关闭码反推某设备是否存在。
func TestConnAuthStateFailuresUseUnauthorized(t *testing.T) {
	t.Run("enroll 失败", func(t *testing.T) {
		f := newFixture(t, Options{SendQueue: 8})
		f.stub.setEnroll(0, "", errors.New("enroll token 无效"))
		f.serve()
		h := testHello()
		h.EnrollToken = "bad"
		f.push(helloFrame(t, h))
		f.waitClose(t, agentproto.CloseUnauthorized)
		if f.conn.State() == StateActive {
			t.Fatal("鉴权失败绝不得进入 StateActive")
		}
		f.expectState(t, StateClosed)
		if f.hub.OnlineCount() != 0 {
			t.Fatalf("失败连接不得留在注册表里: %d", f.hub.OnlineCount())
		}
		if len(f.sock.frames()) != 0 {
			t.Fatal("鉴权失败时不得下发 hello_ack")
		}
	})

	t.Run("agent token 鉴权失败", func(t *testing.T) {
		f := newFixture(t, Options{SendQueue: 8})
		f.stub.setAuth(0, errors.New("agent token 无效"))
		f.serve()
		h := testHello()
		h.AgentToken = "bad"
		f.push(helloFrame(t, h))
		f.waitClose(t, agentproto.CloseUnauthorized)
		f.expectState(t, StateClosed)
		if f.hub.OnlineCount() != 0 {
			t.Fatalf("失败连接不得留在注册表里: %d", f.hub.OnlineCount())
		}
	})

	t.Run("设备被停用 → 同一个码且不入湖", func(t *testing.T) {
		f := newFixture(t, Options{SendQueue: 8})
		f.stub.setAuth(1003, nil)
		f.stub.setAccepting(false)
		f.serve()
		h := testHello()
		h.AgentToken = "agent-token-existing"
		f.push(helloFrame(t, h))
		f.waitClose(t, agentproto.CloseUnauthorized)
		f.expectState(t, StateClosed)
		if n := f.stub.ingestCount(); n != 0 {
			t.Fatalf("停用设备不得入湖: %d 条", n)
		}
		if f.hub.OnlineCount() != 0 {
			t.Fatal("停用设备不得注册进 hub")
		}
		if n := f.stub.decideCallCount(); n != 1 {
			t.Fatalf("IsAccepting 调用 %d 次, want 1（鉴权后必须查一次启停）", n)
		}
	})

	t.Run("启停查询故障也拒绝（不伪装成「设备正常」）", func(t *testing.T) {
		f := newFixture(t, Options{SendQueue: 8})
		f.stub.setAuth(1004, nil)
		f.stub.mu.Lock()
		f.stub.decideErr = errors.New("db down")
		f.stub.mu.Unlock()
		f.serve()
		h := testHello()
		h.AgentToken = "agent-token-existing"
		f.push(helloFrame(t, h))
		f.waitClose(t, agentproto.CloseUnauthorized)
		f.expectState(t, StateClosed)
	})
}

// ── 测试 7：重复 hello ──────────────────────────────────────

// TestConnRepeatedHelloClosesProtocolViolation 钉住状态机只能前进：
// 已 StateActive 再发 hello → 4008（而不是重新 enroll —— 那会让一条连接
// 在运行中把设备换成另一台，注册表与指标归属一起错乱）。
func TestConnRepeatedHelloClosesProtocolViolation(t *testing.T) {
	f := newFixture(t, Options{SendQueue: 8})
	enroll(t, f)

	h := testHello()
	h.EnrollToken = "enroll-token"
	f.push(helloFrame(t, h))
	f.waitClose(t, agentproto.CloseProtocolViolation)
	f.expectState(t, StateClosed)
	if n := f.stub.enrollCallCount(); n != 1 {
		t.Fatalf("重复 hello 不得再走一次 enroll（调用 %d 次）", n)
	}
	if got := len(f.sock.frames()); got != 1 {
		t.Fatalf("重复 hello 不得再回一次 hello_ack（出站数据帧 %d 条）", got)
	}
}

// ── 测试 8：读超时与活跃刷新 ────────────────────────────────

// TestConnHeartbeatTimeoutCloses4005 钉住读超时：PongWait 内没有任何入站消息
// （对端静默，ping 也不回 pong）→ 4005。这条守卫靠**内存 socket 真的按读期限
// 返回超时错误**来触发，而不是喂一个自造错误 —— 否则测不出读循环是否真设了期限。
func TestConnHeartbeatTimeoutCloses4005(t *testing.T) {
	f := newFixture(t, Options{PongWait: 40 * time.Millisecond, PingInterval: 15 * time.Millisecond, SendQueue: 8})
	f.serve()
	f.waitClose(t, agentproto.CloseHeartbeatTimeout)
	f.expectState(t, StateClosed)
}

// TestConnReadLimitDeadlineAndPong 钉住读循环里三个「保活与自保」的接线：
// 单帧上限（无上限时一条巨帧就能打爆服务端内存）、任何入站消息都刷新读期限、
// pong 同样刷新读期限（否则 ping/pong keepalive 形同虚设）。
func TestConnReadLimitDeadlineAndPong(t *testing.T) {
	const maxFrame = 4096
	f := newFixture(t, Options{MaxMessageBytes: maxFrame, PongWait: 2 * time.Second, PingInterval: time.Second, SendQueue: 8})
	f.serve()

	waitFor(t, "SetReadLimit 生效", func() bool { return f.sock.readLimitOf() == maxFrame })
	if got := f.sock.readLimitOf(); got != int64(maxFrame) {
		t.Fatalf("读上限 = %d, want %d", got, maxFrame)
	}

	// pong 必须把读期限往后推。
	before := f.sock.deadlineOf()
	time.Sleep(5 * time.Millisecond)
	f.sock.pong(t)
	if after := f.sock.deadlineOf(); !after.After(before) {
		t.Fatalf("pong 未刷新读期限: %v → %v", before, after)
	}

	// 任何入站消息（含 hello 之外的普通消息）同样算「活跃」。
	enroll(t, f)
	before = f.sock.deadlineOf()
	time.Sleep(5 * time.Millisecond)
	f.push(heartbeatFrame(t))
	waitFor(t, "心跳被处理（Touch 计数）", func() bool { return len(f.stub.touchedIDs()) == 2 })
	if after := f.sock.deadlineOf(); !after.After(before) {
		t.Fatalf("入站消息未刷新读期限: %v → %v", before, after)
	}
	f.expectNoClose(t, 60*time.Millisecond)
}

// ── 测试 9/10：心跳与上报 ───────────────────────────────────

// TestConnHeartbeatTouchesAndKeepsActive 钉住心跳的唯一语义：刷新 last_seen_at，
// 且状态不变（心跳不是状态迁移）。
func TestConnHeartbeatTouchesAndKeepsActive(t *testing.T) {
	f := newFixture(t, Options{SendQueue: 8})
	enroll(t, f)
	waitFor(t, "hello 后的 Touch", func() bool { return len(f.stub.touchedIDs()) == 1 })

	f.push(heartbeatFrame(t))
	waitFor(t, "心跳后的 Touch", func() bool { return len(f.stub.touchedIDs()) == 2 })
	for _, id := range f.stub.touchedIDs() {
		if id != 1001 {
			t.Fatalf("Touch 的设备 = %d, want 1001", id)
		}
	}
	f.expectState(t, StateActive)
	f.expectNoClose(t, 60*time.Millisecond)
}

// TestConnMetricsIngest 钉住上报路径：已鉴权 → 入湖；未鉴权 → 拒且不入湖；
// 语义非法（Validate 失败）→ 4002 且不入湖。
func TestConnMetricsIngest(t *testing.T) {
	t.Run("未鉴权上报 → 4008 且不入湖", func(t *testing.T) {
		f := newFixture(t, Options{SendQueue: 8})
		f.serve()
		now := time.Now().UnixMilli()
		f.push(metricsFrame(t, now, now, nil))
		f.waitClose(t, agentproto.CloseProtocolViolation)
		if n := f.stub.ingestCount(); n != 0 {
			t.Fatalf("未鉴权连接不得入湖: %d 条", n)
		}
	})

	t.Run("已鉴权上报 → 入湖且刷新 last_seen_at", func(t *testing.T) {
		f := newFixture(t, Options{SendQueue: 8})
		enroll(t, f)
		waitFor(t, "hello 后的 Touch", func() bool { return len(f.stub.touchedIDs()) == 1 })

		now := time.Now().UnixMilli()
		f.push(metricsFrame(t, now, now, nil))
		waitFor(t, "样本入湖", func() bool { return f.stub.ingestCount() == 1 })
		got := f.stub.ingestedSamples()[0]
		if got.T != now {
			t.Fatalf("入湖样本 t = %d, want %d（不得篡改 ts）", got.T, now)
		}
		if got.CPUUsedPercent != 12.5 {
			t.Fatalf("入湖样本 cpu = %v, want 12.5", got.CPUUsedPercent)
		}
		f.expectState(t, StateActive)
		waitFor(t, "上报后的 Touch", func() bool { return len(f.stub.touchedIDs()) == 2 })
		f.expectNoClose(t, 60*time.Millisecond)
	})

	t.Run("语义非法样本 → 4002 且不入湖", func(t *testing.T) {
		f := newFixture(t, Options{SendQueue: 8})
		enroll(t, f)
		now := time.Now().UnixMilli()
		f.push(metricsFrame(t, now, now, func(s *agentproto.MetricsSample) {
			s.CPUUsedPercent = -1 // 百分比必须落在 0-100
		}))
		f.waitClose(t, agentproto.CloseMalformedMessage)
		if n := f.stub.ingestCount(); n != 0 {
			t.Fatalf("非法样本不得入湖: %d 条", n)
		}
	})

	t.Run("入湖失败不关连接", func(t *testing.T) {
		f := newFixture(t, Options{SendQueue: 8})
		f.stub.mu.Lock()
		f.stub.ingestErr = errors.New("redis down")
		f.stub.mu.Unlock()
		enroll(t, f)
		now := time.Now().UnixMilli()
		f.push(metricsFrame(t, now, now, nil))
		waitFor(t, "上报后的 Touch", func() bool { return len(f.stub.touchedIDs()) == 2 })
		// 热层故障是服务端问题：关连接只会让整机指标在故障期间全丢。
		f.expectState(t, StateActive)
		f.expectNoClose(t, 60*time.Millisecond)
	})
}

// ── 测试 11：时钟偏移 ───────────────────────────────────────

// TestConnHelloWithClockSkewStillCompletesHandshake 钉住「hello **不**做时钟偏移软校验」。
//
// 缺陷本体（修复前）：软校验排在分派**之前**，且它的语义是「拒绝该条但不断连」。
// 首帧 hello 若时钟偏移超限，于是被拒绝 → 连接既没握手成功、也没关闭，
// **永久停在 StateAwaitHello**，直到 PongWait（默认 90s）才因读超时按
// CloseHeartbeatTimeout（4005）关闭 —— 一台时钟不准的设备因此永远连不上，
// 而症状是一个指向「网络/心跳」的关闭码。
//
// 断言（三件事都必须成立）：
//  1. hello_ack 真的发出去了（连接进 Active）—— agent 由此拿到 ServerTime 自行校准；
//  2. 连接**没有**停在 AwaitHello、也没有被关闭；
//  3. 偏移**不计入** SkewCount（它的语义是「被拒的消息数」，hello 没有被拒）。
func TestConnHelloWithClockSkewStillCompletesHandshake(t *testing.T) {
	f := newFixture(t, Options{SendQueue: 8})
	f.stub.setEnroll(1001, "agent-token-1", nil)
	f.serve()

	now := time.Now().UnixMilli()
	skewed := now - agentproto.MaxClockSkewMs - 3600_000 // 偏了 1 小时以上
	if !agentproto.ExceedsClockSkew(skewed, now) {
		t.Fatalf("夹具失效：ts=%d 未超出允许偏移 %d", skewed, agentproto.MaxClockSkewMs)
	}

	h := testHello()
	h.EnrollToken = "enroll-token"
	// 只有信封 ts 偏移，载荷本身完全合法（这正是「设备的钟不准」的真实形态）。
	f.push(rawFrame(t, agentproto.CurrentVersion, agentproto.TypeAgentHello, skewed, h))

	// 缺陷形态是「既没有 hello_ack、也没有关闭帧，连接挂在 StateAwaitHello 直到
	// PongWait 读超时」。先有界等待「二者之一发生」，再**断言状态**：这样反向验证
	// （恢复旧行为）得到的是一条直接指向缺陷的红灯，而不是「2s 内没有出站帧」
	// （那句话既可以是没有 ack，也可以是没有关闭帧，指向不明）。
	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		if len(f.sock.frames()) > 0 || len(f.sock.closeFrames()) > 0 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if got := f.conn.State(); got == StateAwaitHello {
		t.Fatal("连接停在 StateAwaitHello：偏移超限的首帧 hello 被拒绝但没关连接（缺陷本体：既没握手成功，也没关闭）")
	}

	msg := f.waitData(t)
	if msg.Type != agentproto.TypeCoreHelloAck {
		// 偏移超限的首帧必须照常握手；收到别的说明 hello 又被拒了。
		t.Fatalf("首帧 hello 偏移超限时出站消息 = %q, want %q（握手必须完成）",
			msg.Type, agentproto.TypeCoreHelloAck)
	}
	ack := &agentproto.HelloAck{}
	if err := msg.DecodeData(ack); err != nil {
		t.Fatalf("解码 hello_ack: %v", err)
	}
	if !ack.Accepted || ack.DeviceID != "1001" {
		t.Fatalf("hello_ack = %+v, want accepted/device_id=1001", ack)
	}
	// ServerTime 是 agent 自我校准的唯一来源，必须存在（否则「让 agent 自己校准」
	// 这条修复理由不成立）。
	if ack.ServerTime == 0 {
		t.Fatal("hello_ack.ServerTime = 0：agent 无从校准时钟")
	}

	f.waitState(t, StateActive)
	if got := f.conn.State(); got == StateAwaitHello {
		t.Fatal("连接停在 StateAwaitHello：偏移超限的首帧 hello 被拒绝但没关连接（缺陷本体）")
	}
	if got := f.conn.SkewCount(); got != 0 {
		t.Fatalf("SkewCount = %d, want 0（hello 没有被拒，不得计入「被拒消息数」）", got)
	}
	f.expectNoClose(t, 60*time.Millisecond)
}

// TestConnClockSkewRejectsMessageWithoutDisconnect 钉住软校验语义：
// 偏移超限的**那一条**被拒并计数，**不断连**（agent 的时钟不会因为我们关连接就变准，
// 断连只会让整机指标长期全丢）。
func TestConnClockSkewRejectsMessageWithoutDisconnect(t *testing.T) {
	f := newFixture(t, Options{SendQueue: 8})
	enroll(t, f)

	now := time.Now().UnixMilli()
	skewed := now - agentproto.MaxClockSkewMs - 1000
	if !agentproto.ExceedsClockSkew(skewed, now) {
		t.Fatalf("夹具失效：ts=%d 未超出允许偏移 %d", skewed, agentproto.MaxClockSkewMs)
	}

	f.push(metricsFrame(t, skewed, skewed, nil))
	waitFor(t, "偏移计数", func() bool { return f.conn.SkewCount() == 1 })
	if n := f.stub.ingestCount(); n != 0 {
		t.Fatalf("偏移超限的样本不得入湖: %d 条", n)
	}
	f.expectState(t, StateActive)
	f.expectNoClose(t, 60*time.Millisecond)

	// 紧跟一条正常的必须照常入湖：拒绝一条不得污染后续。
	f.push(metricsFrame(t, now, now, nil))
	waitFor(t, "正常样本入湖", func() bool { return f.stub.ingestCount() == 1 })
	if got := f.conn.SkewCount(); got != 1 {
		t.Fatalf("偏移计数 = %d, want 1", got)
	}
}

// ── 测试 12/13：背压与关闭幂等 ──────────────────────────────

// TestConnBackpressureDropsAndCounts 钉住背压策略：发送队列满时**丢弃并计数**，
// 绝不阻塞读循环（阻塞读循环会让 TCP 接收窗口关闭，进而把 agent 也拖死）。
// 用例刻意**不启动写协程**：队列因此真的会满，这是唯一能确定性触发丢弃的办法。
func TestConnBackpressureDropsAndCounts(t *testing.T) {
	const queue = 2
	f := newFixture(t, Options{SendQueue: queue})
	msg := mustMessage(t)

	// 填队列的动作放在协程里 + 带超时收集结果：一旦实现改成阻塞式入队，
	// 这里会以「阻塞了」失败，而不是把整个测试挂死到 go test 超时。
	results := make(chan error, queue+2)
	go func() {
		for i := 0; i < queue+2; i++ {
			results <- f.conn.SendMessage(msg)
		}
	}()

	for i := 0; i < queue+2; i++ {
		select {
		case err := <-results:
			if i < queue {
				if err != nil {
					t.Fatalf("第 %d 条（队列未满）不应失败: %v", i+1, err)
				}
				continue
			}
			if !errors.Is(err, errSendQueueFull) {
				t.Fatalf("第 %d 条（队列已满）错误 = %v, want errSendQueueFull", i+1, err)
			}
		case <-time.After(waitTimeout):
			t.Fatal("队列满时 SendMessage 阻塞了 —— 背压必须是「丢弃并计数」")
		}
	}
	if got := f.conn.DroppedCount(); got != 2 {
		t.Fatalf("丢弃计数 = %d, want 2", got)
	}
}

// TestConnBackpressureNeverDropsCloseOrPing 钉住背压的**例外**：
// 队列满时数据帧可以丢，但控制帧（ping / 关闭）必须照常送达 ——
// 丢心跳会让对端与我们都误判链路已死，丢关闭帧会让对端一直挂着等关闭。
func TestConnBackpressureNeverDropsCloseOrPing(t *testing.T) {
	f := newFixture(t, Options{SendQueue: 1})
	msg := mustMessage(t)

	if err := f.conn.SendMessage(msg); err != nil {
		t.Fatalf("填队列: %v", err)
	}
	if err := f.conn.SendMessage(msg); !errors.Is(err, errSendQueueFull) {
		t.Fatalf("夹具失效：队列未满（err=%v）", err)
	}
	dropped := f.conn.DroppedCount()

	// ① ping：走 WriteControl，绕开发送队列。
	f.conn.sendPing()
	if got := f.sock.pingCount(); got != 1 {
		t.Fatalf("队列满时 ping 被丢弃（ping=%d）—— 丢心跳会误判断线", got)
	}

	// ② 关闭帧：同样不走队列，且必须只下发一次。
	f.conn.CloseWith(agentproto.CloseUnauthorized, "测试")
	frames := f.sock.closeFrames()
	if len(frames) != 1 {
		t.Fatalf("关闭帧数 = %d, want 1", len(frames))
	}
	if got := frames[0].closeCode(t); got != agentproto.CloseUnauthorized {
		t.Fatalf("关闭码 = %d, want %d", got, agentproto.CloseUnauthorized)
	}
	if got := f.conn.DroppedCount(); got != dropped {
		t.Fatalf("控制帧不得计入丢弃计数: %d → %d", dropped, got)
	}
}

// TestConnCloseWithIsIdempotentOnLiveSocket 钉住 Task 1 的两条关闭语义在
// 「已接管真实 socket」的连接上同样成立：幂等（只下发一次、首个码生效）
// 且关闭后 Serve 必须返回（读循环要被解开，否则停机排空会挂住到读超时）。
func TestConnCloseWithIsIdempotentOnLiveSocket(t *testing.T) {
	f := newFixture(t, Options{PongWait: 5 * time.Second, PingInterval: time.Second, SendQueue: 8})
	served := f.serve()

	f.conn.CloseWith(agentproto.CloseServerShutdown, "停机")
	waitFor(t, "关闭帧", func() bool { return len(f.sock.closeFrames()) == 1 })
	f.expectState(t, StateClosed)

	f.conn.CloseWith(agentproto.CloseUnauthorized, "第二次关闭")
	time.Sleep(50 * time.Millisecond)
	frames := f.sock.closeFrames()
	if len(frames) != 1 {
		t.Fatalf("重复 CloseWith 不得再次下发关闭帧: %d 帧", len(frames))
	}
	if got := frames[0].closeCode(t); got != agentproto.CloseServerShutdown {
		t.Fatalf("生效的关闭码 = %d, want 首个码 %d", got, agentproto.CloseServerShutdown)
	}
	if !served(2 * time.Second) {
		t.Fatal("关闭后 Serve 必须返回（否则 handler 的注销与停机排空都会挂住）")
	}
	if !f.sock.isClosed() {
		t.Fatal("Serve 返回后底层 socket 必须被关闭（否则 TCP 连接泄漏）")
	}
}

// TestConnPingIntervalFollowsHeartbeatPolicy 钉住 ping 节奏的推导：
// 以 opts.PingInterval 为上限，且不慢于 agent 的心跳节奏（Policy.HeartbeatInterval）。
func TestConnPingIntervalFollowsHeartbeatPolicy(t *testing.T) {
	f := newFixture(t, Options{PingInterval: time.Hour, PongWait: 2 * time.Hour})
	f.stub.setPolicy(10*time.Second, 15*time.Millisecond)
	if got := f.conn.pingInterval(); got != 15*time.Millisecond {
		t.Fatalf("ping 间隔 = %v, want 15ms（不得慢于 agent 的心跳节奏）", got)
	}

	f.stub.setPolicy(10*time.Second, 3*time.Hour)
	if got := f.conn.pingInterval(); got != time.Hour {
		t.Fatalf("ping 间隔 = %v, want 1h（不得快于配置上限）", got)
	}

	// Policy 未装配时退回配置值，绝不 panic、也绝不退化成零间隔。
	noPolicy := newFixtureWithDeps(t, Options{PingInterval: time.Hour, PongWait: 2 * time.Hour}, Deps{}, nil)
	if got := noPolicy.conn.pingInterval(); got != time.Hour {
		t.Fatalf("Policy 未装配时 ping 间隔 = %v, want %v", got, time.Hour)
	}
}

// TestConnNilDepsFailClosedWithoutPanic 钉住「依赖未装配」的失败方向：
// 鉴权类依赖缺失 → 4001（失败关闭，绝不放行）；入湖依赖缺失 → 记日志丢样本、
// 不关连接（关连接只会让 agent 反复重连也照样丢）。任何一条都不得 panic。
func TestConnNilDepsFailClosedWithoutPanic(t *testing.T) {
	t.Run("Enroller 未装配 → 4001", func(t *testing.T) {
		f := newFixtureWithDeps(t, Options{SendQueue: 8}, Deps{}, nil)
		f.serve()
		h := testHello()
		h.EnrollToken = "enroll-token"
		f.push(helloFrame(t, h))
		f.waitClose(t, agentproto.CloseUnauthorized)
		f.expectState(t, StateClosed)
	})

	t.Run("Authenticator 未装配 → 4001", func(t *testing.T) {
		f := newFixtureWithDeps(t, Options{SendQueue: 8}, Deps{}, nil)
		f.serve()
		h := testHello()
		h.AgentToken = "agent-token"
		f.push(helloFrame(t, h))
		f.waitClose(t, agentproto.CloseUnauthorized)
	})

	t.Run("Decider 未装配 → 4001", func(t *testing.T) {
		stub := newStubDeps()
		stub.setEnroll(1001, "tok", nil)
		f := newFixtureWithDeps(t, Options{SendQueue: 8}, Deps{Enroller: stub, Policy: stub}, stub)
		f.serve()
		h := testHello()
		h.EnrollToken = "enroll-token"
		f.push(helloFrame(t, h))
		f.waitClose(t, agentproto.CloseUnauthorized)
		f.expectState(t, StateClosed)
	})

	t.Run("Ingestor/Toucher 未装配：不断连、不 panic", func(t *testing.T) {
		stub := newStubDeps()
		stub.setEnroll(1001, "tok", nil)
		f := newFixtureWithDeps(t, Options{SendQueue: 8}, Deps{Enroller: stub, Decider: stub, Policy: stub}, stub)
		f.serve()
		h := testHello()
		h.EnrollToken = "enroll-token"
		f.push(helloFrame(t, h))
		f.waitState(t, StateActive)

		now := time.Now().UnixMilli()
		f.push(metricsFrame(t, now, now, nil))
		f.push(heartbeatFrame(t))
		f.expectState(t, StateActive)
		f.expectNoClose(t, 80*time.Millisecond)
	})
}

// ─ 测试 14：入站帧级限流（CloseRateLimited 4006）────────────
//
// 缺口本体：协议登记了 CloseRateLimited(4006)「限流」，但仓库里**没有任何**限流
// 实现 —— 一条失控的 agent（bug 或被攻陷）可以无限量刷帧，把 core 的 CPU 与 Redis
// 带宽吃满。这组断言钉住四件事：
//  1. 超限 → 4006 关闭（而不是静默放行）；
//  2. 超限的那一帧**在任何写路径之前**就被拦下（Ingestor / Touch 调用次数不增）；
//  3. **未鉴权阶段同样受限**，且键只含 IP 的 host 部分（不带端口）；
//  4. 限流器不可用（nil 或报错）→ **fail-open**（放行 + 不关连接）。

// newRateLimitFixture 造一个「六个能力面指向同一个替身 + 限流器由入参给」的夹具。
// lim 传 nil 即「Limiter 未装配」的形态（fail-open 的另一条路径）。
func newRateLimitFixture(t *testing.T, lim RateLimiter) (*fixture, *stubDeps) {
	t.Helper()
	stub := newStubDeps()
	stub.setEnroll(1001, "agent-token-1", nil)
	deps := Deps{
		Enroller:      stub,
		Authenticator: stub,
		Ingestor:      stub,
		Toucher:       stub,
		Decider:       stub,
		Policy:        stub,
		Limiter:       lim,
	}
	return newFixtureWithDeps(t, Options{SendQueue: 8}, deps, stub), stub
}

// TestConnRateLimitClosesWith4006BeforeDispatch 钉住「超限关闭」与「分派前拦下」，
// 并逐次断言限流键的**身份**随阶段变化（hello 前按 IP，hello 后按设备）。
func TestConnRateLimitClosesWith4006BeforeDispatch(t *testing.T) {
	// 放行前 2 帧：hello（未鉴权身份）+ 第 1 条上报（设备身份）；第 3 帧拒绝。
	lim := &stubLimiter{allowN: 2}
	f, stub := newRateLimitFixture(t, lim)
	served := f.serve()

	h := testHello()
	h.EnrollToken = "enroll-token"
	f.push(helloFrame(t, h))
	f.waitState(t, StateActive)

	now := time.Now().UnixMilli()
	f.push(metricsFrame(t, now, now, nil))
	waitFor(t, "第 1 条上报入湖", func() bool { return stub.ingestCount() == 1 })
	waitFor(t, "上报后的 Touch", func() bool { return len(stub.touchedIDs()) == 2 })

	// 第 3 帧：超限。它必须**不**进入分派（否则照样入湖、照样刷 last_seen_at，
	// 限流就只是「关连接」而不是「停止消耗」）。
	f.push(metricsFrame(t, now, now, nil))
	f.waitClose(t, agentproto.CloseRateLimited)
	f.expectState(t, StateClosed)

	if got := stub.ingestCount(); got != 1 {
		t.Fatalf("超限帧仍被分派入湖（Ingestor 调用 %d 次, want 1）—— 限流必须生效在分派之前", got)
	}
	if got := len(stub.touchedIDs()); got != 2 {
		t.Fatalf("超限帧仍刷新了 last_seen_at（Touch %d 次, want 2）—— 限流必须生效在任何写路径之前", got)
	}
	if got := f.conn.RateLimitedCount(); got != 1 {
		t.Fatalf("RateLimitedCount = %d, want 1（超限必须记账，否则「限流真的触发过」无证据）", got)
	}
	if !served(2 * time.Second) {
		t.Fatal("超限后读循环必须终止（否则这条连接会继续读帧，只是不再分派）")
	}

	calls := lim.callsSnapshot()
	if len(calls) != 3 {
		t.Fatalf("Allow 调用 %d 次, want 3（每帧一次）", len(calls))
	}
	if calls[0].deviceID != 0 {
		t.Fatalf("hello 帧的身份 deviceID = %d, want 0（此刻尚未鉴权，键必须按 IP）", calls[0].deviceID)
	}
	for i, c := range calls[1:] {
		if c.deviceID != 1001 {
			t.Fatalf("鉴权后的第 %d 帧身份 deviceID = %d, want 1001（键必须按设备）", i+1, c.deviceID)
		}
	}
	if got, want := FrameLimitKey(calls[0].deviceID, calls[0].remoteIP), "agent:ws:preauth:10.0.0.1"; got != want {
		t.Fatalf("未鉴权帧的限流键 = %q, want %q（远端 IP 只取 host 部分）", got, want)
	}
	if got, want := FrameLimitKey(calls[1].deviceID, calls[1].remoteIP), "agent:device:1001:frames"; got != want {
		t.Fatalf("已鉴权帧的限流键 = %q, want %q（按设备计数，重连不重置额度）", got, want)
	}
}

// TestConnRateLimitSmokeFrames 钉住未鉴权阶段的两条边界：
//   - 首帧就被限流 → 4006（**不是** 4008）：未鉴权阶段同样受限，
//     否则攻击者只要不发 hello 就能把握手之前的 CPU 刷满；
//   - 解码都过不去的帧**不询问**限流器 → 4002：限流的生效时机是
//     「已解码并校验过的消息之后」，不是「收到字节之后」。
func TestConnRateLimitSmokeFrames(t *testing.T) {
	t.Run("未鉴权首帧超限 → 4006（键按远端 IP，不含端口）", func(t *testing.T) {
		lim := &stubLimiter{allowN: 0} // 一律拒绝
		f, stub := newRateLimitFixture(t, lim)
		f.serve()

		f.push(heartbeatFrame(t))
		f.waitClose(t, agentproto.CloseRateLimited)
		f.expectState(t, StateClosed)
		if got := f.conn.RateLimitedCount(); got != 1 {
			t.Fatalf("RateLimitedCount = %d, want 1", got)
		}
		calls := lim.callsSnapshot()
		if len(calls) != 1 {
			t.Fatalf("Allow 调用 %d 次, want 1", len(calls))
		}
		if calls[0].deviceID != 0 {
			t.Fatalf("未鉴权阶段必须以 deviceID=0 上报身份, got %d", calls[0].deviceID)
		}
		if calls[0].remoteIP != "10.0.0.1" {
			t.Fatalf("远端 IP = %q, want %q（**只取 host 部分**：带端口＝每条 TCP 连接一个键，限流形同虚设）",
				calls[0].remoteIP, "10.0.0.1")
		}
		// 首帧就被拦下 ⇒ 连 hello 都没进：鉴权与入湖路径一次都没碰。
		if n := stub.enrollCallCount() + stub.authCallCount(); n != 0 {
			t.Fatalf("被限流的首帧仍进入了鉴权路径（调用 %d 次）", n)
		}
		if n := stub.ingestCount(); n != 0 {
			t.Fatalf("被限流的帧仍入湖 %d 条", n)
		}
	})

	t.Run("解码失败的帧 → 4002 且不询问限流器", func(t *testing.T) {
		lim := &stubLimiter{allowN: 0}
		f, _ := newRateLimitFixture(t, lim)
		f.serve()

		f.push([]byte("{not-json"))
		f.waitClose(t, agentproto.CloseMalformedMessage)
		if got := lim.callCount(); got != 0 {
			t.Fatalf("解码失败的帧询问了限流器 %d 次 —— 生效时机必须是「已解码并校验过」之后", got)
		}
	})
}

// TestConnRateLimitFailsOpen 钉住 fail-open 的**两条**路径：限流器未装配（nil）
// 与限流器报错，都必须**放行 + 不关连接**。
//
// 取向的理由：限流器守的是 CPU / Redis 带宽这类容量资源，而它自己依赖 Redis ——
// 把它当权威会让「Redis 抖一下」放大成全体 agent 的关闭与重连风暴（重连要查库、
// 要重新 enroll），代价高于挺过这段抖动。这与 accept()（设备启停）的 fail-closed
// 是两个方向，理由也不同：那条守卫管的是安全。
func TestConnRateLimitFailsOpen(t *testing.T) {
	t.Run("Limiter 未装配（nil）→ 照常放行", func(t *testing.T) {
		f, stub := newRateLimitFixture(t, nil)
		f.serve()

		h := testHello()
		h.EnrollToken = "enroll-token"
		f.push(helloFrame(t, h))
		f.waitState(t, StateActive)

		now := time.Now().UnixMilli()
		f.push(metricsFrame(t, now, now, nil))
		waitFor(t, "样本入湖（限流器缺失不得拒绝服务）", func() bool { return stub.ingestCount() == 1 })

		f.expectState(t, StateActive)
		f.expectNoClose(t, 80*time.Millisecond)
		if got := f.conn.RateLimitedCount(); got != 0 {
			t.Fatalf("RateLimitedCount = %d, want 0（fail-open 路径不得记成「被限流」）", got)
		}
	})

	t.Run("Allow 返回错误 → 照常放行且不关连接", func(t *testing.T) {
		lim := &stubLimiter{allowN: 1 << 30} // 若实现改去看 bool（报错时恒 false），握手当场就会断
		lim.setError(errors.New("redis down"))
		f, stub := newRateLimitFixture(t, lim)
		f.serve()

		h := testHello()
		h.EnrollToken = "enroll-token"
		f.push(helloFrame(t, h))
		// hello 也是「一帧」：限流器报错时握手必须照常完成（否则 Redis 抖动 = 全通道不可用）。
		f.waitState(t, StateActive)

		now := time.Now().UnixMilli()
		f.push(metricsFrame(t, now, now, nil))
		waitFor(t, "样本入湖（限流器故障不得拒绝服务）", func() bool { return stub.ingestCount() == 1 })

		f.expectState(t, StateActive)
		f.expectNoClose(t, 80*time.Millisecond)
		if got := lim.callCount(); got < 2 {
			t.Fatalf("Allow 调用 %d 次, want ≥2（hello 与上报各一次）", got)
		}
		if got := f.conn.RateLimitedCount(); got != 0 {
			t.Fatalf("RateLimitedCount = %d, want 0（报错路径不得记成「被限流」）", got)
		}
	})
}

// bareAddr 是「String() 不含 host:port」的 net.Addr（unix socket 等同型），
// 用来驱动 remoteIPOf 的兜底分支。
type bareAddr string

func (a bareAddr) Network() string { return "bare" }
func (a bareAddr) String() string  { return string(a) }

// TestFrameLimitKeyRules 钉住键规则本身（纯函数层，与读循环无关）：
// 二选一形态定死，且**端口绝不进入键**。
func TestFrameLimitKeyRules(t *testing.T) {
	if got, want := FrameLimitKey(0, "192.0.2.7"), "agent:ws:preauth:192.0.2.7"; got != want {
		t.Fatalf("未鉴权键 = %q, want %q", got, want)
	}
	if got, want := FrameLimitKey(1001, "192.0.2.7"), "agent:device:1001:frames"; got != want {
		t.Fatalf("已鉴权键 = %q, want %q（IP 不参与：额度必须跟着设备走，重连不重置）", got, want)
	}
	// 同一 IP 的不同源端口必须落到**同一个**键上 —— 这正是「不带端口」的目的：
	// 带端口等于每条 TCP 连接一个计数器，攻击者开 N 条连接就拿到 N 倍额度。
	fromPort := func(p int) string {
		return FrameLimitKey(0, remoteIPOf(&net.TCPAddr{IP: net.ParseIP("192.0.2.7"), Port: p}))
	}
	if p1, p2 := fromPort(1111), fromPort(2222); p1 != p2 {
		t.Fatalf("同一 IP 的两个源端口算出不同键（%q vs %q）—— 端口进了键，限流形同虚设", p1, p2)
	}
	if got, want := fromPort(1111), "agent:ws:preauth:192.0.2.7"; got != want {
		t.Fatalf("带端口地址的键 = %q, want %q", got, want)
	}

	// remoteIPOf 的提取分支（含兜底）。
	for _, tc := range []struct {
		name string
		addr net.Addr
		want string
	}{
		{"IPv4 host:port", &net.TCPAddr{IP: net.ParseIP("192.0.2.7"), Port: 54321}, "192.0.2.7"},
		{"IPv6 host:port", &net.TCPAddr{IP: net.ParseIP("2001:db8::1"), Port: 443}, "2001:db8::1"},
		{"无端口可解析 → 原样返回", bareAddr("unix-socket"), "unix-socket"},
		{"addr 为 nil → 空串（所有这类连接共用一个键，方向与限流一致）", nil, ""},
	} {
		if got := remoteIPOf(tc.addr); got != tc.want {
			t.Fatalf("%s: remoteIPOf = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// ─ 守卫：关闭码登记集与分派完备性 ──────────────────────────

// TestConnUsesRegisteredCloseCodes 钉住状态机用到的每个码都在协议登记集内，
// 并反向钉住「AllCloseCodes 只含业务码」这一前提（1000/1001 不在其中）——
// 前提被改掉时，上面那些拿业务码做断言的用例会集体失真。
func TestConnUsesRegisteredCloseCodes(t *testing.T) {
	for _, code := range []int{
		agentproto.CloseVersionMismatch,
		agentproto.CloseUnauthorized,
		agentproto.CloseMalformedMessage,
		agentproto.CloseUnsupportedType,
		agentproto.CloseHeartbeatTimeout,
		agentproto.CloseProtocolViolation,
		agentproto.CloseDuplicateInstance, // hub 顶替路径（Task 1）
		agentproto.CloseRateLimited,       // 入站帧级限流（本任务：超限关闭）
	} {
		if !isRegisteredCloseCode(code) {
			t.Fatalf("关闭码 %d 未在协议登记集 %v", code, agentproto.AllCloseCodes())
		}
	}
	if isRegisteredCloseCode(agentproto.CloseNormal) || isRegisteredCloseCode(agentproto.CloseGoingAway) {
		t.Fatalf("登记集不应包含标准码 1000/1001（实际 %v）—— 状态机里的关闭码必须是业务码",
			agentproto.AllCloseCodes())
	}
}

// TestConnDispatchCoversAllAgentToCoreTypes 钉住分派表的完备性：
// 协议里每个 agent→core 类型都必须在状态机里有对应处理 ——
// 协议新增类型而忘了接线时，这条断言会红，而不是让该类型被静默忽略。
func TestConnDispatchCoversAllAgentToCoreTypes(t *testing.T) {
	got := make([]string, 0, len(agentHandlers))
	for typ := range agentHandlers {
		got = append(got, typ)
	}
	slices.Sort(got)

	want := make([]string, 0, len(got))
	for _, typ := range agentproto.AllTypes() {
		if agentproto.DirectionOf(typ) == agentproto.DirAgentToCore {
			want = append(want, typ)
		}
	}
	// 比的是**集合**（协议注册顺序与 map 迭代顺序都不是契约），故两边都排序。
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("分派表 = %v, 协议里 agent→core 类型 = %v（必须一一对应）", got, want)
	}
}
