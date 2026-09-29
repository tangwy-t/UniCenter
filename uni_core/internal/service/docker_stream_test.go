package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agenthub"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstream"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// fakeStreamSender 记录下行控制帧（CoreDockerFrame）与目标设备。
type fakeStreamSender struct {
	mu      sync.Mutex
	frames  []*agentproto.CoreDockerFrame
	devices []uint64
	offline bool
}

func (f *fakeStreamSender) SendToDevice(deviceID uint64, msg *agentproto.Message) error {
	if f.offline {
		return agenthub.ErrDeviceOffline
	}
	if msg.Type != agentproto.TypeCoreDockerFrame {
		return nil
	}
	var fr agentproto.CoreDockerFrame
	if err := msg.DecodeData(&fr); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.frames = append(f.frames, &fr)
	f.devices = append(f.devices, deviceID)
	return nil
}

func (f *fakeStreamSender) snapshot() []*agentproto.CoreDockerFrame {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*agentproto.CoreDockerFrame, len(f.frames))
	copy(out, f.frames)
	return out
}

type streamFixture struct {
	svc     *DockerStreamService
	reg     *dockerstream.Registry
	sender  *fakeStreamSender
	tickets *dockerstate.TicketStore
	redis   *miniredis.Miniredis
	now     *time.Time
}

func newStreamFixture(t *testing.T, idle time.Duration) *streamFixture {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	now := time.Unix(1700000000, 0)
	reg := dockerstream.NewRegistryWithOptions(dockerstream.Options{
		IdleTimeout: idle,
		Now:         func() time.Time { return now },
	}, nil)
	sender := &fakeStreamSender{}
	svc := NewDockerStreamService(reg, dockerstate.NewTicketStore(rdb), sender, nil)
	svc.now = func() time.Time { return now }
	return &streamFixture{svc: svc, reg: reg, sender: sender,
		tickets: dockerstate.NewTicketStore(rdb), redis: mr, now: &now}
}

const testSession = "sess-0123456789abcdef"

func (f *streamFixture) seedSession(t *testing.T, device, user uint64, action string) {
	t.Helper()
	if err := f.reg.Register(dockerstream.Meta{
		SessionID: testSession, DeviceID: device, UserID: user, Action: action,
		Ref: "123", Kind: dockerstream.KindForAction(action), CreatedAt: *f.now,
	}); err != nil {
		t.Fatal(err)
	}
}

// 签发：无会话不发；有会话则每次一张新票、绑定三要素、在 Redis 里可见。
func TestStreamIssueTicket(t *testing.T) {
	f := newStreamFixture(t, time.Minute)
	ctx := context.Background()

	if tk, err := f.svc.IssueTicket(ctx, &dockerstate.CmdRecord{Ref: "1", UserID: 1, DeviceID: 2}); err != nil || tk != "" {
		t.Fatalf("没有会话的指令不得签发票据: %q %v", tk, err)
	}
	if tk, err := f.svc.IssueTicket(ctx, nil); err != nil || tk != "" {
		t.Fatalf("nil 记录不得 panic/签发: %q %v", tk, err)
	}

	rec := &dockerstate.CmdRecord{Ref: "1", UserID: 42, DeviceID: 7, SessionID: testSession}
	tk1, err := f.svc.IssueTicket(ctx, rec)
	if err != nil || tk1 == "" {
		t.Fatalf("签发票据失败: %q %v", tk1, err)
	}
	tk2, err := f.svc.IssueTicket(ctx, rec)
	if err != nil || tk2 == "" {
		t.Fatalf("第二次签发失败: %q %v", tk2, err)
	}
	if tk1 == tk2 {
		t.Fatal("每次轮询响应都必须带一张**新**票据")
	}
	got, err := f.svc.RedeemTicket(ctx, tk1)
	if err != nil || got == nil || got.UserID != 42 || got.DeviceID != 7 || got.SessionID != testSession {
		t.Fatalf("票据兑换不符: %+v %v", got, err)
	}
	if again, err := f.svc.RedeemTicket(ctx, tk1); err != nil || again != nil {
		t.Fatalf("票据必须单次使用: %+v %v", again, err)
	}
}

