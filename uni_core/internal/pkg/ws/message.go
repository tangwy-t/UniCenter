// Package ws provides WebSocket connection management and real-time push.
package ws

import (
	"encoding/json"
	"strconv"
)

// Message type constants for client->server communication.
const (
	MsgTypeAuth = "auth"
	MsgTypePing = "ping"
)

// Message type constants for server->client communication.
const (
	MsgTypeAuthOK    = "auth_ok"
	MsgTypeAuthErr   = "auth_err"
	MsgTypeNewNotice = "new_notice"
	MsgTypePong      = "pong"
	MsgTypeKicked    = "kicked"
)

// ClientMessage is the JSON message sent from client to server.
type ClientMessage struct {
	Type  string `json:"type"`
	Token string `json:"token,omitempty"`
}

// ServerMessage is the JSON message sent from server to client.
// Data 为已序列化的 JSON 片段;无载荷消息 Data 为 nil 时省略 data 键,
// 与旧 interface{} + omitempty 行为逐字节一致。
type ServerMessage struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// ─── 各消息类型的专用载荷结构(字段即协议,勿改 JSON tag)─────────────

// authOKPayload auth_ok 载荷。
// user_id 以字符串传输:snowflake ID 超过 JavaScript Number.MAX_SAFE_INTEGER
// (2^53-1),按 JSON number 序列化会丢精度,前端已按字符串解析。
type authOKPayload struct {
	UserID string `json:"user_id"`
}

// messagePayload auth_err / kicked 共用的 {message} 载荷。
type messagePayload struct {
	Message string `json:"message"`
}

// marshalServerMessage 把载荷序列化后装入 ServerMessage。
// data 为 nil 时省去 data 键(pong 等无载荷消息)。
func marshalServerMessage(msgType string, data any) ([]byte, error) {
	msg := ServerMessage{Type: msgType}
	if data != nil {
		raw, err := json.Marshal(data)
		if err != nil {
			return nil, err
		}
		msg.Data = raw
	}
	return json.Marshal(msg)
}

// MarshalAuthOK 构造 auth_ok 报文。
func MarshalAuthOK(userID uint64) ([]byte, error) {
	return marshalServerMessage(MsgTypeAuthOK, authOKPayload{UserID: strconv.FormatUint(userID, 10)})
}

// MarshalAuthErr 构造 auth_err 报文。
func MarshalAuthErr(message string) ([]byte, error) {
	return marshalServerMessage(MsgTypeAuthErr, messagePayload{Message: message})
}

// MarshalKicked 构造 kicked 报文(他端登录/被踢通知)。
func MarshalKicked(reason string) ([]byte, error) {
	return marshalServerMessage(MsgTypeKicked, messagePayload{Message: reason})
}

// MarshalPong 构造 pong 报文(无载荷)。
func MarshalPong() ([]byte, error) {
	return marshalServerMessage(MsgTypePong, nil)
}

// MarshalNewNotice 构造 new_notice 报文;noticeData 为通知的完整 JSON 对象
// (service 层已序列化好,直接原样嵌入 data,不做二次加工)。
func MarshalNewNotice(noticeData json.RawMessage) ([]byte, error) {
	return marshalServerMessage(MsgTypeNewNotice, noticeData)
}

// PushEvent is the message published to Redis Pub/Sub for cross-instance broadcast.
// When IsAll is true, the receiving Hub pushes to all online users.
// When IsAll is false, the receiving Hub pushes only to the specified UserIDs.
type PushEvent struct {
	Type       string          `json:"type"`
	NoticeData json.RawMessage `json:"notice_data"`
	UserIDs    []uint64        `json:"user_ids,omitempty"`
	IsAll      bool            `json:"is_all"`
}

// KickEvent is published to Redis channel "user:kick" for cross-instance single sign-on.
type KickEvent struct {
	UserID    uint64 `json:"user_id"`
	Reason    string `json:"reason"`
	KickToken string `json:"kick_token,omitempty"`
}
