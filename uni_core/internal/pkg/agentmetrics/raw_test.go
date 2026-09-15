package agentmetrics

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

func newTestRedis(t *testing.T) (*miniredis.Miniredis, goredis.UniversalClient) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return mr, rdb
}

func sample(t *testing.T, tsMs int64, cpu float64) *agentproto.MetricsSample {
	t.Helper()
	s := &agentproto.MetricsSample{T: tsMs, CPUUsedPercent: cpu}
	s.Load1, s.Load5, s.Load15 = 0.5, 0.4, 0.3
	s.MemUsedPercent, s.MemUsedMB, s.MemAvailableMB = 60, 8000, 5000
	s.TCPTotal, s.TCPEstablished, s.TCPListen, s.UDPTotal = 100, 40, 10, 5
	s.ProcCount, s.UptimeSec = 300, 86400
	s.Agent = agentproto.AgentMetric{CollectDurationMs: 12, ReportSuccessCount: 1}
	if err := s.Validate(); err != nil {
		t.Fatalf("测试样本必须合法: %v", err)
	}
	return s
}

func TestRawStoreAppendAndBucket(t *testing.T) {
	_, rdb := newTestRedis(t)
	store := NewRawStore(rdb, RawOptions{Step: 10 * time.Second, MaxPoints: 1000})
	ctx := context.Background()

	// 三个样本，前两个落在 [1000000, 1300000) 内，第三个在外
	for _, ts := range []int64{1_000_000, 1_100_000, 1_400_000} {
		if err := store.Append(ctx, 1001, sample(t, ts, 10)); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	got, err := store.Bucket(ctx, 1001, 1_000_000, 1_300_000)
	if err != nil {
		t.Fatalf("Bucket: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("桶内样本数 = %d, want 2（T ∈ [from,to)）", len(got))
	}
	// 按 T 升序返回
	if got[0].T != 1_000_000 || got[1].T != 1_100_000 {
		t.Fatalf("桶内样本必须按 T 升序: %d, %d", got[0].T, got[1].T)
	}

	// 另一个设备互不干扰
	if n, _ := store.Bucket(ctx, 2002, 0, 9_999_999_999); len(n) != 0 {
		t.Fatalf("不同设备的桶必须隔离，实际 %d 条", len(n))
	}
}

func TestRawStoreQueryAggregatesIntoBuckets(t *testing.T) {
	_, rdb := newTestRedis(t)
	store := NewRawStore(rdb, RawOptions{Step: 10 * time.Second, MaxPoints: 1000, QueryTTL: time.Millisecond})
	ctx := context.Background()
	now := time.Now().UnixMilli()

	// 在最近 1 分钟里每 10s 一个样本，CPU 依次 10,20,30…
	for i := 0; i < 6; i++ {
		ts := now - int64((5-i)*10_000)
		if err := store.Append(ctx, 1001, sample(t, ts, float64(10*(i+1)))); err != nil {
			t.Fatal(err)
		}
	}

	snap, err := store.Query(ctx, 1001, time.Minute, 30*time.Second)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if snap.WindowSeconds != 60 || snap.StepSeconds != 30 {
		t.Fatalf("window/step 回显不符: %d/%d", snap.WindowSeconds, snap.StepSeconds)
	}
	if len(snap.Buckets) == 0 {
		t.Fatal("必须至少产出 1 个桶")
	}
	for _, b := range snap.Buckets {
		if b.T <= 0 {
			t.Fatalf("桶时间必须是正 unix 秒: %d", b.T)
		}
		if b.CPUUsedPercent == nil {
			t.Fatal("有样本的桶里 cpu_used_percent 不应为 nil")
		}
	}
}

func TestRawStoreIndexAndPurge(t *testing.T) {
	_, rdb := newTestRedis(t)
	store := NewRawStore(rdb, RawOptions{Step: 10 * time.Second, MaxPoints: 100})
	ctx := context.Background()

	for _, id := range []uint64{1001, 1002} {
		if err := store.Append(ctx, id, sample(t, time.Now().UnixMilli(), 1)); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := store.Index(ctx)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("活跃设备数 = %d, want 2", len(ids))
	}

	if err := store.Purge(ctx, 1001); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	ids, _ = store.Index(ctx)
	if len(ids) != 1 || ids[0] != 1002 {
		t.Fatalf("Purge 后活跃设备应只剩 1002, got %v", ids)
	}
	if got, _ := store.Bucket(ctx, 1001, 0, 9_999_999_999_999); len(got) != 0 {
		t.Fatalf("Purge 后原始点必须已删除, got %d", len(got))
	}
	store.Forget(1001) // 幂等，不 panic
}
