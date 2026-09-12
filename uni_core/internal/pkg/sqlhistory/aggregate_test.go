package sqlhistory

import (
	"testing"
	"time"

	"github.com/tangwy-t/webmanager-server/internal/pkg/metricshistory"
)

// pt 构造一个采样点:T 为 now 前 offsetSec 秒,count=count,各耗时字段=v。
func pt(now time.Time, offsetSec int64, count int64, v float64) Point {
	return Point{
		T:     now.Add(-time.Duration(offsetSec) * time.Second).UnixMilli(),
		Count: count, AvgMs: v, P50Ms: v, P95Ms: v, P99Ms: v, MaxMs: v,
	}
}

// bucketsFor 构造测试用分桶方案(与 Query 路径同源;测试参数固定合法)。
func bucketsFor(now time.Time, window, step time.Duration) metricshistory.Buckets {
	b, err := metricshistory.AlignBucketsAt(now, window, step)
	if err != nil {
		panic(err)
	}
	return b
}

// TestAggregateBuckets 验证绝对时间对齐、count 求和、count 加权平均
// 合并、max 取最大、空桶剔除与 recent_qps。
func TestAggregateBuckets(t *testing.T) {
	now := time.Date(2026, 9, 3, 20, 0, 30, 0, time.Local) // 秒对齐,消除边界抖动
	points := []Point{
		pt(now, 0, 2, 10),  // pos=5 → clamp 到最后一桶
		pt(now, 1, 3, 20),  // pos=4,与上一条同桶
		pt(now, 3, 1, 100), // pos=2,独立桶
	}

	snap, err := aggregate(points, bucketsFor(now, 5*time.Second, time.Second))
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if snap.WindowSeconds != 5 || snap.StepSeconds != 1 {
		t.Fatalf("window/step = %d/%d, want 5/1", snap.WindowSeconds, snap.StepSeconds)
	}
	if len(snap.Buckets) != 2 {
		t.Fatalf("buckets = %d, want 2(空桶剔除)", len(snap.Buckets))
	}

	b1, b2 := snap.Buckets[0], snap.Buckets[1]
	if b1.Count != 1 || b1.AvgMs != 100 || b1.MaxMs != 100 {
		t.Fatalf("bucket0 = %+v, want count=1 avg=max=100", b1)
	}
	if time.Time(b1.Timestamp).Unix() != now.Add(-3*time.Second).Unix() {
		t.Fatalf("bucket0 ts = %v, want %v", time.Time(b1.Timestamp), now.Add(-3*time.Second))
	}
	if b2.Count != 5 || b2.QPS != 5 {
		t.Fatalf("bucket1 count/qps = %d/%v, want 5/5", b2.Count, b2.QPS)
	}
	if b2.AvgMs != 16 {
		t.Fatalf("bucket1 avg = %v, want 16 (加权平均: (2*10+3*20)/5)", b2.AvgMs)
	}
	if b2.MaxMs != 20 {
		t.Fatalf("bucket1 max = %v, want 20", b2.MaxMs)
	}
	if snap.RecentQPS == 0 {
		t.Fatal("recent_qps 应包含最近一个 step 内的 5 条")
	}
}

// TestAggregateIgnoresOutdated 窗口外的点不参与任何统计。
func TestAggregateIgnoresOutdated(t *testing.T) {
	now := time.Date(2026, 9, 3, 20, 0, 30, 0, time.Local)
	snap, err := aggregate([]Point{pt(now, 30, 1, 50)}, bucketsFor(now, 10*time.Second, time.Second))
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if len(snap.Buckets) != 0 {
		t.Fatalf("buckets = %d, want 0(窗口外)", len(snap.Buckets))
	}
	if snap.RecentQPS != 0 {
		t.Fatalf("recent_qps = %v, want 0", snap.RecentQPS)
	}
}

// TestAggregateBucketCap 桶数超限时 step 自动放大:24h@1s=86400 桶
// > 4000 → step = ceil(86400/4000) = 22s。
func TestAggregateBucketCap(t *testing.T) {
	now := time.Date(2026, 9, 3, 20, 0, 0, 0, time.Local)
	snap, err := aggregate([]Point{pt(now, 0, 1, 1)}, bucketsFor(now, 24*time.Hour, time.Second))
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if snap.StepSeconds != 22 {
		t.Fatalf("step_seconds = %d, want 22 (自动放大)", snap.StepSeconds)
	}
}

// TestAggregateQuantizesToTwoDecimals 验证 SQL 历史聚合输出量化到 2 位小数:
// count 加权平均((3*1.23+4*2.34)/7 = 1.8642857...)与 QPS(7/3 = 2.3333...)
// 都必须输出干净 2 位。
func TestAggregateQuantizesToTwoDecimals(t *testing.T) {
	now := time.Date(2026, 9, 6, 18, 0, 30, 0, time.Local)
	points := []Point{
		pt(now, 1, 3, 1.23), // 同一 3s 桶:count=3,各耗时字段 1.23
		pt(now, 2, 4, 2.34), // 同一 3s 桶:count=4,各耗时字段 2.34
	}
	snap, err := aggregate(points, bucketsFor(now, 9*time.Second, 3*time.Second))
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if len(snap.Buckets) != 1 {
		t.Fatalf("buckets = %d, want 1", len(snap.Buckets))
	}
	b := snap.Buckets[0]
	if b.Count != 7 {
		t.Fatalf("count = %d, want 7", b.Count)
	}
	// QPS = 7/3 = 2.3333... → 2.33
	if b.QPS != 2.33 {
		t.Fatalf("qps = %v, want 2.33", b.QPS)
	}
	// 加权平均 = (3*1.23 + 4*2.34)/7 = 13.05/7 = 1.8642857... → 1.86
	if b.AvgMs != 1.86 {
		t.Fatalf("avg_ms = %v, want 1.86", b.AvgMs)
	}
	if b.P50Ms != 1.86 || b.P95Ms != 1.86 || b.P99Ms != 1.86 {
		t.Fatalf("p50/p95/p99 = %v/%v/%v, want 1.86(输入各分位相同)", b.P50Ms, b.P95Ms, b.P99Ms)
	}
	// max(1.23, 2.34) = 2.34
	if b.MaxMs != 2.34 {
		t.Fatalf("max_ms = %v, want 2.34", b.MaxMs)
	}
	// RecentQPS:两点都落在最近一个 step 内 → 7/3 → 2.33
	if snap.RecentQPS != 2.33 {
		t.Fatalf("recent_qps = %v, want 2.33", snap.RecentQPS)
	}
}
