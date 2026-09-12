package util

import "testing"

func TestParseCSVUint64s(t *testing.T) {
	cases := []struct {
		in     string
		want   []uint64
		wantOK bool
	}{
		{"", nil, true},
		{"  ", nil, true},
		{"1,2,3", []uint64{1, 2, 3}, true},
		{"1, 2, 3", []uint64{1, 2, 3}, true},
		{"1,,3", []uint64{1, 3}, true}, // 空项跳过
		{"1,abc", nil, false},          // 非法项
		{"1,0", nil, false},            // 0 视为非法
	}
	for _, c := range cases {
		got, ok := ParseCSVUint64s(c.in)
		if ok != c.wantOK {
			t.Errorf("ParseCSVUint64s(%q) ok = %v, want %v", c.in, ok, c.wantOK)
			continue
		}
		if !uint64SliceEq(got, c.want) {
			t.Errorf("ParseCSVUint64s(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestCSVUint64Count(t *testing.T) {
	if got := CSVUint64Count("1,2,3"); got != 3 {
		t.Errorf("CSVUint64Count(1,2,3) = %d, want 3", got)
	}
	if got := CSVUint64Count("abc"); got != 0 {
		t.Errorf("CSVUint64Count(abc) = %d, want 0", got)
	}
}
