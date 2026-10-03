package dockerevents

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agenthub"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstream"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── 测试用的可注入时钟 ──────────────────────────────────────────────────
//
// 退避节奏与结果窗口都必须能被**确定性**驱动：靠 sleep 测等于把阈值押在 CI 的
// 调度抖动上（与 agent 侧 sessions 测试的 fakeClock 同一理由）。

type testClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []testWaiter
}

type testWaiter struct {
	at time.Time
	ch chan time.Time
}

func newTestClock() *testClock { return &testClock{now: time.Unix(1790000000, 0)} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	c.mu.Lock()
	defer c.mu.Unlock()
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.waiters = append(c.waiters, testWaiter{at: c.now.Add(d), ch: ch})
	return ch
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	var fire []testWaiter
	rest := c.waiters[:0]
	for _, w := range c.waiters {
		if !w.at.After(now) {
			fire = append(fire, w)
		} else {
			rest = append(rest, w)
		}
	}
	c.waiters = rest
	c.mu.Unlock()
	for _, w := range fire {
		w.ch <- now
	}
}

// ── 替身 ─────────────────────────────────────────────────────────────────

// senderMsg 是一条下行消息的记录形态（断言「对谁、发了什么」用）。
type senderMsg struct {
	deviceID  uint64
	typ       string
	ref       string
	sessionID string
}

// fakeSender 记录型替身：默认送达成功，errs 里注入的 (device → error) 模拟离线/失败。
type fakeSender struct {
	mu   sync.Mutex
	sent []senderMsg
	errs map[uint64]error
}

func newFakeSender() *fakeSender { return &fakeSender{errs: map[uint64]error{}} }

func (s *fakeSender) SendToDevice(deviceID uint64, msg *agentproto.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := senderMsg{deviceID: deviceID, typ: msg.Type}
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
	// 先记再判错：离线/失败的**尝试**也要被断言看见（退避个数就是它的账）。
	s.sent = append(s.sent, m)
	return s.errs[deviceID]
}

func (s *fakeSender) cmdRefs(deviceID uint64) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, m := range s.sent {
		if m.deviceID == deviceID && m.typ == agentproto.TypeCoreDockerCmd {
			out = append(out, m.ref)
		}
	}
	return out
}

func (s *fakeSender) cancels(deviceID uint64) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, m := range s.sent {
		if m.deviceID == deviceID && m.typ == agentproto.TypeCoreDockerFrame {
			out = append(out, m.sessionID)
		}
	}
	return out
}

// fakeCmdStore 记录型替身：只记 Create 调用（result 回链的挂靠点由测试手工断言）。
type fakeCmdStore struct {
	mu      sync.Mutex
	created []*dockerstate.CmdRecord
}

func (s *fakeCmdStore) Create(_ context.Context, rec *dockerstate.CmdRecord, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.created = append(s.created, rec)
	return nil
}

func (s *fakeCmdStore) records() []*dockerstate.CmdRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*dockerstate.CmdRecord(nil), s.created...)
}

// fakeHosts 态化替身：set 是 docker:hosts 集合，envs 是各家快照（可随时翻 docker_ok）。
type fakeHosts struct {
	mu   sync.Mutex
	envs map[uint64]bool // deviceID → docker_ok
	err  error
}

func newFakeHosts(ok ...uint64) *fakeHosts {
	f := &fakeHosts{envs: map[uint64]bool{}}
	for _, id := range ok {
		f.envs[id] = true
	}
	return f
}

