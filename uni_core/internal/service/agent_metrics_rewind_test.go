package service

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

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

// ── Task 1：回退「待追平」标记（问题②：回退是否生效不可观测）────────────
//
// 问题②：回退**本身**只花一次 Redis 读 + 一次写，而它的效果（那些小时被重新走过、
// 1h 空洞被补出来）全部记在 rollup 的 `HoursWritten` 里 —— 一个与「回退」这个动作无关的
// 计数（同样的数字也来自「设备刚好有新数据」）。运维执行完回退没有任何读数或告警能回答
// 「回退生效了吗、追平了吗」。故：`RewindHours` 写下标记，rollup 每轮在收尾 CAS 之后
// 看一眼，追平了就 log.Info + 删标记 + 计数。

// rewindMarkerKey 是**契约字面量**：不复用 agentmetrics.RewindMarkerKey，否则键名写错也自洽
// （同 rollupCursorKey / rollupRepairKey 的处理）。运维按 spec 键名读它，形态与游标键同族：
// `agent:device:{id}:rewind_1h`。
func rewindMarkerKey(deviceID uint64) string {
	return "agent:device:" + itoaU64(deviceID) + ":rewind_1h"
}

// rewindMarker5mKey 是 5m 档的标记键（本任务**刻意不写**它，见 markRewindPending 的注释）。
func rewindMarker5mKey(deviceID uint64) string {
	return "agent:device:" + itoaU64(deviceID) + ":rewind_5m"
}

// loggedLine 是一条日志记录（消息 + 摊平的字段）。
type loggedLine struct {
	msg    string
	fields map[string]any
}

// rewindLogSpy 记录 Info 的消息与字段：本任务的「追平可观测」断言要求 log.Info
// **带设备号与目标** —— 只数次数不足以钉住这一点，而 fakeLogger 只统计 Warn。
type rewindLogSpy struct {
	infos []loggedLine
	warns int
}

func (l *rewindLogSpy) Debug(string, ...zap.Field) {}
func (l *rewindLogSpy) Info(msg string, fields ...zap.Field) {
	l.infos = append(l.infos, newLoggedLine(msg, fields))
}
func (l *rewindLogSpy) Warn(string, ...zap.Field)  { l.warns++ }
func (l *rewindLogSpy) Error(string, ...zap.Field) {}
func (l *rewindLogSpy) IsDebug() bool              { return false }

// newLoggedLine 把 zap 字段摊成 map（只保留断言要用的标量类型，其余原样放 Interface）。
func newLoggedLine(msg string, fields []zap.Field) loggedLine {
	m := make(map[string]any, len(fields))
	for _, fl := range fields {
		switch fl.Type {
		case zapcore.Int64Type, zapcore.Uint64Type, zapcore.Int32Type, zapcore.Uint32Type:
			m[fl.Key] = fl.Integer
		case zapcore.StringType:
			m[fl.Key] = fl.String
		default:
			m[fl.Key] = fl.Interface
		}
	}
	return loggedLine{msg: msg, fields: m}
}

// hasInfo 找一条「消息含 sub，且同时带 deviceId 与 target 字段」的 Info 记录。
func (l *rewindLogSpy) hasInfo(sub string, deviceID uint64, target int64) bool {
	for _, line := range l.infos {
		if !strings.Contains(line.msg, sub) {
			continue
		}
		if got, ok := line.fields["deviceId"].(int64); !ok || uint64(got) != deviceID {
			continue
		}
		if got, ok := line.fields["target"].(int64); !ok || got != target {
			continue
		}
		return true
	}
	return false
}

// markerGetFailer 只让**标记键**的 GET 失败，其余命令原样委托（嵌入 goredis.Cmdable 拿到
// 全部方法，只覆盖一个）：用来证明「观测设施的读失败不阻断数据通路」。
type markerGetFailer struct {
	goredis.Cmdable
	key string
}

func (c markerGetFailer) Get(ctx context.Context, key string) *goredis.StringCmd {
	if key == c.key {
		return goredis.NewStringResult("", errors.New("injected marker GET failure"))
	}
	return c.Cmdable.Get(ctx, key)
}

