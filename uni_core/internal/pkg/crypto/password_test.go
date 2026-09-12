package crypto

import (
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// TestHashAndVerifyRoundTrip 新方案:哈希后能验证通过,且两次哈希同一密码
// 因盐不同而得到不同哈希。
func TestHashAndVerifyRoundTrip(t *testing.T) {
	hash, salt, err := HashPassword("s3cret-раss!", bcrypt.MinCost)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if salt == "" {
		t.Fatal("salt 为空")
	}
	if !VerifyPassword("s3cret-раss!", hash, salt) {
		t.Fatal("VerifyPassword 应通过正确密码")
	}
	if VerifyPassword("wrong", hash, salt) {
		t.Fatal("VerifyPassword 不应通过错误密码")
	}

	hash2, salt2, _ := HashPassword("s3cret-раss!", bcrypt.MinCost)
	if hash == hash2 && salt == salt2 {
		t.Fatal("两次哈希应因随机盐而不同")
	}
}

// TestLongPasswordNotTruncated 长密码(>40 字符,超过旧方案的 bcrypt 72 字节
// 截断边界)在新方案下应完整保留熵:改最后一位字符即可让验证失败。
func TestLongPasswordNotTruncated(t *testing.T) {
	long := strings.Repeat("a", 60) + "X"
	almost := strings.Repeat("a", 60) + "Y" // 仅末位不同

	hash, salt, err := HashPassword(long, bcrypt.MinCost)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !VerifyPassword(long, hash, salt) {
		t.Fatal("长密码应验证通过")
	}
	// 旧方案(salt+раss 直接 bcrypt)下,60+1 字符超 72 字节会被截断,末位差异被丢弃;
	// 新方案 sha256 归一化后末位差异必须能影响结果。
	if VerifyPassword(almost, hash, salt) {
		t.Fatal("仅末位不同的长密码不应验证通过(截断已修复)")
	}
}

// TestVerifyLegacyPassword 向后兼容:旧格式(salt+раss 直接 bcrypt,无 sha256
// 归一化)存储的历史密码仍能验证,存量用户无需强制改密即可登录。
func TestVerifyLegacyPassword(t *testing.T) {
	password := "legacy-раss"
	salt := "0a0b0c0d0e0f10111213141516" // 32 字符 hex 盐,模拟旧实现
	legacy := salt + password
	hashBytes, err := bcrypt.GenerateFromPassword([]byte(legacy), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}

	if !VerifyPassword(password, string(hashBytes), salt) {
		t.Fatal("旧格式密码应能通过验证(向后兼容回退失败)")
	}
	if VerifyPassword("wrong", string(hashBytes), salt) {
		t.Fatal("旧格式密码不应通过错误密码")
	}
}

// TestSHA256Hex 确保 SHA256Hex 输出 64 字符 hex。
func TestSHA256Hex(t *testing.T) {
	got := SHA256Hex("abc")
	if len(got) != 64 {
		t.Fatalf("SHA256Hex 长度 = %d, want 64", len(got))
	}
	if SHA256Hex("abc") != SHA256Hex("abc") {
		t.Fatal("SHA256Hex 应确定性")
	}
}
