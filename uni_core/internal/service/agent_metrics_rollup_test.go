package service

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/glebarez/sqlite"
	goredis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agentmetrics"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/database"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/snowflake"
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
)

// ── 夹具常量：时间全部对齐（1800000000 既是 3600 也是 300 的整数倍），bucket_ts 可逐字写死 ──

const (
	rollupBaseTS  = int64(1800000000)
	rollupDevID   = uint64(2002)
	rollupHourSec = int64(3600)
	// rollupBootstrapDays 与实现的默认起点窗口**同源**（改实现会立刻打到断言上）。
	rollupBootstrapDays = 180
	// rollupReportSec 是夹具的上报间隔（ExpectedSamplesPerHour(10s) == 360）。
	rollupReportSec = 10
)

// rollupCursorKey / rollupRepairKey 是**契约字面量**：不复用 agentmetrics.CursorKey /
// RepairSetKey，否则键名写错也自洽（同 flush 测试对 cursor_5m 的处理）。
func rollupCursorKey(deviceID uint64) string {
	return "agent:device:" + itoaU64(deviceID) + ":cursor_1h"
}

func rollupRepairKey(deviceID uint64) string {
	return "agent:device:" + itoaU64(deviceID) + ":repair_1h"
}

// alignDownHour 是测试侧独立的对齐实现（不复用实现里的 alignDown，否则对齐写错也自洽）。
// 夹具里的时间恒为正数，故直接取模即可。
func alignDownHour(sec int64) int64 { return sec - sec%rollupHourSec }

// fakeDeviceSource 是设备枚举替身：rollup 只把它当 deviceID 列表用（热层的 Index 同形）。
type fakeDeviceSource struct {
	ids []uint64
	err error
}

func (f fakeDeviceSource) Index(context.Context) ([]uint64, error) { return f.ids, f.err }

// fakeHourWriter 是 RollupMetricRepo 的替身：读走真仓储，**写可注入失败**。
//
// 为什么需要替身：本任务要求「WriteHour 失败 → cursor_1h 不推进」，而真仓储只有触发
// SQL 错误才失败，那种失败同时污染了「行确实写不进去」这个前提（同 flush 测试的理由）。
type fakeHourWriter struct {
	inner RollupMetricRepo

	failOnCall int // 第几次 WriteHour（1-based）失败；0 = 从不失败
	calls      int
	written    []*entity.DeviceMetricWide
}

func (f *fakeHourWriter) ReadWideRows(ctx context.Context, table string, deviceID uint64,
	from, to int64) ([]entity.DeviceMetricWide, error) {
	return f.inner.ReadWideRows(ctx, table, deviceID, from, to)
}

// ExistingBucketTimestamps 必须照样委托：重扫（第 3 步）用的是这条读，
// 替身若把它截住，重扫会静默看到「窗口里没有 5m 行」而永远不补 —— 正是要防的那种失效。
func (f *fakeHourWriter) ExistingBucketTimestamps(ctx context.Context, table string, deviceID uint64,
	from, to int64) ([]int64, error) {
	return f.inner.ExistingBucketTimestamps(ctx, table, deviceID, from, to)
}

func (f *fakeHourWriter) WriteHour(ctx context.Context, w *entity.DeviceMetricWide) error {
	f.calls++
	if f.failOnCall > 0 && f.calls == f.failOnCall {
		return errors.New("injected WriteHour failure")
	}
	if err := f.inner.WriteHour(ctx, w); err != nil {
		return err
	}
	f.written = append(f.written, w)
	return nil
}

// rollupFixture 是一次测试的全部依赖。
type rollupFixture struct {
	db     *gorm.DB
	rdb    goredis.UniversalClient
	mr     *miniredis.Miniredis
	repo   *repository.DeviceMetricRepo
	writer *fakeHourWriter // spy 模式下的注入点（见 newRollupFixture）
	clock  *fixedClock
	log    *fakeLogger
	svc    *AgentMetricsRollupService
}

