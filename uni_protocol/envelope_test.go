package agentproto

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func validEnvelopeJSON() []byte {
	ts := time.Now().UnixMilli()
	return []byte(`{"v":1,"id":"1234567890","type":"agent.heartbeat","ts":` +
		jsonItoa(ts) + `,"data":{}}`)
}

func jsonItoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}

func TestDecodeValidEnvelope(t *testing.T) {
	m, err := Decode(validEnvelopeJSON())
	if err != nil {
		t.Fatalf("Decode 失败: %v", err)
	}
	if m.V != CurrentVersion || m.ID != "1234567890" || m.Type != TypeAgentHeartbeat {
		t.Fatalf("字段不符: %+v", m)
	}
	if m.TS <= 0 {
		t.Fatalf("ts 未解析: %d", m.TS)
	}
}

func TestDecodeRejectsMissingFields(t *testing.T) {
	cases := []struct {
		name string
		body string
		want error
	}{
		{"缺 v", `{"id":"1","type":"agent.heartbeat","ts":1,"data":{}}`, ErrUnsupportedVersion},
		{"v 不支持", `{"v":99,"id":"1","type":"agent.heartbeat","ts":1,"data":{}}`, ErrUnsupportedVersion},
		{"缺 id", `{"v":1,"type":"agent.heartbeat","ts":1,"data":{}}`, ErrMissingField},
		{"缺 type", `{"v":1,"id":"1","ts":1,"data":{}}`, ErrMissingField},
		{"ts 为 0", `{"v":1,"id":"1","type":"agent.heartbeat","ts":0,"data":{}}`, ErrInvalidTimestamp},
		{"type 字符非法", `{"v":1,"id":"1","type":"Agent Hello","ts":1,"data":{}}`, ErrMalformed},
		{"data 是数组", `{"v":1,"id":"1","type":"agent.heartbeat","ts":1,"data":[]}`, ErrMalformed},
		{"data 是标量", `{"v":1,"id":"1","type":"agent.heartbeat","ts":1,"data":1}`, ErrMalformed},
		{"JSON 损坏", `{"v":1,"id":"1"`, ErrMalformed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, err := Decode([]byte(c.body))
			if err == nil {
				t.Fatalf("期望失败但成功了: %+v", m)
			}
			if m != nil {
				t.Fatal("失败时必须返回 nil Message，绝不返回半成品")
			}
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want errors.Is(..., %v)", err, c.want)
			}
		})
	}
}

func TestEmptyPayloadThreeStatesAreAllValid(t *testing.T) {
	base := `{"v":1,"id":"1","type":"agent.heartbeat","ts":1`
	for _, suffix := range []string{`}`, `,"data":null}`, `,"data":{}}`} {
		if _, err := Decode([]byte(base + suffix)); err != nil {
			t.Fatalf("空载荷三态之一应当合法，suffix=%q err=%v", suffix, err)
		}
	}
}

func TestDecodeRejectsOversizedMessage(t *testing.T) {
	big := make([]byte, MaxMessageBytes+1)
	for i := range big {
		big[i] = ' '
	}
	_, err := Decode(big)
	if !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("err = %v, want ErrMessageTooLarge", err)
	}
}

func TestRoundTripIsByteStable(t *testing.T) {
	m, err := Decode(validEnvelopeJSON())
	if err != nil {
		t.Fatal(err)
	}
	b1, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	m2, err := Decode(b1)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := m2.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if string(b1) != string(b2) {
		t.Fatalf("二次编码不稳定:\n%s\n%s", b1, b2)
	}
}

func TestDecodeData(t *testing.T) {
	type payload struct {
		InstanceID string `json:"instance_id"`
	}
	m, err := NewMessage("1", TypeAgentHello, payload{InstanceID: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	var out payload
	if err := m.DecodeData(&out); err != nil {
		t.Fatal(err)
	}
	if out.InstanceID != "abc" {
		t.Fatalf("DecodeData 结果不符: %+v", out)
	}
	// 空载荷不应报错，也不应改动 out
	hb, err := NewMessage("2", TypeAgentHeartbeat, nil)
	if err != nil {
		t.Fatal(err)
	}
	out.InstanceID = "keep"
	if err := hb.DecodeData(&out); err != nil {
		t.Fatal(err)
	}
	if out.InstanceID != "keep" {
		t.Fatal("空载荷时 DecodeData 不应改动 out")
	}
}

func TestNewMessageWritesEmptyObjectForNilPayload(t *testing.T) {
	m, err := NewMessage("1", TypeAgentHeartbeat, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(m.Data) != "{}" {
		t.Fatalf("nil 载荷应写 {}，实际 %q", string(m.Data))
	}
}

func TestExceedsClockSkew(t *testing.T) {
	now := int64(1_700_000_000_000)
	if ExceedsClockSkew(now, now) {
		t.Fatal("零偏差不应超限")
	}
	if ExceedsClockSkew(now+MaxClockSkewMs, now) {
		t.Fatal("正好等于上限不应超限（闭区间）")
	}
	if !ExceedsClockSkew(now+MaxClockSkewMs+1, now) {
		t.Fatal("超过上限应超限")
	}
	if !ExceedsClockSkew(now-MaxClockSkewMs-1, now) {
		t.Fatal("过去方向超过上限应超限")
	}
}

func TestValidateDoesNotEnforceClockSkew(t *testing.T) {
	// 时钟偏差是软校验：契约层不得据此拒绝消息（否则 golden 文件无法长期复用）
	old := `{"v":1,"id":"1","type":"agent.heartbeat","ts":1,"data":{}}`
	if _, err := Decode([]byte(old)); err != nil {
		t.Fatalf("陈旧时间戳不应被拒绝: %v", err)
	}
	if !strings.Contains(validEnvelopeJSONString(), `"type":"agent.heartbeat"`) {
		t.Fatal("测试辅助函数损坏")
	}
}

func validEnvelopeJSONString() string { return string(validEnvelopeJSON()) }
