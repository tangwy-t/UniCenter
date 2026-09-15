package service

import (
	"context"
	"errors"
	"testing"

	goredis "github.com/redis/go-redis/v9"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
)

// ── 多设备夹具（既有 flushFixture 只覆盖单设备 flushDevID）──

func (f *flushFixture) seedFor(t *testing.T, deviceID uint64, pts ...agentproto.MetricsSample) {
	t.Helper()
	for i := range pts {
		if err := f.raw.Append(context.Background(), deviceID, &pts[i]); err != nil {
			t.Fatalf("seed raw append device=%d: %v", deviceID, err)
		}
	}
}

func (f *flushFixture) setCursorFor(t *testing.T, deviceID uint64, sec int64) {
	t.Helper()
	if err := f.rdb.Set(context.Background(), flushCursorKey(deviceID), sec, 0).Err(); err != nil {
		t.Fatalf("set cursor device=%d: %v", deviceID, err)
	}
}

func (f *flushFixture) cursorFor(t *testing.T, deviceID uint64) (int64, bool) {
	t.Helper()
	v, err := f.rdb.Get(context.Background(), flushCursorKey(deviceID)).Int64()
	if err == goredis.Nil {
		return 0, false
	}
	if err != nil {
		t.Fatalf("read cursor device=%d: %v", deviceID, err)
	}
	return v, true
}

// mustCursor 断言游标此刻的**精确值**（对齐后的秒数可逐字写死）。
func (f *flushFixture) mustCursor(t *testing.T, deviceID uint64, want int64) {
	t.Helper()
	got, ok := f.cursorFor(t, deviceID)
	if !ok {
		t.Fatalf("device=%d 的游标键不存在, want %d", deviceID, want)
	}
	if got != want {
		t.Fatalf("device=%d 的游标 = %d, want %d", deviceID, got, want)
	}
}

func (f *flushFixture) wideRowCount(t *testing.T, deviceID uint64) int64 {
	t.Helper()
	var n int64
	if err := f.db.Model(&entity.DeviceMetricWide{}).Where("device_id = ?", deviceID).Count(&n).Error; err != nil {
		t.Fatalf("count wide rows: %v", err)
	}
	return n
}

// backfillTarget 是测试侧独立算出的回溯起点（不复用实现的 alignDown，否则对齐写错也自洽）。
func backfillTarget(now int64, hours int) int64 {
	target := now - int64(hours)*3600
	if target < 0 {
		return 0
	}
	return alignDown300(target)
}

// backfillAt 是夹具时钟：now 落在 5min 边界 + 900s 处，reportInterval=10 → closeGrace=20s。
//
// 为什么必须是 +900 而不是 +600：`upper = alignDown(now−grace)` 是**半开**上界，
// now−grace 必须跨过下一个桶边界（=900s 处），最后一个已闭桶才是 flushBaseTS+300。
// 取 +600 时 upper 恰好等于 flushBaseTS+300，桶 flushBaseTS+300 反而还没「闭」。
const backfillAt = int64(flushBaseTS + 900)

// frozenWriter 是「第一次写就失败」的替身。
//
// 用途：flushDevice 一旦写失败就**立即返回且不写水位**，于是游标停在
// 「刚被 backfill 回退到」的那个值上 —— 这是观测回溯起点唯一稳定的手段
// （正常路径下重放会把水位一路推进到最后一个已闭桶，回退值就被抹掉了）。
func frozenWriter() *fakeBucketWriter {
	return &fakeBucketWriter{failOnCall: 1, failErr: errors.New("frozen: 注入写入失败以冻结水位")}
}

// ─ 核心语义：回退游标 → 重放已越过的桶 ─────────────────

