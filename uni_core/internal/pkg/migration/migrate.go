package migration

import (
	"fmt"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"

	"gorm.io/gorm"
)

// joinTableSetups 声明全部 many2many 关联与其显式 join 实体的对应关系。
//
// 这一步不能省。GORM 默认会依据 many2many tag 合成一个匿名 join schema,
// 该 schema 只含两个外键列, 不含 id。后果是:
//
//  1. 全局 id:generate 回调 (internal/pkg/database) 用
//     Statement.Schema.LookUpField("ID") 取字段, 在合成 schema 上返回 nil,
//     于是回调安静跳过 —— 写出的 join 行 ID 恒为 0。
//     而 id 是主键, 故只有第一行能落库。
//  2. GORM 插 join 行时带 clause.OnConflict{DoNothing: true}
//     (gorm/callbacks/associations.go), 把后续主键冲突降级为静默跳过,
//     调用方拿到的是 err == nil。
//
// 两者叠加的表现就是: 给用户分配多个角色时, 只有第一个角色生效,
// 且没有任何报错。SetupJoinTable 把 relation.JoinTable 换成真正带 id 的
// 实体 schema, 让回调能正确生成雪花 ID, 从根上消除该冲突。
//
// 新增 many2many 关联时, 必须在此处补一条, 否则会重现上述静默丢数据。
// 该约束由 migrate_join_table_test.go 的 TestJoinTableSetups_CoverAllMany2Many 守卫。
var joinTableSetups = []struct {
	model interface{}
	field string
	join  interface{}
}{
	{&entity.SysUser{}, "Roles", &entity.SysUserRole{}},
	{&entity.SysRole{}, "Users", &entity.SysUserRole{}},
	{&entity.SysRole{}, "Menus", &entity.SysRoleMenu{}},
	{&entity.SysRole{}, "Depts", &entity.SysRoleDept{}},
}

// setupJoinTables 把所有 many2many 关联指向显式的 join 实体。
// 必须在 AutoMigrate 之前调用: AutoMigrate 会依据 JoinTable schema 决定
// 建表语句, 晚于它设置就会基于合成 schema 建出缺 id 列的表。
func setupJoinTables(db *gorm.DB) error {
	for _, s := range joinTableSetups {
		if err := db.SetupJoinTable(s.model, s.field, s.join); err != nil {
			return fmt.Errorf("setup join table %T.%s: %w", s.model, s.field, err)
		}
	}
	return nil
}

// autoMigrateEntities 是 AutoMigrate 的实体清单。
//
// 例外的两类实体：
//  1. **分区指标表**（device_metric_5m/_1h/_disk/_diskio/_nic/_sensor）**不在本清单**。
//     AutoMigrate 只能建普通表，而 MySQL 事后转分区是整表 COPY 重建、PostgreSQL
//     官方明确不允许把普通表转成分区表。它们由迁移 v008 注册的 pre-migrate 钩子
//     以分区形态建表（见 migration.RegisterPreMigrate）。
//  2. many2many 的 join 实体必须同时登记在 joinTableSetups，否则雪花 ID 回调用
//     合成 schema 取不到 ID 字段、静默写出 id=0 的 join 行（见该变量注释）。
//
// 提取成变量而非内联在 MigrateAll 里，是为了让守卫测试能内省本清单
// （auto_migrate_guard_test.go 的 TestAutoMigrateExcludesPartitionedMetricTables）。
var autoMigrateEntities = []any{
	&entity.SysUser{},
	&entity.SysRole{},
	&entity.SysDept{},
	&entity.SysMenu{},
	&entity.SysUserRole{},
	&entity.SysRoleMenu{},
	&entity.SysRoleDept{},
	&entity.SysDictType{},
	&entity.SysDictData{},
	&entity.SysConfig{},
	&entity.SysNotice{},
	&entity.SysNoticeUser{},
	&entity.SysFile{},
	&entity.SysJob{},
	&entity.SysJobLog{},
	&entity.SysOperationLog{},
	&entity.SysLoginLog{},
	&entity.SysMigration{},
	// ── 设备监控（业务域实体）────────────────────────────────
	&entity.Device{},
	&entity.DeviceResource{},
	// 指标 6 表**刻意缺席**，见上方说明。
}

// AutoMigrateEntities 返回 AutoMigrate 清单的副本（守卫测试内省用）。
func AutoMigrateEntities() []any {
	out := make([]any, len(autoMigrateEntities))
	copy(out, autoMigrateEntities)
	return out
}

// MigrateAll 执行全部实体的 AutoMigrate。
// 新增实体时只需追加到 autoMigrateEntities，无需修改 main()。
func MigrateAll(db *gorm.DB) error {
	if err := setupJoinTables(db); err != nil {
		return err
	}
	return db.AutoMigrate(autoMigrateEntities...)
}
