package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"github.com/tangwy-t/UniCenter/uni_core/internal/middleware"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstream"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/ws"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── 夹具 ────────────────────────────────────────────────────────────────

const streamSessionID = "sess-0123456789abcdef"

// seedStreamRecord 建一条**已建立会话**的指令记录（succeeded + session_id）。
func seedStreamRecord(t *testing.T, env *dockerTestEnv, device, user uint64, action string) *dockerstate.CmdRecord {
	t.Helper()
	ctx := context.Background()
	rec := &dockerstate.CmdRecord{Ref: "1790000000001", DeviceID: device, Action: action,
		Target: "mysql", UserID: user, Perm: recordPerm(action), SessionID: streamSessionID}
	if err := env.cmds.Create(ctx, rec, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := env.cmds.Complete(ctx, rec, &agentproto.DockerCmdResult{Ref: rec.Ref, OK: true, SessionID: streamSessionID}); err != nil {
		t.Fatal(err)
	}
	return rec
}

func recordPerm(action string) string {
	switch action {
	case agentproto.DockerActionContainerExec:
		return "docker:exec"
	default:
		return "docker:inspect"
	}
}

// seedStreamSession 在注册表里登记会话（模拟 agent 回 result 后的登记结果）。
func seedStreamSession(t *testing.T, env *dockerTestEnv, device, user uint64, action string) *dockerstream.Session {
	t.Helper()
	if err := env.sessions.Register(dockerstream.Meta{
		SessionID: streamSessionID, DeviceID: device, UserID: user, Action: action,
		Ref: "1790000000001", Kind: dockerstream.KindForAction(action), CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	return env.sessions.Get(streamSessionID)
}

// logStreamContext 造一个日志流请求上下文（可取消，模拟客户端断开）。
func logStreamContext(rec *dockerstate.CmdRecord, uid uint64) (*httptest.ResponseRecorder, *gin.Context, context.CancelFunc) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	ctx, cancel := context.WithCancel(context.Background())
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/docker/hosts/7/cmds/"+rec.Ref+"/stream", nil).WithContext(ctx)
	c.Params = gin.Params{{Key: "id", Value: "7"}, {Key: "ref", Value: rec.Ref}}
	c.Set(middleware.CtxClaims, testClaims(uid))
	return w, c, cancel
}

// waitStream 是本文件内对既有 waitFor 的薄封装（异步转发路径的断言不靠 sleep 猜）。
func waitStream(t *testing.T, what string, cond func() bool) {
	t.Helper()
	waitFor(t, what, 3*time.Second, cond)
}

// TestDockerExecUpGraderSharesOriginCheckWithConsole：终端的 Origin 判定必须是
// **同一个函数值**（`ws.CheckOrigin`），不是又一份拷贝 —— 三个 WS 端点（console /
// agent / 终端）肩并肩挂在同一个 api 组上，策略分叉会变成线上玄学。
func TestDockerExecUpGraderSharesOriginCheckWithConsole(t *testing.T) {
	if dockerExecUpGrader.CheckOrigin == nil {
		t.Fatal("终端 upGrader 没有装 CheckOrigin：gorilla 会退回默认同源判定")
	}
	if reflect.ValueOf(dockerExecUpGrader.CheckOrigin).Pointer() != reflect.ValueOf(ws.CheckOrigin).Pointer() {
		t.Fatal("终端的 Origin 判定不是 ws.CheckOrigin（策略改动会漏掉这个端点）")
	}
}

// ── 结果 DTO：ticket 签发与归属 ─────────────────────────────────────────

// TestCmdResultIssuesFreshStreamTicket：建立了会话的指令，**每次**轮询都带一张新票；
// 没有会话的指令不带；轮询者只能看自己的 ref（否则票据就是劫持终端的大门钥匙）。
func TestCmdResultIssuesFreshStreamTicket(t *testing.T) {
	ctx := context.Background()
	env := newTestDockerHandler(t, []string{"docker:inspect"})
	rec := seedStreamRecord(t, env, 7, 1, agentproto.DockerActionContainerLogs)

	poll := func(uid uint64) (int, string) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/docker/hosts/7/cmds/"+rec.Ref, nil)
		c.Params = gin.Params{{Key: "id", Value: "7"}, {Key: "ref", Value: rec.Ref}}
		c.Set(middleware.CtxClaims, testClaims(uid))
		env.handler.CmdResult(c)
		var body struct {
			Data struct {
				StreamTicket string `json:"streamTicket"`
			} `json:"data"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body.Data.StreamTicket
	}

	code, tk1 := poll(1)
	if code != http.StatusOK || tk1 == "" {
		t.Fatalf("建立会话的指令必须带 streamTicket: code=%d ticket=%q", code, tk1)
	}
	code, tk2 := poll(1)
	if code != http.StatusOK || tk2 == "" || tk2 == tk1 {
		t.Fatalf("每次轮询必须带一张**新**票据: code=%d t1=%q t2=%q", code, tk1, tk2)
	}
	// 票据绑定三要素：用服务面兑换验证（userId=1、device=7、session）。
	got, err := env.streams.RedeemTicket(ctx, tk2)
	if err != nil || got == nil || got.UserID != 1 || got.DeviceID != 7 || got.SessionID == "" {
		t.Fatalf("票据绑定不符: %+v %v", got, err)
	}

	// 非发起人：403 且**不得**拿到票据（票据绑的是发起人，泄漏=劫持）。
	code, tk3 := poll(2)
	if code != http.StatusForbidden || tk3 != "" {
		t.Fatalf("非发起人必须 403 且不带票据: code=%d ticket=%q", code, tk3)
	}

	// 没有会话的指令（一次性取日志）：正常返回但不带票据。
	env2 := newTestDockerHandler(t, []string{"docker:inspect"})
	plain := &dockerstate.CmdRecord{Ref: "1790000000002", DeviceID: 7, Action: agentproto.DockerActionContainerLogs,
		Target: "mysql", UserID: 1, Perm: "docker:inspect"}
	if err := env2.cmds.Create(ctx, plain, time.Minute); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/docker/hosts/7/cmds/"+plain.Ref, nil)
	c.Params = gin.Params{{Key: "id", Value: "7"}, {Key: "ref", Value: plain.Ref}}
	c.Set(middleware.CtxClaims, testClaims(1))
	env2.handler.CmdResult(c)
	_, tkPlain := func() (int, string) {
		var body struct {
			Data struct {
				StreamTicket string `json:"streamTicket"`
			} `json:"data"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body.Data.StreamTicket
	}()
	if tkPlain != "" {
		t.Fatalf("没有会话的指令不得带票据: %q", tkPlain)
	}
}

// ── 日志流（NDJSON）──────────────────────────────────────────────────────

// TestLogStreamRejects: 权限、归属、无会话、非日志 action 四条拒绝路径。
func TestLogStreamRejects(t *testing.T) {
	t.Run("无权限 403", func(t *testing.T) {
		env := newTestDockerHandler(t, []string{"docker:list"}) // 缺 docker:inspect
		rec := seedStreamRecord(t, env, 7, 1, agentproto.DockerActionContainerLogs)
		w, c, cancel := logStreamContext(rec, 1)
		defer cancel()
		env.handler.LogStream(c)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "无操作权限") {
			t.Fatalf("无权限必须 403: %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("非发起人 403", func(t *testing.T) {
		env := newTestDockerHandler(t, []string{"docker:inspect"})
		rec := seedStreamRecord(t, env, 7, 1, agentproto.DockerActionContainerLogs)
		w, c, cancel := logStreamContext(rec, 2)
		defer cancel()
		env.handler.LogStream(c)
		if w.Code != http.StatusForbidden {
			t.Fatalf("非发起人必须 403: %d %s", w.Code, w.Body.String())
		}
		if env.sessions.Len() != 0 {
			t.Fatal("被拒的请求不得登记任何会话")
		}
	})

	t.Run("一次性取日志（无会话）409", func(t *testing.T) {
		env := newTestDockerHandler(t, []string{"docker:inspect"})
		ctx := context.Background()
		rec := &dockerstate.CmdRecord{Ref: "1790000000003", DeviceID: 7, Action: agentproto.DockerActionContainerLogs,
			Target: "mysql", UserID: 1, Perm: "docker:inspect"}
		if err := env.cmds.Create(ctx, rec, time.Minute); err != nil {
			t.Fatal(err)
		}
		w, c, cancel := logStreamContext(rec, 1)
		defer cancel()
		env.handler.LogStream(c)
		if w.Code != http.StatusConflict {
			t.Fatalf("没有会话必须 409: %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("终端指令不能接日志端点 400", func(t *testing.T) {
		env := newTestDockerHandler(t, []string{"docker:exec"})
		rec := seedStreamRecord(t, env, 7, 1, agentproto.DockerActionContainerExec)
		w, c, cancel := logStreamContext(rec, 1)
		defer cancel()
		env.handler.LogStream(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("exec 指令必须 400: %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("会话不存在 404", func(t *testing.T) {
		env := newTestDockerHandler(t, []string{"docker:inspect"})
		rec := seedStreamRecord(t, env, 7, 1, agentproto.DockerActionContainerLogs) // 注册表未登记
		w, c, cancel := logStreamContext(rec, 1)
		defer cancel()
		env.handler.LogStream(c)
		if w.Code != http.StatusNotFound {
			t.Fatalf("会话不存在必须 404: %d %s", w.Code, w.Body.String())
		}
	})
}

// TestLogStreamForwardsNDJSON：缓冲帧按序转成 NDJSON 行（data 为 base64），
// eof 那一行收尾后会话被清理；Content-Type 是 x-ndjson。
func TestLogStreamForwardsNDJSON(t *testing.T) {
	ctx := context.Background()
	env := newTestDockerHandler(t, []string{"docker:inspect"})
	rec := seedStreamRecord(t, env, 7, 1, agentproto.DockerActionContainerLogs)
	seedStreamSession(t, env, 7, 1, agentproto.DockerActionContainerLogs)

	for _, f := range []*agentproto.DockerFrame{
		{SessionID: streamSessionID, Seq: 1, Data: []byte("hello ")},
		{SessionID: streamSessionID, Seq: 2, Data: []byte("world\n")},
		{SessionID: streamSessionID, Seq: 3, Data: []byte("bye"), EOF: true},
	} {
		if err := env.sessions.DeliverDockerFrame(ctx, 7, f); err != nil {
			t.Fatal(err)
		}
	}

	w, c, cancel := logStreamContext(rec, 1)
	defer cancel()
	env.handler.LogStream(c)

	if ct := w.Header().Get("Content-Type"); ct != "application/x-ndjson" {
		t.Fatalf("Content-Type 必须是 application/x-ndjson，实际 %q", ct)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d (%s)", w.Code, w.Body.String())
	}
	lines := strings.Split(strings.TrimSpace(w.Body.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("应有 3 行 NDJSON，实际 %d: %q", len(lines), lines)
	}
	type line struct {
		Seq  uint64 `json:"seq"`
		Data string `json:"data"`
		EOF  bool   `json:"eof"`
	}
	want := []line{
		{1, "aGVsbG8g", false},
		{2, "d29ybGQK", false},
		{3, "Ynll", true},
	}
	for i, raw := range lines {
		var got line
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatalf("第 %d 行不是合法 JSON: %v (%q)", i, err, raw)
		}
		if got != want[i] {
			t.Fatalf("第 %d 行不符: got %+v want %+v", i, got, want[i])
		}
	}
	if env.sessions.Get(streamSessionID) != nil {
		t.Fatal("eof 转发完必须清理会话")
	}
	// 自然结束**不得**下发 cancel（agent 侧已经结束了）。
	for _, fr := range env.sender.frameOps() {
		if fr.Op == agentproto.DockerFrameOpCancel {
			t.Fatalf("正常 eof 收尾不得下发 cancel: %+v", fr)
		}
	}
}

// TestLogStreamDisconnectCancelsAgent：客户端断开（请求 ctx 取消）→ 向 agent 下发
// cancel 并清理会话 —— 否则日志进程会一直挂到 agent 的空闲超时。
func TestLogStreamDisconnectCancelsAgent(t *testing.T) {
	env := newTestDockerHandler(t, []string{"docker:inspect"})
	rec := seedStreamRecord(t, env, 7, 1, agentproto.DockerActionContainerLogs)
	sess := seedStreamSession(t, env, 7, 1, agentproto.DockerActionContainerLogs)

	w, c, cancel := logStreamContext(rec, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		env.handler.LogStream(c)
	}()
	waitStream(t, "接入完成", func() bool { return sess.Attached() })
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("客户端断开后处理器必须返回")
	}
	if env.sessions.Get(streamSessionID) != nil {
		t.Fatal("断开后必须清理会话")
	}
	frames := env.sender.frameOps()
	if len(frames) != 1 || frames[0].Op != agentproto.DockerFrameOpCancel ||
		frames[0].SessionID != streamSessionID {
		t.Fatalf("断开必须下发 cancel: %+v", frames)
	}
	if len(env.sender.deviceIDs()) != 1 || env.sender.deviceIDs()[0] != 7 {
		t.Fatalf("cancel 必须发往会话设备: %v", env.sender.deviceIDs())
	}
	_ = w
}

// ── 终端流（WebSocket）────────────────────────────────────────────────────

// newStreamHTTPServer 起一个真实 HTTP 服务器（WS 需要真实握手），并注入登录身份。
func newStreamHTTPServer(t *testing.T, env *dockerTestEnv, uid uint64) *httptest.Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.CtxClaims, testClaims(uid))
		c.Next()
	})
	r.GET("/api/v1/docker/hosts/:id/cmds/:ref/stream", env.handler.LogStream)
	r.GET("/api/v1/docker/hosts/:id/stream/exec", env.handler.ExecStream)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

// dialExec 连终端端点；握手失败时返回 HTTP 状态码（0 表示成功）。
func dialExec(t *testing.T, srv *httptest.Server, ticket string) (*websocket.Conn, int, error) {
	t.Helper()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") +
		"/api/v1/docker/hosts/7/stream/exec?ticket=" + ticket
	conn, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		if resp != nil {
			return nil, resp.StatusCode, err
		}
		return nil, 0, err
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, http.StatusSwitchingProtocols, nil
}

// issueTicket 直接走票据存储签发一张（等价于轮询拿到了 streamTicket）。
func issueTicket(t *testing.T, env *dockerTestEnv, user, device uint64) string {
	t.Helper()
	tk, err := env.tickets.Issue(context.Background(), user, device, streamSessionID)
	if err != nil {
		t.Fatal(err)
	}
	return tk.Ticket
}

// TestExecStreamTicketRejects：无票/坏票/过期/跨主机/重复消费的失败形态
// （401/403，不泄漏细节）；同一会话的第二条连接 409。
func TestExecStreamTicketRejects(t *testing.T) {
	ctx := context.Background()
	env := newTestDockerHandler(t, nil)
	srv := newStreamHTTPServer(t, env, 1)

	t.Run("无 ticket → 401", func(t *testing.T) {
		url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/docker/hosts/7/stream/exec"
		_, resp, err := websocket.DefaultDialer.Dial(url, nil)
		if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("无 ticket 必须 401: resp=%v err=%v", resp, err)
		}
	})

	t.Run("未知 ticket → 401", func(t *testing.T) {
		_, code, err := dialExec(t, srv, "no-such-ticket")
		if err == nil || code != http.StatusUnauthorized {
			t.Fatalf("未知 ticket 必须 401: code=%d err=%v", code, err)
		}
	})

	t.Run("过期 ticket → 401", func(t *testing.T) {
		seedStreamSession(t, env, 7, 1, agentproto.DockerActionContainerExec)
		tk := issueTicket(t, env, 1, 7)
		env.mr.FastForward(dockerstate.StreamTicketTTL + time.Second)
		_, code, err := dialExec(t, srv, tk)
		if err == nil || code != http.StatusUnauthorized {
			t.Fatalf("过期 ticket 必须 401: code=%d err=%v", code, err)
		}
	})

	t.Run("跨主机 ticket → 403", func(t *testing.T) {
		// 票据绑定设备 8，但路径是 7。
		tk, err := env.tickets.Issue(ctx, 1, 8, streamSessionID)
		if err != nil {
			t.Fatal(err)
		}
		_, code, derr := dialExec(t, srv, tk.Ticket)
		if derr == nil || code != http.StatusForbidden {
			t.Fatalf("跨主机 ticket 必须 403: code=%d err=%v", code, derr)
		}
	})
}

// TestExecWSRelaysAndControls：端到端 —— 建会话、升级、收 data/eof、
// 发 input/resize/cancel 都到达 agent 侧假实现；票据单次使用；重复连接 409。
func TestExecWSRelaysAndControls(t *testing.T) {
	ctx := context.Background()
	env := newTestDockerHandler(t, nil)
	srv := newStreamHTTPServer(t, env, 1)
	seedStreamSession(t, env, 7, 1, agentproto.DockerActionContainerExec)

	tk1 := issueTicket(t, env, 1, 7)
	conn, code, err := dialExec(t, srv, tk1)
	if err != nil {
		t.Fatalf("有效票据必须升级成功: code=%d err=%v", code, err)
	}
	// 连接成功即作废（单次使用）。
	if _, code, err := dialExec(t, srv, tk1); err == nil || code != http.StatusUnauthorized {
		t.Fatalf("已用票据必须 401: code=%d err=%v", code, err)
	}

	// 数据帧 → 客户端收到 {"type":"data","data":base64}；eof 可与末帧数据同帧。
	if err := env.sessions.DeliverDockerFrame(ctx, 7,
		&agentproto.DockerFrame{SessionID: streamSessionID, Seq: 1, Data: []byte("hi")}); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var msg execServerMsg
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatalf("读取 data 帧失败: %v", err)
	}
	if msg.Type != "data" || string(msg.Data) != "hi" {
		t.Fatalf("data 消息不符: %+v", msg)
	}

	// 客户端 → agent：input / resize。
	if err := conn.WriteJSON(execClientMsg{Type: "input", Data: "ls -la\n"}); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteJSON(execClientMsg{Type: "resize", Cols: 120, Rows: 40}); err != nil {
		t.Fatal(err)
	}
	waitStream(t, "input/resize 到达 agent", func() bool { return len(env.sender.frameOps()) >= 2 })
	frames := env.sender.frameOps()
	if frames[0].Op != agentproto.DockerFrameOpInput || string(frames[0].Data) != "ls -la\n" {
		t.Fatalf("input 未到达 agent: %+v", frames[0])
	}
	if frames[1].Op != agentproto.DockerFrameOpResize || frames[1].Cols != 120 || frames[1].Rows != 40 {
		t.Fatalf("resize 未到达 agent: %+v", frames[1])
	}

	// 使用中的会话：第二条连接（新票据）必须 409。
	tk2 := issueTicket(t, env, 1, 7)
	if _, code, err := dialExec(t, srv, tk2); err == nil || code != http.StatusConflict {
		t.Fatalf("同一会话第二条连接必须 409: code=%d err=%v", code, err)
	}

	// eof：客户端收到 data（若有）+ eof，随后连接关闭。
	if err := env.sessions.DeliverDockerFrame(ctx, 7,
		&agentproto.DockerFrame{SessionID: streamSessionID, Seq: 2, Data: []byte("bye"), EOF: true}); err != nil {
		t.Fatal(err)
	}
	if err := conn.ReadJSON(&msg); err != nil || msg.Type != "data" || string(msg.Data) != "bye" {
		t.Fatalf("末帧数据不符: %+v %v", msg, err)
	}
	if err := conn.ReadJSON(&msg); err != nil || msg.Type != "eof" {
		t.Fatalf("必须收到 eof: %+v %v", msg, err)
	}
	waitStream(t, "eof 后会话清理", func() bool { return env.sessions.Get(streamSessionID) == nil })
}

// TestExecWSCancelFromClient：客户端 {type:"cancel"} → agent 收到 cancel、会话释放。
func TestExecWSCancelFromClient(t *testing.T) {
	env := newTestDockerHandler(t, nil)
	srv := newStreamHTTPServer(t, env, 1)
	seedStreamSession(t, env, 7, 1, agentproto.DockerActionContainerExec)

	conn, code, err := dialExec(t, srv, issueTicket(t, env, 1, 7))
	if err != nil {
		t.Fatalf("升级失败: code=%d err=%v", code, err)
	}
	if err := conn.WriteJSON(execClientMsg{Type: "cancel"}); err != nil {
		t.Fatal(err)
	}
	waitStream(t, "cancel 到达 agent 且会话释放", func() bool {
		if env.sessions.Get(streamSessionID) != nil {
			return false
		}
		for _, fr := range env.sender.frameOps() {
			if fr.Op == agentproto.DockerFrameOpCancel {
				return true
			}
		}
		return false
	})
}

// TestExecWSDisconnectCancelsAgent：客户端直接断开（不显式 cancel）→
// 写侧收手、读侧检测到断开 → 向 agent 下发 cancel（plan §0 的断开清理）。
func TestExecWSDisconnectCancelsAgent(t *testing.T) {
	env := newTestDockerHandler(t, nil)
	srv := newStreamHTTPServer(t, env, 1)
	seedStreamSession(t, env, 7, 1, agentproto.DockerActionContainerExec)

	conn, code, err := dialExec(t, srv, issueTicket(t, env, 1, 7))
	if err != nil {
		t.Fatalf("升级失败: code=%d err=%v", code, err)
	}
	waitStream(t, "接入完成", func() bool {
		s := env.sessions.Get(streamSessionID)
		return s != nil && s.Attached()
	})
	_ = conn.Close()
	waitStream(t, "断开后下发 cancel 并清理", func() bool {
		if env.sessions.Get(streamSessionID) != nil {
			return false
		}
		for _, fr := range env.sender.frameOps() {
			if fr.Op == agentproto.DockerFrameOpCancel {
				return true
			}
		}
		return false
	})
}
