package util

import "testing"

// TestIsInlineSafeMime 覆盖 IsInlineSafeMime 的安全/不安全判定，含带参数
// 后缀（; charset=…）的 MIME 变体，确保按首个 "; " 前缀归一化。
func TestIsInlineSafeMime(t *testing.T) {
	unsafe := []string{
		"text/html",
		"text/html; charset=utf-8",
		"application/xhtml+xml",
		"image/svg+xml",
		"text/javascript",
		"application/javascript",
		"application/x-javascript",
		"text/ecmascript",
		"application/ecmascript",
		"text/xml",
		"application/xml",
		"text/vbscript",
		"application/x-shockwave-flash",
	}
	for _, m := range unsafe {
		if IsInlineSafeMime(m) {
			t.Errorf("%q 应被判定为不可内联渲染", m)
		}
	}

	safe := []string{
		"image/png",
		"image/jpeg",
		"image/gif",
		"image/webp",
		"application/pdf",
		"text/plain",
		"video/mp4",
		"application/octet-stream",
	}
	for _, m := range safe {
		if !IsInlineSafeMime(m) {
			t.Errorf("%q 应被判定为可安全内联", m)
		}
	}
}
