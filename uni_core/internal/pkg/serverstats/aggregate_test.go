package serverstats

import (
	"testing"
	"time"

	"github.com/tangwy-t/webmanager-server/internal/pkg/metricshistory"
)

func f64p(v float64) *float64 { return &v }

// bucketsFor 构造测试用分桶方案(与 Query 路径同源;测试参数固定合法)。
func bucketsFor(now time.Time, window, step time.Duration) metricshistory.Buckets {
	b, err := metricshistory.AlignBucketsAt(now, window, step)
	if err != nil {
		panic(err)
	}
	return b
}

// 桶边界按绝对时间对齐;桶内均值;uptime/gcNum 取末值;空桶剔除。
func TestAggregateAlignmentAvgAndLastValue(t *testing.T) {
	now := time.Unix(1800000, 0)
	step := 60 * time.Second
	window := 5 * time.Minute
	ms := func(s int64) int64 { return s * 1000 }

	points := []Point{
		{T: ms(now.Unix() - 120), CPU: 10, MemSys: 20, GCNum: 7, Uptime: 100},
		{T: ms(now.Unix() - 90), CPU: 30, MemSys: 40, GCNum: 8, Uptime: 120},
		{T: ms(now.Unix() - 30), CPU: 50, MemSys: 60, GCNum: 9, Uptime: 140},
	}

	snap, err := aggregate(points, bucketsFor(now, window, step))
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if snap.WindowSeconds != 300 || snap.StepSeconds != 60 {
		t.Fatalf("window/step = %d/%d, want 300/60", snap.WindowSeconds, snap.StepSeconds)
	}
	if len(snap.Buckets) != 2 {
		t.Fatalf("buckets = %d, want 2", len(snap.Buckets))
	}

	// 前两点落在同一 60s 桶:均值 + 末值
	b0 := snap.Buckets[0]
	if b0.CPU == nil || *b0.CPU != 20 {
		t.Fatalf("b0.cpu = %v, want 20(10 与 30 的均值)", b0.CPU)
	}
	if b0.MemSys == nil || *b0.MemSys != 30 {
		t.Fatalf("b0.memSys = %v, want 30", b0.MemSys)
	}
	if b0.Uptime == nil || *b0.Uptime != 120 {
		t.Fatalf("b0.uptime = %v, want 120(末值)", b0.Uptime)
	}
	if b0.GCNum == nil || *b0.GCNum != 8 {
		t.Fatalf("b0.gcNum = %v, want 8(末值)", b0.GCNum)
	}

	// 桶时间戳 = 对齐后的绝对时间
	wantTS := ms(now.Unix()-30) - ms(now.Unix()-30)%(step.Milliseconds())
	if time.Time(snap.Buckets[1].Timestamp).UnixMilli() != wantTS {
		t.Fatalf("b1.timestamp = %v, want %d", snap.Buckets[1].Timestamp, wantTS)
	}
	if snap.Buckets[1].CPU == nil || *snap.Buckets[1].CPU != 50 {
		t.Fatalf("b1.cpu = %v, want 50", snap.Buckets[1].CPU)
	}
}

// 缺值字段(指针 nil)不参与平均;桶内全缺则该字段为 nil。
func TestAggregateSkipsMissingFields(t *testing.T) {
	now := time.Unix(1800000, 0)
	points := []Point{
		{T: now.UnixMilli() - 5000, CPU: 10, Load1: f64p(2.0)},
		{T: now.UnixMilli() - 3000, CPU: 30, Load1: nil},
	}
	snap, err := aggregate(points, bucketsFor(now, 10*time.Minute, 5*time.Second))
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if len(snap.Buckets) != 1 {
		t.Fatalf("buckets = %d, want 1", len(snap.Buckets))
	}
	b := snap.Buckets[0]
	if b.CPU == nil || *b.CPU != 20 {
		t.Fatalf("cpu = %v, want 20(两值平均)", b.CPU)
	}
	if b.Load1 == nil || *b.Load1 != 2.0 {
		t.Fatalf("load1 = %v, want 2.0(仅有的值参与平均)", b.Load1)
	}
	if b.GCPauseMs != nil {
		t.Fatalf("gcPauseMs = %v, want nil(全缺省略)", b.GCPauseMs)
	}
}

