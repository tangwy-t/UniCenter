package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tangwy-t/UniCenter/uni_core/internal/middleware"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerevents"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── 事件聚合端点（六期·监控面）───────────────────────────────────────────

// syncRecorder 是**并发安全**的响应记录器：流式端点一边写、测试一边等行数是常态，
// 普通 httptest.Recorder 的 Body 在这种时序下是数据竞争，race 下必红。
type syncRecorder struct {
	mu   sync.Mutex
	body bytes.Buffer
	hdr  http.Header
	code int
}

func newSyncRecorder() *syncRecorder {
	return &syncRecorder{hdr: http.Header{}, code: http.StatusOK}
}

func (s *syncRecorder) Header() http.Header { return s.hdr }

func (s *syncRecorder) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.body.Write(b)
}

func (s *syncRecorder) WriteHeader(code int) {
	s.mu.Lock()
	s.code = code
	s.mu.Unlock()
}

// Flush 是 http.Flusher 的实现：NDJSON 端点据此逐行冲刷（这里无缓冲，空操作）。
func (s *syncRecorder) Flush() {}

func (s *syncRecorder) lines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw := strings.TrimSpace(s.body.String())
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "\n")
}

func (s *syncRecorder) Code() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.code
}

// ── 常驻管理器的替身（小接口逐字满足，不拖 Redis）────────────────────────

type evSenderMsg struct {
	deviceID  uint64
	typ       string
	ref       string
	sessionID string
}

type fakeEvSender struct {
	mu   sync.Mutex
	sent []evSenderMsg
}

func (s *fakeEvSender) SendToDevice(deviceID uint64, msg *agentproto.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := evSenderMsg{deviceID: deviceID, typ: msg.Type}
	switch msg.Type {
	case agentproto.TypeCoreDockerCmd:
		var cmd agentproto.DockerCmd
		if err := json.Unmarshal(msg.Data, &cmd); err == nil {
			m.ref = cmd.Ref
		}
	case agentproto.TypeCoreDockerFrame:
		var f agentproto.CoreDockerFrame
		if err := json.Unmarshal(msg.Data, &f); err == nil {
			m.sessionID = f.SessionID
		}
	}
	s.sent = append(s.sent, m)
	return nil
}

func (s *fakeEvSender) cmdRefs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, m := range s.sent {
		if m.typ == agentproto.TypeCoreDockerCmd {
			out = append(out, m.ref)
		}
	}
	return out
}

func (s *fakeEvSender) cancels() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, m := range s.sent {
		if m.typ == agentproto.TypeCoreDockerFrame {
			out = append(out, m.sessionID)
		}
	}
	return out
}

type fakeEvCmds struct{}

func (fakeEvCmds) Create(context.Context, *dockerstate.CmdRecord, time.Duration) error {
	return nil
}

type fakeEvHosts struct{}

func (fakeEvHosts) Hosts(context.Context) ([]uint64, error) { return []uint64{7}, nil }

func (fakeEvHosts) Get(_ context.Context, deviceID uint64) (*dockerstate.Envelope, error) {
	if deviceID != 7 {
		return nil, nil
	}
	return &dockerstate.Envelope{State: agentproto.DockerState{DockerOK: true}}, nil
}

// eventsFixture 起一个**真管理器**（替身依赖 + 后台 Run），供端点级测试驱动。
type eventsFixture struct {
	handler *DockerHandler
	mgr     *dockerevents.Manager
	sender  *fakeEvSender
	cancel  context.CancelFunc
	done    chan struct{}
}

