package agentproto

import (
	"bytes"
	"encoding/json"
	"time"
)

const (
	// MaxMessageBytes 是单帧消息的字节上限（单帧单消息）。
	MaxMessageBytes = 1 << 20

	// MaxClockSkewMs 是信封 ts 与本地时钟的允许偏差（毫秒，软校验）。
	MaxClockSkewMs = 5 * 60 * 1000

	// MaxIDLen 是信封 id 的字节长度上限。
	MaxIDLen = 64
)

// Message 是 agent 与 core 之间所有消息的信封。
//
// Data 用 json.RawMessage 而非 interface{}：避免大整数被解成 float64 丢精度，
// 也让未知类型可以原样透传、载荷延迟到需要时再解码。
type Message struct {
	V    int             `json:"v"`
	ID   string          `json:"id"`
	Type string          `json:"type"`
	TS   int64           `json:"ts"`
	Data json.RawMessage `json:"data"`
}

// NewMessage 构造一条消息；data 为 nil 时 Data 写空对象 {}。
func NewMessage(id, typ string, data any) (*Message, error) {
	raw, err := MarshalPayload(data)
	if err != nil {
		return nil, err
	}
	m := &Message{
		V:    CurrentVersion,
		ID:   id,
		Type: typ,
		TS:   time.Now().UnixMilli(),
		Data: raw,
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return m, nil
}

// MarshalPayload 把任意载荷编码为 json.RawMessage；nil 编码为空对象。
func MarshalPayload(v any) (json.RawMessage, error) {
	if v == nil {
		return json.RawMessage(`{}`), nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, decodeErrf(StagePayload, "", ErrUnrepresentable, err)
	}
	return json.RawMessage(b), nil
}

// Marshal 编码并自校验。
func (m *Message) Marshal() ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, decodeErrf(StageEnvelope, "", ErrUnrepresentable, err)
	}
	if len(b) > MaxMessageBytes {
		return nil, decodeErr(StageEnvelope, "", ErrMessageTooLarge)
	}
	return b, nil
}

// Unmarshal 只解析信封，不做字段校验。
func Unmarshal(b []byte) (*Message, error) {
	if len(b) > MaxMessageBytes {
		return nil, decodeErr(StageEnvelope, "", ErrMessageTooLarge)
	}
	m := &Message{}
	if err := json.Unmarshal(b, m); err != nil {
		return nil, decodeErrf(StageEnvelope, "", ErrMalformed, err)
	}
	return m, nil
}

// Decode 解析并校验信封。失败时返回 (nil, 非 nil typed error)，绝不返回半成品。
func Decode(b []byte) (*Message, error) {
	m, err := Unmarshal(b)
	if err != nil {
		return nil, err
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return m, nil
}

// DecodeData 把 Data 解码到 out；空载荷时 out 保持不变。
func (m *Message) DecodeData(out any) error {
	if isDataEmpty(m.Data) {
		return nil
	}
	if err := json.Unmarshal(m.Data, out); err != nil {
		return decodeErrf(StagePayload, "", ErrInvalidPayload, err)
	}
	return nil
}

// Validate 校验信封结构。时钟偏差不在其列（见 ExceedsClockSkew）。
func (m *Message) Validate() error {
	if !IsSupported(m.V) {
		return decodeErr(StageVersion, "v", ErrUnsupportedVersion)
	}
	switch {
	case m.ID == "":
		return decodeErr(StageEnvelope, "id", ErrMissingField)
	case len(m.ID) > MaxIDLen:
		return decodeErr(StageEnvelope, "id", ErrMalformed)
	case !isDecimalID(m.ID):
		return decodeErr(StageEnvelope, "id", ErrInvalidID)
	}
	switch {
	case m.Type == "":
		return decodeErr(StageEnvelope, "type", ErrMissingField)
	case !ValidTypeName(m.Type):
		return decodeErr(StageEnvelope, "type", ErrMalformed)
	}
	if m.TS <= 0 {
		return decodeErr(StageEnvelope, "ts", ErrInvalidTimestamp)
	}
	return validateDataShape(m.Data)
}

// isDecimalID 报告 s 是否为非空的十进制无符号整数串。
// 雪花 id 一律以十进制字符串上线（避免 JS 精度丢失），故契约层拒绝其它形态：
// 这既让 ErrInvalidID 可达，也避免「看起来像 id 的任意串」进入日志与存储。
func isDecimalID(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// isDataEmpty 把 data 缺失 / null / 空串统一视为空载荷。
func isDataEmpty(data json.RawMessage) bool {
	t := bytes.TrimSpace(data)
	return len(t) == 0 || bytes.Equal(t, []byte("null"))
}

// validateDataShape 要求 data 顶层必须是对象（或空）。
func validateDataShape(data json.RawMessage) error {
	if isDataEmpty(data) {
		return nil
	}
	t := bytes.TrimSpace(data)
	if t[0] != '{' || !json.Valid(t) {
		return decodeErr(StageEnvelope, "data", ErrMalformed)
	}
	return nil
}

// ExceedsClockSkew 报告 ts 与 nowMs 的偏差是否超过 MaxClockSkewMs。
// 契约层不据此拒绝消息（软校验），由调用方计数与告警，且不得篡改 ts。
func ExceedsClockSkew(ts, nowMs int64) bool {
	d := ts - nowMs
	if d < 0 {
		d = -d
	}
	return d > MaxClockSkewMs
}
