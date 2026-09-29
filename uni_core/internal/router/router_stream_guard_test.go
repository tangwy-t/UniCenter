package router

import (
	"os"
	"strings"
	"testing"
)

// 终端流端点的**挂载位置**守卫。
//
// 为什么需要一条源码级断言：浏览器的 WebSocket **带不了 Authorization 头**，
// 终端的凭据是 result 里签发的一次性 ticket（spec §4.1）。把这条路由挪进 auth 组，
// JWT 中间件会在握手阶段把它拦成 401（正文「未登录或 token 已过期」而不是票据结论），
// 页面表现是「终端永远连不上」，而 handler 层的测试全都挂在裸引擎上、**照样全绿**
// —— 2026-09-28 生产联调正是这么踩到的（日志流正常、终端 401）。
//
// 与 /agent/ws、/agent/releases/:version/download 同型：那两处的教训已经写在
// router.go 的注释里，这条测试是给终端流补上同一道防线。
//
// 断言的是**分组归属**而不是装配结果：真装配需要整套依赖（DB/Redis/agenthub），
// 那会让这条守卫变得又重又脆；而回归的形态只有一个 —— 有人把这一行挪回 auth 组内。
func TestExecStreamRouteIsRegisteredOutsideAuthGroup(t *testing.T) {
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("读取 router.go 失败: %v", err)
	}
	text := string(src)

	const onAPIGroup = `api.GET("/docker/hosts/:id/stream/exec", deps.Docker.Hdl.ExecStream)`
	const onAuthGroup = `docker.GET("/hosts/:id/stream/exec", deps.Docker.Hdl.ExecStream)`

	if !strings.Contains(text, onAPIGroup) {
		t.Fatalf("终端流必须以 %s 的形式挂在 api 组上（当前缺失）", onAPIGroup)
	}
	if strings.Contains(text, onAuthGroup) {
		t.Fatalf("终端流不得回到 auth 组（%s）—— 浏览器 WebSocket 带不了 Authorization 头，会被 JWT 中间件拦成 401", onAuthGroup)
	}

	// 日志流相反：它走 fetch + ReadableStream，**能**带 Authorization 头，故留在 auth 组。
	const logStreamOnAuth = `docker.GET("/hosts/:id/cmds/:ref/stream", deps.Docker.Hdl.LogStream)`
	if !strings.Contains(text, logStreamOnAuth) {
		t.Fatalf("日志流应留在 auth 组上（%s）", logStreamOnAuth)
	}
}