func (f *fakeHosts) Hosts(_ context.Context) ([]uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	ids := make([]uint64, 0, len(f.envs))
	for id := range f.envs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

func (f *fakeHosts) Get(_ context.Context, deviceID uint64) (*dockerstate.Envelope, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ok, exists := f.envs[deviceID]; !exists {
		return nil, nil // 从未上报：不在集合里的主机根本不会被枚举
	} else if !ok {
		return &dockerstate.Envelope{State: agentproto.DockerState{DockerOK: false, Error: "docker 不可用"}}, nil
	}
	return &dockerstate.Envelope{State: agentproto.DockerState{DockerOK: true}}, nil
}

func (f *fakeHosts) setOK(id uint64, ok bool) {
	f.mu.Lock()
	f.envs[id] = ok
	f.mu.Unlock()
}

func (f *fakeHosts) del(id uint64) {
	f.mu.Lock()
	delete(f.envs, id)
	f.mu.Unlock()
}

// ── 主夹具 ───────────────────────────────────────────────────────────────

type mgrEnv struct {
	m      *Manager
	clk    *testClock
	sender *fakeSender
	cmds   *fakeCmdStore
	hosts  *fakeHosts
	online map[uint64]bool
}

func newTestManager(t *testing.T, okHosts ...uint64) *mgrEnv {
	t.Helper()
	clk := newTestClock()
	sender := newFakeSender()
	cmds := &fakeCmdStore{}
	hosts := newFakeHosts(okHosts...)
	online := map[uint64]bool{}
	for _, id := range okHosts {
		online[id] = true
	}
	m := NewManager(Options{
		Now:   clk.Now,
		After: clk.After,
		// 结果等待/对账周期给大值：测试里用 Advance 精确触发，不被周期对账抢跑。
		ResultWait:        time.Hour,
		ReconcileInterval: time.Hour,
	}, sender, cmds, hosts,
		func(_ context.Context, deviceID uint64) string {
			return map[uint64]string{7: "alpha", 9: "beta"}[deviceID]
		}, logger.NewNop())
	m.WithOnline(func(deviceID uint64) bool { return online[deviceID] })
	// ref 全时唯一（跨试例单调）：ref 在真实键空间里必须唯一，靠「当前订阅数」
	// 生成会在退订重订后重复 —— 测试替身就该逼出这个性质。
	m.WithIDGen(func() string { return "179000000" + u64s(testRefSeq.Add(1)) })
	return &mgrEnv{m: m, clk: clk, sender: sender, cmds: cmds, hosts: hosts, online: online}
}

// refSeq 是测试夹具的 ref 序号（见上）。
var testRefSeq atomic.Uint64

func u64s(v uint64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

// dol 跑一轮对账（确定性：不依赖 goroutine）。
func (e *mgrEnv) dol() { e.m.runOnce(context.Background()) }

// ev 编一条测试事件。
func ev(typ, action, name, id string) agentproto.DockerEventItem {
	return agentproto.DockerEventItem{T: 1790000000000, Type: typ, Action: action, ActorName: name, ActorID: id}
}

// frame 编一帧带一条事件记录（会话句柄固定合法形态）。
func frame(sessionID string, seq uint64, items ...agentproto.DockerEventItem) *agentproto.DockerFrame {
	var data []byte
	for _, it := range items {
		b, _ := json.Marshal(it)
		data = append(data, b...)
		data = append(data, '\n')
	}
	return &agentproto.DockerFrame{SessionID: sessionID, Seq: seq, Data: data}
}

// emit 把事件经帧投递进管理器（模拟 agent 上行）。
func (e *mgrEnv) emit(deviceID uint64, sessionID string, seq uint64, items ...agentproto.DockerEventItem) {
	if err := e.m.DeliverDockerFrame(context.Background(), deviceID, frame(sessionID, seq, items...)); err != nil {
		panic(err)
	}
}

func resultOK(deviceID uint64, ref, sessionID string) *agentproto.DockerCmdResult {
	return &agentproto.DockerCmdResult{Ref: ref, OK: true, SessionID: sessionID}
}

func resultFail(deviceID uint64, ref, errMsg string) *agentproto.DockerCmdResult {
	return &agentproto.DockerCmdResult{Ref: ref, Error: errMsg}
}

// nextWithTimeout 从客户端读下一条事件（带真实时钟上限，防死等）。
func nextWithTimeout(t *testing.T, cl *Client) Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	e, err := cl.Next(ctx)
	if err != nil {
		t.Fatalf("Next 失败: %v", err)
	}
	return e
}

// ── 引用计数生命周期 ────────────────────────────────────────────────────

// 多客户端共享同一组订阅：首连建立、断一些留任、全部断开才清理（cancel 逐台下发、
// 环形缓冲保留给下一个客户端回放）。
func TestRefCountSharedSubscriptionAndCleanup(t *testing.T) {
	env := newTestManager(t, 7, 9)
	c0 := env.m.Subscribe()
	env.dol()
	if got := env.sender.cmdRefs(7); len(got) != 1 {
		t.Fatalf("首客户端必须触发对主机 7 发起订阅，实际 %v", got)
	}
	if got := env.sender.cmdRefs(9); len(got) != 1 {
		t.Fatalf("首客户端必须触发对主机 9 发起订阅，实际 %v", got)
	}
	env.m.OnEventsResult(7, resultOK(7, env.sender.cmdRefs(7)[0], "sess-events-00000007"))
	env.m.OnEventsResult(9, resultOK(9, env.sender.cmdRefs(9)[0], "sess-events-00000009"))
	if env.m.SubscribedHosts() != 2 {
		t.Fatalf("两台都应已订阅，实际 %d", env.m.SubscribedHosts())
	}

	// 第二、三客户端：同享订阅 —— 不再发新指令。
	c1 := env.m.Subscribe()
	c2 := env.m.Subscribe()
	if env.m.SubscriberCount() != 3 {
		t.Fatalf("引用计数应为 3，实际 %d", env.m.SubscriberCount())
	}
	env.dol()
	if got := env.sender.cmdRefs(7); len(got) != 1 {
		t.Fatalf("后连的客户端不得重复订阅主机 7，实际 %v", got)
	}
	// 断两个：订阅全部留任。
	c1.Close()
	c2.Close()
	env.dol()
	if env.m.SubscriberCount() != 1 || env.m.SubscribedHosts() != 2 {
		t.Fatalf("断一部分客户端后订阅必须留任: clients=%d hosts=%d",
			env.m.SubscriberCount(), env.m.SubscribedHosts())
	}

	// 剩最后一个客户端仍在实时扇出。
	env.emit(7, "sess-events-00000007", 1, ev("container", "start", "redis", "ab"))
	got := nextWithTimeout(t, c0)
	if got.Item.Action != "start" || got.DeviceID != 7 || got.Hostname != "alpha" {
		t.Fatalf("扇出事件不符: %+v", got)
	}

	// 最后一个断开：全部取消 + 引用计数归零；环形缓冲保留。
	c0.Close()
	if env.m.SubscriberCount() != 0 {
		t.Fatalf("引用计数必须归零，实际 %d", env.m.SubscriberCount())
	}
	if env.m.SubscribedHosts() != 0 {
		t.Fatalf("最后一个客户端断开后不得留孤儿订阅，实际 %d", env.m.SubscribedHosts())
	}
	if got := env.sender.cancels(7); len(got) != 1 || got[0] != "sess-events-00000007" {
		t.Fatalf("主机 7 必须收到 cancel: %v", got)
	}
	if got := env.sender.cancels(9); len(got) != 1 || got[0] != "sess-events-00000009" {
		t.Fatalf("主机 9 必须收到 cancel: %v", got)
	}

	// 再连：先回放环形缓冲里的旧事件，再进实时（旧订阅已全退，需重新发起）。
	c3 := env.m.Subscribe()
	env.dol()
	if got := env.sender.cmdRefs(7); len(got) != 2 {
		t.Fatalf("再连必须重新订阅主机 7，实际 %v", got)
	}
	re := nextWithTimeout(t, c3)
	if re.Item.Action != "start" {
		t.Fatalf("回放必须给出断开前的事件: %+v", re)
	}
	c3.Close()
}

// 没有客户端时对账**静止**：不发任何指令 —— 「不开面板不打扰主机」的常驻纪律。
func TestNoClientsNoSubscriptions(t *testing.T) {
	env := newTestManager(t, 7)
	env.dol()
	if got := len(env.sender.cmdRefs(7)); got != 0 {
		t.Fatalf("没有客户端时不得发起订阅，实际 %v", env.sender.cmdRefs(7))
	}
}

// ── 环形缓冲回放 ────────────────────────────────────────────────────────

// 回放顺序：按主机升序、每主机内按到达序（跨主机不承诺全序 —— agent 时钟不可信，
// 接收序只在单台内部有意义）。
func TestRingReplayOrderAcrossHosts(t *testing.T) {
	env := newTestManager(t, 9, 7) // 集合无序：排序应在回放时发生
	env.m.Subscribe()
	env.dol()
	env.m.OnEventsResult(9, resultOK(9, env.sender.cmdRefs(9)[0], "sess-events-00000009"))
	env.m.OnEventsResult(7, resultOK(7, env.sender.cmdRefs(7)[0], "sess-events-00000007"))

	env.emit(9, "sess-events-00000009", 1, ev("container", "create", "nine-a", "1"))
	env.emit(7, "sess-events-00000007", 1, ev("container", "stop", "seven-a", "2"))
	env.emit(9, "sess-events-00000009", 2, ev("container", "die", "nine-b", "3"))

	env.clk.Advance(time.Second) // 不推进时钟也该成立，防「靠时间排序」的实现
	cl := env.m.Subscribe()
	var seq []string
	for range 3 {
		got := nextWithTimeout(t, cl)
		seq = append(seq, got.Item.ActorName)
	}
	want := []string{"seven-a", "nine-a", "nine-b"} // 主机 7 < 9；9 内部按到达序
	if len(seq) != len(want) {
		t.Fatalf("回放条数不符: %v", seq)
	}
	for i := range want {
		if seq[i] != want[i] {
			t.Fatalf("回放顺序不符: got %v want %v", seq, want)
		}
	}
	cl.Close()
}

// 每主机只留最近 50 条：环形截断按主机独立记账，一台高频不会挤掉另一台的历史。
func TestRingCapPerHost(t *testing.T) {
	env := newTestManager(t, 7, 9)
	env.m.Subscribe()
	env.dol()
	env.m.OnEventsResult(7, resultOK(7, env.sender.cmdRefs(7)[0], "sess-events-00000007"))
	env.m.OnEventsResult(9, resultOK(9, env.sender.cmdRefs(9)[0], "sess-events-00000009"))
	for i := 0; i < 60; i++ {
		env.emit(7, "sess-events-00000007", uint64(i+1),
			ev("container", "start", "host7", string(rune('a'+i%26))))
	}
	env.emit(9, "sess-events-00000009", 1, ev("container", "start", "nine", "9"))

	env.clk.Advance(time.Second)
	cl := env.m.Subscribe()
	n := 0
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		if _, err := cl.Next(ctx); err != nil {
			cancel()
			break
		}
		cancel()
		n++
	}
	if n != 51 { // 主机 7 的最近 50 条 + 主机 9 的 1 条
		t.Fatalf("回放条数应为 51（每主机环形截断），实际 %d", n)
	}
	cl.Close()
}

