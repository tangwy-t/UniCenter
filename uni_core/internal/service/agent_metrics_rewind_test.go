package service

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agentmetrics"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
)

// 本文件覆盖 `RewindHours`（按档位回退水位，不重放）。它是 Plan 2E 的收尾：
// 在它之前，`CursorStore.Rewind` 的唯一生产调用点是 BackfillOnce（只回退 cursor_5m），
// 于是 **`cursor_1h` 没有任何回退机制**，而 rollup 的按需重扫窗口默认只有 24h ——
// 形成于 24h 之前的 1h 空洞（有 5m 行、却没有 1h 行）只能靠运维手工改 Redis 键。
//
// 关键依据（写在最前面，因为下面每条断言都靠它）：1h 的重算**不需要原始点** ——
// rollupHour 读的是**库里的 5m 行**（ReadWideRows），而 5m 行保留 30 天
// （`sys.agent.historyRetentionDays`），原始点只保留 24h。故 1h 空洞的自愈范围由
// **5m 保留期**决定（30 天），而不是由重扫窗口（24h）或 raw 保留期决定。

// ── 夹具：rollup 夹具 + flush 服务（回退入口）装在同一份依赖上 ──

// rewindFixture 把「5m/1h 两张真表 + miniredis + rollup 服务」与 flush 服务
// （RewindHours 的宿主）装在同一份 db/rdb 上：本文件的核心断言是
// 「回退 cursor_1h → **下一轮** RollupOnce 补出 1h 行」，两个服务必须是同一份依赖，
// 否则断言测的就不是生产里那条链路。
type rewindFixture struct {
	*rollupFixture
	cfg   rewindCfg
	flush *AgentMetricsFlushService
}

// rewindCfg 是**按配置键回值**的配置替身。
//
// 为什么不复用 flush 夹具的 fakeConfig：它对任何键都回 reportSec（10），于是
// `sys.agent.historyRetentionDays` 会被读成 10 天 —— 而本文件的断言正是**保留期边界**
// （5m 行留 30 天 → 1h 回退窗口最多 30 天），上界必须由测试显式给定（同 rollupCfg 的理由）。
type rewindCfg struct {
	reportSec int
	// retentionDays = 0 表示**缺键**（回落调用方给的默认值 30 天），
	// 用来钉住「配置面板里没加这条时上界是 30 天」。
	retentionDays int
}

func (c rewindCfg) GetString(_ context.Context, _ string, def string) string { return def }

func (c rewindCfg) GetInt(_ context.Context, key string, def int) int {
	switch key {
	case configAgentHistoryRetentionDays:
		if c.retentionDays == 0 {
			return def
		}
		return c.retentionDays
	case configAgentReportInterval:
		if c.reportSec == 0 {
			return def
		}
		return c.reportSec
	default:
		return def
	}
}

// newRewindFixture 在 rollup 夹具之上装一份 flush 服务；retentionDays=0 表示 5m 保留期
// 走缺省（30 天）。
//
// 重扫窗口显式钉成**生产缺省 24h**：rollup 夹具的 fakeConfig 对任何键都回 reportSec（10），
// 会把 `sys.agent.rollupRescanHours` 读成 10h —— 那是一个夹具瑕疵（既有测试各自按需覆盖），
// 而本文件要断言的是「>24h 的空洞够不着重扫窗口」，窗口必须是那个真实缺省值。
func newRewindFixture(t *testing.T, at int64, retentionDays int) *rewindFixture {
	t.Helper()
	rf := newRollupFixture(t, at, false, 0).withRescanHours(defaultRollupRescanHours)

	// 活跃设备索引（= raw.Index 的读出口）。这里直接 SAdd 而不压原始点：
	// 1h 的回退只读这个索引，**不读原始窗**（这正是本能力成立的依据）。
	if err := rf.rdb.SAdd(context.Background(), agentmetrics.DeviceIndexKey, rollupDevID).Err(); err != nil {
		t.Fatalf("SAdd 活跃索引: %v", err)
	}
	raw := agentmetrics.NewRawStore(rf.rdb, agentmetrics.RawOptions{
		Step: time.Duration(rollupReportSec) * time.Second, MaxPoints: 1000, QueryTTL: time.Second,
	})
	cfg := rewindCfg{reportSec: rollupReportSec, retentionDays: retentionDays}
	return &rewindFixture{
		rollupFixture: rf,
		cfg:           cfg,
		flush: NewAgentMetricsFlushService(raw, rf.repo,
			repository.NewDeviceResourceRepository(rf.db), cfg, logger.NewNop()).
			WithCursorStore(rf.rdb).
			WithClock(rf.clock.now),
	}
}

