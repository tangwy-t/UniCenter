package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agenthub"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/ws"
)

// 本文件用**真实 TCP**（httptest.NewServer + gorilla 客户端）驱动未鉴权的 agent
// 升级入口：Task 2 的 conn_test.go 用内存 socket 替身覆盖了读循环内部，而
// 「升级是否成功 / Origin 策略 / 挂载点是否真在鉴权组之外」只有真实握手能证明。
//
// 依赖真假参半：hub 用真 *agenthub.Hub（注销语义就长在它身上，替身证明不了
// OnlineCount 归零），service 面用最小假实现（本任务不碰服务层）。

const agentWSPath = "/agent/ws"

// testDeviceID 是假 enroller 签发的设备 ID。取 1 而非 0：0 是 snowflake 的
// 「未绑定」哨兵，conn.go 会按失败关闭处理。
const testDeviceID uint64 = 1001

// ── 极简假依赖 ──────────────────────────────────────────────

type fakeEnroller struct {
	mu     sync.Mutex
	calls  int
	device uint64
}

func (f *fakeEnroller) Enroll(_ context.Context, _ *agentproto.Hello) (uint64, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.device, "tok-1001", nil
}

func (f *fakeEnroller) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakeDecider struct{}

func (fakeDecider) IsAccepting(_ context.Context, _ uint64) (bool, error) { return true, nil }

type fakeToucher struct{}

func (fakeToucher) Touch(_ context.Context, _ uint64) error { return nil }

// ── 夹具 ────────────────────────────────────────────────────

type agentWSFixture struct {
	t      *testing.T
	hub    *agenthub.Hub
	hdl    *AgentWSHandler
	srv    *httptest.Server
	enroll *fakeEnroller
}

func newAgentWSFixture(t *testing.T, engine *gin.Engine) *agentWSFixture {
	t.Helper()
	log := logger.NewNop()
	hub := agenthub.NewHub(agenthub.Options{}, log)
	enroll := &fakeEnroller{device: testDeviceID}
	hdl := NewAgentWSHandler(hub, agenthub.Deps{
		Enroller: enroll,
		Decider:  fakeDecider{},
		Toucher:  fakeToucher{},
	}, log)

	if engine == nil {
		engine = gin.New()
		engine.GET(agentWSPath, hdl.Serve)
	}
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)

	return &agentWSFixture{t: t, hub: hub, hdl: hdl, srv: srv, enroll: enroll}
}

// wsURL 把 httptest 的 http:// 换成 ws://。
func (f *agentWSFixture) wsURL() string {
	return "ws" + strings.TrimPrefix(f.srv.URL, "http") + agentWSPath
}

// dial 用真实 TCP 拨号；err 非 nil 时 resp 携带握手响应（非 101 时 gorrila 会返回它）。
func (f *agentWSFixture) dial(headers http.Header, origin string) (*websocket.Conn, *http.Response, error) {
	f.t.Helper()
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	if headers == nil {
		headers = http.Header{}
	}
	if origin != "" {
		headers.Set("Origin", origin)
	}
	return dialer.Dial(f.wsURL(), headers)
}

// dialOK 拨号并要求握手成功（101）。
func (f *agentWSFixture) dialOK() *websocket.Conn {
	f.t.Helper()
	conn, resp, err := f.dial(nil, "")
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		f.t.Fatalf("握手应成功，实际失败（status=%d）: %v", status, err)
	}
	f.t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// waitFor 是带超时的轮询。
//
// 为什么不用固定 sleep 断言瞬时值：注销发生在 handler 的 defer 里，与测试的
// 下一次读取是**并发**的；固定 sleep 要么短到偶发变红、要么长到拖慢整个包。
// 轮询到超时才判失败，既不误报也不掩盖「永远不会归零」的实现缺陷。
func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s：%v 内未满足", what, timeout)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ── 帧构造 ──────────────────────────────────────────────────

