package repository

import (
	"context"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/database"
	"gorm.io/gorm"
)

// 守卫：列 DDL 的列名集合必须与实体字段的 gorm column 完全一致。
// 这是「手写 DDL 与实体漂移」的唯一防线（DDL 不能参数化，也无法用 AutoMigrate 表达分区）。
func TestMetricDDLColumnsMatchEntityTags(t *testing.T) {
	cases := []struct {
		table string
		model any
	}{
		{entity.TableNameMetric5m, entity.DeviceMetricWide{}},
		{entity.TableNameMetric1h, entity.DeviceMetricWide{}}, // 同一结构体，列集必须一致
		{entity.TableNameMetricDisk, entity.DeviceMetricDisk{}},
		{entity.TableNameMetricDiskIO, entity.DeviceMetricDiskIO{}},
		{entity.TableNameMetricNIC, entity.DeviceMetricNIC{}},
		{entity.TableNameMetricSensor, entity.DeviceMetricSensor{}},
	}
	// 修正记录（执行 Task 6 时实测到的两处计划解析缺陷，探针证据见提交说明）：
	//  1. 原正则写作 "`gorm:\"column:([a-z0-9_]+)`"（两端带反引号）。reflect.StructTag
	//     返回的 tag **不含** 反引号，故原正则对 46 个字段命中 0 次 → want 恒为空集，
	//     「实体列在 DDL 中缺失」这个方向永远不可能触发（守卫是死的）。
	//  2. 原 token 解析只 Trim 掉反引号/引号，PRIMARY KEY (a, b) 约束被 "," 切开后的
	//     续行 token（如 "bucket_ts)"）会被当成一个列名 → 每个表都多出一个假列。
	// 两处均为**解析修正，断言与期望值一字未改**（两个方向都仍逐列比对）。
	colRe := regexp.MustCompile("gorm:\"column:([a-z0-9_]+)")
	colNameRe := regexp.MustCompile("^[a-z0-9_]+$")

	for _, c := range cases {
		ddl, ok := MetricColumnDDL(c.table)
		if !ok {
			t.Fatalf("%s 缺少列 DDL 定义", c.table)
		}
		want := map[string]bool{}
		rt := reflect.TypeOf(c.model)
		for i := 0; i < rt.NumField(); i++ {
			m := colRe.FindStringSubmatch(string(rt.Field(i).Tag))
			if len(m) == 2 {
				want[m[1]] = true
			}
		}
		got := map[string]bool{}
		for _, line := range strings.Split(ddl, ",") {
			f := strings.Fields(strings.TrimSpace(line))
			if len(f) == 0 {
				continue
			}
			name := strings.Trim(f[0], "`\"")
			if name == "" || strings.EqualFold(name, "PRIMARY") || strings.EqualFold(name, "UNIQUE") || strings.EqualFold(name, "KEY") {
				continue
			}
			if !colNameRe.MatchString(name) {
				continue // PRIMARY KEY (...) 约束的续行 token（如 "bucket_ts)"）：不是列定义
			}
			got[name] = true
		}
		for name := range want {
			if !got[name] {
				t.Errorf("%s: 实体列 %q 在列 DDL 中缺失", c.table, name)
			}
		}
		for name := range got {
			if !want[name] {
				t.Errorf("%s: 列 DDL 中的 %q 在实体里没有对应字段", c.table, name)
			}
		}
	}
}

func TestMetricColumnDDLDeclaresCompositePrimaryKey(t *testing.T) {
	wide, _ := MetricColumnDDL(entity.TableNameMetric5m)
	if !strings.Contains(strings.ToUpper(wide), "PRIMARY KEY") {
		t.Fatal("宽表列 DDL 必须声明复合主键")
	}
	disk, _ := MetricColumnDDL(entity.TableNameMetricDisk)
	if !strings.Contains(disk, "resource_id") || !strings.Contains(disk, "bucket_ts") {
		t.Fatal("子表主键必须是 (resource_id, bucket_ts)")
	}
}

func TestMetricTablesCountAndOrder(t *testing.T) {
	tables := MetricTables()
	if len(tables) != 6 {
		t.Fatalf("指标表应为 6 张, got %d", len(tables))
	}
	if tables[0] != entity.TableNameMetric5m {
		t.Fatalf("顺序必须稳定且以 _5m 打头, got %q", tables[0])
	}
}

func TestEnsureSchemaOnSQLiteFallsBackToPlainTables(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	database.NewCallbacks(nil).Register(db)
	repo := NewDeviceMetricSchemaRepository(db)

	if err := repo.EnsureSchema(context.Background(), "sqlite", time.Date(2026, 9, 14, 7, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("EnsureSchema(sqlite): %v", err)
	}
	for _, tbl := range MetricTables() {
		if !db.Migrator().HasTable(tbl) {
			t.Fatalf("%s 未被创建", tbl)
		}
	}
	// 幂等：再跑一次不得报错
	if err := repo.EnsureSchema(context.Background(), "sqlite", time.Date(2026, 9, 14, 7, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("EnsureSchema 必须幂等: %v", err)
	}
}
