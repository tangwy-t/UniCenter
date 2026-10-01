package router

import (
	"os"
	"strings"
	"testing"
)

// stats-history 读面路由的**挂载 + 权限**守卫。
//
// 为什么需要一条源码级断言（与 TestEventsStreamRouteMountedWithPerm 同型）：
// 路由级 perm 是中间件层的事 —— handler 层的测试挂在裸引擎上、路径权限照旧
// 全绿。这条端点的权限档是 docker:inspect（与 stats 实时流同档：两处看的是
// 同一个容器的同一类读数）；谁把它挪成 docker:list（读面其余端点的档），就
// 等于把「能列主机清单」变成「能读任意容器的 CPU/内存历史」—— 只有读面权限
// 的人不该顺带拿到这份观测数据。丢了 perm 或降了档，这条测试红。
func TestStatsHistoryRouteMountedWithInspectPerm(t *testing.T) {
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("读取 router.go 失败: %v", err)
	}
	text := string(src)

	const want = `docker.GET("/hosts/:id/containers/:cid/stats-history", perm(permission.PermDockerInspect), deps.Docker.Hdl.StatsHistory)`
	if !strings.Contains(text, want) {
		t.Fatalf("stats-history 必须以 docker:inspect 挂在 auth 组上（当前缺失或权限档漂移），期待:\n%s", want)
	}
	// 不得出现降档形态（docker:list）：降档的失败形态就是一行之差，逐字盯住。
	const degraded = `docker.GET("/hosts/:id/containers/:cid/stats-history", perm(permission.PermDockerList)`
	if strings.Contains(text, degraded) {
		t.Fatalf("stats-history 不得降到 docker:list —— 只有读面权限的人不该顺带拿到容器观测数据")
	}
}