// ── result 认领与暂存 ───────────────────────────────────────────────────

// 帧抢在 result 之前到线（agent 建立会话的异步竞态）：先暂存，result 认领后按序补发；
// 蹭进暂存窗口的异会话帧在认领时无声丢弃。
func TestFramesBeforeResultAdmittedInOrder(t *testing.T) {
	env := newTestManager(t, 7)
	cl := env.m.Subscribe()
	env.dol()
	ref := env.sender.cmdRefs(7)[0]

	env.emit(7, "sess-events-00000007", 1, ev("container", "start", "first", "1"))
	env.emit(7, "sess-events-00000077", 1, ev("container", "start", "intruder", "9")) // 用户会话的早到帧
	env.emit(7, "sess-events-00000007", 2, ev("container", "die", "second", "2"))

	env.m.OnEventsResult(7, resultOK(7, ref, "sess-events-00000007"))
	got := nextWithTimeout(t, cl)
	if got.Item.ActorName != "first" {
		t.Fatalf("暂存帧必须按序补发，先到的是 %+v", got.Item)
	}
	got = nextWithTimeout(t, cl)
	if got.Item.ActorName != "second" {
		t.Fatalf("暂存帧第二帧不符: %+v", got.Item)
	}
	// 异会话帧不得出现（它属于用户流）。
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	if _, err := cl.Next(ctx); err == nil {
		t.Fatal("异会话帧不得被认领补发")
	}
	cancel()
	cl.Close()
}

// result 失败/超时：转入退避、暂存清空 —— 重试是幂等的（agent 端槽位只有一条，
// 重复订阅被拒也只是再退避一轮）。
func TestFailedResultBacksOff(t *testing.T) {
	env := newTestManager(t, 7)
	env.m.Subscribe()
	env.dol()
	ref := env.sender.cmdRefs(7)[0]
	env.m.OnEventsResult(7, resultFail(7, ref, "订阅 Docker 事件失败"))
	if env.m.SubscribedHosts() != 1 {
		t.Fatal("失败后订阅账目保留（退避状态），由对账循环重试")
	}
	if got := env.sender.cmdRefs(7); len(got) != 1 {
		t.Fatalf("失败后不得立即重发，实际 %v", got)
	}
	env.clk.Advance(time.Second) // 退避基数 1s
	env.dol()
	if got := env.sender.cmdRefs(7); len(got) != 2 {
		t.Fatalf("退避到期后必须重发订阅，实际 %v", got)
	}
}

// ── 单台失败与重连 ──────────────────────────────────────────────────────

