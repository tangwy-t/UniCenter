package ws

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// TestCheckOrigin 是 console WS 的 Origin 策略守卫（逐条钉住四条规则）。
//
// 断言对象从 checkOrigin 改名为 CheckOrigin，**内容一字未动**：判定实现从
// 包内私有函数提升为导出函数，是为了让 agent WS 端点复用**同一个**函数值
// （见 TestUpGraderUsesSharedCheckOrigin）。
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
			if got := CheckOrigin(r); got != tc.want {
				t.Fatalf("CheckOrigin(origin=%q, host=%q) = %v, want %v", tc.origin, tc.host, got, tc.want)
			}
		})
	}
}

// TestUpGraderUsesSharedCheckOrigin 钉住 console 侧的升级器**就是**用这个共享函数。
//
// 只测 CheckOrigin 本身不够：upGrader 完全可以被改成 `CheckOrigin: someCopyOfIt`，
// 那时上面的表驱动断言照旧全绿，而两个端点已经各用一份实现了（正是要修的缺陷形态）。
func TestUpGraderUsesSharedCheckOrigin(t *testing.T) {
	if reflect.ValueOf(upGrader.CheckOrigin).Pointer() != reflect.ValueOf(CheckOrigin).Pointer() {
		t.Fatal("console upGrader 的 CheckOrigin 不是共享的 ws.CheckOrigin —— " +
			"策略改动会漏掉一个端点（agent 侧同理由 handler 包的守卫覆盖）")
	}
	// 反向兜底：CheckOrigin 不能是 nil（nil 会让 gorilla 回退到默认的「同源」判定，
	// 于是非浏览器客户端（agent）的握手会被拒）。
	if upGrader.CheckOrigin == nil {
		t.Fatal("console upGrader 没有装 CheckOrigin：gorilla 会退回默认同源判定")
	}
	var _ func(*http.Request) bool = CheckOrigin
}
