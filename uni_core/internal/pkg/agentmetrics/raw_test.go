package agentmetrics

import (
	"context"
	"encoding/json"
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

// ── 桶读取的可见范围守卫（P1）─────────────────────────────────────────
//
// 背景：原始窗用 LPUSH 写入（metricshistory.Window.Append），**index 0 是最新点**。
// 因此「LRange 取多少条」的正确答案是「从最新点往回覆盖到 fromMs 需要多少条」，
// 与**请求窗口的宽度**无关。曾经按窗口宽度估算 fetch，导致 flush 逐桶回填时
// 所有老桶都被读成空桶 → 水位越过 → device_metric_5m 永久空洞。
//
// 既有用例抓不到它：TestRawStoreAppendAndBucket 只放 3 个点，最新 60 点的切片
// 恰好覆盖全部。下面这组用例用「多点多桶」把这个盲区堵上。

const (
	rawTestStepMs   = int64(10_000)  // 10s 上报节奏
	rawTestBucketMs = int64(300_000) // 5min 桶
	// rawTestBase 是固定基准时间：本组用例完全不依赖墙钟。
	rawTestBase = int64(1_700_000_000_000)
)

// TestRawStoreBucketSeesEveryBucketOfFullWindow 是缺陷 #1 的**直接**守卫：
// 窗里放 300 个点（每 10s，覆盖 50 分钟），**逐桶**断言每个 5min 桶都返回 30 点，
// **含最老的那些桶**（离最新点 50 分钟，远超旧实现的可见范围 fetch×Step ≈ 10 分钟）。
//
// 修前必须变红（老桶返回 0 点），修后全绿。
func TestRawStoreBucketSeesEveryBucketOfFullWindow(t *testing.T) {
	_, rdb := newTestRedis(t)
	// MaxPoints 取 1000：远大于 300 点，保证「读不满」只可能来自 fetch 推错，
	// 而不是来自容量上限。
	store := NewRawStore(rdb, RawOptions{Step: 10 * time.Second, MaxPoints: 1000})
	ctx := context.Background()

	const (
		pointsPerBucket = 30
		bucketCount     = 10
		total           = pointsPerBucket * bucketCount // 300 个点 = 50 分钟
	)
	for i := 0; i < total; i++ {
		ts := rawTestBase + int64(i)*rawTestStepMs
		if err := store.Append(ctx, 1001, sample(t, ts, 50)); err != nil {
			t.Fatalf("Append #%d: %v", i, err)
		}
	}

	for k := 0; k < bucketCount; k++ {
		from := rawTestBase + int64(k)*rawTestBucketMs
		to := from + rawTestBucketMs
		got, err := store.Bucket(ctx, 1001, from, to)
		if err != nil {
			t.Fatalf("桶 #%d [%d,%d): %v", k, from, to, err)
		}
		if len(got) != pointsPerBucket {
			t.Fatalf("桶 #%d [%d,%d) 内样本数 = %d, want %d"+
				"（桶离现在多远都必须读得到；旧实现只看得见最新 fetch×Step ≈ 10 分钟）",
				k, from, to, len(got), pointsPerBucket)
		}
		// 内容也要对：恰好是这 30 个点，且按 T 升序
		for i, p := range got {
			want := from + int64(i)*rawTestStepMs
			if p.T != want {
				t.Fatalf("桶 #%d 第 %d 个点 T = %d, want %d", k, i, p.T, want)
			}
		}
	}
}

// TestRawStoreBucketBoundaryAtNewestPointAndFutureWindow 守卫两个边界：
//  1. 请求的桶**从最新点本身**开始（fromMs == newestT）→ 必须能读到最新点本身；
//  2. 请求一个**未来**窗口（fromMs > newestT）→ 返回空且不报错、不 panic；
//  3. 退化空区间（fromMs == toMs）→ 同样不 panic、不报错。
func TestRawStoreBucketBoundaryAtNewestPointAndFutureWindow(t *testing.T) {
	_, rdb := newTestRedis(t)
	store := NewRawStore(rdb, RawOptions{Step: 10 * time.Second, MaxPoints: 1000})
	ctx := context.Background()

	var newest int64
	for i := 0; i < 5; i++ {
		newest = rawTestBase + int64(i)*rawTestStepMs
		if err := store.Append(ctx, 1001, sample(t, newest, float64(10+i*10))); err != nil {
			t.Fatal(err)
		}
	}

	// 1) 桶的左端点恰好等于最新点：这一条必须被读到（不能因为「最新点是头部」
	//    就在边界上丢掉）。
	got, err := store.Bucket(ctx, 1001, newest, newest+rawTestBucketMs)
	if err != nil {
		t.Fatalf("左端点=最新点的桶: %v", err)
	}
	if len(got) != 1 || got[0].T != newest {
		t.Fatalf("左端点=最新点的桶应恰好返回最新点 %d, got %+v", newest, got)
	}

	// 2) 未来窗口：最新点还没走到 fromMs，区间内不可能有数据。
	futureFrom := newest + rawTestStepMs
	got, err = store.Bucket(ctx, 1001, futureFrom, futureFrom+rawTestBucketMs)
	if err != nil {
		t.Fatalf("未来窗口必须返回空而不是报错: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("未来窗口内不应有样本, got %d", len(got))
	}

	// 3) 退化空区间：toMs == fromMs。
	if got, err = store.Bucket(ctx, 1001, newest, newest); err != nil || len(got) != 0 {
		t.Fatalf("空区间 [x,x) 应返回空且无错误, got %d, err %v", len(got), err)
	}
}

// TestRawStoreBucketExtremelyOldWindowIsBounded 守卫「极老窗口」：
//   - 不得报错（最老窗口的 fetch 远大于窗里真实点数）；
//   - 读取量必须被 MaxPoints 夹住，**不得**退化成「读全窗」；
//   - 空窗/非空窗都只发 1~2 次 Redis 请求（LINDEX 探最新点 + 一次 LRange）。
//
// 夹具故意用 rdb.LPush 直接写入，绕过 Append 的 LTRIM：这样窗里点数（1200）
// 多于 MaxPoints（300），若读取量没被夹住，就必须额外触达容量以外的点。
func TestRawStoreBucketExtremelyOldWindowIsBounded(t *testing.T) {
	mr, rdb := newTestRedis(t)
	const maxPoints = 300
	store := NewRawStore(rdb, RawOptions{Step: 10 * time.Second, MaxPoints: maxPoints})
	ctx := context.Background()

	key := historyKey(1001)
	total := 1200
	for i := 0; i < total; i++ {
		payload, err := json.Marshal(*sample(t, rawTestBase+int64(i)*rawTestStepMs, 50))
		if err != nil {
			t.Fatal(err)
		}
		if err := rdb.LPush(ctx, key, payload).Err(); err != nil {
			t.Fatalf("LPush #%d: %v", i, err)
		}
	}

	// 预热一次：把建连时的握手命令排除在计数之外。
	if _, err := store.Bucket(ctx, 1001, rawTestBase, rawTestBase+1); err != nil {
		t.Fatalf("预热调用: %v", err)
	}
	before := mr.CommandCount()

	// 请求「窗里留下数据的 3.3 小时里最老的那一小时」：离最新点极远。
	got, err := store.Bucket(ctx, 1001, rawTestBase, rawTestBase+3_600_000)
	if err != nil {
		t.Fatalf("极老窗口不得报错: %v", err)
	}
	if int64(len(got)) > maxPoints {
		t.Fatalf("读取量必须被 MaxPoints 夹住: 返回 %d > %d", len(got), maxPoints)
	}
	if d := mr.CommandCount() - before; d != 2 {
		t.Fatalf("极老窗口应恰好 2 次 Redis 请求（LINDEX + LRange），实际 %d", d)
	}
}

// TestRawStoreBucketEmptyWindowIssuesSingleRedisCall 守卫空窗：
// 列表为空时返回空切片，且**只发一次** Redis 请求（LINDEX 就足以判空，
// 不必再发一次注定空手而归的 LRange）。
func TestRawStoreBucketEmptyWindowIssuesSingleRedisCall(t *testing.T) {
	mr, rdb := newTestRedis(t)
	store := NewRawStore(rdb, RawOptions{Step: 10 * time.Second, MaxPoints: 1000})
	ctx := context.Background()

	// 预热一次：把建连时的握手命令排除在计数之外。
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	before := mr.CommandCount()

	got, err := store.Bucket(ctx, 4321, rawTestBase, rawTestBase+rawTestBucketMs)
	if err != nil {
		t.Fatalf("空窗不得报错: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("空窗应返回空, got %d", len(got))
	}
	if got == nil {
		t.Fatal("空窗应返回非 nil 空切片（调用方不应被逼着判 nil）")
	}
	if d := mr.CommandCount() - before; d != 1 {
		t.Fatalf("空窗必须只发 1 次 Redis 请求（LINDEX），实际 %d", d)
	}
}
