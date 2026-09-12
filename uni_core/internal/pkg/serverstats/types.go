// Package serverstats 提供服务器监控历史指标的 Redis 滚动窗口存储与聚合。
// 写入侧由采样协程每 2s 追加一个 Point,读取侧按 window/step 聚合为时间桶。
package serverstats

import (
	"context"
	"time"

	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

// historyKey 历史采样点的 Redis list key(全局唯一;当前单实例部署)。
const historyKey = "monitor:server:history"

// maxPoints 滚动窗口容量:24 小时 ÷ 2 秒采样周期。
const maxPoints = 43200

// sampleStepSeconds 采样周期(秒),与 handler 的 defaultSampleInterval(2s)一致;
// 窗口取点启发式按该值估算需要回读的最新条数(列表头部)。
const sampleStepSeconds = 2

// Point 一次采样的原始指标,即写入 Redis 的 JSON 形状。
// 可能采集不到的指标用指针表达"缺值"。
type Point struct {
	T          int64    `json:"t"` // unix 毫秒时间戳
	CPU        float64  `json:"cpu"`
	MemSys     float64  `json:"memSys"`
	HeapAlloc  float64  `json:"heapAlloc"`
	SysMem     float64  `json:"sysMem"`
	Goroutines uint64   `json:"goroutines"`
	GCNum      uint32   `json:"gcNum"`
	GCPauseMs  *float64 `json:"gcPauseMs,omitempty"`
	Disk       float64  `json:"disk"`
	Load1      *float64 `json:"load1,omitempty"`
	Uptime     int64    `json:"uptime"`
}

// Bucket 一个聚合时间桶(历史接口响应的元素)。
// 指针字段为 nil 时 JSON 省略该字段:表示桶内没有任何采样点提供该指标。
type Bucket struct {
	Timestamp  util.JSONTime `json:"timestamp"`
	CPU        *float64      `json:"cpu,omitempty"`
	MemSys     *float64      `json:"memSys,omitempty"`
	HeapAlloc  *float64      `json:"heapAlloc,omitempty"`
	SysMem     *float64      `json:"sysMem,omitempty"`
	Goroutines *float64      `json:"goroutines,omitempty"`
	GCNum      *uint32       `json:"gcNum,omitempty"`
	GCPauseMs  *float64      `json:"gcPauseMs,omitempty"`
	Disk       *float64      `json:"disk,omitempty"`
	Load1      *float64      `json:"load1,omitempty"`
	Uptime     *int64        `json:"uptime,omitempty"`
}

// Snapshot 历史查询响应(与 SQL /history 同构)。
type Snapshot struct {
	WindowSeconds int64    `json:"window_seconds"`
	StepSeconds   int64    `json:"step_seconds"`
	Buckets       []Bucket `json:"buckets"`
}

// Store 是 handler 依赖的最小接口(消费方定义,测试可注入内存实现)。
type Store interface {
	Append(ctx context.Context, p Point) error
	Query(ctx context.Context, window, step time.Duration) (*Snapshot, error)
}
