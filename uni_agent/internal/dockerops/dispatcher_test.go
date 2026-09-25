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
	d.Handle(context.Background(), &agentproto.DockerCmd{Ref: "2", Action: agentproto.DockerActionComposeUp,
		Options: agentproto.DockerCmdOptions{Target: "uni-center"}})
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