func (f *rewindFixture) ctx() context.Context { return context.Background() }

// mustCursor1h 断言 cursor_1h 此刻的**精确值**（对齐后的秒数可逐字写死）。
func (f *rewindFixture) mustCursor1h(t *testing.T, want int64) {
	t.Helper()
	got, ok := f.cursor(t)
	if !ok {
		t.Fatalf("cursor_1h 键不存在, want %d", want)
	}
	if got != want {
		t.Fatalf("cursor_1h = %d, want %d", got, want)
	}
}

// cursor5m 读 cursor_5m（键不存在时返回 (0,false)）；键名走 flush 测试的契约字面量。
func (f *rewindFixture) cursor5m(t *testing.T) (int64, bool) {
	t.Helper()
	v, err := f.rdb.Get(context.Background(), flushCursorKey(rollupDevID)).Int64()
	if errors.Is(err, goredis.Nil) {
		return 0, false
	}
	if err != nil {
		t.Fatalf("读 cursor_5m: %v", err)
	}
	return v, true
}

// target1h 是测试侧独立算出的 1h 回退目标（不复用实现的公式，否则端点语义写错也自洽）：
// 「游标 = 窗口起点 − 1 小时」，于是 rollup 的普通区间 `[cursor+3600, …)` 恰好从窗口起点开始。
func target1h(now int64, hours int) int64 {
	return alignDownHour(now) - int64(hours)*rollupHourSec - rollupHourSec
}

// ── 核心断言：>24h（重扫窗口之外）但仍在 5m 保留期内的 1h 空洞 ──

