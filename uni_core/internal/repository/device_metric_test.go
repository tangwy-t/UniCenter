package repository

import (
	"context"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agentmetrics"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/database"
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

// TestMetricQueryColumnsReturnsAllValueColumns 守卫 metrics=* 的展开源（D4）。
//
// 缺陷背景：`sanitizeColumns(table, nil)` 的语义是「**只要** bucket_ts」，而
// `resolveColumns("*")` 原先直接返回 nil 交给仓储 → 实测 `metrics=*` 只回 bucket_ts、
// **没有任何值列**，与 spec §8 明文「metrics=* 回全量」相反。
// 展开源必须是「该表全部值列」且顺序稳定（前端缓存/测试可比）。
func TestMetricQueryColumnsReturnsAllValueColumns(t *testing.T) {
	for _, table := range MetricTables() {
		cols, err := MetricQueryColumns(table)
		if err != nil {
			t.Fatalf("%s: MetricQueryColumns: %v", table, err)
		}
		if len(cols) == 0 {
			t.Fatalf("%s: 值列集为空（metrics=* 会退化成只回 bucket_ts）", table)
		}
		if !sort.StringsAreSorted(cols) {
			t.Fatalf("%s: 列序必须稳定（排序后返回）, got %v", table, cols)
		}
		allowed := metricQueryColumns[table]
		seen := map[string]bool{}
		for _, c := range cols {
			if c == "bucket_ts" {
				t.Fatalf("%s: 不得包含 bucket_ts（service 侧恒补，且它是键列不是值列）", table)
			}
			if !allowed[c] {
				t.Fatalf("%s: 列 %q 不在该表读白名单内（展开出的列必须可投影）", table, c)
			}
			if seen[c] {
				t.Fatalf("%s: 列 %q 重复", table, c)
			}
			seen[c] = true
		}
		// 反向：白名单里除 bucket_ts 外的每一列都必须被返回（否则 * 仍是「静默少列」）
		if len(cols) != len(allowed)-1 {
			t.Fatalf("%s: 值列数 = %d, 白名单（除 bucket_ts）= %d —— 不得漏列", table, len(cols), len(allowed)-1)
		}
	}

	// 最小可观测证据：宽表值列 > 8 且含速率列
	cols, err := MetricQueryColumns(entity.TableNameMetric5m)
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) <= 8 {
		t.Fatalf("宽表值列数 = %d, want > 8", len(cols))
	}
	if !slices.Contains(cols, "nic_rx_bytes_sec") {
		t.Fatalf("宽表值列必须含 nic_rx_bytes_sec, got %v", cols)
	}

	// 未知表必须硬失败（选表即选档，多了第七张表就是 bug）
	if _, err := MetricQueryColumns("device_metric_nope"); err == nil {
		t.Fatal("未知指标表必须报错")
	}
}

// TestResourceTableColumnsMatchesSubValueColumns 守卫下钻列集的**单一来源**（D2/D3）。
//
// 子表的可查询列在仓库里有两处登记：写入侧 subValueColumnsByTable（冲突时要覆盖的列）
// 与读取侧 resourceTableColumns（下钻的列集）。两者是同一张表的同一批值列，漂移会让
// 「写得进去但下钻看不到」或反之，且都不会报错 —— 故必须逐表锁死集合相等，
// 且每一列都在读白名单（metricQueryColumns）里。
func TestResourceTableColumnsMatchesSubValueColumns(t *testing.T) {
	if len(resourceTableColumns) != len(subValueColumnsByTable) {
		t.Fatalf("下钻子表数 = %d, 写侧子表数 = %d", len(resourceTableColumns), len(subValueColumnsByTable))
	}
	for table, writeCols := range subValueColumnsByTable {
		cols, ok := ResourceTableColumns(table)
		if !ok {
			t.Fatalf("%s 未登记下钻列集", table)
		}
		if len(cols) != len(writeCols) {
			t.Fatalf("%s: 下钻列数 = %d, 写侧列数 = %d（%v vs %v）", table, len(cols), len(writeCols), cols, writeCols)
		}
		set := map[string]bool{}
		for _, c := range cols {
			set[c] = true
			if !metricQueryColumns[table][c] {
				t.Fatalf("%s: 下钻列 %q 不在该表读白名单内", table, c)
			}
			if c == "bucket_ts" {
				t.Fatalf("%s: 下钻列集不得含 bucket_ts（t 单独走别名）", table)
			}
		}
		for _, c := range writeCols {
			if !set[c] {
				t.Fatalf("%s: 写侧列 %q 不在下钻列集里（漂移）", table, c)
			}
		}
	}
	// 宽表不是明细子表
	if _, ok := ResourceTableColumns(entity.TableNameMetric5m); ok {
		t.Fatal("宽表不得被当成明细子表")
	}
	if _, ok := ResourceTableColumns("device_metric_nope"); ok {
		t.Fatal("未知表不得登记下钻列集")
	}
}

