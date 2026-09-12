package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/contextkeys"
)

// stubReadAllService 内嵌接口空变体,仅重写 MarkAllRead(其它方法不会被本测试触及)。
type stubReadAllService struct {
	NoticeServiceInterface
	err error
}

func (s *stubReadAllService) MarkAllRead(_ context.Context, userID uint64) error {
	return s.err
}

func newReadAllContext(t *testing.T, withUser bool) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/notices/read-all", nil)
	if withUser {
		c.Request = c.Request.WithContext(contextkeys.WithUserID(c.Request.Context(), 7))
	}
	return c, w
}

func TestNoticeHandlerMarkAllRead(t *testing.T) {
	t.Run("成功:白名单登录态,HTTP 200 code 0", func(t *testing.T) {
		h := NewNoticeHandler(&stubReadAllService{})
		c, w := newReadAllContext(t, true)
		h.MarkAllRead(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		if !containsCode(w.Body.String(), apperror.CodeOK) {
			t.Fatalf("body = %s, want code %d", w.Body.String(), apperror.CodeOK)
		}
	})

	t.Run("未登录:HTTP 401 code 10001", func(t *testing.T) {
		h := NewNoticeHandler(&stubReadAllService{})
		c, w := newReadAllContext(t, false)
		h.MarkAllRead(c)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", w.Code)
		}
		if !containsCode(w.Body.String(), apperror.CodeUnauthorized) {
			t.Fatalf("body = %s, want code %d", w.Body.String(), apperror.CodeUnauthorized)
		}
	})

	t.Run("仓库失败:HTTP 500 映射", func(t *testing.T) {
		h := NewNoticeHandler(&stubReadAllService{err: errors.New("db down")})
		c, w := newReadAllContext(t, true)
		h.MarkAllRead(c)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", w.Code)
		}
		if !containsCode(w.Body.String(), apperror.CodeInternal) {
			t.Fatalf("body = %s, want code %d", w.Body.String(), apperror.CodeInternal)
		}
	})
}

// containsCode 判定响应 JSON 形如 {"code":N,...}。
func containsCode(body string, code int) bool {
	return strings.Contains(body, fmt.Sprintf(`"code":%d`, code))
}
