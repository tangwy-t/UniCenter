package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agenthub"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── 构建上下文上传中转（core ② 后端）───────────────────────────────────────
//
// 断言面按任务切分：
//   - 流式中转的**帧形状**：分片计数、seq 严格 1..N、恰一帧 FINAL、载荷拼接
//     与原始字节逐位一致 —— 「大文件分片计数」与「零增删」在这里钉死；
//   - 完成控制帧携带**与 body 一致的**期望哈希/大小/推导名；
//   - 离线拒绝、通道断、取消、魔数拒、超限拒各走各的结论句与收尾（中止帧）。

// fakeBuildCtxChannel 是 DockerBuildContextChannel 的账本替身：
// 每帧解码成 BinaryFrame 记账；可注入离线（首帧 ErrDeviceOffline）与
// 「第 N 次发送断链」（failAtN；ctx 取消照真实 EnqueueBinary 语义返回 ctx.Err()）。
type fakeBuildCtxChannel struct {
	mu sync.Mutex

	frames  []recordedFrame       // 二进制分片帧（按送达顺序）
	texts   []*agentproto.Message // 文本信封（完成/中止控制帧，按送达顺序）
	offline bool                  // 首帧即 ErrDeviceOffline（吃住「预检通过却被抢占下线」）
	failAtN int                   // 第 N 次二进制发送返回 failErr（1-based；0 = 不注入）
	failErr error
	sends   int // 二进制发送计数
}

type recordedFrame struct {
	session uint64
	seq     uint64
	final   bool
	payload []byte
}

func (f *fakeBuildCtxChannel) SendBinaryToDevice(ctx context.Context, _ uint64, payload []byte) error {
	if ctx.Err() != nil {
		// 与真实 EnqueueBinary 一致：ctx 取消即解除阻塞并返回取消错误。
		return ctx.Err()
	}
	f.mu.Lock()
	f.sends++
	failing := f.failAtN > 0 && f.sends == f.failAtN
	var failErr error
	if failing {
		failErr = f.failErr
	}
	offline := f.offline
	f.mu.Unlock()
	if failing {
		return failErr
	}
	if offline {
		return agenthub.ErrDeviceOffline
	}
	dec, err := agentproto.UnpackBinaryFrame(payload)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.frames = append(f.frames, recordedFrame{
		session: dec.SessionID, seq: dec.Seq, final: dec.Final,
		payload: append([]byte{}, dec.Payload...),
	})
	return nil
}

func (f *fakeBuildCtxChannel) armFail(n int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failAtN, f.failErr = n, err
}

func (f *fakeBuildCtxChannel) SendToDevice(_ uint64, msg *agentproto.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.offline {
		return agenthub.ErrDeviceOffline
	}
	f.texts = append(f.texts, msg)
	return nil
}

func (f *fakeBuildCtxChannel) snapshot() ([]recordedFrame, []*agentproto.Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	frames := make([]recordedFrame, len(f.frames))
	copy(frames, f.frames)
	msgs := make([]*agentproto.Message, len(f.texts))
	copy(msgs, f.texts)
	return frames, msgs
}

// lastControl 返回最后一条指定类型的控制消息（失败即 Fatal）。
func lastControl(t *testing.T, ch *fakeBuildCtxChannel, typ string) *agentproto.Message {
	t.Helper()
	_, msgs := ch.snapshot()
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Type == typ {
			return msgs[i]
		}
	}
	t.Fatalf("未找到类型 %s 的控制消息，实际 %d 条", typ, len(msgs))
	return nil
}

// lastControlOrNil 返回最后一条指定类型的控制消息；没有返回 nil。
func lastControlOrNil(ch *fakeBuildCtxChannel, typ string) *agentproto.Message {
	_, msgs := ch.snapshot()
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Type == typ {
			return msgs[i]
		}
	}
	return nil
}

// newTestBuildCtxService 造一个「会话号固定、在线性可拨」的服务。
func newTestBuildCtxService(ch *fakeBuildCtxChannel, online bool) *DockerBuildContextService {
	svc := NewDockerBuildContextService(ch, logger.NewNop())
	svc.online = func(uint64) bool { return online }
	svc.idGen = func() string { return "4242424242424241" }
	return svc
}

