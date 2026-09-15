package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/glebarez/sqlite"
	goredis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agentmetrics"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/database"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/snowflake"
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
)

// ── 夹具常量：全部时间都是「对齐到 5min 边界的 unix 秒」，故 bucket_ts 可逐字写死 ──

const (
	flushBaseTS = int64(1800000000) // % 300 == 0
	flushDevID  = uint64(1001)
	// flushBootstrapHours 与实现的 Bootstrap 窗口**同源**（改动实现会立刻打到断言上）。
	flushBootstrapHours = 24
)

// flushCursorKey 是**契约字面量**：不复用 agentmetrics.CursorKey，否则键名写错也自洽
// （同 latest_test.go 对 latestKey 的处理）。
func flushCursorKey(deviceID uint64) string {
	return "agent:device:" + itoaU64(deviceID) + ":cursor_5m"
}

func itoaU64(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// fixedClock 是可拨动的时钟替身（游标区间直接由它推导，不用真实 now）。
type fixedClock struct{ t time.Time }

func (c *fixedClock) now() time.Time { return c.t }

// fakeConfig 是 AgentConfigGetter 的替身：只回一个固定的 reportInterval。
type fakeConfig struct{ reportSec int }

func (f fakeConfig) GetString(_ context.Context, _ string, def string) string { return def }
func (f fakeConfig) GetInt(_ context.Context, _ string, def int) int {
	if f.reportSec == 0 {
		return def
	}
	return f.reportSec
}

// fakeBucketWriter 是 DeviceMetricRepo 的替身，用于注入可控失败。
//
// 为什么用替身而不是真仓储：本任务要求「WriteBucket 失败 → 游标不推进」，
// 真仓储只有触发 SQL 错误才失败，那种失败同时污染了「行确实写不进去」这个前提。
type fakeBucketWriter struct {
	failOnCall int   // 第几次调用（1-based）失败；0 = 从不失败
	failErr    error // 失败时返回的错误

	results []error // 非空时按调用序返回（「先失败后成功」用它）

	calls   int
	written []*entity.DeviceMetricWide
	subs    []repository.MetricSubRows
}

func (f *fakeBucketWriter) WriteBucket(_ context.Context, w *entity.DeviceMetricWide, subs repository.MetricSubRows) error {
	f.calls++
	if len(f.results) > 0 {
		if f.calls <= len(f.results) && f.results[f.calls-1] != nil {
			return f.results[f.calls-1]
		}
	} else if f.failOnCall > 0 && f.calls == f.failOnCall {
		if f.failErr != nil {
			return f.failErr
		}
		return errors.New("injected WriteBucket failure")
	}
	f.written = append(f.written, w)
	f.subs = append(f.subs, subs)
	return nil
}

// flushFixture 是一次测试的全部依赖。
type flushFixture struct {
	db     *gorm.DB
	rdb    goredis.UniversalClient
	mr     *miniredis.Miniredis
	raw    *agentmetrics.RawStore
	repo   *repository.DeviceMetricRepo
	res    *repository.DeviceResourceRepo
	writer *fakeBucketWriter // 替身模式下的注入点（SVCMode 见 newFlushFixture）
	clock  *fixedClock
	svc    *AgentMetricsFlushService
	cfg    AgentConfigGetter
}

// newFlushFixture 起内存 sqlite + miniredis + 生产同款雪花回调。
//
// writer 为 nil 时用**真仓储**（落库断言需要真行）；非 nil 时用替身（故障注入）。
func newFlushFixture(t *testing.T, at int64, reportSec int, writer DeviceMetricWriter) *flushFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	// 生产同款雪花回调：资源维度的 id **只有**这一条来源。
	// DeviceResource 的注释写明「必须保留名为 ID 的字段」——回调靠 LookUpField("ID")
	// 取字段，缺了它主键永远是 0，于是第二行资源就撞 UNIQUE(device_resource.id)。
	node, err := snowflake.New(1, logger.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	database.NewCallbacks(node).Register(db)
	if err := db.AutoMigrate(
		&entity.DeviceMetricWide{}, &entity.DeviceMetricDisk{}, &entity.DeviceMetricDiskIO{},
		&entity.DeviceMetricNIC{}, &entity.DeviceMetricSensor{}, &entity.DeviceResource{},
	); err != nil {
		t.Fatal(err)
	}

	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	raw := agentmetrics.NewRawStore(rdb, agentmetrics.RawOptions{
		Step: 10 * time.Second, MaxPoints: 10000, QueryTTL: time.Second,
	})
	metricRepo := repository.NewDeviceMetricRepository(db)
	resRepo := repository.NewDeviceResourceRepository(db)

	clock := &fixedClock{t: time.Unix(at, 0)}
	cfg := fakeConfig{reportSec: reportSec}

	f := &flushFixture{db: db, rdb: rdb, mr: mr, raw: raw, repo: metricRepo, res: resRepo, clock: clock, cfg: cfg}
	if writer == nil {
		writer = metricRepo
	} else if fw, ok := writer.(*fakeBucketWriter); ok {
		f.writer = fw
	}
	f.svc = NewAgentMetricsFlushService(raw, writer, resRepo, cfg, logger.NewNop()).
		WithCursorStore(rdb).
		WithClock(clock.now)
	return f
}

// seed 压入一条原始点（走 RawStore.Append，与生产写路径同源）。
func (f *flushFixture) seed(t *testing.T, pts ...agentproto.MetricsSample) {
	t.Helper()
	for i := range pts {
		if err := f.raw.Append(context.Background(), flushDevID, &pts[i]); err != nil {
			t.Fatalf("seed raw append: %v", err)
		}
	}
}

// setCursor 显式写游标（unix 秒）。
func (f *flushFixture) setCursor(t *testing.T, sec int64) {
	t.Helper()
	if err := f.rdb.Set(context.Background(), flushCursorKey(flushDevID), sec, 0).Err(); err != nil {
		t.Fatalf("set cursor: %v", err)
	}
}

// cursor 读游标；键不存在时返回 (0,false)。
func (f *flushFixture) cursor(t *testing.T) (int64, bool) {
	t.Helper()
	v, err := f.rdb.Get(context.Background(), flushCursorKey(flushDevID)).Int64()
	if err == goredis.Nil {
		return 0, false
	}
	if err != nil {
		t.Fatalf("read cursor: %v", err)
	}
	return v, true
}

// alignDown300 是测试侧独立的对齐实现（不复用实现里的函数，否则对齐写错也自洽）。
func alignDown300(sec int64) int64 { return sec - sec%300 }

// ─ 原始样本构造 ─────────────────────────────────────────

// flushSample 造一条带磁盘+传感器明细的样本；cpu 决定均值断言的水位。
func flushSample(tMs int64, cpu float64, mount string, usedGB float64, temp float64) agentproto.MetricsSample {
	return agentproto.MetricsSample{
		T:              tMs,
		CPUUsedPercent: cpu,
		Load1:          0.5,
		MemUsedPercent: 40,
		MemUsedMB:      4096,
		Disks: []agentproto.DiskMetric{{
			Mountpoint: mount, FSType: "ext4",
			UsedPercent: usedGB, UsedGB: usedGB, TotalGB: 100,
		}},
		Sensors: []agentproto.SensorMetric{{Name: "coretemp", TemperatureC: &temp}},
	}
}

// ── Step 1 的 7 条断言 ──────────────────────────────────

// 1. 单桶落库：3 个同桶点 → 1 行宽表、samples==3、cpu 为均值。
func TestFlushSingleBucketPersistsWideRow(t *testing.T) {
	f := newFlushFixture(t, flushBaseTS+600, 10, nil)
	f.setCursor(t, flushBaseTS-300) // 只有 [base, base+300) 是已闭桶
	f.seed(t,
		flushSample(flushBaseTS*1000, 10, "/", 20, 60),
		flushSample(flushBaseTS*1000+10_000, 30, "/", 20, 66),
		flushSample(flushBaseTS*1000+20_000, 50, "/", 20, 63),
	)

	stats, err := f.svc.FlushOnce(context.Background())
	if err != nil {
		t.Fatalf("FlushOnce: %v", err)
	}
	if stats.BucketsWritten != 1 || stats.DevicesScanned != 1 || stats.Errors != 0 {
		t.Fatalf("stats = %+v, want 1 written / 1 scanned / 0 errors", stats)
	}

	var rows []entity.DeviceMetricWide
	if err := f.db.Where("device_id = ?", flushDevID).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("宽表行数 = %d, want 1", len(rows))
	}
	w := rows[0]
	if w.BucketTS != flushBaseTS {
		t.Fatalf("bucket_ts = %d, want %d", w.BucketTS, flushBaseTS)
	}
	if w.Samples != 3 {
		t.Fatalf("samples = %d, want 3", w.Samples)
	}
	if w.CPUUsedPercent == nil || *w.CPUUsedPercent != 30 {
		t.Fatalf("cpu_used_percent = %v, want 均值 30", w.CPUUsedPercent)
	}
	if w.MaxTemperatureC == nil || *w.MaxTemperatureC != 66 {
		t.Fatalf("max_temperature_c = %v, want max 66", w.MaxTemperatureC)
	}
}

