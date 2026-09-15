package migrations

import (
	"fmt"

	"gorm.io/gorm"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/migration"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/util"
)

func init() {
	migration.Register(migration.Migration{
		Version:     11,
		Description: "分区维护审计表（agent_metric_partition_log）与 agent 入站帧级限流阈值",
		Up:          seedAgentPartitionLogConfig,
	})
}

// ─ 本迁移为什么**只**种一个配置键 ──────────────────────────────
//
// spec §7.3 要求的分区审计表 `agent_metric_partition_log` 由 **AutoMigrate 清单**
// （internal/pkg/migration/migrate.go 的 autoMigrateEntities）建表，**不在**本迁移的
// Up 里建。理由不是省事，而是三条硬的：
//
//  1. **审计表是普通表**（spec §7.3 明文「不分区」），而版本化迁移的 Up 跑在
//     AutoMigrate **之后**（见 migration.Run 的顺序），它要建表只能手写 DDL ——
//     那就多出一份「列定义的第二真相」，与实体字段永久有漂移风险；
//  2. **恢复旧备份要能自愈**：AutoMigrate 每次启动都跑，而版本账本（sys_migration）
//     会被旧备份一起带回来。建表放在 AutoMigrate 里，任何一次「恢复了没有这张表的
//     备份」都会在下次启动自动补齐；放在 Up 里则要人肉发现（这正是 §7.3 说
//     「binlog 重放到 AutoMigrate 新建的空表」会丢结构的那类事故）；
//  3. 审计表**不需要分区**，所以它不触发指标表那条「必须建表时内联分区」的约束
//     （见 pre-migrate 钩子 hook_agent_metrics.go）—— AutoMigrate 建普通表恰好够用。
//
// 守卫：internal/pkg/migration/auto_migrate_guard_test.go 断言审计表**在**清单内、
// 而 6 张指标表**仍不在**（后者一旦混进 AutoMigrate，分区形态将永远建不出来）。
//
// ── 唯一新增的配置键 ──────────────────────────────────────
//
// `sys.agent.maxFramesPerMin` 是 agent **入站帧级**限流的阈值（协议登记的
// CloseRateLimited=4006 的判定依据，见 wireup 的 agentFrameLimiter）。
// 在 v011 之前它只有 wireup 侧的**缺省值** 900，库里没有这一行 ——
// 运维在配置页看不到、也改不了这个阈值（「防护的阈值不可见」本身就是缺口）。
// 值 900 与 wireup 的 defaultAgentMaxFramesPerMin 同值，由本文件的测试按字面量交叉钉住。
//
// 为什么不顺手加一个「审计开关」：审计是 spec §7.3 明文要求的运维流水。
// 做成可关掉的开关，只会把「为什么这条分区没有留痕」变成一个新的排障盲区
// （而且是那种「当时没人记得有人关过它」的盲区）。
var agentPartitionLogConfigDefinitions = []configDef{
	{Key: "sys.agent.maxFramesPerMin", Value: "900", Type: "N", Name: "Agent 入站帧级限流阈值",
		Remark: "单个 agent 连接每分钟最大入站帧数(0 或缺失按 900 处理)", Enabled: true},
}

// seedAgentPartitionLogConfig 按 config_key 去重地补齐配置键。
//
// 与 v009 的 seedAgentJobConfigs 同款取向：真实库里这一行可能已经被手工补过
// （运维按自己的设备数调过阈值），种子的意义只是「别让它缺失」，
// 不是「让它回到默认值」——覆写会把运维调过的值打回 900，且不可观测。
func seedAgentPartitionLogConfig(tx *gorm.DB) error {
	for _, def := range agentPartitionLogConfigDefinitions {
		var n int64
		if err := tx.Model(&entity.SysConfig{}).
			Where("config_key = ?", def.Key).Count(&n).Error; err != nil {
			return fmt.Errorf("v011 查询配置 %s: %w", def.Key, err)
		}
		if n > 0 {
			continue
		}
		row := entity.SysConfig{
			Name:        def.Name,
			ConfigKey:   def.Key,
			ConfigValue: def.Value,
			ConfigType:  def.Type,
			Remark:      util.Ptr(def.Remark),
			Status:      util.Ptr[int8](boolToInt8(def.Enabled)),
		}
		if err := tx.Create(&row).Error; err != nil {
			return fmt.Errorf("v011 写入配置 %s: %w", def.Key, err)
		}
	}
	return nil
}