func newEventsFixture(t *testing.T, env *dockerTestEnv) *eventsFixture {
	t.Helper()
	sender := &fakeEvSender{}
	mgr := dockerevents.NewManager(dockerevents.Options{
		// 对账给长周期、靠 wake 驱动：Subscribe/kick 之后的下一轮是**确定的**
		//（等 sender 收到指令即「已对过账」），不会被周期循环抢跑。
		ReconcileInterval: time.Hour,
	}, sender, fakeEvCmds{}, fakeEvHosts{},
		func(_ context.Context, deviceID uint64) string { return "alpha" },
		logger.NewNop()).
		WithOnline(func(deviceID uint64) bool { return deviceID == 7 })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		mgr.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	env.handler.WithEvents(mgr)
	return &eventsFixture{handler: env.handler, mgr: mgr, sender: sender, cancel: cancel, done: done}
}

func evFrame(sessionID string, seq uint64, typ, action, name string) *agentproto.DockerFrame {
	raw, _ := json.Marshal(agentproto.DockerEventItem{
		T: 1790000000000, Type: typ, Action: action, ActorName: name,
	})
	return &agentproto.DockerFrame{SessionID: sessionID, Seq: seq, Data: append(raw, '\n')}
}

// 端到端：回放（打开即有内容）→ 实时扇出 → 客户端断开引用计数归零、常驻订阅取消。
//
// 权限拒绝在**路由层**（静态 perm docker:list，见 router 的挂载守卫测试）——
// 处理器不按指令校验归属（聚合流没有「发起人」），故这里只守鉴权之外的契约。
func TestEventsStreamReplayFanoutAndRefCount(t *testing.T) {
	env := newTestDockerHandler(t, []string{"docker:list"})
	f := newEventsFixture(t, env)
	const sid = "sess-events-00000007"

	// 预备：先有一个消费者建立常驻订阅并放活，播一条事件进环形缓冲。
	c0 := f.mgr.Subscribe()
	waitFor(t, "常驻订阅指令已下发", 3*time.Second, func() bool { return len(f.sender.cmdRefs()) == 1 })
	ref := f.sender.cmdRefs()[0]
	f.mgr.OnEventsResult(7, &agentproto.DockerCmdResult{Ref: ref, OK: true, SessionID: sid})
	if err := f.mgr.DeliverDockerFrame(context.Background(), 7, evFrame(sid, 1, "container", "start", "redis")); err != nil {
		t.Fatal(err)
	}
	rc, cancelC0 := context.WithTimeout(context.Background(), 3*time.Second)
	e, err := c0.Next(rc)
	cancelC0()
	if err != nil || e.Item.ActorName != "redis" {
		t.Fatalf("预备事件的扇出不符: %+v %v", e, err)
	}

	// 端点接入：先回放（redis start），再实时（nginx die）。
	w := newSyncRecorder()
	c, _ := gin.CreateTestContext(w)
	rctx, rcancel := context.WithCancel(context.Background())
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/docker/events", nil).WithContext(rctx)
	c.Set(middleware.CtxClaims, testClaims(1))
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.handler.EventsStream(c)
	}()
	waitFor(t, "端点客户端登记", 3*time.Second, func() bool { return f.mgr.SubscriberCount() == 2 })
	// 实时扇出：这一帧经同一会话投递，抵达端点客户端的队列。
	if err := f.mgr.DeliverDockerFrame(context.Background(), 7, evFrame(sid, 2, "container", "die", "nginx")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "回放+实时两行已写出", 3*time.Second, func() bool { return len(w.lines()) >= 2 })

	// 断开：引用计数 -1；c0 断开后归零 → 常驻订阅取消（cancel 逐台下发）。
	rcancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("端点处理器未随 ctx 断开退出")
	}
	if f.mgr.SubscriberCount() != 1 {
		t.Fatalf("断端后引用计数应为 1（c0 仍在），实际 %d", f.mgr.SubscriberCount())
	}
	c0.Close()
	if f.mgr.SubscriberCount() != 0 {
		t.Fatalf("全部断开后引用计数必须归零，实际 %d", f.mgr.SubscriberCount())
	}
	waitFor(t, "全部退订（cancel 下发）", 3*time.Second, func() bool {
		return f.mgr.SubscribedHosts() == 0
	})
	if got := f.sender.cancels(); len(got) != 1 || got[0] != sid {
		t.Fatalf("退订必须下发 cancel: %v", got)
	}

	// 行形状与顺序：首帧 sentinel（kind=opened，消费端跳过）→ 回放的 start →
	// 实时的 die；归属注入 host_id/hostname。
	type line struct {
		HostID    uint64 `json:"host_id,string"` // 流行是字符串形态的雪花 id（防 JS 精度丢失）
		Hostname  string `json:"hostname"`
		T         int64  `json:"t"`
		Type      string `json:"type"`
		Action    string `json:"action"`
		ActorName string `json:"actor_name"`
		ExitCode  *int32 `json:"exit_code"`
	}
	lines := w.lines()
	if len(lines) != 3 {
		t.Fatalf("应恰好三行 NDJSON（sentinel + 两行数据），实际 %d: %q", len(lines), lines)
	}
	if lines[0] != `{"kind":"opened"}` {
		t.Fatalf("首帧必须是 sentinel（流建立即发，代理据此放行响应头），实际 %q", lines[0])
	}
	var first, second line
	if err := json.Unmarshal([]byte(lines[1]), &first); err != nil {
		t.Fatalf("第 2 行不是合法 JSON: %v (%q)", err, lines[1])
	}
	if err := json.Unmarshal([]byte(lines[2]), &second); err != nil {
		t.Fatalf("第 3 行不是合法 JSON: %v (%q)", err, lines[2])
	}
	if first.Action != "start" || first.ActorName != "redis" || first.HostID != 7 || first.Hostname != "alpha" {
		t.Fatalf("回放行不符: %+v", first)
	}
	if second.Action != "die" || second.ActorName != "nginx" || second.HostID != 7 {
		t.Fatalf("实时行不符: %+v", second)
	}
	if ct := w.hdr.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Fatalf("Content-Type 必须是 application/x-ndjson，实际 %q", ct)
	}
	if code := w.Code(); code != http.StatusOK {
		t.Fatalf("status=%d", code)
	}
}

