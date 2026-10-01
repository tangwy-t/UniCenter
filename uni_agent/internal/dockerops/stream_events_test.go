package dockerops

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── 事件流（docker:events，core 的常驻订阅）────────────────────────────────

// decodeEventFrame 把一帧的 data 解析成**恰好一行**的事件记录（JSON 行契约的守卫）。
func decodeEventFrame(t *testing.T, f *agentproto.DockerFrame) agentproto.DockerEventItem {
	t.Helper()
	line := strings.TrimSuffix(string(f.Data), "\n")
	if strings.Contains(line, "\n") {
		t.Fatalf("一帧必须恰含一个事件行，实际 %q", f.Data)
	}
	var e agentproto.DockerEventItem
	if err := json.Unmarshal([]byte(line), &e); err != nil {
		t.Fatalf("帧 data 必须是 JSON 事件行: %v (%q)", err, f.Data)
	}
	if err := e.Validate(); err != nil {
		t.Fatalf("事件必须过协议校验: %v", err)
	}
	return e
}

// 建立与推送：host 级指令（无 target）打开订阅、逐事件成帧（记录即帧、seq 单调）；
// 字段逐项保真（T 是采集时刻而非 daemon 挂钟）。
func TestStreamExecutorEventsPushesFrames(t *testing.T) {
	ch := make(chan EventItem, 4)
	api := &stubAPI{eventsStream: ch}

	sink := &frameSink{}
	m, _ := newTestSessions(sink, streamIdleTimeout)
	e := NewStreamExecutor(api, m)
	if _, err := e.Do(context.Background(), &agentproto.DockerCmd{Ref: "1",
		Action: agentproto.DockerActionEvents}); err != nil {
		t.Fatal(err)
	}
	sid := e.takeSessionID()
	if !agentproto.IsDockerSessionID(sid) {
		t.Fatalf("会话 id 必须过协议校验: %q", sid)
	}

	ch <- EventItem{Type: "container", Action: "start", ActorName: "redis", ActorID: "ab12"}
	waitFor(t, "事件一成帧", func() bool { return sink.count() >= 1 })
	ch <- EventItem{Type: "image", Action: "tag", ActorName: "mysql:8.0", ActorID: "sha256:beef"}
	waitFor(t, "事件二成帧", func() bool { return sink.count() >= 2 })

	frames := sink.all()
	if len(frames) != 2 {
		t.Fatalf("两条事件应成两帧，实际 %d", len(frames))
	}
	if frames[0].Seq != 1 || frames[1].Seq != 2 {
		t.Fatalf("seq 必须从 1 单调递增: %d %d", frames[0].Seq, frames[1].Seq)
	}
	e1 := decodeEventFrame(t, frames[0])
	if e1.Type != "container" || e1.Action != "start" || e1.ActorName != "redis" || e1.ActorID != "ab12" {
		t.Fatalf("事件 1 不符: %+v", e1)
	}
	if e1.T != 1790000000000 {
		t.Fatalf("T 必须是会话时钟的采集时刻: %d", e1.T)
	}
	e2 := decodeEventFrame(t, frames[1])
	if e2.Type != "image" || e2.Action != "tag" {
		t.Fatalf("事件 2 不符: %+v", e2)
	}

	// 订阅是常驻的：不因「推进时钟超过空闲超时」而收摊（无事件 ≠ 被遗忘）。
	close(ch)
	waitFor(t, "eof 帧", func() bool {
		fs := sink.all()
		return len(fs) > 0 && fs[len(fs)-1].EOF
	})
	waitFor(t, "槽位释放", func() bool { return m.count() == 0 })
}

// 非法记录被 agent 拦下（协议白名单是最后一道）：外部动作/带路径形态进不了线，
// 合法事件照常成帧。
func TestStreamExecutorEventsDropsInvalid(t *testing.T) {
	ch := make(chan EventItem, 4)
	api := &stubAPI{eventsStream: ch}

	sink := &frameSink{}
	m, _ := newTestSessions(sink, streamIdleTimeout)
	e := NewStreamExecutor(api, m)
	if _, err := e.Do(context.Background(), &agentproto.DockerCmd{Ref: "1",
		Action: agentproto.DockerActionEvents}); err != nil {
		t.Fatal(err)
	}

	ch <- EventItem{Type: "service", Action: "create"} // 订阅范围外（filter 被绕过/版本漂移）
	time.Sleep(30 * time.Millisecond)
	if sink.count() != 0 {
		t.Fatalf("非法类型不得成帧，实际 %d 帧", sink.count())
	}
	ch <- EventItem{Type: "container", Action: "explode"}
	time.Sleep(30 * time.Millisecond)
	if sink.count() != 0 {
		t.Fatalf("白名单外动作不得成帧，实际 %d 帧", sink.count())
	}
	ch <- EventItem{Type: "container", Action: "die", ActorName: "redis", ActorID: "ab"}
	waitFor(t, "合法事件成帧", func() bool { return sink.count() >= 1 })
	if e1 := decodeEventFrame(t, sink.all()[0]); e1.Action != "die" {
		t.Fatalf("合法事件不符: %+v", e1)
	}

	close(ch)
	waitFor(t, "槽位释放", func() bool {
		fs := sink.all()
		return len(fs) > 0 && fs[len(fs)-1].EOF && m.count() == 0
	})
}