// 2. 空桶不写：桶内无样本 → 不产出行。
func TestFlushEmptyBucketWritesNoRow(t *testing.T) {
	f := newFlushFixture(t, flushBaseTS+600, 10, nil)
	f.setCursor(t, flushBaseTS-300)
	// 刻意不 seed：整个 [base, base+300) 是空桶。

	stats, err := f.svc.FlushOnce(context.Background())
	if err != nil {
		t.Fatalf("FlushOnce: %v", err)
	}
	if stats.BucketsWritten != 0 {
		t.Fatalf("BucketsWritten = %d, want 0（空桶不写行）", stats.BucketsWritten)
	}
	var n int64
	f.db.Model(&entity.DeviceMetricWide{}).Count(&n)
	if n != 0 {
		t.Fatalf("宽表行数 = %d, want 0（绝不写空行）", n)
	}
}

// 3. 资源 name→id：明细行指向 device_resource 行，且跨轮幂等（只有一行维度）。
func TestFlushResolvesResourceNamesToIDs(t *testing.T) {
	f := newFlushFixture(t, flushBaseTS+600, 10, nil)
	f.setCursor(t, flushBaseTS-300)
	f.seed(t, flushSample(flushBaseTS*1000, 20, "/data", 20, 60))

	if _, err := f.svc.FlushOnce(context.Background()); err != nil {
		t.Fatalf("FlushOnce: %v", err)
	}
	var res []entity.DeviceResource
	if err := f.db.Where("device_id = ? AND kind = ?", flushDevID, agentmetrics.ResourceKindDisk).Find(&res).Error; err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("disk 资源行数 = %d, want 1", len(res))
	}
	if res[0].Name != "/data" || res[0].FSType != "ext4" {
		t.Fatalf("资源行 = %+v, want name=/data fs_type=ext4", res[0])
	}

	var disks []entity.DeviceMetricDisk
	if err := f.db.Find(&disks).Error; err != nil {
		t.Fatal(err)
	}
	if len(disks) != 1 {
		t.Fatalf("disk 明细行数 = %d, want 1", len(disks))
	}
	if disks[0].ResourceID != res[0].ID {
		t.Fatalf("明细 resource_id = %d, want %d（name→id 未解析）", disks[0].ResourceID, res[0].ID)
	}
	if disks[0].ResourceID == 0 {
		t.Fatal("明细 resource_id = 0：资源行没有拿到雪花 id")
	}

	// 第二轮重放同一桶（游标重置）必须仍然只有一行维度：幂等 upsert。
	f.setCursor(t, flushBaseTS-300)
	stats, err := f.svc.FlushOnce(context.Background())
	if err != nil {
		t.Fatalf("FlushOnce(2): %v", err)
	}
	if stats.BucketsWritten != 1 {
		t.Fatalf("第二轮 BucketsWritten = %d, want 1（游标被人为重置）", stats.BucketsWritten)
	}
	var resCount, diskCount int64
	// 计数必须**带 kind**：`Count(&entity.DeviceResource{})` 会把同一轮的 sensor 维度
	// 一起数进来（夹具每组样本都带一个传感器），那会让「跨轮幂等」这条断言量到别的东西。
	f.db.Model(&entity.DeviceResource{}).
		Where("device_id = ? AND kind = ?", flushDevID, agentmetrics.ResourceKindDisk).Count(&resCount)
	f.db.Model(&entity.DeviceMetricDisk{}).Count(&diskCount)
	if resCount != 1 {
		t.Fatalf("第二轮后 disk 资源行数 = %d, want 1（跨轮幂等）", resCount)
	}
	if diskCount != 1 {
		t.Fatalf("第二轮后 disk 明细行数 = %d, want 1（同桶重放是 UPSERT）", diskCount)
	}
}