// TestRewindHoursWritesPendingRewindMarker 钉住标记的**写入侧**（Step 1 的 5 + 7）：
//   - 1h 回退必须写下标记，值 == 回退目标（也 == 回退后的水位）；
//   - 5m 档**不写** 1h 标记（键是档位专属），也不写 5m 标记（没有消费方，见下）；
//   - 水位键缺失时让位给消费方的 Bootstrap 语义 → 没有回退，也没有标记。
func TestRewindHoursWritesPendingRewindMarker(t *testing.T) {
	now := rollupBaseTS + 30*rollupHourSec + 60
	ctx := context.Background()

	t.Run("1h 回退写下标记且值 == 回退目标", func(t *testing.T) {
		f := newRewindFixture(t, now, 0)
		f.setCursor(t, alignDownHour(now))
		stats, err := f.flush.RewindHours(ctx, nil, agentmetrics.Resolution1h, 24)
		if err != nil {
			t.Fatalf("RewindHours: %v", err)
		}
		if stats.CursorsRewound != 1 {
			t.Fatalf("cursorsRewound = %d, want 1（stats=%+v）", stats.CursorsRewound, stats)
		}
		target := target1h(now, 24)
		f.mustCursor1h(t, target)
		f.mustMarker(t, rewindMarkerKey(rollupDevID), target)
		// 键名契约（字面量已在上面的 helper 里写死，这里再钉一次形态）：
		// 档位名必须在设备号**之后**，且与游标键同前缀。
		key, kerr := agentmetrics.RewindMarkerKey(rollupDevID, agentmetrics.Resolution1h)
		if kerr != nil {
			t.Fatalf("RewindMarkerKey(1h) 报错: %v", kerr)
		}
		if key != rewindMarkerKey(rollupDevID) {
			t.Fatalf("RewindMarkerKey(1h) = %q, want %q（与契约字面量一致）", key, rewindMarkerKey(rollupDevID))
		}
		key5m, kerr5 := agentmetrics.RewindMarkerKey(rollupDevID, agentmetrics.Resolution5m)
		if kerr5 != nil {
			t.Fatalf("RewindMarkerKey(5m) 报错: %v", kerr5)
		}
		if key5m != rewindMarker5mKey(rollupDevID) {
			t.Fatalf("RewindMarkerKey(5m) = %q, want %q", key5m, rewindMarker5mKey(rollupDevID))
		}
		if key5m == key {
			t.Fatal("两个档位的标记键不得相同：同键会让 5m 的回退把 1h 的待追平状态覆盖掉")
		}
	})

	t.Run("5m 回退不写 1h 标记", func(t *testing.T) {
		f := newRewindFixture(t, now, 0)
		f.setCursor(t, alignDownHour(now))
		if err := f.rdb.Set(ctx, flushCursorKey(rollupDevID), alignDownHour(now), 0).Err(); err != nil {
			t.Fatalf("set cursor_5m: %v", err)
		}
		stats, err := f.flush.RewindHours(ctx, nil, agentmetrics.Resolution5m, 6)
		if err != nil {
			t.Fatalf("RewindHours(5m): %v", err)
		}
		if stats.CursorsRewound != 1 {
			t.Fatalf("cursorsRewound = %d, want 1（stats=%+v）", stats.CursorsRewound, stats)
		}
		if _, ok := f.marker(t, rewindMarkerKey(rollupDevID)); ok {
			t.Fatal("5m 回退写下了 1h 的待追平标记：标记的消费方是 rollup 的 cursor_1h，" +
				"5m 的回退写它会给出一个永远不会被追平、也永远不会被清掉的假状态")
		}
		// 也不写 5m 自己的标记：5m 的回退效果由 flush 在本轮重放里体现（BackfillOnce 是
		// 「回退 + 重放」不可分），没有第二个读者 —— 一个永远为真的「待追平」比没有更糟。
		if _, ok := f.marker(t, rewindMarker5mKey(rollupDevID)); ok {
			t.Fatal("5m 回退写下了 5m 标记：没有任何读取方，它只会永久留在 Redis 里")
		}
	})

	t.Run("水位键缺失时不回退也不写标记", func(t *testing.T) {
		f := newRewindFixture(t, now, 0)
		stats, err := f.flush.RewindHours(ctx, nil, agentmetrics.Resolution1h, 24)
		if err != nil {
			t.Fatalf("RewindHours: %v", err)
		}
		if stats.CursorsRewound != 0 {
			t.Fatalf("cursorsRewound = %d, want 0（水位键缺失 → 让位给消费方的 Bootstrap 语义）",
				stats.CursorsRewound)
		}
		if _, ok := f.marker(t, rewindMarkerKey(rollupDevID)); ok {
			t.Fatal("没有回退却写下了标记：标记只描述「水位被退回过」，凭 now 推导出的假标记会永远追不平")
		}
	})
}

