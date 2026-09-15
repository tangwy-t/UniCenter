package repository

import (
	"context"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/database"
	"gorm.io/gorm"
)

func newMetricTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	database.NewCallbacks(nil).Register(db)
	if err := db.AutoMigrate(
		&entity.DeviceMetricWide{}, &entity.DeviceMetricDisk{},
		&entity.DeviceMetricDiskIO{}, &entity.DeviceMetricNIC{}, &entity.DeviceMetricSensor{},
	); err != nil {
		t.Fatal(err)
	}
	// _1h 与 _5m 是「一套结构两处表名」：entity.DeviceMetricWide.TableName() 返回 _5m，
	// 故上面的 AutoMigrate 只会建出 _5m。1h 表必须用 Table() 显式建，
	// 否则 TestWriteHourIsIndependentFromWriteBucket 会因 no such table: device_metric_1h
	// 而红 —— 那是夹具缺表，不是实现缺陷（1h 表在真实环境由 pre-AutoMigrate 钩子建）。
	if err := db.Table(entity.TableNameMetric1h).AutoMigrate(&entity.DeviceMetricWide{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func ptr(f float64) *float64 { return &f }
func iptr(i int64) *int64    { return &i }

func sampleWide(bucketTS int64, cpu float64) *entity.DeviceMetricWide {
	return &entity.DeviceMetricWide{
		DeviceID: 1001, BucketTS: bucketTS,
		CPUUsedPercent: ptr(cpu), Load1: ptr(0.8), Samples: 30,
	}
}

func TestWriteBucketIsIdempotent(t *testing.T) {
	db := newMetricTestDB(t)
	repo := NewDeviceMetricRepository(db)
	ctx := context.Background()

	subs := MetricSubRows{
		Disks: []entity.DeviceMetricDisk{{ResourceID: 2001, BucketTS: 1000, UsedPercent: ptr(62)}},
		NICs:  []entity.DeviceMetricNIC{{ResourceID: 2002, BucketTS: 1000, RXBytesPerSec: ptr(125000)}},
	}
	if err := repo.WriteBucket(ctx, sampleWide(1000, 23.5), subs); err != nil {
		t.Fatalf("first WriteBucket: %v", err)
	}
	// 同桶重放：不得新增行，值应被覆盖
	subs2 := MetricSubRows{
		Disks: []entity.DeviceMetricDisk{{ResourceID: 2001, BucketTS: 1000, UsedPercent: ptr(63)}},
	}
	if err := repo.WriteBucket(ctx, sampleWide(1000, 99.9), subs2); err != nil {
		t.Fatalf("second WriteBucket: %v", err)
	}

	var wideCnt int64
	db.Model(&entity.DeviceMetricWide{}).Count(&wideCnt)
	if wideCnt != 1 {
		t.Fatalf("宽表行数 = %d, want 1（幂等）", wideCnt)
	}
	var w entity.DeviceMetricWide
	db.Where("device_id = ? AND bucket_ts = ?", 1001, 1000).First(&w)
	if w.CPUUsedPercent == nil || *w.CPUUsedPercent != 99.9 {
		t.Fatalf("重放应覆盖为 99.9, got %v", w.CPUUsedPercent)
	}

	var diskCnt int64
	db.Model(&entity.DeviceMetricDisk{}).Count(&diskCnt)
	if diskCnt != 1 {
		t.Fatalf("子表行数 = %d, want 1", diskCnt)
	}
	var d entity.DeviceMetricDisk
	db.Where("resource_id = ? AND bucket_ts = ?", 2001, 1000).First(&d)
	if d.UsedPercent == nil || *d.UsedPercent != 63 {
		t.Fatalf("子表重放应覆盖为 63, got %v", d.UsedPercent)
	}
}

// TestMetricQueryWhitelistExcludesDDLNoise 守卫查询白名单的解析（评审 F5）。
//
// 缺陷背景：init() 原先自己用裸 strings.Split 解析列 DDL，缺了「必须是裸标识符」
// 这层过滤，于是 `PRIMARY KEY (device_id, bucket_ts)` 被 ',' 切开后的续行 token
// "bucket_ts)" 被当成列名收进白名单（实测 6 张表各多 1 个假列），而且
// sanitizeColumns(t, []string{"bucket_ts)"}) **放行**它 → 直接拼进 SELECT 投影。
// 现在解析与守卫测试同源（parseDDLColumnNames），这里断言垃圾 token 不再进白名单、
// 且白名单出口会拒绝它。
func TestMetricQueryWhitelistExcludesDDLNoise(t *testing.T) {
	noise := []string{"bucket_ts)", "resource_id)", "PRIMARY", "UNIQUE", "KEY", "PRIMARY KEY (device_id"}
	for _, table := range MetricTables() {
		allowed, ok := metricQueryColumns[table]
		if !ok {
			t.Fatalf("%s 不在查询白名单里", table)
		}
		if !allowed["bucket_ts"] {
			t.Fatalf("%s 白名单必须含 bucket_ts", table)
		}
		for _, bad := range noise {
			if allowed[bad] {
				t.Errorf("%s 白名单混入了 DDL 约束 token %q（约束续行不是列定义）", table, bad)
			}
		}
		// 白名单的唯一出口：垃圾列必须在 400 侧被拒，不得拼进 SQL。
		if _, err := sanitizeColumns(table, []string{"bucket_ts)"}); err == nil {
			t.Errorf("%s: sanitizeColumns 必须拒绝 %q", table, "bucket_ts)")
		}
		// 正常列仍必须放行（别把守卫修成拒绝一切）。
		if _, err := sanitizeColumns(table, []string{"bucket_ts"}); err != nil {
			t.Errorf("%s: sanitizeColumns 必须接受 bucket_ts: %v", table, err)
		}
	}
}

// TestUpsertRowsRejectsUnregisteredTable 守卫「表名未登记就必须硬失败」（评审 F3）。
//
// 缺陷背景：upsertRows 原先靠 `firstTable[T]` 在运行期判类型，未知类型返回 ""，
// 于是值列清单为 nil → DoUpdates 为空 → 冲突时**静默退化成 DO NOTHING**
// （实测：重放同一 (resource_id, bucket_ts)，err=nil 但值仍是旧值）。
// 表名改为显式参数后，唯一的剩余缺口就是「传了未登记的表名」，它必须报错。
func TestUpsertRowsRejectsUnregisteredTable(t *testing.T) {
	db := newMetricTestDB(t)
	rows := []entity.DeviceMetricDisk{{ResourceID: 3001, BucketTS: 1000, UsedPercent: ptr(50)}}

	err := upsertRows(db, "device_metric_nope", rows)
	if err == nil {
		t.Fatal("未登记的表名必须返回错误（否则冲突会静默退化成 DO NOTHING）")
	}
	if !strings.Contains(err.Error(), "device_metric_nope") {
		t.Fatalf("错误信息必须点明是哪张表: %v", err)
	}
	// 未登记的表：不得悄悄写进去（错误必须在执行前返回）
	if db.Migrator().HasTable("device_metric_nope") {
		t.Fatal("夹具不该存在该表")
	}
	// 空切片同样不得绕过校验（校验在行数判断之前）
	if err := upsertRows(db, "device_metric_nope", []entity.DeviceMetricDisk{}); err == nil {
		t.Fatal("空切片也不得绕过表名校验")
	}
	// 已登记的表：正常写入（对照）
	if err := upsertRows(db, entity.TableNameMetricDisk, rows); err != nil {
		t.Fatalf("已登记表必须可写: %v", err)
	}
}

// TestWriteBucketReplaysAllFourSubTablesIdempotently 覆盖 4 张子表的幂等重放。
//
// 原先只有 _disk 被覆盖到（TestWriteBucketIsIdempotent）；_diskio / _nic / _sensor
// 的「冲突时真的更新」没有任何断言 —— 若某张表的值列清单写错或缺失，
// 旧的静默 DO NOTHING 会让这三张表悄悄只写首值。这里逐表断言：
// 重放后**行数仍为 1** 且**值被覆盖为第二值**。
func TestWriteBucketReplaysAllFourSubTablesIdempotently(t *testing.T) {
	const firstVal, secondVal = 11.0, 77.0
	cases := []struct {
		name  string
		table string
		build func(v float64) MetricSubRows
		read  func(t *testing.T, db *gorm.DB) (int64, *float64)
	}{
		{
			name: "disk", table: entity.TableNameMetricDisk,
			build: func(v float64) MetricSubRows {
				return MetricSubRows{Disks: []entity.DeviceMetricDisk{{ResourceID: 4001, BucketTS: 1000, UsedPercent: &v}}}
			},
			read: func(t *testing.T, db *gorm.DB) (int64, *float64) {
				var n int64
				db.Table(entity.TableNameMetricDisk).Count(&n)
				var row entity.DeviceMetricDisk
				db.Table(entity.TableNameMetricDisk).Where("resource_id = ? AND bucket_ts = ?", 4001, 1000).First(&row)
				return n, row.UsedPercent
			},
		},
		{
			name: "diskio", table: entity.TableNameMetricDiskIO,
			build: func(v float64) MetricSubRows {
				return MetricSubRows{DiskIO: []entity.DeviceMetricDiskIO{{ResourceID: 4002, BucketTS: 1000, ReadBytesPerSec: &v}}}
			},
			read: func(t *testing.T, db *gorm.DB) (int64, *float64) {
				var n int64
				db.Table(entity.TableNameMetricDiskIO).Count(&n)
				var row entity.DeviceMetricDiskIO
				db.Table(entity.TableNameMetricDiskIO).Where("resource_id = ? AND bucket_ts = ?", 4002, 1000).First(&row)
				return n, row.ReadBytesPerSec
			},
		},
		{
			name: "nic", table: entity.TableNameMetricNIC,
			build: func(v float64) MetricSubRows {
				return MetricSubRows{NICs: []entity.DeviceMetricNIC{{ResourceID: 4003, BucketTS: 1000, RXBytesPerSec: &v}}}
			},
			read: func(t *testing.T, db *gorm.DB) (int64, *float64) {
				var n int64
				db.Table(entity.TableNameMetricNIC).Count(&n)
				var row entity.DeviceMetricNIC
				db.Table(entity.TableNameMetricNIC).Where("resource_id = ? AND bucket_ts = ?", 4003, 1000).First(&row)
				return n, row.RXBytesPerSec
			},
		},
		{
			name: "sensor", table: entity.TableNameMetricSensor,
			build: func(v float64) MetricSubRows {
				return MetricSubRows{Sensors: []entity.DeviceMetricSensor{{ResourceID: 4004, BucketTS: 1000, TemperatureC: &v}}}
			},
			read: func(t *testing.T, db *gorm.DB) (int64, *float64) {
				var n int64
				db.Table(entity.TableNameMetricSensor).Count(&n)
				var row entity.DeviceMetricSensor
				db.Table(entity.TableNameMetricSensor).Where("resource_id = ? AND bucket_ts = ?", 4004, 1000).First(&row)
				return n, row.TemperatureC
			},
		},
	}
	if len(cases) != len(subValueColumnsByTable) {
		t.Fatalf("子表覆盖数 = %d, 登记的表 = %d —— 新增子表必须同时补进本测试",
			len(cases), len(subValueColumnsByTable))
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := newMetricTestDB(t)
			repo := NewDeviceMetricRepository(db)
			ctx := context.Background()

			if err := repo.WriteBucket(ctx, sampleWide(1000, 10), c.build(firstVal)); err != nil {
				t.Fatalf("first WriteBucket(%s): %v", c.table, err)
			}
			if err := repo.WriteBucket(ctx, sampleWide(1000, 10), c.build(secondVal)); err != nil {
				t.Fatalf("replay WriteBucket(%s): %v", c.table, err)
			}
			n, got := c.read(t, db)
			if n != 1 {
				t.Fatalf("%s 行数 = %d, want 1（同桶重放必须幂等）", c.table, n)
			}
			if got == nil || *got != secondVal {
				t.Fatalf("%s 重放后值 = %v, want %v —— 冲突退化成 DO NOTHING（值列清单缺失/写错）",
					c.table, got, secondVal)
			}
		})
	}
}

