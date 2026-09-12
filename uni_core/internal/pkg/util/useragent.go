package util

import "strings"

// ParseBrowser 从 User-Agent 串做简单字符串匹配提取浏览器名。
// 未识别时返回 "Unknown"。
func ParseBrowser(ua string) string {
	uaLower := strings.ToLower(ua)
	switch {
	case strings.Contains(ua, "Edg/"):
		return "Edge"
	case strings.Contains(ua, "Firefox/"):
		return "Firefox"
	case strings.Contains(ua, "Chrome/") && !strings.Contains(ua, "Edg/"):
		return "Chrome"
	case strings.Contains(ua, "Safari/") && !strings.Contains(uaLower, "chrome") && !strings.Contains(uaLower, "edg"):
		return "Safari"
	default:
		return "Unknown"
	}
}

// ParseOS 从 User-Agent 串做简单字符串匹配提取操作系统名。
// 未识别时返回 "Unknown"。
func ParseOS(ua string) string {
	switch {
	case strings.Contains(ua, "Windows NT") || strings.Contains(ua, "Windows"):
		return "Windows"
	case strings.Contains(ua, "Mac OS") || strings.Contains(ua, "Macintosh"):
		return "Mac"
	case strings.Contains(ua, "Linux") && !strings.Contains(ua, "Android"):
		return "Linux"
	case strings.Contains(ua, "Android"):
		return "Android"
	case strings.Contains(ua, "iPhone") || strings.Contains(ua, "iPad"):
		return "iOS"
	default:
		return "Unknown"
	}
}

// ParseUserAgent 同时提取浏览器与操作系统名，以字符串指针返回，便于
// 直接写入可空的实体字段（nil 语义由调用方的非空判断决定；此处恒返回
// 非 nil 指针）。
func ParseUserAgent(ua string) (browser, os *string) {
	b := ParseBrowser(ua)
	o := ParseOS(ua)
	return &b, &o
}
