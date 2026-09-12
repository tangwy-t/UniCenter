package handler

import (
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/captcha"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
)

// ── 测试替身 ────────────────────────────────────────────

// stubAuthHdlService 满足 AuthServiceInterface;仅被测方法有行为,其余零值返回。
type stubAuthHdlService struct {
	logoutErr    error
	gotLogoutTok string
	loginResp    *response.LoginResp
	loginErr     error
}

func (s *stubAuthHdlService) Login(context.Context, *request.LoginReq, string, string) (*response.LoginResp, error) {
	return s.loginResp, s.loginErr
}
func (s *stubAuthHdlService) Logout(_ context.Context, token, _ string) error {
	s.gotLogoutTok = token
	return s.logoutErr
}
func (s *stubAuthHdlService) GetUserInfo(context.Context) (*response.UserInfoResp, error) {
	return nil, nil
}
func (s *stubAuthHdlService) GetUserOverview(context.Context) (*response.UserOverviewResp, error) {
	return nil, nil
}
func (s *stubAuthHdlService) ChangePassword(context.Context, *request.ChangePasswordReq, string) error {
	return nil
}
func (s *stubAuthHdlService) VerifyPassword(context.Context, *request.VerifyPasswordReq) (*response.VerifyPasswordResp, error) {
	return nil, nil
}
func (s *stubAuthHdlService) UpdateProfile(context.Context, *request.UpdateProfileReq) error {
	return nil
}
func (s *stubAuthHdlService) UploadAvatar(context.Context, *multipart.FileHeader) (string, error) {
	return "", nil
}
func (s *stubAuthHdlService) AvatarFilePath(context.Context, uint64) (string, error) {
	return "", nil
}
func (s *stubAuthHdlService) RefreshToken(context.Context, *request.RefreshTokenReq, string, string) (*response.RefreshTokenResp, error) {
	return nil, nil
}

type stubCaptcha struct{}

func (stubCaptcha) Generate(context.Context, string) (*captcha.Result, error) { return nil, nil }
func (stubCaptcha) Unlock(context.Context, uint64) error                      { return nil }

func newAuthHandlerCtx(t *testing.T, method, target, body string) (*httptest.ResponseRecorder, *gin.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	c.Request = r
	return w, c
}

// ── Logout ──────────────────────────────────────────────

// TestAuthHandlerLogout_StripsBearerPrefix 会话 token 必须以裸 token 形式
// 交给 service —— session 存储的键形如 access:<token>,带上 "Bearer "
// 前缀会删不掉任何东西,登出静默失效(白名单里的 token 仍可用)。
func TestAuthHandlerLogout_StripsBearerPrefix(t *testing.T) {
	svc := &stubAuthHdlService{}
	h := NewAuthHandler(svc, stubCaptcha{}, logger.NewNop())

	w, c := newAuthHandlerCtx(t, http.MethodPost, "/api/v1/logout", "")
	c.Request.Header.Set("Authorization", "Bearer tok-abc123")
	h.Logout(c)

	if svc.gotLogoutTok != "tok-abc123" {
		t.Fatalf("传给 service 的 token = %q, want %q(未去掉 Bearer 前缀)", svc.gotLogoutTok, "tok-abc123")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200; body=%s", w.Code, w.Body.String())
	}
}

// TestAuthHandlerLogout_ReportsFailureOnStoreError 会话存储故障时不得谎报成功。
// 若返回 200,客户端会丢弃本地 token 而服务端白名单仍在,该 token 在其
// JWT 过期前依然有效 —— 用户以为已登出,实际没有。
func TestAuthHandlerLogout_ReportsFailureOnStoreError(t *testing.T) {
	svc := &stubAuthHdlService{logoutErr: errors.New("redis down")}
	h := NewAuthHandler(svc, stubCaptcha{}, logger.NewNop())

	w, c := newAuthHandlerCtx(t, http.MethodPost, "/api/v1/logout", "")
	c.Request.Header.Set("Authorization", "Bearer tok")
	h.Logout(c)

	if w.Code == http.StatusOK {
		t.Fatalf("会话存储故障却返回 200,客户端会误以为已登出; body=%s", w.Body.String())
	}
}

// TestAuthHandlerLogout_MissingHeaderStillCallsService 无 Authorization 头时
// 传空 token(而非 panic),由 service 侧决定语义。
func TestAuthHandlerLogout_MissingHeaderStillCallsService(t *testing.T) {
	svc := &stubAuthHdlService{}
	h := NewAuthHandler(svc, stubCaptcha{}, logger.NewNop())

	_, c := newAuthHandlerCtx(t, http.MethodPost, "/api/v1/logout", "")
	h.Logout(c) // 不应 panic

	if svc.gotLogoutTok != "" {
		t.Fatalf("token = %q, want 空", svc.gotLogoutTok)
	}
}

// ── Login ───────────────────────────────────────────────

func TestAuthHandlerLogin_InvalidJSONReturns400(t *testing.T) {
	h := NewAuthHandler(&stubAuthHdlService{}, stubCaptcha{}, logger.NewNop())

	w, c := newAuthHandlerCtx(t, http.MethodPost, "/api/v1/login", "{not json")
	h.Login(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", w.Code)
	}
}

func TestAuthHandlerLogin_SuccessEnvelope(t *testing.T) {
	svc := &stubAuthHdlService{loginResp: &response.LoginResp{AccessToken: "at", RefreshToken: "rt"}}
	h := NewAuthHandler(svc, stubCaptcha{}, logger.NewNop())

	w, c := newAuthHandlerCtx(t, http.MethodPost, "/api/v1/login", `{"username":"u","password":"p"}`)
	h.Login(c)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	// 成功必须是 code === 0(前端唯一判定依据)。
	var env struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("响应不是合法 JSON: %v; body=%s", err, w.Body.String())
	}
	if env.Code != 0 {
		t.Fatalf("code = %d, want 0", env.Code)
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		t.Fatalf("data 为空; body=%s", w.Body.String())
	}
}

// TestAuthHandlerLogin_ServiceErrorPropagates 业务错误必须走 app.Error
// 映射为对应的 HTTP 状态,而不是一律 200。
func TestAuthHandlerLogin_ServiceErrorPropagates(t *testing.T) {
	svc := &stubAuthHdlService{loginErr: apperror.Unauthorized("用户名或密码错误")}
	h := NewAuthHandler(svc, stubCaptcha{}, logger.NewNop())

	w, c := newAuthHandlerCtx(t, http.MethodPost, "/api/v1/login", `{"username":"u","password":"bad"}`)
	h.Login(c)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401; body=%s", w.Code, w.Body.String())
	}
}