// TestRewind1hHealsHoleOlderThanRescanWindow 是本次收尾的**核心断言**：
//
//	① 造一个 28 小时之前的 1h 空洞（5m 行在库里，1h 行没有）；
//	② 不回退 → 补不出来（重扫窗口只有 24h，普通区间的下界在空洞之后）；
//	③ RewindHours(…, Resolution1h, 30) 把 cursor_1h 退到 now−31h；
//	④ 下一轮 RollupOnce **把那个 1h 行补出来**，且值正确（按 samples 加权，不是简单平均）。
//
// 这条在引入 RewindHours 之前是**红**的：那时 cursor_1h 没有任何回退机制。
func TestRewind1hHealsHoleOlderThanRescanWindow(t *testing.T) {
	// now 落在第 30 个小时的 +60s 处（> closeGrace 20s）：upper = 第 30 个小时起点，
	// 于是「已闭小时」恰是 [base, base+30h)。
	now := rollupBaseTS + 30*rollupHourSec + 60
	// 空洞：第 2 个小时（距 now 28 小时 > 重扫窗口 24h；仍在 5m 保留期 30 天以内）。
	hole := rollupBaseTS + 2*rollupHourSec
	f := newRewindFixture(t, now, 0)
	ctx := f.ctx()

	// 空洞小时的 12 行 5m：samples 恒为 10、cpu 依次 1..12。
	// 加权均值 = Σ(10×(i+1)) / 120 = 6.5（未加权均值也是 6.5 —— 两者相等，故这里不用它
	// 区分「加权 vs 未加权」，只用它证明「算的是这个小时的真值」，见下面的 samples 断言）。
	f.seed5m(t, fullHourRows(hole, func(i int) fiveMin {
		return fiveMin{samples: 10, cpu: float64(i + 1), uptime: int64(1000 + i)}
	})...)
	// cursor_1h 已经在最新（生产里的常态：rollup 每轮都在推进它）。
	f.setCursor(t, alignDownHour(now))

	// ② 不回退：重扫窗口 [now−24h, now) 够不着 28 小时前的空洞，普通区间也在它后面。
	before, err := f.svc.RollupOnce(ctx)
	if err != nil {
		t.Fatalf("前置 RollupOnce: %v", err)
	}
	if before.HoursWritten != 0 || before.HoursBackfilled != 0 {
		t.Fatalf("前置 stats = %+v, want 0 written / 0 backfilled（此时还没有回退）", before)
	}
	if row := f.hourRow(t, hole); row != nil {
		t.Fatalf("未回退时空洞小时却已经有 1h 行（bucket_ts=%d）—— 前置场景不成立", hole)
	}

	// ③ 回退 cursor_1h：30 小时的窗口足以覆盖 28 小时前的那个空洞。
	stats, err := f.flush.RewindHours(ctx, nil, agentmetrics.Resolution1h, 30)
	if err != nil {
		t.Fatalf("RewindHours: %v", err)
	}
	if stats.Resolution != agentmetrics.Resolution1h || stats.WindowHours != 30 || stats.DevicesScanned != 1 {
		t.Fatalf("stats = %+v, want resolution=1h / windowHours=30 / devicesScanned=1", stats)
	}
	// 回退后的水位先读下来、留到**最后**再断言（rollup 一轮会把它推走，所以只能现在读）。
	//
	// 为什么把机制读数排到最后：本条的标题断言是「空洞被补出来了」，反向验证时红灯必须
	// 落在它上面 —— 若先断言 cursorsRewound == 1，反向去掉 1h 回退之后红灯会停在
	// 「没回退」这句上，看的人反而看不到「那个小时仍然没有 1h 行」这个真正的事实。
	rewound, rewoundOK := f.cursor(t)

	// ④ 下一轮 rollup：普通区间 [cursor+3600, upper) 现在覆盖那个空洞。
	after, err := f.svc.RollupOnce(ctx)
	if err != nil {
		t.Fatalf("RollupOnce（回退后）: %v", err)
	}

	row := f.hourRow(t, hole)
	if row == nil {
		t.Fatalf("回退 cursor_1h 后，下一轮 rollup 仍未补出 1h 行（bucket_ts=%d）—— "+
			"重扫窗口（%dh）够不着它，而 cursor_1h 没有被回退就等于这条空洞永远只能手工改 Redis 键",
			hole, defaultRollupRescanHours)
	}
	if row.Samples != 120 {
		t.Fatalf("补出的 1h 行 samples = %d, want 120（= Σ12 行 × 10）", row.Samples)
	}
	if row.CPUUsedPercent == nil || math.Abs(*row.CPUUsedPercent-6.5) > 1e-9 {
		t.Fatalf("补出的 1h 行 cpu_used_percent = %v, want 6.5（按 samples 加权的真值）", row.CPUUsedPercent)
	}
	if row.UptimeSec == nil || *row.UptimeSec != 1011 {
		t.Fatalf("补出的 1h 行 uptime_sec = %v, want 1011（单调量取 LAST）", row.UptimeSec)
	}

	// ⑤ 机制读数（放在最后，见上面那条注释）。
	if stats.CursorsRewound != 1 {
		t.Fatalf("cursorsRewound = %d, want 1（stats=%+v）", stats.CursorsRewound, stats)
	}
	if !rewoundOK || rewound != target1h(now, 30) {
		t.Fatalf("回退后的 cursor_1h = %d(ok=%v), want %d", rewound, rewoundOK, target1h(now, 30))
	}
	// 补出的小时走的是**普通区间**（不是重扫）：HoursWritten 记它，HoursBackfilled 仍是 0。
	// 这条区分很重要 —— 它证明补出空洞靠的是水位回退，而不是重扫窗口被放大。
	if after.HoursWritten != 1 || after.HoursBackfilled != 0 {
		t.Fatalf("回退后 stats = %+v, want 1 written（普通区间）/ 0 backfilled（重扫没参与）", after)
	}
}

