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

// ── stats 实时流（container:stats，监控面）────────────────────────────────

// statsSampleOf 编一个协议样本（字段逐项显式给出，断言时逐项比对）。
func statsSampleOf(t *testing.T, cpu, memUse, memLimit, rx, tx float64) agentproto.DockerStatsSample {
	t.Helper()
	return agentproto.DockerStatsSample{
		T: 1790000000000, CPUPercent: cpu, MemUsageMB: memUse, MemLimitMB: memLimit,
		NetRXBytesSec: rx, NetTXBytesSec: tx,
	}
}

// decodeStatsFrame 把一帧的 data 解析成**恰好一行**的样本（JSON 行契约的守卫）。
func decodeStatsFrame(t *testing.T, f *agentproto.DockerFrame) agentproto.DockerStatsSample {
	t.Helper()
	line := strings.TrimSuffix(string(f.Data), "\n")
	if strings.Contains(line, "\n") {
		t.Fatalf("一帧必须恰含一个样本行，实际 %q", f.Data)
	}
	var s agentproto.DockerStatsSample
	if err := json.Unmarshal([]byte(line), &s); err != nil {
		t.Fatalf("帧 data 必须是 JSON 样本行: %v (%q)", err, f.Data)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("样本必须过协议校验: %v", err)
	}
	return s
}

// 会话建立：target 原样传给 API、结果句柄合法、槽位被占；**首帧立即到**（不需
// 推进任何时钟 —— stats 的整形间隔为 0，daemon 首样本随到随发，抽屉打开即有数据）。
func TestStreamExecutorStatsFirstFrameImmediate(t *testing.T) {
	ch := make(chan StatsSample, 2)
	cl := &fakeCloser{}
	api := &stubAPI{statsStream: ch, statsCloser: cl}
	// 首样本**预先就位**：模拟「daemon 连接建立后立刻给当前读数」。
	ch <- StatsSample{CPUPercent: 12.34, MemUsageMB: 512.5, MemLimitMB: 1024}

	sink := &frameSink{}
	m, _ := newTestSessions(sink, streamIdleTimeout)
	e := NewStreamExecutor(api, m)

	if _, err := e.Do(context.Background(), &agentproto.DockerCmd{Ref: "1",
		Action:  agentproto.DockerActionContainerStats,
		Options: agentproto.DockerCmdOptions{Target: "mysql"}}); err != nil {
		t.Fatal(err)
	}
	sid := e.takeSessionID()
	if !agentproto.IsDockerSessionID(sid) {
		t.Fatalf("会话 id 必须过协议校验: %q", sid)
	}
	if calls := api.statsCalls(); len(calls) != 1 || calls[0] != "mysql" {
		t.Fatalf("stats 流调用不符: %v", calls)
	}

	// 不推进时钟：首帧必须在真实的一小段等待内到达（样本即帧，不等 1s 采样周期）。
	waitFor(t, "首帧立即到达", func() bool { return sink.count() >= 1 })
	frames := sink.all()
	if len(frames) != 1 {
		t.Fatalf("此刻应恰好一帧，实际 %d", len(frames))
	}
	s := decodeStatsFrame(t, frames[0])
	want := statsSampleOf(t, 12.34, 512.5, 1024, 0, 0)
	if s != want {
		t.Fatalf("首帧样本不符: %+v vs %+v", s, want)
	}
	if s.T <= 0 {
		t.Fatalf("样本必须带采样时刻: %+v", s)
	}

	// 收尾：关闭数据源（容器停止）→ eof 帧 → 槽位释放。
	close(ch)
	waitFor(t, "eof 帧", func() bool {
		fs := sink.all()
		return len(fs) > 0 && fs[len(fs)-1].EOF
	})
	waitFor(t, "槽位释放", func() bool { return m.count() == 0 })
}

// 采样节奏：样本逐个到达（daemon 的 1s 节奏）→ 一帧恰一行、seq 单调、值逐项保真；
// 通道关闭即 eof。
func TestStreamExecutorStatsSampleCadence(t *testing.T) {
	ch := make(chan StatsSample, 4)
	api := &stubAPI{statsStream: ch}

	sink := &frameSink{}
	m, _ := newTestSessions(sink, streamIdleTimeout)
	e := NewStreamExecutor(api, m)
	if _, err := e.Do(context.Background(), &agentproto.DockerCmd{Ref: "1",
		Action:  agentproto.DockerActionContainerStats,
		Options: agentproto.DockerCmdOptions{Target: "mysql"}}); err != nil {
		t.Fatal(err)
	}
	ch <- StatsSample{CPUPercent: 1, MemUsageMB: 10, MemLimitMB: 20,
		NetRXBytesSec: 100, NetTXBytesSec: 50}
	waitFor(t, "样本一成帧", func() bool { return sink.count() >= 1 })
	ch <- StatsSample{CPUPercent: 2, MemUsageMB: 11, MemLimitMB: 20,
		NetRXBytesSec: 300, NetTXBytesSec: 75}
	waitFor(t, "样本二成帧", func() bool { return sink.count() >= 2 })
	frames := sink.all()
	if len(frames) != 2 {
		t.Fatalf("两个样本应成两帧，实际 %d", len(frames))
	}
	if frames[0].Seq != 1 || frames[1].Seq != 2 {
		t.Fatalf("seq 必须从 1 单调递增: %d %d", frames[0].Seq, frames[1].Seq)
	}
	if got := decodeStatsFrame(t, frames[0]); got != statsSampleOf(t, 1, 10, 20, 100, 50) {
		t.Fatalf("样本 1 不符: %+v", got)
	}
	if got := decodeStatsFrame(t, frames[1]); got != statsSampleOf(t, 2, 11, 20, 300, 75) {
		t.Fatalf("样本 2 不符: %+v", got)
	}

	close(ch)
	waitFor(t, "eof 与槽位释放", func() bool {
		fs := sink.all()
		return len(fs) > 0 && fs[len(fs)-1].EOF && m.count() == 0
	})
}