// 窗口外点丢弃;无有效点返回空桶集而非 nil。
func TestAggregateWindowCutoffAndEmpty(t *testing.T) {
	now := time.Unix(1800000, 0)
	old := Point{T: (now.Unix() - 3600) * 1000, CPU: 99}
	snap, err := aggregate([]Point{old}, bucketsFor(now, 5*time.Minute, 60*time.Second))
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if len(snap.Buckets) != 0 {
		t.Fatalf("buckets = %d, want 0(窗口外点应被丢弃)", len(snap.Buckets))
	}
	snap2, err := aggregate(nil, bucketsFor(now, 5*time.Minute, 60*time.Second))
	if err != nil {
		t.Fatalf("aggregate empty: %v", err)
	}
	if snap2.Buckets == nil || len(snap2.Buckets) != 0 {
		t.Fatalf("empty input buckets = %#v, want 空切片", snap2.Buckets)
	}
}

// 聚合均值必须量化到 2 位小数:即使输入已是 2 位,均值仍会带长尾
// (如 1.23/1.23/1.24 平均 = 1.2333333333333334),输出应保持干净 2 位。
// 用 3 个采样点让均值远离 .005 边界,断言不受浮点表示误差影响。
func TestAggregateQuantizesMeanToTwoDecimals(t *testing.T) {
	now := time.Unix(1800000, 0)
	points := []Point{
		{T: now.UnixMilli() - 3000, CPU: 12.27, MemSys: 1.23, HeapAlloc: 0.333, Disk: 80.01, Load1: f64p(0.666)},
		{T: now.UnixMilli() - 2000, CPU: 12.26, MemSys: 1.23, HeapAlloc: 0.334, Disk: 80.02, Load1: f64p(0.667)},
		{T: now.UnixMilli() - 1000, CPU: 12.26, MemSys: 1.24, HeapAlloc: 0.334, Disk: 80.02, Load1: f64p(0.667)},
	}
	snap, err := aggregate(points, bucketsFor(now, time.Minute, 5*time.Second))
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if len(snap.Buckets) != 1 {
		t.Fatalf("buckets = %d, want 1", len(snap.Buckets))
	}
	b := snap.Buckets[0]
	// (12.27+12.26+12.26)/3 = 12.2633... → 12.26
	if b.CPU == nil || *b.CPU != 12.26 {
		t.Fatalf("cpu = %v, want 12.26", b.CPU)
	}
	// (1.23+1.23+1.24)/3 = 1.2333... → 1.23
	if b.MemSys == nil || *b.MemSys != 1.23 {
		t.Fatalf("memSys = %v, want 1.23", b.MemSys)
	}
	// (0.333+0.334+0.334)/3 = 0.33366... → 0.33
	if b.HeapAlloc == nil || *b.HeapAlloc != 0.33 {
		t.Fatalf("heapAlloc = %v, want 0.33", b.HeapAlloc)
	}
	// (80.01+80.02+80.02)/3 = 80.0166... → 80.02
	if b.Disk == nil || *b.Disk != 80.02 {
		t.Fatalf("disk = %v, want 80.02", b.Disk)
	}
	// (0.666+0.667+0.667)/3 = 0.6666... → 0.67
	if b.Load1 == nil || *b.Load1 != 0.67 {
		t.Fatalf("load1 = %v, want 0.67", b.Load1)
	}
	if b.GCPauseMs != nil {
		t.Fatalf("gcPauseMs = %v, want nil(未采样则省略)", b.GCPauseMs)
	}
}
