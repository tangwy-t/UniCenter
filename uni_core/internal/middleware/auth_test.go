package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/tangwy-t/webmanager-server/internal/pkg/contextkeys"
	"github.com/tangwy-t/webmanager-server/internal/pkg/jwt"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
)

// stubSecretCfg 返回固定 JWT secret,满足 ConfigGetterInterface。
type stubSecretCfg struct{ secret string }

func (s stubSecretCfg) GetString(_ context.Context, key, _ string) string {
	if key == jwt.SecretConfigKey {
		return s.secret
	}
	return ""
}
func (stubSecretCfg) GetInt(context.Context, string, int) int    { return 7200 }
func (stubSecretCfg) GetBool(context.Context, string, bool) bool { return false }

// stubTokenStore 满足 TokenStoreInterface。
type stubTokenStore struct{ valid bool }

func (s stubTokenStore) IsAccessValid(context.Context, string) (bool, error) { return s.valid, nil }

// TestAuthStoresFullClaims Auth 通过后 gin context 必须携带完整的 *jwt.Claims,
// 且标量字段与解析出的 claims 一致;请求 ctx 同时注入 contextkeys.UserID。
func TestAuthStoresFullClaims(t *testing.T) {
	const secret = "test-secret-0123456789-0123456789" // ≥32 bytes
	scopes := []jwt.ScopeClaim{{Dimension: "dept", Level: 3, SelfID: 42}}
	token, err := jwt.GenerateAccessToken(7, []string{"a"}, scopes, secret, 3600)
	if err != nil {
		t.Fatalf("GenerateAccessToken: %v", err)
	}

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.Request.Header.Set("Authorization", "Bearer "+token)

	mw := Auth(stubSecretCfg{secret: secret}, stubTokenStore{valid: true}, logger.NewNop())
	mw(c)

	if c.IsAborted() {
		t.Fatal("Auth 不应中止合法请求")
	}
	raw, ok := c.Get(CtxClaims)
	if !ok {
		t.Fatal("gin context 中缺少 CtxClaims")
	}
	claims, ok := raw.(*jwt.Claims)
	if !ok {
		t.Fatalf("CtxClaims = %T, want *jwt.Claims", raw)
	}
	if claims.UserID != 7 || len(claims.Scopes) != 1 || claims.Scopes[0].SelfID != 42 {
		t.Fatalf("claims = %+v, 与签发内容不一致", claims)
	}
	if uid, ok := contextkeys.UserIDFromCtx(c.Request.Context()); !ok || uid != 7 {
		t.Fatalf("请求 ctx 应携带 userID=7, got %d/%v", uid, ok)
	}
}

// TestAuthRejectsRevoked 会话白名单拒绝时中止并清空下游 key(与旧行为一致)。
func TestAuthRejectsRevoked(t *testing.T) {
	const secret = "test-secret-0123456789-0123456789"
	token, err := jwt.GenerateAccessToken(7, nil, nil, secret, 3600)
	if err != nil {
		t.Fatalf("GenerateAccessToken: %v", err)
	}

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.Request.Header.Set("Authorization", "Bearer "+token)

	Auth(stubSecretCfg{secret: secret}, stubTokenStore{valid: false}, logger.NewNop())(c)

	if !c.IsAborted() {
		t.Fatal("被吊销 token 应中止请求")
	}
	if _, ok := c.Get(CtxClaims); ok {
		t.Fatal("拒绝路径不应写入 CtxClaims")
	}
}