// 「数据曾经落库、后来这行丢了（恢复旧备份 / 分区被回收后要求重放）」
// 是 flush **永远救不回来**的场景：游标已经越过那些桶，flush 只会从
// cursor+300 往后走。backfill 的唯一职责就是把这些桶重新读一遍。
func TestBackfillRewindsCursorAndReplaysLostBucket(t *testing.T) {
	f := newFlushFixture(t, backfillAt, 10, nil)
	ctx := context.Background()

	// 桶 flushBaseTS+300 里有一个点（它是当前唯一一个已闭桶）。
	f.seedFor(t, flushDevID, flushSample((flushBaseTS+300)*1000, 10, "/", 20, 40))
	f.setCursorFor(t, flushDevID, flushBaseTS)

	// 先跑一轮正常 flush：写入 1 个桶，游标前进到 flushBaseTS+300。
	stats, err := f.svc.FlushOnce(ctx)
	if err != nil {
		t.Fatalf("FlushOnce: %v", err)
	}
	if stats.BucketsWritten != 1 {
		t.Fatalf("前置 flush 写入桶数 = %d, want 1", stats.BucketsWritten)
	}
	f.mustCursor(t, flushDevID, flushBaseTS+300)

	// 模拟「这一行丢了」。
	if err := f.db.Where("device_id = ? AND bucket_ts = ?", flushDevID, flushBaseTS+300).
		Delete(&entity.DeviceMetricWide{}).Error; err != nil {
		t.Fatalf("删除 5m 行: %v", err)
	}
	if n := f.wideRowCount(t, flushDevID); n != 0 {
		t.Fatalf("删除后仍有 %d 行", n)
	}

	bs, err := f.svc.BackfillOnce(ctx, nil, 24)
	if err != nil {
		t.Fatalf("BackfillOnce: %v", err)
	}
	if bs.WindowHours != 24 {
		t.Fatalf("生效窗口 = %d 小时, want 24", bs.WindowHours)
	}
	if bs.CursorsRewound != 1 {
		t.Fatalf("被回退的游标数 = %d, want 1（游标在窗口之内，必须回退）", bs.CursorsRewound)
	}
	if bs.Flush.BucketsWritten != 1 {
		t.Fatalf("重放写入桶数 = %d, want 1（那个丢失的桶必须被重新写出来）", bs.Flush.BucketsWritten)
	}
	if n := f.wideRowCount(t, flushDevID); n != 1 {
		t.Fatalf("重放后 5m 行数 = %d, want 1", n)
	}
	if bs.DevicesScanned != 1 {
		t.Fatalf("参与回溯的设备数 = %d, want 1", bs.DevicesScanned)
	}
	// 重放之后水位回到「最后一个已闭桶」（重放本身也是一次正常的落库）。
	f.mustCursor(t, flushDevID, flushBaseTS+300)
}

// 关键安全性质：**绝不把游标往前推**。
//
// 游标比回溯窗口还旧（服务连续停机 > 24h、或 Redis 从很旧的备份恢复）时，
// 把游标「对齐」到今天−24h 会**永久跳过**中间那段桶 —— 它们是 5m 唯一真值的
// 一部分，跳过去就再也补不回来。此时正确做法是**什么都不做**：落后的游标由
// flush 自己的 `[cursor+300, now−grace)` 区间正常补齐（下面的断言就是证据）。
func TestBackfillNeverMovesCursorForward(t *testing.T) {
	f := newFlushFixture(t, backfillAt, 10, nil)
	ctx := context.Background()

	// 游标 26h 前（比 24h 窗口更旧），但桶里有数据 → flush 会一路补到今天。
	stale := backfillTarget(backfillAt, 26)
	f.seedFor(t, flushDevID, flushSample((flushBaseTS+300)*1000, 10, "/", 20, 40))
	f.setCursorFor(t, flushDevID, stale)

	bs, err := f.svc.BackfillOnce(ctx, nil, 24)
	if err != nil {
		t.Fatalf("BackfillOnce: %v", err)
	}
	if bs.CursorsRewound != 0 {
		t.Fatalf("被回退的游标数 = %d, want 0（目标起点比当前游标更新，回退会跳过中间桶）", bs.CursorsRewound)
	}
	// flush 自己把落后的游标补齐到「最后一个已闭桶」——这正是 backfill 不必
	// 重复实现「补齐落后游标」的依据（它是 flush 的既有语义，不是 backfill 的活）。
	f.mustCursor(t, flushDevID, flushBaseTS+300)
	if bs.Flush.BucketsWritten != 1 {
		t.Fatalf("落后游标的补齐写入桶数 = %d, want 1（空桶不算写入）", bs.Flush.BucketsWritten)
	}
}