type helloPayload struct {
	InstanceID   string `json:"instance_id"`
	Hostname     string `json:"hostname"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	AgentVersion string `json:"agent_version"`
	EnrollToken  string `json:"enroll_token"`
}

type wireFrame struct {
	V    int             `json:"v"`
	ID   string          `json:"id"`
	Type string          `json:"type"`
	TS   int64           `json:"ts"`
	Data json.RawMessage `json:"data"`
}

// validHelloFrame 造一份必填字段齐全、带 enroll_token 的 hello。
func validHelloFrame(t *testing.T) []byte {
	t.Helper()
	payload, err := json.Marshal(helloPayload{
		InstanceID:   "i-1001",
		Hostname:     "host-1001",
		OS:           "linux",
		Arch:         "amd64",
		AgentVersion: "1.0.0",
		EnrollToken:  "enroll-tok",
	})
	if err != nil {
		t.Fatalf("编码 hello 载荷: %v", err)
	}
	return mustJSON(t, wireFrame{
		V:    agentproto.CurrentVersion,
		ID:   "1",
		Type: agentproto.TypeAgentHello,
		TS:   time.Now().UnixMilli(),
		Data: payload,
	})
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("编码信封: %v", err)
	}
	return b
}

// registeredCloseCode 报告 code 是否在协议登记的业务关闭码集内（4000-4008）。
//
// **不能**改判成「只要 err != nil 就算过」：那样服务端下发 1000/1001（甚至根本没
// 下发关闭帧）也会绿。这里读到的必须是登记过的码。
func registeredCloseCode(code int) bool {
	return slicesContainsInt(agentproto.AllCloseCodes(), code)
}

func slicesContainsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// requireRegisteredClose 从一条入站读里取出关闭码并断言它登记过。
func requireRegisteredClose(t *testing.T, conn *websocket.Conn, what string) int {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("%s：设置读期限: %v", what, err)
	}
	_, _, err := conn.ReadMessage()
	if err == nil {
		t.Fatalf("%s：服务端应关闭连接，实际仍可读", what)
	}
	var ce *websocket.CloseError
	if !errors.As(err, &ce) {
		t.Fatalf("%s：应收到关闭帧，实际错误: %v", what, err)
	}
	if !registeredCloseCode(ce.Code) {
		t.Fatalf("%s：关闭码 %d 未登记在 AllCloseCodes()=%v 中", what, ce.Code, agentproto.AllCloseCodes())
	}
	return ce.Code
}

// ── 断言 1：升级成功 ────────────────────────────────────────

func TestAgentWSHandshakeSucceeds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	f := newAgentWSFixture(t, nil)

	conn := f.dialOK()

	// 能写：客户端发出 hello（写成功即证明底层连接是活的）。
	if err := conn.WriteMessage(websocket.TextMessage, validHelloFrame(t)); err != nil {
		t.Fatalf("写出 hello 失败: %v", err)
	}
	// 能读：服务端回 hello_ack。
	ack := readHelloAck(t, conn)
	if !ack.Accepted {
		t.Fatalf("hello_ack.accepted 应为 true: %+v", ack)
	}
	if ack.DeviceID != "1001" {
		t.Fatalf("hello_ack.device_id 应为 1001，实际 %q", ack.DeviceID)
	}
	if f.enroll.callCount() != 1 {
		t.Fatalf("enroller 应被调用 1 次，实际 %d", f.enroll.callCount())
	}
}

// readHelloAck 读到 core.hello_ack 并解出载荷。
func readHelloAck(t *testing.T, conn *websocket.Conn) *agentproto.HelloAck {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("设置读期限: %v", err)
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("读取 hello_ack 失败: %v", err)
	}
	m, err := agentproto.Decode(raw)
	if err != nil {
		t.Fatalf("解码应答信封失败: %v（原文 %s）", err, raw)
	}
	if m.Type != agentproto.TypeCoreHelloAck {
		t.Fatalf("应答类型应为 %s，实际 %s", agentproto.TypeCoreHelloAck, m.Type)
	}
	ack := &agentproto.HelloAck{}
	if err := m.DecodeData(ack); err != nil {
		t.Fatalf("解码 hello_ack 载荷失败: %v", err)
	}
	return ack
}

// ─ 断言 2：Origin 策略与既有 console WS 一致 ───────────────

// 既有 internal/pkg/ws/handler.go 的 checkOrigin 策略是：
//   - 无 Origin → 放行（非浏览器客户端；**agent 正是这一类**）
//   - Origin 主机 == 请求 Host（任意端口） → 放行
//   - Origin 主机是 localhost/127.0.0.1/::1 → 放行（开发环境跨端口）
//   - 其余（真跨站） → 拒绝
//
// 这里逐条钉住 agent 端点采用**同一套**策略，而不是自造一套。
func TestAgentWSOriginPolicyMatchesConsole(t *testing.T) {
	gin.SetMode(gin.TestMode)
	f := newAgentWSFixture(t, nil)

	// 非浏览器客户端（无 Origin）必须放行 —— agent 不带 Origin。
	if conn, _, err := f.dial(nil, ""); err != nil {
		t.Fatalf("无 Origin 的客户端应被放行: %v", err)
	} else {
		_ = conn.Close()
	}

	// 同主机 Origin 放行。
	if conn, _, err := f.dial(nil, f.srv.URL); err != nil {
		t.Fatalf("同主机 Origin 应被放行: %v", err)
	} else {
		_ = conn.Close()
	}

	// 真跨站 Origin 拒绝（状态码为 gorilla 默认的 403）。
	conn, resp, err := f.dial(nil, "http://evil.example.com")
	if err == nil {
		_ = conn.Close()
		t.Fatal("跨站 Origin 应被拒绝，实际握手成功")
	}
	if resp == nil {
		t.Fatalf("拒绝时应返回握手响应: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("跨站 Origin 应返回 403，实际 %d", resp.StatusCode)
	}
}

// TestAgentUpGraderSharesOriginCheckWithConsole 钉住「两处用的是**同一个函数**」。
//
// 缺陷本体：agent 侧的 Origin 判定曾经是 console 侧逐行复制的一份拷贝（注释还自称
// 「复用同一套判定」）。拷贝的问题不是「当前不一致」，而是**改动会漏**：策略收紧时
// 只改一边，另一边静默沿用旧策略，而两个端点挂在同一个 api 组上。
//
// 断言分三层，缺一层都能被绕过：
//  1. **函数值同一**（reflect 取代码指针）：`agentUpGrader.CheckOrigin` 必须就是
//     ws.CheckOrigin —— 这是「同一份实现」的直接证据。行为一致做不到这一点：
//     两份拷贝当前恰好相同也会「行为一致」。
//  2. **行为矩阵一致**：在若干 Origin 用例上两者的返回值必须逐个相同
//     （防止将来有人把共享函数包一层再挂上去，指针相同但语义已被改写）。
//  3. **策略本身的期望值**：用例带 want，避免「两边一起错」也算通过。
func TestAgentUpGraderSharesOriginCheckWithConsole(t *testing.T) {
	if agentUpGrader.CheckOrigin == nil {
		t.Fatal("agent upGrader 没有装 CheckOrigin：gorilla 会退回默认同源判定，" +
			"不带 Origin 的 agent 直接被拒（整条上报通道不可用）")
	}
	if reflect.ValueOf(agentUpGrader.CheckOrigin).Pointer() != reflect.ValueOf(ws.CheckOrigin).Pointer() {
		t.Fatal("agent 与 console 的 Origin 判定不是同一个函数（又变成了一份拷贝）—— " +
			"策略改动会漏掉一个端点")
	}

	cases := []struct {
		name   string
		origin string
		host   string
		want   bool
	}{
		{"无 Origin（agent 正是这一类）", "", "api.example.com", true},
		{"同主机任意端口", "https://example.com:5173", "example.com:8080", true},
		{"同主机同端口", "https://api.example.com", "api.example.com", true},
		{"真跨站拒绝", "https://evil.example.net", "api.example.com", false},
		{"localhost 开发放行", "http://localhost:5173", "api.example.com", true},
		{"环回地址开发放行", "http://127.0.0.1:3000", "api.example.com", true},
		{"伪装 localhost 子域拒绝", "http://localhost.evil.com", "api.example.com", false},
		{"无法解析的 Origin 拒绝", "http://[::1", "api.example.com", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://"+tc.host+"/agent/ws", nil)
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			r.Host = tc.host

			agentGot := agentUpGrader.CheckOrigin(r)
			consoleGot := ws.CheckOrigin(r)
			if agentGot != consoleGot {
				t.Fatalf("同一请求上 agent 判定 = %v、console 判定 = %v（两处策略已分叉）", agentGot, consoleGot)
			}
			if agentGot != tc.want {
				t.Fatalf("CheckOrigin(origin=%q, host=%q) = %v, want %v", tc.origin, tc.host, agentGot, tc.want)
			}
		})
	}
}

// ── 断言 3：首帧非 hello → 登记过的业务关闭码 ───────────────

func TestAgentWSFirstFrameNotHelloClosesWithRegisteredCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	f := newAgentWSFixture(t, nil)

	conn := f.dialOK()
	// 非法 JSON：既不是 hello，连信封都解不开 → 4002（CloseMalformedMessage）。
	if err := conn.WriteMessage(websocket.TextMessage, []byte("{not json")); err != nil {
		t.Fatalf("写出非法 JSON 帧失败: %v", err)
	}

	code := requireRegisteredClose(t, conn, "首帧非 hello")
	if code != agentproto.CloseMalformedMessage {
		t.Fatalf("非法 JSON 首帧应下发 %d，实际 %d", agentproto.CloseMalformedMessage, code)
	}
	if f.enroll.callCount() != 0 {
		t.Fatalf("非法首帧不应触达 enroller，实际调用 %d 次", f.enroll.callCount())
	}
}

// 方向正确但 hello 之前发心跳 → 4008（CloseProtocolViolation）。
// 与上一条分开：两者都属「首帧非 hello」，但码不同，合并会掩盖状态机判定。
func TestAgentWSHeartbeatBeforeHelloClosesWithProtocolViolation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	f := newAgentWSFixture(t, nil)

	conn := f.dialOK()
	hb := mustJSON(t, wireFrame{
		V:    agentproto.CurrentVersion,
		ID:   "1",
		Type: agentproto.TypeAgentHeartbeat,
		TS:   time.Now().UnixMilli(),
		Data: []byte(`{}`),
	})
	if err := conn.WriteMessage(websocket.TextMessage, hb); err != nil {
		t.Fatalf("写出心跳帧失败: %v", err)
	}

	code := requireRegisteredClose(t, conn, "hello 前的心跳")
	if code != agentproto.CloseProtocolViolation {
		t.Fatalf("hello 前的心跳应下发 %d，实际 %d", agentproto.CloseProtocolViolation, code)
	}
}

// ── 断言 4：注册发生在 hello 成功之后 ───────────────────────

func TestAgentWSRegisterHappensAfterHello(t *testing.T) {
	gin.SetMode(gin.TestMode)
	f := newAgentWSFixture(t, nil)

	conn := f.dialOK()

	// 握手阶段（尚未 hello）：连接可以存在，但**不得**进注册表 ——
	// 否则未鉴权的匿名连接会占住设备槽位，还会被 DrainAll 当成在线设备推送。
	if n := f.hub.OnlineCount(); n != 0 {
		t.Fatalf("握手阶段不得注册进 hub，实际 OnlineCount=%d", n)
	}

	if err := conn.WriteMessage(websocket.TextMessage, validHelloFrame(t)); err != nil {
		t.Fatalf("写出 hello 失败: %v", err)
	}
	_ = readHelloAck(t, conn)

	// hello_ack 已回，注册必须已经发生。
	waitFor(t, "hello 成功后应注册进 hub", 5*time.Second, func() bool { return f.hub.OnlineCount() == 1 })
	if _, ok := f.hub.Get(testDeviceID); !ok {
		t.Fatalf("hub 中应存在设备 %d 的连接", testDeviceID)
	}
}

// ── 断言 5：客户端断开 → hub 最终注销 ───────────────────────

func TestAgentWSUnregistersAfterClientDisconnect(t *testing.T) {
	gin.SetMode(gin.TestMode)
	f := newAgentWSFixture(t, nil)

	conn := f.dialOK()
	if err := conn.WriteMessage(websocket.TextMessage, validHelloFrame(t)); err != nil {
		t.Fatalf("写出 hello 失败: %v", err)
	}
	_ = readHelloAck(t, conn)

	waitFor(t, "hello 成功后应注册进 hub", 5*time.Second, func() bool { return f.hub.OnlineCount() == 1 })

	// 客户端主动断开（不发关闭帧）：模拟 agent 掉线。
	_ = conn.Close()

	// 注销由 handler 在 Serve 返回后按**连接身份**做，与这条读并发；
	// 轮询到超时才算失败。
	waitFor(t, "客户端断开后 hub 应注销", 5*time.Second, func() bool { return f.hub.OnlineCount() == 0 })
	if _, ok := f.hub.Get(testDeviceID); ok {
		t.Fatalf("设备 %d 应已从 hub 移除", testDeviceID)
	}
}

// ── 反向验证：/agent/ws 必须在鉴权组之外 ────────────────────
//
// 这条断言是「挂载位置」的守卫：把路由挪进 auth 组时它必须变红。
// 用一个最小 auth 中间件而不是真 Auth 中间件：本任务要证明的是**分组归属**，
// 而不是 JWT 校验本身（那是 middleware 包的测试范围）；同型判定（缺 token 即
// 401 中止）已足够。
func TestAgentWSRouteIsOutsideAuthGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	f := newAgentWSFixture(t, nil)
	// 夹具已在无鉴权的 engine 上挂了 /agent/ws —— 先证明它可无凭据升级。
	if conn, _, err := f.dial(nil, ""); err != nil {
		t.Fatalf("未鉴权组内应可无凭据升级: %v", err)
	} else {
		_ = conn.Close()
	}

	// 对照：同一 handler 挂进 auth 组时，无凭据握手必须 401。
	guarded := gin.New()
	auth := guarded.Group("", requireBearerToken())
	auth.GET(agentWSPath, f.hdl.Serve)
	gsrv := httptest.NewServer(guarded)
	t.Cleanup(gsrv.Close)

	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, resp, err := dialer.Dial("ws"+strings.TrimPrefix(gsrv.URL, "http")+agentWSPath, nil)
	if err == nil {
		_ = conn.Close()
		t.Fatal("挂进鉴权组后，无凭据握手应失败")
	}
	if resp == nil {
		t.Fatalf("鉴权拒绝时应返回握手响应: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("挂进鉴权组后应返回 401，实际 %d", resp.StatusCode)
	}
}

// requireBearerToken 模拟 auth 组的最小行为：无 Bearer 头即 401 中止。
func requireBearerToken() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !strings.HasPrefix(c.GetHeader("Authorization"), "Bearer ") {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		c.Next()
	}
}
