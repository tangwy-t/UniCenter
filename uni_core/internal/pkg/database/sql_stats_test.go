package database

import (
	"testing"
	"time"
)

// TestSnapshotHistoryAggregates 验证时序快照的分桶聚合:
// 计数/错误/慢查询标志、反映真实排序的分位数、以及透出的慢查询阈值。
func TestSnapshotHistoryAggregates(t *testing.T) {
	stats := NewSQLStats(64, 200*time.Millisecond)

	durations := []time.Duration{
		10 * time.Millisecond,
		20 * time.Millisecond, // 标记为错误
		30 * time.Millisecond,
		210 * time.Millisecond, // 超过阈值 → 慢查询
	}
	for i, d := range durations {
		stats.Record("sys_user", "SELECT", d, "SELECT * FROM sys_user", i == 1, d > 200*time.Millisecond)
	}

	snap := stats.SnapshotHistory(time.Hour, time.Second)
	if snap.WindowSeconds != 3600 {
		t.Fatalf("window_seconds = %d, want %d", snap.WindowSeconds, 3600)
	}
	if snap.StepSeconds != 1 {
		t.Fatalf("step_seconds = %d, want 1", snap.StepSeconds)
	}
	if snap.SlowThresholdMs != 200 {
		t.Fatalf("slow_threshold_ms = %d, want 200", snap.SlowThresholdMs)
	}

	var total, errCount, slowCount int64
	var maxMs float64
	found := false
	for _, b := range snap.Buckets {
		total += b.Count
		errCount += b.ErrorCount
		slowCount += b.SlowCount
		if b.MaxMs > maxMs {
			maxMs = b.MaxMs
		}
		if b.Count == 0 {
			continue
		}
		found = true
		if !(b.P50Ms <= b.P95Ms && b.P95Ms <= b.P99Ms && b.P99Ms <= b.MaxMs) {
			t.Fatalf("percentile ordering broken: p50=%v p95=%v p99=%v max=%v", b.P50Ms, b.P95Ms, b.P99Ms, b.MaxMs)
		}
	}
	if !found {
		t.Fatal("expected at least one non-empty bucket")
	}
	if total != int64(len(durations)) {
		t.Fatalf("sum(count) = %d, want %d", total, len(durations))
	}
	if errCount != 1 || slowCount != 1 {
		t.Fatalf("errCount=%d slowCount=%d, want 1 / 1", errCount, slowCount)
	}
	if maxMs < 200 {
		t.Fatalf("maxMs = %v, want >= 200", maxMs)
	}
}

// TestSnapshotHistoryBucketCap 验证桶数超限时 step 自动放大:
// 24h @ 1s = 86400 桶 > 桶数上限(metricshistory 4000),step 应放大为 ceil(86400/4000)=22s。
func TestSnapshotHistoryBucketCap(t *testing.T) {
	stats := NewSQLStats(64, 200*time.Millisecond)
	snap := stats.SnapshotHistory(24*time.Hour, time.Second)
	if snap.WindowSeconds != 24*3600 {
		t.Fatalf("window_seconds = %d, want %d", snap.WindowSeconds, 24*3600)
	}
	if snap.StepSeconds != 22 {
		t.Fatalf("step_seconds = %d, want 22 (auto-upscaled)", snap.StepSeconds)
	}
}

// TestSnapshotHistoryEmpty 验证无任何采样的空状态。
func TestSnapshotHistoryEmpty(t *testing.T) {
	stats := NewSQLStats(64, 200*time.Millisecond)
	snap := stats.SnapshotHistory(30*time.Minute, time.Second)
	if len(snap.Buckets) != 0 {
		t.Fatalf("buckets = %d, want 0 for idle stats", len(snap.Buckets))
	}
	if snap.RecentQPS != 0 {
		t.Fatalf("recent_qps = %v, want 0", snap.RecentQPS)
	}
}

// ivClose 浮点近似比较(1e-6 容差)。
func ivClose(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= 1e-6
}