// TestRewind1hWindowBoundaryIsInclusive 钉住 1h 回退的**端点语义**：
// 「回退 N 小时」意味着被 rollup 处理的数据范围**恰好**是 `[now−N, now)` —— 一小时不多不少。
//
// 为什么需要一条专门的断言：游标的语义是「已成功回滚到（**含**）的小时」，而普通区间的
// 下界是 `cursor + 3600`。若把窗口起点直接写成水位，**起点那个小时本身会被排除**
// （它的 1h 行永远不会被写出来）；若多减一小时，范围就变成 N+1 小时、超出运维请求的窗口。
// 这里用「正好落在窗口起点上（now−26h）的空洞」与「窗口前一个小时（now−27h）的空洞」
// 同时排除这两种错法。
//
// 窗口取 26h（> 24h 的重扫窗口）不是随意的：若窗口 ≤ 24h，「窗口之外」的那个小时会落在
// rollup 的**按需重扫**窗口里（默认 24h），于是它被写出来是重扫的功劳、而不是回退越界 ——
// 断言就测不出「回退比请求的窗口更深」这个错误。（这条约束本身也是本入口存在意义的旁证：
// 24h 以外的空洞没有任何其它路径能补。）
func TestRewind1hWindowBoundaryIsInclusive(t *testing.T) {
	now := rollupBaseTS + 30*rollupHourSec + 60
	boundary := rollupBaseTS + 4*rollupHourSec // = now−26h：**在**窗口内（起点那一小时）
	outside := rollupBaseTS + 3*rollupHourSec  // = now−27h：**在**窗口之外，不许被回退覆盖
	f := newRewindFixture(t, now, 0)
	ctx := f.ctx()

	f.seed5m(t, fullHourRows(boundary, func(int) fiveMin {
		return fiveMin{samples: 10, cpu: 4}
	})...)
	f.seed5m(t, fullHourRows(outside, func(int) fiveMin {
		return fiveMin{samples: 10, cpu: 5}
	})...)
	f.setCursor(t, alignDownHour(now))

	if _, err := f.flush.RewindHours(ctx, nil, agentmetrics.Resolution1h, 26); err != nil {
		t.Fatalf("RewindHours: %v", err)
	}
	// 水位 = 起点 − 1 小时（起点成为第一个被处理的小时）。
	f.mustCursor1h(t, target1h(now, 26))

	if _, err := f.svc.RollupOnce(ctx); err != nil {
		t.Fatalf("RollupOnce: %v", err)
	}
	if row := f.hourRow(t, boundary); row == nil {
		t.Fatalf("窗口起点（now−26h, bucket_ts=%d）没有被补出来 —— 水位的端点语义写错了"+
			"（把起点直接当水位会让它被 cursor+3600 排除）", boundary)
	}
	if row := f.hourRow(t, outside); row != nil {
		t.Fatalf("窗口之外的小时（now−27h, bucket_ts=%d）也被补了出来 —— 回退比请求的窗口更深", outside)
	}
}

// ── 边界夹取 ────────────────────────────────────────────

// TestRewindHoursClampsWindowPerResolution 逐条钉住夹取规则：
//   - 1h 的上界 = **5m 行的保留期**（`sys.agent.historyRetentionDays`，缺键 → 30 天 = 720h）：
//     退到没有 5m 行的地方毫无意义（rollup 只会白扫空小时）；
//   - 5m 的上界 = raw 点的保留期（24h），与 BackfillOnce **逐字同界**；
//   - `hours <= 0` → 用满该档位的保留期（「能补多少补多少」）。
func TestRewindHoursClampsWindowPerResolution(t *testing.T) {
	now := rollupBaseTS + 30*rollupHourSec + 60

	cases := []struct {
		name          string
		retentionDays int
		res           agentmetrics.Resolution
		hours         int
		wantHours     int
		wantCursor    int64
	}{
		{"1h 超过 5m 保留期 → 夹到 30 天", 0, agentmetrics.Resolution1h, 10_000, 720,
			target1h(now, 720)},
		{"1h hours=0 → 用满保留期", 0, agentmetrics.Resolution1h, 0, 720, target1h(now, 720)},
		{"1h hours<0 → 用满保留期", 0, agentmetrics.Resolution1h, -5, 720, target1h(now, 720)},
		{"1h 正好等于保留期 → 原样", 0, agentmetrics.Resolution1h, 720, 720, target1h(now, 720)},
		{"1h 保留期被调到 7 天 → 上界跟着缩", 7, agentmetrics.Resolution1h, 10_000, 168,
			target1h(now, 168)},
		{"5m 上界是 raw 保留期 24h（与 BackfillOnce 同界）", 0, agentmetrics.Resolution5m, 10_000, 24,
			backfillTarget(now, 24)},
		{"5m hours=0 → 24h", 0, agentmetrics.Resolution5m, 0, 24, backfillTarget(now, 24)},
		// 保留期被调短时，5m 档**不受影响**：它的依据是 Redis 原始点的保留期，
		// 与 5m 行在库里的保留期不是同一个事实。
		{"5m 不受 5m 行保留期配置影响", 7, agentmetrics.Resolution5m, 0, 24, backfillTarget(now, 24)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRewindFixture(t, now, tc.retentionDays)
			// 水位放在「最新」，任何目标都必须能回退到（否则断言测的是「不回退」）。
			f.setCursor(t, alignDownHour(now))
			if err := f.rdb.Set(context.Background(), flushCursorKey(rollupDevID),
				alignDownHour(now), 0).Err(); err != nil {
				t.Fatalf("set cursor_5m: %v", err)
			}

			stats, err := f.flush.RewindHours(f.ctx(), nil, tc.res, tc.hours)
			if err != nil {
				t.Fatalf("RewindHours: %v", err)
			}
			if stats.WindowHours != tc.wantHours {
				t.Fatalf("windowHours = %d, want %d（stats=%+v）", stats.WindowHours, tc.wantHours, stats)
			}
			if stats.Resolution != tc.res {
				t.Fatalf("stats.Resolution = %s, want %s", stats.Resolution, tc.res)
			}
			if stats.CursorsRewound != 1 {
				t.Fatalf("cursorsRewound = %d, want 1（stats=%+v）", stats.CursorsRewound, stats)
			}
			if tc.res == agentmetrics.Resolution1h {
				f.mustCursor1h(t, tc.wantCursor)
			} else {
				got, ok := f.cursor5m(t)
				if !ok || got != tc.wantCursor {
					t.Fatalf("cursor_5m = %d(ok=%v), want %d", got, ok, tc.wantCursor)
				}
			}
			// 只碰**被点名的档位**：另一族水位键必须原封不动（回退档位串了会静默丢数据）。
			if tc.res == agentmetrics.Resolution1h {
				got, ok := f.cursor5m(t)
				if !ok || got != alignDownHour(now) {
					t.Fatalf("cursor_5m = %d(ok=%v), want %d（1h 回退不得碰到 5m 水位）",
						got, ok, alignDownHour(now))
				}
			} else {
				f.mustCursor1h(t, alignDownHour(now))
			}
		})
	}

	// 3 小时那个窗口的夹取不该被上界抹平（确保上面的断言不是因为「一律夹到上界」而通过）。
	f := newRewindFixture(t, now, 0)
	f.setCursor(t, alignDownHour(now))
	stats, err := f.flush.RewindHours(f.ctx(), nil, agentmetrics.Resolution1h, 3)
	if err != nil {
		t.Fatalf("RewindHours: %v", err)
	}
	if stats.WindowHours != 3 {
		t.Fatalf("windowHours = %d, want 3（窗口小于上界时必须原样）", stats.WindowHours)
	}
	f.mustCursor1h(t, target1h(now, 3))
}

