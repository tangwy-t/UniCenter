// Package migration 提供轻量级版本化数据库迁移系统。
// 每次启动只执行未应用的迁移，事务中先 INSERT 版本记录再执行 Up()，
// DB 主键约束充当分布式锁，支持多实例并发部署。
//
// # 迁移策略：只前进（forward-only），不提供 Down/回滚
//
// 本框架刻意不实现 Down/回滚，原因：
//   - 数据迁移不可逆：种子数据（如 admin 用户、系统菜单）上线后
//     会被运营修改，Down 无法判断"恢复种子值"还是"保留修改"；
//   - 结构变更由 AutoMigrate 自动对比完成，本身不存在"撤销"概念，
//     Down 只能覆盖手写部分，做出来也是半吊子；
//   - 一个写错的 Down（回滚时误删数据）比没有 Down 危害更大。
//
// ## 出了问题怎么办
//
// 写一个新的正向迁移修复（如 vNNN_fix_xxx.go），永不回头修改已发布的
// 迁移文件。已应用的迁移文件在事后被发现有 bug 也一样：不改旧文件
// （多实例部署中旧文件已被跳过，改了也不生效），追加新版本修正。
//
// ## 写新迁移的兼容性要求
//
// 为了让"旧二进制 + 新库结构"在发布窗口内仍能运行：
//   - 只加列/加表，不删列、不改列类型；
//   - 新列必须给默认值或允许 NULL，避免旧代码 INSERT 失败；
//   - 确需删列时分两步：先发版本让代码停止读写该列，
//     再在下个版本迁移里删除。
//
// 新迁移文件命名：migrations/vNNN_seed_xxx.go（初始化种子按领域拆分）
// 或后续增量修复可用 vNNN_描述.go，注册见 register.go。
package migration

import (
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/database"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
)

// Migration 表示一个数据库迁移。
type Migration struct {
	Version     int
	Description string
	// Up 是迁移的执行逻辑，接收 *gorm.DB 事务实例。
	// 返回 error 时事务自动回滚，version 记录自动消失。
	Up func(tx *gorm.DB) error
}

// Run 执行所有未应用的迁移。已在其他实例执行的迁移通过主键冲突跳过，
// 单个迁移在事务中执行，失败时自动回滚。
func Run(db *gorm.DB, log logger.LoggerInterface) error {

	// ── AutoMigrate ────────────────────────────────────────────────
	if err := MigrateAll(db); err != nil {
		log.Error("failed to auto-migrate", zap.Error(err))
		return fmt.Errorf("failed to auto-migrate: %w", err)
	}
	log.Info("auto-migrate completed")

	// 查询当前已应用的最高版本
	var maxVersion int
	if err := db.Model(&entity.SysMigration{}).Select("COALESCE(MAX(version), 0)").Scan(&maxVersion).Error; err != nil {
		return fmt.Errorf("migration: 查询当前版本失败: %w", err)
	}

	all := All()

	// 找到最后一个已应用版本在迁移列表中的位置（版本号可能不连续）
	lastApplied := 0
	for i, m := range all {
		if m.Version <= maxVersion {
			lastApplied = i + 1
		}
	}
	if lastApplied >= len(all) {
		log.Info("migration: 所有迁移已应用，跳过", zap.Int("currentVersion", maxVersion), zap.Int("total", len(all)))
		return nil
	}

	pending := all[lastApplied:]
	// 起始标记:与结尾的"全部完成"呼应,日志被 SQL 刷屏时可按
	// "migration: 开始" / "migration: 全部完成" 检索首尾。
	log.Info("migration: 开始",
		zap.Int("pending", len(pending)),
		zap.Int("currentVersion", maxVersion),
		zap.Int("targetVersion", all[len(all)-1].Version))

	start := time.Now()
	executed, skipped := 0, 0
	for _, m := range pending {
		log.Info("migration: 执行中", zap.Int("version", m.Version), zap.String("description", m.Description))

		skippedThis := false
		err := db.Transaction(func(tx *gorm.DB) error {
			// 先占坑：INSERT version 记录
			record := entity.SysMigration{
				Version:     m.Version,
				Description: m.Description,
				AppliedAt:   time.Now(),
			}
			if err := tx.Create(&record).Error; err != nil {
				// 主键冲突 = 其他实例已执行，正常跳过
				if database.IsDuplicateKey(err) {
					log.Info("migration: 其他实例已执行，跳过", zap.Int("version", m.Version))
					skippedThis = true
					return nil
				}
				return fmt.Errorf("migration: 记录版本 %d 失败: %w", m.Version, err)
			}

			// 执行迁移逻辑
			if err := m.Up(tx); err != nil {
				return fmt.Errorf("migration: 版本 %d 执行失败: %w", m.Version, err)
			}

			return nil
		})
		if err != nil {
			return err
		}
		if skippedThis {
			skipped++
		} else {
			executed++
		}
	}

	log.Info("migration: 全部完成",
		zap.Int("executed", executed),
		zap.Int("skippedByOtherInstance", skipped),
		zap.Int("fromVersion", maxVersion),
		zap.Int("toVersion", all[len(all)-1].Version),
		zap.Duration("elapsed", time.Since(start)))
	return nil
}
