package util

import (
	"testing"
	"time"
)

func TestFormatBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
	}
	for _, c := range cases {
		if got := FormatBytes(c.in); got != c.want {
			t.Errorf("FormatBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatIDs(t *testing.T) {
	if got := FormatIDs(nil); got != "" {
		t.Errorf("FormatIDs(nil) = %q, want empty", got)
	}
	if got := FormatIDs([]uint64{1, 2, 3}); got != "1, 2, 3" {
		t.Errorf("FormatIDs([1 2 3]) = %q, want %q", got, "1, 2, 3")
	}
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{45 * time.Second, "45s"},
		{5*time.Minute + 30*time.Second, "5m30s"},
		{3*time.Hour + 15*time.Minute + 30*time.Second, "3h15m30s"},
	}
	for _, c := range cases {
		if got := FormatDuration(c.in); got != c.want {
			t.Errorf("FormatDuration(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBytesToMB(t *testing.T) {
	if got := BytesToMB(1048576); got != 1.0 {
		t.Errorf("BytesToMB(1MiB) = %v, want 1.0", got)
	}
	if got := BytesToMB(1572864); got != 1.5 { // 1.5 MiB
		t.Errorf("BytesToMB(1.5MiB) = %v, want 1.5", got)
	}
}
