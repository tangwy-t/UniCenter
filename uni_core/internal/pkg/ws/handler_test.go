package ws

import (
	"net/http/httptest"
	"testing"
)

func TestCheckOrigin(t *testing.T) {
	cases := []struct {
		name   string
		origin string
		host   string
		want   bool
	}{
		{"no origin (non-browser client)", "", "api.example.com", true},
		{"same host any port", "https://example.com:5173", "example.com:8080", true},
		{"same host same port", "https://api.example.com", "api.example.com", true},
		{"cross-site denied", "https://evil.example.net", "api.example.com", false},
		{"localhost dev allowed", "http://localhost:5173", "api.example.com", true},
		{"loopback ip dev allowed", "http://127.0.0.1:3000", "api.example.com", true},
		{"evil mimicking localhost subdomain denied", "http://localhost.evil.com", "api.example.com", false},
		{"unparseable origin denied", "http://[::1", "api.example.com", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://"+tc.host+"/ws", nil)
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			r.Host = tc.host
			if got := checkOrigin(r); got != tc.want {
				t.Fatalf("checkOrigin(origin=%q, host=%q) = %v, want %v", tc.origin, tc.host, got, tc.want)
			}
		})
	}
}
