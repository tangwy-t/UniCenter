// Package sqlhistory 提供 SQL 监控历史的 Redis 滚动窗口存储:
// 3s 采样器 Append 区间摘要点,Query 按 window/step 内存聚合,
// 24h 固定保留、跨后端重启不丢。
package sqlhistory

import "github.com/tangwy-t/webmanager-server/internal/pkg/util"

// historyKey Redis 列表 key:当前单实例部署,全局唯一。
const historyKey = "monitor:sql:history"

// sampleStepSeconds 采样周期(秒),与 SQLMonitorService 采样器默认节奏一致。
const sampleStepSeconds = 3

// maxPoints 滚动窗口容量:24h ÷ 3s = 28800 条。
const maxPoints = 28800

// Point 单个采样区间((上次 tick, 本次 tick])的聚合点。
type Point struct {
	T          int64   `json:"t"` // unix 毫秒:区间结束时刻
	Count      int64   `json:"count"`
	AvgMs      float64 `json:"avg_ms"`
	P50Ms      float64 `json:"p50_ms"`
	P95Ms      float64 `json:"p95_ms"`
	P99Ms      float64 `json:"p99_ms"`
	MaxMs      float64 `json:"max_ms"`
	ErrorCount int64   `json:"error_count"`
	SlowCount  int64   `json:"slow_count"`
}

// Bucket 查询聚合桶,JSON 字段与前端契约一致。
type Bucket struct {
	Timestamp  util.JSONTime `json:"timestamp"`
	Count      int64         `json:"count"`
	QPS        float64       `json:"qps"`
	AvgMs      float64       `json:"avg_ms"`
	P50Ms      float64       `json:"p50_ms"`
	P95Ms      float64       `json:"p95_ms"`
	P99Ms      float64       `json:"p99_ms"`
	MaxMs      float64       `json:"max_ms"`
	ErrorCount int64         `json:"error_count"`
	SlowCount  int64         `json:"slow_count"`
}

// Snapshot 查询响应;SlowThresholdMs 由 service 侧回填后返回给前端。
type Snapshot struct {
	WindowSeconds int64    `json:"window_seconds"`
	StepSeconds   int64    `json:"step_seconds"`
	RecentQPS     float64  `json:"recent_qps"`
	Buckets       []Bucket `json:"buckets"`
}
