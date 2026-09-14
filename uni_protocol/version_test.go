package agentproto

import "testing"

func TestIsSupportedBoundaries(t *testing.T) {
	cases := []struct {
		v    int
		want bool
	}{
		{CurrentVersion, true},
		{MinVersion, true},
		{MinVersion - 1, false},
		{CurrentVersion + 1, false},
		{0, false},
		{-1, false},
		{1 << 30, false},
	}
	for _, c := range cases {
		if got := IsSupported(c.v); got != c.want {
			t.Errorf("IsSupported(%d) = %v, want %v", c.v, got, c.want)
		}
	}
}

func TestSupportedVersions(t *testing.T) {
	got := SupportedVersions()
	if len(got) == 0 {
		t.Fatal("SupportedVersions() 为空")
	}
	// 必须升序、无重复、且含 CurrentVersion
	seen := map[int]bool{}
	prev := 0
	for i, v := range got {
		if i > 0 && v <= prev {
			t.Fatalf("SupportedVersions() 未严格递增: %v", got)
		}
		if seen[v] {
			t.Fatalf("SupportedVersions() 含重复: %v", got)
		}
		seen[v] = true
		prev = v
	}
	if !seen[CurrentVersion] {
		t.Fatalf("SupportedVersions() 不含 CurrentVersion: %v", got)
	}
	if !IsSupported(CurrentVersion) {
		t.Fatal("IsSupported(CurrentVersion) 必须为 true")
	}
}
