package dockerops

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// collectSink 收集回传的结果（替代 transport 的上行出口）。
type collectSink struct {
	mu    sync.Mutex
	got   []*agentproto.DockerCmdResult
	block time.Duration
}

func (s *collectSink) send(r *agentproto.DockerCmdResult) error {
	if s.block > 0 {
		time.Sleep(s.block)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, r)
	return nil
}

func (s *collectSink) all() []*agentproto.DockerCmdResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*agentproto.DockerCmdResult(nil), s.got...)
}

// fakeExec 是一个可控执行器。
type fakeExec struct {
	mu      sync.Mutex
	calls   []string
	payload []byte
	err     error
	sleep   time.Duration
}

func (f *fakeExec) Do(ctx context.Context, cmd *agentproto.DockerCmd) ([]byte, error) {
	if f.sleep > 0 {
		select {
		case <-time.After(f.sleep):
		case <-ctx.Done():
			return nil, &ExecError{Msg: "执行超时"}
		}
	}
	f.mu.Lock()
	f.calls = append(f.calls, cmd.Action+"|"+cmd.Options.Target)
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return f.payload, nil
}

func (f *fakeExec) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// 未知 action 与「未实现 action」必须回**不同**的结论句：前者是 core 写错了（要改代码），
// 后者只是这一版 agent 还没做（要升级 agent）—— 糊成一句会让排障走错方向。
func TestDispatcherRejectsUnknownAndUnimplemented(t *testing.T) {
	exec := &fakeExec{}
	sink := &collectSink{}
	d := NewDispatcher(exec, sink.send, testLogger())
	d.Handle(context.Background(), &agentproto.DockerCmd{Ref: "1", Action: "container:destroy",
		Options: agentproto.DockerCmdOptions{Target: "x"}})
	// 监控面落地后白名单 30 条在本版全部实现：要验证「未实现档」这一分支，只能临时
	// 摘掉一条（模拟旧版 agent 收到新 core 发来的 action）—— 拿真实缺口当样例会随
	// 功能落地而失真，这正是这条用例的历史教训（三次）。
	const missing = agentproto.DockerActionComposeFilePatch
	saved, had := implementedActions[missing]
	delete(implementedActions, missing)
	t.Cleanup(func() {
		if had {
			implementedActions[missing] = saved
		}
	})
	d.Handle(context.Background(), &agentproto.DockerCmd{Ref: "2", Action: missing,
		Options: agentproto.DockerCmdOptions{Target: "x"}})
	for _, c := range []string{"1", "2"} {
		d.ExecuteNow(context.Background(), c) // 测试用同步入口：见实现说明
	}
	got := sink.all()
	if len(got) != 2 {
		t.Fatalf("两条都应有结果，实际 %d", len(got))
	}
	if got[0].OK || got[0].Error != "未知操作" {
		t.Fatalf("未知 action 的结论句不符: %+v", got[0])
	}
	if got[1].OK || got[1].Error != "该操作尚未开放" {
		t.Fatalf("未实现 action 的结论句不符: %+v", got[1])
	}
	if exec.count() != 0 {
		t.Fatal("被拒的指令绝不能触达执行器")
	}
}

// 参数校验必须发生在**执行之前**（缺 target 的指令执行起来会去操作一个空名字）。
func TestDispatcherValidatesBeforeExecute(t *testing.T) {
	exec := &fakeExec{}
	sink := &collectSink{}
	d := NewDispatcher(exec, sink.send, testLogger())
	d.Handle(context.Background(), &agentproto.DockerCmd{Ref: "1", Action: agentproto.DockerActionContainerLogs})
	d.ExecuteNow(context.Background(), "1")
	got := sink.all()
	if len(got) != 1 || got[0].OK || got[0].Error != "指令参数不合法" {
		t.Fatalf("缺必填项必须被拒: %+v", got)
	}
	if exec.count() != 0 {
		t.Fatal("参数非法的指令绝不能触达执行器")
	}
}

// 指令**串行**执行：这是 §10 的硬纪律（compose 类操作天然互相冲突）。
func TestDispatcherSerializesCommands(t *testing.T) {
	exec := &fakeExec{sleep: 40 * time.Millisecond}
	sink := &collectSink{}
	d := NewDispatcher(exec, sink.send, testLogger())
	ctx := context.Background()
	for i, ref := range []string{"1", "2", "3"} {
		d.Handle(ctx, &agentproto.DockerCmd{Ref: ref, Action: agentproto.DockerActionContainerInspect,
			Options: agentproto.DockerCmdOptions{Target: "c" + ref}})
		_ = i
	}
	go d.Run(ctx)
	deadline := time.After(3 * time.Second)
	for len(sink.all()) < 3 {
		select {
		case <-deadline:
			t.Fatalf("三条指令未在时限内回结果，实际 %d 条", len(sink.all()))
		case <-time.After(10 * time.Millisecond):
		}
	}
	// 串行的证据：三条 40ms 的指令总耗时 ≥ 120ms（并行会接近 40ms）
	// —— 用调用序列断言更直接：执行顺序必须与受理顺序一致。
	exec.mu.Lock()
	defer exec.mu.Unlock()
	want := []string{"container:inspect|c1", "container:inspect|c2", "container:inspect|c3"}
	for i := range want {
		if exec.calls[i] != want[i] {
			t.Fatalf("执行顺序不符（必须串行且保序）: %v", exec.calls)
		}
	}
}

