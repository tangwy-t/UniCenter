package migrations

import (
	"testing"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/permission"
)

// 本文件守卫 v013 的三条硬约束。与 v012 的守卫同款：它们都属于
// 「破了不报错、只表现为页面异常或权限割裂」的那一类，故必须靠测试。
//
// 前端侧守卫（菜单 path / Component 与 uni_console/src/modules/device/index.ts
// 逐字一致）与前端路由**同批提交**（阶段 E）—— 那之前前端还没有这两个路由，
// 提前写只有两种结果：测试红，或断言一个不存在的东西。

// TestV013MenusHangUnderDeviceDir 两个新页面必须挂在「设备管理」目录下，
// 而不是变成顶级菜单（顶级会与「服务监控」「系统管理」并列，看起来像产品设计如此）。
func TestV013MenusHangUnderDeviceDir(t *testing.T) {
	menus := 0
	for _, d := range agentUpgradeMenuDefinitions {
		if d.Type != "menu" {
			continue
		}
		menus++
		if d.Parent != "device" {
			t.Fatalf("菜单 %s 的 Parent = %q, want %q", d.Key, d.Parent, "device")
		}
	}
	if menus != 2 {
		t.Fatalf("本迁移应恰有 2 个 menu 定义（Agent 版本 / 升级任务），实得 %d", menus)
	}
}

// TestV013ButtonParentsResolve 每个按钮的父键都必须在本批定义里出现过
// （先父后子书写），或指向两个既有的菜单键。
//
// 漏了父键的后果：种子里 idByKey 解析失败 → 直接报错回滚（本测试把它提前到
// 写代码时发现，而不是等某台机器上跑迁移时失败）。
func TestV013ButtonParentsResolve(t *testing.T) {
	known := map[string]bool{"device": true, "device:index": true}
	for _, d := range agentUpgradeMenuDefinitions {
		if d.Type == "btn" && !known[d.Parent] {
			t.Fatalf("按钮 %s 的父 %q 未解析（父必须先于子书写，或父是既有菜单键）", d.Key, d.Parent)
		}
		// 本切片按先父后子书写：正向遍历时父键必须已经出现过。
		known[d.Key] = true
	}
}

// TestV013PermsAreRegistered 权限码必须已在 permission.All() 注册。
//
// 反向（All() 里每个码都被某菜单引用）由 v002_seed_menu_perm_test 统一覆盖 ——
// 前提是本批定义已追加进 menuDefBatches（seed_registry.go），
// 否则它被静默漏扫，新码会被判成死常量。
func TestV013PermsAreRegistered(t *testing.T) {
	all := map[string]bool{}
	for _, code := range permission.All() {
		all[code] = true
	}
	for _, d := range agentUpgradeMenuDefinitions {
		if d.Perms == "" {
			t.Fatalf("菜单 %s 没有权限码", d.Key)
		}
		if !all[d.Perms] {
			t.Fatalf("菜单 %s 的权限码 %q 未注册进 permission.All()", d.Key, d.Perms)
		}
	}
	// 六个新码必须都被引用到（漏一个就成死常量）。
	want := []string{
		permission.PermDeviceUpgrade,
		permission.PermDeviceUpgradeGlobal,
		permission.PermDeviceReleaseList,
		permission.PermDeviceReleaseUpload,
		permission.PermDeviceReleasePublish,
		permission.PermDeviceReleaseDelete,
	}
	referenced := map[string]bool{}
	for _, d := range agentUpgradeMenuDefinitions {
		referenced[d.Perms] = true
	}
	for _, code := range want {
		if !referenced[code] {
			t.Fatalf("权限码 %q 没有任何菜单引用（会被权限双向守卫判为死常量）", code)
		}
	}
}

// TestV013ConfigDefaultsAreSafe 两个配置键的**默认值**是安全相关的：
//
//   - 全站目标版本默认必须为空（空 = 关闭）。一旦默认有值，一次迁移就把所有设备
//     （含之后新注册的）点了自动升级 —— 这是不可逆的产品行为，不能有默认。
//   - 上传上限必须显著大于文件模块的默认 10MB：静态 Go 二进制 15–25MB，
//     沿用文件模块上限会让上传直接 400。
func TestV013ConfigDefaultsAreSafe(t *testing.T) {
	byKey := map[string]configDef{}
	for _, def := range agentUpgradeConfigDefinitions {
		byKey[def.Key] = def
	}
	tv, ok := byKey["sys.agent.targetVersion"]
	if !ok {
		t.Fatal("缺少 sys.agent.targetVersion")
	}
	if tv.Value != "" {
		t.Fatalf("全站目标版本默认值必须为空（空=关闭），实得 %q", tv.Value)
	}
	ms, ok := byKey["sys.agent.release.maxSize"]
	if !ok {
		t.Fatal("缺少 sys.agent.release.maxSize")
	}
	if ms.Value != "209715200" {
		t.Fatalf("发布物上传上限默认应为 209715200（200MB），实得 %q", ms.Value)
	}
}
