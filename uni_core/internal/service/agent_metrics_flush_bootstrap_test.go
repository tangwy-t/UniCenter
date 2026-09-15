package service

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agentmetrics"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
)

// newBareFlushService 只装配 flush 需要的最小依赖（不需要 DB；写侧用替身）。
// 用于**纯区间/水位语义**的守卫：这里测的是「起点与区间」，不是落库。
func newBareFlushService(t *testing.T, now time.Time, reportSec int) (*AgentMetricsFlushService, *agentmetrics.RawStore, goredis.UniversalClient) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	raw := agentmetrics.NewRawStore(rdb, agentmetrics.RawOptions{Step: 10 * time.Second, MaxPoints: 1000})
	svc := NewAgentMetricsFlushService(raw, &fakeBucketWriter{}, noopResourceRepo{}, fakeConfig{reportSec: reportSec}, logger.NewNop()).
		WithCursorStore(rdb).
		WithClock(func() time.Time { return now })
	return svc, raw, rdb
}

// noopResourceRepo 是资源维度仓储的替身（本守卫不检查资源解析）。
type noopResourceRepo struct{}

func (noopResourceRepo) UpsertSeen(context.Context, []entity.DeviceResource, time.Time) error {
	return nil
}

func (noopResourceRepo) ResolveID(context.Context, uint64, string, string) (uint64, error) {
	return 1, nil
}

