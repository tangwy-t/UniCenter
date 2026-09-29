package dockerops

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

type stateSink struct {
	mu      sync.Mutex
	states  []*agentproto.DockerState
	results []*agentproto.DockerCmdResult
}

func (s *stateSink) state(st *agentproto.DockerState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states = append(s.states, st)
	return nil
}

func (s *stateSink) result(r *agentproto.DockerCmdResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results = append(s.results, r)
	return nil
}

func (s *stateSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.states)
}

func (s *stateSink) resultCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.results)
}

// waitFor 轮询等待一个条件成立（测试里的既有模式：10ms 步进 + 3s 兜底）。
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for !cond() {
		select {
		case <-deadline:
			t.Fatalf("等待「%s」超时", what)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// 配置必须**落盘**并能在下次启动时载入：hello_ack 只在重连时下发，而 agent 会重启。
// 不落盘的症状是「重启后保护清单凭空消失，且服务端不会再发」。
func TestRuntimePersistsAndLoadsConfig(t *testing.T) {
	dir := t.TempDir()
	sink := &stateSink{}
	r := New(Deps{StateDir: dir, SendState: sink.state, SendResult: sink.result,
		Log: testLogger(), API: &stubAPI{}, Now: func() time.Time { return time.Unix(1790000000, 0) }})
	r.OnConfig(&agentproto.DockerConfig{ConfigVersion: 7, Protected: defaultProtected,
		TransferDir: "/var/lib/uni_agent/transfer", SnapshotInterval: 30})

	b, err := os.ReadFile(filepath.Join(dir, dockerStateFileName))
	if err != nil {
		t.Fatalf("配置必须落盘: %v", err)
	}
	var f dockerStateFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if f.ConfigVersion != 7 || f.Protected != defaultProtected || f.SnapshotIntervalSec != 30 {
		t.Fatalf("落盘内容不符: %+v", f)
	}

	// 新实例从同一目录启动 → 立刻按落盘的清单采集（不必等下一次重连）
	r2 := New(Deps{StateDir: dir, SendState: sink.state, SendResult: sink.result,
		Log: testLogger(), API: &stubAPI{}, Now: func() time.Time { return time.Unix(1790000000, 0) }})
	if !r2.protected.Container("mysql") {
		t.Fatal("重启后必须载入落盘的保护清单（否则底座容器在重启窗口内失去保护）")
	}
	if got := r2.currentInterval(); got != 30*time.Second {
		t.Fatalf("重启后必须载入落盘的快照周期，实际 %v", got)
	}
}

// OnConnected 必须**非阻塞**（它在连接的读循环里被调用）：只做一次「立刻采一帧」的触发。
func TestRuntimeOnConnectedIsNonBlocking(t *testing.T) {
	sink := &stateSink{}
	r := New(Deps{StateDir: t.TempDir(), SendState: sink.state, SendResult: sink.result,
		Log: testLogger(), API: &stubAPI{}, Now: func() time.Time { return time.Unix(1790000000, 0) }})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.SnapshotLoop(ctx)
	done := make(chan struct{})
	go func() { r.OnConnected(); close(done) }()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnConnected 阻塞了（它会拖住整条连接的读循环）")
	}
	deadline := time.After(2 * time.Second)
	for sink.count() == 0 {
		select {
		case <-deadline:
			t.Fatal("OnConnected 后必须立刻采一帧（首帧不等周期，spec §2）")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// 指令入口必须**非阻塞**且队列满时立刻回一句结论。
func TestRuntimeOnCmdIsNonBlockingAndRejectsWhenBusy(t *testing.T) {
	sink := &stateSink{}
	exec := &fakeExec{sleep: 300 * time.Millisecond}
	r := New(Deps{StateDir: t.TempDir(), SendState: sink.state, SendResult: sink.result,
		Log: testLogger(), API: &stubAPI{},
		Now: func() time.Time { return time.Unix(1790000000, 0) }})
	r.exec = exec
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.SnapshotLoop(ctx)
	go r.Run(ctx)

	cmd := func(ref string) *agentproto.DockerCmd {
		return &agentproto.DockerCmd{Ref: ref, Action: agentproto.DockerActionContainerInspect,
			Options: agentproto.DockerCmdOptions{Target: "mysql"}}
	}
	start := time.Now()
	for i := 0; i < dispatchQueueCap+3; i++ {
		r.OnCmd(cmd(strconv.Itoa(i)))
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("OnCmd 必须非阻塞，实际耗时 %v", elapsed)
	}
	// 队列满的那几条必须已经拿到「排队已满」的结论
	deadline := time.After(3 * time.Second)
	for {
		sink.mu.Lock()
		busy := 0
		for _, res := range sink.results {
			if res.Error == "指令排队已满，请稍后重试" {
				busy++
			}
		}
		sink.mu.Unlock()
		if busy > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("队列满时必须立刻回一句结论（而不是静默丢弃）")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// 写操作成功后必须触发一次**立即采帧**：core 的 /state 是周期上报的缓存（30s），
// 不在结果落定时补一帧，页面点完「停止」要等半分钟列表才动（§11.0「不等 30s 周期」）。
// 读操作与失败**不**触发：读不改主机状态，失败也不是「改动已发生」。
func TestRuntimeTriggersSnapshotAfterSuccessfulWriteOnly(t *testing.T) {
	sink := &stateSink{}
	exec := &fakeExec{}
	// 周期取一小时：断言窗口内出现的额外帧只可能来自 trigger，不会与周期 tick 混淆。
	r := New(Deps{StateDir: t.TempDir(), SendState: sink.state, SendResult: sink.result,
		Log: testLogger(), API: &stubAPI{}, Interval: time.Hour,
		Now: func() time.Time { return time.Unix(1790000000, 0) }})
	r.exec = exec
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.SnapshotLoop(ctx)
	go r.Run(ctx)
	waitFor(t, "首帧", func() bool { return sink.count() >= 1 })

	send := func(ref, action string) {
		t.Helper()
		before := sink.resultCount()
		r.OnCmd(&agentproto.DockerCmd{Ref: ref, Action: action,
			Options: agentproto.DockerCmdOptions{Target: "mysql"}})
		waitFor(t, "指令 "+ref+" 的结果", func() bool { return sink.resultCount() == before+1 })
	}
	settled := func(window time.Duration) {
		t.Helper()
		time.Sleep(window) // 让 trigger 有机会被消费；窗口内不该有帧
	}

	// 读操作成功：不触发。
	send("1", agentproto.DockerActionContainerInspect)
	settled(150 * time.Millisecond)
	if got := sink.count(); got != 1 {
		t.Fatalf("读操作不改主机状态，不得触发采帧，实际帧数 %d", got)
	}

	// 写操作失败：不触发。
	exec.err = &ExecError{Msg: "执行失败"}
	send("2", agentproto.DockerActionContainerStart)
	settled(150 * time.Millisecond)
	if got := sink.count(); got != 1 {
		t.Fatalf("失败不是「改动已发生」，不得触发采帧，实际帧数 %d", got)
	}

	// 写操作成功：立刻多一帧。
	exec.err = nil
	send("3", agentproto.DockerActionContainerStart)
	waitFor(t, "写成功后的立即帧", func() bool { return sink.count() >= 2 })
	settled(150 * time.Millisecond)
	if got := sink.count(); got != 2 {
		t.Fatalf("恰好一条写成功应恰好触发一帧（通道深度 1 会合并），实际帧数 %d", got)
	}
}

// 控制帧现在**投递**给会话管理器（三期接线）：会话未知（或已结束）时只忽略，
// 不得 panic、不得回任何东西 —— core 的 sweep 与 agent 的释放之间有窗口，
// 「用户点取消、帧晚到」是正常时序。
func TestRuntimeIgnoresStreamFramesForUnknownSession(t *testing.T) {
	r := New(Deps{StateDir: t.TempDir(), SendState: func(*agentproto.DockerState) error { return nil },
		SendResult: func(*agentproto.DockerCmdResult) error { return nil },
		SendFrame:  func(*agentproto.DockerFrame) error { return nil },
		Log:        testLogger(), API: &stubAPI{}, Now: func() time.Time { return time.Unix(1790000000, 0) }})
	r.OnFrame(&agentproto.CoreDockerFrame{SessionID: "0123456789abcdef", Op: agentproto.DockerFrameOpCancel})
	r.OnFrame(&agentproto.CoreDockerFrame{SessionID: "0123456789abcdef", Op: agentproto.DockerFrameOpInput, Data: []byte("x")})
	r.OnFrame(&agentproto.CoreDockerFrame{SessionID: "0123456789abcdef", Op: agentproto.DockerFrameOpResize, Cols: 80, Rows: 24})
	r.OnFrame(nil) // 防御性：nil 不得 panic
}