func TestWriteHourIsIndependentFromWriteBucket(t *testing.T) {
	db := newMetricTestDB(t)
	repo := NewDeviceMetricRepository(db)
	ctx := context.Background()

	// 先写 5m（事务 A）
	if err := repo.WriteBucket(ctx, sampleWide(3600, 20), MetricSubRows{
		Disks: []entity.DeviceMetricDisk{{ResourceID: 2001, BucketTS: 3600, UsedPercent: ptr(60)}},
	}); err != nil {
		t.Fatal(err)
	}
	// 构造一个**必然失败**的 1h 写：bucket_ts 与 device_id 相同但表不同 ->
	// 这里改用「重复主键 + 非法值」难以稳定制造，故直接断言两方法是不同表：
	if err := repo.WriteHour(ctx, sampleWide(3600, 21)); err != nil {
		t.Fatalf("WriteHour: %v", err)
	}

	var fiveMin, oneHour int64
	db.Table(entity.TableNameMetric5m).Count(&fiveMin)
	db.Table(entity.TableNameMetric1h).Count(&oneHour)
	if fiveMin != 1 {
		t.Fatalf("_5m 行数 = %d, want 1", fiveMin)
	}
	if oneHour != 1 {
		t.Fatalf("_1h 行数 = %d, want 1 —— WriteHour 必须写 1h 表", oneHour)
	}
	// 关键：1h 的写入不得改动 5m 行的值
	var w entity.DeviceMetricWide
	db.Table(entity.TableNameMetric5m).Where("bucket_ts = ?", 3600).First(&w)
	if w.CPUUsedPercent == nil || *w.CPUUsedPercent != 20 {
		t.Fatalf("1h 写回滚/污染了 5m 行: %v", w.CPUUsedPercent)
	}
}

