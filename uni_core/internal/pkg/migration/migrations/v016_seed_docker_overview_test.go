package migrations

import (
	"testing"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/permission"
)

// 本文件守卫 v016 总览菜单的五条硬约束。它们都属于「破了不报错、只表现为页面
// 异常」的那一类（菜单静默消失 / 变成顶级菜单 / 排序错位），故必须靠测试钉住。
// 与 v012 的差别：v012 有「菜单 path ↔ 前端路由」的字面量守卫，v016 的前端页面
// 是后一个切片，前端路由尚不存在 —— 该守卫留给前端落地时补（见种子文件开头
// 的「前端依赖」说明），这里钉住种子**本身**的形状与行为。

// TestV016OverviewMenuDefinitionsShape 钉住定义形状：挂接关系、权限码、path、排序。
func TestV016OverviewMenuDefinitionsShape(t *testing.T) {
	if len(dockerOverviewMenuDefinitions) != 1 {
		t.Fatalf("本迁移应恰有 1 个菜单定义，实得 %d", len(dockerOverviewMenuDefinitions))
	}
	d := dockerOverviewMenuDefinitions[0]

	// 必须挂在「Docker 管理」目录下：Seed 查父（parent_id 不会静默置 0），
	// 但定义本身的 Parent 键漂移会让查父时的假设与声明对不上。
	if d.Parent != "docker" {
		t.Fatalf("菜单 %s 的 Parent = %q, want %q（应挂在 Docker 管理目录下）", d.Key, d.Parent, "docker")
	}
	// 权限码与 GET /docker/overview 的路由静态 perm 一致：菜单可见性与接口鉴权
	// 不一致是典型的割裂体验（看得到菜单、点进去满屏 403）。
	if d.Perms != permission.PermDockerList {
		t.Fatalf("菜单 %s 的 Perms = %q, want %q（须与 GET /docker/overview 的鉴权一致）",
			d.Key, d.Perms, permission.PermDockerList)
	}
	// path 逐字：MenuProcessor 用菜单 path 去前端路由表取组件，差一个字符该菜单
	// 就**静默消失**（前端路由需在控制塔页面切片落地时注册 '/docker'）。
	if d.Path != "/docker" {
		t.Fatalf("菜单 %s 的 Path = %q, want %q（与前端 docker 插件路由逐字一致）", d.Key, d.Path, "/docker")
	}
	if d.Type != "menu" || d.Key != "docker:overview" || d.Name != "Docker 总览" {
		t.Fatalf("菜单定义与规格不符: %+v", d)
	}
	if d.Icon != "ri:ship-line" {
		t.Fatalf("菜单 %s 的 Icon = %q, want %q（复用 docker 菜单族已用过的 iconify 名）",
			d.Key, d.Icon, "ri:ship-line")
	}
}

// TestV016OverviewSortsBeforeContainers 总览必须排在「容器」列表之前：
// 控制塔是 docker 域的首页，点开目录先看到跨主机总览。
//
// 与 v015 的容器定义**交叉**取期望值（而不是硬编码 0/1）：排序关系是两批种子之间
// 的约束，任何一边改了排号，本测试都能抓住。
func TestV016OverviewSortsBeforeContainers(t *testing.T) {
	var containersSort int
	found := false
	for _, d := range dockerMenuDefinitions {
		if d.Key == "docker:containers" {
			containersSort = d.Sort
			found = true
		}
	}
	if !found {
		t.Fatalf("v015 定义里找不到 docker:containers（键名改了？）")
	}
	for _, d := range dockerOverviewMenuDefinitions {
		if d.Sort >= containersSort {
			t.Fatalf("菜单 %s 的 Sort = %d, 必须 < docker:containers 的 %d（总览在容器列表之前）",
				d.Key, d.Sort, containersSort)
		}
	}
}

// TestV016OverviewPermIsRegistered 权限码必须已在 permission.All() 注册
// （本测试与 v002 的双向守卫交叉钉住：v002 扫 menuDefBatches 里的字面量，
// 这里扫 permission 包 —— 两个方向各看一头）。
func TestV016OverviewPermIsRegistered(t *testing.T) {
	for _, code := range permission.All() {
		if code == permission.PermDockerList {
			return
		}
	}
	t.Fatalf("permission.All() 里没有 %q", permission.PermDockerList)
}

// TestV016ReplayIsIdempotent 在 v015 已落库的夹具上连跑两次 seedDockerOverviewMenu：
// 首次新增一行，重放必须查到 (parent_id, path) 命中并跳过 —— 重复插入的症状是
// 侧边栏两条「Docker 总览」。
func TestV016ReplayIsIdempotent(t *testing.T) {
	db := newDockerSeedTestDB(t)

	// 先让 v015 落库（真实库的状态：Docker 管理目录已存在）。
	if err := seedDockerMenus(db); err != nil {
		t.Fatalf("seedDockerMenus（模拟 v015 已执行）: %v", err)
	}
	base := countDockerMenus(t, db)

	if err := seedDockerOverviewMenu(db); err != nil {
		t.Fatalf("首次 seedDockerOverviewMenu: %v", err)
	}
	if got := countDockerMenus(t, db); got != base+int64(len(dockerOverviewMenuDefinitions)) {
		t.Fatalf("首次落地菜单行数 = %d, want %d", got, base+int64(len(dockerOverviewMenuDefinitions)))
	}

	if err := seedDockerOverviewMenu(db); err != nil {
		t.Fatalf("重放 seedDockerOverviewMenu 必须无错: %v", err)
	}
	if got := countDockerMenus(t, db); got != base+1 {
		t.Fatalf("重放后菜单行数 = %d, want %d（重复插入会让侧边栏出现两条同名菜单）", got, base+1)
	}

	// 落库行的挂接必须正确：parent 指向 v015 建下的「Docker 管理」目录，
	// 可见 + 启用（否则菜单在侧边栏不显示，且不报错）。
	var dir, row entity.SysMenu
	if err := db.Where("type = ? AND name = ?", "dir", "Docker 管理").First(&dir).Error; err != nil {
		t.Fatalf("查 Docker 管理目录: %v", err)
	}
	if err := db.Where("parent_id = ? AND name = ?", dir.ID, "Docker 总览").First(&row).Error; err != nil {
		t.Fatalf("查 Docker 总览菜单行: %v", err)
	}
	if row.Visible == nil || *row.Visible != entity.MenuVisible {
		t.Fatalf("总览菜单必须可见（visible），否则静默不进侧边栏: %+v", row)
	}
	if row.Status == nil || *row.Status != entity.MenuStatusEnabled {
		t.Fatalf("总览菜单必须启用（status）: %+v", row)
	}
}

// TestV016FailsWhenParentMissing 父目录不在时（例如从零库错序执行）必须**报错**，
// 而不是静默把菜单挂成顶级项 —— 顶级菜单与「服务监控」并列，看起来像产品设计
// 如此，没有报错就没人会发现。
func TestV016FailsWhenParentMissing(t *testing.T) {
	db := newDockerSeedTestDB(t)
	if err := seedDockerOverviewMenu(db); err == nil {
		t.Fatal("无「Docker 管理」目录时 seedDockerOverviewMenu 必须报错（不得静默置 parent_id=0）")
	}
	if got := countDockerMenus(t, db); got != 0 {
		t.Fatalf("失败后不得残留任何菜单行, got %d", got)
	}
}
