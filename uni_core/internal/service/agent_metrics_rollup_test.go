package service

import (
	"context"
	"errors"
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

	// 水位前移到最后一个已处理的小时：区间是 [base, base+10800)，故最后一个小时是
	// base+7200（base+3600 / base+7200 是空小时，空小时照常推进）。
	if cur, ok := f.cursor(t); !ok || cur != rollupBaseTS+2*rollupHourSec {
		t.Fatalf("cursor_1h = %d(ok=%v), want %d", cur, ok, rollupBaseTS+2*rollupHourSec)
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
	if !ok || cursorAfterFirst != rollupBaseTS+2*rollupHourSec {
		t.Fatalf("cursor_1h = %d(ok=%v), want %d", cursorAfterFirst, ok, rollupBaseTS+2*rollupHourSec)
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
	// repair 优先：这个小时的水位已经在游标**之后**，普通区间 [cursor+3600, upper) 是空的，
	// 故本轮扫到的那 1 个小时只可能来自 repair 集合。
	if stats2.HoursScanned != 1 || stats2.HoursRepaired != 1 || stats2.HoursWritten != 1 {
		t.Fatalf("stats(2) = %+v, want 1 scanned / 1 repaired / 1 written（repair 必须被优先重算）", stats2)
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
	if stats.HoursSkipped != 1 || stats.HoursWritten != 0 {
		t.Fatalf("stats(2) = %+v, want 1 skipped / 0 written（无 5m 行 → 不写空行）", stats)
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
	if cur != rollupBaseTS+rollupHourSec {
		t.Fatalf("cursor_1h = %d, want %d（最后一个已处理的小时，空小时照常推进）",
			cur, rollupBaseTS+rollupHourSec)
	}

	stats2, err := f.svc.RollupOnce(context.Background())
	if err != nil {
		t.Fatalf("RollupOnce(2): %v", err)
	}
	if stats2.HoursScanned != 0 || stats2.HoursWritten != 0 {
		t.Fatalf("stats(2) = %+v, want 0/0（水位已前移，不得重复回滚）", stats2)
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
	if cur2, _ := f.cursor(t); cur2 != rollupBaseTS+rollupHourSec {
		t.Fatalf("cursor_1h = %d, want %d（成功后前移）", cur2, rollupBaseTS+rollupHourSec)
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

// 7. 空小时：不写空行，但照常推进游标（否则长期空闲的设备会卡在第一个空小时）。
func TestRollupSkipsEmptyHourWithoutRow(t *testing.T) {
	now := rollupBaseTS + 2*rollupHourSec + 60
	f := newRollupFixture(t, now, false, 0)
	f.seed5m(t, fullHourRows(rollupBaseTS, func(int) fiveMin {
		return fiveMin{samples: 30, cpu: 20}
	})...)

	stats, err := f.svc.RollupOnce(context.Background())
	if err != nil {
		t.Fatalf("RollupOnce: %v", err)
	}
	if stats.HoursSkipped != 1 || stats.HoursWritten != 1 {
		t.Fatalf("stats = %+v, want 1 skipped / 1 written", stats)
	}
	if f.count1h(t) != 1 {
		t.Fatalf("1h 行数 = %d, want 1（空小时绝不写空行）", f.count1h(t))
	}
	if f.hourRow(t, rollupBaseTS+rollupHourSec) != nil {
		t.Fatal("空小时产出了 1h 行")
	}
	if cur, _ := f.cursor(t); cur != rollupBaseTS+rollupHourSec {
		t.Fatalf("cursor_1h = %d, want %d（空小时照常推进）", cur, rollupBaseTS+rollupHourSec)
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