// TestRewindMarkerOnlyMovesEarlier 钉住标记的**单调方向**（Step 1 的 7 后半）：
// 重复回退时标记取**更早**的那个目标，绝不被后来的浅回退推后。
//
// 场景（生产里很常见）：运维在 T 执行「回退 24 小时」（目标 T−25h），几小时后再执行同一条
// 命令 —— 此时 now 前移，新的目标是 (T+5h)−25h，也就是**更晚**的一个水位。若标记被它覆盖，
// 「待追平」的区间就从 T−25h 缩到 T−20h，前 5 个小时重新变成无人观测（而配额下它们很可能
// 还没被走过）。
func TestRewindMarkerOnlyMovesEarlier(t *testing.T) {
	now := rollupBaseTS + 30*rollupHourSec + 60
	f := newRewindFixture(t, now, 0)
	ctx := f.ctx()
	f.setCursor(t, alignDownHour(now))

	if _, err := f.flush.RewindHours(ctx, nil, agentmetrics.Resolution1h, 24); err != nil {
		t.Fatalf("RewindHours(1): %v", err)
	}
	deep := target1h(now, 24)
	f.mustCursor1h(t, deep)
	f.mustMarker(t, rewindMarkerKey(rollupDevID), deep)

	// 时间前进 5 小时；水位被 rollup 推到了最新（模拟这几轮里它一直在追平）。
	later := now + 5*rollupHourSec
	f.clock.t = time.Unix(later, 0)
	f.setCursor(t, alignDownHour(later))

	shallow := target1h(later, 24)
	if shallow <= deep {
		t.Fatalf("前置不成立：新目标 %d 必须比旧目标 %d 更晚（否则本条测不到「推后」）", shallow, deep)
	}
	stats, err := f.flush.RewindHours(ctx, nil, agentmetrics.Resolution1h, 24)
	if err != nil {
		t.Fatalf("RewindHours(2): %v", err)
	}
	if stats.CursorsRewound != 1 {
		t.Fatalf("cursorsRewound = %d, want 1（水位比新目标更新 → 这次回退确实生效）", stats.CursorsRewound)
	}
	// 水位按新目标走（回退本身是「最新一次请求说了算」）……
	f.mustCursor1h(t, shallow)
	// ……但标记必须留在更早的那个目标上：待追平区间只许变长，不许被浅回退截短。
	f.mustMarker(t, rewindMarkerKey(rollupDevID), deep)
}

// TestRewindMarkerCaughtUpOnlyAfterBacklogWalked 钉住标记的**追平侧**（Step 1 的 6）：
//   - 未追平时标记**必须仍在**（否则观测就失效了）—— 尤其是「配额只走了 48/720 小时」
//     的那些轮：那时水位已经越过标记值，但绝大多数小时根本没被走过；
//   - 走完整个 720 小时的窗口之后 → log.Info（带设备号与目标）+ 删标记 + RewindsCaughtUp++。
//
// 这条断言同时是两个问题的交汇点：问题①（配额把追平切成 15 轮）与问题②（回退是否生效
// 必须可观测）。若「追平」只看 `cursor >= target`，回退后的**第一轮**就会把标记删掉并
// 打出「已追平」，而那 672 个小时还要再走 14 轮 —— 一个比没有标记更糟的假信号。
func TestRewindMarkerCaughtUpOnlyAfterBacklogWalked(t *testing.T) {
	f := newRewindFixture(t, quotaWindowNow, 0)
	spy := &rewindLogSpy{}
	f.withRollupDeps(rollupCfg{rescanHours: defaultRollupRescanHours, maxHours: defaultRollupMaxHoursPerRound}, nil, spy)
	seedQuotaWindow(t, f.rollupFixture)
	target, upper := quotaRewindTarget(), quotaWindowUpper()
	ctx := f.ctx()

	// 前置：水位在最新（生产里的常态），回退 720 小时 → 水位与标记都落到目标上。
	f.setCursor(t, alignDownHour(quotaWindowNow))
	rw, err := f.flush.RewindHours(ctx, nil, agentmetrics.Resolution1h, quotaWindowHours)
	if err != nil {
		t.Fatalf("RewindHours: %v", err)
	}
	if rw.CursorsRewound != 1 || rw.WindowHours != quotaWindowHours {
		t.Fatalf("前置 stats = %+v, want 1 rewound / 720 hours", rw)
	}
	f.mustCursor1h(t, target)
	f.mustMarker(t, rewindMarkerKey(rollupDevID), target)

	rounds := quotaWindowHours / defaultRollupMaxHoursPerRound
	for round := 1; round <= rounds; round++ {
		stats := f.rollupRound(t)

		if round < rounds {
			// 未追平：标记必须仍在（值不变），且不得被计数。
			if got, ok := f.marker(t, rewindMarkerKey(rollupDevID)); !ok || got != target {
				t.Fatalf("第 %d 轮后标记 = %d(ok=%v), want %d：本轮只走了 48/%d 个小时，"+
					"标记绝不能因为「水位已越过回退目标」而被当成追平删掉",
					round, got, ok, target, quotaWindowHours)
			}
			if stats.RewindsCaughtUp != 0 {
				t.Fatalf("第 %d 轮 stats = %+v, want rewindsCaughtUp=0（追平还没发生）", round, stats)
			}
			if stats.HoursDeferred <= 0 {
				t.Fatalf("第 %d 轮 stats = %+v, want hoursDeferred>0（配额用尽）", round, stats)
			}
			continue
		}

		// 最后一轮：水位走到窗口末尾（追平），标记被清掉并计一次数。
		if cur, _ := f.cursor(t); cur != upper-rollupHourSec {
			t.Fatalf("第 %d 轮 cursor_1h = %d, want %d（追平轮的水位应落在窗口的最后一个小时）",
				round, cur, upper-rollupHourSec)
		}
		if stats.HoursDeferred != 0 {
			t.Fatalf("第 %d 轮 stats = %+v, want hoursDeferred=0（配额未用尽）", round, stats)
		}
		if stats.RewindsCaughtUp != 1 {
			t.Fatalf("第 %d 轮 stats = %+v, want rewindsCaughtUp=1（追平必须被计数）", round, stats)
		}
		if _, ok := f.marker(t, rewindMarkerKey(rollupDevID)); ok {
			t.Fatal("追平之后标记必须被删除：留着它就等于「永远待追平」，运维再也无法从标记上读到任何信息")
		}
		if !spy.hasInfo("已追平", rollupDevID, target) {
			t.Fatalf("追平时必须 log.Info 且**带设备号与目标**（deviceId=%d target=%d），实际记录 = %+v",
				rollupDevID, target, spy.infos)
		}
	}

	// 追平之后的轮次不得重复计数（标记已删 = 这件事只发生一次）。
	after := f.rollupRound(t)
	if after.RewindsCaughtUp != 0 {
		t.Fatalf("追平后的轮次 stats = %+v, want rewindsCaughtUp=0（标记已删，不得重复计数）", after)
	}
}

