package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// HTTPCallTask 通过 HTTP 请求调用目标 URL 的定时任务。
type HTTPCallTask struct {
	client *http.Client
}

// NewHTTPCallTask 创建 HTTP 回调任务实例。
func NewHTTPCallTask(client *http.Client) *HTTPCallTask {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	if client.Transport == nil {
		client.Transport = &http.Transport{
			DialContext: (&net.Dialer{
				Timeout: 10 * time.Second,
				// SSRF 防护（消除 TOCTOU）：在建立 TCP 连接时按"实际连接的 IP"
				// 校验，而非前置 DNS 解析的结果 —— 防止 DNS rebinding 在校验
				// 与连接两次解析之间切回内网地址，也覆盖多 A 记录只校验
				// 第一条的绕过。Control 对每次拨号（含重定向后的新连接）生效。
				Control: validateConnection,
			}).DialContext,
		}
	}
	// SSRF 防护：对每次重定向目标重新校验 URL
	if client.CheckRedirect == nil {
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("http-call: 重定向次数过多")
			}
			return validateURL(req.URL.String())
		}
	}
	return &HTTPCallTask{client: client}
}

func (t *HTTPCallTask) Name() string        { return "http-call" }
func (t *HTTPCallTask) DisplayName() string { return "HTTP 回调" }

// HTTPCallParams 定义 invoke_params 的 JSON 结构。
type HTTPCallParams struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

func (t *HTTPCallTask) Execute(ctx context.Context, params json.RawMessage) error {
	var p HTTPCallParams
	if err := json.Unmarshal(params, &p); err != nil {
		return fmt.Errorf("http-call: 解析参数失败: %w", err)
	}
	if p.URL == "" {
		return fmt.Errorf("http-call: url 不能为空")
	}
	if p.Method == "" {
		p.Method = "GET"
	}

	// SSRF 防护（第一道）：快速拒绝字面内网 IP 与非 HTTP(S) scheme。
	// 域名的解析结果由 Dialer.Control 在真正连接时校验，两道防线互为补充。
	if err := validateURL(p.URL); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(p.Method), p.URL, bytes.NewBufferString(p.Body))
	if err != nil {
		return fmt.Errorf("http-call: 创建请求失败: %w", err)
	}
	for k, v := range p.Headers {
		req.Header.Set(k, v)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("http-call: 请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http-call: 响应状态码 %d", resp.StatusCode)
	}
	return nil
}

// validateConnection 由 net.Dialer 在每次建立 TCP 连接时调用，addr 是
// 实际拨号的地址 —— 这是权威的 SSRF 校验点。
func validateConnection(network, addr string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("http-call: 无效的连接地址: %s", addr)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("http-call: 无法解析连接 IP: %s", host)
	}
	if isBlockedIP(ip) {
		return fmt.Errorf("http-call: 禁止访问内网地址: %s", ip.String())
	}
	return nil
}

// validateURL 做请求前的静态检查：scheme 白名单 + 字面 IP 拒绝。
// 不再做前置 DNS 解析 —— 域名最终解析到哪个 IP 由 validateConnection
// 在真实连接时校验，避免两次解析之间的 rebinding 窗口。
func validateURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("http-call: 无法解析 URL: %s", rawURL)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("http-call: 不支持的协议: %s", u.Scheme)
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		if isBlockedIP(ip) {
			return fmt.Errorf("http-call: 禁止访问内网地址: %s", ip.String())
		}
	}
	return nil
}

// isBlockedIP 判断是否为禁止访问的地址（回环/私网/链路本地/未指定）。
func isBlockedIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

// ParamSchema 返回 invoke_params 参数 schema（供前端动态表单）。
func (t *HTTPCallTask) ParamSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url":     map[string]any{"type": "string", "title": "请求 URL", "description": "目标 HTTP 地址"},
			"method":  map[string]any{"type": "string", "title": "请求方法", "enum": []string{"GET", "POST"}, "default": "GET"},
			"headers": map[string]any{"type": "object", "title": "请求头"},
			"body":    map[string]any{"type": "string", "title": "请求体", "multiline": true},
		},
		"required": []string{"url"},
	}
}