// 一台离线（发送失败）不拖累另一台：失败台退避、健康台照常收事件和扇出。
func TestOneHostFailureDoesNotAffectAnother(t *testing.T) {
	env := newTestManager(t, 7, 9)
	env.sender.errs[9] = agenthub.ErrDeviceOffline
	cl := env.m.Subscribe()
	env.dol()
	env.m.OnEventsResult(7, resultOK(7, env.sender.cmdRefs(7)[0], "sess-events-00000007"))

	if got := env.sender.cmdRefs(9); len(got) != 1 {
		t.Fatalf("离线主机也应尝试过一次订阅，实际 %v", got)
	}
	// 健康台正常扇出。
	env.emit(7, "sess-events-00000007", 1, ev("container", "start", "healthy", "h"))
	got := nextWithTimeout(t, cl)
	if got.Item.ActorName != "healthy" {
		t.Fatalf("健康主机的事件必须照常到达: %+v", got)
	}
	// 失败台在退避窗口内不重发、到期后重发。
	env.dol()
	if got := env.sender.cmdRefs(9); len(got) != 1 {
		t.Fatalf("退避窗口内不得重发，实际 %v", got)
	}
	env.clk.Advance(time.Second)
	env.dol()
	if got := env.sender.cmdRefs(9); len(got) != 2 {
		t.Fatalf("退避到期必须重发，实际 %v", got)
	}
	cl.Close()
}

// 重连后重订：active 会话的主机掉线 → 离线清退（cancel）→ 重连（online 恢复）→
// 退避到期自动重订，新会话正常收帧。
func TestReconnectResubscribes(t *testing.T) {
	env := newTestManager(t, 7)
	env.m.Subscribe()
	env.dol()
	env.m.OnEventsResult(7, resultOK(7, env.sender.cmdRefs(7)[0], "sess-events-00000007"))
	env.emit(7, "sess-events-00000007", 1, ev("container", "start", "before", "1"))

	// 掉线：对账发现 online=false → 退订并转入退避（cancel 刻意不发 —— 连接已断，
	// 发也送不到；agent 侧会话靠发送失败/重连收摊自会）。
	env.online[7] = false
	env.dol()
	if got := env.sender.cancels(7); len(got) != 0 {
		t.Fatalf("离线时 cancel 送不出去，不得空发: %v", got)
	}
	if env.m.SubscribedHosts() != 1 {
		t.Fatal("退避状态仍在对账账目里")
	}
	// 回到在线（agent 重连）：退避到期后自动重订。
	env.online[7] = true
	env.clk.Advance(time.Second)
	env.dol()
	if got := env.sender.cmdRefs(7); len(got) != 2 {
		t.Fatalf("重连后必须重发订阅，实际 %v", got)
	}
	env.m.OnEventsResult(7, resultOK(7, env.sender.cmdRefs(7)[1], "sess-events-00000008"))
	env.emit(7, "sess-events-00000008", 1, ev("container", "start", "after", "2"))
	if env.m.SubscribedHosts() != 1 {
		t.Fatal("重订后账目必须回到 active")
	}
}

// eof 也是单台失败的一种：agent 端订阅自然结束（daemon 重启）→ 退避重订，
// 环形缓冲里的既有历史不受影响；**eof 帧可与末帧数据同帧**（帧契约），最后一笔
// 数据不能因收摊被吞掉。
func TestEOFTriggersResubscribeWithBackoff(t *testing.T) {
	env := newTestManager(t, 7)
	cl := env.m.Subscribe()
	env.dol()
	env.m.OnEventsResult(7, resultOK(7, env.sender.cmdRefs(7)[0], "sess-events-00000007"))
	env.emit(7, "sess-events-00000007", 1, ev("container", "start", "before-eof", "1"))

	eof := frame("sess-events-00000007", 2, ev("container", "die", "last-before-eof", "2"))
	eof.EOF = true
	if err := env.m.DeliverDockerFrame(context.Background(), 7, eof); err != nil {
		t.Fatal(err)
	}
	// 末帧数据必须照常扇出（eof 只是收摊信号，不是丢数据的理由）：队列里先有
	// 「before-eof」（订阅在前），随后必须是 eof 同帧的这笔。
	if got := nextWithTimeout(t, cl); got.Item.ActorName != "before-eof" {
		t.Fatalf("第一笔不符: %+v", got)
	}
	if got := nextWithTimeout(t, cl); got.Item.ActorName != "last-before-eof" {
		t.Fatalf("eof 同帧的末笔数据不得被吞: %+v", got)
	}
	if got := env.sender.cmdRefs(7); len(got) != 1 {
		t.Fatalf("eof 后不得立即重发（须退避），实际 %v", got)
	}
	env.clk.Advance(time.Second)
	env.dol()
	if got := env.sender.cmdRefs(7); len(got) != 2 {
		t.Fatalf("退避到期必须重订，实际 %v", got)
	}
	cl.Close()
}

// ── 主机清单对账 ────────────────────────────────────────────────────────

// 新增可管主机 → 下一轮对账即订阅；设备删除/docker_ok 翻转 → 退订 + 清环形缓冲。
func TestReconcileJoinsAndLeaves(t *testing.T) {
	env := newTestManager(t, 7)
	env.m.Subscribe()
	env.dol()
	env.m.OnEventsResult(7, resultOK(7, env.sender.cmdRefs(7)[0], "sess-events-00000007"))
	env.emit(7, "sess-events-00000007", 1, ev("container", "start", "seven", "7"))

	// 新主机 9 的首帧快照到线：下一轮对账增量订阅。
	env.hosts.setOK(9, true)
	env.online[9] = true
	env.dol()
	if got := env.sender.cmdRefs(9); len(got) != 1 {
		t.Fatalf("新增可管主机必须被增量订阅，实际 %v", got)
	}
	env.m.OnEventsResult(9, resultOK(9, env.sender.cmdRefs(9)[0], "sess-events-00000009"))

	// docker_ok 翻转（daemon 挂掉）：退订 + cancel + 环形缓冲清掉。
	env.hosts.setOK(7, false)
	env.dol()
	if got := env.sender.cancels(7); len(got) != 1 {
		t.Fatalf("docker 不可用的主机必须被退订，实际 %v", got)
	}
	if env.m.SubscribedHosts() != 1 {
		t.Fatalf("只剩主机 9，实际 %d", env.m.SubscribedHosts())
	}
	// 回放不得再出现主机 7 的旧事件。
	cl := env.m.Subscribe()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	if _, err := cl.Next(ctx); err == nil {
		t.Fatal("退出可管清单的主机，其环形缓冲必须清掉")
	}
	cancel()
	cl.Close()

	// 设备删除同理。
	env.hosts.del(9)
	env.dol()
	if env.m.SubscribedHosts() != 0 {
		t.Fatalf("设备删除后必须全量退订，实际 %d", env.m.SubscribedHosts())
	}
	if got := env.sender.cancels(9); len(got) != 1 {
		t.Fatalf("被删设备必须收到 cancel: %v", got)
	}
}

