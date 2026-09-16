package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agentmetrics"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
)

// 本文件钉住 Plan 2G Task 2 的**真缺陷出口**：flush 每轮的运行期窗口错配检查。
//
// 缺陷形态（与 Task 1 的启动期校验刻意区分）：`MaxPoints` 在启动时按当轮的
// `sys.agent.reportInterval` 冻结（条数上限），而 `reportInterval` **可热更**。运维把间隔从
// 10s 调到 5s 之后，热层实际只剩 `RawWindowSpan(MaxPoints, 5s) ≈ 14.4h` 的点，而服务侧仍按
// 24h 回填/回退 —— 那一段**必然**读不到点，表现为空桶（BucketsSkipped），**既不报错、
// 也没有任何读数指向它**。
//
// 为什么必须是运行期检查：启动期的两个值（Step 与 MaxPoints）同源于**同一次启动的同一个
// 配置值**，且容量自带 1.2 倍余量 → 跨度结构性恒为 1.2×24h > 24h，wireup 那条 Warn 在今天
// **不可达**（Task 1 的执行者已用探针证明）。真实错配只发生在「进程先起、之后热更配置」。
//
// 观测方式：**真** `agentmetrics.RawStore`（容量由夹具显式给出，就是生产里那个被冻结的数）
// + **真** zap core（observer）。用真 RawStore 而不是替身：这里要断的正是「容量从哪来」——
// 替身若能自己返回一个数，夹具就可能在「拿当前配置推导容量」的错路上绿掉，而那是本检查
// 唯一的失效模式（两边都取冻结值 → 比对恒自洽 → 永远不响）。消息文本与结构化字段都要断，
// 一个只记「调用过 Warn」的替身证明不了内容。
//
// 反向验证（**可编译**）：把 FlushOnce 轮初那次 `warnOnRawWindowShortfall()` 调用删掉
// （或把 span 的比较写成恒假）→ TestFlushWarnsOnRuntime... 与 TestFlushWarnsOncePerRound...
// 变红；还原后复绿。两者都不动任何生产语义。

// ── 夹具 ──────────────────────────────────────────────

// rawWindowLogSpy 是捕获到的日志 + 让断言能逐字段取值的句柄。
type rawWindowLogSpy struct {
	logs *observer.ObservedLogs
}

// runtimeShortfallWarns 筛出「运行期窗口跨度短于回填假设」那一条 Warn。
//
// 用**稳定前缀**（rawWindowRuntimeShortfallMarker）而不是整句匹配：文案可以调整，
// 前缀是契约（运维也按它 grep/告警），且它必须与 wireup 的启动期前缀区分开 ——
// 混在一个 grep 里会让人以为「启动期那条响了」，而它在今天不可能响。
func runtimeShortfallWarns(spy *rawWindowLogSpy) []observer.LoggedEntry {
	var out []observer.LoggedEntry
	for _, e := range spy.logs.All() {
		if strings.Contains(e.Message, rawWindowRuntimeShortfallMarker) {
			out = append(out, e)
		}
	}
	return out
}

// newRawWindowSpy 起一个只观测 Warn 的 logger（与 wireup_test 的 withObservedWarn 同款：
// 级别阈值只是「收集什么」，不改变生产代码的任何行为）。
func newRawWindowSpy(t *testing.T) (*logger.Logger, *rawWindowLogSpy) {
	t.Helper()
	core, logs := observer.New(zapcore.WarnLevel)
	lg, err := logger.NewWithCore(core)
	if err != nil {
		t.Fatalf("logger.NewWithCore: %v", err)
	}
	return lg, &rawWindowLogSpy{logs: logs}
}

// spanFixture 是窗口比对的夹具句柄。
type spanFixture struct {
	svc *AgentMetricsFlushService
	raw *agentmetrics.RawStore
	rdb goredis.UniversalClient
	spy *rawWindowLogSpy
	// now 是本轮的「现在」（= flushBaseTS + 24h，起点恰好落在 flushBaseTS，区间可直接算）。
	now time.Time
}

