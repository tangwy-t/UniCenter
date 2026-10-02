package middleware

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/contextkeys"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/datascope"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
)

// captureOpLogService captures operation log entries created by the middleware.
type captureOpLogService struct {
	logs chan *entity.SysOperationLog
}

func (s *captureOpLogService) Create(ctx context.Context, log *entity.SysOperationLog) error {
	s.logs <- log
	return nil
}

// ctxCaptureOpLogService 额外捕获 Create 收到的 ctx,用于断言异步落库
// 是否携带请求上下文中的 TraceID 与 ScopeContext。
type ctxCaptureOpLogService struct {
	ctx chan context.Context
}

func (s *ctxCaptureOpLogService) Create(ctx context.Context, _ *entity.SysOperationLog) error {
	s.ctx <- ctx
	return nil
}

// setupOpLogRouter builds a test engine with the same middleware order as production:
// Recovery (outer) → OperationLogMiddleware → route handler.
func setupOpLogRouter() (*gin.Engine, *captureOpLogService) {
	gin.SetMode(gin.TestMode)
	svc := &captureOpLogService{logs: make(chan *entity.SysOperationLog, 1)}
	log := logger.NewNop()

	r := gin.New()
	r.Use(Recovery(log))
	r.Use(OperationLogMiddleware(svc, log))
	return r, svc
}

// waitLog reads one captured log entry, failing the test on timeout (save is async).
func waitLog(t *testing.T, svc *captureOpLogService) *entity.SysOperationLog {
	t.Helper()
	select {
	case entry := <-svc.logs:
		return entry
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for operation log entry")
		return nil
	}
}

// TestOperationLogMiddlewareRecordsSuccess verifies a successful envelope
// (HTTP 200 + code 0) is stored with code 0 and no error message.
func TestOperationLogMiddlewareRecordsSuccess(t *testing.T) {
	r, svc := setupOpLogRouter()
	r.POST("/ok", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"code": 0, "msg": "success", "data": nil})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/ok", strings.NewReader(`{"name":"test"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", w.Code)
	}

	entry := waitLog(t, svc)
	if entry.Code != apperror.CodeOK {
		t.Fatalf("expected code %d, got %d", apperror.CodeOK, entry.Code)
	}
	if entry.ErrorMsg != nil {
		t.Fatalf("expected nil errorMsg for success, got %q", *entry.ErrorMsg)
	}
	if entry.RequestParams == nil || !strings.Contains(*entry.RequestParams, `"name"`) {
		t.Fatalf("expected request params to be recorded, got %v", entry.RequestParams)
	}
}

// TestOperationLogMiddlewareRecordsBusiness400 verifies that a handler error
// (HTTP 400 envelope with business code) stores the business code and message.
func TestOperationLogMiddlewareRecordsBusiness400(t *testing.T) {
	r, svc := setupOpLogRouter()
	r.POST("/fail", func(c *gin.Context) {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40000, "msg": "参数错误", "data": nil})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/fail", strings.NewReader(`{"name":"test"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400, got %d", w.Code)
	}

	entry := waitLog(t, svc)
	if entry.Code != apperror.CodeBadRequest {
		t.Fatalf("expected code %d, got %d", apperror.CodeBadRequest, entry.Code)
	}
	if entry.ErrorMsg == nil || *entry.ErrorMsg != "参数错误" {
		t.Fatalf("expected errorMsg 参数错误, got %v", entry.ErrorMsg)
	}
	if entry.RequestMethod == nil || *entry.RequestMethod != http.MethodPost {
		t.Fatalf("expected POST request method, got %v", entry.RequestMethod)
	}
}