// 取消即停：cancel 控制帧 → 上游关闭、槽位立刻释放、随后到达的事件不再成帧 ——
// 「客户端断开清理」的纪律照 stats。
func TestStreamExecutorEventsCancelStops(t *testing.T) {
	ch := make(chan EventItem, 4)
	cl := &fakeCloser{}
	api := &stubAPI{eventsStream: ch, eventsCloser: cl}
	sink := &frameSink{}
	m, _ := newTestSessions(sink, streamIdleTimeout)
	e := NewStreamExecutor(api, m)
	if _, err := e.Do(context.Background(), &agentproto.DockerCmd{Ref: "1",
		Action: agentproto.DockerActionEvents}); err != nil {
		t.Fatal(err)
	}
	sid := e.takeSessionID()
	ch <- EventItem{Type: "container", Action: "start"}
	waitFor(t, "首帧", func() bool { return sink.count() >= 1 })

	m.OnFrame(&agentproto.CoreDockerFrame{SessionID: sid, Op: agentproto.DockerFrameOpCancel})
	waitFor(t, "cancel 关闭上游", func() bool { return cl.isClosed() })
	waitFor(t, "cancel 释放槽位", func() bool { return m.count() == 0 })
	ch <- EventItem{Type: "container", Action: "stop"}
	time.Sleep(50 * time.Millisecond)
	if got := sink.count(); got != 1 {
		t.Fatalf("cancel 后不得再有帧，实际 %d", got)
	}
	close(ch)
}

// 常驻豁免：平静主机（没有任何事件）不因空闲超时收摊 —— 10 分钟空闲清退是
// 「被遗忘的用户终端/卡死日志源」的纪律，对 core 的常驻订阅不成立。
func TestStreamExecutorEventsExemptFromIdleTimeout(t *testing.T) {
	api := &stubAPI{eventsStream: make(chan EventItem), eventsCloser: &fakeCloser{}}
	sink := &frameSink{}
	m, clk := newTestSessions(sink, streamIdleTimeout)
	e := NewStreamExecutor(api, m)
	if _, err := e.Do(context.Background(), &agentproto.DockerCmd{Ref: "1",
		Action: agentproto.DockerActionEvents}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "会话建立", func() bool { return m.count() == 1 })

	clk.Advance(2 * streamIdleTimeout)
	waitFor(t, "空闲判定窗口推进", func() bool { return true })
	if m.count() != 1 {
		t.Fatalf("常驻会话不得被空闲清退，实际槽位 %d", m.count())
	}
	if sink.count() != 0 {
		t.Fatalf("空闲推进后不得发出任何帧，实际 %d", sink.count())
	}
}

// 独立槽位：3 条用户流占满也不挡常驻订阅；反过来它也不占用户槽位的账；
// 第二条 events 订阅被拒（每台设备至多一条）。
func TestStreamExecutorEventsDedicatedSlot(t *testing.T) {
	api := &stubAPI{eventsStream: make(chan EventItem), eventsCloser: &fakeCloser{}}
	sink := &frameSink{}
	m, _ := newTestSessions(sink, streamIdleTimeout)
	e := NewStreamExecutor(api, m)

	// 占满 3 条用户流槽位。
	for range 3 {
		if _, err := m.open(streamLog); err != nil {
			t.Fatalf("用户流应能占满 3 条: %v", err)
		}
	}
	if _, err := e.Do(context.Background(), &agentproto.DockerCmd{Ref: "1",
		Action: agentproto.DockerActionEvents}); err != nil {
		t.Fatalf("用户流占满时 events 仍应建立（独立槽位）: %v", err)
	}
	if m.count() != 4 {
		t.Fatalf("槽位应为 3 用户 + 1 events，实际 %d", m.count())
	}
	// 第二条 events 订阅：拒绝（专享槽位只有一条）。
	_, err := e.Do(context.Background(), &agentproto.DockerCmd{Ref: "2",
		Action: agentproto.DockerActionEvents})
	var ee *ExecError
	if !errors.As(err, &ee) || ee.Msg != "事件流订阅已存在" {
		t.Fatalf("重复订阅必须回结论句，实际 %v", err)
	}
	if m.count() != 4 {
		t.Fatalf("被拒的订阅不得占槽位，实际 %d", m.count())
	}
}

