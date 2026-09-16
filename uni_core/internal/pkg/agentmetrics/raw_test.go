package agentmetrics

import (
	"context"
	"encoding/json"
	"reflect"
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

// ── BucketRange：一次读覆盖整段（消除逐桶读取的读放大，Plan 2E Task 1）─────
//
// 背景：flush 曾**逐桶**调用 Bucket，而 fetch（= LRange 取多少条）由「最新点离该桶起点
// 多远」推出（见 BucketRange 的注释）——于是积压越久、每桶读得越多：积压 24h 的首轮
// 288 个桶最坏各读 MaxPoints（10368）条 ≈ 2.5M 条 JSON，且同一批点被重复读 288 遍。
// BucketRange 让「读多少」只取决于**最老**的那个桶，一次 LRange 覆盖整段。

// TestBucketRangeMatchesPerBucketReadsExactly 守卫等价性：同一段区间上，
// 「一次 BucketRange + 按 T 切出的每桶子集」必须与「逐桶调用 Bucket」**逐条相同**
// ——同样的左闭右开边界、同样的升序、同样的脏数据跳过、同样的字段值。
//
// 这是 flush 改造（读一次 + 内存切桶）的行为等价依据：取点方式变了，取到的点不能变。
func TestBucketRangeMatchesPerBucketReadsExactly(t *testing.T) {
	_, rdb := newTestRedis(t)
	store := NewRawStore(rdb, RawOptions{Step: 10 * time.Second, MaxPoints: 1000})
	ctx := context.Background()

	const (
		pointsPerBucket = 30
		bucketCount     = 10
	)
	total := pointsPerBucket * bucketCount
	for i := 0; i < total; i++ {
		ts := rawTestBase + int64(i)*rawTestStepMs
		if err := store.Append(ctx, 1001, sample(t, ts, float64(i%90))); err != nil {
			t.Fatalf("Append #%d: %v", i, err)
		}
	}
	// 请求区间**之外**的两个点（左外一个、右外一个）：必须被 [fromMs,toMs) 过滤掉，
	// 且不得混进任何桶。
	span := int64(bucketCount) * rawTestBucketMs
	for _, ts := range []int64{rawTestBase - rawTestStepMs, rawTestBase + span} {
		if err := store.Append(ctx, 1001, sample(t, ts, 7)); err != nil {
			t.Fatalf("Append 区间外点 %d: %v", ts, err)
		}
	}
	// 两条脏 JSON（只可能来自旧版本协议）：直接替换列表中间的两条（index 从头部数，
	// 即最新在前）。逐桶读会跳过它们，整段读必须同样跳过 —— 而不是整段失败。
	for _, idx := range []int64{5, 155} {
		if err := rdb.LSet(ctx, historyKey(1001), idx, "{不是 JSON").Err(); err != nil {
			t.Fatalf("LSet #%d: %v", idx, err)
		}
	}

	whole, err := store.BucketRange(ctx, 1001, rawTestBase, rawTestBase+span)
	if err != nil {
		t.Fatalf("BucketRange: %v", err)
	}
	// 区间内的点 = 300 − 2 条脏数据；区间外的 2 个点不得混进来。
	if len(whole) != total-2 {
		t.Fatalf("整段点数 = %d, want %d（脏数据必须跳过、区间外的点必须过滤）", len(whole), total-2)
	}
	for i := 1; i < len(whole); i++ {
		if whole[i-1].T >= whole[i].T {
			t.Fatalf("整段必须按 T 严格升序: [%d]=%d, [%d]=%d", i-1, whole[i-1].T, i, whole[i].T)
		}
	}

	var seen int
	for k := 0; k < bucketCount; k++ {
		from := rawTestBase + int64(k)*rawTestBucketMs
		to := from + rawTestBucketMs
		perBucket, err := store.Bucket(ctx, 1001, from, to)
		if err != nil {
			t.Fatalf("桶 #%d 逐桶读: %v", k, err)
		}
		// 用 T 判界从整段里切出该桶：**不复用被测的 groupByBucket**（那在 service 包），
		// 这里只信「整段一次读」的结果。
		var sliced []agentproto.MetricsSample
		for _, p := range whole {
			if p.T >= from && p.T < to {
				sliced = append(sliced, p)
			}
		}
		if len(sliced) != len(perBucket) {
			t.Fatalf("桶 #%d 点数：整段切桶 = %d, 逐桶读 = %d（取点方式不得改变结果）",
				k, len(sliced), len(perBucket))
		}
		for i := range sliced {
			if !reflect.DeepEqual(sliced[i], perBucket[i]) {
				t.Fatalf("桶 #%d 第 %d 条：整段切桶 = %+v, 逐桶读 = %+v",
					k, i, sliced[i], perBucket[i])
			}
		}
		seen += len(sliced)
	}
	if seen != len(whole) {
		t.Fatalf("逐桶切出的点数之和 = %d, 整段 = %d（有点落在所有桶之外或漏桶）", seen, len(whole))
	}
}

// TestBucketRangeCovers288BucketsWithConstantRedisCommands 直接钉住「读放大被消除」：
// 覆盖 288 个桶（= 24h 积压）的一次 BucketRange 调用，miniredis 的命令增量必须是
// **常数级**（1 次 LINDEX 探最新点 + 1 次 LRange），而不是 288（或 576）次。
func TestBucketRangeCovers288BucketsWithConstantRedisCommands(t *testing.T) {
	mr, rdb := newTestRedis(t)
	store := NewRawStore(rdb, RawOptions{Step: 10 * time.Second, MaxPoints: 10000})
	ctx := context.Background()

	const (
		bucketCount = 288 // 24h / 5min
		perBucket   = 3   // 每桶 3 个点：命令数只取决于「读几次」，不取决于点密度
	)
	for k := 0; k < bucketCount; k++ {
		base := rawTestBase + int64(k)*rawTestBucketMs
		for i := 0; i < perBucket; i++ {
			if err := store.Append(ctx, 1001, sample(t, base+int64(i)*rawTestStepMs, 50)); err != nil {
				t.Fatalf("Append 桶 #%d 点 #%d: %v", k, i, err)
			}
		}
	}
	span := int64(bucketCount) * rawTestBucketMs

	// 预热一次：把建连时的握手命令排除在计数之外。
	if _, err := store.BucketRange(ctx, 1001, rawTestBase, rawTestBase+1); err != nil {
		t.Fatalf("预热调用: %v", err)
	}

	before := mr.CommandCount()
	got, err := store.BucketRange(ctx, 1001, rawTestBase, rawTestBase+span)
	if err != nil {
		t.Fatalf("BucketRange: %v", err)
	}
	all := mr.CommandCount() - before
	if all != 2 {
		t.Fatalf("覆盖 %d 个桶的 BucketRange 必须只发 2 次 Redis 请求（LINDEX + LRange），实际 %d",
			bucketCount, all)
	}
	if len(got) != bucketCount*perBucket {
		t.Fatalf("一次读必须覆盖全部 %d 个桶: 返回 %d 条, want %d",
			bucketCount, len(got), bucketCount*perBucket)
	}

	// 对照（读数放大本体）：逐桶读取的请求数随桶数**线性增长**。
	before = mr.CommandCount()
	var perBucketPts int
	for k := 0; k < bucketCount; k++ {
		from := rawTestBase + int64(k)*rawTestBucketMs
		pts, err := store.Bucket(ctx, 1001, from, from+rawTestBucketMs)
		if err != nil {
			t.Fatalf("逐桶读 #%d: %v", k, err)
		}
		perBucketPts += len(pts)
	}
	each := mr.CommandCount() - before
	if perBucketPts != len(got) {
		t.Fatalf("逐桶读取的点数之和 = %d, 整段读 = %d（两者必须取到同一批点）", perBucketPts, len(got))
	}
	if each < bucketCount {
		t.Fatalf("逐桶读取的请求数 = %d，应随桶数线性增长（≥ %d 次）；整段读 = %d 次",
			each, bucketCount, all)
	}
}
