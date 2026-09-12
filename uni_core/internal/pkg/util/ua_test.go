package util

import "testing"

func TestParseUA(t *testing.T) {
	cases := []struct {
		ua      string
		browser string
		os      string
	}{
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36", "Chrome", "Windows"},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Edg/120.0 Chrome/120.0 Safari/537.36", "Edge", "Windows"},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15", "Safari", "macOS"},
		{"Mozilla/5.0 (X11; Linux x86_64; rv:109.0) Gecko/20100101 Firefox/119.0", "Firefox", "Linux"},
		{"Mozilla/5.0 (Linux; Android 13) AppleWebKit/537.36 Chrome/120.0 Mobile Safari/537.36", "Chrome", "Android"},
		{"", "Unknown", "Unknown"},
	}
	for _, c := range cases {
		p := ParseUA(c.ua)
		if p.Browser != c.browser || p.OS != c.os {
			t.Errorf("ParseUA(%q) = %+v, want %s/%s", c.ua, p, c.browser, c.os)
		}
	}
}
