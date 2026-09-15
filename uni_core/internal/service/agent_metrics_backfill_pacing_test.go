package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
)

// ─ S4：回填必须限速（spec §7.1「按设备/桶分批 + 批间节奏」）──

// TestBackfillSplitsDevicesIntoPacedBatches 钉住「分批 + 批间节奏」真的生效。
//
// 为什么必须能观测：限速是一个**只在 DB 侧体现**的隐变量 —— 实现退化成「一次性
// 全量回放」时，所有既有断言（回退了几个游标、重放写了几个桶）都照旧全绿，
// 只有线上 DB 会感觉到。故这里把批数与停顿次数记进读数、把 sleeper 换成观测器，
// 让「这一轮切了几批、停了几次、每次多久」成为可直接断言的量。
//
// 断言三件事：
//  1. 批数 = ceil(设备数 / 批大小)，停顿次数 = 批数 − 1（**最后一批之后不再停顿**）；
//  2. 每次停顿的时长就是注入的节奏值（不是硬编码的 sleep）；
//  3. **切批不改变落库范围**：所有设备都被重放，水位都重新推进到最后一个已闭桶。
func TestBackfillSplitsDevicesIntoPacedBatches(t *testing.T) {
	ctx := context.Background()
	// 批大小在这里**刻意调小**（每件默认值由 TestBackfillPacingDefaultsAreConservative 钉住）：
	// 满窗口回填是「288 桶/设备」，用默认的 50 台/批要造 120 台设备、跑十几秒；
	// 切批是**正确性无关**的限速参数，调小它不改变被测逻辑，只让测试快一个数量级。
	const (
		batchSize   = 5
		deviceCount = 12 // 12 / 5 = 3 批（2.4 → 向上取整）
	)
	f := newFlushFixture(t, backfillAt, 10, nil)
	f.svc.backfillBatchDevices = batchSize
	ids := make([]uint64, 0, deviceCount)
	for i := 0; i < deviceCount; i++ {
		id := uint64(3000 + i)
		ids = append(ids, id)
		f.seedFor(t, id, flushSample((flushBaseTS+300)*1000, 20, "/", 20, 60))
		f.setCursorFor(t, id, flushBaseTS+300)
	}

	// 注入节奏与观测器：**不真 sleep**（真等 3s×2 只会让测试变慢，且观测不到次数）。
	const pacing = 3 * time.Second
	var waits []time.Duration
	f.svc.WithBackfillPacing(pacing)
	f.svc.sleep = func(d time.Duration) { waits = append(waits, d) }

	stats, err := f.svc.BackfillOnce(ctx, nil, 24)
	if err != nil {
		t.Fatalf("BackfillOnce: %v", err)
	}

	wantBatches := (deviceCount + batchSize - 1) / batchSize
	if stats.Batches != wantBatches {
		t.Fatalf("批数 = %d, want %d（设备 %d 台 / 每批 %d 台，向上取整）",
			stats.Batches, wantBatches, deviceCount, batchSize)
	}
	if wantBatches < 2 {
		t.Fatalf("夹具失效：批数 %d < 2，本测试要断言的是「多批 + 批间停顿」", wantBatches)
	}
	if stats.PacingWaits != wantBatches-1 {
		t.Fatalf("批间停顿次数 = %d, want %d（= 批数 − 1：最后一批之后不得再等）",
			stats.PacingWaits, wantBatches-1)
	}
	if len(waits) != stats.PacingWaits {
		t.Fatalf("观测到的停顿次数 = %d，读数里的 PacingWaits = %d（两者必须一致：读数是真实发生的次数，不是推算值）",
			len(waits), stats.PacingWaits)
	}
	for i, d := range waits {
		if d != pacing {
			t.Fatalf("第 %d 次停顿 = %v, want %v（必须用注入的节奏，而不是硬编码的 sleep）", i+1, d, pacing)
		}
	}

	// 切批不改语义：回退与重放覆盖**全部**被枚举到的设备。
	if stats.DevicesScanned != deviceCount {
		t.Fatalf("DevicesScanned = %d, want %d", stats.DevicesScanned, deviceCount)
	}
	if stats.CursorsRewound != deviceCount {
		t.Fatalf("被回退的游标数 = %d, want %d（device_ids 为空 = 全部设备）", stats.CursorsRewound, deviceCount)
	}
	if stats.Flush.DevicesScanned != deviceCount {
		t.Fatalf("重放扫描设备数 = %d, want %d（分批累加必须覆盖全部批次）", stats.Flush.DevicesScanned, deviceCount)
	}
	if stats.Flush.BucketsWritten != deviceCount {
		t.Fatalf("重放写入桶数 = %d, want %d（每台一个已闭桶）", stats.Flush.BucketsWritten, deviceCount)
	}
	for _, id := range ids {
		f.mustCursor(t, id, flushBaseTS+300)
	}
}

