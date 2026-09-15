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

// 守卫：分区维护审计表**必须**在 AutoMigrate 清单里，且**必须**继续缺席指标表清单。
//
// 两个方向缺一不可：
//   - 在：审计表是普通表（spec §7.3「不分区」），AutoMigrate 是唯一能建它的地方
//     （指标表那条 pre-migrate 钩子只建分区表；版本化迁移的 Up 跑在 AutoMigrate 之后，
//     在它里面手写 DDL 会多出第二份列定义）。若它掉出清单，生产上「协调器写审计行」
//     会以「表不存在」失败——而那条失败按设计**不得**中断分区维护，于是症状会变成
//     「分区照常维护、审计永远是空的」，一个不会自己冒出来的静默缺口。
//   - 缺席指标表：见上方 TestAutoMigrateExcludesPartitionedMetricTables。
//
// 同名双保险：本测试与 TestAutoMigrateExcludesPartitionedMetricTables 共用同一份
// AutoMigrateEntities()，故「加错了哪张表」两个方向都会红。
func TestAutoMigrateIncludesPartitionLogTable(t *testing.T) {
	found := false
	for _, m := range AutoMigrateEntities() {
		tn, ok := m.(interface{ TableName() string })
		if !ok {
			continue
		}
		if tn.TableName() == entity.TableNameAgentPartitionLog {
			found = true
		}
	}
	if !found {
		t.Fatalf("%s 必须加入 AutoMigrate 清单（普通表只有这里能建）", entity.TableNameAgentPartitionLog)
	}
	// 表名常量本身必须与实体 TableName() 一致：审计行的 table_name 列、仓储查询、
	// 迁移种子与运维 SQL 都以这个字符串为准，写错一个字符就是「表建了、但没人找得到它」。
	if got := (entity.AgentPartitionLog{}).TableName(); got != entity.TableNameAgentPartitionLog {
		t.Fatalf("AgentPartitionLog.TableName() = %q, want %q", got, entity.TableNameAgentPartitionLog)
	}
	if entity.TableNameAgentPartitionLog == "" {
		t.Fatal("审计表名不能为空")
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
