package migrations

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 本文件守卫 v013 两个菜单与**前端插件路由**的逐字一致（与 v012 同款机制）。
//
// 为什么必须有：后端菜单模式下 MenuProcessor 用菜单的 path 去前端路由表取组件 ——
//
//	const component = routeMap.get(menu.path)
//	if (!component) continue        // 取不到就整条菜单消失（不抛错、不打日志）
//
// path 差一个字符的唯一症状是「登录后侧边栏没有这一项」，控制台干净得像什么都没发生；
// 而两处代码在**不同仓库目录**（Go 种子 vs TS 路由）里，人眼 review 极易漏掉。
//
// 本测试直接读前端源文件做字面量比对，故它同时守住「有人在一边改了 path」。

// TestV013MenusMatchFrontendRoutes 两个新菜单的 path 必须与前端注册的路由逐字一致。
func TestV013MenusMatchFrontendRoutes(t *testing.T) {
	pairs := []struct {
		seedKey   string
		routeName string
	}{
		{"device:release", "DeviceRelease"},
		{"device:upgrade-task", "DeviceUpgradeTask"},
	}
	paths := seedMenuPaths(t)
	for _, p := range pairs {
		seedPath, ok := paths[p.seedKey]
		if !ok {
			t.Fatalf("v013 缺少菜单定义 %s", p.seedKey)
		}
		frontPath := frontendRoutePath(t, p.routeName)
		if seedPath != frontPath {
			t.Fatalf("菜单 %s 的 path 与前端路由不一致：\n  种子 = %q\n  前端 = %q\n"+
				"MenuProcessor 用菜单 path 取组件，不一致会让该菜单**静默消失**",
				p.seedKey, seedPath, frontPath)
		}
	}
}

// TestV013ComponentsMatchFrontendModules 菜单的 Component 字段必须能对应上前端文件。
//
// Component 不参与取组件（本项目用 path 取），故它漂移了**没有任何症状** ——
// 于是会长期停留在错误值上，误导后来读库的人（v012 对同一条也做了守卫）。
func TestV013ComponentsMatchFrontendModules(t *testing.T) {
	root := repoRoot(t)
	checked := 0
	for _, d := range agentUpgradeMenuDefinitions {
		if d.Component == "" {
			continue
		}
		parts := strings.SplitN(d.Component, "/", 2)
		if len(parts) != 2 {
			t.Fatalf("菜单 %s 的 Component = %q 形态不符（期望 <module>/<view>）", d.Key, d.Component)
		}
		vue := filepath.Join(root, "uni_console", "src", "modules", parts[0], "views", parts[1]+".vue")
		if _, err := os.Stat(vue); err != nil {
			t.Fatalf("菜单 %s 的 Component = %q 对应不到前端文件 %s: %v",
				d.Key, d.Component, vue, err)
		}
		checked++
	}
	if checked != 2 {
		t.Fatalf("本迁移应恰有 2 个带 Component 的菜单（Agent 版本 / 升级任务），实得 %d", checked)
	}
}

// seedMenuPaths 从本批定义里取出「菜单键 → path」。
func seedMenuPaths(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, d := range agentUpgradeMenuDefinitions {
		if d.Type == "menu" {
			out[d.Key] = d.Path
		}
	}
	return out
}

// frontendRoutePath 从前端插件清单里取出指定路由名的 path 字面量。
//
// 与 v012 的实现同款（正则找 `name: '<路由名>'` 之前最近的 path 字面量）：
// 不为一个字符串断言引入 TS 解析器 —— 迁移测试应当能独立跑，不依赖前端工具链。
func frontendRoutePath(t *testing.T, routeName string) string {
	t.Helper()
	root := repoRoot(t)
	src := filepath.Join(root, "uni_console", "src", "modules", "device", "index.ts")
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("读取前端路由文件 %s: %v", src, err)
	}
	text := string(b)
	nameIdx := strings.Index(text, "name: '"+routeName+"'")
	if nameIdx < 0 {
		t.Fatalf("%s 里找不到 name: '%s'（该路由未注册？）", src, routeName)
	}
	head := text[:nameIdx]
	re := regexp.MustCompile(`path:\s*'([^']+)'`)
	matches := re.FindAllStringSubmatch(head, -1)
	if len(matches) == 0 {
		t.Fatalf("%s 里 %s 之前找不到任何 path 字面量", src, routeName)
	}
	return matches[len(matches)-1][1]
}
