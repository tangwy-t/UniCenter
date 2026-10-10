package router

import (
	"os"
	"strings"
	"testing"
)

// 全局跨资源检索（4d）路由的**挂载 + 权限**守卫。
//
// 为什么需要一条源码级断言（与 TestStatsHistoryRouteMountedWithInspectPerm 同型）：
// 路由级 perm 是中间件层的事 —— handler 层的测试挂在裸引擎上、路径权限照旧全绿。
// 这条端点的权限档是 docker:list（与读面其余端点同档：它是「读」的全局入口，
// 只是把检索面从「一页一类」提到跨主机跨类型）。丢了 perm 就等于把全部主机上的
// 容器/镜像/卷/网络/项目名向任何登录用户敞开。
func TestDockerSearchRouteMountedWithListPerm(t *testing.T) {
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("读取 router.go 失败: %v", err)
	}
	text := string(src)

	const want = `docker.GET("/search", perm(permission.PermDockerList), deps.Docker.Hdl.Search)`
	if !strings.Contains(text, want) {
		t.Fatalf("全局跨资源检索必须以 docker:list 挂在 auth 组上（当前缺失或权限档漂移），期待:\n%s", want)
	}
	// 不得挂在 api 组（那会绕开 JWT + 权限）：auth 组之外的 `/docker/search` 一律红。
	const offAuth = `api.GET("/docker/search"`
	if strings.Contains(text, offAuth) {
		t.Fatalf("全局跨资源检索不得挂在 api 组（绕开 JWT/权限）")
	}
}
