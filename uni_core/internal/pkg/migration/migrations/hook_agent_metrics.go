package migrations

import (
	"context"

	"gorm.io/gorm"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/migration"
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
)

func init() {
	// 建表钩子：必须早于 AutoMigrate，且不参与版本号跳过机制（每次启动都执行）。
	//
	// 为什么单独一个文件、而不是塞进某个 vNNN 种子迁移里：
	//   - 它不是「一次性版本化迁移」，而是**每次启动都要跑的幂等 reconcile**；
	//   - 建表实现落在 repository 包 —— 那里同时看得到列 DDL 与实体字段，
	//     便于 device_metric_schema_test.go 的列名一致性守卫。
	//     （已核实无循环依赖：migration 父包既不依赖 repository、也不 import 本子包。）
	migration.RegisterPreMigrate("agent-metric-tables", func(ctx context.Context, db *gorm.DB, dialect string) error {
		return repository.EnsureMetricSchema(ctx, db, dialect)
	})
}