// 打开失败：回结论句、不建会话、不占槽位（照 stats 的纪律）。
func TestStreamExecutorEventsOpenFailure(t *testing.T) {
	api := &stubAPI{eventsErr: io.ErrUnexpectedEOF}
	sink := &frameSink{}
	m, _ := newTestSessions(sink, streamIdleTimeout)
	e := NewStreamExecutor(api, m)
	_, err := e.Do(context.Background(), &agentproto.DockerCmd{Ref: "1",
		Action: agentproto.DockerActionEvents})
	var ee *ExecError
	if !errors.As(err, &ee) || ee.Msg != "订阅 Docker 事件失败" {
		t.Fatalf("打开失败必须回结论句，实际 %v", err)
	}
	if m.count() != 0 {
		t.Fatalf("失败的订阅不得占槽位，实际 %d", m.count())
	}
}

// Runtime 全链路：events 指令 → result 带 session_id（协议校验通过）→ cancel 释放槽位。
func TestRuntimeEventsCommandCarriesSessionID(t *testing.T) {
	api := &stubAPI{eventsStream: make(chan EventItem), eventsCloser: &fakeCloser{}}
	sink := &stateSink{}
	frames := &frameSink{}
	r := New(Deps{StateDir: t.TempDir(), SendState: sink.state, SendResult: sink.result,
		SendFrame: frames.send, Log: testLogger(), API: api,
		Now: func() time.Time { return time.Unix(1790000000, 0) }})

	r.OnCmd(&agentproto.DockerCmd{Ref: "1", Action: agentproto.DockerActionEvents})
	r.dispatcher().ExecuteNow(context.Background(), "1")
	waitFor(t, "events 结果", func() bool { return sink.resultCount() == 1 })
	res := sink.lastResult(t)
	if !res.OK {
		t.Fatalf("events 建立订阅必须成功: %+v", res)
	}
	if !agentproto.IsDockerSessionID(res.SessionID) {
		t.Fatalf("result.session_id 必须过协议校验: %q", res.SessionID)
	}
	if err := res.Validate(); err != nil {
		t.Fatalf("带 session_id 的结果必须语义合法: %v", err)
	}

	r.OnFrame(&agentproto.CoreDockerFrame{SessionID: res.SessionID, Op: agentproto.DockerFrameOpCancel})
	waitFor(t, "cancel 释放槽位", func() bool { return r.sessions.count() == 0 })
}

// 连接更替只收常驻：OnConnected 收掉上一段连接的 events 会话（槽位腾出，core 重连后
// 重订不会被「订阅已存在」挡住），用户的 stats 会话不受牵连。
func TestRuntimeOnReconnectedTearsDownEventsOnly(t *testing.T) {
	api := &stubAPI{
		statsStream:  make(chan StatsSample),
		statsCloser:  &fakeCloser{},
		eventsStream: make(chan EventItem),
		eventsCloser: &fakeCloser{},
	}
	sink := &stateSink{}
	frames := &frameSink{}
	r := New(Deps{StateDir: t.TempDir(), SendState: sink.state, SendResult: sink.result,
		SendFrame: frames.send, Log: testLogger(), API: api,
		Now: func() time.Time { return time.Unix(1790000000, 0) }})

	// 一条用户 stats 流。
	r.OnCmd(&agentproto.DockerCmd{Ref: "1", Action: agentproto.DockerActionContainerStats,
		Options: agentproto.DockerCmdOptions{Target: "mysql"}})
	r.dispatcher().ExecuteNow(context.Background(), "1")
	// 一条常驻 events 流。
	r.OnCmd(&agentproto.DockerCmd{Ref: "2", Action: agentproto.DockerActionEvents})
	r.dispatcher().ExecuteNow(context.Background(), "2")
	waitFor(t, "两条会话建立", func() bool { return r.sessions.count() == 2 })

	r.OnConnected()
	waitFor(t, "events 被收掉", func() bool { return r.sessions.count() == 1 })
	// 逐一断言：活下来的必须是 stats（用户会话不受连接更替牵连）。
	r.sessions.mu.Lock()
	var left *streamSession
	for _, s := range r.sessions.sessions {
		left = s
	}
	r.sessions.mu.Unlock()
	if left == nil || left.kind != streamStats {
		t.Fatalf("OnConnected 只该收 events，活下来的却是 %v", left)
	}
}
