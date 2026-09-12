package jwt

import (
	"strings"
	"testing"
	"time"
)

const testSecret = "unit-test-secret-0123456789abcdef" // ≥32 bytes

// TestAccessTokenRoundTrip 签发→解析闭环:claims 字段与签名内容一致。
func TestAccessTokenRoundTrip(t *testing.T) {
	scopes := []ScopeClaim{{Dimension: "dept", Level: 3, SelfID: 42}}
	token, err := GenerateAccessToken(7, []string{"a", "b"}, scopes, testSecret, 3600)
	if err != nil {
		t.Fatalf("GenerateAccessToken: %v", err)
	}
	claims, err := ParseAccessToken(token, testSecret)
	if err != nil {
		t.Fatalf("ParseAccessToken: %v", err)
	}
	if claims.UserID != 7 || claims.Subject != "access" {
		t.Fatalf("claims = %+v", claims)
	}
	if len(claims.Perms) != 2 || claims.Perms[0] != "a" {
		t.Fatalf("Perms = %v", claims.Perms)
	}
	if len(claims.Scopes) != 1 || claims.Scopes[0].SelfID != 42 {
		t.Fatalf("Scopes = %v", claims.Scopes)
	}
	if claims.ExpiresAt == nil || time.Until(claims.ExpiresAt.Time) < 3500*time.Second {
		t.Fatalf("ExpiresAt = %v, want ~3600s 后", claims.ExpiresAt)
	}
}

// TestTokenTypeSeparation access/refresh 同签名同密钥,类型必须互斥:
// refresh 冒充 access(反之亦然)必须被拒。这是 token 挟持防护的核心契约。
func TestTokenTypeSeparation(t *testing.T) {
	access, err := GenerateAccessToken(1, nil, nil, testSecret, 3600)
	if err != nil {
		t.Fatal(err)
	}
	refresh, err := GenerateRefreshToken(1, testSecret, 604800)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseRefreshToken(access, testSecret); err == nil {
		t.Fatal("access token 被 ParseRefreshToken 接受")
	}
	if _, err := ParseAccessToken(refresh, testSecret); err == nil {
		t.Fatal("refresh token 被 ParseAccessToken 接受")
	}
}

// TestSecretTooShort 短密钥(<32B)签发与解析都必须拒绝。
func TestSecretTooShort(t *testing.T) {
	if _, err := GenerateAccessToken(1, nil, nil, "short", 3600); err == nil {
		t.Fatal("短密钥签发应报错")
	}
	if _, err := ParseToken("whatever.payload.sig", "short"); err == nil {
		t.Fatal("短密钥解析应报错")
	}
}

// TestWrongSecretRejected 错误密钥解析必须失败。
func TestWrongSecretRejected(t *testing.T) {
	token, err := GenerateAccessToken(1, nil, nil, testSecret, 3600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseAccessToken(token, "another-secret-0123456789abcdef"); err == nil {
		t.Fatal("错误密钥应解析失败")
	}
}

// TestExpiredTokenRejected 过期 token 必须失败。
func TestExpiredTokenRejected(t *testing.T) {
	token, err := GenerateAccessToken(1, nil, nil, testSecret, -1) // 立即过期
	if err != nil {
		t.Fatal(err)
	}
	_, err = ParseAccessToken(token, testSecret)
	if err == nil {
		t.Fatal("过期 token 应解析失败")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Fatalf("err = %v, 预期包含 expired", err)
	}
}