// 会话号常量必须与 newTestBuildCtxService 的 idGen 一致（测试内契约）。
const testSessionID = uint64(4242424242424241)

// gzipBody 造一段「以 gzip 魔数开头 + 确定填充」的伪 tar.gz 字节。
// 上传段只看魔数与大小，确定填充保证哈希断言与拼接断言都逐位可验。
func gzipBody(n int) []byte {
	b := make([]byte, n)
	b[0], b[1] = 0x1f, 0x8b
	for i := 2; i < n; i++ {
		b[i] = byte(i*31 + 7)
	}
	return b
}

func wantSHA(t *testing.T, b []byte) string {
	t.Helper()
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// armedReader 记录「是否被读过」—— 钉住「即拒不等传完」（离线/超限在
// 吃到 body 之前就该给结论）。
type armedReader struct{ read bool }

func (a *armedReader) Read(p []byte) (int, error) {
	a.read = true
	return 0, io.EOF
}

// ── 流式中转与分片计数 ────────────────────────────────────────────────────

func TestTransferStreamsChunksInOrder(t *testing.T) {
	// 帧结构（对照 Transfer 的一帧前瞻）：头 2 字节经魔数预读后并入 seq=1；
	// 之后每 256KB 一片；末片 FINAL=1。body = 2 + 3×256KB + 12345。
	body := gzipBody(2 + 3*uploadChunkBytes + 12345)
	ch := &fakeBuildCtxChannel{}
	svc := newTestBuildCtxService(ch, true)

	name, err := svc.Transfer(context.Background(), 7, bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("Transfer = %v", err)
	}
	want := agentproto.DockerBuildCtxTransferName(testSessionID)
	if name != want {
		t.Fatalf("filename = %q, want 会话号推导名 %q", name, want)
	}

	frames, msgs := ch.snapshot()
	if len(frames) != 5 {
		t.Fatalf("分片数 = %d, want 5（1 头帧 + 3 整片 + 1 末片）", len(frames))
	}
	var got []byte
	for i, fr := range frames {
		if i == 0 && fr.seq != 1 {
			t.Fatalf("首帧 seq = %d, want 1", fr.seq)
		}
		if i > 0 && fr.seq != frames[i-1].seq+1 {
			t.Fatalf("帧 %d seq = %d：序列必须严格 +1", i, fr.seq)
		}
		if fr.session != testSessionID {
			t.Fatalf("帧 %d 会话号 = %d, want %d", i, fr.session, testSessionID)
		}
		if fr.final == (i != len(frames)-1) {
			t.Fatalf("帧 %d final = %v：FINAL 必须且只能落在末帧", i, fr.final)
		}
		got = append(got, fr.payload...)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("分片拼接 != 请求体（len %d vs %d）", len(got), len(body))
	}
	// 完成控制帧：推导名、期望哈希、大小。
	fin := lastControl(t, ch, agentproto.TypeCoreDockerBuildCtxFinish)
	var finPayload agentproto.CoreDockerBuildCtxFinish
	if err := fin.DecodeData(&finPayload); err != nil {
		t.Fatalf("完成帧载荷解码: %v", err)
	}
	if finPayload.Name != want || finPayload.SizeBytes != int64(len(body)) {
		t.Fatalf("完成帧 %+v, want name=%s size=%d", finPayload, want, len(body))
	}
	if finPayload.SHA256Hex != wantSHA(t, body) {
		t.Fatalf("完成帧哈希 = %s, want 全程累计 %s", finPayload.SHA256Hex, wantSHA(t, body))
	}
	// 成功路径不得有中止帧。
	for _, m := range msgs {
		if m.Type == agentproto.TypeCoreDockerBuildCtxAbort {
			t.Fatal("成功上传不得发送中止帧")
		}
	}
}

func TestTransferFrameCountAcrossSizes(t *testing.T) {
	// 帧数数学：body≥2 时 = 1（seq=1 的头帧）+ ceil((len-2)/256KB)，
	// 2 字节整的边界上末片由「读回 (0, EOF)」补为 FINAL。
	cases := []struct {
		name      string
		bodyLen   int
		wantFrame int
	}{
		{"仅魔数 2 字节", 2, 1},
		{"3 字节", 3, 2},
		{"恰好 1 片", 2 + uploadChunkBytes, 2},
		{"1 片多 1 字节", 2 + uploadChunkBytes + 1, 3},
		{"恰好 2 片", 2 + 2*uploadChunkBytes, 3},
		{"3 片", 2 + 3*uploadChunkBytes, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := gzipBody(tc.bodyLen)
			ch := &fakeBuildCtxChannel{}
			svc := newTestBuildCtxService(ch, true)
			if _, err := svc.Transfer(context.Background(), 7, bytes.NewReader(body), int64(len(body))); err != nil {
				t.Fatal(err)
			}
			frames, _ := ch.snapshot()
			if len(frames) != tc.wantFrame {
				t.Fatalf("分片数 = %d, want %d", len(frames), tc.wantFrame)
			}
			if !frames[len(frames)-1].final {
				t.Fatal("末帧必须带 FINAL")
			}
		})
	}
}