// newSpanFixture 起夹具：**冻结的容量与当前配置的间隔是两个入参** —— 这正是缺陷的两个独立
// 来源，夹具必须能让它们不一致（否则所有断言都只能构造「一致」那一种状态）。
//
//	frozenStep/frozenMaxPoints —— 装配时（启动）冻结下来、交给 RawStore 的那一对；
//	currentReportSec         —— **当前**配置里的 reportInterval（可热更后的值）。
func newSpanFixture(t *testing.T, frozenStep time.Duration, frozenMaxPoints int64,
	currentReportSec int) *spanFixture {
	t.Helper()

	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	raw := agentmetrics.NewRawStore(rdb, agentmetrics.RawOptions{
		Step: frozenStep, MaxPoints: frozenMaxPoints,
	})
	lg, spy := newRawWindowSpy(t)
	now := time.Unix(flushBaseTS+flushBootstrapHours*3600, 0)

	svc := NewAgentMetricsFlushService(raw, &fakeBucketWriter{}, noopResourceRepo{},
		fakeConfig{reportSec: currentReportSec}, lg).
		WithCursorStore(rdb).
		WithClock(func() time.Time { return now })
	return &spanFixture{svc: svc, raw: raw, rdb: rdb, spy: spy, now: now}
}

// seedDevice 给某台设备压一条位于「本轮第一个桶」的原始点（生产写路径），并返回它 ——
// Append 同时会把设备登记进活跃集合，故它也是 Index 的来源。
func (f *spanFixture) seedDevice(t *testing.T, deviceID uint64, bucketSec int64) {
	t.Helper()
	s := flushSample((flushBaseTS+bucketSec)*1000, 20, "/", 20, 60)
	if err := f.raw.Append(context.Background(), deviceID, &s); err != nil {
		t.Fatalf("seed raw append device=%d: %v", deviceID, err)
	}
}

// ── 断言 1：真实错配被抓住（核心）────────────────────

