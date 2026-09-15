package repository

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/database"
	"gorm.io/gorm"
)

func TestMetricTableNames(t *testing.T) {
	cases := map[string]string{
		entity.DeviceMetricWide{}.TableName():   entity.TableNameMetric5m,
		entity.DeviceMetricDisk{}.TableName():   entity.TableNameMetricDisk,
		entity.DeviceMetricDiskIO{}.TableName(): entity.TableNameMetricDiskIO,
		entity.DeviceMetricNIC{}.TableName():    entity.TableNameMetricNIC,
		entity.DeviceMetricSensor{}.TableName(): entity.TableNameMetricSensor,
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("TableName() = %q, want %q", got, want)
		}
	}
	if entity.TableNameMetric5m == entity.TableNameMetric1h {
		t.Fatal("5m 与 1h 必须是两张物理表（保留期不同，分区需要各自独立回收）")
	}
}

func TestMetricModelsHaveCompositePrimaryKeyAndNoIDColumn(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	database.NewCallbacks(nil).Register(db)
	if err := db.AutoMigrate(
		&entity.DeviceMetricWide{}, &entity.DeviceMetricDisk{},
		&entity.DeviceMetricDiskIO{}, &entity.DeviceMetricNIC{}, &entity.DeviceMetricSensor{},
	); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}

	// 指标表无 id 列（自然复合主键）；雪花回调靠 LookUpField("ID") 取字段，
	// 字段不存在时静默跳过，安全。
	for _, m := range []any{&entity.DeviceMetricWide{}, &entity.DeviceMetricDisk{}} {
		stmt := &gorm.Statement{DB: db}
		if err := stmt.Parse(m); err != nil {
			t.Fatal(err)
		}
		if stmt.Schema.LookUpField("ID") != nil {
			t.Fatalf("%s 不应有 ID 字段（指标表用自然复合主键）", stmt.Schema.Table)
		}
		if stmt.Schema.PrioritizedPrimaryField != nil && len(stmt.Schema.PrimaryFields) < 2 {
			t.Fatalf("%s 的主键应为复合键, got %d 列", stmt.Schema.Table, len(stmt.Schema.PrimaryFields))
		}
	}
}
