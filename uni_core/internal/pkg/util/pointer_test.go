package util

import "testing"

func TestPtr(t *testing.T) {
	p := Ptr(42)
	if p == nil || *p != 42 {
		t.Fatalf("Ptr(42) = %v, want *42", p)
	}
	ps := Ptr("x")
	if ps == nil || *ps != "x" {
		t.Fatalf("Ptr(x) = %v, want *x", ps)
	}
}

func TestCoalesce(t *testing.T) {
	var pf *float64
	if got := Coalesce(pf); got != 0 {
		t.Errorf("Coalesce(nil *float64) = %v, want 0", got)
	}
	v := 3.14
	if got := Coalesce(&v); got != 3.14 {
		t.Errorf("Coalesce(&3.14) = %v, want 3.14", got)
	}
	var ps *string
	if got := Coalesce(ps); got != "" {
		t.Errorf("Coalesce(nil *string) = %q, want empty", got)
	}
	s := "x"
	if got := Coalesce(&s); got != "x" {
		t.Errorf("Coalesce(&x) = %q, want x", got)
	}
}

// TestPtrCoalesceRoundTrip 验证 Ptr 与 Coalesce 互逆。
func TestPtrCoalesceRoundTrip(t *testing.T) {
	if got := Coalesce(Ptr(7)); got != 7 {
		t.Fatalf("Coalesce(Ptr(7)) = %v, want 7", got)
	}
}