// ── 只后退 ──────────────────────────────────────────────

// TestRewindHoursNeverAdvancesCursor 钉住「只后退」：水位已经比目标更旧（或正好等于目标）时
// **一个字节都不写**，绝不把水位前移 —— 前移会永久跳过中间那段小时，而 1h 行的重算正是
// 靠那段小时。
//
// 同时钉住「键缺失时让位给消费方的 Bootstrap 语义」：此时**不初始化**（写下一个凭 now
// 推导的目标只可能比 rollup 的 `最早 5m 行 − 1h` 更浅或更深，两者都不会多补出一行数据）。
func TestRewindHoursNeverAdvancesCursor(t *testing.T) {
	now := rollupBaseTS + 30*rollupHourSec + 60
	target := target1h(now, 24) // 请求 24 小时

	t.Run("水位已比目标更旧", func(t *testing.T) {
		f := newRewindFixture(t, now, 0)
		older := target - 3*rollupHourSec
		f.setCursor(t, older)
		stats, err := f.flush.RewindHours(f.ctx(), nil, agentmetrics.Resolution1h, 24)
		if err != nil {
			t.Fatalf("RewindHours: %v", err)
		}
		if stats.CursorsRewound != 0 {
			t.Fatalf("cursorsRewound = %d, want 0（水位已更旧，无可回退）", stats.CursorsRewound)
		}
		f.mustCursor1h(t, older)
	})

	t.Run("水位正好等于目标", func(t *testing.T) {
		f := newRewindFixture(t, now, 0)
		f.setCursor(t, target)
		stats, err := f.flush.RewindHours(f.ctx(), nil, agentmetrics.Resolution1h, 24)
		if err != nil {
			t.Fatalf("RewindHours: %v", err)
		}
		if stats.CursorsRewound != 0 {
			t.Fatalf("cursorsRewound = %d, want 0（边界：等于目标不算回退）", stats.CursorsRewound)
		}
		f.mustCursor1h(t, target)
	})

	t.Run("水位键缺失", func(t *testing.T) {
		f := newRewindFixture(t, now, 0)
		stats, err := f.flush.RewindHours(f.ctx(), nil, agentmetrics.Resolution1h, 24)
		if err != nil {
			t.Fatalf("RewindHours: %v", err)
		}
		if stats.CursorsRewound != 0 || stats.DevicesScanned != 1 {
			t.Fatalf("stats = %+v, want 1 scanned / 0 rewound", stats)
		}
		if _, ok := f.cursor(t); ok {
			t.Fatal("水位键缺失时不得写入：初始化是消费方 Bootstrap 语义的事（rollup 会取「最早 5m 行 − 1h」）")
		}
	})

	t.Run("device_ids 之外不回退", func(t *testing.T) {
		f := newRewindFixture(t, now, 0)
		f.setCursor(t, alignDownHour(now))
		stats, err := f.flush.RewindHours(f.ctx(), []uint64{rollupDevID + 1}, agentmetrics.Resolution1h, 24)
		if err != nil {
			t.Fatalf("RewindHours: %v", err)
		}
		if stats.CursorsRewound != 0 {
			t.Fatalf("cursorsRewound = %d, want 0（该设备不在 device_ids 里）", stats.CursorsRewound)
		}
		f.mustCursor1h(t, alignDownHour(now))
	})
}

