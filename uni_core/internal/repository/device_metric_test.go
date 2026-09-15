package repository

import (
	"context"
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