// 4. 水位游标：第一轮后游标已前移，第二轮不得重复写。
func TestFlushCursorAdvancesAndSecondRoundWritesNothing(t *testing.T) {
	f := newFlushFixture(t, flushBaseTS+600, 10, nil)
	f.setCursor(t, flushBaseTS-300)
	f.seed(t, flushSample(flushBaseTS*1000, 20, "/", 20, 60))

	if _, err := f.svc.FlushOnce(context.Background()); err != nil {
		t.Fatalf("FlushOnce(1): %v", err)
	}
	cur, ok := f.cursor(t)
	if !ok {
		t.Fatal("cursor_5m 键不存在：游标必须落回 Redis")
	}
	if cur != flushBaseTS {
		t.Fatalf("cursor_5m = %d, want %d（已成功落库到含该桶）", cur, flushBaseTS)
	}

	stats, err := f.svc.FlushOnce(context.Background())
	if err != nil {
		t.Fatalf("FlushOnce(2): %v", err)
	}
	if stats.BucketsWritten != 0 {
		t.Fatalf("第二轮 BucketsWritten = %d, want 0（游标已前移，不得重复写）", stats.BucketsWritten)
	}
	var n int64
	f.db.Model(&entity.DeviceMetricWide{}).Count(&n)
	if n != 1 {
		t.Fatalf("宽表行数 = %d, want 1", n)
	}
}