// TestBackfillPacingDefaultsAreConservative 钉住默认参数的**量级**（而不是钉死数值）。
//
// 为什么钉量级而不是字面量：默认值的依据是「一批的 DB 影响有界、且总时长可接受」
// （见 defaultBackfillBatchDevices 的注释），那是量级论证，不是某个魔数。
// 但两条退化必须被拦住：
//   - 批大小退化成 1（等于没分批，批数与停顿次数爆掉）或退化成极大（等于一次性突发）；
//   - 节奏退化成 0/极大（0 = 限速形同不存在；极大 = 24h 回填跑不完）。
func TestBackfillPacingDefaultsAreConservative(t *testing.T) {
	if defaultBackfillBatchDevices < 10 || defaultBackfillBatchDevices > 200 {
		t.Fatalf("默认批大小 = %d 台，超出保守区间 [10,200]（1 = 没分批，500 = 一次突发）",
			defaultBackfillBatchDevices)
	}
	if defaultBackfillPacing <= 0 || defaultBackfillPacing > 30*time.Second {
		t.Fatalf("默认批间节奏 = %v，超出保守区间 (0,30s]", defaultBackfillPacing)
	}
	// 生产装配（不注入）必须真的带上这两个默认值 —— 否则「限速」只存在于注释里。
	svc, _, _ := newBareFlushService(t, time.Unix(1800000000, 0), 10)
	if svc.pacing != defaultBackfillPacing || svc.backfillBatchDevices != defaultBackfillBatchDevices {
		t.Fatalf("装配后的限速参数 = (%v, %d 台), want (%v, %d 台)",
			svc.pacing, svc.backfillBatchDevices, defaultBackfillPacing, defaultBackfillBatchDevices)
	}
	if svc.sleep == nil {
		t.Fatal("sleep 未装配：批间节奏会被静默跳过")
	}
}

// TestWithBackfillPacingRejectsNonPositive 钉住「传 0 不是关掉限速」
// （照 WithEmptyHourGrace / WithMaxRepairHours 的先例：0 最可能的意思是「忘了填」）。
func TestWithBackfillPacingRejectsNonPositive(t *testing.T) {
	svc, _, _ := newBareFlushService(t, time.Unix(1800000000, 0), 10)

	svc.WithBackfillPacing(0).WithBackfillPacing(-time.Second)
	if svc.pacing != defaultBackfillPacing {
		t.Fatalf("非正数节奏把默认值改成了 %v（应保持 %v）", svc.pacing, defaultBackfillPacing)
	}
	svc.WithBackfillPacing(7 * time.Second)
	if svc.pacing != 7*time.Second {
		t.Fatalf("正数节奏未生效：%v", svc.pacing)
	}
}

// TestSplitDevicesKeepsOrderAndCoversEveryone 是切批本身的守卫：
// 向上取整、顺序不变、**不丢设备也不重复**（切批时丢一台 = 那台设备本轮静默不重放）。
func TestSplitDevicesKeepsOrderAndCoversEveryone(t *testing.T) {
	devices := make([]uint64, 0, 11)
	for i := uint64(1); i <= 11; i++ {
		devices = append(devices, i)
	}

	batches := splitDevices(devices, 5)
	if len(batches) != 3 {
		t.Fatalf("批数 = %d, want 3（11 台 / 每批 5 台，向上取整）", len(batches))
	}
	if len(batches[2]) != 1 {
		t.Fatalf("最后一批 = %d 台, want 1（余数批）", len(batches[2]))
	}
	flat := make([]uint64, 0, len(devices))
	for _, b := range batches {
		flat = append(flat, b...)
	}
	if len(flat) != len(devices) {
		t.Fatalf("切批后设备数 = %d, want %d（不得丢设备）", len(flat), len(devices))
	}
	for i := range devices {
		if flat[i] != devices[i] {
			t.Fatalf("切批后顺序变了：位置 %d = %d, want %d", i, flat[i], devices[i])
		}
	}

	// 边界：空列表 → 0 批（不是「1 个空批」：空批会让 PacingWaits 多算一次停顿）。
	if got := splitDevices(nil, 5); got != nil {
		t.Fatalf("空设备列表切出 %d 批, want 0 批", len(got))
	}
	// 边界：批大小非法 → 整批一批（不 panic、不丢设备）。
	for _, n := range []int{0, -1} {
		got := splitDevices(devices, n)
		if len(got) != 1 || len(got[0]) != len(devices) {
			t.Fatalf("批大小 %d 时切出 %d 批，want 1 批含全部设备", n, len(got))
		}
	}
}