// 取消即停：cancel 控制帧 → 上游被关闭、槽位立刻释放、随后到达的样本不再成帧。
func TestStreamExecutorStatsCancelStops(t *testing.T) {
	ch := make(chan StatsSample, 4)
	cl := &fakeCloser{}
	api := &stubAPI{statsStream: ch, statsCloser: cl}
	sink := &frameSink{}
	m, _ := newTestSessions(sink, streamIdleTimeout)
	e := NewStreamExecutor(api, m)
	if _, err := e.Do(context.Background(), &agentproto.DockerCmd{Ref: "1",
		Action:  agentproto.DockerActionContainerStats,
		Options: agentproto.DockerCmdOptions{Target: "mysql"}}); err != nil {
		t.Fatal(err)
	}
	sid := e.takeSessionID()
	ch <- StatsSample{CPUPercent: 1}
	waitFor(t, "首帧", func() bool { return sink.count() >= 1 })

	m.OnFrame(&agentproto.CoreDockerFrame{SessionID: sid, Op: agentproto.DockerFrameOpCancel})
	waitFor(t, "cancel 关闭上游", func() bool { return cl.isClosed() })
	waitFor(t, "cancel 释放槽位", func() bool { return m.count() == 0 })
	// 迟到样本：写会话不再是 op，但帧计数不得再长。
	ch <- StatsSample{CPUPercent: 99}
	time.Sleep(50 * time.Millisecond)
	if got := sink.count(); got != 1 {
		t.Fatalf("cancel 后不得再有帧，实际 %d", got)
	}
	close(ch) // 让 copyStats 的读循环退出（会话已 teardown，markEOF 是空操作）
}

// 打开失败：回结论句、不建会话、不占槽位。
func TestStreamExecutorStatsOpenFailure(t *testing.T) {
	api := &stubAPI{statsStreamErr: io.ErrUnexpectedEOF}
	sink := &frameSink{}
	m, _ := newTestSessions(sink, streamIdleTimeout)
	e := NewStreamExecutor(api, m)
	_, err := e.Do(context.Background(), &agentproto.DockerCmd{Ref: "1",
		Action:  agentproto.DockerActionContainerStats,
		Options: agentproto.DockerCmdOptions{Target: "mysql"}})
	var ee *ExecError
	if !errors.As(err, &ee) || ee.Msg != "读取容器性能数据失败" {
		t.Fatalf("打开失败必须回结论句，实际 %v", err)
	}
	if m.count() != 0 {
		t.Fatalf("失败的会话不得占槽位，实际 %d", m.count())
	}
}

// Runtime 全链路：stats 指令 → result 带 session_id（协议校验通过）→ cancel 释放槽位。
func TestRuntimeStatsCommandCarriesSessionID(t *testing.T) {
	api := &stubAPI{statsStream: make(chan StatsSample)}
	sink := &stateSink{}
	frames := &frameSink{}
	r := New(Deps{StateDir: t.TempDir(), SendState: sink.state, SendResult: sink.result,
		SendFrame: frames.send, Log: testLogger(), API: api,
		Now: func() time.Time { return time.Unix(1790000000, 0) }})

	r.OnCmd(&agentproto.DockerCmd{Ref: "1", Action: agentproto.DockerActionContainerStats,
		Options: agentproto.DockerCmdOptions{Target: "mysql"}})
	r.dispatcher().ExecuteNow(context.Background(), "1")
	waitFor(t, "stats 结果", func() bool { return sink.resultCount() == 1 })
	res := sink.lastResult(t)
	if !res.OK {
		t.Fatalf("stats 建立会话必须成功: %+v", res)
	}
	if !agentproto.IsDockerSessionID(res.SessionID) {
		t.Fatalf("result.session_id 必须过协议校验: %q", res.SessionID)
	}
	if err := res.Validate(); err != nil {
		t.Fatalf("带 session_id 的结果必须语义合法: %v", err)
	}
	if r.sessions.count() != 1 {
		t.Fatalf("stats 会话必须占住槽位，实际 %d", r.sessions.count())
	}

	r.OnFrame(&agentproto.CoreDockerFrame{SessionID: res.SessionID, Op: agentproto.DockerFrameOpCancel})
	waitFor(t, "cancel 释放槽位", func() bool { return r.sessions.count() == 0 })
}