func seedRaw(t *testing.T, raw *agentmetrics.RawStore, tMs int64) {
	t.Helper()
	s := flushSample(tMs, 20, "/", 20, 60)
	if err := raw.Append(context.Background(), flushDevID, &s); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// cursorSec 读水位键（键不存在即 Fatal：本守卫里它必须存在）。
func cursorSec(t *testing.T, rdb goredis.UniversalClient) int64 {
	t.Helper()
	v, err := rdb.Get(context.Background(), flushCursorKey(flushDevID)).Int64()
	if err != nil {
		t.Fatalf("读 %s: %v（水位必须落回 Redis）", flushCursorKey(flushDevID), err)
	}
	return v
}

// TestFlushBootstrapStartIs24HoursAgo 是「起点 = now−24h」的**直接**守卫。
//
// 为什么需要它：起点是「本轮处理哪个区间」的唯一决定因素，但既有测试只能**间接**证明它
// （数桶）。这条间接证据有个致命的反向验证问题 —— 把起点临时改成 0（旧行为）后，
// 一轮要扫 6_000_000+ 个桶，于是「断言变红」表现为**测试超时被杀**，而不是一条可读的失败。
// 本守卫直接读实现实际会用的起点（CursorFor），毫秒级、可读地钉住它：
//   - 起点太大 → 最近的已闭桶被跳过（数据永久丢失）；
//   - 起点太小（0）→ 每轮扫全历史（且 24h 之外的桶必然是空的，纯浪费）。
func TestFlushBootstrapStartIs24HoursAgo(t *testing.T) {
	// ① 窗口完整落在正时间轴内：起点 = now − 24h（对齐到 5min）。
	const base = int64(1800000000) // 300 的整数倍
	now := time.Unix(base+24*3600, 0)
	svc, _, rdb := newBareFlushService(t, now, 10)
	got, err := svc.CursorFor(context.Background(), flushDevID)
	if err != nil {
		t.Fatalf("CursorFor: %v", err)
	}
	if got != base {
		t.Fatalf("起点 = %d, want %d（= now−24h 对齐；0 或其它值都说明 Bootstrap 起点退化）", got, base)
	}
	if cur := cursorSec(t, rdb); cur != got {
		t.Fatalf("起点必须落回 Redis：键值 = %d, CursorFor = %d", cur, got)
	}

	// ② 对齐：now 不是 5min 边界时，起点必须**向下**对齐（否则区间下界会落在桶中间，
	// 每个桶被算两次或漏算）。
	unaligned := time.Unix(base+24*3600+137, 0)
	svc2, _, _ := newBareFlushService(t, unaligned, 10)
	got2, err := svc2.CursorFor(context.Background(), flushDevID)
	if err != nil {
		t.Fatalf("CursorFor(2): %v", err)
	}
	if want := alignDown300(base + 137); got2 != want {
		t.Fatalf("起点 = %d, want %d（now 未对齐时起点要向下对齐到 5min 边界）", got2, want)
	}
}

// TestFlushBootstrapStartClampsAtEpoch 是起点**下界**的守卫：`now−24h` 为负时必须夹到 0。
//
// 为什么必须夹：负水位会被真的写进 Redis，下一轮 `from = cursor+300` 仍为负，
// 于是每轮都从「纪元之前」重扫；它与「从 0 起步」在语义上无法区分（都是扫全部可得历史），
// 只是把同一个缺陷换了个符号 —— 而 5min 栅格在 unix 秒域里以 0 为界。
// 这里用「纪元左右」的 now，让 0 起点与正确起点**只差几个桶**，退化立刻体现在计数上。
func TestFlushBootstrapStartClampsAtEpoch(t *testing.T) {
	now := time.Unix(1800, 0) // now−24h = −84600 → 必须夹到 0
	svc, raw, rdb := newBareFlushService(t, now, 10)
	if got, err := svc.CursorFor(context.Background(), flushDevID); err != nil || got != 0 {
		t.Fatalf("起点 = %d (err=%v), want 0（now−24h 为负必须夹到 0，不得写入负水位）", got, err)
	}
	// 上界 = alignDown(now−20s,300) = 1500（半开），故本轮处理 [300,1500) 共 4 个桶。
	// 种子放进最后一个桶 [1200,1500)：它必须被落库；[1500,1800) 那个桶**不得**被碰。
	seedRaw(t, raw, 1200*1000)

	stats, err := svc.FlushOnce(context.Background())
	if err != nil {
		t.Fatalf("FlushOnce: %v", err)
	}
	if stats.BucketsSkipped != 3 {
		t.Fatalf("BucketsSkipped = %d, want 3（起点夹到 0 → 区间 [300,1500) 共 4 个桶）", stats.BucketsSkipped)
	}
	if stats.BucketsWritten != 1 {
		t.Fatalf("BucketsWritten = %d, want 1", stats.BucketsWritten)
	}
	if got := cursorSec(t, rdb); got != 1200 {
		t.Fatalf("cursor_5m = %d, want 1200（本轮最后一个桶）", got)
	}
}

// TestFlushBootstrapCoversExactly24Hours 钉住「整 24h、一桶不多不少」的区间计数。
//
// 前提（起点 = now−24h）由 TestFlushBootstrapStartIs24HoursAgo 直接守卫；
// 这里验的是**区间端点**：下界是 cursor+300（cursor 自己那个桶不重放）、
// 上界是半开的 alignDown(now−CloseGrace,300)（还在收数据的桶不写）。
func TestFlushBootstrapCoversExactly24Hours(t *testing.T) {
	const base = int64(1800000000) // 300 的整数倍
	now := time.Unix(base+24*3600, 0)
	svc, raw, rdb := newBareFlushService(t, now, 10)
	// 种子放进本轮第一个会被处理的桶 [base+300, base+600)
	seedRaw(t, raw, (base+300)*1000)

	stats, err := svc.FlushOnce(context.Background())
	if err != nil {
		t.Fatalf("FlushOnce: %v", err)
	}
	// 起点 = base → 上界 = base+86100 = alignDown(now−20s,300)，区间 [base+300, base+86100)
	// 恰好 286 个桶（1 个含样本 → written，285 空 → skipped）。
	if stats.BucketsSkipped+stats.BucketsWritten != 286 {
		t.Fatalf("本轮处理的桶数 = %d, want 286（=24h/5min；起点 %d 时必须恰好这么多）",
			stats.BucketsSkipped+stats.BucketsWritten, base)
	}
	if stats.BucketsSkipped != 285 || stats.BucketsWritten != 1 {
		t.Fatalf("stats = %+v, want skipped=285 written=1", stats)
	}
	if got := cursorSec(t, rdb); got != base+86100-300 {
		t.Fatalf("cursor_5m = %d, want %d（本轮最后一个桶 = 上界−300）", got, base+86100-300)
	}
	// Bootstrap() 单独调用时**不得覆写**已推进的水位（覆写会让最近 24h 被重放）。
	if err := svc.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if got := cursorSec(t, rdb); got != base+86100-300 {
		t.Fatalf("Bootstrap 覆写了已有水位：cursor_5m = %d, want %d", got, base+86100-300)
	}
}