// 游标恰好等于目标起点：无事可做（不写 Redis，省掉一轮无谓的 SET）。
func TestBackfillSkipsWhenCursorAlreadyAtTarget(t *testing.T) {
	f := newFlushFixture(t, backfillAt, 10, nil)

	f.seedFor(t, flushDevID, flushSample((flushBaseTS+300)*1000, 10, "/", 20, 40))
	f.setCursorFor(t, flushDevID, backfillTarget(backfillAt, 24))

	bs, err := f.svc.BackfillOnce(context.Background(), nil, 24)
	if err != nil {
		t.Fatalf("BackfillOnce: %v", err)
	}
	if bs.CursorsRewound != 0 {
		t.Fatalf("被回退的游标数 = %d, want 0（相等不该算回退）", bs.CursorsRewound)
	}
	if bs.Flush.BucketsWritten != 1 {
		t.Fatalf("重放写入桶数 = %d, want 1", bs.Flush.BucketsWritten)
	}
}

// ── 窗口夹取：raw 点只保留 24h，更长的窗口全是空桶 ───────

func TestBackfillClampsWindowToRawRetention(t *testing.T) {
	cases := []struct {
		in       int
		wantWin  int
		wantFrom int64
	}{
		{0, 24, backfillTarget(backfillAt, 24)},   // 缺省 = 用满保留窗口
		{1, 1, backfillTarget(backfillAt, 1)},     // 显式短窗
		{6, 6, backfillTarget(backfillAt, 6)},     // 显式短窗
		{100, 24, backfillTarget(backfillAt, 24)}, // 超长窗夹到保留期
	}
	for _, c := range cases {
		// 冻结水位（写失败即中止）才能逐字断言**回溯起点**。
		f := newFlushFixture(t, backfillAt, 10, frozenWriter())
		f.seedFor(t, flushDevID, flushSample((flushBaseTS+300)*1000, 10, "/", 20, 40))
		f.setCursorFor(t, flushDevID, flushBaseTS+300)

		bs, err := f.svc.BackfillOnce(context.Background(), nil, c.in)
		if !errors.Is(err, ErrFlushPartial) {
			t.Fatalf("hours=%d: 冻结写入必须让重放报 ErrFlushPartial, got %v", c.in, err)
		}
		if bs.WindowHours != c.wantWin {
			t.Fatalf("hours=%d 的生效窗口 = %d, want %d", c.in, bs.WindowHours, c.wantWin)
		}
		if bs.CursorsRewound != 1 {
			t.Fatalf("hours=%d 被回退的游标数 = %d, want 1", c.in, bs.CursorsRewound)
		}
		f.mustCursor(t, flushDevID, c.wantFrom)
	}
}

// 回溯起点必须夹到 0 下界：unix 秒原点之前的水位会被真的写进 Redis，
// 且与「从 0 起步」在语义上无法区分（同 Bootstrap 的 bootstrapStart）。
func TestBackfillWindowClampsBelowEpoch(t *testing.T) {
	f := newFlushFixture(t, 3600, 10, frozenWriter()) // now = 纪元后 1 小时
	f.seedFor(t, flushDevID, flushSample(300*1000, 10, "/", 20, 40))
	f.setCursorFor(t, flushDevID, 300)

	bs, err := f.svc.BackfillOnce(context.Background(), nil, 24)
	if !errors.Is(err, ErrFlushPartial) {
		t.Fatalf("冻结写入必须让重放报 ErrFlushPartial, got %v", err)
	}
	if bs.CursorsRewound != 1 {
		t.Fatalf("被回退的游标数 = %d, want 1", bs.CursorsRewound)
	}
	f.mustCursor(t, flushDevID, 0)
}

// 时间对齐守卫：回溯起点必须落在 5min 栅格上 —— 否则 flush 的
// `[cursor+300, upper)` 会从一个半桶开始，第一个桶永远错位。
func TestBackfillTargetIsAlignedToBucketGrid(t *testing.T) {
	at := backfillAt + 137 // now 故意不在栅格上
	f := newFlushFixture(t, at, 10, frozenWriter())
	f.seedFor(t, flushDevID, flushSample((flushBaseTS+300)*1000, 10, "/", 20, 40))
	f.setCursorFor(t, flushDevID, flushBaseTS+300)

	if _, err := f.svc.BackfillOnce(context.Background(), nil, 24); !errors.Is(err, ErrFlushPartial) {
		t.Fatalf("冻结写入必须让重放报 ErrFlushPartial, got %v", err)
	}
	got, ok := f.cursorFor(t, flushDevID)
	if !ok {
		t.Fatal("游标键不存在")
	}
	if got%300 != 0 {
		t.Fatalf("回溯后的游标 = %d，未对齐到 300s 栅格", got)
	}
	if want := backfillTarget(at, 24); got != want {
		t.Fatalf("回溯后的游标 = %d, want %d", got, want)
	}
}

