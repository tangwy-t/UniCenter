package migrations

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 本文件守卫 v015 的 Docker 菜单与**前端插件路由表**的逐字一致（v012/v013 同款机制）。
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
// 7a 后本守卫覆盖**最终菜单面**（v015 直种子最终集，原 v016 的总览菜单已并入）：
// 总览 /docker、容器 /docker/containers、镜像与存储 /docker/resources、项目
// /docker/projects。images/volumes/networks 三个旧列表路由已在前端整体删除
// （不做 redirect），若有人把旧 path 加回路由表，本测试不会拦 —— 「旧 path 清零」
// 由前端 routes.test.ts 的源码扫描守卫负责。
//
// 隔离副本（scripts/release-precheck.sh 的 .tmp-iso/）里没有前端树：repoRoot 找不到
// 仓库根时自身 t.Skipf —— 这正是 v012/v013 守卫留下的先例（被比对的另一半不在这台
// 机器上，没有对象可比，跳过才是正确语义；全仓里跑时守卫照常生效）。
func TestV015DockerMenuPathsMatchFrontendRoutes(t *testing.T) {
	root := repoRoot(t)
	src := filepath.Join(root, "uni_console", "src", "modules", "docker", "index.ts")
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("读取前端路由文件 %s: %v", src, err)
	}
	text := string(b)

	// 先从前端路由表里取出「路由 path → 懒加载的 view 名」。用正则取字面量而不是引入
	// TS 解析器（与 v013 的 frontendRoutePath 同款）：迁移测试应当能独立跑，不依赖
	// 前端工具链。
	re := regexp.MustCompile(
		`path:\s*['"]([^'"]+)['"][\s\S]*?import\(['"]\./views/([A-Za-z0-9_-]+)\.vue['"]\)`)
	routes := map[string]string{}
	for _, m := range re.FindAllStringSubmatch(text, -1) {
		routes[m[1]] = m[2]
	}

	// 最终菜单面的四个 type=menu 定义（1 dir + 4 menu + 5 btn）。
	checked := 0
	for _, d := range dockerMenuDefinitions {
		if d.Type != "menu" {
			continue
		}
		view, ok := routes[d.Path]
		if !ok {
			t.Errorf("前端路由表里没有菜单 path %q（该菜单会在控制台静默消失）", d.Path)
			continue
		}
		// Component 是 `<module>/<view>`，前端清单里对应的字面量是
		// `() => import('./views/<view>.vue')` —— 后端的 `docker/containers` 这个
		// 拼法在前端源码里并不存在，故比对的是映射之后的结果（v012/v013 对同一条
		// 也是先做映射再比对：那边把 Component 解析成文件路径后 os.Stat）。
		// Component 不参与取组件，漂移了没有任何症状，会长期停在误导后来人的值上。
		if want := strings.TrimPrefix(d.Component, "docker/"); view != want {
			t.Errorf("菜单 %s（path %q）的 Component = %q 指向前端 views/%s.vue，但该路由加载的是 views/%s.vue",
				d.Key, d.Path, d.Component, want, view)
		}
		checked++
	}
	if checked != 4 {
		t.Fatalf("本迁移应恰有 4 个页面菜单（type=menu，7a 最终面），实得 %d", checked)
	}
}
