package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
)

type stubOnlineService struct {
	listErr error
	kickErr error
	gotTok  string
}

func (s *stubOnlineService) List(context.Context, *request.OnlineUserQuery) (*app.PageResponse, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return app.NewPageResponse([]any{}, 0, 1, 10), nil
}
func (s *stubOnlineService) Kick(_ context.Context, _ *request.KickSessionReq, tok string) error {
	s.gotTok = tok
	return s.kickErr
}

func TestOnlineHandlerList(t *testing.T) {
	svc := &stubOnlineService{}
	h := NewOnlineHandler(svc)
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/monitor/online?page=1&pageSize=10", nil)
	h.List(c)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
}

func TestOnlineHandlerKickPassesToken(t *testing.T) {
	svc := &stubOnlineService{}
	h := NewOnlineHandler(svc)
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/monitor/online/kick", strings.NewReader(`{"uid":1}`))
	c.Request.Header.Set("Authorization", "Bearer mytoken")
	c.Request.Header.Set("Content-Type", "application/json")
	h.Kick(c)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	if svc.gotTok != "mytoken" {
		t.Fatalf("gotTok = %q", svc.gotTok)
	}
}

func TestOnlineHandlerKickErrorMapping(t *testing.T) {
	svc := &stubOnlineService{kickErr: apperror.Forbidden("无权操作该用户会话")}
	h := NewOnlineHandler(svc)
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/monitor/online/kick", strings.NewReader(`{"uid":1}`))
	c.Request.Header.Set("Content-Type", "application/json")
	h.Kick(c)
	if w.Code != http.StatusForbidden {
		t.Fatalf("code = %d", w.Code)
	}
}
