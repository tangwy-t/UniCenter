package migrations

import (
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/migration"
)

// TestV011IsRegistered 钉住版本注册（重复版本会在 Register 里 panic，
// 漏注册则本迁移整条不执行）。
func TestV011IsRegistered(t *testing.T) {
	found := false
	for _, m := range migration.All() {
		if m.Version == 11 {
			found = true
			if m.Up == nil {
				t.Fatal("v011 的 Up 不能为空")
			}
		}
	}
	if !found {
		t.Fatal("v011 未注册（新增迁移必须在 init() 中 migration.Register）")
	}
}

// framesLimitKey / framesLimitSeeded 是**契约字面量**：不复用实现里的常量，
// 否则键名或默认值写错也自洽。900 与 wireup 的 defaultAgentMaxFramesPerMin 同值
// （wireup.go 的注释已声明「种子键由 v011 种下」；那处是未导出常量，
// 故这里按字面量交叉钉住，改一处必须同时改另一处的测试）。
const (
	framesLimitKey    = "sys.agent.maxFramesPerMin"
	framesLimitSeeded = "900"
)

// TestV011ConfigDefinitionsContract 钉住「只补种一个配置键」以及它的形状。
//
// 为什么把「只有一个」也写成断言：计划与 spec §7.3 都要求 v011 **只**种这把键，
// 且**不得**加任何「审计开关」类配置 —— 审计做成可关掉的开关，只会把
// 「为什么这条分区没有留痕」变成一个新的排障盲区。多一个键就是一次静默的契约放宽。
func TestV011ConfigDefinitionsContract(t *testing.T) {
	if len(agentPartitionLogConfigDefinitions) != 1 {
		t.Fatalf("v011 应只补种一个配置键，实际 %d 个", len(agentPartitionLogConfigDefinitions))
	}
	def := agentPartitionLogConfigDefinitions[0]
	if def.Key != framesLimitKey {
		t.Fatalf("配置键 = %q, want %q", def.Key, framesLimitKey)
	}
	if def.Value != framesLimitSeeded {
		t.Fatalf("%s 的默认值 = %q, want %q（与 wireup 的缺省阈值同值）", def.Key, def.Value, framesLimitSeeded)
	}
	if def.Type != "N" {
		t.Fatalf("%s 的 Type = %q, want \"N\"（帧数阈值是数值）", def.Key, def.Type)
	}
	if def.Name == "" || def.Remark == "" {
		t.Fatalf("%s 的名称/说明不能为空（配置页直接展示）", def.Key)
	}
	if !def.Enabled {
		t.Fatalf("%s 必须落库为启用：限流器每帧都读它", def.Key)
	}

	// 反向守卫：**任何**迁移批次都不得种入「审计开关」类配置键。
	// 审计是 spec §7.3 明文要求的运维流水，不是可选项。
	batches := map[string][]configDef{
		"v006 系统配置":    configDefinitions,
		"v008 设备配置":    deviceConfigDefinitions,
		"v009 任务/分区配置": agentJobConfigDefinitions,
		"v011 分区审计配置":  agentPartitionLogConfigDefinitions,
	}
	for name, defs := range batches {
		for _, d := range defs {
			lower := strings.ToLower(d.Key)
			if strings.Contains(lower, "audit") || strings.Contains(lower, "partitionlog") ||
				strings.Contains(lower, "partition_log") {
				t.Fatalf("%s 里出现了审计开关类配置键 %q：审计是 spec §7.3 的明文要求，"+
					"做成可关掉的开关会让「这条分区为什么没有留痕」变成排障盲区", name, d.Key)
			}
		}
	}
}

