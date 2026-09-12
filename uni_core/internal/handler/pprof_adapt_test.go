package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// newPprofTestRouter 构造仅挂载 pprof 原始端点适配器的最小 gin 路由,
// 用于验证 AdaptPprof 的分派行为(与生产路由前缀一致)。
func newPprofTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/v1/monitor/debug/pprof/*any", AdaptPprof())
	r.POST("/api/v1/monitor/debug/pprof/*any", AdaptPprof())
	return r
}

// TestAdaptPprofDispatch 验证 Go 1.26 下 pprof.Index 不再分派的四个
// 特殊端点(cmdline/profile/symbol/trace)能被 AdaptPprof 显式路由到
// 标准库专用处理器,而不是落入 pprof.Lookup 兜底返回 404。
func TestAdaptPprofDispatch(t *testing.T) {
	r := newPprofTestRouter()

	cases := []struct {
		name         string
		method       string
		path         string
		body         string
		wantCode     int
		wantHeader   string // "Key: Contains"
		allowUnknown bool   // 该用例允许出现 "Unknown profile"(即确认走了 Lookup 兜底)
	}{
		{
			name:     "快照 goroutine 正常分派",
			method:   http.MethodGet,
			path:     "/api/v1/monitor/debug/pprof/goroutine?debug=0",
			wantCode: http.StatusOK,
		},
		{
			name:     "索引页可访问",
			method:   http.MethodGet,
			path:     "/api/v1/monitor/debug/pprof/",
			wantCode: http.StatusOK,
		},
		{
			name:     "cmdline 分派到专用处理器",
			method:   http.MethodGet,
			path:     "/api/v1/monitor/debug/pprof/cmdline",
			wantCode: http.StatusOK,
		},
		{
			name:       "profile 分派到 CPU 采集处理器",
			method:     http.MethodGet,
			path:       "/api/v1/monitor/debug/pprof/profile?seconds=1",
			wantCode:   http.StatusOK,
			wantHeader: `Content-Disposition: filename="profile"`,
		},
		{
			name:       "trace 分派到执行跟踪处理器",
			method:     http.MethodGet,
			path:       "/api/v1/monitor/debug/pprof/trace?seconds=1",
			wantCode:   http.StatusOK,
			wantHeader: `Content-Disposition: filename="trace"`,
		},
		{
			name:     "symbol 分派到专用处理器",
			method:   http.MethodPost,
			path:     "/api/v1/monitor/debug/pprof/symbol",
			body:     "0x1234",
			wantCode: http.StatusOK,
		},
		{
			name:         "未知快照名仍走 Lookup 兜底 404",
			method:       http.MethodGet,
			path:         "/api/v1/monitor/debug/pprof/no_such_profile",
			wantCode:     http.StatusNotFound,
			allowUnknown: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			if tc.body != "" {
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != tc.wantCode {
				t.Fatalf("%s %s = %d (body: %.100q), want %d",
					tc.method, tc.path, w.Code, w.Body.String(), tc.wantCode)
			}
			if tc.wantHeader != "" {
				parts := strings.SplitN(tc.wantHeader, ": ", 2)
				got := w.Header().Get(parts[0])
				if !strings.Contains(got, parts[1]) {
					t.Fatalf("header %s = %q, want contains %q", parts[0], got, parts[1])
				}
			}
			if tc.allowUnknown {
				// 反向断言:确认确实落入了 Lookup 兜底分支
				if !strings.Contains(w.Body.String(), "Unknown profile") {
					t.Fatalf("%s: 期望 Lookup 兜底 \"Unknown profile\", 实际 %.100q", tc.name, w.Body.String())
				}
			} else if strings.Contains(w.Body.String(), "Unknown profile") {
				t.Fatalf("%s: 响应落入 Lookup 兜底 \"Unknown profile\"", tc.name)
			}
		})
	}
}
