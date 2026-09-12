// Package crypto provides password hashing and verification utilities.
package crypto

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

const saltSize = 16

// HashPassword generates a random salt, then hashes salt+password with bcrypt.
// Returns the bcrypt hash and the hex-encoded salt.
//
// 输入经 SHA-256 归一化:bcrypt 对 >72 字节的输入会静默截断,此前直接
// 拼接「32 字符 hex 盐 + 密码(≤64)」会超出 72 字节,使长密码后半段熵被
// 丢弃。改为先 sha256(salt+password) 得到固定 32 字节(64 字符 hex)再
// bcrypt,输入恒 ≤72 字节,完整保留密码熵且不再截断。
func HashPassword(password string, cost int) (hash string, salt string, err error) {
	saltBytes := make([]byte, saltSize)
	if _, err := rand.Read(saltBytes); err != nil {
		return "", "", fmt.Errorf("generate salt: %w", err)
	}
	salt = hex.EncodeToString(saltBytes)

	normalized := normalize(salt, password)
	hashBytes, err := bcrypt.GenerateFromPassword([]byte(normalized), cost)
	if err != nil {
		return "", "", fmt.Errorf("bcrypt hash: %w", err)
	}
	return string(hashBytes), salt, nil
}

// VerifyPassword checks a password against a stored bcrypt hash and salt.
// It prepends the salt to the plaintext password before comparing.
//
// 向后兼容:优先按新方案 sha256(salt+password) 归一化后比对;失败再回退到
// 旧方案「salt+password 直接 bcrypt」,使历史存量用户无需强制改密即可登录。
func VerifyPassword(password string, hash string, salt string) bool {
	normalized := normalize(salt, password)
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(normalized)) == nil {
		return true
	}
	// 旧格式回退:直接 salt+password(历史行为)。
	legacy := salt + password
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(legacy)) == nil
}

// normalize 用 SHA-256 将 salt+password 归一化为固定 64 字符 hex 字符串,
// 保证 bcrypt 输入恒 ≤72 字节,消除长密码截断。
func normalize(salt, password string) string {
	h := sha256.Sum256([]byte(salt + password))
	return hex.EncodeToString(h[:])
}

// SHA256Hex returns the hex-encoded SHA-256 hash of the input.
func SHA256Hex(input string) string {
	h := sha256.Sum256([]byte(input))
	return hex.EncodeToString(h[:])
}