// sentinel 的**病灶钉子**：保留窗口空 + 无任何事件时，流建立后仍必须立刻写出一帧
// —— 一个字节都不发正是「经代理页面停『连接中…』」的根因（实测：直连 :8088 头 8ms
// 即到，经 vite dev 代理则压到首字节才放行）。这条测试在无数据的静默流上断言
// 首帧在建立后极短时间内可见。
func TestEventsStreamSendsOpenedSentinelOnEmptyWindow(t *testing.T) {
	env := newTestDockerHandler(t, []string{"docker:list"})
	f := newEventsFixture(t, env)

	w := newSyncRecorder()
	c, _ := gin.CreateTestContext(w)
	rctx, rcancel := context.WithCancel(context.Background())
	defer rcancel()
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/docker/events", nil).WithContext(rctx)
	c.Set(middleware.CtxClaims, testClaims(1))
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.handler.EventsStream(c)
	}()
	// 转出事件（占位：夹具里没有任何事件，窗口是空的 —— 这正是病灶场景）。
	waitFor(t, "空窗口流也必须立刻发出 sentinel", 3*time.Second, func() bool {
		return len(w.lines()) >= 1
	})
	if got := w.lines(); len(got) != 1 || got[0] != `{"kind":"opened"}` {
		t.Fatalf("空窗口流的首帧必须恰是 sentinel，实际 %q", got)
	}
	rcancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("端点处理器未随 ctx 断开退出")
	}
}