// ── 离线拒绝 ─────────────────────────────────────────────────────────────

func TestTransferOfflineRejectedBeforeReadingBody(t *testing.T) {
	armed := &armedReader{}
	ch := &fakeBuildCtxChannel{}
	svc := newTestBuildCtxService(ch, false) // 预检即离线

	_, err := svc.Transfer(context.Background(), 7, armed, 1024)
	var ae *apperror.AppError
	if !errors.As(err, &ae) || ae.HTTPStatus != 503 {
		t.Fatalf("offline 应 503: %v", err)
	}
	if armed.read {
		t.Fatal("离线拒绝读了 body —— 预检必须发生在吃请求体之前")
	}
}

func TestTransferOfflineFirstSendRace(t *testing.T) {
	// 预检通过、但首帧发送时设备已下线（注册表竞态）：结论句仍是 503。
	ch := &fakeBuildCtxChannel{}
	ch.mu.Lock()
	ch.offline = true
	ch.mu.Unlock()
	svc := newTestBuildCtxService(ch, true)

	body := gzipBody(1024)
	_, err := svc.Transfer(context.Background(), 7, bytes.NewReader(body), int64(len(body)))
	var ae *apperror.AppError
	if !errors.As(err, &ae) || ae.HTTPStatus != 503 {
		t.Fatalf("首帧离线应 503: %v", err)
	}
	if frames, _ := ch.snapshot(); len(frames) != 0 {
		t.Fatalf("离线帧不得入账本: %d", len(frames))
	}
}

// ── 通道中断 / 取消：中止帧兜底 ───────────────────────────────────────────

func TestTransferChannelBreakMidStreamAborts(t *testing.T) {
	body := gzipBody(2 + 4*uploadChunkBytes)
	ch := &fakeBuildCtxChannel{}
	ch.armFail(3, errors.New("模拟链路断裂"))
	svc := newTestBuildCtxService(ch, true)

	_, err := svc.Transfer(context.Background(), 7, bytes.NewReader(body), int64(len(body)))
	var ae *apperror.AppError
	if !errors.As(err, &ae) || ae.HTTPStatus != 503 {
		t.Fatalf("通道断应 503: %v", err)
	}
	frames, _ := ch.snapshot()
	if len(frames) != 2 { // 头帧 + 第 1 片成功，第 3 次发送失败
		t.Fatalf("中断前应送达 2 帧: %d", len(frames))
	}
	abort := lastControl(t, ch, agentproto.TypeCoreDockerBuildCtxAbort)
	var abortPayload agentproto.CoreDockerBuildCtxAbort
	if err := abort.DecodeData(&abortPayload); err != nil {
		t.Fatal(err)
	}
	if abortPayload.SessionID != testSessionID {
		t.Fatalf("中止帧会话 = %d, want %d", abortPayload.SessionID, testSessionID)
	}
	if fin := lastControlOrNil(ch, agentproto.TypeCoreDockerBuildCtxFinish); fin != nil {
		t.Fatal("中断后不得有完成帧")
	}
}

