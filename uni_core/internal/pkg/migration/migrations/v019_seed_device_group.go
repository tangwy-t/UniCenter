package migrations

import (
	"fmt"

	"gorm.io/gorm"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/migration"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/permission"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/util"
)

func init() {
	migration.Register(migration.Migration{
		Version:     19,
		Description: "新增设备分组/标签写按钮权限",
		Up:          seedDeviceGroupPerm,
	})
}

// deviceGroupMenuDefinitions 是新增的「设置分组」按钮权限定义。
//
// ── 为什么是 v019 而不是改 v008 ─────────────────────────────────
//
// v008 已在真实库执行过（sys_migration 有版本 8 的账本记录）。迁移框架只跑
// 「版本 > 当前最大版本」的迁移（见 migration.Run），故修改 v008 的定义对
// **已部署的库完全无效** —— 新按钮必须走新版本号（与 v012 新增总览菜单同一理由）。
//
// ── 只写种子、不写 DDL ─────────────────────────────────────────
//
// group_name / tags 两列由 AutoMigrate 依 entity.Device 拥有（migrate.go 的教义：
// 版本化迁移的 Up 跑在 AutoMigrate 之后，在它里面手写 DDL 会多出第二份列定义）。
// 本迁移**不碰列**，只补一个承载 device:index:group 权限码的按钮节点 —— 有了它，
// 角色管理界面才能把该权限分配给用户（写端点 PATCH /devices/:id 由它守门）。
//
// Sort = 5：device:index 下既有按钮 query=1 / enable=2 / disable=3 / delete=4，
// 本按钮取当前最大 + 1（由 TestV019ButtonSortFollowsExisting 守卫）。
var deviceGroupMenuDefinitions = []menuDef{
	{Key: "device:index:group", Parent: "device:index", Name: "设置分组", Type: "btn",
		Perms: permission.PermDeviceGroup, Sort: 5},
}

// seedDeviceGroupPerm 把「设置分组」按钮挂到**已存在**的 device:index 菜单下。
//
// 与 v012 同一形态：
//
//  1. **查父而不靠 idByKey**：父菜单 device:index 由 v008 创建，本迁移只加子按钮，
//     必须从库里查。查不到即报错 —— 不静默把 ParentID 置 0（那会让按钮变成顶级项）。
//
//  2. **幂等：按 (parent_id, perms) 查重后跳过**。迁移框架保证同一版本只执行一次，
//     但「从旧备份恢复 + 重新启动」会让同一 Up 在语义上重放（备份里的 sys_migration
//     可能落后于实际数据）。按钮无 path，权限码即身份（与 v015 的 btn 查重口径一致）。
func seedDeviceGroupPerm(tx *gorm.DB) error {
	parent, err := findMenuID(tx, "type = ? AND path = ?", "menu", "/device/index")
	if err != nil {
		return fmt.Errorf("v019 找不到「设备列表」菜单 device:index（v008 应已创建）: %w", err)
	}

	for _, d := range deviceGroupMenuDefinitions {
		var n int64
		if err := tx.Model(&entity.SysMenu{}).
			Where("parent_id = ? AND perms = ?", parent, d.Perms).Count(&n).Error; err != nil {
			return fmt.Errorf("v019 查询按钮 %s: %w", d.Key, err)
		}
		if n > 0 {
			continue // 已存在（重放/手工建过）→ 保持现状，不覆盖运维可能的调整
		}

		menu := entity.SysMenu{
			ParentID:  util.Ptr(parent),
			Name:      d.Name,
			Type:      d.Type,
			Perms:     strPtr(d.Perms),
			Path:      strPtr(d.Path),
			Component: strPtr(d.Component),
			Sort:      util.Ptr(d.Sort),
			Icon:      strPtr(d.Icon),
			Visible:   util.Ptr[int8](entity.MenuVisible),
			Status:    util.Ptr[int8](entity.MenuStatusEnabled),
		}
		if err := tx.Create(&menu).Error; err != nil {
			return fmt.Errorf("v019 创建按钮 %s: %w", d.Key, err)
		}
	}
	return nil
}