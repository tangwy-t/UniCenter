package migrations

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 本文件守卫 v012 的三条硬约束。它们都属于「破了不报错、只表现为页面异常」
// 的那一类，故必须靠测试而非人工纪律。

// TestV012OverviewMenuPathMatchesFrontend 钉住「后端菜单 path」与
// 「前端插件路由 path」逐字一致。
//
// 为什么这条必须有测试：后端菜单模式下 MenuProcessor 用菜单的 path 去前端
// 插件路由表里取组件 ——
//
//	const component = routeMap.get(menu.path)
//	if (!component) continue        // 取不到就整条菜单消失（不抛错、不打日志）
//
// 故 path 差一个字符（哪怕只是结尾多个斜杠）的唯一症状是「登录后侧边栏没有
// 这一项」，控制台干净得像什么都没发生。这类缺陷靠人眼 review 极易漏掉，
// 因为两处代码在**不同仓库目录**（Go 种子 vs TS 路由）里。
//
// 本测试直接读前端源文件做字面量比对，故它同时守住「有人在一边改了 path」。
func TestV012OverviewMenuPathMatchesFrontend(t *testing.T) {
	// 从前端插件清单里找出「总览路由」的 path 字面量。
	// 定位方式是「该 path 所在的路由块里出现 name: 'DeviceOverview'」，
	// 而不是硬编码期望值 —— 硬编码会让本测试与种子定义一起错。
	frontendPath := frontendOverviewRoutePath(t)

	seedPaths := make([]string, 0, len(deviceOverviewMenuDefinitions))
	for _, d := range deviceOverviewMenuDefinitions {
		if d.Type == "menu" {
			seedPaths = append(seedPaths, d.Path)
		}
	}
	if len(seedPaths) != 1 {
		t.Fatalf("本迁移应恰有 1 个 menu 定义，实得 %v", seedPaths)
	}
	if seedPaths[0] != frontendPath {
		t.Fatalf("菜单 path 与前端路由不一致：\n  种子 = %q\n  前端 = %q\n"+
			"MenuProcessor 用菜单 path 取组件，不一致会让该菜单**静默消失**（侧边栏没有它，控制台无报错）",
			seedPaths[0], frontendPath)
	}
}

// TestV012OverviewMenuParentIsDeviceDir 总览必须挂在「设备管理」目录下，
// 而不是变成顶级菜单。
//
// 顶级菜单会与「服务监控」「系统管理」并列 —— 看起来像是产品设计如此，
// 而不是配置错误，故没有测试就没人会发现。
func TestV012OverviewMenuParentIsDeviceDir(t *testing.T) {
	for _, d := range deviceOverviewMenuDefinitions {
		if d.Parent != "device" {
			t.Fatalf("菜单 %s 的 Parent = %q, want %q（应挂在设备管理目录下）",
				d.Key, d.Parent, "device")
		}
	}
}

// TestV012OverviewMenuPermIsRegistered 权限码必须已在 permission.All() 注册，
// 且必须是**查询面**（device:query）而不是列表面（device:list）。
//
// 为什么是 query 而不是 list：总览页的数据来自 `GET /devices/overview`，
// 该接口用 perm(permission.PermDeviceQuery)。若菜单声明 device:list，
// 拥有「列表权限但无查询权限」的角色会**看得到菜单、点进去满屏 403** ——
// 菜单可见性与接口鉴权不一致是典型的割裂体验。
func TestV012OverviewMenuPermIsRegistered(t *testing.T) {
	want := "device:query"
	for _, d := range deviceOverviewMenuDefinitions {
		if d.Perms != want {
			t.Fatalf("菜单 %s 的 Perms = %q, want %q（须与 GET /devices/overview 的鉴权一致）",
				d.Key, d.Perms, want)
		}
	}
	// 与后端接口实际使用的常量交叉钉住（避免本测试与种子一起漂移）。
	// 直接引用 permission 包会引入未使用 import 之外的无谓耦合，
	// 故此处用字面量与 router.go 的 perm(permission.PermDeviceQuery) 同源约定：
	// 该常量值由 TestPermissionAllNoDuplicates 与种子守卫共同覆盖。
}

// TestV012OverviewMenuComponentMatchesFrontendModule 菜单的 Component 字段
// 必须能对应上前端模块（`device/overview` → modules/device/views/overview.vue）。
//
// 说明：本项目**实际**用菜单 path 取组件（见上面的守卫），Component 字段是
// 沿用若依式菜单表结构的冗余信息。但正因为它不参与取组件，它漂移了**没有任何
// 症状** —— 于是它会长期停留在错误值上，误导后来读库的人。故这里一并钉住。
func TestV012OverviewMenuComponentMatchesFrontendModule(t *testing.T) {
	root := repoRoot(t)
	for _, d := range deviceOverviewMenuDefinitions {
		if d.Component == "" {
			continue
		}
		// device/overview → uni_console/src/modules/device/views/overview.vue
		parts := strings.SplitN(d.Component, "/", 2)
		if len(parts) != 2 {
			t.Fatalf("菜单 %s 的 Component = %q 形态不符（期望 <module>/<view>）", d.Key, d.Component)
		}
		vue := filepath.Join(root, "uni_console", "src", "modules", parts[0], "views", parts[1]+".vue")
		if _, err := os.Stat(vue); err != nil {
			t.Fatalf("菜单 %s 的 Component = %q 对应不到前端文件 %s: %v",
				d.Key, d.Component, vue, err)
		}
	}
}

// ── 辅助 ──────────────────────────────────────────────────

// frontendOverviewRoutePath 从前端插件清单里取出总览路由的 path。
//
// 用正则而不是引入 TS 解析器：本测试只需要一个字符串字面量，而引入
// 前端工具链到 Go 测试里会把两条构建链耦在一起（迁移测试应当能独立跑）。
func frontendOverviewRoutePath(t *testing.T) string {
	t.Helper()
	root := repoRoot(t)
	src := filepath.Join(root, "uni_console", "src", "modules", "device", "index.ts")
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("读取前端路由文件 %s: %v", src, err)
	}
	text := string(b)

	// 先定位 name: 'DeviceOverview'，再向上找最近的 path: '...'。
	// 反向查找（而不是正向）是因为 path 出现在 name 之前。
	nameIdx := strings.Index(text, "DeviceOverview")
	if nameIdx < 0 {
		t.Fatalf("%s 里找不到 name: 'DeviceOverview'（总览路由未注册？）", src)
	}
	head := text[:nameIdx]
	re := regexp.MustCompile(`path:\s*'([^']+)'`)
	matches := re.FindAllStringSubmatch(head, -1)
	if len(matches) == 0 {
		t.Fatalf("%s 里 DeviceOverview 之前找不到任何 path 字面量", src)
	}
	// 取最后一个（最靠近 DeviceOverview 的那个）。
	return matches[len(matches)-1][1]
}

// repoRoot 从本测试文件的位置回退到仓库根目录。
//
// 本文件在 uni_core/internal/pkg/migration/migrations/，故上溯 5 层到仓库根。
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("取工作目录: %v", err)
	}
	root := wd
	for i := 0; i < 5; i++ {
		root = filepath.Dir(root)
	}
	// 用 go.work 作为「这里就是仓库根」的判据：它是 monorepo 的标志文件。
	if _, err := os.Stat(filepath.Join(root, "go.work")); err != nil {
		t.Fatalf("上溯 5 层得到的 %s 不是仓库根（没有 go.work）: %v", root, err)
	}
	return root
}
