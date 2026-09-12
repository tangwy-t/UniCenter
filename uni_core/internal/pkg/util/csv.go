package util

import (
	"strconv"
	"strings"
)

// ParseCSVUint64s 解析逗号分隔的无符号 ID 串。
//
// 跳过前后空白与空项；任一项非法（非数字或为 0）时返回 ok=false。
// 空串（TrimSpace 后为空）视为有效的空集合，返回 (nil, true)。
func ParseCSVUint64s(raw string) ([]uint64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, true
	}
	parts := strings.Split(raw, ",")
	ids := make([]uint64, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		v, err := strconv.ParseUint(p, 10, 64)
		if err != nil || v == 0 {
			return nil, false
		}
		ids = append(ids, v)
	}
	return ids, true
}

// CSVUint64Count 返回 CSV 串中有效 ID 的个数；解析失败按 0 处理。
func CSVUint64Count(raw string) int {
	ids, _ := ParseCSVUint64s(raw)
	return len(ids)
}