// 5. 失败不推进游标：WriteBucket 失败 → 游标留在原处，下一轮重试同一桶。
func TestFlushWriteFailureKeepsCursorAndRetries(t *testing.T) {
	writer := &fakeBucketWriter{failOnCall: 1}
	f := newFlushFixture(t, flushBaseTS+600, 10, writer)
	f.setCursor(t, flushBaseTS-300)
	f.seed(t, flushSample(flushBaseTS*1000, 20, "/", 20, 60))

	stats, err := f.svc.FlushOnce(context.Background())
	if err == nil {
		t.Fatal("FlushOnce 必须把「本轮有桶失败」上抛为 error，实际返回 nil")
	}
	if stats.Errors == 0 {
		t.Fatalf("stats = %+v, Errors 必须 > 0", stats)
	}
	if stats.BucketsWritten != 0 {
		t.Fatalf("BucketsWritten = %d, want 0（写入失败）", stats.BucketsWritten)
	}
	cur, ok := f.cursor(t)
	if !ok {
		t.Fatal("cursor_5m 键丢失：失败轮不得删除游标")
	}
	if cur != flushBaseTS-300 {
		t.Fatalf("cursor_5m = %d, want %d（失败不推进游标）", cur, flushBaseTS-300)
	}

	// 下一轮（替身不再失败）必须重试同一个桶并成功。
	writer.failOnCall = 0
	stats2, err := f.svc.FlushOnce(context.Background())
	if err != nil {
		t.Fatalf("FlushOnce(2): %v", err)
	}
	if stats2.BucketsWritten != 1 {
		t.Fatalf("第二轮 BucketsWritten = %d, want 1（必须重试同一批桶）", stats2.BucketsWritten)
	}
	if len(writer.written) != 1 || writer.written[0].BucketTS != flushBaseTS {
		t.Fatalf("重试写入的桶 = %+v, want bucket_ts=%d", writer.written, flushBaseTS)
	}
	if cur2, _ := f.cursor(t); cur2 != flushBaseTS {
		t.Fatalf("cursor_5m = %d, want %d（成功后前移）", cur2, flushBaseTS)
	}
}

// 6a. 两段提交：flush **只**写 5m，绝不碰 1h（1h 归 Task 5 的 rollup）。
func TestFlushNeverWritesHourTable(t *testing.T) {
	f := newFlushFixture(t, flushBaseTS+600, 10, nil)
	f.setCursor(t, flushBaseTS-300)
	f.seed(t, flushSample(flushBaseTS*1000, 20, "/", 20, 60))

	if _, err := f.svc.FlushOnce(context.Background()); err != nil {
		t.Fatalf("FlushOnce: %v", err)
	}
	var n5m, n1h int64
	f.db.Table(entity.TableNameMetric5m).Count(&n5m)
	f.db.Table(entity.TableNameMetric1h).Count(&n1h)
	if n5m != 1 {
		t.Fatalf("%s 行数 = %d, want 1", entity.TableNameMetric5m, n5m)
	}
	if n1h != 0 {
		t.Fatalf("%s 行数 = %d, want 0（两段提交：flush 只写 5m）", entity.TableNameMetric1h, n1h)
	}
	if cur, _ := f.cursor(t); cur != flushBaseTS {
		t.Fatalf("cursor_5m = %d, want %d", cur, flushBaseTS)
	}
	// 1h 游标（Task 5 的领地）在本任务里**不得**被触碰。
	if f.rdb.Exists(context.Background(), "agent:device:1001:cursor_1h").Val() != 0 {
		t.Fatal("flush 不得写 cursor_1h（1h 由 Task 5 的 rollup 负责）")
	}
}

