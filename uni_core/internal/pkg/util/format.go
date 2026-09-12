package util

import (
	"fmt"
	"strings"
	"time"
)

// FormatBytes 把字节数格式化为带单位的人类可读文案（1024 进制）。
// 例：FormatBytes(1536) → "1.5 KB"；FormatBytes(512) → "512 B"。
func FormatBytes(size int64) string {
	const unit = 1024
	units := []string{"B", "KB", "MB", "GB", "TB"}
	value := float64(size)
	i := 0
	for value >= unit && i < len(units)-1 {
		value /= unit
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d %s", size, units[i])
	}
	return fmt.Sprintf("%.1f %s", value, units[i])
}

// FormatIDs 把一组 uint64 序号格式化为逗号分隔的字符串（", " 连接），
// 用于拼进错误消息。例：FormatIDs([]uint64{1,2,3}) → "1, 2, 3"。
func FormatIDs(ids []uint64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf("%d", id)
	}
	return strings.Join(parts, ", ")
}

// FormatDuration 把时长格式化为人类可读串（精确到秒）：如 "3h15m30s"、
// "5m30s"、"45s"。不足 1 小时不显示小时位，不足 1 分钟不显示分钟位。
func FormatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second

	if h > 0 {
		return fmt.Sprintf("%dh%dm%ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm%ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// BytesToMB 把字节数换算为兆字节（MB），并用 Round2 量化到 2 位小数。
func BytesToMB(b uint64) float64 {
	return Round2(float64(b) / 1024 / 1024)
}