func TestTransferContextCancelSendsAbort(t *testing.T) {
	body := gzipBody(2 + 4*uploadChunkBytes)
	ch := &fakeBuildCtxChannel{}
	svc := newTestBuildCtxService(ch, true)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 首帧发送的阻塞点直接解除（真实语义：EnqueueBinary 返回 ctx.Err()）
	_, err := svc.Transfer(ctx, 7, bytes.NewReader(body), int64(len(body)))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ctx 取消应原样返回 Canceled: %v", err)
	}
	_, msgs := ch.snapshot()
	if len(msgs) == 0 || msgs[len(msgs)-1].Type != agentproto.TypeCoreDockerBuildCtxAbort {
		t.Fatalf("取消路径必须补一发中止帧: %v", msgs)
	}
}

// ── 魔数 / 空 body / 超限：上传段的输入面拒绝 ──────────────────────────────

func TestTransferRejectsBadMagicBeforeAnyFrame(t *testing.T) {
	raw := append([]byte("not-a-gzip"), make([]byte, 4096)...)
	ch := &fakeBuildCtxChannel{}
	svc := newTestBuildCtxService(ch, true)

	_, err := svc.Transfer(context.Background(), 7, bytes.NewReader(raw), int64(len(raw)))
	var ae *apperror.AppError
	if !errors.As(err, &ae) || ae.Code != apperror.CodeBadRequest {
		t.Fatalf("魔数不符应 400: %v", err)
	}
	if frames, msgs := ch.snapshot(); len(frames) != 0 || len(msgs) != 0 {
		t.Fatalf("魔数拒绝不得发出任何帧（frames=%d texts=%d）—— agent 零状态", len(frames), len(msgs))
	}
}

func TestTransferRejectsEmptyAndTinyBodies(t *testing.T) {
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"空 body", nil},
		{"1 字节", []byte{0x1f}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ch := &fakeBuildCtxChannel{}
			svc := newTestBuildCtxService(ch, true)
			_, err := svc.Transfer(context.Background(), 7, bytes.NewReader(tc.body), int64(len(tc.body)))
			var ae *apperror.AppError
			if !errors.As(err, &ae) || ae.Code != apperror.CodeBadRequest {
				t.Fatalf("应 400: %v", err)
			}
			if frames, msgs := ch.snapshot(); len(frames) != 0 || len(msgs) != 0 {
				t.Fatalf("不得发出任何帧: frames=%d texts=%d", len(frames), len(msgs))
			}
		})
	}
}

func TestTransferRejectsContentLengthOverLimitImmediately(t *testing.T) {
	ch := &fakeBuildCtxChannel{}
	svc := newTestBuildCtxService(ch, true)

	old := uploadMaxBytes
	uploadMaxBytes = 1024
	t.Cleanup(func() { uploadMaxBytes = old })

	armed := &armedReader{}
	_, err := svc.Transfer(context.Background(), 7, armed, 2048)
	var ae *apperror.AppError
	if !errors.As(err, &ae) || ae.Code != apperror.CodeBadRequest {
		t.Fatalf("Content-Length 超限应 400 即拒: %v", err)
	}
	if armed.read {
		t.Fatal("超限必须在读 body 之前拒绝（不等传完）")
	}
}

func TestTransferRejectsStreamOverLimit(t *testing.T) {
	ch := &fakeBuildCtxChannel{}
	svc := newTestBuildCtxService(ch, true)

	old := uploadMaxBytes
	uploadMaxBytes = 8
	t.Cleanup(func() { uploadMaxBytes = old })

	body := gzipBody(2 + 1024) // 流式中途触线（无 Content-Length 形态）
	_, err := svc.Transfer(context.Background(), 7, bytes.NewReader(body), -1)
	var ae *apperror.AppError
	if !errors.As(err, &ae) || ae.Code != apperror.CodeBadRequest {
		t.Fatalf("流式超限应 400: %v", err)
	}
	_, msgs := ch.snapshot()
	if len(msgs) == 0 || msgs[len(msgs)-1].Type != agentproto.TypeCoreDockerBuildCtxAbort {
		t.Fatal("流式超限必须补中止帧（agent 删半成品）")
	}
}
