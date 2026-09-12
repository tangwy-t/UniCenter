package util

import "testing"

func TestMissingIDs(t *testing.T) {
	cases := []struct {
		requested, existing []uint64
		want                []uint64
	}{
		{[]uint64{1, 2}, []uint64{1}, []uint64{2}},
		{[]uint64{1, 2}, []uint64{1, 2}, nil},
		{nil, []uint64{1}, nil},
		{[]uint64{}, nil, nil},
	}
	for _, c := range cases {
		got := MissingIDs(c.requested, c.existing)
		if !uint64SliceEq(got, c.want) {
			t.Fatalf("MissingIDs(%v, %v) = %v, want %v", c.requested, c.existing, got, c.want)
		}
	}
}

func TestDedupIDs(t *testing.T) {
	got := DedupIDs([]uint64{3, 1, 3, 2, 1})
	want := []uint64{3, 1, 2}
	if !uint64SliceEq(got, want) {
		t.Fatalf("DedupIDs = %v, want %v", got, want)
	}
}

func uint64SliceEq(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