// 接入的错误映射：归属不符 403、重复 409、不存在 404、路径设备不符 404。
func TestStreamAttachMapping(t *testing.T) {
	f := newStreamFixture(t, time.Minute)
	f.seedSession(t, 7, 42, agentproto.DockerActionContainerLogs)

	if _, err := f.svc.AttachSession(testSession, 43, 7); !isAppErr(err, apperror.CodeForbidden) {
		t.Fatalf("非发起人必须 403: %v", err)
	}
	sess, err := f.svc.AttachSession(testSession, 42, 7)
	if err != nil {
		t.Fatalf("发起人必须能接入: %v", err)
	}
	if sess.Kind() != dockerstream.KindLog {
		t.Fatalf("kind 不符: %v", sess.Kind())
	}
	if _, err := f.svc.AttachSession(testSession, 42, 7); !isAppErr(err, apperror.CodeConflict) {
		t.Fatalf("重复接入必须 409: %v", err)
	}
	f.svc.ReleaseSession(testSession)
	if _, err := f.svc.AttachSession("no-such-session-id", 42, 7); !isAppErr(err, apperror.CodeNotFound) {
		t.Fatalf("不存在的会话必须 404: %v", err)
	}
	// 路径设备与会话设备不符：拒绝且不留占用（会话可被正确的主机接入）。
	if _, err := f.svc.AttachSession(testSession, 42, 8); !isAppErr(err, apperror.CodeNotFound) {
		t.Fatalf("设备不符必须拒绝: %v", err)
	}
	if _, err := f.svc.AttachSession(testSession, 42, 7); err != nil {
		t.Fatalf("设备不符的失败不得留下占用: %v", err)
	}
}

// 控制帧：input/resize 原样到 agent；cancel 同时移除会话；非法 resize 被协议拦下。
func TestStreamControlFrames(t *testing.T) {
	f := newStreamFixture(t, time.Minute)
	f.seedSession(t, 7, 42, agentproto.DockerActionContainerExec)
	sess, err := f.svc.AttachSession(testSession, 42, 7)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if err := f.svc.SendInput(ctx, sess, []byte("ls\n")); err != nil {
		t.Fatalf("input 必须发出: %v", err)
	}
	if err := f.svc.SendResize(ctx, sess, 120, 40); err != nil {
		t.Fatalf("resize 必须发出: %v", err)
	}
	// 非法尺寸被协议校验拦下（不发给 agent 一帧它必须拒的东西）。
	if err := f.svc.SendResize(ctx, sess, 0, 0); err == nil {
		t.Fatal("非法 resize 必须被拦下")
	}
	if err := f.svc.SendInput(ctx, sess, nil); err != nil {
		t.Fatal("空输入是无操作，不是错误")
	}
	frames := f.sender.snapshot()
	if len(frames) != 2 {
		t.Fatalf("应有 input+resize 两帧，实际 %d: %+v", len(frames), frames)
	}
	if frames[0].Op != agentproto.DockerFrameOpInput || string(frames[0].Data) != "ls\n" ||
		frames[0].SessionID != testSession {
		t.Fatalf("input 帧不符: %+v", frames[0])
	}
	if frames[1].Op != agentproto.DockerFrameOpResize || frames[1].Cols != 120 || frames[1].Rows != 40 {
		t.Fatalf("resize 帧不符: %+v", frames[1])
	}
	if f.sender.devices[0] != 7 {
		t.Fatalf("控制帧必须发往会话设备: %v", f.sender.devices)
	}

	f.svc.CancelSession(ctx, sess, "测试取消")
	frames = f.sender.snapshot()
	if len(frames) != 3 || frames[2].Op != agentproto.DockerFrameOpCancel {
		t.Fatalf("cancel 必须下发: %+v", frames)
	}
	if f.reg.Get(testSession) != nil {
		t.Fatal("cancel 后会话必须从注册表移除")
	}
}

