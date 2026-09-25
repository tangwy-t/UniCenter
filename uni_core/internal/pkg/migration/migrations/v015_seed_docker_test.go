package migrations

import (
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/permission"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/util"
)

// 菜单与配置键的**存在性**守卫（DB 无关的部分）：定义列表本身的完整性。
//
// 真正落库的验证在既有 v002_seed_menu_perm_test（权限双向）与集成测试里；
// 这里守的是「定义写对了」这件事，让错误在单测阶段就暴露。
func TestDockerMenuDefinitionsAreComplete(t *testing.T) {
	// 五个列表页 + 一个目录
	var menus, dirs, btns int
	keys := map[string]bool{}
	for _, d := range dockerMenuDefinitions {
		if keys[d.Key] {
			t.Fatalf("重复的菜单键 %q（同键会让后一条覆盖前一条的 id 解析）", d.Key)
		}
		keys[d.Key] = true
		switch d.Type {
		case "dir":
			dirs++
		case "menu":
			menus++
			if !strings.HasPrefix(d.Path, "/docker/") {
				t.Fatalf("菜单 %s 的 path 必须是 /docker/ 前缀（与前端路由逐字一致）", d.Key)
			}
			if d.Component == "" || d.Component != strings.TrimPrefix(d.Path, "/") {
				t.Fatalf("菜单 %s 的 Component(%q) 必须等于去掉前导 / 的 Path(%q)", d.Key, d.Component, d.Path)
			}
		case "btn":
			btns++
			if d.Perms == "" {
				t.Fatalf("按钮 %s 必须承载一个权限码（否则它是死节点）", d.Key)
			}
		default:
			t.Fatalf("未知菜单类型 %q", d.Type)
		}
	}
	if dirs != 1 || menus != 5 {
		t.Fatalf("应为 1 个目录 + 5 个列表页，实际 %d + %d", dirs, menus)
	}
	if btns != 5 {
		t.Fatalf("应为 5 个按钮权限节点，实际 %d", btns)
	}
	// 六个权限码必须全部被引用（与 v002 守卫同口径，这里做一次快速自检）
	used := map[string]bool{}
	for _, d := range dockerMenuDefinitions {
		if d.Perms != "" {
			used[d.Perms] = true
		}
	}
	for _, code := range []string{
		permission.PermDockerList, permission.PermDockerInspect, permission.PermDockerManage,
		permission.PermDockerDelete, permission.PermDockerConfig, permission.PermDockerExec,
	} {
		if !used[code] {
			t.Fatalf("权限码 %s 没有菜单/按钮承载（v002 双向守卫会判为死常量）", code)
		}
	}
}

// 默认保护清单必须包含底座（它一旦为空，uni-center 自己的容器就成了可删对象）。
func TestDockerProtectedDefaultCoversBase(t *testing.T) {
	var v string
	for _, d := range dockerConfigDefinitions {
		if d.Key == "sys.docker.protected" {
			v = d.Value
		}
	}
	for _, must := range []string{"uni-center-core", "uni-center-console", "mysql", "redis",
		"project:uni-center", "volume:uni-center-uploads"} {
		if !strings.Contains(v, must) {
			t.Fatalf("默认保护清单缺 %q：%q", must, v)
		}
	}
	// 快照周期的默认值必须在协议允许区间内（<10 会被协议层拒）
	for _, d := range dockerConfigDefinitions {
		if d.Key == "sys.docker.snapshotInterval" && d.Value != "30" {
			t.Fatalf("快照周期默认值应为 30，实际 %q", d.Value)
		}
	}
}

// ── 落库/重放守卫（复用同包 v009 的 sqlite 夹具）────────────────────────

// newDockerSeedTestDB 在 v009 的内存库夹具上补出 sys_menu 表。
//
// 复用 newAgentSeedTestDB（v009_seed_agent_jobs_test.go）：它已注册生产同款
// 雪花/审计回调 —— 菜单 ID 靠雪花回调生成，缺了它第二行就撞主键。
func newDockerSeedTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newAgentSeedTestDB(t)
	if err := db.AutoMigrate(&entity.SysMenu{}); err != nil {
		t.Fatalf("AutoMigrate sys_menu: %v", err)
	}
	return db
}

func countDockerMenus(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&entity.SysMenu{}).Count(&n).Error; err != nil {
		t.Fatalf("count sys_menu: %v", err)
	}
	return n
}

func countDockerConfigRows(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&entity.SysConfig{}).
		Where("config_key LIKE ?", "sys.docker.%").Count(&n).Error; err != nil {
		t.Fatalf("count sys_config: %v", err)
	}
	return n
}