// 6b. 两段提交：1h 写失败不得影响 5m —— 用「1h 表不存在」制造真失败，
// 断言 5m 行仍在（两个事务，事务 B 失败不回滚事务 A）。
func TestFlushHourWriteFailureDoesNotRollbackFiveMinute(t *testing.T) {
	f := newFlushFixture(t, flushBaseTS+600, 10, nil)
	// 删掉 1h 表：WriteHour 必然报 no such table（真失败，不是替身）。
	if err := f.db.Migrator().DropTable(entity.TableNameMetric1h); err != nil {
		t.Fatal(err)
	}
	f.setCursor(t, flushBaseTS-300)
	f.seed(t, flushSample(flushBaseTS*1000, 20, "/", 20, 60))

	if err := f.repo.WriteHour(context.Background(), &entity.DeviceMetricWide{
		DeviceID: flushDevID, BucketTS: flushBaseTS,
	}); err == nil {
		t.Fatal("1h 表已删，WriteHour 必须报错（夹具前提不成立）")
	}

	var n int64
	f.db.Table(entity.TableNameMetric5m).Where("device_id = ?", flushDevID).Count(&n)
	if n != 0 {
		t.Fatalf("前置校验：5m 行数 = %d, want 0", n)
	}
	// 走 flush 落 5m；1h 仍不可写，但 5m 必须落进去。
	stats, err := f.svc.FlushOnce(context.Background())
	if err != nil || stats.BucketsWritten != 1 {
		t.Fatalf("FlushOnce = %+v, %v; want 1 written", stats, err)
	}
	f.db.Table(entity.TableNameMetric5m).Where("device_id = ?", flushDevID).Count(&n)
	if n != 1 {
		t.Fatalf("5m 行数 = %d, want 1（1h 不可用不得影响 5m）", n)
	}
}

// 7. Bootstrap 从 24h 前起步：水位键不存在 → 起始游标 = alignDown(now−24h, 300)。
func TestFlushBootstrapFrom24HoursAgo(t *testing.T) {
	f := newFlushFixture(t, flushBaseTS, 10, nil)
	// 设备必须先「活跃」才会被 Bootstrap 枚举到（枚举源 = Redis 活跃设备 SET，
	// 由 RawStore.Append 维护）——这也正是生产里的顺序：agent 先上报，flush 才看得到它。
	f.seed(t, flushSample(flushBaseTS*1000, 20, "/", 20, 60))

	want := alignDown300(flushBaseTS - flushBootstrapHours*3600)
	if _, ok := f.cursor(t); ok {
		t.Fatal("前置：cursor_5m 不得预先存在")
	}
	if err := f.svc.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	got, ok := f.cursor(t)
	if !ok {
		t.Fatal("Bootstrap 后 cursor_5m 必须存在")
	}
	if got != want {
		t.Fatalf("Bootstrap 后 cursor_5m = %d, want %d（now−24h 对齐到 5min 边界）", got, want)
	}
	if got < flushBaseTS-86400 {
		t.Fatalf("Bootstrap 游标 = %d 早于 now−24h，等于从更早（甚至 0）开始扫描", got)
	}
}

// 7b. FlushOnce 在没有游标时也必须走 Bootstrap 语义（不得从 0 开始扫全历史）。
func TestFlushWithoutCursorStartsFrom24HoursAgo(t *testing.T) {
	f := newFlushFixture(t, flushBaseTS, 10, nil)
	f.seed(t, flushSample(flushBaseTS*1000, 20, "/", 20, 60))

	stats, err := f.svc.FlushOnce(context.Background())
	if err != nil {
		t.Fatalf("FlushOnce: %v", err)
	}
	if stats.DevicesScanned != 1 {
		t.Fatalf("DevicesScanned = %d, want 1", stats.DevicesScanned)
	}
	// 已闭桶区间的**长度**由「轮初水位」决定，而水位本身（= 最后一个已成功落库的桶）
	// **不属于**本轮的区间：下界是 cursor+300。故首轮处理的是 [now−24h+300, now)，
	// 共 287 个桶（= 24h/300s − 1）；其中 1 个桶含种子样本故为 Written，
	// 其余 286 个是空桶 —— 288 那种算法会多算 cursor 自己那个桶。
	if stats.BucketsSkipped != 286 {
		t.Fatalf("BucketsSkipped = %d, want 286（=24h/5min−2，说明起点是 now−24h 而非 0）", stats.BucketsSkipped)
	}
	if stats.BucketsWritten != 0 {
		t.Fatalf("BucketsWritten = %d, want 0（种子样本在 now−36h，早掉出 24h 原始窗）", stats.BucketsWritten)
	}
	// 水位推进到本轮的最后一个桶。上界 = alignDown(now−CloseGrace, 300) 且是半开区间，
	// 故最后一个桶 = alignDown(now−20s, 300) − 300（CloseGrace = 10s×2）。
	lastBucket := alignDown300(flushBaseTS-20) - 300
	if cur, _ := f.cursor(t); cur != lastBucket {
		t.Fatalf("cursor_5m = %d, want %d（空桶照常推进游标）", cur, lastBucket)
	}
}

