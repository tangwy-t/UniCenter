package migrations

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 本文件守卫 v016 的「Docker 总览」菜单与**前端插件路由表**的逐字一致（v012/v013/v015
// 同款机制）。它是 v016 落地时留下的最后一块拼图：种子注释（v016_seed_docker_overview.go
// 开头的「前端依赖」）明确要求前端控制塔页面落地时补上这条守卫 —— 在那之前本文件不存在，
// 侧边栏也看不到该菜单（菜单模式下 MenuProcessor 取不到组件就整条静默消失）。
//
// 为什么必须有：后端菜单模式下 MenuProcessor 用菜单的 path 去前端路由表取组件 ——
//
//	const component = routeMap.get(menu.path)
//	if (!component) continue        // 取不到就整条菜单消失（不抛错、不打日志）
//
// path 差一个字符的唯一症状是「登录后侧边栏没有这一项」，控制台干净得像什么都没发生；
// 而两处代码在**不同仓库目录**（Go 种子 vs TS 路由）里，人眼 review 极易漏掉。
//
// 本测试直接读前端源文件比对，故它同时守住「有人在一边改了 path / 换了页面文件」。
//
// 隔离副本（scripts/release-precheck.sh 的 .tmp-iso/）里没有前端树：repoRoot 找不到
// 仓库根时自身 t.Skipf —— 与 v012/v013/v015 守卫同一先例（被比对的另一半不在这台
// 机器上，没有对象可比，跳过才是正确语义；全仓里跑时守卫照常生效）。
func TestV016OverviewMenuPathMatchesFrontendRoute(t *testing.T) {
	root := repoRoot(t)
	src := filepath.Join(root, "uni_console", "src", "modules", "docker", "index.ts")
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("读取前端路由文件 %s: %v", src, err)
	}
	text := string(b)

	// 先从前端路由表里取出「路由 path → 懒加载的 view 名」。用正则取字面量而不是引入
	// TS 解析器（与 v013/v015 的同款取舍）：迁移测试应当能独立跑，不依赖前端工具链。
	re := regexp.MustCompile(
		`path:\s*['"]([^'"]+)['"][\s\S]*?import\(['"]\./views/([A-Za-z0-9_-]+)\.vue['"]\)`)
	routes := map[string]string{}
	for _, m := range re.FindAllStringSubmatch(text, -1) {
		routes[m[1]] = m[2]
	}

	checked := 0
	for _, d := range dockerOverviewMenuDefinitions {
		if d.Type != "menu" {
			continue
		}
		view, ok := routes[d.Path]
		if !ok {
			t.Errorf("前端路由表里没有菜单 path %q（该菜单会在控制台静默消失）", d.Path)
			continue
		}
		// Component 是 `<module>/<view>`，与 v015 同一条：它不参与取组件，漂移了没有
		// 任何症状，故比对「映射之后的结果」钉住它，别让它在库里误导后来人。
		if want := strings.TrimPrefix(d.Component, "docker/"); view != want {
			t.Errorf("菜单 %s（path %q）的 Component = %q 指向前端 views/%s.vue，但该路由加载的是 views/%s.vue",
				d.Key, d.Path, d.Component, want, view)
		}
		checked++
	}
	if checked != 1 {
		t.Fatalf("本迁移应恰有 1 个总览菜单（type=menu），实得 %d", checked)
	}
}