// 枚举失败（Redis 抖动）不得误发退订：一轮读不到 ≠ 主机没了。
func TestReconcileEnumFailureDoesNotUnsubscribe(t *testing.T) {
	env := newTestManager(t, 7)
	env.m.Subscribe()
	env.dol()
	env.m.OnEventsResult(7, resultOK(7, env.sender.cmdRefs(7)[0], "sess-events-00000007"))

	env.hosts.err = errors.New("redis down")
	env.dol()
	if env.m.SubscribedHosts() != 1 {
		t.Fatalf("枚举失败不得退订任何主机，实际 %d", env.m.SubscribedHosts())
	}
	if got := env.sender.cancels(7); len(got) != 0 {
		t.Fatalf("枚举失败不得下发 cancel，实际 %v", got)
	}
	env.hosts.err = nil
}

// ── 帧路由 ──────────────────────────────────────────────────────────────

// fakeFrameSink 是路由测试的可编程替身（命中计数 + 可注入错误）。
type fakeFrameSink struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (s *fakeFrameSink) DeliverDockerFrame(context.Context, uint64, *agentproto.DockerFrame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.err
}

func (s *fakeFrameSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *fakeFrameSink) reset(err error) {
	s.mu.Lock()
	s.calls = 0
	s.err = err
	s.mu.Unlock()
}

// 二段路由：用户注册表认识 → 命中即止（不落第二段）；ErrNotFound → 落第二段；
// 其它错误（归属不符/非法帧）**永不**落第二段 —— 不该换个主人再投一次。
func TestFrameRouterTwoStageRouting(t *testing.T) {
	f := frame("sess-events-00000007", 9, ev("container", "start", "x", "1"))
	primary, fallback := &fakeFrameSink{}, &fakeFrameSink{}

	primary.reset(nil)
	fallback.reset(nil)
	if err := (FrameRouter{Primary: primary, Fallback: fallback}).
		DeliverDockerFrame(context.Background(), 7, f); err != nil {
		t.Fatal(err)
	}
	if primary.count() != 1 || fallback.count() != 0 {
		t.Fatalf("primary 命中即止: primary=%d fallback=%d", primary.count(), fallback.count())
	}

	primary.reset(dockerstream.ErrNotFound)
	fallback.reset(nil)
	if err := (FrameRouter{Primary: primary, Fallback: fallback}).
		DeliverDockerFrame(context.Background(), 7, f); err != nil {
		t.Fatal(err)
	}
	if primary.count() != 1 || fallback.count() != 1 {
		t.Fatalf("ErrNotFound 必须落第二段: primary=%d fallback=%d", primary.count(), fallback.count())
	}

	primary.reset(dockerstream.ErrForbidden)
	fallback.reset(nil)
	err := (FrameRouter{Primary: primary, Fallback: fallback}).
		DeliverDockerFrame(context.Background(), 7, f)
	if !errors.Is(err, dockerstream.ErrForbidden) {
		t.Fatalf("归属不符必须原样上抛: %v", err)
	}
	if fallback.count() != 0 {
		t.Fatalf("归属不符不得落第二段，fallback 被调了 %d 次", fallback.count())
	}

	// 未装 primary：直接走 fallback（装配顺序的容错）。
	fallback.reset(nil)
	if err := (FrameRouter{Fallback: fallback}).
		DeliverDockerFrame(context.Background(), 7, f); err != nil {
		t.Fatal(err)
	}
	if fallback.count() != 1 {
		t.Fatalf("无 primary 时必须直落 fallback: %d", fallback.count())
	}
}

// 管理器自己的帧入口对「不认识的主机」回 ErrNotFound（路由器第二段的判定语义）；
// 对非法帧就地丢弃（已处理，不再让路由器记错误日志）。
func TestManagerDeliverUnknownHostAndInvalidFrame(t *testing.T) {
	env := newTestManager(t, 7)
	if err := env.m.DeliverDockerFrame(context.Background(), 3,
		frame("sess-events-00000003", 1, ev("container", "start", "x", "1"))); !errors.Is(err, dockerstream.ErrNotFound) {
		t.Fatalf("不认识的主机必须回 ErrNotFound: %v", err)
	}
	bad := &agentproto.DockerFrame{SessionID: "short", Seq: 0}
	if err := env.m.DeliverDockerFrame(context.Background(), 7, bad); err != nil {
		t.Fatalf("非法帧处理完成即止，不得让路由器再记一遍: %v", err)
	}
}

// ── 常驻内部消费者（七期·通知联动的关键状态机改动）───────────────────────
//
// 管理器从「全 HTTP 引用计数」变为「双面消费端」：内部消费者不计入 HTTP 面的
// 归零判据 —— console 全断开时订阅保持常开；两个面都空才全量退订。
// HTTP 端点的行为（6b 钉住的 13 条用例）在此之下必须零回退。

// fakeResident 是记录型内部消费者替身（DeliverDockerEvent 只记账，绝不阻塞）。
type fakeResident struct {
	mu  sync.Mutex
	got []Event
}

func (r *fakeResident) DeliverDockerEvent(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, e)
}

func (r *fakeResident) events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.got...)
}

