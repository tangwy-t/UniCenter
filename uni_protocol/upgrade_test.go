package agentproto

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func validDirective() *UpgradeDirective {
	return &UpgradeDirective{
		RequestID:     "1234567890123456789",
		TargetVersion: "0.2.0",
		SHA256:        strings.Repeat("0123456789abcdef", 4),
		SizeBytes:     24117248,
	}
}

func validStatus(state string) *UpgradeStatus {
	s := &UpgradeStatus{
		RequestID:     "1234567890123456789",
		State:         state,
		TargetVersion: "0.2.0",
		FromVersion:   "0.1.0",
	}
	switch state {
	case UpgradeStateDownloading:
		p := 62
		s.Progress = &p
	case UpgradeStateFailed, UpgradeStateRolledBack:
		s.ReasonCode = ReasonDownloadFailed
	}
	return s
}

func TestUpgradeDirectiveValidate(t *testing.T) {
	if err := validDirective().Validate(); err != nil {
		t.Fatalf("合法指令不应失败: %v", err)
	}
	cases := []struct {
		name string
		mut  func(*UpgradeDirective)
		want error
	}{
		{"request_id 非十进制", func(d *UpgradeDirective) { d.RequestID = "abc" }, ErrMissingField},
		{"request_id 为空", func(d *UpgradeDirective) { d.RequestID = "" }, ErrMissingField},
		{"目标版本缺段", func(d *UpgradeDirective) { d.TargetVersion = "0.2" }, ErrInvalidPayload},
		{"目标版本为 dev", func(d *UpgradeDirective) { d.TargetVersion = "dev" }, ErrInvalidPayload},
		{"摘要大写", func(d *UpgradeDirective) { d.SHA256 = strings.ToUpper(d.SHA256) }, ErrInvalidPayload},
		{"摘要长度不足", func(d *UpgradeDirective) { d.SHA256 = d.SHA256[:63] }, ErrInvalidPayload},
		{"产物大小为零", func(d *UpgradeDirective) { d.SizeBytes = 0 }, ErrInvalidPayload},
		{"产物大小为负", func(d *UpgradeDirective) { d.SizeBytes = -1 }, ErrInvalidPayload},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := validDirective()
			c.mut(d)
			if err := d.Validate(); !errors.Is(err, c.want) {
				t.Fatalf("期望 %v，实际 %v", c.want, err)
			}
		})
	}
}

func TestUpgradeStatusValidateAcceptsEveryState(t *testing.T) {
	for _, state := range AllUpgradeStates() {
		s := validStatus(state)
		if err := s.Validate(); err != nil {
			t.Fatalf("状态 %s 的合法载荷被拒: %v", state, err)
		}
	}
}

func TestUpgradeStatusValidate(t *testing.T) {
	bad := 101
	neg := -1
	cases := []struct {
		name string
		mut  func(*UpgradeStatus)
		want error
	}{
		{"request_id 非十进制", func(s *UpgradeStatus) { s.RequestID = "x1" }, ErrMissingField},
		{"状态不在白名单", func(s *UpgradeStatus) { s.State = "succeeded" }, ErrInvalidPayload},
		{"目标版本非法", func(s *UpgradeStatus) { s.TargetVersion = "0.2" }, ErrInvalidPayload},
		{"原版本非法", func(s *UpgradeStatus) { s.FromVersion = "dev" }, ErrInvalidPayload},
		{"进度出现在校验阶段", func(s *UpgradeStatus) { s.State = UpgradeStateVerifying }, ErrInvalidPayload},
		{"进度越上界", func(s *UpgradeStatus) { s.Progress = &bad }, ErrInvalidPayload},
		{"进度为负", func(s *UpgradeStatus) { s.Progress = &neg }, ErrInvalidPayload},
		{"失败缺原因码", func(s *UpgradeStatus) { s.State = UpgradeStateFailed; s.Progress = nil }, ErrMissingField},
		{"原因码不在白名单", func(s *UpgradeStatus) {
			s.State = UpgradeStateFailed
			s.Progress = nil
			s.ReasonCode = "made_up_code"
		}, ErrInvalidPayload},
		{"中间阶段带原因码", func(s *UpgradeStatus) { s.ReasonCode = ReasonDownloadFailed }, ErrInvalidPayload},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := validStatus(UpgradeStateDownloading)
			c.mut(s)
			if err := s.Validate(); !errors.Is(err, c.want) {
				t.Fatalf("期望 %v，实际 %v", c.want, err)
			}
		})
	}
}

// succeeded 永远不该出现在设备上报里：成功由服务端在下一次 hello 用版本号裁决。
// 这条守卫防的是「有人为了省事把终态的成功也塞进上报」。
func TestUpgradeStatusHasNoSucceededState(t *testing.T) {
	if IsUpgradeState("succeeded") {
		t.Fatal("状态白名单不得包含 succeeded —— 成功只能在 hello 里裁决")
	}
}