// 队列满时必须立刻回一句结论，而不是让指令排在一个没有尽头的队里。
func TestDispatcherRejectsWhenQueueFull(t *testing.T) {
	exec := &fakeExec{sleep: 200 * time.Millisecond}
	sink := &collectSink{}
	d := NewDispatcher(exec, sink.send, testLogger())
	ctx := context.Background()
	accepted := 0
	for i := 0; i < dispatchQueueCap*2; i++ {
		cmd := &agentproto.DockerCmd{Ref: strconv.Itoa(i), Action: agentproto.DockerActionContainerInspect,
			Options: agentproto.DockerCmdOptions{Target: "c"}}
		if d.Handle(ctx, cmd) {
			accepted++
		}
	}
	if accepted >= dispatchQueueCap*2 {
		t.Fatal("队列有上限，超出后必须拒绝")
	}
}

// 执行失败：结论句进 error，原始细节进 detail（且被截断）—— 两者不得混用。
func TestDispatcherSplitsConclusionAndDetail(t *testing.T) {
	exec := &fakeExec{err: &ExecError{Msg: "读取容器日志失败", Detail: strings.Repeat("x", 5000)}}
	sink := &collectSink{}
	d := NewDispatcher(exec, sink.send, testLogger())
	d.Handle(context.Background(), &agentproto.DockerCmd{Ref: "1", Action: agentproto.DockerActionContainerLogs,
		Options: agentproto.DockerCmdOptions{Target: "mysql"}})
	d.ExecuteNow(context.Background(), "1")
	got := sink.all()[0]
	if got.OK || got.Error != "读取容器日志失败" {
		t.Fatalf("结论句不符: %+v", got)
	}
	if got.Detail == "" || len(got.Detail) > agentproto.MaxDockerResultDetailBytes {
		t.Fatalf("detail 必须存在且被截断，实际长度 %d", len(got.Detail))
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("结果必须能过协议校验: %v", err)
	}
}

// 落定回调必须对**每一条**指令恰好调用一次，并如实报告成败 —— Runtime 据此决定
// 「写操作成功后立即采帧」（ok=false 的路径包括未知/未实现/参数非法/执行失败，
// 它们都不是「改动已发生」，不该触发采集）。
func TestDispatcherNotifiesSettledOnceWithOutcome(t *testing.T) {
	exec := &fakeExec{}
	sink := &collectSink{}
	d := NewDispatcher(exec, sink.send, testLogger())
	type settledCall struct {
		action string
		ok     bool
	}
	var calls []settledCall
	d.SetOnSettled(func(action string, ok bool) { calls = append(calls, settledCall{action, ok}) })

	ctx := context.Background()
	handle := func(ref, action, target string) {
		d.Handle(ctx, &agentproto.DockerCmd{Ref: ref, Action: action,
			Options: agentproto.DockerCmdOptions{Target: target}})
		d.ExecuteNow(ctx, ref)
	}
	handle("1", "container:destroy", "x")                     // 未知 action
	handle("2", agentproto.DockerActionComposeFileWrite, "x") // 未实现 action（四期）
	handle("3", agentproto.DockerActionContainerStart, "")    // 参数非法（缺 target）
	exec.err = &ExecError{Msg: "执行失败"}                        // 下面的执行失败
	handle("4", agentproto.DockerActionContainerStart, "mysql")
	exec.err = nil // 下面的执行成功
	handle("5", agentproto.DockerActionContainerStart, "mysql")

	want := []settledCall{
		{"container:destroy", false},
		{agentproto.DockerActionComposeFileWrite, false},
		{agentproto.DockerActionContainerStart, false},
		{agentproto.DockerActionContainerStart, false},
		{agentproto.DockerActionContainerStart, true},
	}
	if len(calls) != len(want) {
		t.Fatalf("每条指令必须恰好回调一次，got %v want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("回调 %d = %+v, want %+v", i, calls[i], want[i])
		}
	}
}

// fakeSessionExec 是「会话制执行器」的替身：Do 立刻返回，句柄走 sessionNamer。
type fakeSessionExec struct{ id string }

func (f *fakeSessionExec) Do(context.Context, *agentproto.DockerCmd) ([]byte, error) { return nil, nil }
func (f *fakeSessionExec) takeSessionID() string                                     { return f.id }

// 会话句柄必须从执行器透到 result.session_id（可选接口 sessionNamer），且**只取一次**：
// 留在执行器里的旧句柄会串到下一条指令的结果上（前端会拿它去接一个已结束的流）。
func TestDispatcherCarriesSessionID(t *testing.T) {
	exec := &fakeSessionExec{id: "0123456789abcdef"}
	sink := &collectSink{}
	d := NewDispatcher(exec, sink.send, testLogger())
	d.Handle(context.Background(), &agentproto.DockerCmd{Ref: "1", Action: agentproto.DockerActionContainerExec,
		Options: agentproto.DockerCmdOptions{Target: "mysql"}})
	d.ExecuteNow(context.Background(), "1")
	exec.id = "" // 下一条指令不再建会话
	d.Handle(context.Background(), &agentproto.DockerCmd{Ref: "2", Action: agentproto.DockerActionContainerExec,
		Options: agentproto.DockerCmdOptions{Target: "mysql"}})
	d.ExecuteNow(context.Background(), "2")
	got := sink.all()
	if len(got) != 2 {
		t.Fatalf("两条都应有结果，实际 %d", len(got))
	}
	if got[0].SessionID != "0123456789abcdef" || !got[0].OK {
		t.Fatalf("会话句柄必须写进结果: %+v", got[0])
	}
	if err := got[0].Validate(); err != nil {
		t.Fatalf("带会话句柄的结果必须语义合法: %v", err)
	}
	if got[1].SessionID != "" {
		t.Fatalf("句柄必须被取走（不得串到下一条结果）: %+v", got[1])
	}
}