// 内部消费者单独在册即可驱动订阅：HTTP 客户端数为 0，对账照常向可管主机发起订阅，
// 事件以带归属的形态实时到达 —— 「没人看面板」不再等于「没人需要事件流」。
func TestResidentKeepsSubscriptionWithoutClients(t *testing.T) {
	env := newTestManager(t, 7, 9)
	r := &fakeResident{}
	unreg := env.m.RegisterResident("notify", r)
	if env.m.ResidentCount() != 1 {
		t.Fatalf("内部消费者计数应为 1，实际 %d", env.m.ResidentCount())
	}
	env.dol()
	if got := env.sender.cmdRefs(7); len(got) != 1 {
		t.Fatalf("内部消费者必须触发对主机 7 发起订阅，实际 %v", got)
	}
	if got := env.sender.cmdRefs(9); len(got) != 1 {
		t.Fatalf("内部消费者必须触发对主机 9 发起订阅，实际 %v", got)
	}
	env.m.OnEventsResult(7, resultOK(7, env.sender.cmdRefs(7)[0], "sess-events-00000007"))
	env.m.OnEventsResult(9, resultOK(9, env.sender.cmdRefs(9)[0], "sess-events-00000009"))

	env.emit(7, "sess-events-00000007", 1, ev("container", "die", "web", "ab"))
	got := r.events()
	if len(got) != 1 || got[0].DeviceID != 7 || got[0].Hostname != "alpha" || got[0].Item.Action != "die" {
		t.Fatalf("内部消费者应拿到带归属的实时事件: %+v", got)
	}

	// HTTP 客户端接进来再断开：订阅必须留任（内部面还在），也不得发 cancel ——
	// 「看的人走了」不触发退订是常驻语义的核心。
	cl := env.m.Subscribe()
	if env.m.SubscriberCount() != 1 || env.m.ResidentCount() != 1 {
		t.Fatalf("两个面的计数必须独立: clients=%d residents=%d",
			env.m.SubscriberCount(), env.m.ResidentCount())
	}
	cl.Close()
	if env.m.SubscriberCount() != 0 {
		t.Fatalf("HTTP 面归零后客户端计数应为 0，实际 %d", env.m.SubscriberCount())
	}
	if env.m.SubscribedHosts() != 2 {
		t.Fatalf("HTTP 面归零但有内部消费者时订阅必须留任，实际 %d 台", env.m.SubscribedHosts())
	}
	if got := env.sender.cancels(7); len(got) != 0 {
		t.Fatalf("内部消费者在册期间不得下发 cancel: %v", got)
	}

	// 注销内部消费者（此刻 HTTP 面也是 0）：两个面都空了，才轮到全量退订。
	unreg()
	if env.m.ResidentCount() != 0 {
		t.Fatalf("注销后内部消费者计数应为 0，实际 %d", env.m.ResidentCount())
	}
	if env.m.SubscribedHosts() != 0 {
		t.Fatalf("两个面都空必须全量退订，实际 %d 台", env.m.SubscribedHosts())
	}
	if got := env.sender.cancels(7); len(got) != 1 || got[0] != "sess-events-00000007" {
		t.Fatalf("主机 7 必须收到 cancel: %v", got)
	}
	if got := env.sender.cancels(9); len(got) != 1 || got[0] != "sess-events-00000009" {
		t.Fatalf("主机 9 必须收到 cancel: %v", got)
	}
}

// 内部消费者**不做环形回放**：登记前的老事件（活动流意义上的「刚才」）不算实时，
// 通知联动不能把启动前的历史再告警一遍 —— 只收登记后的实时事实。
func TestResidentNoRingReplay(t *testing.T) {
	env := newTestManager(t, 7)
	cl := env.m.Subscribe()
	env.dol()
	env.m.OnEventsResult(7, resultOK(7, env.sender.cmdRefs(7)[0], "sess-events-00000007"))
	env.emit(7, "sess-events-00000007", 1, ev("container", "die", "old", "aa"))

	r := &fakeResident{}
	unreg := env.m.RegisterResident("notify", r)
	env.emit(7, "sess-events-00000007", 2, ev("container", "die", "new", "bb"))
	if got := r.events(); len(got) != 1 || got[0].Item.ActorName != "new" {
		t.Fatalf("内部消费者只应收登记后的实时事件（不得回放环形缓冲）: %+v", got)
	}
	cl.Close()
	unreg()
}

// 注销是**幂等**且按身份生效：同名后登记会替换先登记，先登记的注销函数不再删掉
// 替身 —— 不会出现「旧注销把新消费者误删」的账目漂移。
func TestResidentUnregisterIdempotentAndIdentityScoped(t *testing.T) {
	env := newTestManager(t, 7)
	r1, r2 := &fakeResident{}, &fakeResident{}
	unreg1 := env.m.RegisterResident("notify", r1)
	if env.m.ResidentCount() != 1 {
		t.Fatalf("登记计数应为 1，实际 %d", env.m.ResidentCount())
	}
	unreg2 := env.m.RegisterResident("notify", r2)
	if env.m.ResidentCount() != 1 {
		t.Fatalf("同名替换后计数仍应为 1，实际 %d", env.m.ResidentCount())
	}
	unreg1() // 旧身份：不得删掉新替身
	unreg1() // 幂等：再调一次无副作用
	if env.m.ResidentCount() != 1 {
		t.Fatalf("旧注销不得删掉替身后的消费者，实际 %d", env.m.ResidentCount())
	}
	env.dol()
	if len(env.sender.cmdRefs(7)) == 0 {
		t.Fatal("被替身后的消费者必须仍在驱动订阅")
	}
	unreg2()
	if env.m.ResidentCount() != 0 || env.m.SubscribedHosts() != 0 {
		t.Fatalf("新注销后必须全量退订: residents=%d hosts=%d",
			env.m.ResidentCount(), env.m.SubscribedHosts())
	}
}