// TestOperationLogMiddlewareBusinessCodeWinsOverHTTPStatus verifies the core
// guarantee for this feature: the business code in the envelope decides the
// result even when the HTTP status is 200 (the framework's common case).
func TestOperationLogMiddlewareBusinessCodeWinsOverHTTPStatus(t *testing.T) {
	r, svc := setupOpLogRouter()
	r.POST("/fail-http200", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"code": 40000, "msg": "参数错误", "data": nil})
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/fail-http200", strings.NewReader(`{"x":1}`)))

	if w.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", w.Code)
	}

	entry := waitLog(t, svc)
	if entry.Code != apperror.CodeBadRequest {
		t.Fatalf("expected code %d (business code wins over HTTP 200), got %d", apperror.CodeBadRequest, entry.Code)
	}
	if entry.ErrorMsg == nil || *entry.ErrorMsg != "参数错误" {
		t.Fatalf("expected errorMsg 参数错误, got %v", entry.ErrorMsg)
	}
}

// TestOperationLogMiddlewareRecordsPanic verifies that a panic in the handler
// (500 via Recovery) is recorded with code 50000 and the panic information,
// and that the client still receives the 500 response from Recovery.
func TestOperationLogMiddlewareRecordsPanic(t *testing.T) {
	r, svc := setupOpLogRouter()
	r.POST("/boom", func(c *gin.Context) {
		panic("boom")
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/boom", strings.NewReader(`{"x":1}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected HTTP 500 from Recovery, got %d", w.Code)
	}

	entry := waitLog(t, svc)
	if entry.Code != apperror.CodeInternal {
		t.Fatalf("expected code %d, got %d", apperror.CodeInternal, entry.Code)
	}
	// The response body is written by Recovery after the panic, so errorMsg
	// should carry the panic information captured by the middleware.
	if entry.ErrorMsg == nil || !strings.Contains(*entry.ErrorMsg, "panic: boom") {
		t.Fatalf("expected errorMsg to contain panic info, got %v", entry.ErrorMsg)
	}
}

// TestOperationLogMiddlewareSkipsGET verifies GET requests are not logged.
func TestOperationLogMiddlewareSkipsGET(t *testing.T) {
	r, svc := setupOpLogRouter()
	r.GET("/get", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/get", nil))

	select {
	case <-svc.logs:
		t.Fatal("expected no operation log for GET request")
	case <-time.After(150 * time.Millisecond):
		// pass
	}
}

// TestOperationLogMiddlewarePreservesScopeContext 是 P0 回归测试。
//
// 历史缺陷:saveOperationLog 在 goroutine 内用裸 context.Background()
// 重建上下文,只补了 traceId,丢掉了 datascope.ScopeContext。
// 而 sys_operation_log 与 sys_user 都是 datascope 注册实体,
// OperationLogService.Create 会用该 ctx 反查用户名 —— 失去 scope 后
// 这次查询不再受数据权限约束(跨部门读取),且审计链路与运行时请求
// 的过滤口径不一致。本测试锁定"异步落库 ctx 必须携带 ScopeContext"。
func TestOperationLogMiddlewarePreservesScopeContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &ctxCaptureOpLogService{ctx: make(chan context.Context, 1)}
	log := logger.NewNop()

	// 模拟 ScopeResolverHandler:注入带 dept 维度的 ScopeContext。
	want := &datascope.ScopeContext{
		UserID: 42,
		Dimensions: map[string]*datascope.ResolvedDimension{
			"dept": {Level: datascope.ScopeDept, SelfID: 7, AllowedIDs: []uint64{7}},
		},
	}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		ctx := contextkeys.WithTraceID(c.Request.Context(), "trace-xyz")
		ctx = datascope.WithScopeContext(ctx, want)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	r.Use(OperationLogMiddleware(svc, log))
	r.POST("/scoped", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"code": 0, "msg": "success", "data": nil})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/scoped", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	var got context.Context
	select {
	case got = <-svc.ctx:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for operation log ctx")
	}

	gotScope, ok := datascope.ScopeContextFromCtx(got)
	if !ok || gotScope == nil {
		t.Fatal("异步落库 ctx 丢失了 ScopeContext —— 审计查询将绕过数据权限")
	}
	if gotScope.UserID != want.UserID {
		t.Errorf("ScopeContext.UserID = %d, want %d", gotScope.UserID, want.UserID)
	}
	dim, ok := gotScope.Dimensions["dept"]
	if !ok || dim == nil {
		t.Fatal("ScopeContext 丢失了 dept 维度")
	}
	if dim.Level != datascope.ScopeDept || len(dim.AllowedIDs) != 1 || dim.AllowedIDs[0] != 7 {
		t.Errorf("dept 维度 = %+v, want level=%d allowed=[7]", dim, datascope.ScopeDept)
	}

	// traceId 同样必须保留(原有行为,防止回归)。
	if tid, ok := contextkeys.TraceIDFromCtx(got); !ok || tid != "trace-xyz" {
		t.Errorf("异步落库 ctx 丢失了 traceId, got %q", tid)
	}
}

