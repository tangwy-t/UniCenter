package util

import "strings"

// ParsedUA 是从 User-Agent 解析出的浏览器与操作系统标签。
type ParsedUA struct {
	Browser string
	OS      string
}

// ParseUA 尽力解析浏览器与操作系统,未识别返回 "Unknown"。
// 顺序敏感:Edge 的 UA 同时含 "Edg/" 与 "Chrome",须先判 Edge;Safari 的 UA
// 含 "Safari/" 但 Chrome/Edge UA 也含 "Safari/537.36",故最后判 Safari。
func ParseUA(ua string) ParsedUA {
	p := ParsedUA{Browser: "Unknown", OS: "Unknown"}
	if ua == "" {
		return p
	}
	switch {
	case strings.Contains(ua, "Edg/"):
		p.Browser = "Edge"
	case strings.Contains(ua, "Firefox/"):
		p.Browser = "Firefox"
	case strings.Contains(ua, "Chrome/"):
		p.Browser = "Chrome"
	case strings.Contains(ua, "Safari/"):
		p.Browser = "Safari"
	}
	switch {
	case strings.Contains(ua, "Windows NT"):
		p.OS = "Windows"
	case strings.Contains(ua, "Mac OS X"):
		p.OS = "macOS"
	case strings.Contains(ua, "Android"):
		p.OS = "Android"
	case strings.Contains(ua, "iPhone") || strings.Contains(ua, "iPad"):
		p.OS = "iOS"
	case strings.Contains(ua, "Linux"):
		p.OS = "Linux"
	}
	return p
}
