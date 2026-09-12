package util

import "strings"

// IsInlineSafeMime 判定 MIME 类型是否允许以内联方式渲染（禁用于 Preview）。
//
// 任何可承载脚本的类型（text/html、image/svg+xml、text/javascript 等）都必须
// 判定为不安全，调用方（文件预览）据此强制附件下载而非内联渲染，避免存储型
// XSS。该函数为无状态纯函数，从 service 包收敛至此，供 handler 与 service
// 两侧共用，消除 handler 仅为这一个函数而依赖 service 的跨层引用。
func IsInlineSafeMime(mimeType string) bool {
	m := strings.ToLower(strings.TrimSpace(strings.SplitN(mimeType, ";", 2)[0]))
	switch m {
	case "text/html", "application/xhtml+xml", "image/svg+xml",
		"text/javascript", "application/javascript", "application/x-javascript",
		"text/ecmascript", "application/ecmascript",
		"text/xml", "application/xml",
		"text/vbscript", "application/x-shockwave-flash":
		return false
	}
	return true
}
