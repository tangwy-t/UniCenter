package session

import (
	"crypto/sha256"
	"encoding/hex"
)

// SessionMeta 是在线会话的可观测元数据。LoginAt/ExpireAt 为 unix 秒。
type SessionMeta struct {
	UserID    uint64 `json:"userId"`
	IP        string `json:"ip"`
	UserAgent string `json:"userAgent"`
	Browser   string `json:"browser"`
	OS        string `json:"os"`
	LoginAt   int64  `json:"loginAt"`
	ExpireAt  int64  `json:"expireAt"`
}

// SID 由 access token 派生的会话标识:列表/踢下线接口只暴露该哈希,
// 绝不回传 token 明文(token 即凭证)。确定性:同一 token 恒得同一 sid。
func SID(token string) string {
	sum := sha256.Sum256([]byte("online:" + token))
	return hex.EncodeToString(sum[:])
}
