package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/redis/cache"
)

// ── 测试替身 ────────────────────────────────────────────

type stubCacheService struct {
	page *response.CacheValuePage
	err  error
	got  *request.GetKeyValueRequest
}

func (s *stubCacheService) ListKeys(context.Context, *request.ListKeysRequest) (*response.ListKeysResponse, error) {
	return nil, nil
}
func (s *stubCacheService) GetKeyValue(_ context.Context, req *request.GetKeyValueRequest) (*response.CacheValuePage, error) {
	s.got = req
	return s.page, s.err
}
func (s *stubCacheService) DeleteKeys(context.Context, *request.DeleteKeysRequest) (*response.DeleteKeysResponse, error) {
	return nil, nil
}
func (s *stubCacheService) GetStats(context.Context) (*cache.Stats, error) { return nil, nil }

func newGetValueContext(t *testing.T, rawQuery string) (*httptest.ResponseRecorder, *gin.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/monitor/cache/keys/value"+rawQuery, nil)
	return w, c
}

func TestCacheHandlerGetKeyValueSuccess(t *testing.T) {
	svc := &stubCacheService{page: &response.CacheValuePage{
		Key: "job:logs", Type: "list", TTL: -1, Total: 30000, Start: 200, HasMore: true,
		Value: []string{"a", "b"},
	}}
	h := NewCacheHandler(svc)

	w, c := newGetValueContext(t, "?key=job:logs&offset=200&limit=100")
	h.GetKeyValue(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s), want 200", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"has_more"`) {
		t.Fatalf("body missing has_more: %s", w.Body.String())
	}
	if svc.got == nil || svc.got.Offset != 200 || svc.got.Limit != 100 || svc.got.Cursor != 0 {
		t.Fatalf("svc got req = %+v", svc.got)
	}
}

func TestCacheHandlerGetKeyValueDefaults(t *testing.T) {
	svc := &stubCacheService{page: &response.CacheValuePage{Key: "k", Type: "list", TTL: -1, Total: 0}}
	h := NewCacheHandler(svc)

	w, c := newGetValueContext(t, "?key=k")
	h.GetKeyValue(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if svc.got == nil || svc.got.Limit != 100 || svc.got.Offset != 0 {
		t.Fatalf("defaults not applied, got = %+v, want Limit 100/Offset 0", svc.got)
	}
}

func TestCacheHandlerGetKeyValueBadParams(t *testing.T) {
	cases := []struct {
		name  string
		query string
	}{
		{"缺 key", "?key="},
		{"limit 为 0", "?key=k&limit=0"},
		{"limit 超全局上限", "?key=k&limit=4194305"},
		{"offset 为负", "?key=k&offset=-1"},
		{"cursor 为负", "?key=k&cursor=-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewCacheHandler(&stubCacheService{})
			w, c := newGetValueContext(t, tc.query)
			h.GetKeyValue(c)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d (body %s), want 400", w.Code, w.Body.String())
			}
		})
	}
}

func TestCacheHandlerGetKeyValueServiceErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"业务 400", apperror.BadRequest("参数错误"), http.StatusBadRequest},
		{"key 不存在 404", apperror.NotFound("key 不存在: k"), http.StatusNotFound},
		{"内部 500", apperror.Internal("查询缓存值失败"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewCacheHandler(&stubCacheService{err: tc.err})
			w, c := newGetValueContext(t, "?key=k")
			h.GetKeyValue(c)
			if w.Code != tc.want {
				t.Fatalf("status = %d (body %s), want %d", w.Code, w.Body.String(), tc.want)
			}
		})
	}
}
