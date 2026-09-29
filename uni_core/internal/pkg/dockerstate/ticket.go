package dockerstate

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// StreamTicketTTL 是一次性流票据的存活窗口（spec §4.1：30 秒）。
//
// 短 TTL 是刻意的：票据出现在 URL（浏览器 WebSocket 无法带 Authorization 头），
// 30 秒内不用就自然作废 —— 代理日志/历史记录里残留的票据很快失去价值。
const StreamTicketTTL = 30 * time.Second

// StreamTicket 是一次性流票据（Redis `docker:stream_ticket:<ticket>`，JSON）。
//
// 绑定三要素（spec §4.1）：userId + sessionId + deviceId。任一不符即拒绝接入 ——
// 票据本身**不承载权限**，它是「已通过 JWT + 权限码 + 归属校验的人，此刻换来的一张
// 一次性入场券」。
type StreamTicket struct {
	// Ticket 是票据明文（crypto/rand 32B 的 base64url，URL 安全，直接放查询参数）。
	Ticket string `json:"ticket"`
	// UserID 是发起人：接入者必须与之一致（与 session 的发起人比对是第二道）。
	UserID uint64 `json:"user_id"`
	// SessionID 是目标会话。
	SessionID string `json:"session_id"`
	// DeviceID 是会话所属设备：接入路径上的 :id 必须与之一致。
	DeviceID uint64 `json:"device_id"`
}

// TicketStore 签发与消费一次性流票据。
type TicketStore struct {
	rdb goredis.UniversalClient
}

// NewTicketStore 构造票据存储。
func NewTicketStore(rdb goredis.UniversalClient) *TicketStore { return &TicketStore{rdb: rdb} }

// consumeScript 原子地「取走并删除」票据：GET+DEL 两步写之间插进第二次连接，
// 就会让同一张票据被用两次 —— 单次使用必须是一个原子判定。
var consumeScript = goredis.NewScript(`
local v = redis.call('GET', KEYS[1])
if not v then return false end
redis.call('DEL', KEYS[1])
return v
`)

// newTicketValue 生成 32 字节随机票据（URL 安全、无填充，直接可用作查询参数）。
func newTicketValue() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// Issue 签发一张票据（TTL 固定 StreamTicketTTL）。
func (s *TicketStore) Issue(ctx context.Context, userID, deviceID uint64, sessionID string) (*StreamTicket, error) {
	val, err := newTicketValue()
	if err != nil {
		return nil, err
	}
	t := &StreamTicket{Ticket: val, UserID: userID, SessionID: sessionID, DeviceID: deviceID}
	b, err := json.Marshal(t)
	if err != nil {
		return nil, err
	}
	if err := s.rdb.Set(ctx, StreamTicketKeyPrefix+val, b, StreamTicketTTL).Err(); err != nil {
		return nil, err
	}
	return t, nil
}

// Consume 消费一张票据：命中即删除（单次使用），不存在/已过期返回 (nil, nil)。
func (s *TicketStore) Consume(ctx context.Context, ticket string) (*StreamTicket, error) {
	if ticket == "" {
		return nil, nil
	}
	res, err := consumeScript.Run(ctx, s.rdb, []string{StreamTicketKeyPrefix + ticket}).Result()
	if err != nil {
		if err == goredis.Nil {
			return nil, nil
		}
		return nil, err
	}
	raw, ok := res.(string)
	if !ok || raw == "" {
		return nil, nil
	}
	var t StreamTicket
	if err := json.Unmarshal([]byte(raw), &t); err != nil {
		return nil, err
	}
	return &t, nil
}
