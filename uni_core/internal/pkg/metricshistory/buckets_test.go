package metricshistory

import (
	"testing"
	"time"
)

// TestAlignBucketsBasic 5m/60s:5 桶、t0 为绝对时间对齐、cutoff=now-5m。
func TestAlignBucketsBasic(t *testing.T) {
	now := time.Date(2026, 9, 9, 20, 0, 30, 0, time.Local)
	b, err := AlignBucketsAt(now, 5*time.Minute, 60*time.Second)
	if err != nil {
		t.Fatalf("align: %v", err)
	}
	if b.WindowSec != 300 || b.StepSec != 60 || b.N != 5 {
		t.Fatalf("window/step/n = %d/%d/%d, want 300/60/5", b.WindowSec, b.StepSec, b.N)
	}
	if got := b.Cutoff(); !got.Equal(now.Add(-5 * time.Minute)) {
		t.Fatalf("cutoff = %v, want %v", got, now.Add(-5*time.Minute))
	}
	// t0 = cutoff 对 step 向下取整:now=:30、window=5m → cutoff=19:55:30
	// → 60s 对齐后 t0=19:55:00(即 now-330s)。
	if got := b.Timestamp(0); !got.Equal(time.Unix(now.Unix()-330, 0)) {
		t.Fatalf("t0 = %v, want %v", got, time.Unix(now.Unix()-330, 0))
	}
}

// TestAlignBucketsBucketCap 24h@1s=86400 桶 > 4000 → step 放大为
// ceil(86400/4000)=22s(与三处旧实现同值)。
func TestAlignBucketsBucketCap(t *testing.T) {
	now := time.Date(2026, 9, 9, 20, 0, 0, 0, time.Local)
	b, err := AlignBucketsAt(now, 24*time.Hour, time.Second)
	if err != nil {
		t.Fatalf("align: %v", err)
	}
	if b.StepSec != 22 {
		t.Fatalf("step_seconds = %d, want 22(自动放大)", b.StepSec)
	}
	if b.N <= 0 || b.N > 4000 {
		t.Fatalf("n = %d, want (0,4000]", b.N)
	}
}

// TestAlignBucketsPos 桶定位:边界点折叠进最后一桶;窗口外返回 !ok。
func TestAlignBucketsPos(t *testing.T) {
	now := time.Date(2026, 9, 9, 20, 0, 30, 0, time.Local)
	b, err := AlignBucketsAt(now, 5*time.Second, time.Second)
	if err != nil {
		t.Fatalf("align: %v", err)
	}
	if pos, ok := b.Pos(now.Unix()); !ok || pos != 4 {
		t.Fatalf("boundary pos = (%d,%v), want (4,true)[折叠进最后一桶]", pos, ok)
	}
	if pos, ok := b.Pos(now.Unix() - 3); !ok || pos != 2 {
		t.Fatalf("mid pos = (%d,%v), want (2,true)", pos, ok)
	}
	if _, ok := b.Pos(now.Unix() - 6); ok {
		t.Fatal("窗口外(早于 cutoff)应返回 !ok")
	}
}

// TestAlignBucketsErrors 非法参数:非正数、step<1s、step>window。
func TestAlignBucketsErrors(t *testing.T) {
	if _, err := AlignBuckets(0, time.Second); err == nil {
		t.Fatal("window<=0 应报错")
	}
	if _, err := AlignBuckets(time.Minute, 500*time.Millisecond); err == nil {
		t.Fatal("step<1s 应报错")
	}
	if _, err := AlignBuckets(30*time.Second, time.Minute); err == nil {
		t.Fatal("step>window 应报错")
	}
}

// TestAlignBucketsQueriesNow AlignBuckets 用真实时钟:Now 约等于 time.Now()。
func TestAlignBucketsQueriesNow(t *testing.T) {
	before := time.Now()
	b, err := AlignBuckets(time.Minute, time.Second)
	if err != nil {
		t.Fatalf("align: %v", err)
	}
	after := time.Now()
	if b.Now.Before(before) || b.Now.After(after) {
		t.Fatalf("Now = %v, want ∈ [%v, %v]", b.Now, before, after)
	}
}
