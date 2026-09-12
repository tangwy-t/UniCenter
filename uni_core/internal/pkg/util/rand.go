package util

import (
	"crypto/rand"
	"encoding/hex"
)

// RandomHex 生成 n 字节的加密安全随机数据并返回其 hex 编码。
//
// 迁移自 service 包的 randomFileKey / generateRandomSecret 两处重复的
// crypto/rand + hex 逻辑：随机源在 util 收敛一处；错误处理（失败即中止、
// 或时间戳兜底）由各调用方按自身域语义决定。
func RandomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
