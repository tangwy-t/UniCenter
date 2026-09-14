package agentproto

import (
	"sort"
	"strings"
	"testing"
)

func TestCloseCodesAreStableAndInRange(t *testing.T) {
	want := map[int]bool{
		4000: true, 4001: true, 4002: true, 4003: true,
		4004: true, 4005: true, 4006: true, 4007: true, 4008: true,
	}
	got := AllCloseCodes()
	if len(got) != len(want) {
		t.Fatalf("AllCloseCodes() 长度 = %d, want %d (%v)", len(got), len(want), got)
	}
	if !sort.IntsAreSorted(got) {
		t.Fatalf("AllCloseCodes() 未升序: %v", got)
	}
	for _, c := range got {
		if !want[c] {
			t.Fatalf("出现非预期关闭码 %d", c)
		}
		if c < 4000 || c > 4999 {
			t.Fatalf("业务关闭码 %d 越出 4000-4999", c)
		}
	}
	if CloseNormal != 1000 || CloseGoingAway != 1001 {
		t.Fatal("标准关闭码取值被改动")
	}
}

func TestCloseReasonFormatAndCap(t *testing.T) {
	for _, code := range AllCloseCodes() {
		s := CloseReason(code)
		if !strings.HasPrefix(s, itoa(code)+"|") {
			t.Fatalf("CloseReason(%d) 格式非 code|human: %q", code, s)
		}
		if len(s) > MaxCloseReasonBytes {
			t.Fatalf("CloseReason(%d) 超过 %d 字节: %d", code, MaxCloseReasonBytes, len(s))
		}
	}
	if got := CloseReason(4999); got == "" {
		t.Fatal("未登记关闭码也必须回一个非空原因串")
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}
