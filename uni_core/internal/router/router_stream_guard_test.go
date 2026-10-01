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

// 事件聚合流的**挂载位置 + 权限**守卫。
//
// 事件流走 fetch + ReadableStream（浏览器能带 Authorization），必须像日志/stats
// 一样挂在 auth 组；且它是总览页活动流的数据源（读面），权限必须是**静态的**
// docker:list（与 /docker/overview 同档）—— 处理器内没有「按指令校验归属」这一步
// （聚合流没有发起人），路由丢了 perm 就等于把全部主机的活动流向任何登录用户敞开。
// 形态与 TestExecStreamRouteIsRegisteredOutsideAuthGroup 相同：源码级断言，
// 因为 handler 层的测试挂裸引擎、路径权限照旧全绿。
func TestEventsStreamRouteMountedWithPerm(t *testing.T) {
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("读取 router.go 失败: %v", err)
	}
	text := string(src)

	const want = `docker.GET("/events", perm(permission.PermDockerList), deps.Docker.Hdl.EventsStream)`
	if !strings.Contains(text, want) {
		t.Fatalf("事件聚合流必须以 %s 挂在 auth 组并带静态 perm(docker:list)（当前缺失）", want)
	}
	const offAuth = `api.GET("/docker/events"`
	if strings.Contains(text, offAuth) {
		t.Fatalf("事件聚合流不得挂进 api 组（%s）—— 它走 fetch + ReadableStream，凭据是 JWT", offAuth)
	}
}

// 凭据管理路由的**静态权限**守卫（4c）。
//
// 凭据是「分发」支柱的密钥材料（能解出私有镜像），CRUD 的权限码必须是
// docker:config —— 六档里的最高管理档（与配置编辑/回滚同档，见
// pkg/permission 的 PermDockerConfig 注释）。漏挂 perm 等于把「往任意主机
// 下发任意仓库凭据」的能力向 docker:list 的只读用户敞开：source 级断言，
// 与 TestEventsStreamRouteMountedWithPerm 同款理由（handler 层测试挂裸引擎）。
func TestRegistryRoutesMountedWithConfigPerm(t *testing.T) {
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("读取 router.go 失败: %v", err)
	}
	text := string(src)

	wants := []string{
		`docker.GET("/registries", perm(permission.PermDockerConfig), deps.Docker.RegistryHdl.List)`,
		`docker.POST("/registries", perm(permission.PermDockerConfig), deps.Docker.RegistryHdl.Create)`,
		`docker.PUT("/registries", perm(permission.PermDockerConfig), deps.Docker.RegistryHdl.Update)`,
		`docker.DELETE("/registries/:registry", perm(permission.PermDockerConfig), deps.Docker.RegistryHdl.Delete)`,
	}
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Fatalf("凭据路由必须以 %s 挂在 auth 组并带静态 perm(docker:config)（当前缺失）", want)
		}
	}
}

// 任务中心路由的**挂载位置 + 权限**守卫（6b）。
//
// 任务中心是读面（跨主机最近指令），权限必须是**静态的** docker:list（与
// /docker/overview、/docker/containers 同档）—— 处理器内没有「按指令校验归属」
// 这一步（任务是跨主机的治理视图，不是发起人自己的轮询视图）。漏挂 perm 等于
// 把「谁在哪台主机上执行过什么」向任何登录用户敞开。
// 形态与 TestEventsStreamRouteMountedWithPerm 相同：源码级断言，因为 handler 层
// 的测试挂裸引擎、路径权限照旧全绿（真实的 403 判定由
// handler.TestTasksRoutePermGuard 用真守卫钉住 —— 两条测试各钉一半）。
func TestTasksRouteMountedWithListPerm(t *testing.T) {
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("读取 router.go 失败: %v", err)
	}
	text := string(src)

	const want = `docker.GET("/tasks", perm(permission.PermDockerList), deps.Docker.Hdl.Tasks)`
	if !strings.Contains(text, want) {
		t.Fatalf("任务中心必须以 %s 挂在 auth 组并带静态 perm(docker:list)（当前缺失）", want)
	}
	const offAuth = `api.GET("/docker/tasks"`
	if strings.Contains(text, offAuth) {
		t.Fatalf("任务中心不得挂进 api 组（%s）—— JWT 登录态是它的身份边界", offAuth)
	}
}

// 构建/推送进度流端点的**挂载位置**守卫（P2）。
//
// build/push 进度与 pull/stats/日志同族：走 fetch + ReadableStream（浏览器能带
// Authorization 头），必须**留在 auth 组**（api 组连 JWT 都不认，装了等于对任何
// 人开放）；句柄取协议派生的 build_<ref>/push_<ref>（受理时预登记）。与
// TestExecStreamRouteIsRegisteredOutsideAuthGroup 同款源码级断言理由：handler
// 层测试挂裸引擎，挂错组照旧全绿。
func TestBuildPushStreamRoutesStayOnAuthGroup(t *testing.T) {
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("读取 router.go 失败: %v", err)
	}
	text := string(src)

	const buildOnAuth = `docker.GET("/hosts/:id/cmds/:ref/build", deps.Docker.Hdl.BuildStream)`
	const pushOnAuth = `docker.GET("/hosts/:id/cmds/:ref/push", deps.Docker.Hdl.PushStream)`
	if !strings.Contains(text, buildOnAuth) {
		t.Fatalf("构建进度流应留在 auth 组上（%s）", buildOnAuth)
	}
	if !strings.Contains(text, pushOnAuth) {
		t.Fatalf("推送进度流应留在 auth 组上（%s）", pushOnAuth)
	}
	if strings.Contains(text, `api.GET("/docker/hosts/:id/cmds/:ref/build"`) {
		t.Fatal("构建进度流不得挂进 api 组 —— 它走 fetch + ReadableStream，凭据是 JWT")
	}
	if strings.Contains(text, `api.GET("/docker/hosts/:id/cmds/:ref/push"`) {
		t.Fatal("推送进度流不得挂进 api 组 —— 它走 fetch + ReadableStream，凭据是 JWT")
	}
}