// 内部消费者与 HTTP 客户端同链扇出：同一条事件两个面各收一份、顺序一致 ——
// 通知联动看到的正是活动流看到的那条（归属与载荷同一实例）。
func TestResidentAndClientShareFanout(t *testing.T) {
	env := newTestManager(t, 7)
	r := &fakeResident{}
	unreg := env.m.RegisterResident("notify", r)
	cl := env.m.Subscribe()
	env.dol()
	env.m.OnEventsResult(7, resultOK(7, env.sender.cmdRefs(7)[0], "sess-events-00000007"))

	env.emit(7, "sess-events-00000007", 1,
		ev("container", "die", "web", "ab"),
		ev("container", "start", "web", "ab"))
	clientGot := []Item(nil)
	for range 2 {
		e := nextWithTimeout(t, cl)
		clientGot = append(clientGot, itemOf(e))
	}
	rg := r.events()
	if len(rg) != 2 {
		t.Fatalf("内部消费者应收到两条: %+v", rg)
	}
	for i := range 2 {
		if rg[i].Item.Action != clientGot[i].Action || rg[i].Hostname != "alpha" {
			t.Fatalf("两面扇出必须同序同形: resident=%+v client=%+v", rg[i], clientGot[i])
		}
	}
	cl.Close()
	unreg()
}

// Item 是别名以便断言（避免给测试引入协议包外的理解负担）。
type Item = agentproto.DockerEventItem

func itemOf(e Event) Item { return e.Item }

// ── 保留窗口与历史查询（本波：/docker/events/history 的数据面）─────────────
//
// 四组口径逐条钉住：**双限**（容量/时长各自独立生效）、**回放与历史分家**
//（回放只吐最近 ReplayDepth 条，历史看得到整个窗口）、**查询过滤与游标**
//（不重不漏、total 恒为全量）、**同 t 决胜**（全序需要到达序兜底）。

// newWindowManager 起一个窗口参数可精确注入的夹具（其余替身与主夹具同源）。
func newWindowManager(t *testing.T, opts Options, okHosts ...uint64) *mgrEnv {
	t.Helper()
	clk := newTestClock()
	sender := newFakeSender()
	cmds := &fakeCmdStore{}
	hosts := newFakeHosts(okHosts...)
	online := map[uint64]bool{}
	for _, id := range okHosts {
		online[id] = true
	}
	opts.Now = clk.Now
	opts.After = clk.After
	if opts.ResultWait <= 0 {
		opts.ResultWait = time.Hour
	}
	if opts.ReconcileInterval <= 0 {
		opts.ReconcileInterval = time.Hour
	}
	m := NewManager(opts, sender, cmds, hosts,
		func(_ context.Context, deviceID uint64) string {
			return map[uint64]string{7: "alpha", 9: "beta"}[deviceID]
		}, logger.NewNop())
	m.WithOnline(func(deviceID uint64) bool { return online[deviceID] })
	m.WithIDGen(func() string { return "179000000" + u64s(testRefSeq.Add(1)) })
	return &mgrEnv{m: m, clk: clk, sender: sender, cmds: cmds, hosts: hosts, online: online}
}

// evAt 编一条指定时刻的测试事件（主夹具的 ev 固定 t，这里要按 t 排序）。
func evAt(t int64, typ, action, name, id string) agentproto.DockerEventItem {
	return agentproto.DockerEventItem{T: t, Type: typ, Action: action, ActorName: name, ActorID: id}
}

// windowEnv 把两台主机的订阅推到 active（emit 的前置），返回 env。
func windowEnv(t *testing.T, opts Options, hosts ...uint64) *mgrEnv {
	t.Helper()
	env := newWindowManager(t, opts, hosts...)
	env.m.Subscribe()
	env.dol()
	for _, id := range hosts {
		env.m.OnEventsResult(id, resultOK(id, env.sender.cmdRefs(id)[0], sessionsOf(id)))
	}
	return env
}

func sessionsOf(deviceID uint64) string {
	return "sess-events-0000000" + u64s(deviceID)
}