// TestIntervalSummaryAggregates 验证区间摘要的计数、均值、精确分位数
// 与错误/慢查询统计,以及慢查询阈值访问器。
func TestIntervalSummaryAggregates(t *testing.T) {
	s := NewSQLStats(64, 200*time.Millisecond)
	s.Record("t", "select", time.Millisecond, "S1", false, false)
	s.Record("t", "select", 2*time.Millisecond, "S2", false, false)
	s.Record("t", "select", 3*time.Millisecond, "S3", false, true)
	s.Record("t", "select", 4*time.Millisecond, "S4", true, false)

	if s.SlowThreshold() != 200 {
		t.Fatalf("SlowThreshold() = %d, want 200", s.SlowThreshold())
	}

	got := s.IntervalSummary(time.Now().Add(-time.Hour))
	if got.Count != 4 {
		t.Fatalf("count = %d, want 4", got.Count)
	}
	if !ivClose(got.AvgMs, 2.5) {
		t.Fatalf("avg_ms = %v, want 2.5", got.AvgMs)
	}
	if !ivClose(got.P50Ms, 2.5) || !ivClose(got.P95Ms, 3.85) || !ivClose(got.P99Ms, 3.97) {
		t.Fatalf("p50/p95/p99 = %v/%v/%v, want 2.5/3.85/3.97", got.P50Ms, got.P95Ms, got.P99Ms)
	}
	if !ivClose(got.MaxMs, 4) {
		t.Fatalf("max_ms = %v, want 4", got.MaxMs)
	}
	if got.ErrorCount != 1 || got.SlowCount != 1 {
		t.Fatalf("error=%d slow=%d, want 1/1", got.ErrorCount, got.SlowCount)
	}
}

// TestIntervalSummaryExcludesOld 验证 since 边界过滤:边界前的记录不计入。
func TestIntervalSummaryExcludesOld(t *testing.T) {
	s := NewSQLStats(64, 200*time.Millisecond)
	s.Record("t", "select", time.Millisecond, "S1", false, false)

	got := s.IntervalSummary(time.Now())
	if got.Count != 0 {
		t.Fatalf("count = %d, want 0 (记录在 since 之前)", got.Count)
	}
}

