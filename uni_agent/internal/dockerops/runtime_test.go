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

// 一期不消费流帧：收到 control 帧必须只是记 Debug，不得 panic、不得回任何东西。
func TestRuntimeIgnoresStreamFrames(t *testing.T) {
	r := New(Deps{StateDir: t.TempDir(), SendState: func(*agentproto.DockerState) error { return nil },
		SendResult: func(*agentproto.DockerCmdResult) error { return nil },
		Log:        testLogger(), API: &stubAPI{}, Now: func() time.Time { return time.Unix(1790000000, 0) }})
	r.OnFrame(&agentproto.CoreDockerFrame{SessionID: "0123456789abcdef", Op: agentproto.DockerFrameOpCancel})
}