// 双限各自独立生效：容量先到 → 按容量截断；时长先到 → 按时长裁剪（且**静默主机**
// 也照裁 —— 读侧的过滤与写侧的裁剪是同一把尺）。
func TestRetainWindowDualLimits(t *testing.T) {
	env := windowEnv(t, Options{ReplayDepth: 3, RetainDepth: 5, RetainWindow: time.Minute}, 7)
	for i := 1; i <= 8; i++ {
		env.emit(7, sessionsOf(7), uint64(i), evAt(int64(1790000000000+i*1000), "container", "start", "c"+u64s(uint64(i)), "id"))
	}
	page, err := env.m.Query(QueryFilter{}, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 5 || len(page.Items) != 5 {
		t.Fatalf("容量限应为 5 条，实际 total=%d items=%d", page.Total, len(page.Items))
	}
	if page.Items[0].Item.ActorName != "c8" || page.Items[4].Item.ActorName != "c4" {
		t.Fatalf("容量截断必须丢最旧（留最近 5 条）: %s … %s",
			page.Items[0].Item.ActorName, page.Items[4].Item.ActorName)
	}

	// 时长限：推进 61 秒后（不产生新事件 —— 静默主机的窗口不主动清扫），
	// 读侧照样把过期的全部过滤掉。
	env.clk.Advance(61 * time.Second)
	page, err = env.m.Query(QueryFilter{}, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("超时长的条目必须查不到（静默主机亦然），实际 total=%d", page.Total)
	}
	// 定时长判定走 **core 接收时刻**（假时钟推进即过期），不碰事件自带的 t。
	if got := page.Items; len(got) != 0 {
		t.Fatalf("过期条目不得留窗口: %+v", got)
	}
}

// 回放与历史**分家**：回放只吐最近 ReplayDepth 条（打开页面不灌千行），
// 历史查询看得到整个保留窗口。
func TestReplayDepthApartFromRetainDepth(t *testing.T) {
	env := windowEnv(t, Options{ReplayDepth: 2, RetainDepth: 10, RetainWindow: time.Hour}, 7)
	for i := 1; i <= 6; i++ {
		env.emit(7, sessionsOf(7), uint64(i), evAt(int64(1790000000000+i), "container", "start", "c"+u64s(uint64(i)), "id"))
	}
	cl := env.m.Subscribe()
	got := []string{}
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		e, err := cl.Next(ctx)
		cancel()
		if err != nil {
			break
		}
		got = append(got, e.Item.ActorName)
	}
	cl.Close()
	if len(got) != 2 || got[0] != "c5" || got[1] != "c6" {
		t.Fatalf("回放必须只给最近 2 条（c5,c6），实际 %v", got)
	}
	page, err := env.m.Query(QueryFilter{}, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 6 {
		t.Fatalf("历史必须看得到整个窗口的 6 条，实际 %d", page.Total)
	}
}

// 查询过滤：hostId / type / keyword 三项独立收窄（keyword 大小写不敏感，
// 命中主体名 / 主体 id / 动作原文三处任一）。
func TestQueryFilters(t *testing.T) {
	env := windowEnv(t, Options{RetainWindow: time.Hour}, 7, 9)
	env.emit(7, sessionsOf(7), 1, evAt(1790000001000, "container", "die", "web", "abcd1234"))
	env.emit(7, sessionsOf(7), 2, evAt(1790000002000, "image", "pull", "redis:7", "sha256:beef"))
	env.emit(9, sessionsOf(9), 1, evAt(1790000003000, "container", "start", "api", "9999"))

	all, _ := env.m.Query(QueryFilter{}, 0, "")
	if all.Total != 3 {
		t.Fatalf("无过滤应 3 条，实际 %d", all.Total)
	}
	only7, _ := env.m.Query(QueryFilter{HostID: 7}, 0, "")
	if only7.Total != 2 {
		t.Fatalf("hostId=7 应 2 条，实际 %d", only7.Total)
	}
	images, _ := env.m.Query(QueryFilter{Type: "image"}, 0, "")
	if images.Total != 1 || images.Items[0].Item.ActorName != "redis:7" {
		t.Fatalf("type=image 应 1 条，实际 %+v", images)
	}
	byName, _ := env.m.Query(QueryFilter{Keyword: "WEB"}, 0, "")
	if byName.Total != 1 || byName.Items[0].Item.ActorName != "web" {
		t.Fatalf("keyword 必须命中主体名（大小写不敏感），实际 %+v", byName)
	}
	byId, _ := env.m.Query(QueryFilter{Keyword: "BEEF"}, 0, "")
	if byId.Total != 1 || byId.Items[0].Item.ActorName != "redis:7" {
		t.Fatalf("keyword 必须命中主体 id（大小写不敏感），实际 %+v", byId)
	}
	byAction, _ := env.m.Query(QueryFilter{Keyword: "start"}, 0, "")
	if byAction.Total != 1 || byAction.Items[0].Hostname != "beta" {
		t.Fatalf("keyword 必须命中动作原文，实际 %+v", byAction)
	}
	// 组合：先主机、再类型（逐层收窄的语义就在这一条上）。
	combo, _ := env.m.Query(QueryFilter{HostID: 7, Type: "container"}, 0, "")
	if combo.Total != 1 || combo.Items[0].Item.Action != "die" {
		t.Fatalf("组合过滤应 1 条（7 上的 container），实际 %+v", combo)
	}
}

// 游标分页：不重不漏、total 恒为全量、末页无游标；同 t 的并列条目由到达序决胜，
// 不因分页边界重复或丢失。
func TestQueryCursorPagination(t *testing.T) {
	env := windowEnv(t, Options{RetainWindow: time.Hour}, 7)
	// 10 条：其中两条同 t（第 5、6 条），用来钉同 t 决胜。
	for i := 1; i <= 10; i++ {
		at := int64(1790000000000 + i*1000)
		if i == 6 {
			at = 1790000005000 // 与第 5 条同 t
		}
		env.emit(7, sessionsOf(7), uint64(i), evAt(at, "container", "start", "c"+u64s(uint64(i)), "id"))
	}
	seen := []string{}
	cursor := ""
	pages := 0
	for {
		page, err := env.m.Query(QueryFilter{}, 3, cursor)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		if page.Total != 10 {
			t.Fatalf("翻页期间 total 必须恒为全量 10，实际 %d", page.Total)
		}
		for _, e := range page.Items {
			seen = append(seen, e.Item.ActorName)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
		if pages > 6 {
			t.Fatal("游标分页未收敛")
		}
	}
	if pages != 4 {
		t.Fatalf("limit=3 翻 10 条应 4 页，实际 %d", pages)
	}
	if len(seen) != 10 {
		t.Fatalf("分页合计必须恰 10 条（不重不漏），实际 %d: %v", len(seen), seen)
	}
	want := []string{"c10", "c9", "c8", "c7", "c6", "c5", "c4", "c3", "c2", "c1"}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("分页顺序必须与单页一致（同 t 按到达序降序）: got %v want %v", seen, want)
		}
	}
	// 游标越过窗口尽头（位置取最旧一条的 t 与更小的到达序）：空页且不再给游标
	//（不给原地打转的入口）；total 仍是全量（「窗口里有多少」与「这页拿到什么」
	// 是两个事实）。
	tail, err := env.m.Query(QueryFilter{}, 3, "1790000001000:0")
	if err != nil {
		t.Fatal(err)
	}
	if len(tail.Items) != 0 || tail.NextCursor != "" || tail.Total != 10 {
		t.Fatalf("游标越过尽头应空页 + 无游标 + total 全量，实际 %+v", tail)
	}
}

// 非法游标：管理器给出可判别的错误（HTTP 层折成 400），不静默当成「从头开始」
// —— 静默会把「翻页」悄悄变成「反复看第一页」。
func TestQueryBadCursor(t *testing.T) {
	env := windowEnv(t, Options{RetainWindow: time.Hour}, 7)
	for _, bad := range []string{"abc", "123", ":5", "x:y", "12:", "12:9999999999999999999999"} {
		if _, err := env.m.Query(QueryFilter{}, 0, bad); !errors.Is(err, ErrBadCursor) {
			t.Fatalf("游标 %q 必须报 ErrBadCursor，实际 %v", bad, err)
		}
	}
	// 合法游标（哪怕只剩空页）不得报错。
	if _, err := env.m.Query(QueryFilter{}, 0, "1790000000000:1"); err != nil {
		t.Fatalf("合法游标不得报错: %v", err)
	}
}