// TestFlushWarnsOnRuntimeFrozenCapacityVsHotStepMismatch 复现运维的真实动作：
// 进程按 10s 启动（容量冻结为 RawMaxPoints(10s) = 10368），随后把
// `sys.agent.reportInterval` 热更为 5s —— 热层实际只剩 10368×5s = 14h24m 的点，
// 服务侧却仍按 24h 回填/回退。
//
// 必须**恰好一条** Warn，且：
//   - 消息里同时有两个**实际跨度**（14h24m0s 与 24h0m0s，不是「期望值」或占位符）；
//   - 消息里有当前配置的间隔（5s）与冻结时用的容量（10368）—— 运维据此才知道改的是哪个数；
//   - 消息里写出后果与唯一的补救（这段窗口的原始点读不到 / 空桶 / HoursSkipped → 需重启）；
//   - 结构化字段里有同一对数（供日志检索/面板按字段取值）。
//
// 期望跨度用**字面量乘法**算（不复用 RawWindowSpan）：否则反向验证（把 span 的比较写坏）
// 会先撞死在夹具自检上，而不是撞在本断言的 Warn 上 —— 那就分不清「实现错」与「夹具错」。
func TestFlushWarnsOnRuntimeFrozenCapacityVsHotStepMismatch(t *testing.T) {
	const frozenStep = 10 * time.Second
	frozenMaxPoints := agentmetrics.RawMaxPoints(frozenStep) // 10368
	const hotSec = 5

	f := newSpanFixture(t, frozenStep, frozenMaxPoints, hotSec)
	f.seedDevice(t, flushDevID, 300)

	wantSpan := time.Duration(frozenMaxPoints) * time.Duration(hotSec) * time.Second
	if wantSpan >= agentmetrics.RawBootstrapWindow {
		t.Fatalf("夹具无效：冻结容量(%d)×热更后的 %ds = %s 不小于 %s，构造不出「窗口不足」状态",
			frozenMaxPoints, hotSec, wantSpan, agentmetrics.RawBootstrapWindow)
	}

	if _, err := f.svc.FlushOnce(context.Background()); err != nil {
		t.Fatalf("FlushOnce: %v（该 Warn 不得把本轮变成失败）", err)
	}

	warns := runtimeShortfallWarns(f.spy)
	if len(warns) != 1 {
		t.Fatalf("运行期窗口错配必须**恰好一条** Warn，实际 %d 条（全部 Warn：%v）",
			len(warns), f.spy.logs.All())
	}
	msg := warns[0].Message
	// 两个实际跨度 + 当前间隔 + 冻结容量 + 后果 + 补救，逐项都要在消息正文里。
	for _, want := range []string{
		wantSpan.String(),                        // 14h24m0s —— 热层**实际**覆盖多久
		agentmetrics.RawBootstrapWindow.String(), // 24h0m0s —— 服务侧**假设**多久
		"5s",                                     // 当前配置的间隔（热更后的值）
		"10368",                                  // 冻结时用的容量
		"空桶", "HoursSkipped", "读不到", "重启",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("Warn 消息里必须写出 %q，实际：%s", want, msg)
		}
	}
	// 结构化字段：与消息文本是同一对数（缺字段 = 采集端取不到）。
	fields := warns[0].ContextMap()
	for key, want := range map[string]any{
		"rawWindowSpan":   wantSpan.String(),
		"bootstrapWindow": agentmetrics.RawBootstrapWindow.String(),
		"reportInterval":  (time.Duration(hotSec) * time.Second).String(),
		"maxPoints":       frozenMaxPoints,
	} {
		if got := fields[key]; got != want {
			t.Fatalf("Warn 字段 %s = %v, want %v（消息与字段必须对得上）", key, got, want)
		}
	}
}

// ── 断言 2：不错报 ──────────────────────────────────

// TestFlushDoesNotWarnWhenFrozenCapacityMatchesCurrentInterval：配置与冻结容量**一致**时
// （正常部署：启动之后没人动过 reportInterval）→ 0 条该 Warn。
//
// 否则正常部署每一轮（5 分钟一次、500 台设备）都在噪声里泡着，真正的那条会被淹掉。
// 「没打 Warn」不能是空话：同时断言这一对的跨度**确实** ≥ 回填假设（即夹具真的处在
// 「自洽」那一侧，而不是碰巧因为容量为 0、跨度为 0 之类的退化状态而没报警）。
func TestFlushDoesNotWarnWhenFrozenCapacityMatchesCurrentInterval(t *testing.T) {
	const step = 10 * time.Second
	maxPoints := agentmetrics.RawMaxPoints(step)

	f := newSpanFixture(t, step, maxPoints, int(step/time.Second))
	f.seedDevice(t, flushDevID, 300)

	if span := agentmetrics.RawWindowSpan(maxPoints, step); span < agentmetrics.RawBootstrapWindow {
		t.Fatalf("夹具无效：自洽配置的跨度 %s < %s（那这一轮的 0 条 Warn 什么也证明不了）",
			span, agentmetrics.RawBootstrapWindow)
	}
	if _, err := f.svc.FlushOnce(context.Background()); err != nil {
		t.Fatalf("FlushOnce: %v", err)
	}
	if warns := runtimeShortfallWarns(f.spy); len(warns) != 0 {
		t.Fatalf("配置与冻结容量一致时不得打这条 Warn，却打了 %d 条：%v", len(warns), warns)
	}
}

// ── 断言 3：每轮只打一次（不是每设备一次）────────────