// sweep：空闲超时 → 下发 cancel 并清理；设备离线 → 清理（不发也送不到）。
func TestStreamSweep(t *testing.T) {
	f := newStreamFixture(t, 10*time.Minute)
	f.seedSession(t, 7, 42, agentproto.DockerActionContainerLogs)
	f.seedSession2(t, 8, 42) // 另一台设备（离线判定用）
	ctx := context.Background()

	*f.now = f.now.Add(11 * time.Minute)
	f.svc.WithDeviceOnline(func(deviceID uint64) bool { return deviceID != 8 })
	n, err := f.svc.Sweep(ctx)
	if err != nil || n != 2 {
		t.Fatalf("两条都应被清理: n=%d err=%v", n, err)
	}
	if f.reg.Len() != 0 {
		t.Fatalf("sweep 后注册表必须为空: %d", f.reg.Len())
	}
	cancels := 0
	for _, fr := range f.sender.snapshot() {
		if fr.Op == agentproto.DockerFrameOpCancel {
			cancels++
		}
	}
	if cancels != 2 {
		t.Fatalf("两条清理都应尝试下发 cancel: %+v", f.sender.snapshot())
	}
}

// seedSession2 登记第二条会话（sweep 的多会话场景）。
func (f *streamFixture) seedSession2(t *testing.T, device, user uint64) {
	t.Helper()
	if err := f.reg.Register(dockerstream.Meta{
		SessionID: "sess-ffffffffffffffff", DeviceID: device, UserID: user,
		Action: agentproto.DockerActionContainerLogs, Ref: "456",
		Kind: dockerstream.KindLog, CreatedAt: *f.now,
	}); err != nil {
		t.Fatal(err)
	}
}

func isAppErr(err error, code int) bool {
	var ae *apperror.AppError
	return errors.As(err, &ae) && ae.Code == code
}

// TestAgentIngestRegistersStreamSession：agent 的 result 带 session_id 时**登记会话**
// （三期接线），且只在 ok=true 且归属相符时登记 —— 帧随后到达就能找到主人，
// 轮询拿到记录后就能签票据。
func TestAgentIngestRegistersStreamSession(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()

	cmdStore := dockerstate.NewCmdStore(rdb)
	reg := dockerstream.NewRegistry(nil)
	svc := NewAgentIngestService(nil, nil, nil, nil, cmdStore, nil, logger.NewNop()).WithDockerSessions(reg)

	rec := &dockerstate.CmdRecord{Ref: "1790000000001", DeviceID: 7, Action: agentproto.DockerActionContainerExec,
		Target: "mysql", UserID: 42, Perm: "docker:exec"}
	if err := cmdStore.Create(ctx, rec, time.Minute); err != nil {
		t.Fatal(err)
	}

	// ① 归属不符（别的设备回这条 ref）：结果丢弃，会话不登记。
	if err := svc.CompleteDockerCmd(ctx, 8, &agentproto.DockerCmdResult{
		Ref: rec.Ref, OK: true, SessionID: "sess-0123456789abcdef"}); err != nil {
		t.Fatal(err)
	}
	if reg.Len() != 0 {
		t.Fatal("归属不符的结果不得登记会话")
	}

	// ② 正常结果：登记会话，元数据全部来自指令记录。
	if err := svc.CompleteDockerCmd(ctx, 7, &agentproto.DockerCmdResult{
		Ref: rec.Ref, OK: true, SessionID: "sess-0123456789abcdef"}); err != nil {
		t.Fatal(err)
	}
	sess := reg.Get("sess-0123456789abcdef")
	if sess == nil {
		t.Fatal("带 session_id 的结果必须登记会话")
	}
	if sess.DeviceID() != 7 || sess.UserID() != 42 || sess.Kind() != dockerstream.KindPTY ||
		sess.Action() != agentproto.DockerActionContainerExec || sess.Ref() != rec.Ref {
		t.Fatalf("会话元数据不符: %+v", sess)
	}

	// ③ 失败结果（ok=false）即使带 session_id 也不登记（协议上不可出现，防御性处理）。
	rec2 := &dockerstate.CmdRecord{Ref: "1790000000002", DeviceID: 7, Action: agentproto.DockerActionContainerLogs,
		Target: "mysql2", UserID: 42, Perm: "docker:inspect"}
	if err := cmdStore.Create(ctx, rec2, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := svc.CompleteDockerCmd(ctx, 7, &agentproto.DockerCmdResult{
		Ref: rec2.Ref, OK: false, Error: "起会话失败", SessionID: "sess-ffffffffffffffff"}); err != nil {
		t.Fatal(err)
	}
	if reg.Get("sess-ffffffffffffffff") != nil {
		t.Fatal("失败结果不得登记会话")
	}
}
