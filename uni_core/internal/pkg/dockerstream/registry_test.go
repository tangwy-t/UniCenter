package dockerstream

import (
	"context"
	"errors"
	"testing"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── 夹具 ────────────────────────────────────────────────────────────────

func testMeta(id string, device, user uint64) Meta {
	return Meta{SessionID: id, DeviceID: device, UserID: user,
		Action: agentproto.DockerActionContainerLogs, Ref: "123", Kind: KindLog}
}

func frame(session string, seq uint64, data string, eof bool) *agentproto.DockerFrame {
	return &agentproto.DockerFrame{SessionID: session, Seq: seq, Data: []byte(data), EOF: eof}
}

// newClockedRegistry 构造一个时钟可推进的注册表（空闲超时要能被确定性驱动）。
func newClockedRegistry(t *testing.T, opts Options) (*Registry, *time.Time) {
	t.Helper()
	now := time.Unix(1700000000, 0)
	opts.Now = func() time.Time { return now }
	return NewRegistryWithOptions(opts, nil), &now
}

// ── 投递与消费 ──────────────────────────────────────────────────────────

// 帧按序进缓冲、按序被消费；eof 帧之后会话收尾（取空即 ErrClosed）。
func TestRegistryDeliverDrainAndEOF(t *testing.T) {
	r, _ := newClockedRegistry(t, Options{})
	if err := r.Register(testMeta("sess-0123456789abcdef", 7, 42)); err != nil {
		t.Fatal(err)
	}
	if err := r.DeliverDockerFrame(context.Background(), 7, frame("sess-0123456789abcdef", 1, "hello ", false)); err != nil {
		t.Fatal(err)
	}
	if err := r.DeliverDockerFrame(context.Background(), 7, frame("sess-0123456789abcdef", 2, "world\n", true)); err != nil {
		t.Fatal(err)
	}
	sess, err := r.Attach("sess-0123456789abcdef", 42)
	if err != nil {
		t.Fatal(err)
	}
	f, err := sess.Next(context.Background())
	if err != nil || f.Seq != 1 || string(f.Data) != "hello " {
		t.Fatalf("第一帧不符: %+v %v", f, err)
	}
	f, err = sess.Next(context.Background())
	if err != nil || f.Seq != 2 || !f.EOF || string(f.Data) != "world\n" {
		t.Fatalf("第二帧（eof）不符: %+v %v", f, err)
	}
	if _, err := sess.Next(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("eof 取空后必须 ErrClosed: %v", err)
	}
	// 收尾后（释放占用）不得再接入：缓冲已排空，流已经结束。
	r.Release("sess-0123456789abcdef")
	if _, err := r.Attach("sess-0123456789abcdef", 42); !errors.Is(err, ErrClosed) {
		t.Fatalf("已收尾的会话不得再接入: %v", err)
	}
}

// 缓冲满**丢最旧**而不是阻塞：投递路径在 agent 读循环里，堵住它会连累指标/心跳。
func TestRegistryOverflowDropsOldestWithoutBlocking(t *testing.T) {
	r, _ := newClockedRegistry(t, Options{QueueDepth: 2})
	if err := r.Register(testMeta("sess-0123456789abcdef", 7, 42)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for seq := uint64(1); seq <= 3; seq++ {
		if err := r.DeliverDockerFrame(ctx, 7, frame("sess-0123456789abcdef", seq, "x", false)); err != nil {
			t.Fatalf("投递第 %d 帧不得失败/阻塞: %v", seq, err)
		}
	}
	sess, _ := r.Attach("sess-0123456789abcdef", 42)
	if sess.Dropped() != 1 {
		t.Fatalf("溢出必须丢最旧并计数，dropped=%d", sess.Dropped())
	}
	f, _ := sess.Next(ctx)
	if f.Seq != 2 {
		t.Fatalf("丢的必须是最旧帧（剩 2,3），got seq=%d", f.Seq)
	}
}

// 一台设备发来别人的 session_id：丢弃 + ErrForbidden（伪造/串号信号），不落进任何会话。
func TestRegistryDeliverRejectsDeviceMismatch(t *testing.T) {
	r, _ := newClockedRegistry(t, Options{})
	if err := r.Register(testMeta("sess-0123456789abcdef", 7, 42)); err != nil {
		t.Fatal(err)
	}
	err := r.DeliverDockerFrame(context.Background(), 8, frame("sess-0123456789abcdef", 1, "x", false))
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("设备不符必须 ErrForbidden: %v", err)
	}
	if s := r.Get("sess-0123456789abcdef"); s.Buffered() != 0 {
		t.Fatal("设备不符的帧不得进缓冲")
	}
	if err := r.DeliverDockerFrame(context.Background(), 7, frame("no-such-session-id", 1, "x", false)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未知会话必须 ErrNotFound: %v", err)
	}
}

// eof 先到、客户端后接：缓冲里的数据（含 eof）必须能完整重放 ——
// 「短命的 follow 在轮询拿到票据前就结束了」是真实时序。
func TestRegistryAttachAfterEOFReplaysBuffer(t *testing.T) {
	r, _ := newClockedRegistry(t, Options{})
	if err := r.Register(testMeta("sess-0123456789abcdef", 7, 42)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := r.DeliverDockerFrame(ctx, 7, frame("sess-0123456789abcdef", 1, "done\n", true)); err != nil {
		t.Fatal(err)
	}
	sess, err := r.Attach("sess-0123456789abcdef", 42)
	if err != nil {
		t.Fatalf("eof 后仍应允许接入（缓冲未排空）: %v", err)
	}
	f, err := sess.Next(ctx)
	if err != nil || f.Seq != 1 || !f.EOF || string(f.Data) != "done\n" {
		t.Fatalf("缓冲必须原样重放: %+v %v", f, err)
	}
	if _, err := sess.Next(ctx); !errors.Is(err, ErrClosed) {
		t.Fatalf("取空后必须 ErrClosed: %v", err)
	}
}

// ── 接入语义 ────────────────────────────────────────────────────────────

// 接入是排他的（第二个 409 语义）且归属严格（非发起人 403 语义）。
func TestRegistryAttachExclusiveAndOwnership(t *testing.T) {
	r, _ := newClockedRegistry(t, Options{})
	if err := r.Register(testMeta("sess-0123456789abcdef", 7, 42)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Attach("sess-0123456789abcdef", 43); !errors.Is(err, ErrForbidden) {
		t.Fatalf("非发起人必须 ErrForbidden: %v", err)
	}
	if _, err := r.Attach("sess-0123456789abcdef", 42); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Attach("sess-0123456789abcdef", 42); !errors.Is(err, ErrBusy) {
		t.Fatalf("重复接入必须 ErrBusy: %v", err)
	}
	// 释放后可重接（升级失败/一次断线重试的路径）。
	r.Release("sess-0123456789abcdef")
	if _, err := r.Attach("sess-0123456789abcdef", 42); err != nil {
		t.Fatalf("释放后必须能重接: %v", err)
	}
	r.Remove("sess-0123456789abcdef")
	if _, err := r.Attach("sess-0123456789abcdef", 42); !errors.Is(err, ErrNotFound) {
		t.Fatalf("移除后必须 ErrNotFound: %v", err)
	}
}

// Next 阻塞时必须能被 Remove 唤醒（客户端断开/取消的收尾路径）。
func TestRegistryRemoveUnblocksConsumer(t *testing.T) {
	r, _ := newClockedRegistry(t, Options{})
	if err := r.Register(testMeta("sess-0123456789abcdef", 7, 42)); err != nil {
		t.Fatal(err)
	}
	sess, err := r.Attach("sess-0123456789abcdef", 42)
	if err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, 1)
	go func() {
		_, err := sess.Next(context.Background())
		errCh <- err
	}()
	time.Sleep(10 * time.Millisecond) // 让消费侧进入等待
	r.Remove("sess-0123456789abcdef")
	select {
	case err := <-errCh:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("Remove 后 Next 必须 ErrClosed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Remove 未唤醒等待中的消费者")
	}
}

// ── 上限与清理 ──────────────────────────────────────────────────────────

// 每设备上限溢出时**淘汰最旧**（自愈），新会话必须可用。
func TestRegistryPerDeviceLimitEvictsOldest(t *testing.T) {
	r, _ := newClockedRegistry(t, Options{MaxSessionsPerDevice: 2})
	base := time.Unix(1700000000, 0)
	for i, id := range []string{"sess-0000000000000001", "sess-0000000000000002", "sess-0000000000000003"} {
		m := testMeta(id, 7, 42)
		m.CreatedAt = base.Add(time.Duration(i) * time.Second)
		if err := r.Register(m); err != nil {
			t.Fatal(err)
		}
	}
	if r.CountByDevice(7) != 2 {
		t.Fatalf("每设备只允许 2 条（测试配置），实际 %d", r.CountByDevice(7))
	}
	if r.Get("sess-0000000000000001") != nil {
		t.Fatal("溢出时必须淘汰最旧的一条")
	}
	if r.Get("sess-0000000000000003") == nil {
		t.Fatal("新会话必须登记成功（否则用户刚建的终端打不开）")
	}
	// 淘汰的那条即使有等待中的消费者也会被唤醒（ErrClosed）。
	// 这里只验证它已从注册表消失：投递得到 ErrNotFound。
	if err := r.DeliverDockerFrame(context.Background(), 7, frame("sess-0000000000000001", 1, "x", false)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("被淘汰的会话不能再收帧: %v", err)
	}
}

// 空闲超时按「无数据」判定；投递与控制帧都会刷新活动时刻。
func TestRegistryIdleExpiry(t *testing.T) {
	r, now := newClockedRegistry(t, Options{IdleTimeout: 10 * time.Minute})
	if err := r.Register(testMeta("sess-0123456789abcdef", 7, 42)); err != nil {
		t.Fatal(err)
	}
	if got := r.Expired(*now); len(got) != 0 {
		t.Fatal("刚登记不该过期")
	}
	*now = now.Add(9 * time.Minute)
	if got := r.Expired(*now); len(got) != 0 {
		t.Fatal("未到 10 分钟不该过期")
	}
	*now = now.Add(2 * time.Minute)
	got := r.Expired(*now)
	if len(got) != 1 || got[0].ID() != "sess-0123456789abcdef" {
		t.Fatalf("超过空闲时限必须过期: %+v", got)
	}
	// 有数据来往即刷新：再投一帧后又回到「未过期」。
	r2, now2 := newClockedRegistry(t, Options{IdleTimeout: 10 * time.Minute})
	if err := r2.Register(testMeta("sess-0123456789abcdef", 7, 42)); err != nil {
		t.Fatal(err)
	}
	*now2 = now2.Add(9 * time.Minute)
	if err := r2.DeliverDockerFrame(context.Background(), 7, frame("sess-0123456789abcdef", 1, "x", false)); err != nil {
		t.Fatal(err)
	}
	*now2 = now2.Add(2 * time.Minute)
	if got := r2.Expired(*now2); len(got) != 0 {
		t.Fatal("刚有数据来往不该判空闲超时")
	}
}

// 登记参数不全必须拒绝（0 号设备/空 session/0 号用户都是编程错误）。
func TestRegistryRegisterRejectsBadMeta(t *testing.T) {
	r, _ := newClockedRegistry(t, Options{})
	if err := r.Register(Meta{}); err == nil {
		t.Fatal("空元数据必须拒绝")
	}
	if err := r.Register(testMeta("sess-0123456789abcdef", 7, 42)); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(testMeta("sess-0123456789abcdef", 7, 42)); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("重复 session_id 必须拒绝（串号信号）: %v", err)
	}
}

// KindForAction：exec 是终端、stats 是统计流，其余（含日志 Follow 与未知 action）
// 按日志处理。
func TestKindForAction(t *testing.T) {
	if KindForAction(agentproto.DockerActionContainerExec) != KindPTY {
		t.Fatal("exec 必须判为终端会话")
	}
	if KindForAction(agentproto.DockerActionContainerStats) != KindStats {
		t.Fatal("stats 必须判为统计流会话")
	}
	if KindForAction(agentproto.DockerActionContainerLogs) != KindLog {
		t.Fatal("logs 必须判为日志会话")
	}
	if KindForAction("some:unknown") != KindLog {
		t.Fatal("未知 action 按日志兜底（少登记比错登记更糟）")
	}
}

// ── 拉取进度会话（4b）───────────────────────────────────────────────────

// TestRegistryPullExemptFromDeviceLimitAndIdle：拉取进度会话不进「每设备 3 条用户流」
// 的账目（不占号、不被淘汰、不算上限），且豁免空闲判定期 —— 它的寿命 = 指令寿命
// （排队几十分钟一帧没有是常态），由 docker_stream 服务的终态对账回收；空闲清退对它
// 的语义是「腰斩一场合法的拉取」。
func TestRegistryPullExemptFromDeviceLimitAndIdle(t *testing.T) {
	r, now := newClockedRegistry(t, Options{MaxSessionsPerDevice: 2, IdleTimeout: 10 * time.Minute})
	base := *now
	for i, id := range []string{"sess-0000000000000001", "sess-0000000000000002"} {
		m := testMeta(id, 7, 42)
		m.CreatedAt = base.Add(time.Duration(i) * time.Second)
		if err := r.Register(m); err != nil {
			t.Fatal(err)
		}
	}
	// 用户流已满额时拉取会话必须照常登记（它是写指令的一部分，不是用户又开了一条流）。
	if err := r.Register(Meta{
		SessionID: "pull_1790000000000000001", DeviceID: 7, UserID: 42,
		Action: agentproto.DockerActionImagePull, Ref: "1790000000000000001",
		Kind: KindPull, CreatedAt: *now,
	}); err != nil {
		t.Fatal(err)
	}
	if r.Get("sess-0000000000000001") == nil || r.Get("sess-0000000000000002") == nil {
		t.Fatal("拉取会话不得挤掉任何一条用户流")
	}
	if got := r.CountByDevice(7); got != 2 {
		t.Fatalf("用户流账目不得计入拉取会话（受理预检的 409 才会正确）: %d", got)
	}

	// 空闲多久都不过期（豁免只给拉取会话）：24 小时后两条用户流照常过期，
	// 拉取会话不在其列。
	*now = now.Add(24 * time.Hour)
	got := r.Expired(*now)
	if len(got) != 2 {
		t.Fatalf("用户流照常按空闲过期，实际 %d 条", len(got))
	}
	for _, s := range got {
		if s.ID() == "pull_1790000000000000001" {
			t.Fatal("拉取会话豁免空闲判定期，不得出现在过期名单里")
		}
	}

	// 帧照常投递（device 归属校验不因 kind 放宽）：拉取会话自己收得到帧。
	if err := r.DeliverDockerFrame(context.Background(), 7,
		frame("pull_1790000000000000001", 1, "x", false)); err != nil {
		t.Fatalf("拉取会话必须能收帧: %v", err)
	}
	if err := r.DeliverDockerFrame(context.Background(), 8,
		frame("pull_1790000000000000001", 2, "x", false)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("异设备的同句柄帧必须被拒（归属不因 kind 放宽）: %v", err)
	}
}