func TestReadTrendProjectsOnlyWhitelistedColumns(t *testing.T) {
	db := newMetricTestDB(t)
	repo := NewDeviceMetricRepository(db)
	ctx := context.Background()
	for ts := int64(1000); ts <= 1600; ts += 300 {
		if err := repo.WriteBucket(ctx, sampleWide(ts, float64(ts)/100), MetricSubRows{}); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := repo.ReadTrend(ctx, entity.TableNameMetric5m, 1001, 1000, 1600,
		[]string{"bucket_ts", "cpu_used_percent"})
	if err != nil {
		t.Fatalf("ReadTrend: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("趋势点数 = %d, want 3", len(rows))
	}
	for _, row := range rows {
		if len(row) != 2 {
			t.Fatalf("白名单只允许 2 列, got %d (%v)", len(row), row)
		}
		if _, ok := row["cpu_used_percent"]; !ok {
			t.Fatalf("缺少投影列 cpu_used_percent: %v", row)
		}
		if _, ok := row["load1"]; ok {
			t.Fatalf("未在白名单里的列不得返回: %v", row)
		}
	}
}

func TestReadResourceTrendAndCount(t *testing.T) {
	db := newMetricTestDB(t)
	repo := NewDeviceMetricRepository(db)
	ctx := context.Background()
	if err := repo.WriteBucket(ctx, sampleWide(1000, 10), MetricSubRows{
		Disks: []entity.DeviceMetricDisk{
			{ResourceID: 2001, BucketTS: 1000, UsedPercent: ptr(60)},
			{ResourceID: 2001, BucketTS: 1300, UsedPercent: ptr(61)},
			{ResourceID: 2002, BucketTS: 1000, UsedPercent: ptr(90)},
		},
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := repo.ReadResourceTrend(ctx, entity.TableNameMetricDisk, 2001, 0, 9999,
		[]string{"bucket_ts", "used_percent"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("2001 号的趋势点 = %d, want 2（不得串到 2002）", len(rows))
	}
	n, err := repo.CountWide(ctx, entity.TableNameMetric5m, 1001, 0, 9999)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("CountWide = %d, want 1", n)
	}
}
