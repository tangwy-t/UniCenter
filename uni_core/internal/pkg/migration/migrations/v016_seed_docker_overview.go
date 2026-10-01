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
		Version:     16,
		Description: "新增 Docker 总览菜单（控制塔跨主机总览页）",
		Up:          seedDockerOverviewMenu,
	})
}

// dockerOverviewMenuDefinitions 是控制塔总览页的菜单定义（**新增**，不改 v015 ——
// v015 已在真实库执行过（sys_migration 有版本 15 的账本记录），改老定义对已部署
// 的库无效 —— 见 v012 开头的同款说明）。
//
// Sort=0：排在「容器」列表（v015 里 Sort=1）之前 —— 控制塔是 docker 域的首页，
// 点开目录先看到跨主机总览，再进各资源列表。
//
// Icon 复用 docker 菜单族用过的 ri:ship-line（航队隐喻，与总览响应的 fleet KPI
// 命名同一套语言；也在离线图标集内，check:icons 不会因为新图标名翻车）。
//
// ── 前端依赖（本切片只做主后端半边，窗口期必须写清楚）────────────────
//
// 后端菜单模式下 MenuProcessor 用菜单 path 去前端插件路由表取组件，取不到则
// **整条菜单静默消失**（不报错、不打日志）。前端控制塔页面（切片 2）落地时必须：
//
//	① 在 uni_console/src/modules/docker/index.ts 注册 path: '/docker' 的路由
//	   （component: () => import('./views/overview.vue')）；
//	② 补上 v012/v013/v015 同款的「菜单 path ↔ 前端路由」逐字守卫测试，把这条
//	   约束从注释升格为自动检查。
//
// 在那之前侧边栏暂时看不到本菜单 —— 这是「后端先落地、前端随后」的既有成本，
// 不是 bug（接口与类型此时已可供前端 slice 直接消费）。
var dockerOverviewMenuDefinitions = []menuDef{
	{Key: "docker:overview", Parent: "docker", Name: "Docker 总览", Type: "menu",
		Perms: permission.PermDockerList, Path: "/docker",
		Component: "docker/overview", Sort: 0, Icon: "ri:ship-line"},
}

// seedDockerOverviewMenu 把总览菜单挂到**已存在**的「Docker 管理」目录下。
//
// 与 v015 的写法有两处刻意不同（与 v012 同款，因为本迁移同样是「只加子菜单」）：
//  1. **查父而不靠 idByKey**：父目录是 v015 留下的，必须从库里查；查不到就报错
//     （不静默把 ParentID 置 0 —— 那会让菜单变成顶级项，看起来像 bug 但不报错）。
//  2. **幂等：按 (parent_id, path) 查重后跳过**：从旧备份恢复 + 重启会让同一 Up
//     在语义上重放，重复插入的症状是侧边栏两条同名菜单。
func seedDockerOverviewMenu(tx *gorm.DB) error {
	var parent entity.SysMenu
	if err := tx.Where("type = ? AND name = ?", "dir", "Docker 管理").First(&parent).Error; err != nil {
		return fmt.Errorf("v016 找不到「Docker 管理」目录菜单（v015 应已创建）: %w", err)
	}

	for _, d := range dockerOverviewMenuDefinitions {
		var n int64
		if err := tx.Model(&entity.SysMenu{}).
			Where("parent_id = ? AND path = ?", parent.ID, d.Path).
			Count(&n).Error; err != nil {
			return fmt.Errorf("v016 查询菜单 %s: %w", d.Key, err)
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
			return fmt.Errorf("v016 创建菜单 %s: %w", d.Key, err)
		}
	}
	return nil
}
