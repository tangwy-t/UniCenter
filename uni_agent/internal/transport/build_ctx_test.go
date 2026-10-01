package transport

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── 构建上下文上传通道的传输层路由（v1.3）──────────────────────────────
//
// 钉住「二进制分片 → OnBinaryFrame」「完成/中止控制帧 → OnBuildCtxFinish/
// OnBuildCtxAbort」的接线，以及「坏帧不 panic、不断连」的旁路取向。读循环的
// 消息类型分支与真实 socket 的交互（BinaryMessage → handleBinaryFrame）
// 走协议层与接收器双层测试，这里钉住**路由表**本身。

// recordingDockerHook 实现全部 DockerHook 方法并记账。
type recordingDockerHook struct {
	mu        sync.Mutex
	binaries  []agentproto.BinaryFrame
	finishes  []*agentproto.CoreDockerBuildCtxFinish
	aborts    []*agentproto.CoreDockerBuildCtxAbort
	disc      int
	connected int
	cmds      int
	frames    int
	configs   *agentproto.DockerConfig
}

func (h *recordingDockerHook) OnBinaryFrame(f agentproto.BinaryFrame) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.binaries = append(h.binaries, f)
}
func (h *recordingDockerHook) OnBuildCtxFinish(m *agentproto.CoreDockerBuildCtxFinish) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.finishes = append(h.finishes, m)
}
func (h *recordingDockerHook) OnBuildCtxAbort(m *agentproto.CoreDockerBuildCtxAbort) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.aborts = append(h.aborts, m)
}
func (h *recordingDockerHook) OnDisconnected() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.disc++
}
func (h *recordingDockerHook) OnConnected() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.connected++
}
func (h *recordingDockerHook) OnConfig(cfg *agentproto.DockerConfig) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.configs = cfg
}
func (h *recordingDockerHook) OnCmd(*agentproto.DockerCmd) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cmds++
}
func (h *recordingDockerHook) OnFrame(*agentproto.CoreDockerFrame) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.frames++
}

func TestHandleBinaryFrameRoutesToHook(t *testing.T) {
	hook := &recordingDockerHook{}
	c := New(Config{URL: "ws://unused", HeartbeatInterval: time.Second})
	c.SetDockerHook(hook)

	payload := []byte("chunk-bytes")
	frame, err := agentproto.PackBinaryFrame(42, 7, true, payload)
	if err != nil {
		t.Fatal(err)
	}
	c.handleBinaryFrame(frame)

	got := hook.binaries
	if len(got) != 1 || got[0].SessionID != 42 || got[0].Seq != 7 || !got[0].Final {
		t.Fatalf("路由后的帧 = %+v", got)
	}
	if string(got[0].Payload) != string(payload) {
		t.Fatalf("payload = %q, want %q", got[0].Payload, payload)
	}

	// 坏帧：不 panic、不路由。
	c.handleBinaryFrame([]byte("garbage-not-a-frame"))
	c.handleBinaryFrame(nil)
	if len(hook.binaries) != 1 {
		t.Fatalf("坏帧不得路由到 hook: %d", len(hook.binaries))
	}
}

func TestHandleFrameRoutesBuildCtxControls(t *testing.T) {
	hook := &recordingDockerHook{}
	c := New(Config{URL: "ws://unused", HeartbeatInterval: time.Second})
	c.SetDockerHook(hook)

	finMsg, err := agentproto.NewMessage("1", agentproto.TypeCoreDockerBuildCtxFinish, &agentproto.CoreDockerBuildCtxFinish{
		SessionID: 42, Name: agentproto.DockerBuildCtxTransferName(42),
		SHA256Hex: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		SizeBytes: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := finMsg.Marshal()
	c.handleFrame(b)
	if len(hook.finishes) != 1 || hook.finishes[0].SessionID != 42 || hook.finishes[0].SizeBytes != 10 {
		t.Fatalf("完成帧路由 = %+v", hook.finishes)
	}

	abortMsg, err := agentproto.NewMessage("2", agentproto.TypeCoreDockerBuildCtxAbort, &agentproto.CoreDockerBuildCtxAbort{SessionID: 42})
	if err != nil {
		t.Fatal(err)
	}
	b, _ = abortMsg.Marshal()
	c.handleFrame(b)
	if len(hook.aborts) != 1 || hook.aborts[0].SessionID != 42 {
		t.Fatalf("中止帧路由 = %+v", hook.aborts)
	}

	// 老消息照旧：core.docker.frame / core.docker.cmd / hello_ack 的路由不受影响。
	oldFrame, err := agentproto.NewMessage("3", agentproto.TypeCoreDockerFrame, &agentproto.CoreDockerFrame{
		SessionID: agentproto.DockerPullSessionID("1790000000000000888"), Op: agentproto.DockerFrameOpCancel,
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ = oldFrame.Marshal()
	c.handleFrame(b)
	if hook.frames != 1 {
		t.Fatalf("core.docker.frame 老路由被破坏: %d", hook.frames)
	}
}

// TestHandleFrameIgnoresBadBuildCtxControl：控制帧载荷非法（协约 Validate 拒绝）
// 时丢弃该条、不 panic、不路由 —— 与未知类型的取向一致。
func TestHandleFrameIgnoresBadBuildCtxControl(t *testing.T) {
	hook := &recordingDockerHook{}
	c := New(Config{URL: "ws://unused", HeartbeatInterval: time.Second})
	c.SetDockerHook(hook)

	// session_id=0 的完成帧：DecodeTypedFor 的载荷校验会拒。
	m := &agentproto.Message{
		V:    agentproto.CurrentVersion,
		ID:   "9",
		Type: agentproto.TypeCoreDockerBuildCtxFinish,
		TS:   time.Now().UnixMilli(),
		Data: json.RawMessage(`{"session_id":0,"name":"x","sha256":"bad","size_bytes":1}`),
	}
	b, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	c.handleFrame(b)
	if len(hook.finishes) != 0 {
		t.Fatalf("非法控制帧不得路由: %+v", hook.finishes)
	}
}