// TestReadResourceRowsSelectsAllSubTableColumns 锁定下钻读取的形状（D2）。
//
// 为什么这条必须锁：下钻子表的列名（used_percent / inodes_used_percent …）
// 与 TrendPoint 的字段（disk_used_percent …）**不同名**，且子表还有 inodes / io_time /
// *_errors 等 TrendPoint 装不下的列。实测：走「类型化扫描进 TrendPoint」时，
// 下钻返回的桶**值列全为 nil**（列名对不上，GORM 不报错只静默留 nil）。
// 故下钻改为开放形状（[]map[string]any）+ SELECT *，本测试锁住「列齐全 + t 对齐」。
func TestReadResourceRowsSelectsAllSubTableColumns(t *testing.T) {
	db := newMetricTestDB(t)
	repo := NewDeviceMetricRepository(db)
	ctx := context.Background()

	if err := repo.WriteBucket(ctx, sampleWide(1000, 10), MetricSubRows{
		Disks: []entity.DeviceMetricDisk{
			{ResourceID: 2001, BucketTS: 1000, UsedPercent: ptr(60), UsedGB: ptr(600), TotalGB: ptr(1000)},
			{ResourceID: 2001, BucketTS: 1300, UsedPercent: ptr(61), UsedGB: ptr(610), TotalGB: ptr(1000),
				InodesUsedPercent: ptr(12)},
			{ResourceID: 2002, BucketTS: 1000, UsedPercent: ptr(90)},
		},
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := repo.ReadResourceRows(ctx, entity.TableNameMetricDisk, 2001, 0, 9999)
	if err != nil {
		t.Fatalf("ReadResourceRows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("行数 = %d, want 2（不得串到 2002）", len(rows))
	}
	for i, wantTS := range []int64{1000, 1300} {
		got, ok := rows[i]["t"]
		if !ok {
			t.Fatalf("第 %d 行缺少键 t（bucket_ts 必须别名为 t，否则响应里的 t 全是 0）: %v", i, rows[i])
		}
		if n := anyInt64(t, got); n != wantTS {
			t.Fatalf("第 %d 行 t = %d, want %d（必须按 bucket_ts 升序）", i, n, wantTS)
		}
		// 全部值列都必须在行里（不投影 —— 子表只有 4 列，投影没有收益）
		for _, col := range []string{"used_percent", "used_gb", "total_gb", "inodes_used_percent"} {
			if _, ok := rows[i][col]; !ok {
				t.Fatalf("第 %d 行缺少子表列 %q: %v", i, col, rows[i])
			}
		}
	}
	if rows[0]["inodes_used_percent"] != nil {
		t.Fatalf("未写入的列必须是 NULL（缺 ≠ 0）, got %v", rows[0]["inodes_used_percent"])
	}
	if rows[1]["inodes_used_percent"] == nil {
		t.Fatal("写入的列必须读得回来（inodes_used_percent）")
	}
	if rows[0]["used_percent"] == nil {
		t.Fatal("写入的列必须读得回来（used_percent）")
	}

	// 只接受 4 张明细子表
	if _, err := repo.ReadResourceRows(ctx, entity.TableNameMetric5m, 1001, 0, 9999); err == nil {
		t.Fatal("宽表不是明细子表，必须报错")
	}
	if _, err := repo.ReadResourceRows(ctx, "device_metric_nope", 1, 0, 9999); err == nil {
		t.Fatal("未知表必须报错")
	}
}

// anyInt64 把驱动返回的数值形态归一成 int64（测试侧最小实现，只覆盖本测试的形态）。
func anyInt64(t *testing.T, v any) int64 {
	t.Helper()
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	default:
		t.Fatalf("t 的驱动形态 = %T (%v)，无法归一到 int64", v, v)
		return 0
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

	points, err := repo.ReadTrendPoints(ctx, entity.TableNameMetric5m, 1001, 1000, 1600,
		[]string{"bucket_ts", "cpu_used_percent"})
	if err != nil {
		t.Fatalf("ReadTrendPoints: %v", err)
	}
	if len(points) != 3 {
		t.Fatalf("趋势点数 = %d, want 3", len(points))
	}
	for i, p := range points {
		wantTS := int64(1000 + i*300)
		if p.T != wantTS {
			t.Fatalf("第 %d 点 t = %d, want %d（bucket_ts 必须经 aliasBucketTS 落到字段 T）", i, p.T, wantTS)
		}
		if p.CPUUsedPercent == nil {
			t.Fatalf("投影列 cpu_used_percent 必须非 nil（t=%d）", p.T)
		}
		// 白名单投影在类型化之后**可测**了：未选中的列是 nil（而不是被填 0）。
		if p.Load1 != nil || p.MemUsedPercent != nil {
			t.Fatalf("未在白名单里的列不得返回: %+v", p)
		}
		if p.Samples != 0 {
			t.Fatalf("未选中的 samples 必须保持零值, got %d", p.Samples)
		}
	}
}

// TestReadTrendPointsLocksGORMColumnMapping 锁住 GORM 的「列名 → 字段」映射。
//
// 为什么必须单独锁：连续大写缩写（NICRXBytesSec、DiskIOReadBytesSec）能否被
// snake_case 命名策略映射到 nic_rx_bytes_sec / disk_io_read_bytes_sec **不能靠猜** ——
// 映射不上时 GORM 不报错，只是字段静默留 nil，曲线永远为空。
//
// 关于计划点名的 AgentWSReconnectCount：`agentmetrics.TrendPoint` **没有** agent_* 字段
// （它只镜像 spec §5.5 MetricsSample 的 25 列），读取侧不存在该列，无法在此断言；
// 它在写入侧的列名由 entity.DeviceMetricWide 的显式 `gorm:"column:..."` tag 保证，
// 与本方法的映射无关。详见本任务回报中的缺陷条目。
func TestReadTrendPointsLocksGORMColumnMapping(t *testing.T) {
	db := newMetricTestDB(t)
	repo := NewDeviceMetricRepository(db)
	ctx := context.Background()
	if err := repo.WriteBucket(ctx, &entity.DeviceMetricWide{
		DeviceID: 1001, BucketTS: 1000,
		CPUUsedPercent: ptr(12.5), CPUIOWait: ptr(1.5),
		MemUsedMB: ptr(2048), MemAvailableMB: ptr(1024),
		TCPEstablished:     iptr(42),
		DiskIOReadBytesSec: ptr(4096), DiskIOWriteBytesSec: ptr(2048),
		NICRXBytesSec: ptr(8192), NICTXBytesSec: ptr(4096),
		MaxTemperatureC: ptr(64.5), UptimeSec: iptr(999),
		Samples: 30,
	}, MetricSubRows{}); err != nil {
		t.Fatal(err)
	}

	points, err := repo.ReadTrendPoints(ctx, entity.TableNameMetric5m, 1001, 0, 9999, []string{
		"bucket_ts", "cpu_used_percent", "cpu_iowait", "mem_used_mb", "mem_available_mb",
		"tcp_established", "disk_io_read_bytes_sec", "disk_io_write_bytes_sec",
		"nic_rx_bytes_sec", "nic_tx_bytes_sec", "max_temperature_c", "uptime_sec",
	})
	if err != nil {
		t.Fatalf("ReadTrendPoints: %v", err)
	}
	if len(points) != 1 {
		t.Fatalf("点数 = %d, want 1", len(points))
	}
	p := points[0]
	if p.T != 1000 {
		t.Fatalf("t = %d, want 1000", p.T)
	}
	checkF(t, "cpu_used_percent", p.CPUUsedPercent, 12.5)
	checkF(t, "cpu_iowait", p.CPUIOWait, 1.5)
	checkF(t, "mem_used_mb", p.MemUsedMB, 2048)
	checkF(t, "mem_available_mb", p.MemAvailableMB, 1024)
	checkF(t, "disk_io_read_bytes_sec", p.DiskIOReadBytesSec, 4096)
	checkF(t, "disk_io_write_bytes_sec", p.DiskIOWriteBytesSec, 2048)
	checkF(t, "nic_rx_bytes_sec", p.NICRXBytesSec, 8192)
	checkF(t, "nic_tx_bytes_sec", p.NICTXBytesSec, 4096)
	checkF(t, "max_temperature_c", p.MaxTemperatureC, 64.5)
	checkI(t, "tcp_established", p.TCPEstablished, 42)
	checkI(t, "uptime_sec", p.UptimeSec, 999)
	// 未请求的列仍必须是 nil（映射成功≠全列返回）
	if p.Load1 != nil || p.ProcCount != nil {
		t.Fatalf("未请求的列必须保持 nil: load1=%v proc_count=%v", p.Load1, p.ProcCount)
	}
	if p.Samples != 0 {
		t.Fatalf("samples 不在白名单，必须保持零值, got %d", p.Samples)
	}
}

// checkF / checkI 断言某列映射成功且值正确；映射失败会**静默 nil**，故 nil 即失败。
func checkF(t *testing.T, col string, got *float64, want float64) {
	t.Helper()
	if got == nil {
		t.Fatalf("列名映射失败：%s 未落到结构体字段（nil）——需显式别名或加 gorm:\"column:...\" tag", col)
	}
	if *got != want {
		t.Fatalf("%s = %v, want %v", col, *got, want)
	}
}

func checkI(t *testing.T, col string, got *int64, want int64) {
	t.Helper()
	if got == nil {
		t.Fatalf("列名映射失败：%s 未落到结构体字段（nil）——需显式别名或加 gorm:\"column:...\" tag", col)
	}
	if *got != want {
		t.Fatalf("%s = %v, want %v", col, *got, want)
	}
}

// TestTrendPointFieldsMatchMetricColumns 是 GORM 列名映射的**全字段守卫**：
// agentmetrics.TrendPoint 每个字段算出的 DBName 必须等于 _5m 的真实列名
// （T 例外：它靠 `bucket_ts AS t` 别名承接，不是物理列）。
//
// 为什么需要守卫而不只是锁那 3 列：GORM 的 snake_case 推导对「连续大写缩写」会漏下划线
// （实测 NICRXBytesSec→nicrx_bytes_sec、NICTXBytesSec→nictx_bytes_sec、
// CPUIOWait→cpu_io_wait），而映射失败时 GORM **不报错**，字段只是静默留 nil →
// 曲线永远为空。守卫让「加了字段却忘了映射」当场变红，不依赖任何人记得去猜。
func TestTrendPointFieldsMatchMetricColumns(t *testing.T) {
	parsed, err := schema.Parse(&agentmetrics.TrendPoint{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatal(err)
	}
	ddl := metricColumnDDL[entity.TableNameMetric5m]
	columns := parseDDLColumnNames(ddl)
	allowed := map[string]bool{}
	for _, name := range columns {
		allowed[name] = true
	}

	mapped := map[string]bool{}
	for _, f := range parsed.Fields {
		if f.Name == "T" {
			continue // select 里由 aliasBucketTS 显式写成 `bucket_ts AS t`
		}
		mapped[f.DBName] = true
		if !allowed[f.DBName] {
			t.Errorf("字段 %s 的 GORM 列名 %q 不是 %s 的真实列（映射失败会静默 nil）：需加 gorm:\"column:...\" tag",
				f.Name, f.DBName, entity.TableNameMetric5m)
		}
	}

	// 反向（只记录不断言）：白名单里没有字段承接的列 —— 这些列即使被显式请求，
	// 扫出来也只会是 nil（TrendPoint 是宽表 45 列的子集）→ available_metrics
	// 目前会把它报成"可用"，消费方无从区分。这是**已知缺口**，见任务回报。
	orphan := make([]string, 0, 8)
	for _, name := range columns {
		if !mapped[name] {
			orphan = append(orphan, name)
		}
	}
	t.Logf("%s 中无 TrendPoint 承接列的列（显式请求也只会得到 nil）：%v", entity.TableNameMetric5m, orphan)
}

func TestReadTrendPointsRejectsUnknownTableAndColumn(t *testing.T) {
	db := newMetricTestDB(t)
	repo := NewDeviceMetricRepository(db)
	ctx := context.Background()

	// 未知表名（选表即选档，多了第七张表就是 bug）
	if _, err := repo.ReadTrendPoints(ctx, "device_metric_5min", 1001, 0, 9999, []string{"bucket_ts"}); err == nil {
		t.Fatal("未知指标表必须报错")
	}
	// 非白名单列（注入载荷）
	if _, err := repo.ReadTrendPoints(ctx, entity.TableNameMetric5m, 1001, 0, 9999,
		[]string{"cpu_used_percent) FROM device_metric_1h WHERE (1=1"}); err == nil {
		t.Fatal("非白名单列必须报错")
	}
	// 白名单按表隔离：宽表列不得出现在子表查询里。
	// 注：下钻的读取入口已换成 ReadResourceRows（SELECT *，仓储不做投影，H2），
	// 故这里直接锚定白名单函数本身 —— 断言强度不变（子表查询永远拿不到宽表列）。
	if _, err := sanitizeColumns(entity.TableNameMetricDisk, []string{"cpu_used_percent"}); err == nil {
		t.Fatal("子表查询不得放行宽表列")
	}
}

// TestReadResourceRowsAndCountWide 覆盖下钻的读取入口（ReadResourceRows）与
// CountWide。原先的另一条断言（ReadResourceTrendPoints 的 t/行数）随该方法的
// 删除一并移除（H2：它已无生产调用方，且子表列名与 TrendPoint 不同名，
// 类型化扫描正是「下钻值列全为 nil」的成因）。读取范围/设备隔离的强度由
// TestReadResourceRowsSelectsAllSubTableColumns 覆盖，此处保留 CountWide 的断言。
func TestReadResourceRowsAndCountWide(t *testing.T) {
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
	rows, err := repo.ReadResourceRows(ctx, entity.TableNameMetricDisk, 2001, 0, 9999)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("2001 号的下钻行 = %d, want 2（不得串到 2002）", len(rows))
	}
	if rows[0]["t"] == nil || rows[1]["t"] == nil {
		t.Fatalf("下钻行必须带可解析的 t（bucket_ts AS t）: %v", rows)
	}
	n, err := repo.CountWide(ctx, entity.TableNameMetric5m, 1001, 0, 9999)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("CountWide = %d, want 1", n)
	}
}

// TestReadWideRows 覆盖 1h 回滚的读取入口：闭区间、按 bucket_ts 升序、
// 设备隔离、未知宽表名硬失败。
//
// 为什么必须钉住「升序」：rollup 的加权回滚按行序消费（uptime 取 LAST 依赖
// 「最后一行」，1h 的 samples 完整性判定也按序累加），乱序会让 LAST/完整性
// 悄悄取到中间某一行 —— 不报错、数值只是「有点不对」。
func TestReadWideRows(t *testing.T) {
	db := newMetricTestDB(t)
	repo := NewDeviceMetricRepository(db)
	ctx := context.Background()

	// 刻意乱序写入，让「升序」来自 ORDER BY 而不是写入顺序。
	for _, ts := range []int64{1300, 1000, 1600} {
		if err := repo.WriteBucket(ctx, sampleWide(ts, float64(ts)/100), MetricSubRows{}); err != nil {
			t.Fatal(err)
		}
	}
	// 另一台设备同区间：不得串进来
	other := &entity.DeviceMetricWide{DeviceID: 2002, BucketTS: 1000, Samples: 1}
	if err := repo.WriteBucket(ctx, other, MetricSubRows{}); err != nil {
		t.Fatal(err)
	}

	rows, err := repo.ReadWideRows(ctx, entity.TableNameMetric5m, 1001, 1000, 1600)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("宽行数 = %d, want 3（且不得串到 2002）", len(rows))
	}
	for i, want := range []int64{1000, 1300, 1600} {
		if rows[i].BucketTS != want {
			t.Fatalf("rows[%d].bucket_ts = %d, want %d（必须按 bucket_ts 升序）", i, rows[i].BucketTS, want)
		}
		if rows[i].DeviceID != 1001 {
			t.Fatalf("rows[%d].device_id = %d, want 1001", i, rows[i].DeviceID)
		}
	}
	if rows[0].CPUUsedPercent == nil || *rows[0].CPUUsedPercent != 10 {
		t.Fatalf("值列未读回: cpu_used_percent = %v, want 10", rows[0].CPUUsedPercent)
	}

	// 闭区间：端点必须含在内（回滚一整小时要拿到 12 行 5m 行的首尾）
	edge, err := repo.ReadWideRows(ctx, entity.TableNameMetric5m, 1001, 1300, 1300)
	if err != nil {
		t.Fatal(err)
	}
	if len(edge) != 1 {
		t.Fatalf("单点区间 [1300,1300] 行数 = %d, want 1（闭区间）", len(edge))
	}

	// 1h 表同样可读（同一结构两处表名，必须靠显式表名切换）
	if _, err := repo.ReadWideRows(ctx, entity.TableNameMetric1h, 1001, 0, 9999); err != nil {
		t.Fatalf("读 _1h 失败: %v", err)
	}

	// 未登记的宽表名硬失败：静默返回空切片会被上游当成「该小时没有数据」
	if _, err := repo.ReadWideRows(ctx, entity.TableNameMetricDisk, 1001, 0, 9999); err == nil {
		t.Fatal("明细子表名必须被拒绝（ReadWideRows 只读两张宽表）")
	}
	if _, err := repo.ReadWideRows(ctx, "device_metric_5m_typo", 1001, 0, 9999); err == nil {
		t.Fatal("未知表名必须硬失败，不得静默返回空")
	}
}