// exit_code 透传：die 事件的退出码必须原样出现在流行上（前端红染事实源）；
// nil（非 die / 旧 agent）**不出现在行里**（omitempty —— 消费侧不显示不猜）。
func TestEventsStreamCarriesExitCode(t *testing.T) {
	env := newTestDockerHandler(t, []string{"docker:list"})
	f := newEventsFixture(t, env)
	const sid = "sess-events-00000007"

	c0 := f.mgr.Subscribe()
	waitFor(t, "常驻订阅指令已下发", 3*time.Second, func() bool { return len(f.sender.cmdRefs()) == 1 })
	f.mgr.OnEventsResult(7, &agentproto.DockerCmdResult{Ref: f.sender.cmdRefs()[0], OK: true, SessionID: sid})

	code := int32(137)
	raw, _ := json.Marshal(agentproto.DockerEventItem{
		T: 1790000000000, Type: "container", Action: "die", ActorName: "web", ExitCode: &code,
	})
	if err := f.mgr.DeliverDockerFrame(context.Background(), 7,
		&agentproto.DockerFrame{SessionID: sid, Seq: 1, Data: append(raw, '\n')}); err != nil {
		t.Fatal(err)
	}

	w := newSyncRecorder()
	c, _ := gin.CreateTestContext(w)
	rctx, rcancel := context.WithCancel(context.Background())
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/docker/events", nil).WithContext(rctx)
	c.Set(middleware.CtxClaims, testClaims(1))
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.handler.EventsStream(c)
	}()
	waitFor(t, "带退出码的行已写出", 3*time.Second, func() bool { return len(w.lines()) >= 2 })
	rcancel()
	<-done
	c0.Close()

	lines := w.lines()
	var got struct {
		Action   string `json:"action"`
		ExitCode *int32 `json:"exit_code"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &got); err != nil {
		t.Fatalf("末行不是合法 JSON: %v (%q)", err, lines[len(lines)-1])
	}
	if got.Action != "die" || got.ExitCode == nil || *got.ExitCode != 137 {
		t.Fatalf("die 的退出码必须原样透传: %+v", got)
	}
}

// 历史查询端点：过滤 / 排序 / 游标分页 / total 的口径全在这一条上钉住（管理器
// 侧的窗口裁剪与去重键在 dockerevents 的单测里）。
func TestEventsHistoryQueryFiltersAndCursor(t *testing.T) {
	env := newTestDockerHandler(t, []string{"docker:list"})
	f := newEventsFixture(t, env)
	const sid = "sess-events-00000007"

	c0 := f.mgr.Subscribe()
	waitFor(t, "常驻订阅指令已下发", 3*time.Second, func() bool { return len(f.sender.cmdRefs()) == 1 })
	f.mgr.OnEventsResult(7, &agentproto.DockerCmdResult{Ref: f.sender.cmdRefs()[0], OK: true, SessionID: sid})

	// 五条事件：t 递增（历史应按 t 降序返回），类型/主体名各不相同。
	emitAt := func(seq uint64, at int64, typ, action, name string) {
		raw, _ := json.Marshal(agentproto.DockerEventItem{T: at, Type: typ, Action: action, ActorName: name})
		if err := f.mgr.DeliverDockerFrame(context.Background(), 7,
			&agentproto.DockerFrame{SessionID: sid, Seq: seq, Data: append(raw, '\n')}); err != nil {
			t.Fatal(err)
		}
	}
	emitAt(1, 1790000001000, "container", "start", "web")
	emitAt(2, 1790000002000, "container", "die", "web")
	emitAt(3, 1790000003000, "image", "pull", "redis:7")
	emitAt(4, 1790000004000, "volume", "create", "data-vol")
	emitAt(5, 1790000005000, "container", "restart", "api")
	c0.Close()

	get := func(query string) response.DockerEventHistoryResp {
		t.Helper()
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/docker/events/history"+query, nil)
		c.Set(middleware.CtxClaims, testClaims(1))
		f.handler.EventsHistory(c)
		if w.Code != http.StatusOK {
			t.Fatalf("查询 %q 应 200，实际 %d (%s)", query, w.Code, w.Body.String())
		}
		var resp struct {
			Data response.DockerEventHistoryResp `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("响应不是合法 JSON: %v (%s)", err, w.Body.String())
		}
		return resp.Data
	}

	// 无过滤：全部 5 条、t 降序（最新在前）。
	all := get("")
	if all.Total != 5 || len(all.Items) != 5 {
		t.Fatalf("无过滤应给全量 5 条，实际 total=%d items=%d", all.Total, len(all.Items))
	}
	if all.Items[0].Action != "restart" || all.Items[4].Action != "start" {
		t.Fatalf("必须按 t 降序（最新在前）: %+v", all.Items)
	}
	if all.Items[0].HostID != 7 || all.Items[0].Hostname != "alpha" {
		t.Fatalf("归属必须随行注入: %+v", all.Items[0])
	}

	// 类型过滤。
	containers := get("?type=container")
	if containers.Total != 3 || len(containers.Items) != 3 {
		t.Fatalf("container 过滤应 3 条，实际 total=%d items=%d", containers.Total, len(containers.Items))
	}
	// 关键字过滤（大小写不敏感，主体名子串）。
	web := get("?keyword=WEB")
	if web.Total != 2 {
		t.Fatalf("keyword=WEB 应命中 2 条（大小写不敏感），实际 %d", web.Total)
	}
	// 主机过滤（这台夹具只有 7）：命中；不存在的主机：空数组 + total 0。
	if got := get("?hostId=7"); got.Total != 5 {
		t.Fatalf("hostId=7 应 5 条，实际 %d", got.Total)
	}
	empty := get("?hostId=9")
	if empty.Total != 0 || len(empty.Items) != 0 {
		t.Fatalf("不存在的主机应给空数组，实际 total=%d items=%d", empty.Total, len(empty.Items))
	}

	// 游标分页：limit=2 翻三页 —— 不重不漏、total 恒为全量、末页无游标。
	seen := []string{}
	cursor := ""
	pages := 0
	for {
		q := "?limit=2"
		if cursor != "" {
			q += "&cursor=" + cursor
		}
		page := get(q)
		pages++
		if page.Total != 5 {
			t.Fatalf("翻页期间 total 必须恒为全量 5，实际 %d", page.Total)
		}
		for _, it := range page.Items {
			seen = append(seen, it.Action)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
		if pages > 5 {
			t.Fatal("游标分页未收敛（NextCursor 自循环）")
		}
	}
	want := []string{"restart", "create", "pull", "die", "start"}
	if len(seen) != len(want) {
		t.Fatalf("分页后应恰好 5 条，实际 %d: %v", len(seen), seen)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("分页顺序必须与单页一致: got %v want %v", seen, want)
		}
	}
	if pages != 3 {
		t.Fatalf("limit=2 应三页翻完，实际 %d 页", pages)
	}

	// 非法参数：类型 / 游标都 400（结论句而不是静默忽略）。
	for _, q := range []string{"?type=banana", "?cursor=not-a-cursor", "?cursor=123"} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/docker/events/history"+q, nil)
		c.Set(middleware.CtxClaims, testClaims(1))
		f.handler.EventsHistory(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("非法参数 %q 必须 400，实际 %d (%s)", q, w.Code, w.Body.String())
		}
	}
}

// 历史查询未装配：500 语义（与流端点同款降级）。
func TestEventsHistoryNotWired(t *testing.T) {
	env := newTestDockerHandler(t, []string{"docker:list"})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/docker/events/history", nil)
	c.Set(middleware.CtxClaims, testClaims(1))
	env.handler.EventsHistory(c)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("未装配必须 500，实际 %d (%s)", w.Code, w.Body.String())
	}
}

// 未装配：路由仍在但管理器缺失 → 500 语义（与其余流端点一致的降级）。
func TestEventsStreamNotWired(t *testing.T) {
	env := newTestDockerHandler(t, []string{"docker:list"})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/docker/events", nil)
	c.Set(middleware.CtxClaims, testClaims(1))
	env.handler.EventsStream(c)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("未装配必须 500，实际 %d (%s)", w.Code, w.Body.String())
	}
}
