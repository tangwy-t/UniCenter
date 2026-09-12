package util

import "testing"

func TestParseBrowser(t *testing.T) {
	cases := []struct {
		ua   string
		want string
	}{
		{"Mozilla/5.0 Edg/120", "Edge"},
		{"Mozilla/5.0 Firefox/120", "Firefox"},
		{"Mozilla/5.0 Chrome/120", "Chrome"},
		{"Mozilla/5.0 Safari/604.1", "Safari"},
		{"curl/8.0", "Unknown"},
	}
	for _, c := range cases {
		if got := ParseBrowser(c.ua); got != c.want {
			t.Errorf("ParseBrowser(%q) = %q, want %q", c.ua, got, c.want)
		}
	}
}

func TestParseOS(t *testing.T) {
	cases := []struct {
		ua   string
		want string
	}{
		{"Windows NT 10.0", "Windows"},
		{"Mac OS X 10_15", "Mac"},
		{"Linux x86_64", "Linux"},
		{"Android 14", "Android"},
		{"iPhone OS 17", "iOS"},
		{"curl", "Unknown"},
	}
	for _, c := range cases {
		if got := ParseOS(c.ua); got != c.want {
			t.Errorf("ParseOS(%q) = %q, want %q", c.ua, got, c.want)
		}
	}
}

func TestParseUserAgent(t *testing.T) {
	browser, os := ParseUserAgent("Mozilla/5.0 Chrome/120 (Windows NT 10.0)")
	if browser == nil || os == nil {
		t.Fatal("ParseUserAgent 应返回非 nil 指针")
	}
	if *browser != "Chrome" || *os != "Windows" {
		t.Errorf("ParseUserAgent = (%q, %q), want (Chrome, Windows)", *browser, *os)
	}
}
