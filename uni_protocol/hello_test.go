package agentproto

import (
	"errors"
	"testing"
)

func baseHello() *Hello {
	return &Hello{
		InstanceID:   "6f1c0b2e-0000-4000-8000-000000000001",
		Hostname:     "web-01",
		OS:           "linux",
		Arch:         "amd64",
		AgentVersion: "0.1.0",
	}
}

func TestHelloCredentialThreeStates(t *testing.T) {
	// 都没有 → 拒
	if _, err := baseHello().Credential(); !errors.Is(err, ErrMissingField) {
		t.Fatalf("两个 token 都缺失应返回 ErrMissingField，实际 %v", err)
	}
	// 都有 → 拒（歧义，不暗自取优先级）
	h := baseHello()
	h.EnrollToken = "enroll"
	h.AgentToken = "agent"
	if _, err := h.Credential(); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("两个 token 同时存在应返回 ErrInvalidPayload，实际 %v", err)
	}
	// 只有一个 → 通过并给出种类
	h = baseHello()
	h.EnrollToken = "enroll"
	if k, err := h.Credential(); err != nil || k != CredentialEnroll {
		t.Fatalf("仅 enroll_token 应判为 CredentialEnroll，实际 k=%v err=%v", k, err)
	}
	h = baseHello()
	h.AgentToken = "agent"
	if k, err := h.Credential(); err != nil || k != CredentialAgent {
		t.Fatalf("仅 agent_token 应判为 CredentialAgent，实际 k=%v err=%v", k, err)
	}
}

func TestHelloValidateRequiredFields(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Hello)
		want error
	}{
		{"缺 instance_id", func(h *Hello) { h.InstanceID = "" }, ErrMissingField},
		{"缺 hostname", func(h *Hello) { h.Hostname = "" }, ErrMissingField},
		{"缺 os", func(h *Hello) { h.OS = "" }, ErrMissingField},
		{"缺 arch", func(h *Hello) { h.Arch = "" }, ErrMissingField},
		{"缺 agent_version", func(h *Hello) { h.AgentVersion = "" }, ErrMissingField},
		{"cpu_cores 为负", func(h *Hello) { h.CPUCores = -1 }, ErrInvalidPayload},
		{"mem_total_mb 为负", func(h *Hello) { h.MemTotalMB = -1 }, ErrInvalidPayload},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := baseHello()
			h.EnrollToken = "enroll"
			c.mut(h)
			err := h.Validate()
			if err == nil {
				t.Fatal("期望失败但通过了")
			}
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want errors.Is(..., %v)", err, c.want)
			}
		})
	}
	if err := func() error { h := baseHello(); h.EnrollToken = "enroll"; return h.Validate() }(); err != nil {
		t.Fatalf("合法 Hello 不应失败: %v", err)
	}
}

func TestHelloAck_Accept(t *testing.T) {
	ok := &HelloAck{Accepted: true, DeviceID: "1234567890", ReportInterval: 10, V: CurrentVersion}
	if err := ok.Validate(); err != nil {
		t.Fatalf("合法 accepted ack 不应失败: %v", err)
	}
	bad := &HelloAck{Accepted: true, DeviceID: "", ReportInterval: 10}
	if err := bad.Validate(); !errors.Is(err, ErrMissingField) {
		t.Fatalf("accepted=true 缺 device_id 应报 ErrMissingField，实际 %v", err)
	}
	tooSmall := &HelloAck{Accepted: true, DeviceID: "1", ReportInterval: 1}
	if err := tooSmall.Validate(); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("report_interval=1 应报 ErrInvalidPayload（最小 2），实际 %v", err)
	}
}

func TestHelloAck_Reject(t *testing.T) {
	bad := &HelloAck{Accepted: false}
	if err := bad.Validate(); !errors.Is(err, ErrMissingField) {
		t.Fatalf("accepted=false 必须带 reject_reason，实际 %v", err)
	}
	ok := &HelloAck{Accepted: false, RejectReason: "invalid enroll token"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("合法 rejected ack 不应失败: %v", err)
	}
}
