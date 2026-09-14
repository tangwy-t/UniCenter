package agentproto

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestDecodeErrIsClassifiable(t *testing.T) {
	err := decodeErr(StageEnvelope, "id", ErrMissingField)

	if !errors.Is(err, ErrMissingField) {
		t.Fatal("errors.Is 未命中哨兵 ErrMissingField")
	}

	var de *DecodeError
	if !errors.As(err, &de) {
		t.Fatal("errors.As 未命中 *DecodeError")
	}
	if de.Stage != StageEnvelope || de.Field != "id" {
		t.Fatalf("定位信息丢失: stage=%q field=%q", de.Stage, de.Field)
	}
}

func TestDecodeErrKeepsCauseAndSentinel(t *testing.T) {
	cause := &json.SyntaxError{}
	err := decodeErrf(StageEnvelope, "", ErrMalformed, cause)

	if !errors.Is(err, ErrMalformed) {
		t.Fatal("带 cause 时 errors.Is(哨兵) 必须仍成立")
	}
	if !errors.As(err, &cause) {
		t.Fatal("带 cause 时底层原因必须可通过 errors.As 取出")
	}
}

func TestDecodeErrorStringIsNonEmpty(t *testing.T) {
	err := decodeErr(StagePayload, "cpu_used_percent", ErrInvalidPayload)
	if err.Error() == "" {
		t.Fatal("Error() 不应为空")
	}
	if !contains(err.Error(), string(StagePayload)) || !contains(err.Error(), "cpu_used_percent") {
		t.Fatalf("Error() 应包含 stage 与 field: %q", err.Error())
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