// TestRollupToleratesRewindMarkerReadFailure 钉住「观测设施不得成为数据通路的单点」：
// 标记读失败 → 只记 log.Warn 并继续本轮（不返回错误、不删标记、该回滚的小时照常回滚）。
func TestRollupToleratesRewindMarkerReadFailure(t *testing.T) {
	now := rollupBaseTS + 30*rollupHourSec + 60
	f := newRewindFixture(t, now, 0)
	ctx := f.ctx()
	f.setCursor(t, alignDownHour(now))
	if _, err := f.flush.RewindHours(ctx, nil, agentmetrics.Resolution1h, 6); err != nil {
		t.Fatalf("RewindHours: %v", err)
	}
	target := target1h(now, 6)
	f.mustMarker(t, rewindMarkerKey(rollupDevID), target)

	// 一个待补的小时：证明这一轮**照常干活**（数据通路不受观测故障影响）。
	late := target + rollupHourSec
	f.seed5m(t, fullHourRows(late, func(int) fiveMin {
		return fiveMin{samples: 30, cpu: 33}
	})...)

	spy := &rewindLogSpy{}
	f.withRollupDeps(rollupCfg{rescanHours: defaultRollupRescanHours, maxHours: defaultRollupMaxHoursPerRound},
		markerGetFailer{Cmdable: f.rdb, key: rewindMarkerKey(rollupDevID)}, spy)

	stats, err := f.svc.RollupOnce(ctx)
	if err != nil {
		t.Fatalf("标记读失败不得让整轮失败（观测设施不是数据通路），实际 err = %v", err)
	}
	if spy.warns < 1 {
		t.Fatal("标记读失败必须 log.Warn（静默吞掉 = 观测失效且无人知道）")
	}
	if stats.RewindsCaughtUp != 0 {
		t.Fatalf("stats = %+v, want rewindsCaughtUp=0（读不到标记就不能声称追平）", stats)
	}
	// 标记仍然在：读失败不等于「没有标记」，更不许顺手删掉它。
	if _, ok := f.marker(t, rewindMarkerKey(rollupDevID)); !ok {
		t.Fatal("标记读失败的一轮不得删除标记")
	}
	// 数据通路照常：那个小时被回滚出来（读失败没有连坐普通区间）。
	row := f.hourRow(t, late)
	if row == nil {
		t.Fatal("标记读失败连坐了普通区间：那个小时的 1h 行没有被写出来")
	}
	if row.Samples != 360 || row.CPUUsedPercent == nil || *row.CPUUsedPercent != 33 {
		t.Fatalf("补出的 1h 行 = samples %d / cpu %v, want 360 / 33", row.Samples, row.CPUUsedPercent)
	}
}