// TestFlushWarnsOncePerRoundNotPerDevice：≥3 台设备时仍然**只有一条**。
//
// 这个错配是**集群级**的（容量与间隔都不带设备维度），逐设备各打一条等于 500 台刷 500 条
// 同文日志 —— 那正是本任务要避免的失效模式（真信号被自己淹掉）。故检查落在 FlushOnce 的
// 轮初、**不在**逐设备路径（flushDevice）。
//
// 单靠「一条」还不够：必须确认这一轮**真的处理了多台设备**，否则「只有一条」可能只是因为
// 只枚举到一台（断言在空设备集上永真）。故同时断言 DevicesScanned == 3。
func TestFlushWarnsOncePerRoundNotPerDevice(t *testing.T) {
	const frozenStep = 10 * time.Second
	frozenMaxPoints := agentmetrics.RawMaxPoints(frozenStep)

	f := newSpanFixture(t, frozenStep, frozenMaxPoints, 5)
	devices := []uint64{flushDevID, flushDevID + 1, flushDevID + 2}
	for _, id := range devices {
		f.seedDevice(t, id, 300)
	}

	stats, err := f.svc.FlushOnce(context.Background())
	if err != nil {
		t.Fatalf("FlushOnce: %v", err)
	}
	if stats.DevicesScanned != len(devices) {
		t.Fatalf("本轮枚举到 %d 台设备，want %d（枚举不到设备时「只有一条 Warn」是空话）",
			stats.DevicesScanned, len(devices))
	}
	if warns := runtimeShortfallWarns(f.spy); len(warns) != 1 {
		t.Fatalf("%d 台设备时该 Warn 必须只有一条（每轮一次，不是每设备一次），实际 %d 条",
			len(devices), len(warns))
	}
}

// ── 断言 4：不阻断本轮 ──────────────────────────────

// TestFlushNotBlockedByRawWindowShortfall：该 Warn 存在时 flush 仍照常完成 —— 返回 nil、
// 桶照常落库、水位照常前移。
//
// 为什么「不阻断」是硬要求：热层窗口短只是**观测范围**变小，热层本身仍然可用；而 5m 是
// **唯一真值来源**，把它拦下来只会让冷层一起停摆（且唯一的补救是重启，拦不拦都不改变
// 那一点）。故这条检查没有返回值、也不返回 error，它只出声。
//
// 水位断言用与 TestFlushBootstrapStartClampsAtEpoch 同一套算式：now = flushBaseTS + 24h
// 且起点 = flushBaseTS，故上界 = alignDown(now−CloseGrace, 300) = flushBaseTS+86100，
// 最后一个桶 = 上界 − 300。
func TestFlushNotBlockedByRawWindowShortfall(t *testing.T) {
	const frozenStep = 10 * time.Second
	frozenMaxPoints := agentmetrics.RawMaxPoints(frozenStep)

	f := newSpanFixture(t, frozenStep, frozenMaxPoints, 5)
	f.seedDevice(t, flushDevID, 300)

	stats, err := f.svc.FlushOnce(context.Background())
	if err != nil {
		t.Fatalf("FlushOnce: %v（窗口不足只是观测范围变小，不得让本轮失败）", err)
	}
	if len(runtimeShortfallWarns(f.spy)) != 1 {
		t.Fatalf("夹具前置不成立：这一轮本该打出一条窗口错配 Warn（实际 %d 条）",
			len(runtimeShortfallWarns(f.spy)))
	}
	if stats.BucketsWritten != 1 {
		t.Fatalf("BucketsWritten = %d, want 1（有原始点的那个桶必须照常落库）", stats.BucketsWritten)
	}
	if stats.Errors != 0 {
		t.Fatalf("Errors = %d, want 0", stats.Errors)
	}
	if got := cursorSec(t, f.rdb); got != flushBaseTS+86100-300 {
		t.Fatalf("cursor_5m = %d, want %d（水位必须照常推进到本轮最后一个桶）",
			got, int64(flushBaseTS+86100-300))
	}
}
