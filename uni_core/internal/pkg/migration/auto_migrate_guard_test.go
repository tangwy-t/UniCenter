package migration

import (
	"testing"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
)

// 守卫：6 张指标表**绝不能**进 AutoMigrate。
// 理由：AutoMigrate 只能建普通表，而 MySQL 事后转分区是整表 COPY 重建、
// PG 官方不允许把普通表转成分区表。指标表由 pre-migrate 钩子以分区形态建。
func TestAutoMigrateExcludesPartitionedMetricTables(t *testing.T) {
	forbidden := map[string]bool{
		entity.TableNameMetric5m:     true,
		entity.TableNameMetric1h:     true,
		entity.TableNameMetricDisk:   true,
		entity.TableNameMetricDiskIO: true,
		entity.TableNameMetricNIC:    true,
		entity.TableNameMetricSensor: true,
	}
	for _, m := range AutoMigrateEntities() {
		tn, ok := m.(interface{ TableName() string })
		if !ok {
			continue
		}
		if forbidden[tn.TableName()] {
			t.Fatalf("%s 进入了 AutoMigrate —— 它会先建成普通表，分区形态将永远建不出来", tn.TableName())
		}
	}
}

// 守卫：device / device_resource 必须在 AutoMigrate 清单里。
func TestAutoMigrateIncludesDeviceTables(t *testing.T) {
	want := map[string]bool{"device": false, "device_resource": false}
	for _, m := range AutoMigrateEntities() {
		tn, ok := m.(interface{ TableName() string })
		if !ok {
			continue
		}
		if _, hit := want[tn.TableName()]; hit {
			want[tn.TableName()] = true
		}
	}
	for table, found := range want {
		if !found {
			t.Fatalf("%s 必须加入 AutoMigrate 清单", table)
		}
	}
}

// 守卫：device 不得注册数据权限维度（它没有 owner/部门维度）。
// scope_entities_test.go 要求 ScopeEntities 与快照双向相等，多注册即失败；
// 这里再显式声明意图，避免后人「顺手补一条」。
func TestDeviceIsNotScopeRegistered(t *testing.T) {
	for _, e := range entity.ScopeEntities {
		if tn, ok := e.(interface{ TableName() string }); ok && tn.TableName() == "device" {
			t.Fatal("device 不得加入 ScopeEntities：它没有数据权限维度，且会让 scope_entities_test 失败")
		}
	}
}