// TestOperationLogPasswordDesensitized：敏感字段必须在**进审计之前**被掩码 ——
// 凭据写请求（/docker/registries 的 password）落到 sys_operation_log 的是 "***"，
// 不是明文；嵌套 JSON 同样递归掩码。
func TestOperationLogPasswordDesensitized(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &captureHookSvc{logs: make(chan *entity.SysOperationLog, 1)}
	r := gin.New()
	r.Use(OperationLogMiddleware(svc, logger.NewNop()))
	r.POST("/docker/registries", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"code": 0})
	})

	body := `{"registry":"harbor.example.com","username":"u","password":"sup3r-s3cret","nested":{"password":"inner-secret"}}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/docker/registries", strings.NewReader(body))
	r.ServeHTTP(w, req)

	entry := waitLog(t, &captureOpLogService{logs: svc.logs})
	got := ""
	if entry.RequestParams != nil {
		got = *entry.RequestParams
	}
	if strings.Contains(got, "sup3r-s3cret") || strings.Contains(got, "inner-secret") {
		t.Fatalf("审计里的密码必须已被掩码,实际 %s", got)
	}
	if !strings.Contains(got, `"password":"***"`) {
		t.Fatalf("password 字段必须是 *** 掩码形态,实际 %s", got)
	}
	if !strings.Contains(got, "harbor.example.com") || !strings.Contains(got, `"username":"u"`) {
		t.Fatalf("非敏感字段（registry/用户名）必须原样入审计,实际 %s", got)
	}
}

// TestOperationLogRegistryOptionIsNotMasked：image:pull 受理审计的 body 里
// registry 是**凭据键而不是秘密** —— 不含密码、可安全入审计（4c 选型 A 的
// 「受理审计不碰密码」前提：密码在审计之后才解出注入,body 里只有 registry 名）。
func TestOperationLogRegistryOptionIsNotMasked(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &captureHookSvc{logs: make(chan *entity.SysOperationLog, 1)}
	r := gin.New()
	r.Use(OperationLogMiddleware(svc, logger.NewNop()))
	r.POST("/docker/hosts/7/cmds", func(c *gin.Context) {
		c.JSON(http.StatusAccepted, gin.H{"code": 0})
	})

	body := `{"action":"image:pull","target":"harbor.example.com/app:1","options":{"registry":"harbor.example.com"}}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/docker/hosts/7/cmds", strings.NewReader(body))
	r.ServeHTTP(w, req)

	entry := waitLog(t, &captureOpLogService{logs: svc.logs})
	got := ""
	if entry.RequestParams != nil {
		got = *entry.RequestParams
	}
	if !strings.Contains(got, `"registry":"harbor.example.com"`) {
		t.Fatalf("registry 凭据键必须原样入审计（它不含秘密）: %s", got)
	}
	if !strings.Contains(got, "image:pull") {
		t.Fatalf("action 必须入审计: %s", got)
	}
}