// newRollupFixture 起内存 sqlite（两张宽表）+ miniredis；spy 为真时写路径走替身。
func newRollupFixture(t *testing.T, at int64, spy bool, maxRepair int) *rollupFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	// 生产同款雪花回调（与 flush 测试一致：夹具与生产共用注册路径）。
	node, err := snowflake.New(1, logger.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	database.NewCallbacks(node).Register(db)
	// _5m 由 DeviceMetricWide.TableName() 建；_1h 是**同一结构体的第二处表名**，
	// 必须显式建表，否则 WriteHour 只会报 no such table（脚本上表现为「1h 行永远写不进去」）。
	if err := db.AutoMigrate(&entity.DeviceMetricWide{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Table(entity.TableNameMetric1h).AutoMigrate(&entity.DeviceMetricWide{}); err != nil {
		t.Fatal(err)
	}

	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	repo := repository.NewDeviceMetricRepository(db)
	f := &rollupFixture{
		db: db, rdb: rdb, mr: mr, repo: repo,
		clock: &fixedClock{t: time.Unix(at, 0)},
		log:   &fakeLogger{},
	}

	var metrics RollupMetricRepo = repo
	if spy {
		f.writer = &fakeHourWriter{inner: repo}
		metrics = f.writer
	}
	f.svc = NewAgentMetricsRollupService(metrics, fakeDeviceSource{ids: []uint64{rollupDevID}},
		fakeConfig{reportSec: rollupReportSec}, f.log).
		WithCursorStore(rdb).
		WithClock(f.clock.now)
	if maxRepair > 0 {
		f.svc = f.svc.WithMaxRepairHours(maxRepair)
	}
	return f
}

//  造 5m 行 ────────────────────────────────────────────

// fiveMin 描述一行 5m 宽行（只填断言会用到的列）。
type fiveMin struct {
	ts      int64
	samples int
	cpu     float64
	uptime  int64
	temp    float64
	// fiveMinOnly 为真时把 5min-only 的列（tcp_time_wait / tcp_close_wait / agent_*）
	// 全部填上非零值 —— 它们必须在 1h 行为 NULL。
	fiveMinOnly bool
	diskTotal   float64
	diskUsed    float64
}

// fullHourRows 造「从 hour 起的 12 行」，samples/cpu 等由入参函数按行给。
func fullHourRows(hour int64, fn func(i int) fiveMin) []fiveMin {
	out := make([]fiveMin, 0, 12)
	for i := 0; i < 12; i++ {
		r := fn(i)
		r.ts = hour + int64(i)*300
		out = append(out, r)
	}
	return out
}

// seed5m 用**生产写路径**（EntityFromWide → WriteBucket）落 5m 宽行。
func (f *rollupFixture) seed5m(t *testing.T, rows ...fiveMin) {
	t.Helper()
	for _, r := range rows {
		w := agentmetrics.Wide{
			DeviceID:        rollupDevID,
			BucketTS:        r.ts,
			Samples:         r.samples,
			CPUUsedPercent:  f64p(r.cpu),
			UptimeSec:       i64p(r.uptime),
			MaxTemperatureC: f64p(r.temp),
		}
		if r.diskTotal > 0 {
			w.DiskTotalGB = f64p(r.diskTotal)
			w.DiskUsedGB = f64p(r.diskUsed)
			w.DiskUsedPercent = f64p(r.diskUsed / r.diskTotal * 100)
		}
		if r.fiveMinOnly {
			w.TCPTimeWait = i64p(11)
			w.TCPCloseWait = i64p(12)
			w.AgentCollectDurationMs = f64p(13)
			w.AgentReportSuccessCount = i64p(14)
			w.AgentReportErrorCount = i64p(15)
			w.AgentWSReconnectCount = i64p(16)
			w.AgentLastReportError = strPtr("boom")
			w.AgentMemResidentMB = f64p(17)
			w.AgentPendingBacklog = i64p(18)
			w.AgentLastReportLatencyMs = f64p(19)
			w.AgentReportDropCount = i64p(20)
			w.AgentUptimeSec = i64p(21)
		}
		row, err := repository.EntityFromWide(w)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.repo.WriteBucket(context.Background(), row, repository.MetricSubRows{}); err != nil {
			t.Fatalf("seed 5m 行: %v", err)
		}
	}
}

// drop5mRange 删掉某段 5m 行（模拟分区保留期回收）。
func (f *rollupFixture) drop5mRange(t *testing.T, from, to int64) {
	t.Helper()
	if err := f.db.Exec("DELETE FROM "+entity.TableNameMetric5m+
		" WHERE device_id = ? AND bucket_ts BETWEEN ? AND ?", rollupDevID, from, to).Error; err != nil {
		t.Fatal(err)
	}
}

// drop1hRange 删掉某段 1h 行：用于制造「有 5m 行、却缺 1h 行」的状态（重扫正是为它存在）。
func (f *rollupFixture) drop1hRange(t *testing.T, from, to int64) {
	t.Helper()
	if err := f.db.Exec("DELETE FROM "+entity.TableNameMetric1h+
		" WHERE device_id = ? AND bucket_ts BETWEEN ? AND ?", rollupDevID, from, to).Error; err != nil {
		t.Fatal(err)
	}
}

func strPtr(s string) *string { return &s }

// ─ 读回断言用的状态 ─────────────────────────────────────

func (f *rollupFixture) hourRow(t *testing.T, ts int64) *entity.DeviceMetricWide {
	t.Helper()
	var row entity.DeviceMetricWide
	err := f.db.Table(entity.TableNameMetric1h).
		Where("device_id = ? AND bucket_ts = ?", rollupDevID, ts).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		t.Fatalf("读 1h 行: %v", err)
	}
	return &row
}

func (f *rollupFixture) count1h(t *testing.T) int64 {
	t.Helper()
	var n int64
	if err := f.db.Table(entity.TableNameMetric1h).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func (f *rollupFixture) cursor(t *testing.T) (int64, bool) {
	t.Helper()
	v, err := f.rdb.Get(context.Background(), rollupCursorKey(rollupDevID)).Int64()
	if errors.Is(err, goredis.Nil) {
		return 0, false
	}
	if err != nil {
		t.Fatalf("读 cursor_1h: %v", err)
	}
	return v, true
}

func (f *rollupFixture) setCursor(t *testing.T, sec int64) {
	t.Helper()
	if err := f.rdb.Set(context.Background(), rollupCursorKey(rollupDevID), sec, 0).Err(); err != nil {
		t.Fatalf("set cursor_1h: %v", err)
	}
}

func (f *rollupFixture) repairMembers(t *testing.T) []int64 {
	t.Helper()
	raw, err := f.rdb.SMembers(context.Background(), rollupRepairKey(rollupDevID)).Result()
	if err != nil {
		t.Fatalf("读 repair 集合: %v", err)
	}
	out := make([]int64, 0, len(raw))
	for _, s := range raw {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatalf("repair 成员 %q 不是小时桶起点: %v", s, err)
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (f *rollupFixture) inRepair(t *testing.T, hour int64) bool {
	t.Helper()
	ok, err := f.rdb.SIsMember(context.Background(), rollupRepairKey(rollupDevID), hour).Result()
	if err != nil {
		t.Fatalf("SIsMember: %v", err)
	}
	return ok
}

// ── Step 1 的 5 条断言 ──────────────────────────────────

// 1. 整小时回滚：12 个 5m 行 → 1h 行；均值类**按 samples 加权**（断言加权值 ≠ 未加权值），
// 单调量取 LAST、温度取 max、比值由 Σ 列重算。
func TestRollupWholeHourWeightedBySamples(t *testing.T) {
	now := rollupBaseTS + 3*rollupHourSec + 60
	f := newRollupFixture(t, now, false, 0)

	// 前 6 行：samples=30、cpu=10；后 6 行：samples=10、cpu=50。
	// 加权 = (10×30×6 + 50×10×6) / (30×6 + 10×6) = 4800/240 = 20
	// 未加权均值 = (10×6 + 50×6)/12 = 30  → 两者必须不同，否则这条断言测不出加权。
	rows := fullHourRows(rollupBaseTS, func(i int) fiveMin {
		if i < 6 {
			return fiveMin{samples: 30, cpu: 10, uptime: int64(1000 + i), temp: 60 + float64(i),
				diskTotal: 100, diskUsed: float64(i + 1)}
		}
		return fiveMin{samples: 10, cpu: 50, uptime: int64(1000 + i), temp: 60 + float64(i),
			diskTotal: 100, diskUsed: float64(i + 1)}
	})
	f.seed5m(t, rows...)

	stats, err := f.svc.RollupOnce(context.Background())
	if err != nil {
		t.Fatalf("RollupOnce: %v", err)
	}
	// 缺省游标 = 最早 5m 行所在小时（base）→ 工作区间 [base, upper)，upper = base+3h
	if stats.HoursScanned != 3 || stats.HoursWritten != 1 || stats.HoursSkipped != 2 ||
		stats.HoursRepaired != 0 || stats.Errors != 0 {
		t.Fatalf("stats = %+v, want 3 scanned / 1 written / 2 skipped / 0 repaired", stats)
	}

	row := f.hourRow(t, rollupBaseTS)
	if row == nil {
		t.Fatal("1h 行不存在：整小时必须被回滚出来")
	}
	if row.Samples != 240 {
		t.Fatalf("1h samples = %d, want 240（= Σsamples）", row.Samples)
	}
	if row.CPUUsedPercent == nil {
		t.Fatal("1h cpu_used_percent = nil")
	}
	if got := *row.CPUUsedPercent; got != 20 {
		t.Fatalf("1h cpu_used_percent = %v, want 20（按 samples 加权）；30 是未加权均值", got)
	}
	if row.CPUUsedPercent != nil && *row.CPUUsedPercent == 30 {
		t.Fatal("1h cpu_used_percent == 未加权均值 30：加权回滚退化成了简单平均（Plan 2B 口径被改）")
	}
	// 单调量取 LAST（bucket_ts 最大的那一行）
	if row.UptimeSec == nil || *row.UptimeSec != 1011 {
		t.Fatalf("1h uptime_sec = %v, want 1011（单调量取 LAST）", row.UptimeSec)
	}
	// 温度取 max
	if row.MaxTemperatureC == nil || *row.MaxTemperatureC != 71 {
		t.Fatalf("1h max_temperature_c = %v, want 71（温度取 max）", row.MaxTemperatureC)
	}
	// 存量比值由 Σ 列重算：Σused = (30×21 + 10×57)/240 = 5.0 → 5.0/100 = 5%
	// （对 5m 比值求平均会得到 6.5% —— 必须不是它）
	if row.DiskUsedGB == nil || *row.DiskUsedGB != 5 {
		t.Fatalf("1h disk_used_gb = %v, want 5（Σ 列按 samples 加权）", row.DiskUsedGB)
	}
	if row.DiskUsedPercent == nil || *row.DiskUsedPercent != 5 {
		t.Fatalf("1h disk_used_percent = %v, want 5（由 1h 的 Σ 列重算；6.5 是对 5m 比值求平均）",
			row.DiskUsedPercent)
	}

	// 完整小时（12 行）**不得**进 repair 集合。
	//
	// 注意 Σsamples = 240 < ExpectedSamplesPerHour(10s) = 360：这说明「行数齐但样本偏薄」，
	// 按 spec §7.3 它是 repair 的候选信号，但**不能**作为 repair 触发条件 ——
	// reportInterval 是可热更的配置，一台按 30s 上报的设备每小时只有 120 个样本，
	// 若按 Σsamples 判 repair，它**每个**小时都会被判为待修，repair 集合会被「永远修不完的
	// 完成项」占满（用户硬约束里明确点名的失效模式）。故：只告警，不入集合。
	if members := f.repairMembers(t); len(members) != 0 {
		t.Fatalf("repair 集合 = %v, want 空（12 行齐的小时不进 repair）", members)
	}
	if f.log.warns < 1 {
		t.Fatal("Σsamples 低于完整小时应有值时必须 log.Warn（可观测性：样本偏薄不能被静默吞掉）")
	}

	// 水位前移到「连续成功前缀」的末尾 = base（本轮唯一写出的小时）。
	//
	// base+3600 / base+7200 都是空小时，但它们的**结束**时刻距 now 分别只有 3660s / 60s，
	// 都落在 emptyHourGrace（默认 2h）的等待窗口内 → 判定为「数据可能还没到齐」，
	// 水位不得越过它们。
	//
	// 旧断言 `cursor_1h == base+7200`（"空小时照常推进"）编码的正是本修复要闭合的缺陷：
	// 把「空」当成「确实没有数据」越过去，等该小时的 5m 行稍后落库（flush 重启补齐、
	// 或 flush 落后 rollup 一轮）时，普通区间（下界恒为 cursor+3600）再也不会回头看它，
	// 那个小时在 1h 表里**永久缺失** —— 而 >30 天的窗口只有 1h 表可查。
	if cur, ok := f.cursor(t); !ok || cur != rollupBaseTS {
		t.Fatalf("cursor_1h = %d(ok=%v), want %d（空小时仍在等待窗口内，水位不得越过它）",
			cur, ok, rollupBaseTS)
	}
}

// 2. 5min-only 列为 NULL：tcp_time_wait / tcp_close_wait / agent_* 在 1h 行必须为 nil。
func TestRollupHourNullsFiveMinuteOnlyColumns(t *testing.T) {
	now := rollupBaseTS + 2*rollupHourSec + 60
	f := newRollupFixture(t, now, false, 0)
	f.seed5m(t, fullHourRows(rollupBaseTS, func(int) fiveMin {
		return fiveMin{samples: 30, cpu: 10, fiveMinOnly: true}
	})...)

	if _, err := f.svc.RollupOnce(context.Background()); err != nil {
		t.Fatalf("RollupOnce: %v", err)
	}
	row := f.hourRow(t, rollupBaseTS)
	if row == nil {
		t.Fatal("1h 行不存在")
	}
	// 先在 5m 行上确认这些列**确实有值** —— 否则「1h 为 nil」可能只是因为源头就是 nil。
	var five struct {
		TCPTimeWait *int64
		AgentDrops  *int64
	}
	if err := f.db.Table(entity.TableNameMetric5m).
		Select("tcp_time_wait, agent_report_drop_count AS agent_drops").
		Where("device_id = ? AND bucket_ts = ?", rollupDevID, rollupBaseTS).Take(&five).Error; err != nil {
		t.Fatal(err)
	}
	if five.TCPTimeWait == nil || five.AgentDrops == nil {
		t.Fatal("前置不成立：5m 行的 5min-only 列没有值")
	}

	// nil 判定必须用**类型化**比较（`got any` 装一个 (*int64)(nil) 时 `got != nil` 为真，
	// 那会让这条断言恒红或恒绿 —— 一次夹具坑，不是实现问题）。
	nulls := []struct {
		name  string
		isNil bool
		got   any
	}{
		{"tcp_time_wait", row.TCPTimeWait == nil, row.TCPTimeWait},
		{"tcp_close_wait", row.TCPCloseWait == nil, row.TCPCloseWait},
		{"agent_collect_duration_ms", row.AgentCollectDurationMs == nil, row.AgentCollectDurationMs},
		{"agent_report_success_count", row.AgentReportSuccessCount == nil, row.AgentReportSuccessCount},
		{"agent_report_error_count", row.AgentReportErrorCount == nil, row.AgentReportErrorCount},
		{"agent_ws_reconnect_count", row.AgentWSReconnectCount == nil, row.AgentWSReconnectCount},
		{"agent_last_report_error", row.AgentLastReportError == nil, row.AgentLastReportError},
		{"agent_mem_resident_mb", row.AgentMemResidentMB == nil, row.AgentMemResidentMB},
		{"agent_pending_backlog", row.AgentPendingBacklog == nil, row.AgentPendingBacklog},
		{"agent_last_report_latency_ms", row.AgentLastReportLatencyMs == nil, row.AgentLastReportLatencyMs},
		{"agent_report_drop_count", row.AgentReportDropCount == nil, row.AgentReportDropCount},
		{"agent_uptime_sec", row.AgentUptimeSec == nil, row.AgentUptimeSec},
	}
	for _, c := range nulls {
		if !c.isNil {
			t.Fatalf("1h 行 %s = %v, want NULL（5min-only 列在 1h 行必须为 NULL）", c.name, c.got)
		}
	}
}

// 3. 不完整小时：本轮**仍写出**（宁可残缺不要空着）且记入 repair；下一轮**优先重算**，
// 补上缺失的 5m 行后 1h 行被更新为正确值，且成员在修复成功后被移除。
func TestRollupIncompleteHourWrittenThenRepaired(t *testing.T) {
	now := rollupBaseTS + 3*rollupHourSec + 60
	f := newRollupFixture(t, now, false, 0)

	// 只造 7 行：cpu 前 6 行 10、第 7 行 50
	f.seed5m(t, fullHourRows(rollupBaseTS, func(i int) fiveMin {
		if i < 6 {
			return fiveMin{samples: 30, cpu: 10}
		}
		return fiveMin{samples: 30, cpu: 50}
	})[:7]...)

	stats1, err := f.svc.RollupOnce(context.Background())
	if err != nil {
		t.Fatalf("RollupOnce(1): %v", err)
	}
	if stats1.HoursWritten != 1 || stats1.HoursRepaired != 0 {
		t.Fatalf("stats(1) = %+v, want 1 written / 0 repaired", stats1)
	}
	row := f.hourRow(t, rollupBaseTS)
	if row == nil {
		t.Fatal("不完整小时也必须写出 1h 行（宁可先有残缺值也不要空着）")
	}
	// 残缺值：(10×30×6 + 50×30×1)/210 = 3300/210 = 15.714… → Round2 → 15.71
	if row.Samples != 210 || row.CPUUsedPercent == nil || *row.CPUUsedPercent != 15.71 {
		t.Fatalf("残缺 1h 行 = samples %d / cpu %v, want 210 / 15.71", row.Samples, row.CPUUsedPercent)
	}
	if !f.inRepair(t, rollupBaseTS) {
		t.Fatalf("repair 集合 = %v, want 含 %d（RowCount=7 < 12 → NeedRepair）",
			f.repairMembers(t), rollupBaseTS)
	}
	cursorAfterFirst, ok := f.cursor(t)
	// 水位只前移到 base（本轮写出的小时），**不**越过紧随其后的两个空小时：
	// 它们的等待窗口还没过（见 emptyHourGrace），越过它们就等于放弃「迟到的小时」。
	// 旧断言（base+7200）编码的是修复前的缺陷行为。
	if !ok || cursorAfterFirst != rollupBaseTS {
		t.Fatalf("cursor_1h = %d(ok=%v), want %d（空小时仍在等待窗口内，水位不得越过）",
			cursorAfterFirst, ok, rollupBaseTS)
	}

	// 补上缺失的 5 行（i=7..11，samples=30、cpu=50）→ 12 行齐，正确加权值 = 30
	f.seed5m(t, fullHourRows(rollupBaseTS, func(i int) fiveMin {
		if i < 6 {
			return fiveMin{samples: 30, cpu: 10}
		}
		return fiveMin{samples: 30, cpu: 50}
	})[7:]...)

	stats2, err := f.svc.RollupOnce(context.Background())
	if err != nil {
		t.Fatalf("RollupOnce(2): %v", err)
	}
	// repair 优先：这个小时的水位在游标**之后**，普通区间永远碰不到它，
	// 故本轮唯一被写出的小时只可能来自 repair 集合。
	//
	// 另外 2 个被扫到的小时是 base+3600 / base+7200 两个空小时（水位停在 base 之后，
	// 普通区间仍会扫过它们）：它们的结束时刻距 now 只有 3660s / 60s，都在等待窗口内，
	// 故只计数、不写行、也不带动水位（旧断言 `HoursScanned == 1` 是按「空小时照常
	// 推进游标、水位已到 base+7200」写的，新契约下这三个小时每轮都会被扫到）。
	if stats2.HoursScanned != 3 || stats2.HoursRepaired != 1 || stats2.HoursWritten != 1 {
		t.Fatalf("stats(2) = %+v, want 3 scanned / 1 repaired / 1 written（repair 必须被优先重算）", stats2)
	}
	row2 := f.hourRow(t, rollupBaseTS)
	if row2 == nil {
		t.Fatal("重算后 1h 行丢失")
	}
	if row2.Samples != 360 || row2.CPUUsedPercent == nil || *row2.CPUUsedPercent != 30 {
		t.Fatalf("重算后 1h 行 = samples %d / cpu %v, want 360 / 30（必须被**更新**为正确值）",
			row2.Samples, row2.CPUUsedPercent)
	}
	if f.inRepair(t, rollupBaseTS) {
		t.Fatal("小时已补全，repair 成员必须被移除（否则集合会被完成项占满）")
	}
	if members := f.repairMembers(t); len(members) != 0 {
		t.Fatalf("repair 集合 = %v, want 空", members)
	}
	if cur, _ := f.cursor(t); cur != cursorAfterFirst {
		t.Fatalf("cursor_1h = %d, want %d（repair 重算不得改动水位）", cur, cursorAfterFirst)
	}
}

// 3b. repair 小时的 5m 行已不存在（保留期回收）→ 不可再重算，成员必须被移除（否则永久占位）。
func TestRollupRepairHourWithVanishedRowsIsDroppedFromSet(t *testing.T) {
	now := rollupBaseTS + 2*rollupHourSec + 60
	f := newRollupFixture(t, now, false, 0)
	f.seed5m(t, fullHourRows(rollupBaseTS, func(int) fiveMin {
		return fiveMin{samples: 30, cpu: 10}
	})[:7]...)

	if _, err := f.svc.RollupOnce(context.Background()); err != nil {
		t.Fatalf("RollupOnce(1): %v", err)
	}
	if !f.inRepair(t, rollupBaseTS) {
		t.Fatal("前置：不完整小时必须已进 repair 集合")
	}

	f.drop5mRange(t, rollupBaseTS, rollupBaseTS+3300)
	stats, err := f.svc.RollupOnce(context.Background())
	if err != nil {
		t.Fatalf("RollupOnce(2): %v", err)
	}
	// repair 路径上「没有 5m 行」= 已被保留期回收（不可修，摘掉成员）；
	// 普通区间上那个空小时 base+3600 仍在等待窗口内（结束距 now 仅 3660s），
	// 故也计入 skipped 且不让水位越过 —— 两处 skipped 合计 2。
	// 旧断言（1 skipped）是按「空小时照常推进游标」写的：新契约下同一个空小时
	// 会被**每一轮**重新扫到，直到它过了等待窗口。
	if stats.HoursSkipped != 2 || stats.HoursWritten != 0 {
		t.Fatalf("stats(2) = %+v, want 2 skipped / 0 written（无 5m 行 → 不写空行）", stats)
	}
	if f.inRepair(t, rollupBaseTS) {
		t.Fatal("5m 行已消失的小时留在 repair 集合里 = 永久占位（集合被不可修项填满）")
	}
	if f.count1h(t) != 1 {
		t.Fatalf("1h 行数 = %d, want 1（既有的残缺行不得被删）", f.count1h(t))
	}
}

// 4. repair 有界：超过上限时丢弃**最老**的小时并 log.Warn（不能无限增长）。
func TestRollupRepairSetBoundedDropsOldest(t *testing.T) {
	// 上限默认值由计划给定（720 = 30 天的小时数）；这里用字面量钉住，避免实现悄悄改小/改大。
	if defaultMaxRepairHours != 720 {
		t.Fatalf("defaultMaxRepairHours = %d, want 720", defaultMaxRepairHours)
	}

	now := rollupBaseTS + 6*rollupHourSec + 60
	f := newRollupFixture(t, now, false, 3) // 上限 3（用小值把上限行为测出来）
	// 造 4 个不完整小时（各 1 行）
	for i := 0; i < 4; i++ {
		f.seed5m(t, fiveMin{ts: rollupBaseTS + int64(i)*rollupHourSec, samples: 30, cpu: 10})
	}
	stats, err := f.svc.RollupOnce(context.Background())
	if err != nil {
		t.Fatalf("RollupOnce: %v", err)
	}
	if stats.HoursWritten != 4 {
		t.Fatalf("stats = %+v, want 4 written", stats)
	}
	members := f.repairMembers(t)
	want := []int64{rollupBaseTS + rollupHourSec, rollupBaseTS + 2*rollupHourSec, rollupBaseTS + 3*rollupHourSec}
	if len(members) != len(want) {
		t.Fatalf("repair 集合 = %v, want %v（超过上限 3 必须丢弃最老的）", members, want)
	}
	for i := range want {
		if members[i] != want[i] {
			t.Fatalf("repair 集合 = %v, want %v（丢弃的必须是最老的 %d）", members, want, rollupBaseTS)
		}
	}
	if f.inRepair(t, rollupBaseTS) {
		t.Fatal("最老的小时仍在集合里：上限没有生效（集合会无限增长）")
	}
	if f.log.warns < 1 {
		t.Fatal("丢弃最老的小时必须 log.Warn（静默丢数据 = 不可观测）")
	}
	// 被丢弃的只是「重算机会」，1h 行本身保留（残缺值好过空洞）。
	if f.hourRow(t, rollupBaseTS) == nil {
		t.Fatal("丢弃 repair 成员时不得连带删掉已写出的残缺 1h 行")
	}
}

// 5a. 游标：只在成功写入后前移；前移后第二轮不得重复写。
func TestRollupCursorAdvancesOnlyAfterSuccess(t *testing.T) {
	now := rollupBaseTS + 2*rollupHourSec + 60
	f := newRollupFixture(t, now, false, 0)
	f.seed5m(t, fullHourRows(rollupBaseTS, func(int) fiveMin {
		return fiveMin{samples: 30, cpu: 20}
	})...)

	if _, err := f.svc.RollupOnce(context.Background()); err != nil {
		t.Fatalf("RollupOnce(1): %v", err)
	}
	cur, ok := f.cursor(t)
	if !ok {
		t.Fatal("cursor_1h 键不存在：游标必须落回 Redis")
	}
	// 水位落在**写出的小时** base 上，而不是它后面那个空小时 base+3600：
	// 后者距 now 结束仅 3660s，仍在等待窗口内（emptyHourGrace）→ 不得越过。
	// 旧断言（base+3600）编码的是修复前的缺陷行为。
	if cur != rollupBaseTS {
		t.Fatalf("cursor_1h = %d, want %d（只前移到已处理前缀的末尾，不越过等待窗口内的空小时）",
			cur, rollupBaseTS)
	}

	stats2, err := f.svc.RollupOnce(context.Background())
	if err != nil {
		t.Fatalf("RollupOnce(2): %v", err)
	}
	// 第二轮仍然会扫到那个空小时（水位停着没动），但它同样不写行、不推进水位：
	// 「扫到」与「重复回滚」是两件事 —— 写出的小时数必须仍是 0。
	if stats2.HoursScanned != 1 || stats2.HoursWritten != 0 {
		t.Fatalf("stats(2) = %+v, want 1 scanned / 0 written（已被扣住的空小时重扫但不得重复回滚）", stats2)
	}
	if f.count1h(t) != 1 {
		t.Fatalf("1h 行数 = %d, want 1", f.count1h(t))
	}
}

// 5b. 失败不推进游标：WriteHour 失败 → cursor_1h 留在轮初值，下一轮重试同一个小时。
func TestRollupWriteFailureKeepsCursorAndRetries(t *testing.T) {
	now := rollupBaseTS + 2*rollupHourSec + 60
	f := newRollupFixture(t, now, true, 0)
	f.writer.failOnCall = 1
	f.seed5m(t, fullHourRows(rollupBaseTS, func(int) fiveMin {
		return fiveMin{samples: 30, cpu: 20}
	})...)

	stats, err := f.svc.RollupOnce(context.Background())
	if err == nil {
		t.Fatal("RollupOnce 必须把「本轮有设备失败」上抛为 error，实际返回 nil")
	}
	if !errors.Is(err, ErrRollupPartial) {
		t.Fatalf("err = %v, want ErrRollupPartial", err)
	}
	if stats.Errors != 1 || stats.HoursWritten != 0 {
		t.Fatalf("stats = %+v, want 1 error / 0 written", stats)
	}
	cur, ok := f.cursor(t)
	if !ok {
		t.Fatal("cursor_1h 键丢失：失败轮不得删除游标")
	}
	// 缺省起点 = 最早 5m 行所在小时 base（游标语义「已含」→ 值比起点早一小时）。
	if cur != rollupBaseTS-rollupHourSec {
		t.Fatalf("cursor_1h = %d, want %d（写失败不得推进水位）", cur, rollupBaseTS-rollupHourSec)
	}
	if f.count1h(t) != 0 {
		t.Fatalf("1h 行数 = %d, want 0（写入失败）", f.count1h(t))
	}

	// 下一轮（替身不再失败）必须重试同一个小时并成功。
	f.writer.failOnCall = 0
	stats2, err := f.svc.RollupOnce(context.Background())
	if err != nil {
		t.Fatalf("RollupOnce(2): %v", err)
	}
	if stats2.HoursWritten != 1 {
		t.Fatalf("stats(2) = %+v, want 1 written（必须重试同一批小时）", stats2)
	}
	if len(f.writer.written) != 1 || f.writer.written[0].BucketTS != rollupBaseTS {
		t.Fatalf("重试写入的行 = %+v, want bucket_ts=%d", f.writer.written, rollupBaseTS)
	}
	if cur2, _ := f.cursor(t); cur2 != rollupBaseTS {
		t.Fatalf("cursor_1h = %d, want %d（成功后前移，但不得越过等待窗口内的空小时 base+3600）",
			cur2, rollupBaseTS)
	}
}

// 6. 缺省游标：起点 = max(now−180d, 该设备最早的 5m 行) 对齐到小时 —— **不得**无脑从 180d 前扫；
// 且最早那个小时必须真的被回滚（起点被 [cursor+3600, upper) 排除会让 1h 表永久缺一行）。
func TestRollupBootstrapCursorStartsAtEarliestFiveMinuteRow(t *testing.T) {
	now := rollupBaseTS + 3*rollupHourSec + 60
	f := newRollupFixture(t, now, false, 0)
	f.seed5m(t, fiveMin{ts: rollupBaseTS, samples: 30, cpu: 10})

	cur, err := f.svc.CursorFor(context.Background(), rollupDevID)
	if err != nil {
		t.Fatalf("CursorFor: %v", err)
	}
	if cur+rollupHourSec != rollupBaseTS {
		t.Fatalf("工作起点 = %d, want %d（= 该设备最早的 5m 行所在小时）", cur+rollupHourSec, rollupBaseTS)
	}
	if blind := alignDownHour(now - rollupBootstrapDays*86400); cur <= blind {
		t.Fatalf("cursor_1h = %d 不晚于 now−180d 对齐值 %d：等于无脑从 180d 前扫", cur, blind)
	}

	stats, err := f.svc.RollupOnce(context.Background())
	if err != nil {
		t.Fatalf("RollupOnce: %v", err)
	}
	if stats.HoursScanned != 3 {
		t.Fatalf("HoursScanned = %d, want 3（起点是数据所在小时，不是 180d 前）", stats.HoursScanned)
	}
	if f.hourRow(t, rollupBaseTS) == nil {
		t.Fatal("最早那个小时必须被回滚：起点被区间下界排除会让 1h 表永久缺一行")
	}

	// 另一支：完全没有 5m 行时，起点退化为 now−180d 对齐值。
	f2 := newRollupFixture(t, rollupBaseTS, false, 0)
	cur2, err := f2.svc.CursorFor(context.Background(), rollupDevID)
	if err != nil {
		t.Fatalf("CursorFor(2): %v", err)
	}
	blind := alignDownHour(rollupBaseTS - rollupBootstrapDays*86400)
	if cur2+rollupHourSec != blind {
		t.Fatalf("无 5m 行时工作起点 = %d, want %d（now−180d 对齐）", cur2+rollupHourSec, blind)
	}
}

// 7. 空小时：不写空行；**已过等待窗口**的空小时让水位越过（不永久卡死），
// **仍在窗口内**的空小时则扣住水位（本修复的核心，见 TestRollupYoungEmptyHourHoldsCursor）。
func TestRollupSkipsEmptyHourWithoutRow(t *testing.T) {
	// now = base+4h+60 → 已闭小时区间是 [base, base+4h)：
	//   base        有 12 行 5m → 写出 1h 行；
	//   base+3600   空，结束距 now 7260s ≥ 2h(7200s) → **已过窗口** → 水位越过它；
	//   base+7200   空，结束距 now 3660s < 2h          → 仍在窗口内 → 水位停住；
	//   base+10800  空，结束距 now 60s                 → 仍在窗口内（已被前者拦住）。
	now := rollupBaseTS + 4*rollupHourSec + 60
	f := newRollupFixture(t, now, false, 0)
	f.seed5m(t, fullHourRows(rollupBaseTS, func(int) fiveMin {
		return fiveMin{samples: 30, cpu: 20}
	})...)

	stats, err := f.svc.RollupOnce(context.Background())
	if err != nil {
		t.Fatalf("RollupOnce: %v", err)
	}
	if stats.HoursSkipped != 3 || stats.HoursWritten != 1 || stats.HoursScanned != 4 {
		t.Fatalf("stats = %+v, want 4 scanned / 1 written / 3 skipped（空小时一律不写行）", stats)
	}
	if f.count1h(t) != 1 {
		t.Fatalf("1h 行数 = %d, want 1（空小时绝不写空行）", f.count1h(t))
	}
	if f.hourRow(t, rollupBaseTS+rollupHourSec) != nil {
		t.Fatal("空小时产出了 1h 行")
	}
	// 水位 = base+3600：**越过**了已过窗口的空小时（证明设备离线不会把游标永久卡死），
	// 但停在第一个仍在等待窗口内的空小时（base+7200）之前（证明迟到的小时还留着机会）。
	// 旧断言恰为 base+3600，但当时的理由是「空小时照常推进」—— 这条测试现在同时
	// 钉住两个方向的边界，理由必须按新契约写清楚。
	if cur, _ := f.cursor(t); cur != rollupBaseTS+rollupHourSec {
		t.Fatalf("cursor_1h = %d, want %d（越过已过窗口的空小时、停在窗口内的空小时之前）",
			cur, rollupBaseTS+rollupHourSec)
	}
}

// 7a. **本修复的核心断言**：空小时且**新**（距今 < emptyHourGrace）→ `cursor_1h` 不推进、
// 下一轮重试同一个小时；随后补上该小时的 5m 行 → 下一轮能写出 1h 行。
//
// 这证明的是「迟到的小时最终会被补上」：修复前，空小时会被当作「确实没有数据」越过去，
// 于是该小时的 5m 行稍后落库时，1h 表里那一行永久缺失（>30 天窗口只有 1h 表可查）。
func TestRollupYoungEmptyHourHoldsCursorUntilRowsArrive(t *testing.T) {
	if defaultEmptyHourGrace != 2*time.Hour {
		t.Fatalf("defaultEmptyHourGrace = %v, want 2h（计划给定：flush 每 5 分钟一轮，2h 是极宽余量）",
			defaultEmptyHourGrace)
	}
	now := rollupBaseTS + 3*rollupHourSec + 60
	const lateHour = rollupBaseTS + rollupHourSec // 这个小时的 5m 行「迟到」
	f := newRollupFixture(t, now, false, 0)
	// 显式把水位放在 base：这一轮只处理 base+3600 起的小时，断言不被 bootstrap 干扰。
	f.setCursor(t, rollupBaseTS)

	// ── 第 1 轮：lateHour 一行 5m 都没有，且它刚结束 3660s（< 2h 等待窗口）──
	stats1, err := f.svc.RollupOnce(context.Background())
	if err != nil {
		t.Fatalf("RollupOnce(1): %v", err)
	}
	if stats1.HoursSkipped != 2 || stats1.HoursWritten != 0 || stats1.HoursScanned != 2 {
		t.Fatalf("stats(1) = %+v, want 2 scanned / 0 written / 2 skipped", stats1)
	}
	if f.count1h(t) != 0 {
		t.Fatalf("1h 行数 = %d, want 0（空小时不写行）", f.count1h(t))
	}
	if cur, ok := f.cursor(t); !ok || cur != rollupBaseTS {
		t.Fatalf("cursor_1h = %d(ok=%v), want %d：空小时仍在等待窗口内，水位**不得推进**",
			cur, ok, rollupBaseTS)
	}
	if f.hourRow(t, lateHour) != nil {
		t.Fatal("lateHour 还没有 5m 行，却写出了 1h 行")
	}

	// ─ 第 2 轮（数据仍未到）：必须**重试同一个小时**，水位依旧不动 ──
	stats2, err := f.svc.RollupOnce(context.Background())
	if err != nil {
		t.Fatalf("RollupOnce(2): %v", err)
	}
	if stats2.HoursScanned != 2 || stats2.HoursSkipped != 2 {
		t.Fatalf("stats(2) = %+v, want 2 scanned / 2 skipped（下一轮必须重试同一批小时）", stats2)
	}
	if cur, _ := f.cursor(t); cur != rollupBaseTS {
		t.Fatalf("cursor_1h = %d, want %d（等待窗口内不得推进）", cur, rollupBaseTS)
	}

	// ── 第 3 轮：5m 行终于落库（flush 补齐 / 落后一轮恢复）→ 必须写出 1h 行 ──
	f.seed5m(t, fullHourRows(lateHour, func(int) fiveMin {
		return fiveMin{samples: 30, cpu: 42}
	})...)
	stats3, err := f.svc.RollupOnce(context.Background())
	if err != nil {
		t.Fatalf("RollupOnce(3): %v", err)
	}
	if stats3.HoursWritten != 1 {
		t.Fatalf("stats(3) = %+v, want 1 written（迟到的 5m 行必须被回滚出来）", stats3)
	}
	row := f.hourRow(t, lateHour)
	if row == nil {
		t.Fatal("迟到的小时最终没有写出 1h 行：正是本修复要闭合的永久空洞")
	}
	if row.Samples != 360 || row.CPUUsedPercent == nil || *row.CPUUsedPercent != 42 {
		t.Fatalf("补出来的 1h 行 = samples %d / cpu %v, want 360 / 42", row.Samples, row.CPUUsedPercent)
	}
	// 水位前移到 lateHour（它已被写出），但仍不越过它后面那个仍在窗口内的空小时。
	if cur, _ := f.cursor(t); cur != lateHour {
		t.Fatalf("cursor_1h = %d, want %d（补上后水位前移到该小时）", cur, lateHour)
	}
}

// 7b. 边界：恰好等于 emptyHourGrace 时的行为必须**明确**并钉住 ——
// 取值约定是 `age < grace` 才算「还在等」，故 age == grace 的那一秒视为**已过窗口** → 推进游标；
// age == grace−1s 则仍在窗口内 → 扣住游标。两条相邻断言把不等号方向钉死。
func TestRollupEmptyHourGraceBoundary(t *testing.T) {
	const hour = rollupBaseTS + rollupHourSec // 被测的空小时
	// hour 的结束时刻 = base+7200；让 now 恰为「结束 + grace」→ age == grace。
	atBoundary := hour + rollupHourSec + int64(defaultEmptyHourGrace/time.Second)

	// （a）age == grace → 不再等，水位越过这个空小时。
	f := newRollupFixture(t, atBoundary, false, 0)
	f.setCursor(t, rollupBaseTS)
	stats, err := f.svc.RollupOnce(context.Background())
	if err != nil {
		t.Fatalf("RollupOnce(边界): %v", err)
	}
	// upper = alignDown(now−CloseGrace) = base+10800，故区间里有两个小时：
	// hour（age == grace → 越过）与 base+7200（age = 3600s < grace → 仍在窗口内，扣住水位）。
	if stats.HoursScanned != 2 || stats.HoursSkipped != 2 || stats.HoursWritten != 0 {
		t.Fatalf("stats = %+v, want 2 scanned / 2 skipped / 0 written", stats)
	}
	if cur, _ := f.cursor(t); cur != hour {
		t.Fatalf("cursor_1h = %d, want %d：age(%ds) == grace(%v) 视为**已过窗口**（约定 age < grace 才等待）",
			cur, hour, defaultEmptyHourGrace/time.Second, defaultEmptyHourGrace)
	}

	// （b）age == grace − 1s → 仍在窗口内，水位不得推进。
	f2 := newRollupFixture(t, atBoundary-1, false, 0)
	f2.setCursor(t, rollupBaseTS)
	if _, err := f2.svc.RollupOnce(context.Background()); err != nil {
		t.Fatalf("RollupOnce(边界-1s): %v", err)
	}
	if cur, _ := f2.cursor(t); cur != rollupBaseTS {
		t.Fatalf("cursor_1h = %d, want %d：age 比 grace 少 1 秒，仍须等待（不等号方向被改反了）",
			cur, rollupBaseTS)
	}

	// （c）WithEmptyHourGrace 只接受正数：传 0/负数必须保持默认，不得静默关掉等待窗口
	//（关掉它就等于退回「1h 永久空洞」的老缺陷）。
	if got := f.svc.WithEmptyHourGrace(0).emptyHourGrace; got != defaultEmptyHourGrace {
		t.Fatalf("WithEmptyHourGrace(0) 后 grace = %v, want 默认 %v（非正数不得静默生效）",
			got, defaultEmptyHourGrace)
	}
	if got := f.svc.WithEmptyHourGrace(-time.Hour).emptyHourGrace; got != defaultEmptyHourGrace {
		t.Fatalf("WithEmptyHourGrace(-1h) 后 grace = %v, want 默认 %v", got, defaultEmptyHourGrace)
	}

	// （d）可注入：把窗口收到 30 分钟，同一个空小时（结束于 base+7200，now = base+10860 → age 3660s
	// > 1800s）就变成「已过窗口」→ 水位越过它。（默认 2h 窗口下同一个小时是扣住的，
	// 见 TestRollupYoungEmptyHourHoldsCursorUntilRowsArrive。）
	f3 := newRollupFixture(t, rollupBaseTS+3*rollupHourSec+60, false, 0)
	f3.svc = f3.svc.WithEmptyHourGrace(30 * time.Minute)
	f3.setCursor(t, rollupBaseTS)
	if _, err := f3.svc.RollupOnce(context.Background()); err != nil {
		t.Fatalf("RollupOnce(注入 30m): %v", err)
	}
	if cur, _ := f3.cursor(t); cur != hour {
		t.Fatalf("cursor_1h = %d, want %d：窗口注入为 30m 后，age 3660s 的空小时应视为已过窗口",
			cur, hour)
	}
}

// 8. CloseGrace：还在收数据的当前小时不得回滚；等它闭了再回滚（不得丢）。
func TestRollupCloseGraceSkipsStillOpenHour(t *testing.T) {
	// now = base+70s：base 这个小时刚结束 70s，CloseGrace = 2×10s 让上界退到 base。
	f := newRollupFixture(t, rollupBaseTS+70, false, 0)
	f.seed5m(t, fullHourRows(rollupBaseTS, func(int) fiveMin {
		return fiveMin{samples: 30, cpu: 20}
	})...)

	stats, err := f.svc.RollupOnce(context.Background())
	if err != nil {
		t.Fatalf("RollupOnce: %v", err)
	}
	if stats.HoursWritten != 0 {
		t.Fatalf("stats = %+v, want 0 written（宽限期内的小时不得回滚）", stats)
	}
	if n := f.count1h(t); n != 0 {
		t.Fatalf("1h 行数 = %d, want 0", n)
	}

	// 时间前进一小时（小时已闭）→ 必须被回滚出来。
	f.clock.t = time.Unix(rollupBaseTS+rollupHourSec+70, 0)
	stats2, err := f.svc.RollupOnce(context.Background())
	if err != nil {
		t.Fatalf("RollupOnce(2): %v", err)
	}
	if stats2.HoursWritten != 1 {
		t.Fatalf("stats(2) = %+v, want 1 written", stats2)
	}
	if f.hourRow(t, rollupBaseTS) == nil {
		t.Fatal("小时闭了之后必须被回滚（宽限期只是延后，不是丢弃）")
	}
}

// 9. 设备枚举失败必须上抛（任务层要靠它记 job_log，不得静默空跑）。
func TestRollupDeviceEnumerationErrorIsReturned(t *testing.T) {
	f := newRollupFixture(t, rollupBaseTS+2*rollupHourSec+60, false, 0)
	svc := NewAgentMetricsRollupService(f.repo,
		fakeDeviceSource{err: errors.New("redis down")}, fakeConfig{reportSec: rollupReportSec}, f.log).
		WithCursorStore(f.rdb).WithClock(f.clock.now)

	stats, err := svc.RollupOnce(context.Background())
	if err == nil {
		t.Fatal("枚举设备失败必须上抛")
	}
	if errors.Is(err, ErrRollupPartial) {
		t.Fatalf("err = %v：枚举失败不是「部分失败」，不该套 sentinel", err)
	}
	if stats.HoursScanned != 0 {
		t.Fatalf("stats = %+v, want 零值", stats)
	}
}

// ── Task 2：按需重扫（迟到落库的 5m 行补出 1h 空洞）──────────────

// rollupCfg 是**按配置键回值**的配置替身。
//
// 为什么不复用 flush 测试的 fakeConfig：它对任何键都回 reportSec（10），于是
// `sys.agent.rollupRescanHours` 会读成 10 —— 本任务要断言的是**窗口边界**（几个小时
// 之前的小时补、再老的不补），窗口长度必须由测试显式给定；而 fakeConfig 是 flush 的夹具，
// 不该为了 rollup 的断言去改它。
type rollupCfg struct {
	// rescanHours = 0 表示**缺键**（回落到调用方给的默认值，模拟「配置面板里没加这条」）。
	rescanHours int
}

func (c rollupCfg) GetString(_ context.Context, _ string, def string) string { return def }

func (c rollupCfg) GetInt(_ context.Context, key string, def int) int {
	switch key {
	case configAgentRollupRescanHours:
		if c.rescanHours == 0 {
			return def
		}
		return c.rescanHours
	case configAgentReportInterval:
		return rollupReportSec
	default:
		return def
	}
}

// withRescanHours 把 fixture 的服务换成「重扫窗口 = n 小时」的那一份（其余装配逐字相同）。
func (f *rollupFixture) withRescanHours(n int) *rollupFixture {
	var metrics RollupMetricRepo = f.repo
	if f.writer != nil {
		metrics = f.writer
	}
	f.svc = NewAgentMetricsRollupService(metrics, fakeDeviceSource{ids: []uint64{rollupDevID}},
		rollupCfg{rescanHours: n}, f.log).
		WithCursorStore(f.rdb).
		WithClock(f.clock.now)
	return f
}

// bucketSetFailer 让**重扫的集合读**失败，其余读写走真仓储（普通区间照常成功）。
type bucketSetFailer struct {
	RollupMetricRepo
	err error
}

func (b bucketSetFailer) ExistingBucketTimestamps(context.Context, string, uint64, int64, int64) ([]int64, error) {
	return nil, b.err
}

// TestRollupMissingHoursSetDifference 逐条钉住重扫「集合差」的规则（纯函数，无 I/O）：
// 只有「有 5m 行却没有 1h 行」的小时被选出、升序、同小时的多行只算一个小时、
// 未闭合的小时不选、**本轮已算过的小时（repair ∪ 普通区间）不选**。
func TestRollupMissingHoursSetDifference(t *testing.T) {
	hour := rollupBaseTS
	upper := hour + 10*rollupHourSec

	fiveMin := []int64{hour + 300, hour, hour + 3300, hour + rollupHourSec, hour + rollupHourSec + 300, hour + 2*rollupHourSec}
	oneHour := []int64{hour}

	got := missingHours(fiveMin, oneHour, upper, nil)
	want := []int64{hour + rollupHourSec, hour + 2*rollupHourSec}
	if !slices.Equal(got, want) {
		t.Fatalf("missingHours = %v, want %v（有 5m 无 1h 的小时：升序、同小时去重）", got, want)
	}

	// ① 本轮已算过的小时（repair 集合 ∪ 普通区间）必须被排除 —— 同一小时一轮只写一次。
	got = missingHours(fiveMin, oneHour, upper, map[int64]bool{hour + rollupHourSec: true})
	if !slices.Equal(got, []int64{hour + 2*rollupHourSec}) {
		t.Fatalf("missingHours(已算过 %d) = %v, want [%d]（去重：同一小时只写一次）",
			hour+rollupHourSec, got, hour+2*rollupHourSec)
	}

	// ② 未闭合的小时（h >= upper）不选：重扫绝不碰尚未闭合、可能还在收数据的小时。
	// （upper 取 h+2h：此时 h 已有 1h 行、h+3600 已闭合且缺失、h+2h 尚未闭合。）
	closed := missingHours(fiveMin, oneHour, hour+2*rollupHourSec, nil)
	if !slices.Equal(closed, []int64{hour + rollupHourSec}) {
		t.Fatalf("missingHours(upper=%d) = %v, want [%d]（只补已闭合的小时）",
			hour+2*rollupHourSec, closed, hour+rollupHourSec)
	}

	// ③ 边界与空输入
	if out := missingHours(nil, nil, upper, nil); len(out) != 0 {
		t.Fatalf("无 5m 行时 missingHours = %v, want 空（没有 5m 行就不可能有缺失的 1h 行）", out)
	}
	if out := missingHours([]int64{hour + 60}, []int64{hour}, upper, nil); len(out) != 0 {
		t.Fatalf("该小时已有 1h 行时 missingHours = %v, want 空", out)
	}
}

// TestRollupRescanHoursConfigFallback 钉住重扫窗口的配置键与缺省值：
// 键名是契约字面量、缺省 24，且非法值（0/负数）**回落默认**而不是静默关掉重扫
// （关掉就等于退回 Plan 2D 遗留的「1h 空洞不自愈」）。
func TestRollupRescanHoursConfigFallback(t *testing.T) {
	if configAgentRollupRescanHours != "sys.agent.rollupRescanHours" {
		t.Fatalf("配置键 = %q, want sys.agent.rollupRescanHours", configAgentRollupRescanHours)
	}
	if defaultRollupRescanHours != 24 {
		t.Fatalf("defaultRollupRescanHours = %d, want 24（计划给定）", defaultRollupRescanHours)
	}
	svc := NewAgentMetricsRollupService(nil, nil, rollupCfg{}, nil)
	// 缺键（配置面板里没有这条）→ 默认 24。
	if got := svc.rescanWindowHours(); got != defaultRollupRescanHours {
		t.Fatalf("缺键时重扫窗口 = %d, want %d", got, defaultRollupRescanHours)
	}
	// 非法值（<=0，含被误改成负数）→ 同样回落默认，而不是静默把重扫关掉
	// （显式配成 "0" 在效果上等同于缺键：两种都落在同一处默认值上，由 `n <= 0` 兜住）。
	for _, bad := range []int{-1, -3} {
		neg := NewAgentMetricsRollupService(nil, nil, rollupCfg{rescanHours: bad}, nil)
		if got := neg.rescanWindowHours(); got != defaultRollupRescanHours {
			t.Fatalf("配置为 %d 时重扫窗口 = %d, want 默认 %d（非法值不得静默关掉重扫）",
				bad, got, defaultRollupRescanHours)
		}
	}
	// 显式配置生效
	on := NewAgentMetricsRollupService(nil, nil, rollupCfg{rescanHours: 6}, nil)
	if got := on.rescanWindowHours(); got != 6 {
		t.Fatalf("配置为 6 时重扫窗口 = %d, want 6", got)
	}
}

// TestRollupRescanBackfillsLateHourAfterCursorPassed 是**本任务的核心断言**：
// 一个「当时一行 5m 都没有」的小时——水位已经越过它（超出 emptyHourGrace）——
// 在它的 5m 行**迟到落库**之后，下一轮必须把它补成 1h 行；且这条路径不推进水位、幂等。
//
// 为什么修前必须红：普通区间的下界恒为 `cursor+3600`，永远不会回头看它；repair 集合也
// 不含它（repair 只装「写出过残缺行」的小时）。那个小时因此在 1h 表里永久缺失，
// 而 >30 天的窗口只有 1h 表可查。
func TestRollupRescanBackfillsLateHourAfterCursorPassed(t *testing.T) {
	now := rollupBaseTS + 6*rollupHourSec + 60 // upper = base+6h
	f := newRollupFixture(t, now, true, 0)     // spy：数清这一轮到底写了几行
	f.withRescanHours(12)

	// 一开始只有 base 有 5m 行：base+1h / base+2h 当时都空 → 水位越过它们（设备离线一小时）
	f.seed5m(t, fullHourRows(rollupBaseTS, func(int) fiveMin {
		return fiveMin{samples: 30, cpu: 20}
	})...)
	if _, err := f.svc.RollupOnce(context.Background()); err != nil {
		t.Fatalf("RollupOnce(1): %v", err)
	}
	const lateHour = rollupBaseTS + 2*rollupHourSec
	cur1, ok := f.cursor(t)
	if !ok || cur1 < lateHour {
		t.Fatalf("前置不成立：水位 = %d(ok=%v) 尚未越过 %d（水位没越过去就测不到「迟到」这件事）",
			cur1, ok, lateHour)
	}
	if f.hourRow(t, lateHour) != nil {
		t.Fatal("前置不成立：lateHour 此刻不该有 1h 行")
	}

	// 迟到落库：lateHour 的 12 行 5m 行在等待窗口**之后**才到（flush 停顿数小时后补上）
	f.seed5m(t, fullHourRows(lateHour, func(int) fiveMin {
		return fiveMin{samples: 30, cpu: 42}
	})...)
	f.writer.calls = 0
	stats2, err := f.svc.RollupOnce(context.Background())
	if err != nil {
		t.Fatalf("RollupOnce(2): %v", err)
	}
	row := f.hourRow(t, lateHour)
	if row == nil {
		t.Fatal("迟到落库的 5m 行没有补出 1h 行：正是本修复要闭合的永久空洞" +
			"（>30 天的窗口只有 1h 表可查）")
	}
	if row.Samples != 360 || row.CPUUsedPercent == nil || *row.CPUUsedPercent != 42 {
		t.Fatalf("补出来的 1h 行 = samples %d / cpu %v, want 360 / 42（口径不变：按 samples 加权）",
			row.Samples, row.CPUUsedPercent)
	}
	if stats2.HoursRescanned != 1 || stats2.HoursBackfilled != 1 || stats2.HoursWritten != 1 {
		t.Fatalf("stats(2) = %+v, want 1 rescanned / 1 backfilled / 1 written", stats2)
	}
	if f.writer.calls != 1 {
		t.Fatalf("本轮 WriteHour 调用 = %d, want 1（只为那个缺失的小时写一次）", f.writer.calls)
	}
	// 不推进水位：重扫只补行，水位由普通区间与 repair 管（本轮普通区间里的小时都是空且仍在等待窗口内）
	if cur2, _ := f.cursor(t); cur2 != cur1 {
		t.Fatalf("cursor_1h = %d, want %d（重扫不得改动水位）", cur2, cur1)
	}

	// 幂等：第三轮按集合差算出「无缺失」→ 一次写都不产生、1h 行数不变
	f.writer.calls = 0
	stats3, err := f.svc.RollupOnce(context.Background())
	if err != nil {
		t.Fatalf("RollupOnce(3): %v", err)
	}
	if stats3.HoursRescanned != 0 || stats3.HoursBackfilled != 0 {
		t.Fatalf("stats(3) = %+v, want 0 rescanned / 0 backfilled（补齐后集合差为空）", stats3)
	}
	if f.writer.calls != 0 {
		t.Fatalf("补齐之后的轮次不得再写：WriteHour 调用 = %d, want 0（幂等）", f.writer.calls)
	}
	if n := f.count1h(t); n != 2 {
		t.Fatalf("1h 行数 = %d, want 2（base + lateHour）", n)
	}
}

// TestRollupRescanOnlyWritesMissingHours 钉住**代价有界**（本任务的选型理由）：
// 一轮里已齐全的小时**不得**被重写，WriteHour 的调用次数必须恰好等于缺失的小时数。
//
// 反向对照是「无脑重扫窗口里的 K 个小时」：那会在本轮把 base / base+2h 两个已正确的小时
// 原样重写一遍（3 次写而不是 1 次），并把这条断言打红。
func TestRollupRescanOnlyWritesMissingHours(t *testing.T) {
	now := rollupBaseTS + 6*rollupHourSec + 60
	f := newRollupFixture(t, now, true, 0)
	f.withRescanHours(12)

	// base 与 base+2h 是**完整**小时；base+1h 当时一行 5m 都没有（水位越过它）
	f.seed5m(t, fullHourRows(rollupBaseTS, func(int) fiveMin {
		return fiveMin{samples: 30, cpu: 10}
	})...)
	f.seed5m(t, fullHourRows(rollupBaseTS+2*rollupHourSec, func(int) fiveMin {
		return fiveMin{samples: 30, cpu: 30}
	})...)
	stats1, err := f.svc.RollupOnce(context.Background())
	if err != nil {
		t.Fatalf("RollupOnce(1): %v", err)
	}
	const lateHour = rollupBaseTS + rollupHourSec
	if stats1.HoursWritten != 2 || stats1.HoursBackfilled != 0 {
		t.Fatalf("stats(1) = %+v, want 2 written / 0 backfilled（已齐全的一轮：重扫一次写都没有）", stats1)
	}
	if f.writer.calls != 2 {
		t.Fatalf("stats(1) 轮 WriteHour 调用 = %d, want 2：正常一轮不得有任何多余的写", f.writer.calls)
	}
	if f.hourRow(t, lateHour) != nil {
		t.Fatal("前置不成立：lateHour 此刻不该有 1h 行")
	}

	// 迟到落库：只有 lateHour 是缺失的
	f.seed5m(t, fullHourRows(lateHour, func(int) fiveMin {
		return fiveMin{samples: 30, cpu: 20}
	})...)
	f.writer.calls = 0
	stats2, err := f.svc.RollupOnce(context.Background())
	if err != nil {
		t.Fatalf("RollupOnce(2): %v", err)
	}
	if f.writer.calls != 1 {
		t.Fatalf("WriteHour 调用 = %d, want 1 —— 只为**真正缺失**的小时写；"+
			"已齐全的小时（base / base+2h）不得被重写", f.writer.calls)
	}
	if stats2.HoursRescanned != 1 || stats2.HoursBackfilled != 1 {
		t.Fatalf("stats(2) = %+v, want 1 rescanned / 1 backfilled", stats2)
	}
	row := f.hourRow(t, lateHour)
	if row == nil || row.Samples != 360 || row.CPUUsedPercent == nil || *row.CPUUsedPercent != 20 {
		t.Fatalf("补齐的 1h 行 = %+v, want samples 360 / cpu 20", row)
	}
	// 已齐全的两个小时仍是**原值**（没有被重算扰动）
	for _, c := range []struct {
		hour int64
		cpu  float64
	}{{rollupBaseTS, 10}, {rollupBaseTS + 2*rollupHourSec, 30}} {
		got := f.hourRow(t, c.hour)
		if got == nil || got.CPUUsedPercent == nil || *got.CPUUsedPercent != c.cpu {
			t.Fatalf("小时 %d 的 1h 行 = %+v, want cpu %v（已齐全的小时不得被重写）", c.hour, got, c.cpu)
		}
	}
}

// TestRollupRescanWindowBoundary 钉住重扫窗口的**边界**（这是明确的能力边界，不是 bug）：
// 窗口 `[alignDown(now,3600) − rescanHours×3600, upper)` 内缺失的小时补、
// 窗口外（更老）的不补、当前正在填充的小时**绝不**碰；且重扫不改动水位。
func TestRollupRescanWindowBoundary(t *testing.T) {
	now := rollupBaseTS + 8*rollupHourSec + 60 // alignDown(now) = base+8h，upper = base+8h
	f := newRollupFixture(t, now, false, 0)
	f.withRescanHours(4) // 窗口 = [base+4h, base+8h)
	// 水位显式放到 base+7h：普通区间退化为空区间，本轮唯一的写入只可能来自重扫。
	f.setCursor(t, rollupBaseTS+7*rollupHourSec)

	const (
		tooOld  = rollupBaseTS + 3*rollupHourSec // 窗口**之外**（更老 1 小时）
		oldest  = rollupBaseTS + 4*rollupHourSec // 恰在窗口下界（含）
		mid     = rollupBaseTS + 5*rollupHourSec // 窗口内
		current = rollupBaseTS + 8*rollupHourSec // 当前正在填充的小时（窗口上界之外）
	)
	seed := func(hour int64, cpu float64) {
		f.seed5m(t, fullHourRows(hour, func(int) fiveMin {
			return fiveMin{samples: 30, cpu: cpu}
		})...)
	}
	seed(tooOld, 31)
	seed(oldest, 41)
	seed(mid, 51)
	seed(current, 61)

	stats, err := f.svc.RollupOnce(context.Background())
	if err != nil {
		t.Fatalf("RollupOnce: %v", err)
	}
	if stats.HoursScanned != 0 {
		t.Fatalf("stats = %+v, want 0 scanned（水位已在 base+7h，普通区间为空）", stats)
	}
	if stats.HoursRescanned != 2 || stats.HoursBackfilled != 2 {
		t.Fatalf("stats = %+v, want 2 rescanned / 2 backfilled（窗口内两个缺失小时）", stats)
	}
	for _, c := range []struct {
		hour int64
		cpu  float64
	}{{oldest, 41}, {mid, 51}} {
		row := f.hourRow(t, c.hour)
		if row == nil {
			t.Fatalf("小时 %d 在窗口内且缺 1h 行，必须被补出来", c.hour)
		}
		if row.Samples != 360 || row.CPUUsedPercent == nil || *row.CPUUsedPercent != c.cpu {
			t.Fatalf("小时 %d 的 1h 行 = samples %d / cpu %v, want 360 / %v", c.hour, row.Samples, row.CPUUsedPercent, c.cpu)
		}
	}
	if f.hourRow(t, tooOld) != nil {
		t.Fatal("超出 rollupRescanHours 的更老小时不得被重扫：这是**明确的能力边界**" +
			"（更老的空洞需手工回退水位再重放），必须被钉住而不是被当成 bug")
	}
	if f.hourRow(t, current) != nil {
		t.Fatal("当前正在填充的小时不得被重扫（窗口不含它；写了就会把还在收数据的小时落成权威值）")
	}
	if cur, _ := f.cursor(t); cur != rollupBaseTS+7*rollupHourSec {
		t.Fatalf("cursor_1h = %d, want %d（重扫不得改动水位）", cur, rollupBaseTS+7*rollupHourSec)
	}

	// ── (b) 闭合边界：h == upper（刚结束、仍在 CloseGrace 内）的小时**不得**被重扫。
	// 计划把窗口写成 [alignDown(now,3600) − K×3600, alignDown(now,3600))，而两者只在
	// 「当前小时的头 CloseGrace 秒」里不同：那时 alignDown(now,3600) 会把一个**刚结束、
	// 随时可能再收到数据**的小时纳进来，重扫就会为它写出一个可能残缺的 1h 行 ——
	// 而 CloseGrace 的纪律恰恰是「还在收数据的小时不得被写成权威值」
	// （见 TestRollupCloseGraceSkipsStillOpenHour）。这一条把该边界端到端钉死：
	// 未闭合的小时不补、已闭合且缺失的照补。
	f2 := newRollupFixture(t, rollupBaseTS+8*rollupHourSec+10, false, 0) // 进入小时 10s（CloseGrace = 20s）
	f2.withRescanHours(4)
	f2.setCursor(t, rollupBaseTS+7*rollupHourSec) // 普通区间为空：断言只看重扫
	const (
		inGrace = rollupBaseTS + 7*rollupHourSec // == upper：刚结束 10s，仍在宽限期内
		closed  = rollupBaseTS + 5*rollupHourSec // 已闭合且缺 1h 行 → 必须补（证明重扫确实跑了）
	)
	seedB := func(hour int64, cpu float64) {
		f2.seed5m(t, fullHourRows(hour, func(int) fiveMin {
			return fiveMin{samples: 30, cpu: cpu}
		})...)
	}
	seedB(inGrace, 71)
	seedB(closed, 81)
	stats2, err := f2.svc.RollupOnce(context.Background())
	if err != nil {
		t.Fatalf("RollupOnce(宽限期内的小时): %v", err)
	}
	if f2.hourRow(t, closed) == nil {
		t.Fatal("已闭合且缺 1h 行的小时在窗口内，必须被补出来（证明重扫真的跑了）")
	}
	if f2.hourRow(t, inGrace) != nil {
		t.Fatal("h == upper（刚结束、仍在 CloseGrace 内）的小时不得被重扫：" +
			"只补**已闭合**的小时，否则会把还在收数据的小时落成权威值")
	}
	if stats2.HoursRescanned != 1 || stats2.HoursBackfilled != 1 {
		t.Fatalf("stats = %+v, want 1 rescanned / 1 backfilled（只剩已闭合的那个缺失小时）", stats2)
	}
	if cur, _ := f2.cursor(t); cur != rollupBaseTS+7*rollupHourSec {
		t.Fatalf("cursor_1h = %d, want %d（重扫不得改动水位）", cur, rollupBaseTS+7*rollupHourSec)
	}
}

// TestRollupRescanWritesRepairHourOnce 钉住与 repair 的关系：
//   - repair 集合里的小时**同样**要参与本轮（它缺 1h 行时会被重算写出）；
//   - 当同一个小时同时落在 repair 与重扫两条路径上时，一轮**只写一次**（去重）；
//   - 另一个**只属于重扫**的迟到小时同轮被补出来 —— 这一条保证「重扫真的跑了」，
//     否则本测试会因为「repair 自己写了那一次」而变成一条与重扫无关的恒绿断言。
func TestRollupRescanWritesRepairHourOnce(t *testing.T) {
	now := rollupBaseTS + 6*rollupHourSec + 60
	f := newRollupFixture(t, now, true, 0)
	f.withRescanHours(12)

	// 一个**残缺**小时（7 行）：1h 行被写出来并进 repair 集合
	const repairHour = rollupBaseTS + 2*rollupHourSec
	f.seed5m(t, fullHourRows(repairHour, func(int) fiveMin {
		return fiveMin{samples: 30, cpu: 10}
	})[:7]...)
	if _, err := f.svc.RollupOnce(context.Background()); err != nil {
		t.Fatalf("RollupOnce(1): %v", err)
	}
	if !f.inRepair(t, repairHour) {
		t.Fatal("前置不成立：残缺小时必须已进 repair 集合")
	}
	// 让这个小时同时满足「有 5m 行 + 在重扫窗口内 + 缺 1h 行」——两条路径都指向它
	f.drop1hRange(t, repairHour, repairHour)
	// 另一个**只属于重扫**的迟到小时（普通区间已经越过它，repair 集合里也没有它）
	const lateHour = rollupBaseTS + rollupHourSec
	f.seed5m(t, fullHourRows(lateHour, func(int) fiveMin {
		return fiveMin{samples: 30, cpu: 20}
	})...)

	f.writer.calls = 0
	f.writer.written = nil
	stats, err := f.svc.RollupOnce(context.Background())
	if err != nil {
		t.Fatalf("RollupOnce(2): %v", err)
	}
	// 两次写：repairHour（repair 路径）+ lateHour（重扫路径）
	if f.writer.calls != 2 {
		t.Fatalf("WriteHour 调用 = %d, want 2（repair 小时一次 + 只属于重扫的迟到小时一次）", f.writer.calls)
	}
	writes := map[int64]int{}
	for _, w := range f.writer.written {
		writes[w.BucketTS]++
	}
	if writes[repairHour] != 1 {
		t.Fatalf("小时 %d 本轮写了 %d 次，want 1：它同时属于 repair 与重扫，一轮只能写一次（去重）",
			repairHour, writes[repairHour])
	}
	if writes[lateHour] != 1 {
		t.Fatalf("小时 %d 本轮写了 %d 次，want 1（它只属于重扫：必须被补出来，证明重扫真的跑了）",
			lateHour, writes[lateHour])
	}
	if stats.HoursRepaired != 1 || stats.HoursBackfilled != 1 {
		t.Fatalf("stats = %+v, want 1 repaired / 1 backfilled", stats)
	}
	if stats.HoursRescanned != 1 {
		t.Fatalf("stats = %+v, want 1 rescanned（repair 那个小时不得被重扫重复计入）", stats)
	}
	if !f.inRepair(t, repairHour) {
		t.Fatal("重扫不得替 repair 做决定：小时仍残缺，必须留在集合里等下一轮重算")
	}
	if row := f.hourRow(t, repairHour); row == nil || row.Samples != 210 {
		t.Fatalf("1h 行 = %+v, want samples 210（7 行 × 30，残缺值好过空洞）", row)
	}
	if row := f.hourRow(t, lateHour); row == nil || row.Samples != 360 {
		t.Fatalf("迟到小时 %d 的 1h 行 = %+v, want samples 360", lateHour, row)
	}
}

// TestRollupRescanReadFailureSurfacesAndKeepsCursor 钉住重扫的失败语义：
// 集合读失败**必须上抛**（静默吞掉 = 空洞永远补不上且无人知道），而且它**不参与水位判定**
// —— 同场景、重扫读正常时水位落在同一处。
func TestRollupRescanReadFailureSurfacesAndKeepsCursor(t *testing.T) {
	now := rollupBaseTS + 6*rollupHourSec + 60
	f := newRollupFixture(t, now, false, 0)
	seed := func(x *rollupFixture) {
		x.seed5m(t, fullHourRows(rollupBaseTS, func(int) fiveMin {
			return fiveMin{samples: 30, cpu: 20}
		})...)
	}
	seed(f)
	f.svc = NewAgentMetricsRollupService(
		bucketSetFailer{RollupMetricRepo: f.repo, err: errors.New("db down")},
		fakeDeviceSource{ids: []uint64{rollupDevID}}, rollupCfg{rescanHours: 12}, f.log).
		WithCursorStore(f.rdb).WithClock(f.clock.now)

	stats, err := f.svc.RollupOnce(context.Background())
	if err == nil {
		t.Fatal("重扫的集合读失败必须上抛（静默吞掉 = 1h 空洞不补且无人知道）")
	}
	if !errors.Is(err, ErrRollupPartial) {
		t.Fatalf("err = %v, want ErrRollupPartial", err)
	}
	if stats.Errors != 1 {
		t.Fatalf("stats = %+v, want 1 error", stats)
	}
	// 普通区间已成功：1h 行照写出（重扫失败不得连坐已经写好的行）
	if f.hourRow(t, rollupBaseTS) == nil {
		t.Fatal("重扫失败不得影响普通区间已写出的 1h 行")
	}
	cur, ok := f.cursor(t)
	if !ok {
		t.Fatal("cursor_1h 键丢失：重扫失败不得删除游标")
	}

	// 对照：同场景、重扫读正常时水位落在同一处 —— 直接证明重扫不参与水位判定。
	f2 := newRollupFixture(t, now, false, 0)
	seed(f2)
	f2.withRescanHours(12)
	if _, err := f2.svc.RollupOnce(context.Background()); err != nil {
		t.Fatalf("RollupOnce(对照): %v", err)
	}
	cur2, _ := f2.cursor(t)
	if cur != cur2 {
		t.Fatalf("重扫读失败时水位 = %d，重扫正常时 = %d（必须相同：重扫只补行，水位由普通区间与 repair 管）",
			cur, cur2)
	}
}
