package agentproto

import (
	"fmt"
	"sort"
	"unicode/utf8"
)

// 标准关闭码与业务关闭码（4000-4999 段，见 RFC 6455 §7.4.2）。
const (
	CloseNormal    = 1000
	CloseGoingAway = 1001

	CloseVersionMismatch   = 4000 // v 不在受支持区间
	CloseUnauthorized      = 4001 // token 缺失/非法/过期，或设备被停用/删除
	CloseMalformedMessage  = 4002 // JSON 非法或信封校验失败
	CloseUnsupportedType   = 4003 // 收到 core.*（方向错误）或回环消息
	CloseDuplicateInstance = 4004 // 同 instance_id 顶号
	CloseHeartbeatTimeout  = 4005 // 最后活跃时间超时
	CloseRateLimited       = 4006 // 限流
	CloseServerShutdown    = 4007 // 服务端停机，agent 应退避重连
	CloseProtocolViolation = 4008 // hello 之前发其它消息 / 重复 hello
)

// MaxCloseReasonBytes 是 WebSocket 关闭原因串的字节上限。
const MaxCloseReasonBytes = 123

var closeReasons = map[int]string{
	CloseVersionMismatch:   "protocol version not supported",
	CloseUnauthorized:      "unauthorized",
	CloseMalformedMessage:  "malformed message",
	CloseUnsupportedType:   "unsupported or wrong-direction message type",
	CloseDuplicateInstance: "duplicate instance",
	CloseHeartbeatTimeout:  "heartbeat timeout",
	CloseRateLimited:       "rate limited",
	CloseServerShutdown:    "server shutting down",
	CloseProtocolViolation: "protocol violation",
}

// AllCloseCodes 返回升序排列的全部业务关闭码（不含 1000/1001）。
func AllCloseCodes() []int {
	out := make([]int, 0, len(closeReasons))
	for code := range closeReasons {
		out = append(out, code)
	}
	sort.Ints(out)
	return out
}

// CloseReason 返回 "code|human" 形式的原因串，并按 UTF-8 边界截断到 MaxCloseReasonBytes。
func CloseReason(code int) string {
	human, ok := closeReasons[code]
	if !ok {
		human = "closed"
	}
	return truncateUTF8(fmt.Sprintf("%d|%s", code, human), MaxCloseReasonBytes)
}

// truncateUTF8 按字节上限截断，且保证结果是合法 UTF-8。
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	b := []byte(s)[:maxBytes]
	for len(b) > 0 && !utf8.Valid(b) {
		b = b[:len(b)-1]
	}
	return string(b)
}