// ── S5：flush 命中「分区缺失」→ P1 + 中止本轮（spec §7.3）──

// bucketWriteCall 是一次 WriteBucket 尝试（含失败的那次）。
type bucketWriteCall struct {
	deviceID uint64
	bucketTS int64
}

// partitionFailWriter 是 DeviceMetricWriter 的替身：**每一次**调用都记账并返回
// 注入的「缺分区」错误。
//
// 为什么不用 fakeBucketWriter（它也有 failErr）：它只在**成功**的调用上记账，
// 而本测试要数的是「尝试了几次」—— 失败的那次恰恰是唯一被尝试的一次。
type partitionFailWriter struct {
	err   error
	calls []bucketWriteCall
}

func (w *partitionFailWriter) WriteBucket(_ context.Context, row *entity.DeviceMetricWide,
	_ repository.MetricSubRows) error {

	w.calls = append(w.calls, bucketWriteCall{deviceID: row.DeviceID, bucketTS: row.BucketTS})
	return w.err
}

// TestFlushAbortsRoundOnMissingPartition 是 S5 的核心断言：
// 注入「假的分区缺失错误」→ 只尝试了**第一个**桶就中止，且错误可被 errors.Is 识别。
//
// 为什么必须中止整轮（而不是逐桶/逐设备失败刷日志）：spec §7.3 写明缺分区的故障域是
// **整个集群的写入**（同一分钟里 500 台一起失败、8 路并发同时报错、有哨兵才退化成
// 「分区变粗」）。此时继续跑 = 500 次**必然失败**的徒劳重试，而真正的故障（分区没建）
// 会被淹在 500 条相同日志里。
func TestFlushAbortsRoundOnMissingPartition(t *testing.T) {
	ctx := context.Background()
	const otherDev = uint64(2002)

	e := &mysql.MySQLError{Number: 1526, Message: "Table has no partition for value 1800000300"}
	writer := &partitionFailWriter{err: e}
	f := newFlushFixture(t, flushBaseTS+900, 10, writer)
	// 两台设备都是活跃且有已闭桶（[base, base+600) 两个桶）——若实现不中止，
	// 至少会有 4 次 WriteBucket 尝试（每台 1 个非空桶…第二桶为空不写）。
	f.setCursorFor(t, flushDevID, flushBaseTS-300)
	f.setCursorFor(t, otherDev, flushBaseTS-300)
	f.seedFor(t, flushDevID, flushSample(flushBaseTS*1000, 20, "/", 20, 60))
	f.seedFor(t, otherDev, flushSample(flushBaseTS*1000, 21, "/", 21, 61))

	stats, err := f.svc.FlushOnce(ctx)
	if !errors.Is(err, ErrMetricPartitionMissing) {
		t.Fatalf("FlushOnce 的 error = %v, want errors.Is(ErrMetricPartitionMissing)（任务层要靠它判 P1 并中止）", err)
	}
	// 底层驱动错误必须仍在链上（否则排障时看不到是哪个引擎的哪个报码）。
	if !errors.Is(err, e) {
		t.Fatalf("哨兵错误丢掉了驱动错误：%v", err)
	}
	// 缺分区**不是**「本轮部分失败」：它不该被任务层当作「下轮重试同一批桶」处理。
	if errors.Is(err, ErrFlushPartial) {
		t.Fatalf("缺分区被归类成 ErrFlushPartial（会让任务层以为下轮重试就能好）：%v", err)
	}

	if len(writer.calls) != 1 {
		t.Fatalf("WriteBucket 尝试次数 = %d, want 1（命中缺分区必须立即中止本轮，不做余下 500 次徒劳重试）",
			len(writer.calls))
	}
	if got := writer.calls[0]; got.bucketTS != flushBaseTS {
		t.Fatalf("第一次尝试的桶 = %d, want %d（必须是本轮第一个桶）", got.bucketTS, flushBaseTS)
	}
	// 只碰了一台设备 → 另一台（以及它的桶）根本没被尝试。
	if stats.DevicesScanned != 2 {
		t.Fatalf("DevicesScanned = %d, want 2（枚举照旧，中止发生在写路径上）", stats.DevicesScanned)
	}
	if stats.BucketsWritten != 0 {
		t.Fatalf("BucketsWritten = %d, want 0（那一次写失败了）", stats.BucketsWritten)
	}
	if stats.Errors != 1 {
		t.Fatalf("Errors = %d, want 1（本轮只记录这一次失败）", stats.Errors)
	}
	// 水位必须留在原处：中止不该顺手推进任何设备的水位。
	f.mustCursor(t, flushDevID, flushBaseTS-300)
	f.mustCursor(t, otherDev, flushBaseTS-300)
	// 前 24h 内补上分区即可完全自愈（spec §7.3），故本轮不推水位、
	// 下轮从同一个桶重来 —— 这一点由上面的游标断言覆盖。
}

