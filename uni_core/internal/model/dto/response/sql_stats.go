package response

// SQL 监控统计快照的 HTTP 契约。
//
// 这些类型此前**只**存在于 internal/pkg/database(sql_stats.go),handler 直接把
// database.StatsSnapshot 交给 app.Success 序列化。问题有两个:
//
//  1. apigen 只解析 internal/model/dto/response,因此 /monitor/sql/stats 的
//     响应结构从未进入 api.generated.d.ts;前端只能在
//     modules/system-monitor/api/sql.ts 里手写一份 SqlStats/SqlQueryEntry,
//     字段名(snake_case)靠人工对齐,后端改字段前端不会有任何编译期提示。
//  2. database 是基础设施包,让它同时承担 HTTP 契约职责,会把"存储层统计"
//     与"对外 API 形状"绑死 —— 调整前者就顺手改了后者。
//
// 在此显式声明对外形状,database 侧保持内部类型;handler 做一次映射
// (字段一一对应,无转换逻辑),换取 apigen 覆盖与前端类型的自动生成。
//
// 注意:JSON 字段名必须与改造前完全一致(snake_case),否则前端既有页面
// 会静默拿不到数据。

// SQLGlobalStats 全局累计统计。
type SQLGlobalStats struct {
	Count           int64   `json:"count"`
	AvgMs           float64 `json:"avg_ms"`
	MaxMs           float64 `json:"max_ms"`
	MinMs           float64 `json:"min_ms"`
	P50Ms           float64 `json:"p50_ms"`
	P95Ms           float64 `json:"p95_ms"`
	P99Ms           float64 `json:"p99_ms"`
	ErrorCount      int64   `json:"error_count"`
	SlowCount       int64   `json:"slow_count"`
	SlowThresholdMs int64   `json:"slow_threshold_ms"`
}

// SQLDimStats 按维度(表名 / 操作类型)聚合的统计。
//
// 分位数字段带 omitempty,与 database.DimSnapshot 一致:未进行过分位数
// 计算(样本不足)时该字段不会出现在 JSON 中,前端已按可选字段声明。
type SQLDimStats struct {
	Count int64   `json:"count"`
	AvgMs float64 `json:"avg_ms"`
	MaxMs float64 `json:"max_ms"`
	MinMs float64 `json:"min_ms"`
	P50Ms float64 `json:"p50_ms,omitempty"`
	P95Ms float64 `json:"p95_ms,omitempty"`
	P99Ms float64 `json:"p99_ms,omitempty"`
}

// SQLQueryEntry 单条 SQL 执行记录(仅用于慢查询列表)。
//
// Timestamp 在 database.QueryEntry 中为 util.JSONTime(自定义 MarshalJSON,
// 序列化为时间字符串),此处用 string 承接同一线上格式。
// IsError / IsSlow **不带** omitempty:与改造前一致,前端始终读到布尔值。
type SQLQueryEntry struct {
	Timestamp  string  `json:"timestamp"`
	DurationMs float64 `json:"duration_ms"`
	SQL        string  `json:"sql"`
	Table      string  `json:"table"`
	Operation  string  `json:"operation"`
	IsError    bool    `json:"is_error"`
	IsSlow     bool    `json:"is_slow"`
}

// SQLStatsSnapshot SQL 监控统计响应(累计或按时间窗口聚合)。
type SQLStatsSnapshot struct {
	Global      SQLGlobalStats         `json:"global"`
	ByTable     map[string]SQLDimStats `json:"by_table"`
	ByOperation map[string]SQLDimStats `json:"by_operation"`
	SlowQueries []SQLQueryEntry        `json:"slow_queries"`
}