// ── 与 BackfillOnce 的关系：5m 那一档的回退是**同一步** ──

// TestRewindHours5mIsTheSameRewindStepAsBackfill 证明两个入口在 5m 档上是**同一条回退**：
// `RewindHours(…, Resolution5m, h)` 落在 `BackfillOnce(…, h)` 回退那一步的同一个值上。
//
// 为什么值得一条断言：5m 的回退公式（`now−hours` 对齐到 5min，**不**减一个桶）与 1h 的
// 公式（起点再减一小时）**刻意不同**，差别来自两个游标的端点语义。若有人「顺手统一」
// 两者，1h 的端点语义会错（起点被排除），而 5m 的回退会比 BackfillOnce 深一个桶 ——
// 前者有断言，后者由本条守住。
func TestRewindHours5mIsTheSameRewindStepAsBackfill(t *testing.T) {
	ctx := context.Background()
	want := backfillTarget(backfillAt, 6) // 测试侧独立算出的起点

	// 甲：新入口只回退（不重放）。回退后游标就停在目标上，可直接观测。
	rewind := newFlushFixture(t, backfillAt, 10, nil)
	rewind.seedFor(t, flushDevID, flushSample((flushBaseTS+300)*1000, 10, "/", 20, 40))
	rewind.setCursorFor(t, flushDevID, backfillAt)
	stats, err := rewind.svc.RewindHours(ctx, nil, agentmetrics.Resolution5m, 6)
	if err != nil {
		t.Fatalf("RewindHours: %v", err)
	}
	if stats.CursorsRewound != 1 || stats.WindowHours != 6 {
		t.Fatalf("stats = %+v, want 1 rewound / 6 hours", stats)
	}
	gotRewind, ok := rewind.cursorFor(t, flushDevID)
	if !ok || gotRewind != want {
		t.Fatalf("RewindHours(5m, 6) 的游标 = %d(ok=%v), want %d", gotRewind, ok, want)
	}

	// 乙：BackfillOnce 的回退那一步（写失败把水位冻结在回退值上 —— 观测它的唯一稳定手段，
	// 同既有测试的 frozenWriter 用法）。
	back := newFlushFixture(t, backfillAt, 10, frozenWriter())
	back.seedFor(t, flushDevID, flushSample((flushBaseTS+300)*1000, 10, "/", 20, 40))
	back.setCursorFor(t, flushDevID, backfillAt)
	if _, err := back.svc.BackfillOnce(ctx, nil, 6); !errors.Is(err, ErrFlushPartial) {
		t.Fatalf("BackfillOnce 的 err = %v, want ErrFlushPartial（替身注入的写失败）", err)
	}
	gotBackfill, ok := back.cursorFor(t, flushDevID)
	if !ok || gotBackfill != want {
		t.Fatalf("BackfillOnce(6) 回退后的游标 = %d(ok=%v), want %d", gotBackfill, ok, want)
	}

	if gotRewind != gotBackfill {
		t.Fatalf("两个入口在 5m 档上退到了不同的值：%d vs %d —— 回退公式分叉了", gotRewind, gotBackfill)
	}
}
