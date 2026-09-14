package agentproto

import (
	"errors"
	"fmt"
)

// 契约层哨兵错误：调用方一律用 errors.Is 判断，不做字符串匹配。
var (
	// ErrMalformed 表示 JSON 语法/结构损坏，或字段存在但格式非法。
	ErrMalformed = errors.New("agentproto: malformed message")
	// ErrMissingField 表示必填字段缺失（配合 DecodeError.Field 定位）。
	ErrMissingField = errors.New("agentproto: missing required field")
	// ErrUnsupportedVersion 表示信封 v 不在受支持区间内。
	ErrUnsupportedVersion = errors.New("agentproto: unsupported protocol version")
	// ErrUnknownType 表示请求解码一个未在注册表中登记的消息类型。
	ErrUnknownType = errors.New("agentproto: unknown message type")
	// ErrInvalidID 表示 id 不是合法的十进制无符号整数串。
	ErrInvalidID = errors.New("agentproto: invalid id")
	// ErrInvalidTimestamp 表示 ts 非法（缺失/非正/越界）。
	ErrInvalidTimestamp = errors.New("agentproto: invalid timestamp")
	// ErrMessageTooLarge 表示消息超过 MaxMessageBytes。
	ErrMessageTooLarge = errors.New("agentproto: message too large")
	// ErrInvalidPayload 表示结构合法但语义非法（Validate 返回）。
	ErrInvalidPayload = errors.New("agentproto: invalid payload")
	// ErrUnrepresentable 表示值无法表示（例如编码 NaN/±Inf）。
	ErrUnrepresentable = errors.New("agentproto: unrepresentable value")
)

// Stage 标出失败发生在契约的哪一层，便于日志与告警分类。
type Stage string

const (
	StageEnvelope Stage = "envelope"
	StageType     Stage = "type"
	StageVersion  Stage = "version"
	StagePayload  Stage = "payload"
)

// DecodeError 是契约层唯一的错误类型：携带定位信息，且可被 errors.Is/As 归类。
type DecodeError struct {
	Stage    Stage
	Field    string
	Sentinel error
	Cause    error
}

func (e *DecodeError) Error() string {
	msg := fmt.Sprintf("agentproto[%s]: %v", e.Stage, e.Sentinel)
	if e.Field != "" {
		msg += fmt.Sprintf(" (field=%s)", e.Field)
	}
	if e.Cause != nil {
		msg += ": " + e.Cause.Error()
	}
	return msg
}

// Unwrap 同时暴露哨兵与底层原因，使 errors.Is(err, ErrXxx) 与 errors.As(err, &底层错误) 都成立。
func (e *DecodeError) Unwrap() []error {
	if e.Cause == nil {
		return []error{e.Sentinel}
	}
	return []error{e.Sentinel, e.Cause}
}

func decodeErr(stage Stage, field string, sentinel error) *DecodeError {
	return &DecodeError{Stage: stage, Field: field, Sentinel: sentinel}
}

func decodeErrf(stage Stage, field string, sentinel, cause error) *DecodeError {
	return &DecodeError{Stage: stage, Field: field, Sentinel: sentinel, Cause: cause}
}