// ── 定向：device_ids 只控制「回退哪些游标」 ──────────────

func TestBackfillScopesRewindToGivenDevices(t *testing.T) {
	const otherDevID = uint64(2002)

	f := newFlushFixture(t, backfillAt, 10, frozenWriter())
	f.seedFor(t, flushDevID, flushSample((flushBaseTS+300)*1000, 10, "/", 20, 40))
	f.seedFor(t, otherDevID, flushSample((flushBaseTS+300)*1000, 11, "/", 21, 41))
	f.setCursorFor(t, flushDevID, flushBaseTS+300)
	f.setCursorFor(t, otherDevID, flushBaseTS+300)

	bs, err := f.svc.BackfillOnce(context.Background(), []uint64{flushDevID}, 24)
	if !errors.Is(err, ErrFlushPartial) {
		t.Fatalf("冻结写入必须让重放报 ErrFlushPartial, got %v", err)
	}
	if bs.DevicesScanned != 2 {
		t.Fatalf("参与枚举的设备数 = %d, want 2（device_ids 只缩小回退范围，不缩小枚举）", bs.DevicesScanned)
	}
	if bs.CursorsRewound != 1 {
		t.Fatalf("被回退的游标数 = %d, want 1（只有被点名的设备）", bs.CursorsRewound)
	}
	f.mustCursor(t, flushDevID, backfillTarget(backfillAt, 24))
	f.mustCursor(t, otherDevID, flushBaseTS+300)
}

// 点名的设备不在活跃索引里：不报错、也不计入回退数（运维删了设备之后
// 那个 job 的 params 可能还留着旧 id，不该因此让整个任务失败）。
func TestBackfillIgnoresUnknownDeviceIDs(t *testing.T) {
	f := newFlushFixture(t, backfillAt, 10, nil)
	f.seedFor(t, flushDevID, flushSample((flushBaseTS+300)*1000, 10, "/", 20, 40))
	f.setCursorFor(t, flushDevID, flushBaseTS+300)

	bs, err := f.svc.BackfillOnce(context.Background(), []uint64{999999}, 24)
	if err != nil {
		t.Fatalf("BackfillOnce: %v", err)
	}
	if bs.CursorsRewound != 0 {
		t.Fatalf("被回退的游标数 = %d, want 0", bs.CursorsRewound)
	}
	f.mustCursor(t, flushDevID, flushBaseTS+300)
}

// ─ 错误与读数 ──────────────────────────────────────────

// 回退发生之后重放失败：错误必须上抛（任务要记 job_log），且 stats 里
// 能读到「回退过 1 个游标 + 1 台设备失败」——运维据此知道下轮会重试同一批桶。
func TestBackfillReportsFlushFailureAfterRewind(t *testing.T) {
	f := newFlushFixture(t, backfillAt, 10, frozenWriter())
	f.seedFor(t, flushDevID, flushSample((flushBaseTS+300)*1000, 10, "/", 20, 40))
	f.setCursorFor(t, flushDevID, flushBaseTS+300)

	bs, err := f.svc.BackfillOnce(context.Background(), nil, 24)
	if !errors.Is(err, ErrFlushPartial) {
		t.Fatalf("重放失败必须上抛 ErrFlushPartial，got %v", err)
	}
	if bs.CursorsRewound != 1 {
		t.Fatalf("被回退的游标数 = %d, want 1", bs.CursorsRewound)
	}
	if bs.Flush.Errors != 1 {
		t.Fatalf("失败设备数 = %d, want 1", bs.Flush.Errors)
	}
}

// 没有任何活跃设备（Redis 空）：不报错、不回退、重放读数为 0。
func TestBackfillWithNoActiveDevicesIsNoop(t *testing.T) {
	f := newFlushFixture(t, backfillAt, 10, nil)

	bs, err := f.svc.BackfillOnce(context.Background(), nil, 24)
	if err != nil {
		t.Fatalf("BackfillOnce: %v", err)
	}
	if bs.DevicesScanned != 0 || bs.CursorsRewound != 0 || bs.Flush.BucketsWritten != 0 {
		t.Fatalf("空 Redis 的读数应为全 0, got %+v", bs)
	}
	if bs.WindowHours != 24 {
		t.Fatalf("生效窗口 = %d, want 24（没有设备也要报出本轮窗口，否则日志看不出用的是什么窗口）", bs.WindowHours)
	}
}