// 附：空桶照常推进游标（与「失败不推进」构成对照）。
func TestFlushEmptyBucketsStillAdvanceCursor(t *testing.T) {
	f := newFlushFixture(t, flushBaseTS+900, 10, nil)
	f.setCursor(t, flushBaseTS-300)
	// 设备要活跃（否则整轮枚举不到设备，skipped 恒为 0，断言等于测了个空）。
	f.seed(t, flushSample(flushBaseTS*1000-86400_000, 20, "/", 20, 60))
	// [base, base+600) 两个桶都是空的，闭桶上界 = base+600
	stats, err := f.svc.FlushOnce(context.Background())
	if err != nil {
		t.Fatalf("FlushOnce: %v", err)
	}
	if stats.BucketsSkipped != 2 || stats.BucketsWritten != 0 {
		t.Fatalf("stats = %+v, want skipped=2 written=0", stats)
	}
	if cur, _ := f.cursor(t); cur != flushBaseTS+300 {
		t.Fatalf("cursor_5m = %d, want %d（空桶照常推进）", cur, flushBaseTS+300)
	}
}

// 附：CloseGrace —— 还在收数据的当前桶不得被写坏。
//
// now = base+400 时：withoutGrace 会闭到 base+300（把 base+300 这个「当前桶」写掉）；
// CloseGrace = 2×10s 让上界退到 base，故 base+300 桶必须留在游标之外、不被写。
func TestFlushCloseGraceSkipsStillOpenBucket(t *testing.T) {
	f := newFlushFixture(t, flushBaseTS+400, 10, nil)
	f.setCursor(t, flushBaseTS-300)
	// 两个桶都有数据：base（已闭）与 base+300（还在收，落在宽限区内）
	f.seed(t,
		flushSample(flushBaseTS*1000, 20, "/", 20, 60),
		flushSample((flushBaseTS+300)*1000, 80, "/", 20, 60),
	)

	stats, err := f.svc.FlushOnce(context.Background())
	if err != nil {
		t.Fatalf("FlushOnce: %v", err)
	}
	if stats.BucketsWritten != 1 {
		t.Fatalf("BucketsWritten = %d, want 1（宽限区内的当前桶不得写）", stats.BucketsWritten)
	}
	var rows []entity.DeviceMetricWide
	f.db.Where("device_id = ?", flushDevID).Find(&rows)
	if len(rows) != 1 || rows[0].BucketTS != flushBaseTS {
		t.Fatalf("落库桶 = %v, want 只有 %d", rows, flushBaseTS)
	}
	if cur, _ := f.cursor(t); cur != flushBaseTS {
		t.Fatalf("cursor_5m = %d, want %d", cur, flushBaseTS)
	}
}

// 附：reportInterval 可热更（CloseGrace = reportInterval×2 由配置推导）。
func TestFlushCloseGraceFollowsReportInterval(t *testing.T) {
	// reportInterval = 300s → CloseGrace = 600s，上界退到 alignDown(now−600, 300)
	f := newFlushFixture(t, flushBaseTS+900, 300, nil)
	f.setCursor(t, flushBaseTS-300)
	f.seed(t, flushSample(flushBaseTS*1000, 20, "/", 20, 60))

	stats, err := f.svc.FlushOnce(context.Background())
	if err != nil {
		t.Fatalf("FlushOnce: %v", err)
	}
	// 上界 = alignDown(base+300, 300) = base+300 → 区间 [base, base+300) 仍包含该桶
	if stats.BucketsWritten != 1 {
		t.Fatalf("BucketsWritten = %d, want 1（CloseGrace 必须随 reportInterval 变化）", stats.BucketsWritten)
	}
}