// TestOperationLogMiddlewareBuildContextBodyExempt 是 P0-3 回归：构建上下文上传
// 的 gzip 字节绝不进 sys_operation_log.request_params —— 落的是「二进制构建
// 上下文 N 字节」占位。两条断言各钉一半事实：
//   - handler 读到**完整**原文字节（豁免路径零接触 body —— 上传通道是流式
//     水管，审计采样不得预读/截断）；
//   - 审计里只有占位，二进制正文（可辨认的哨兵串）一个字节都不在。
func TestOperationLogMiddlewareBuildContextBodyExempt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &captureOpLogService{logs: make(chan *entity.SysOperationLog, 1)}
	r := gin.New()
	r.Use(OperationLogMiddleware(svc, logger.NewNop()))

	var handlerBodyLen int
	r.POST("/api/v1/docker/hosts/:id/build-context", func(c *gin.Context) {
		b, err := io.ReadAll(c.Request.Body)
		if err != nil {
			t.Errorf("handler 读 body 失败: %v", err)
		}
		handlerBodyLen = len(b)
		c.JSON(http.StatusOK, gin.H{"code": 0, "data": gin.H{"filename": "build-ctx-1.tar.gz"}})
	})

	// gzip 魔数开头的二进制正文：嵌入可辨认哨兵串（若泄漏，断言当场抓到）。
	body := append([]byte{0x1f, 0x8b}, bytes.Repeat([]byte("SECRET-BUILD-SRC"), 256)...)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/docker/hosts/7/build-context", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/octet-stream")
	r.ServeHTTP(w, req)

	if handlerBodyLen != len(body) {
		t.Fatalf("handler 应收到完整原文字节（%d），实际 %d —— 豁免路径不得预读/截断 body", len(body), handlerBodyLen)
	}
	entry := waitLog(t, svc)
	if entry.RequestParams == nil {
		t.Fatal("占位文案必须落 request_params（这次上传发生过，审计要看得见）")
	}
	got := *entry.RequestParams
	if want := fmt.Sprintf("二进制构建上下文 %d 字节", len(body)); got != want {
		t.Fatalf("request_params = %q, want 占位 %q", got, want)
	}
	if strings.Contains(got, "SECRET-BUILD-SRC") {
		t.Fatal("二进制正文不得进审计")
	}
}

// TestOperationLogMiddlewareOctetStreamAutoExempt 是豁免规则的普适腿：
// **未声明**的端点只要按惯例声明 octet-stream 就自动豁免 body 捕获（泛化
// 占位文案）—— 「今后任何流式端点都不进 body 捕获」的落点：新端点忘了登记
// 也不会把二进制正文漏进审计。chunked（无 Content-Length）时说「长度未知」。
func TestOperationLogMiddlewareOctetStreamAutoExempt(t *testing.T) {
	cases := []struct {
		name       string
		contentLen int64
		wantSuffix string
	}{
		{"带长度", 1024, "二进制请求体 1024 字节"},
		{"chunked 无长度", -1, "二进制请求体（长度未知）"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			svc := &captureOpLogService{logs: make(chan *entity.SysOperationLog, 1)}
			r := gin.New()
			r.Use(OperationLogMiddleware(svc, logger.NewNop()))
			r.POST("/api/v1/uploads/future-binary", func(c *gin.Context) {
				_, _ = io.Copy(io.Discard, c.Request.Body)
				c.JSON(http.StatusOK, gin.H{"code": 0})
			})

			body := bytes.Repeat([]byte{0x1f, 0x8b}, 512)
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/uploads/future-binary", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/octet-stream")
			req.ContentLength = tc.contentLen
			r.ServeHTTP(w, req)

			entry := waitLog(t, svc)
			if entry.RequestParams == nil {
				t.Fatal("占位文案必须落 request_params")
			}
			if got := *entry.RequestParams; got != tc.wantSuffix {
				t.Fatalf("request_params = %q, want %q", got, tc.wantSuffix)
			}
		})
	}
}

// captureHookSvc 是 OperationLogServiceInterface 的最小钩子替身
// （只关心 RequestParams 就到了,与 captureOpLogService 同形态但用通道)。
type captureHookSvc struct {
	logs chan *entity.SysOperationLog
}

func (s *captureHookSvc) Create(_ context.Context, log *entity.SysOperationLog) error {
	s.logs <- log
	return nil
}
