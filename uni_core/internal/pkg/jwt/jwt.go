// Package jwt provides JWT token generation and parsing utilities.
package jwt

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// SecretConfigKey 是 JWT 签名密钥的配置键名。与 DefaultSecretFallback
// 一并单源:键名字面量散布多处时,任一处的拼写漂移都会让读取点静默
// 落回不安全默认密钥。所有 GetString(..., SecretConfigKey, ...) 与
// 轮换逻辑必须引用此常量,禁止手写字面量。
const SecretConfigKey = "sys.jwt.secret"

// DefaultSecretFallback 是 SecretConfigKey 缺失时各读取点的兜底值,
// 同时是 RotateDefaultJWTSecret 判定"仍在使用不安全默认密钥"的标记之一。
// 收敛为单一常量:安全敏感字面量散布多处时,排查者难以确认已全部覆盖。
// 注意:该值公开可读,绝不用于生产签名,仅保证启动不崩,启动时会被轮换。
const DefaultSecretFallback = "default-jwt-secret-key"

// ScopeClaim represents a single data-scope dimension in the JWT claims.
type ScopeClaim struct {
	Dimension string `json:"dimension"` // "dept", "self", "tenant", ...
	Level     int8   `json:"level"`     // 1=all, 2=custom, 3=dept, 4=dept_and_below, 5=self
	SelfID    uint64 `json:"selfId"`    // self ID in this dimension (dept_id or user_id)
}

// Claims represents the custom JWT claims payload.
type Claims struct {
	UserID uint64       `json:"userId"`
	Perms  []string     `json:"perms"`
	Scopes []ScopeClaim `json:"scopes"`
	jwt.RegisteredClaims
}

// GenerateAccessToken creates a signed JWT access token for the given user.
func GenerateAccessToken(userID uint64, perms []string, scopes []ScopeClaim, secret string, expireIn int) (string, error) {
	return generateToken(userID, perms, scopes, secret, time.Duration(expireIn)*time.Second, "access")
}

// GenerateRefreshToken creates a signed JWT refresh token for the given user.
func GenerateRefreshToken(userID uint64, secret string, expire int) (string, error) {
	return generateToken(userID, nil, nil, secret, time.Duration(expire)*time.Second, "refresh")
}

func generateToken(userID uint64, perms []string, scopes []ScopeClaim, secret string, expire time.Duration, tokenType string) (string, error) {
	if len(secret) < 32 {
		return "", fmt.Errorf("jwt secret must be at least 32 bytes")
	}
	claims := Claims{
		UserID: userID,
		Perms:  perms,
		Scopes: scopes,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(expire)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Subject:   tokenType,
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

// ParseAccessToken parses a JWT and asserts it is an ACCESS token
// (Subject == "access"). Access and refresh tokens share the same secret
// and algorithm; without the type check a 7-day refresh token would pass
// any access-token verification as a valid credential.
func ParseAccessToken(tokenStr string, secret string) (*Claims, error) {
	return parseTokenExpectingType(tokenStr, secret, "access")
}

// ParseRefreshToken parses a JWT and asserts it is a REFRESH token
// (Subject == "refresh"), symmetrically guarding the refresh endpoint
// against access tokens.
func ParseRefreshToken(tokenStr string, secret string) (*Claims, error) {
	return parseTokenExpectingType(tokenStr, secret, "refresh")
}

func parseTokenExpectingType(tokenStr string, secret string, expectedType string) (*Claims, error) {
	claims, err := ParseToken(tokenStr, secret)
	if err != nil {
		return nil, err
	}
	if claims.Subject != expectedType {
		return nil, fmt.Errorf("jwt: token type %q presented where %q is required", claims.Subject, expectedType)
	}
	return claims, nil
}

// ParseToken parses and validates a JWT token string, returning the embedded claims.
// It rejects tokens signed with non-HMAC algorithms. Callers should prefer
// ParseAccessToken / ParseRefreshToken, which also enforce the token type.
func ParseToken(tokenStr string, secret string) (*Claims, error) {
	if len(secret) < 32 {
		return nil, fmt.Errorf("jwt secret must be at least 32 bytes")
	}
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	if claims, ok := token.Claims.(*Claims); ok && token.Valid {
		return claims, nil
	}
	return nil, jwt.ErrSignatureInvalid
}
