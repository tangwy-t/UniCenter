package agentproto

import (
	"errors"
	"regexp"
	"testing"
)

// 这是一条「防漂移守卫」：新增消息类型必须显式登记到注册表，否则本测试失败。
func TestRegistryIsCompleteAndConsistent(t *testing.T) {
	all := AllTypes()
	if len(all) == 0 {
		t.Fatal("AllTypes() 为空")
	}
	seen := map[string]bool{}
	for _, ty := range all {
		if seen[ty] {
			t.Fatalf("AllTypes() 含重复项: %q", ty)
		}
		seen[ty] = true

		spec, ok := LookupType(ty)
		if !ok {
			t.Fatalf("AllTypes() 中的 %q 无法 LookupType", ty)
		}
		if spec.Type != ty {
			t.Fatalf("TypeSpec.Type 与注册键不一致: %q vs %q", spec.Type, ty)
		}
		if spec.New == nil {
			t.Fatalf("%q 缺少 New 工厂", ty)
		}
		if got := spec.New(); got == nil {
			t.Fatalf("%q 的 New 返回 nil", ty)
		}
		if spec.Direction != DirAgentToCore && spec.Direction != DirCoreToAgent {
			t.Fatalf("%q 的 Direction 未设定: %v", ty, spec.Direction)
		}
		// 方向前缀必须与声明一致
		wantPrefix := "agent."
		if spec.Direction == DirCoreToAgent {
			wantPrefix = "core."
		}
		if len(ty) < len(wantPrefix) || ty[:len(wantPrefix)] != wantPrefix {
			t.Fatalf("%q 的前缀与 Direction %v 不符", ty, spec.Direction)
		}
	}

	// 反向：每个常量都必须在 AllTypes 里
	for _, c := range []string{TypeAgentHello, TypeCoreHelloAck, TypeAgentHeartbeat, TypeAgentReportMetrics,
		TypeCoreAgentUpgrade, TypeAgentUpgradeStatus} {
		if !seen[c] {
			t.Fatalf("常量 %q 未登记进 AllTypes()（新增类型必须显式登记）", c)
		}
	}
}

func TestTypeNameCharsetMatchesRegistry(t *testing.T) {
	re := regexp.MustCompile(`^[a-z0-9_.]+$`)
	for _, ty := range AllTypes() {
		if !re.MatchString(ty) {
			t.Fatalf("%q 不符合 type 字符集规范", ty)
		}
		if !ValidTypeName(ty) {
			t.Fatalf("%q 未通过 ValidTypeName", ty)
		}
	}
}

func TestIsKnownTypeAndDirectionOf(t *testing.T) {
	if !IsKnownType(TypeAgentHello) {
		t.Fatal("TypeAgentHello 应被识别")
	}
	if IsKnownType("agent.nonexistent") {
		t.Fatal("未知类型不应被识别")
	}
	if DirectionOf(TypeAgentHello) != DirAgentToCore {
		t.Fatal("agent.hello 方向应为 DirAgentToCore")
	}
	if DirectionOf(TypeCoreHelloAck) != DirCoreToAgent {
		t.Fatal("core.hello_ack 方向应为 DirCoreToAgent")
	}
	if DirectionOf("nope") != DirUnknown {
		t.Fatal("未知类型方向应为 DirUnknown")
	}
}

func TestDecodeTyped(t *testing.T) {
	h := &Hello{InstanceID: "i", Hostname: "n", OS: "linux", Arch: "amd64", AgentVersion: "0.1.0", EnrollToken: "e"}
	m, err := NewMessage("1", TypeAgentHello, h)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeTyped(m)
	if err != nil {
		t.Fatalf("DecodeTyped 失败: %v", err)
	}
	hh, ok := got.(*Hello)
	if !ok {
		t.Fatalf("类型不符: %T", got)
	}
	if hh.InstanceID != "i" {
		t.Fatalf("载荷未解码: %+v", hh)
	}

	// 未知类型 → ErrUnknownType
	m2, err := NewMessage("2", "agent.unknown", nil)
	if err != nil {
		// NewMessage 自身不校验类型是否登记，但若失败也应能容忍
		t.Logf("NewMessage 对未知类型返回错误（可接受）: %v", err)
		return
	}
	if _, err := DecodeTyped(m2); !errors.Is(err, ErrUnknownType) {
		t.Fatalf("未知类型应返回 ErrUnknownType，实际 %v", err)
	}
}

func TestDecodeTypedRejectsWrongDirection(t *testing.T) {
	// core 收到 core.* 视为方向错误（回环/伪造）：必须与「未知类型」可区分，
	// 否则调用方无法把它映射成 CloseUnsupportedType(4003)。
	ack := &HelloAck{Accepted: true, DeviceID: "1", ReportInterval: 10}
	m, err := NewMessage("1", TypeCoreHelloAck, ack)
	if err != nil {
		t.Fatal(err)
	}
	_, err = DecodeTypedFor(m, DirAgentToCore)
	if !errors.Is(err, ErrWrongDirection) {
		t.Fatalf("方向不符应返回 ErrWrongDirection，实际 %v", err)
	}
	if _, err := DecodeTypedFor(m, DirCoreToAgent); err != nil {
		t.Fatalf("方向相符不应失败: %v", err)
	}
}