// v011 走**真实 runner**：审计表必须真的被建出来（普通表，走 AutoMigrate），
// 配置键必须真的落库，且重复执行幂等。
//
// 为什么断言 HasTable 而不是「v011 的 Up 建了表」：审计表**不在** Up 里建
// （普通表只有 autoMigrateEntities 能建，见 v011 文件的注释），所以「表存在」
// 这件事只能由 runner 的完整路径证明：pre-migrate → AutoMigrate → 版本化迁移。
func TestV011SeedsFramesLimitThroughMigrationRunner(t *testing.T) {
	db := newAgentSeedTestDB(t)

	if err := migration.Run(db, logger.NewNop()); err != nil {
		t.Fatalf("migration.Run: %v", err)
	}
	if !db.Migrator().HasTable(entity.TableNameAgentPartitionLog) {
		t.Fatalf("%s 不存在：普通表的建表只能来自 AutoMigrate 清单（autoMigrateEntities）",
			entity.TableNameAgentPartitionLog)
	}
	if got := readConfigValue(t, db, framesLimitKey); got != framesLimitSeeded {
		t.Fatalf("%s = %q, want %q", framesLimitKey, got, framesLimitSeeded)
	}
	if n := countConfigKey(t, db, framesLimitKey); n != 1 {
		t.Fatalf("%s 有 %d 行, want 1", framesLimitKey, n)
	}

	// 二次 runner：版本账本让 v011 不再执行，终态一动不动（表在、值在、只有一行）。
	if err := migration.Run(db, logger.NewNop()); err != nil {
		t.Fatalf("二次 migration.Run: %v", err)
	}
	if !db.Migrator().HasTable(entity.TableNameAgentPartitionLog) {
		t.Fatalf("二次 runner 后 %s 消失了", entity.TableNameAgentPartitionLog)
	}
	if got := readConfigValue(t, db, framesLimitKey); got != framesLimitSeeded {
		t.Fatalf("二次 runner 后 %s = %q, want %q", framesLimitKey, got, framesLimitSeeded)
	}
	if n := countConfigKey(t, db, framesLimitKey); n != 1 {
		t.Fatalf("二次 runner 后 %s 有 %d 行, want 1", framesLimitKey, n)
	}
}

// TestV011PreservesOperatorCustomizedFramesLimit 钉住「只补缺、不覆写」。
//
// 与 v009 的 seedAgentJobConfigs 同款取向：真实库里这一行可能已经被运维按自己的
// 设备规模调过（900 只是「10s 上报 + 心跳」下的约 100 倍余量），
// 种子把它打回默认值是不可观测的破坏。
func TestV011PreservesOperatorCustomizedFramesLimit(t *testing.T) {
	db := newAgentSeedTestDB(t)

	custom := entity.SysConfig{
		Name: "运维调过的帧限流阈值", ConfigKey: framesLimitKey,
		ConfigValue: "20000", ConfigType: "N",
	}
	if err := db.Create(&custom).Error; err != nil {
		t.Fatalf("预置运维值: %v", err)
	}

	if err := seedAgentPartitionLogConfig(db); err != nil {
		t.Fatalf("seedAgentPartitionLogConfig: %v", err)
	}
	if got := readConfigValue(t, db, framesLimitKey); got != "20000" {
		t.Fatalf("%s = %q, want 20000（已存在的行是权威，种子不得覆写）", framesLimitKey, got)
	}
	if n := countConfigKey(t, db, framesLimitKey); n != 1 {
		t.Fatalf("%s 有 %d 行, want 1（不得重复插入）", framesLimitKey, n)
	}
}

// readConfigValue 读回某配置键的值；缺失即失败（缺键与空值是两种不同的故障）。
func readConfigValue(t *testing.T, db *gorm.DB, key string) string {
	t.Helper()
	var row entity.SysConfig
	if err := db.Where("config_key = ?", key).First(&row).Error; err != nil {
		t.Fatalf("读配置 %s: %v", key, err)
	}
	return row.ConfigValue
}

func countConfigKey(t *testing.T, db *gorm.DB, key string) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&entity.SysConfig{}).Where("config_key = ?", key).Count(&n).Error; err != nil {
		t.Fatalf("统计配置 %s: %v", key, err)
	}
	return n
}
