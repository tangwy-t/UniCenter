package handler

import (
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
)

// ── 测试替身 ────────────────────────────────────────────
// stubAuthService 满足 AuthServiceInterface;仅 VerifyPassword 被测,其余零值返回。
type stubAuthService struct {
	verifyResp *response.VerifyPasswordResp
	verifyErr  error
	got        *request.VerifyPasswordReq
}

func (s *stubAuthService) Login(context.Context, *request.LoginReq, string, string) (*response.LoginResp, error) {
	return nil, nil
}
func (s *stubAuthService) Logout(context.Context, string, string) error { return nil }
func (s *stubAuthService) GetUserInfo(context.Context) (*response.UserInfoResp, error) {
	return nil, nil
}
func (s *stubAuthService) GetUserOverview(context.Context) (*response.UserOverviewResp, error) {
	return nil, nil
}
func (s *stubAuthService) ChangePassword(context.Context, *request.ChangePasswordReq, string) error {
	return nil
}
func (s *stubAuthService) VerifyPassword(_ context.Context, req *request.VerifyPasswordReq) (*response.VerifyPasswordResp, error) {
	s.got = req
	return s.verifyResp, s.verifyErr
}
func (s *stubAuthService) UpdateProfile(context.Context, *request.UpdateProfileReq) error {
	return nil
}
func (s *stubAuthService) UploadAvatar(context.Context, *multipart.FileHeader) (string, error) {
	return "", nil
}
func (s *stubAuthService) AvatarFilePath(context.Context, uint64) (string, error) { return "", nil }
func (s *stubAuthService) RefreshToken(context.Context, *request.RefreshTokenReq, string, string) (*response.RefreshTokenResp, error) {
	return nil, nil
}

func newVerifyContext(t *testing.T, body string) (*httptest.ResponseRecorder, *gin.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/user/password/verify", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return w, c
}

func TestAuthHandlerVerifyPasswordOK(t *testing.T) {
	svc := &stubAuthService{verifyResp: &response.VerifyPasswordResp{Valid: true}}
	h := NewAuthHandler(svc, nil, nil)

	w, c := newVerifyContext(t, `{"password":"admin123"}`)
	h.VerifyPassword(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s), want 200", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"valid":true`) {
		t.Fatalf("body missing valid:true: %s", w.Body.String())
	}
	if svc.got == nil || svc.got.Password != "admin123" {
		t.Fatalf("svc got req = %+v", svc.got)
	}
}

func TestAuthHandlerVerifyPasswordBad(t *testing.T) {
	svc := &stubAuthService{verifyErr: apperror.BadRequest("密码不正确")}
	h := NewAuthHandler(svc, nil, nil)

	w, c := newVerifyContext(t, `{"password":"wrong"}`)
	h.VerifyPassword(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestAuthHandlerVerifyPasswordMissingParam(t *testing.T) {
	svc := &stubAuthService{}
	h := NewAuthHandler(svc, nil, nil)

	w, c := newVerifyContext(t, `{}`)
	h.VerifyPassword(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if svc.got != nil {
		t.Fatalf("binding 失败不应触达服务层, got = %+v", svc.got)
	}
}