// TestV015ReplayIsIdempotent 同一个库连跑两次 seedDocker：
//
//   - 第一次：全新落地，行数必须与定义数一致；
//   - 第二次：**所有**菜单/配置都走查重命中分支 —— 若命中时只 `continue` 而不把
//     既有行 ID 回填进 idByKey，父节点被跳过 → 子节点报「父尚未创建」→ 迁移失败
//     回滚，部署被挡。这正是本测试要钉住的那条路径。
//
// 叠加的真实场景：从备份恢复 + 重启（sys_migration 落后于实际数据）会让同一 Up
// 在语义上重放。
func TestV015ReplayIsIdempotent(t *testing.T) {
	db := newDockerSeedTestDB(t)

	if err := seedDocker(db); err != nil {
		t.Fatalf("首次 seedDocker: %v", err)
	}
	firstMenus := countDockerMenus(t, db)
	firstConfigs := countDockerConfigRows(t, db)
	if firstMenus != int64(len(dockerMenuDefinitions)) {
		t.Fatalf("首次落地菜单行数 = %d, want %d", firstMenus, len(dockerMenuDefinitions))
	}
	if firstConfigs != int64(len(dockerConfigDefinitions)) {
		t.Fatalf("首次落地 sys.docker.* 行数 = %d, want %d", firstConfigs, len(dockerConfigDefinitions))
	}

	if err := seedDocker(db); err != nil {
		t.Fatalf("重放 seedDocker 必须无错（查重命中时父键仍须可解析）: %v", err)
	}
	if got := countDockerMenus(t, db); got != firstMenus {
		t.Fatalf("重放后菜单行数 = %d, want %d（重复插入会让侧边栏出现两条同名菜单）", got, firstMenus)
	}
	if got := countDockerConfigRows(t, db); got != firstConfigs {
		t.Fatalf("重放后 sys.docker.* 行数 = %d, want %d", got, firstConfigs)
	}

	// 父 ID 必须指向真实存在的行：dir 为 0，其余必须命中本批的某个父菜单
	//（悬空 ID 的症状是侧边栏整棵子树消失，且不报错）。
	var rows []entity.SysMenu
	if err := db.Find(&rows).Error; err != nil {
		t.Fatalf("find menus: %v", err)
	}
	byID := make(map[uint64]string, len(rows))
	for _, r := range rows {
		byID[r.ID] = r.Name
	}
	for _, r := range rows {
		if r.ParentID == nil {
			t.Fatalf("菜单 %s 的 parent_id 为空（应显式写 0 或父 ID）", r.Name)
		}
		if r.Type == "dir" {
			if *r.ParentID != 0 {
				t.Fatalf("目录 %s 的 parent_id = %d, want 0", r.Name, *r.ParentID)
			}
			continue
		}
		if byID[*r.ParentID] == "" {
			t.Fatalf("菜单 %s 的 parent_id = %d 指向不存在的父行", r.Name, *r.ParentID)
		}
	}
}

// TestV015DirDedupIgnoresForeignRootDirs 钉住 dir 的查重口径。
//
// 真实库里根目录不止一个（v002 的「服务监控」等 dir 的 perms 也是**空串**）。
// 若 dir 沿用 menuExists 的「Path 为空 → 比 perms」分支，查重会命中别人的目录
// → docker 目录永不创建 → 5 个子菜单紧接着报「父节点 docker 尚未创建」→ 整条
// 迁移失败。本测试先播一个外来根目录，再跑 seedDocker。
func TestV015DirDedupIgnoresForeignRootDirs(t *testing.T) {
	db := newDockerSeedTestDB(t)

	foreign := entity.SysMenu{
		ParentID: util.Ptr(uint64(0)),
		Name:     "服务监控",
		Type:     "dir",
		Perms:    util.Ptr(""),
		Visible:  util.Ptr[int8](entity.MenuVisible),
		Status:   util.Ptr[int8](entity.MenuStatusEnabled),
	}
	if err := db.Create(&foreign).Error; err != nil {
		t.Fatalf("播外来根目录: %v", err)
	}

	if err := seedDocker(db); err != nil {
		t.Fatalf("seedDocker: %v", err)
	}
	var n int64
	if err := db.Model(&entity.SysMenu{}).
		Where("parent_id = 0 AND name = ? AND type = ?", "Docker 管理", "dir").
		Count(&n).Error; err != nil {
		t.Fatalf("count docker dir: %v", err)
	}
	if n != 1 {
		t.Fatalf("docker 目录行数 = %d, want 1（dir 查重不得被外来根目录的空 perms 命中）", n)
	}
	if got := countDockerMenus(t, db); got != int64(len(dockerMenuDefinitions))+1 {
		t.Fatalf("菜单总行数 = %d, want %d（外来根目录 + 本批定义）", got, len(dockerMenuDefinitions)+1)
	}
}
