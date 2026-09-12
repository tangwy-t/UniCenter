package response

import "github.com/tangwy-t/webmanager-server/internal/pkg/util"

// SQLHistoryPoint 单个时间桶的聚合指标(SQL 历史时序 HTTP 契约)。
// JSON 字段与改造前的 database.HistoryPoint 完全一致。
type SQLHistoryPoint struct {
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

// SQLHistorySnapshot SQL 历史时序查询响应(HTTP 契约)。
// 由 service 从 sqlhistory.Snapshot(Redis 路径)或
// database.HistorySnapshot(ring buffer 回退)适配而来。
type SQLHistorySnapshot struct {
	WindowSeconds   int64             `json:"window_seconds"`
	StepSeconds     int64             `json:"step_seconds"`
	SlowThresholdMs int64             `json:"slow_threshold_ms"`
	RecentQPS       float64           `json:"recent_qps"`
	Buckets         []SQLHistoryPoint `json:"buckets"`
}
