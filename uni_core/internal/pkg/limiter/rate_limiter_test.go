package limiter

import "testing"

// TestIsSkippedRoute_PrefixIndependent 是 P1 回归测试。
//
// 历史缺陷:skippedPaths 硬编码 "/api/v1/login",而 API 前缀是配置项
// (server.apiPrefix,router.Setup 与 swagger BasePath 都跟随它)。
// 把前缀改成 /api/v2 后登录接口不再被豁免,掉进全局限流且无任何报错。
// 本测试锁定"豁免与 API 前缀无关"。
func TestIsSkippedRoute_PrefixIndependent(t *testing.T) {
	for _, prefix := range []string{"/api/v1", "/api/v2", "/api", "", "/v3/api"} {
		for _, suffix := range []string{"/login", "/captcha/generate"} {
			full := prefix + suffix
			if !isSkippedRoute(full) {
				t.Errorf("isSkippedRoute(%q) = false, want true(前缀变更不应影响豁免)", full)
			}
		}
	}
}

// TestIsSkippedRoute_DoesNotOverMatch 反向约束:豁免只针对精确路由,
// 不能把以 /login 结尾的业务路由一并放过。
func TestIsSkippedRoute_DoesNotOverMatch(t *testing.T) {
	cases := []string{
		"/api/v1/users/login-history", // 以 -history 结尾,非 /login
		"/api/v1/xlogin",              // 无路径分隔符边界
		"/api/v1/login/audit",         // 更深的子路径,不是登录端点本身
		"/api/v1/notices",
	}
	for _, p := range cases {
		if isSkippedRoute(p) {
			t.Errorf("isSkippedRoute(%q) = true, want false(豁免范围过大)", p)
		}
	}
}
