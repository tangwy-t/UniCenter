package util

import "strings"

// ParseExtSet 解析逗号分隔的文件扩展名白名单为集合。
//
// 每个条目做小写化与 TrimSpace，缺点的条目补点；空条目跳过。
// 空配置返回空集合（由调用方决定是「不限制」还是「全拒绝」）。
func ParseExtSet(raw string) map[string]bool {
	set := make(map[string]bool)
	for _, ext := range strings.Split(raw, ",") {
		ext = strings.ToLower(strings.TrimSpace(ext))
		if ext == "" {
			continue
		}
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		set[ext] = true
	}
	return set
}