// TestFlushMissingPartitionFromResourceWriteIsAlsoAborted 钉住「识别发生在写失败路径上」，
// 而不是只认宽表那一处：驱动错误被 fmt.Errorf("%w") 包了两层（writeBucket →
// flushDevice）之后仍必须被识别出来（errors.As 必须穿透包装）。
func TestFlushMissingPartitionWrappedTwiceIsStillDetected(t *testing.T) {
	ctx := context.Background()
	// 用一次「正常写入」的替身，但让**宽表写**返回一个已被包装过的缺分区错误。
	writer := &partitionFailWriter{err: fmt.Errorf("insert 5m sub-rows: %w",
		&mysql.MySQLError{Number: 1526, Message: "Table has no partition for value 1800000300"})}
	f := newFlushFixture(t, flushBaseTS+600, 10, writer)
	f.setCursor(t, flushBaseTS-300)
	f.seed(t, flushSample(flushBaseTS*1000, 20, "/", 20, 60))

	if _, err := f.svc.FlushOnce(ctx); !errors.Is(err, ErrMetricPartitionMissing) {
		t.Fatalf("两层包装后的缺分区未被识别：%v", err)
	}
	if len(writer.calls) != 1 {
		t.Fatalf("WriteBucket 尝试次数 = %d, want 1", len(writer.calls))
	}
	f.mustCursor(t, flushDevID, flushBaseTS-300)
}

// TestFlushOrdinaryWriteFailureStillRetriesNextRound 是对照组：
// **普通**写失败（不是缺分区）必须保持既有语义 —— 逐设备失败、不下发 P1 哨兵、
// 水位留在原处、下轮重试。
//
// 没有这条对照，「把一切写失败都判成缺分区」的过度修复也能让上面那条断言变绿，
// 而代价是整轮 flush 被一个可自愈的错误停掉（本该只失败一台设备）。
func TestFlushOrdinaryWriteFailureStillRetriesNextRound(t *testing.T) {
	ctx := context.Background()
	writer := &partitionFailWriter{err: errors.New("connection reset by peer")}
	f := newFlushFixture(t, flushBaseTS+600, 10, writer)
	f.setCursor(t, flushBaseTS-300)
	f.seed(t, flushSample(flushBaseTS*1000, 20, "/", 20, 60))

	_, err := f.svc.FlushOnce(ctx)
	if !errors.Is(err, ErrFlushPartial) {
		t.Fatalf("普通写失败必须报 ErrFlushPartial, got %v", err)
	}
	if errors.Is(err, ErrMetricPartitionMissing) {
		t.Fatalf("普通写失败被误判成缺分区（会让整轮 flush 无谓中止）：%v", err)
	}
	f.mustCursor(t, flushDevID, flushBaseTS-300)
}
