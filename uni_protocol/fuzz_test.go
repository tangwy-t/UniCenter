package agentproto

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
)

// FuzzDecodeEnvelope 断言：任意字节输入都不得 panic；失败必须是 typed error；
// 成功时必须返回零值以外的合法信封，且 roundtrip 稳定。
func FuzzDecodeEnvelope(f *testing.F) {
	f.Add([]byte(`{"v":1,"id":"1","type":"agent.heartbeat","ts":1,"data":{}}`))
	f.Add([]byte(`{`))
	f.Add([]byte(``))
	f.Add([]byte(`null`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`{"v":1,"id":"1","type":"agent.heartbeat","ts":1,"data":[]}`))
	f.Add([]byte(`{"v":999,"id":"1","type":"agent.heartbeat","ts":1,"data":{}}`))

	f.Fuzz(func(t *testing.T, raw []byte) {
		m, err := Decode(raw)
		if err != nil {
			if m != nil {
				t.Fatalf("失败时必须返回 nil Message，实际 %+v", m)
			}
			var de *DecodeError
			if !errors.As(err, &de) {
				t.Fatalf("错误必须是 *DecodeError，实际 %T: %v", err, err)
			}
			return
		}
		if m == nil {
			t.Fatal("成功时不得返回 nil Message")
		}
		if !IsSupported(m.V) || m.ID == "" || m.Type == "" || m.TS <= 0 {
			t.Fatalf("成功返回的信封不自洽: %+v", m)
		}
		out, err := m.Marshal()
		if err != nil {
			t.Fatalf("已通过校验的信封必须可被编码: %v", err)
		}
		m2, err := Decode(out)
		if err != nil {
			t.Fatalf("自编码结果必须可被解码: %v", err)
		}
		if m.V != m2.V || m.ID != m2.ID || m.Type != m2.Type || m.TS != m2.TS {
			t.Fatalf("roundtrip 不稳定: %+v vs %+v", m, m2)
		}
	})
}

// FuzzDecodeTyped 断言：任意载荷字节都不会 panic，且错误可归类。
func FuzzDecodeTyped(f *testing.F) {
	f.Add([]byte(`{"instance_id":"i","hostname":"h","os":"linux","arch":"amd64","agent_version":"0.1.0","enroll_token":"e"}`))
	f.Add([]byte(`{"t":1}`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`{"cpu_used_percent":-1}`))

	f.Fuzz(func(t *testing.T, payload []byte) {
		m := &Message{V: CurrentVersion, ID: "1", Type: TypeAgentHello, TS: 1, Data: json.RawMessage(payload)}
		got, err := DecodeTyped(m)
		if err != nil {
			var de *DecodeError
			if !errors.As(err, &de) {
				t.Fatalf("错误必须是 *DecodeError，实际 %T: %v", err, err)
			}
			return
		}
		if got == nil {
			t.Fatal("成功时不得返回 nil 载荷")
		}
	})
}

// FuzzIsSupported 断言：任意 int 都不得 panic，且结果确定。
func FuzzIsSupported(f *testing.F) {
	f.Add(1)
	f.Add(0)
	f.Add(-1)
	f.Add(math.MaxInt32)

	f.Fuzz(func(t *testing.T, v int) {
		a := IsSupported(v)
		b := IsSupported(v)
		if a != b {
			t.Fatalf("IsSupported(%d) 结果不确定", v)
		}
	})
}