// Progress 用指针：nil（本阶段没有百分比）与 0（真的下到 0%）必须可区分。
func TestUpgradeStatusProgressNilDistinctFromZero(t *testing.T) {
	zero := 0
	withZero := validStatus(UpgradeStateDownloading)
	withZero.Progress = &zero
	raw, err := json.Marshal(withZero)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"progress":0`) {
		t.Fatalf("进度为 0 必须出现在线上形态里，实际 %s", raw)
	}

	noProgress := validStatus(UpgradeStateDownloading)
	noProgress.Progress = nil
	raw, err = json.Marshal(noProgress)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "progress") {
		t.Fatalf("无进度时不得出现 progress 键，实际 %s", raw)
	}
}

// 状态与原因码各只有一处枚举源：常量清单 ↔ 白名单必须双向一致（漏一个即红）。
func TestUpgradeEnumsAreSingleSourced(t *testing.T) {
	wantStates := []string{
		"downloading", "verifying", "installing", "restarting", "failed", "rolled_back",
	}
	if got := AllUpgradeStates(); len(got) != len(wantStates) {
		t.Fatalf("状态数 %d，期望 %d（新增状态必须同时进白名单）", len(got), len(wantStates))
	}
	for _, s := range wantStates {
		if !IsUpgradeState(s) {
			t.Fatalf("状态 %q 不在白名单", s)
		}
	}
	if IsUpgradeState("") || IsUpgradeState("pending") || IsUpgradeState("timeout") {
		t.Fatal("pending/timeout 是服务端内部态，不得进协议白名单")
	}

	wantReasons := []string{
		"download_failed", "checksum_mismatch", "smoke_test_failed", "no_write_permission",
		"replace_failed", "exec_failed", "not_connected_after_upgrade",
		"platform_unsupported", "artifact_missing",
	}
	if got := AllUpgradeReasonCodes(); len(got) != len(wantReasons) {
		t.Fatalf("原因码数 %d，期望 %d（新增原因码必须同时进白名单）", len(got), len(wantReasons))
	}
	for _, c := range wantReasons {
		if !IsUpgradeReasonCode(c) {
			t.Fatalf("原因码 %q 不在白名单", c)
		}
	}
	if IsUpgradeReasonCode("superseded") || IsUpgradeReasonCode("timeout") {
		t.Fatal("服务端推导的原因码不得进协议白名单")
	}
}

func TestNormalizeReasonDetail(t *testing.T) {
	if got := NormalizeReasonDetail("  boom  "); got != "boom" {
		t.Fatalf("应去掉首尾空白，实际 %q", got)
	}
	long := strings.Repeat("x", 300)
	if got := NormalizeReasonDetail(long); len(got) != 255 {
		t.Fatalf("应截断到 255 字节，实际 %d", len(got))
	}
}

func TestSHA256HexValidation(t *testing.T) {
	if !IsSHA256Hex(strings.Repeat("0", 64)) {
		t.Fatal("64 位小写 hex 应通过")
	}
	for _, bad := range []string{
		strings.Repeat("A", 64), // 大写
		strings.Repeat("0", 63), // 短
		strings.Repeat("0", 65), // 长
		strings.Repeat("g", 64), // 非 hex
		"sha256:" + strings.Repeat("0", 57),
	} {
		if IsSHA256Hex(bad) {
			t.Fatalf("%q 应被拒绝", bad)
		}
	}
}

// 方向守卫：指令只能 core→agent，状态只能 agent→core；反向解码必须得到
// ErrWrongDirection（与「未知类型」区分开，后者要忽略不断连）。
func TestUpgradeMessageDirections(t *testing.T) {
	dir, err := NewMessage("1", TypeCoreAgentUpgrade, validDirective())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeTypedFor(dir, DirCoreToAgent); err != nil {
		t.Fatalf("指令在正确方向应可解码: %v", err)
	}
	if _, err := DecodeTypedFor(dir, DirAgentToCore); !errors.Is(err, ErrWrongDirection) {
		t.Fatalf("指令出现在 agent→core 方向应判 ErrWrongDirection，实际 %v", err)
	}

	st, err := NewMessage("2", TypeAgentUpgradeStatus, validStatus(UpgradeStateRestarting))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeTypedFor(st, DirAgentToCore); err != nil {
		t.Fatalf("状态在正确方向应可解码: %v", err)
	}
	if _, err := DecodeTypedFor(st, DirCoreToAgent); !errors.Is(err, ErrWrongDirection) {
		t.Fatalf("状态出现在 core→agent 方向应判 ErrWrongDirection，实际 %v", err)
	}
}

// 语义校验必须在解码路径上生效：一条状态合法的报文配上越界载荷要当场被拒，
// 而不是静默落进下游存储。
func TestUpgradeStatusDecodeRejectsInvalidPayload(t *testing.T) {
	s := validStatus(UpgradeStateFailed)
	s.ReasonCode = ""
	m, err := NewMessage("3", TypeAgentUpgradeStatus, s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeTypedFor(m, DirAgentToCore); !errors.Is(err, ErrMissingField) {
		t.Fatalf("缺原因码的失败上报应被解码路径拒绝，实际 %v", err)
	}
}

func TestHelloAckUpgradeField(t *testing.T) {
	ok := &HelloAck{Accepted: true, DeviceID: "123", ReportInterval: 10, Upgrade: validDirective()}
	if err := ok.Validate(); err != nil {
		t.Fatalf("带合法指令的 ack 不应失败: %v", err)
	}

	bad := &HelloAck{Accepted: true, DeviceID: "123", Upgrade: validDirective()}
	bad.Upgrade.TargetVersion = "0.2"
	if err := bad.Validate(); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("带非法指令的 ack 应被拒，实际 %v", err)
	}

	rejected := &HelloAck{Accepted: false, RejectReason: "nope", Upgrade: validDirective()}
	if err := rejected.Validate(); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("被拒的 ack 带指令应被拒（构造错误），实际 %v", err)
	}
}
