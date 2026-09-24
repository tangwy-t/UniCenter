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
		Version:     12,
		Description: "新增设备监控总览菜单",
		Up:          seedDeviceOverviewMenu,
	})
}

// deviceOverviewMenuDefinitions 是**新增**的总览页菜单定义。
//
// ── 为什么是 v012 而不是改 v008 ─────────────────────────────────
//
// v008 已在真实库执行过（sys_migration 有版本 8 的账本记录）。迁移框架
// 只跑「版本 > 当前最大版本」的迁移（见 migration.Run），故修改 v008 的定义
// 对**已部署的库完全无效** —— 那正是「升级后菜单不见了，重装才正常」这类
// 问题的根源。新菜单必须走新版本号。
//
// ── 与前端插件路由的双向钉住 ────────────────────────────────
//
// 后端菜单模式下，MenuProcessor 用**菜单的 path** 去前端插件路由表里取组件：
//
//	const component = routeMap.get(menu.path)
//	if (!component) continue          // ← 取不到就整条菜单消失（不报错）
//
// 故 `Path` 必须与 uni_console/src/modules/device/index.ts 里注册的
// `/device/overview` **逐字一致**。差一个字符的后果是「登录后侧边栏没有这一项」，
// 且控制台没有任何错误 —— 极难排查。该约束由本文件的
// TestV012OverviewMenuPathMatchesFrontend 守卫（读前端 index.ts 做字面量比对），
// 不依赖人记住这条规则。
var deviceOverviewMenuDefinitions = []menuDef{
	{Key: "device:overview", Parent: "device", Name: "监控总览", Type: "menu",
		Perms: permission.PermDeviceQuery, Path: "/device/overview",
		Component: "device/overview", Sort: 0, Icon: "material-symbols:monitoring"},
}

// seedDeviceOverviewMenu 把总览菜单挂到**已存在**的「设备管理」目录下。
//
// 与 v008 的写法有两处刻意不同：
//
//  1. **查父而不靠 idByKey**：v008 在同一次 Up 里先建目录再建子菜单，故能靠
//     map 传递父 ID。本迁移只加子菜单，父目录是**上一次迁移留下的**，必须
//     从库里查。查不到就直接报错 —— 不静默把 ParentID 置 0（那会让菜单
//     变成顶级项，在侧边栏里与「服务监控」「系统管理」并列，看起来像 bug 但
//     不报错）。
//
//  2. **幂等：按 (parent_id, path) 查重后跳过**。迁移框架保证同一版本只执行
//     一次，但「从旧备份恢复 + 重新启动」等场景会让同一 Up 在语义上重放
//     （备份里的 sys_migration 可能落后于实际数据）。重复插入的后果是侧边栏
//     出现两条同名菜单 —— 而路由的 name 相同，点第二条会因 vue-router 重名
//     而行为异常。故这里显式查重。
func seedDeviceOverviewMenu(tx *gorm.DB) error {
	var parent entity.SysMenu
	if err := tx.Where("type = ? AND name = ?", "dir", "设备管理").First(&parent).Error; err != nil {
		return fmt.Errorf("v012 找不到「设备管理」目录菜单（v008 应已创建）: %w", err)
	}

	for _, d := range deviceOverviewMenuDefinitions {
		var n int64
		if err := tx.Model(&entity.SysMenu{}).
			Where("parent_id = ? AND path = ?", parent.ID, d.Path).
			Count(&n).Error; err != nil {
			return fmt.Errorf("v012 查询菜单 %s: %w", d.Key, err)
		}
		if n > 0 {
			continue // 已存在（重放/手工建过）→ 保持现状，不覆盖运维可能的调整
		}

		menu := entity.SysMenu{
			ParentID:  util.Ptr(parent.ID),
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
			return fmt.Errorf("v012 创建菜单 %s: %w", d.Key, err)
		}
	}
	return nil
}