// TestSQLMetricsQuantizedToTwoDecimals 验证 SQL 指标全链路量化到 2 位小数:
// 慢查询 DurationMs(µs 源头)、全局 Max/Min/Avg、按表维度、分位数插值、
// 历史分桶 QPS/AvgMs、区间摘要。输入刻意构造非整除长尾(如 2/6 QPS、
// 1234567ns 耗时),输出必须恰好 2 位。
func TestSQLMetricsQuantizedToTwoDecimals(t *testing.T) {
	s := NewSQLStats(64, 200*time.Millisecond)
	s.Record("t1", "SELECT", 1234567*time.Nanosecond, "S1", false, false) // 1.234567ms → 1.23
	s.Record("t1", "SELECT", 2348678*time.Nanosecond, "S2", false, false) // 2.348678ms → 2.35

	// ── 累计快照:全局 + 按表 ──
	snap := s.Snapshot()
	if snap.Global.MinMs != 1.23 || snap.Global.MaxMs != 2.35 {
		t.Fatalf("global min/max = %v/%v, want 1.23/2.35", snap.Global.MinMs, snap.Global.MaxMs)
	}
	// avg = (1234567+2348678)/2 ns = 1.7916225ms → 1.79
	if snap.Global.AvgMs != 1.79 {
		t.Fatalf("global avg = %v, want 1.79", snap.Global.AvgMs)
	}
	// 分位数插值:sorted [1.23, 2.35] → p50=1.79, p95=2.294→2.29, p99=2.3388→2.34
	if snap.Global.P50Ms != 1.79 || snap.Global.P95Ms != 2.29 || snap.Global.P99Ms != 2.34 {
		t.Fatalf("global p50/p95/p99 = %v/%v/%v, want 1.79/2.29/2.34",
			snap.Global.P50Ms, snap.Global.P95Ms, snap.Global.P99Ms)
	}
	dim := snap.ByTable["t1"]
	if dim.AvgMs != 1.79 || dim.MinMs != 1.23 || dim.MaxMs != 2.35 {
		t.Fatalf("by_table = %+v, want avg 1.79 min 1.23 max 2.35", dim)
	}
	op := snap.ByOperation["SELECT"]
	if op.AvgMs != 1.79 || op.MinMs != 1.23 || op.MaxMs != 2.35 {
		t.Fatalf("by_operation = %+v, want avg 1.79 min 1.23 max 2.35", op)
	}

	// ── 慢查询列表:源头 DurationMs 已量化 ──
	if len(snap.SlowQueries) != 2 {
		t.Fatalf("slow_queries = %d, want 2", len(snap.SlowQueries))
	}
	if snap.SlowQueries[0].DurationMs != 2.35 || snap.SlowQueries[1].DurationMs != 1.23 {
		t.Fatalf("slow_queries durations = %v/%v, want 2.35/1.23(降序)",
			snap.SlowQueries[0].DurationMs, snap.SlowQueries[1].DurationMs)
	}

	// ── 窗口模式快照:entries 均值同样量化 ──
	wsnap := s.SnapshotWindow(time.Hour)
	if wsnap.Global.AvgMs != 1.79 {
		t.Fatalf("window avg = %v, want 1.79", wsnap.Global.AvgMs)
	}

	// ── 历史分桶:step 必须整除 window(1h/7s 这类不整除组合会让"now"
	// 的记录落在截断桶位之外被丢弃,属既有边界行为,与量化无关);
	// 6s 整除 1h → 2 条记录同桶,QPS = 2/6 = 0.3333... → 0.33 ──
	h := s.SnapshotHistory(time.Hour, 6*time.Second)
	if len(h.Buckets) != 1 {
		t.Fatalf("history buckets = %d, want 1", len(h.Buckets))
	}
	b := h.Buckets[0]
	if b.Count != 2 {
		t.Fatalf("history count = %d, want 2", b.Count)
	}
	if b.QPS != 0.33 {
		t.Fatalf("history qps = %v, want 0.33(2/6 量化)", b.QPS)
	}
	if b.AvgMs != 1.79 || b.MaxMs != 2.35 {
		t.Fatalf("history avg/max = %v/%v, want 1.79/2.35", b.AvgMs, b.MaxMs)
	}
	if b.P50Ms != 1.79 || b.P95Ms != 2.29 || b.P99Ms != 2.34 {
		t.Fatalf("history p50/p95/p99 = %v/%v/%v, want 1.79/2.29/2.34", b.P50Ms, b.P95Ms, b.P99Ms)
	}
	// RecentQPS:recentCut = now-step。记录时间戳与断言时刻可能跨秒,
	// 圈内 1 条 → 1/6 = 0.1666... → 0.17;圈内 2 条 → 0.33。两种都必须量化。
	if h.RecentQPS != 0.17 && h.RecentQPS != 0.33 {
		t.Fatalf("recent_qps = %v, want 0.17 或 0.33", h.RecentQPS)
	}

	// ── 区间摘要(3s 采样器写 Redis 的数据源)──
	sum := s.IntervalSummary(time.Now().Add(-time.Hour))
	if sum.AvgMs != 1.79 || sum.MaxMs != 2.35 {
		t.Fatalf("interval avg/max = %v/%v, want 1.79/2.35", sum.AvgMs, sum.MaxMs)
	}
	if sum.P50Ms != 1.79 || sum.P95Ms != 2.29 || sum.P99Ms != 2.34 {
		t.Fatalf("interval p50/p95/p99 = %v/%v/%v, want 1.79/2.29/2.34", sum.P50Ms, sum.P95Ms, sum.P99Ms)
	}
}
