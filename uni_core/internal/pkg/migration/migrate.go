package migration

import (
	"fmt"

	"github.com/tangwy-t/webmanager-server/internal/model/entity"

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

// MigrateAll 执行全部实体的 AutoMigrate。
// 新增实体时只需在此处追加，无需修改 main()。
func MigrateAll(db *gorm.DB) error {
	if err := setupJoinTables(db); err != nil {
		return err
	}
	return db.AutoMigrate(
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
	)
}
