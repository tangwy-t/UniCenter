package util

import "strings"

// SanitizeFilename 清理文件名，使其可安全地写入 HTTP 头（如
// Content-Disposition 的 filename 参数）或作为纯文件名展示。
//
// 去除 CR/LF（防头部注入）、双引号（防引用逃逸）与路径分隔符（`\` 与 `/`，
// 防止附件名携带路径），清空后回退为 "download"。
func SanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "\r", "")
	name = strings.ReplaceAll(name, "\n", "")
	name = strings.ReplaceAll(name, `"`, "")
	name = strings.ReplaceAll(name, "\\", "")
	name = strings.ReplaceAll(name, "/", "")
	if name == "" {
		return "download"
	}
	return name
}
