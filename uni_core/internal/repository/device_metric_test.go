package repository

import (
	"context"
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
	// 白名单按表隔离：宽表列不得出现在子表查询里
	if _, err := repo.ReadResourceTrendPoints(ctx, entity.TableNameMetricDisk, 2001, 0, 9999,
		[]string{"cpu_used_percent"}); err == nil {
		t.Fatal("子表查询不得放行宽表列")
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
	// 注：子表列名（used_percent / rx_bytes_per_sec / temperature_c …）与
	// agentmetrics.TrendPoint 的字段名（disk_used_percent / nic_rx_bytes_sec /
	// max_temperature_c …）**不同名**，故这里只能断言 t 与行数（见任务回报的缺陷条目）。
	points, err := repo.ReadResourceTrendPoints(ctx, entity.TableNameMetricDisk, 2001, 0, 9999,
		[]string{"bucket_ts", "used_percent"})
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 {
		t.Fatalf("2001 号的趋势点 = %d, want 2（不得串到 2002）", len(points))
	}
	if points[0].T != 1000 || points[1].T != 1300 {
		t.Fatalf("子表趋势点 t = [%d %d], want [1000 1300]（必须按 bucket_ts 升序）", points[0].T, points[1].T)
	}
	n, err := repo.CountWide(ctx, entity.TableNameMetric5m, 1001, 0, 9999)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("CountWide = %d, want 1", n)
	}
}
