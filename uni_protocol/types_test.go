package agentproto

import "testing"

func TestTypeConstantsAreStable(t *testing.T) {
	cases := map[string]string{
		"TypeAgentHello":         TypeAgentHello,
		"TypeCoreHelloAck":       TypeCoreHelloAck,
		"TypeAgentHeartbeat":     TypeAgentHeartbeat,
		"TypeAgentReportMetrics": TypeAgentReportMetrics,
	}
	want := map[string]string{
		"TypeAgentHello":         "agent.hello",
		"TypeCoreHelloAck":       "core.hello_ack",
		"TypeAgentHeartbeat":     "agent.heartbeat",
		"TypeAgentReportMetrics": "agent.report.metrics",
	}
	for name, got := range cases {
		if got != want[name] {
			t.Errorf("%s = %q, want %q（type 串是线上契约，改名即破坏性变更）", name, got, want[name])
		}
	}
}

func TestValidTypeName(t *testing.T) {
	valid := []string{"agent.hello", "core.hello_ack", "a.b.c", "a1_b2"}
	for _, s := range valid {
		if !ValidTypeName(s) {
			t.Errorf("ValidTypeName(%q) = false, want true", s)
		}
	}
	invalid := []string{"", "Agent.Hello", "agent hello", "agent-hello", "agent.hello!", "агент.hello"}
	for _, s := range invalid {
		if ValidTypeName(s) {
			t.Errorf("ValidTypeName(%q) = true, want false", s)
		}
	}
	long := make([]byte, MaxTypeLen+1)
	for i := range long {
		long[i] = 'a'
	}
	if ValidTypeName(string(long)) {
		t.Errorf("ValidTypeName 未拒绝超过 %d 字节的 type", MaxTypeLen)
	}
}

func TestDirectionZeroValueIsUnknown(t *testing.T) {
	var d Direction
	if d != DirUnknown {
		t.Fatal("Direction 零值必须是 DirUnknown（避免默认值被误判为某个方向）")
	}
}
