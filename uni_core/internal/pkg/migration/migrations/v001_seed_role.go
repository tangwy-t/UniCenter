package migrations

import (
	"gorm.io/gorm"

	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/migration"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

func init() {
	migration.Register(migration.Migration{
		Version:     1,
		Description: "初始化角色",
		Up:          seedRole,
	})
}

// adminRole 超级管理员角色种子模板。v001 只负责创建角色本身；
// admin 用户默认拥有全量菜单（鉴权层直通），无需 sys_role_menu 授权；
// 需要角色 ID 的迁移（如 v004 用户绑定）自行查库，种子间不共享包级变量。
var adminRole = entity.SysRole{
	Name:      "超级管理员",
	Code:      "admin",
	DataScope: util.Ptr[int8](1),
	Sort:      util.Ptr(0),
	Status:    util.Ptr[int8](entity.RoleStatusEnabled),
	Remark:    util.Ptr("系统内置超级管理员角色"),
}

func seedRole(tx *gorm.DB) error {
	// 角色 ID 由 snowflake 回调生成并回写到 slice 元素，本迁移无消费者，
	// 后续迁移需要时自行按 code 查询。
	return tx.CreateInBatches([]entity.SysRole{adminRole}, 1).Error
}
