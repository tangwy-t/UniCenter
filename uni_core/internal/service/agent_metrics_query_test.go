package service

import (
	"context"
	"errors"
	"math"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agentmetrics"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/database"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/metricshistory"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/snowflake"
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
)

func f64p(f float64) *float64 { return &f }

func i64p(v int64) *int64 { return &v }

// maxBucketsForTest 与实现里的上限**同源**（MaxBuckets），避免测试里出现第二个 4000。
const maxBucketsForTest = MaxBuckets

// assertBadRequest 断言错误是 400（apperror.BadRequest）。
func assertBadRequest(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s：必须报 400，但返回 nil", what)
	}
	var ae *apperror.AppError
	if !errors.As(err, &ae) {
		t.Fatalf("%s：必须是 apperror.AppError，got %T (%v)", what, err, err)
	}
	if ae.HTTPStatus != 400 {
		t.Fatalf("%s：HTTPStatus = %d, want 400（%v）", what, ae.HTTPStatus, err)
	}
}

func TestSelectTierBoundaries(t *testing.T) {
	cases := []struct {
		rangeSec int64
		source   string
		table    string
		res      int64
		wantErr  bool
	}{
		{3600, "redis", "", 10, false},                    // 1h → Redis 原始
		{86400, "redis", "", 10, false},                   // 24h 边界仍在 Redis
		{86401, "db", "device_metric_5m", 300, false},     // 刚过 24h → 5min 档
		{2592000, "db", "device_metric_5m", 300, false},   // 30d 边界
		{2592001, "db", "device_metric_1h", 3600, false},  // 刚过 30d → 1h 档
		{15552000, "db", "device_metric_1h", 3600, false}, // 半年
		{3599, "", "", 0, true},                           // 小于 1h → 400
		{15552001, "", "", 0, true},                       // 超过半年 → 400
		{0, "", "", 0, true},                              // 缺省 → 由 service 填默认值，此处视为越界
	}
	for _, c := range cases {
		// 第二参显式传**默认值常量**：本用例表只验「range → 档位/表/原生栅格」的
		// 边界，与 RedisIntervalPolicy 无关；传默认值使期望值（10）与旧行为逐字相同。
		got, err := SelectTier(c.rangeSec, RedisNativeResolutionSec)
		if c.wantErr {
			assertBadRequest(t, "range="+itoa(c.rangeSec), err)
			continue
		}
		if err != nil {
			t.Errorf("range=%d 不应报错: %v", c.rangeSec, err)
			continue
		}
		if got.Source != c.source || got.Table != c.table || got.Resolution != c.res {
			t.Errorf("range=%d → %+v, want source=%s table=%s res=%d", c.rangeSec, got, c.source, c.table, c.res)
		}
		// 越界之外的每一条都必须带 RangeSeconds 且 Scale >= 1（升档倍数语义）
		if got.RangeSeconds != c.rangeSec {
			t.Errorf("range=%d → RangeSeconds=%d 未回填", c.rangeSec, got.RangeSeconds)
		}
		if got.Scale < 1 {
			t.Errorf("range=%d → Scale=%d，必须 >= 1", c.rangeSec, got.Scale)
		}
		// Redis 档不得出现 DB 表名，反之亦然（选档即选表，不留混用缝）
		if c.source == "redis" && got.Table != "" {
			t.Errorf("range=%d → Redis 档不得带表名, got %q", c.rangeSec, got.Table)
		}
		if c.source == "db" && got.Table == "" {
			t.Errorf("range=%d → DB 档必须带表名", c.rangeSec)
		}
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func TestSelectTierRejectsOutOfRange(t *testing.T) {
	for _, r := range []int64{-1, 0, 1, 3599, request.DeviceRangeMin - 1, request.DeviceRangeMax + 1} {
		// 越界必须在**读 redisNativeSec 之前**就拒掉：传一个非法间隔（0）也不得改变错误口径。
		_, err := SelectTier(r, 0)
		assertBadRequest(t, "越界 range="+itoa(r), err)
	}
}

func TestSelectTierNeverMixesTiers(t *testing.T) {
	// 40 天必须整体走 1h 档（不允许前 30 天 5min + 后 10 天 1h 拼接）
	got, err := SelectTier(40*24*3600, RedisNativeResolutionSec)
	if err != nil {
		t.Fatal(err)
	}
	if got.Table != "device_metric_1h" {
		t.Fatalf("40d 必须整体走 1h 档, got %q", got.Table)
	}
	if got.Source != "db" {
		t.Fatalf("source = %q, want db", got.Source)
	}
	if got.Resolution != 3600 {
		t.Fatalf("40d 原生分辨率 = %d, want 3600（不得回落 300s 与 1h 混拼）", got.Resolution)
	}
}

func TestStepLiftForBucketCap(t *testing.T) {
	// 180d @1h = 4320 桶 > 4000 → 升 step 到 2h（Scale=2），桶数 2160
	got, err := SelectTier(15552000, RedisNativeResolutionSec)
	if err != nil {
		t.Fatal(err)
	}
	if got.Scale != 2 {
		t.Fatalf("180d@1h = 4320 桶，必须升到 Scale=2（4320<=4000<8640 时为 2）, got Scale=%d", got.Scale)
	}
	if step := got.Resolution * got.Scale; step != 7200 {
		t.Fatalf("180d 有效桶宽 = %d, want 7200（spec §7.2「半年→2h」）", step)
	}
	if n := got.BucketCount(); n > maxBucketsForTest {
		t.Fatalf("升档后桶数 %d 仍超过 %d 上限", n, maxBucketsForTest)
	}
	// 30d @300s = 8640 桶 → Scale=3 → 2880
	got30, err := SelectTier(2592000, RedisNativeResolutionSec)
	if err != nil {
		t.Fatal(err)
	}
	if got30.Scale != 3 {
		t.Fatalf("30d@300s = 8640 桶，必须升到 Scale=3, got Scale=%d", got30.Scale)
	}
	if step := got30.Resolution * got30.Scale; step != 900 {
		t.Fatalf("30d 有效桶宽 = %d, want 900（spec §7.2「30d→15min」）", step)
	}
	if n := got30.BucketCount(); n > maxBucketsForTest {
		t.Fatalf("30d 升档后桶数 %d 超过上限", n)
	}
}

func TestStepLiftKeepsNativeMultiplesWithinCap(t *testing.T) {
	// 升 step 仍是**原生桶宽的整数倍**（桶边界仍对齐）；且每个合法 range 都不越上限。
	ranges := []int64{3600, 7200, 86400, 86401, 604800, 2592000, 2592001, 7776000, 15552000}
	for _, r := range ranges {
		got, err := SelectTier(r, RedisNativeResolutionSec)
		if err != nil {
			t.Fatalf("range=%d: %v", r, err)
		}
		step := got.Resolution * got.Scale
		if step <= 0 || step%got.Resolution != 0 {
			t.Fatalf("range=%d: 有效桶宽 %d 不是原生 %d 的整数倍", r, step, got.Resolution)
		}
		if n := got.BucketCount(); n > maxBucketsForTest {
			t.Fatalf("range=%d: 升档后桶数 %d 超过 %d", r, n, maxBucketsForTest)
		}
	}
}

func TestStepLiftAppliesToRedisTier(t *testing.T) {
	// 24h @10s = 8640 桶 > 4000 → Scale=3 → 2880 桶（有效桶宽 30s）；
	// 但 Resolution 报的是**原生** 10s，前端只信 response.resolution_seconds。
	got, err := SelectTier(86400, RedisNativeResolutionSec)
	if err != nil {
		t.Fatal(err)
	}
	// H4 的两半现在分开成立：
	//  ① 「Redis 档的原生栅格是一个**具名默认值**而不是散落的魔法数 10」——
	//     由本断言保持：显式传入该常量，Resolution 必须原样回它；
	//  ② 「10 只是缺省、不是唯一取值」—— 由
	//     TestSelectTierRedisResolutionFollowsConfiguredInterval 断言（传 5/30 就必须回 5/30）。
	// 常量本身已不再被 SelectTier 直接读取，缺省由调用方（query 服务）在 policy 为 nil 时传入。
	if got.Resolution != RedisNativeResolutionSec {
		t.Fatalf("Redis 档原生分辨率 = %d, want %d（RedisNativeResolutionSec）",
			got.Resolution, RedisNativeResolutionSec)
	}
	if step := got.Resolution * got.Scale; step != 30 {
		t.Fatalf("24h Redis 档有效桶宽 = %d, want 30（8640 桶需升 k=3）", step)
	}
	if n := got.BucketCount(); n > maxBucketsForTest {
		t.Fatalf("24h 升档后桶数 %d 超过上限", n)
	}
}

// ── Task 1：Redis 档的栅格必须由 sys.agent.reportInterval 驱动 ────────────
//
// 缺陷背景：Redis 档的 Resolution 此前恒为 RedisNativeResolutionSec = 10，而查询
// 服务**不接配置**。运维把 sys.agent.reportInterval 改成 5s 或 30s 后，Redis 档的
// 栅格与升档倍数仍按 10s 推导 —— 桶变稀疏（每桶只装到实际样本的一部分）、
// 且**不报错**：一条静默的错误曲线。
//
// 下面 4 组断言共同把「配置驱动」钉住（任何一条都能单独击穿硬编码 10）：
//   1) SelectTier 的 Resolution 跟随入参间隔；
//   2) 非法/缺失间隔夹到 spec 下限 2s（不得退化成 0：0 会让 BucketCount 除零/Inf）；
//   3) 间隔大于时间窗时夹到 rangeSec（否则 Resolution > RangeSeconds ⇒ 0 桶）；
//   4) service 全链路（含下钻）把 policy 的秒数带进响应与热层入参。

// stubRedisIntervalPolicy 是 RedisIntervalPolicy 的替身：回一个固定间隔。
type stubRedisIntervalPolicy struct{ d time.Duration }

func (p stubRedisIntervalPolicy) ReportInterval() time.Duration { return p.d }

// queryLogSpy 记录 Warn 文案。
//
// 为什么不能用 logger.NewNop()：那会把「policy 缺失时记了一条 Warn」判成**永真**
// （无日志器也「不报错」）。这条断言要看得见日志本身。
type queryLogSpy struct {
	mu    sync.Mutex
	warns []string
}

func (l *queryLogSpy) Debug(string, ...zap.Field) {}
func (l *queryLogSpy) Info(string, ...zap.Field)  {}
func (l *queryLogSpy) Error(string, ...zap.Field) {}
func (l *queryLogSpy) IsDebug() bool              { return false }
func (l *queryLogSpy) Warn(msg string, _ ...zap.Field) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, msg)
}

func (l *queryLogSpy) warnCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.warns)
}

// TestSelectTierRedisResolutionFollowsConfiguredInterval：配置驱动 —— Redis 档的
// 原生栅格必须来自 policy，而不是硬编码 10。
func TestSelectTierRedisResolutionFollowsConfiguredInterval(t *testing.T) {
	cases := []struct {
		intervalSec int64
		rangeSec    int64
		wantRes     int64
	}{
		{10, 3600, 10}, // 默认 10s → 原生 10s
		{5, 3600, 5},   // 改 5s → 原生 5s
		{30, 3600, 30}, // 改 30s → 原生 30s
		{2, 86400, 2},  // spec 下限 2s
	}
	for _, c := range cases {
		got, err := SelectTier(c.rangeSec, c.intervalSec)
		if err != nil {
			t.Fatalf("interval=%d range=%d: %v", c.intervalSec, c.rangeSec, err)
		}
		if got.Source != SourceRedis {
			t.Fatalf("interval=%d range=%d 应走 Redis 档, got %s", c.intervalSec, c.rangeSec, got.Source)
		}
		if got.Resolution != c.wantRes {
			// Errorf 而不是 Fatalf：本表里 5 与 30 两条必须各自暴露（红灯原文里两条
			// 都出现）——「只跟了其中一条」的实现藏不住。
			t.Errorf("interval=%d: Resolution=%d, want %d（硬编码 10 会在此暴露）",
				c.intervalSec, got.Resolution, c.wantRes)
		}
		// 升档必须是**原生栅格的整数倍**：栅格换了而 Scale 还按 10s 算，
		// 就是「桶与数据错位」的另一种形态。
		if step := got.Resolution * got.Scale; step%got.Resolution != 0 {
			t.Fatalf("interval=%d: 有效桶宽 %d 不是原生 %d 的整数倍", c.intervalSec, step, got.Resolution)
		}
		// 有效桶宽推出来的桶数必须是「每桶至少一个上报间隔」，不得比上限还多。
		if n := got.BucketCount(); n == 0 || n > maxBucketsForTest {
			t.Fatalf("interval=%d range=%d: 桶数 %d 越界（0 桶或超过上限 %d）",
				c.intervalSec, c.rangeSec, n, maxBucketsForTest)
		}
	}
}

// TestRedisNativeResolutionClampsToSpecFloor：非法/缺失的上报间隔必须夹到 spec
// 下限 2s，不得退化成 0（0 会让桶数计算除零/Inf）。
func TestRedisNativeResolutionClampsToSpecFloor(t *testing.T) {
	for _, bad := range []int64{0, -1, 1} {
		got, err := SelectTier(3600, bad)
		if err != nil {
			t.Fatalf("bad=%d 不应报错（应夹取）: %v", bad, err)
		}
		if got.Resolution != 2 {
			t.Fatalf("bad=%d → Resolution=%d, want 2（spec：最小 2s）", bad, got.Resolution)
		}
		if got.BucketCount() != 1800 { // 3600/2
			t.Fatalf("bad=%d → 桶数 = %d, want 1800（3600s / 2s）", bad, got.BucketCount())
		}
	}
}

// TestRedisNativeResolutionClampsToRangeWindow：上界夹取 —— 间隔大于时间窗时
// Resolution 必须夹到 rangeSec。
//
// 不加夹取的后果**实测**是热层拒绝分桶（`metricshistory.AlignBuckets` 不接受
// `step > window`）→ 服务包成 500「内部错误」，整条趋势查询不可用（见
// TestMetricsRedisTierClampsIntervalLargerThanWindow 的红灯依据）。这里同时钉住
// 「至少 1 个桶」这条契约面（BucketCount() >= 1）。
func TestRedisNativeResolutionClampsToRangeWindow(t *testing.T) {
	// 1h 窗口 + 2h 上报间隔（运维把间隔调得比最小窗口还大）：真实场景下这是错配，
	// 但契约不能因此产出 500 或 0 桶。
	cases := []struct {
		rangeSec    int64
		intervalSec int64
		wantRes     int64
	}{
		{3600, 7200, 3600},
		{3600, 3601, 3600},
		{3600, 3600, 3600}, // 恰好相等：不夹（仍是 1 桶）
		{86400, 200000, 86400},
	}
	for _, c := range cases {
		got, err := SelectTier(c.rangeSec, c.intervalSec)
		if err != nil {
			t.Fatalf("range=%d interval=%d: %v", c.rangeSec, c.intervalSec, err)
		}
		if got.Resolution != c.wantRes {
			t.Fatalf("range=%d interval=%d → Resolution=%d, want %d（上界夹取到窗口长度）",
				c.rangeSec, c.intervalSec, got.Resolution, c.wantRes)
		}
		if n := got.BucketCount(); n < 1 {
			t.Fatalf("range=%d interval=%d → 桶数 = %d，必须 ≥ 1", c.rangeSec, c.intervalSec, n)
		}
		if got.Scale != 1 {
			t.Fatalf("range=%d interval=%d → Scale=%d, want 1（本身已 ≤ 1 桶，无需升档）",
				c.rangeSec, c.intervalSec, got.Scale)
		}
	}
}

// TestSelectTierDBTiersIgnoreRedisNativeSec：DB 两档（300/3600）由**表结构**决定，
// 不受 reportInterval 影响 —— 把 policy 的值渗进 DB 档会把冷层的真实栅格也改错，
// 那是比原缺陷更隐蔽的错误（DB 行确实是 5min/1h 一行，报成别的值就是撒谎）。
func TestSelectTierDBTiersIgnoreRedisNativeSec(t *testing.T) {
	cases := []struct {
		rangeSec int64
		table    string
		wantRes  int64
	}{
		{86401, entity.TableNameMetric5m, 300},
		{604800, entity.TableNameMetric5m, 300},
		{2592000, entity.TableNameMetric5m, 300},
		{2592001, entity.TableNameMetric1h, 3600},
		{15552000, entity.TableNameMetric1h, 3600},
	}
	for _, c := range cases {
		// 合法但与 10 不同的间隔、以及非法间隔，都不得改变 DB 档
		for _, interval := range []int64{0, 2, 5, 30, 300, 99999} {
			got, err := SelectTier(c.rangeSec, interval)
			if err != nil {
				t.Fatalf("range=%d interval=%d: %v", c.rangeSec, interval, err)
			}
			if got.Source != SourceDB || got.Table != c.table || got.Resolution != c.wantRes {
				t.Fatalf("range=%d interval=%d → source=%s table=%s resolution=%d, want db/%s/%d",
					c.rangeSec, interval, got.Source, got.Table, got.Resolution, c.table, c.wantRes)
			}
		}
	}
}

// TestMetricsRedisTierReportsConfiguredResolution：端到端（service → 响应）——
// policy 的值必须带进响应与热层入参，且桶数上限仍成立。
func TestMetricsRedisTierReportsConfiguredResolution(t *testing.T) {
	ctx := context.Background()

	// 5s 间隔 + 24h：86400/5 = 17280 桶 > 4000 → 必然升档（k=5 → 有效桶宽 25s）。
	// 硬编码 10s 会给 k=3 → 30s，两个值不同，因此这条断言能击穿硬编码。
	raw5 := &stubRawQuerier{snap: &agentmetrics.RawSnapshot{}}
	svc5 := NewAgentMetricsQueryService(raw5, nil, nil,
		stubRedisIntervalPolicy{5 * time.Second}, logger.NewNop())
	resp5, err := svc5.Metrics(ctx, 1001, &request.DeviceMetricsQuery{Range: 86400})
	if err != nil {
		t.Fatalf("5s policy 的 24h 查询: %v", err)
	}
	if resp5.Source != SourceRedis {
		t.Fatalf("24h 必须走 Redis 档, got %q", resp5.Source)
	}
	if resp5.ResolutionSeconds != 25 {
		t.Fatalf("policy=5s → resolution_seconds = %d, want 25（5×Scale=5，8640→17280 桶需升 5 倍）",
			resp5.ResolutionSeconds)
	}
	if raw5.gotStep != 25*time.Second {
		t.Fatalf("热层 step = %v, want 25s —— 响应栅格与热层入参必须同源", raw5.gotStep)
	}
	if n := int64(len(resp5.Buckets)); n > maxBucketsForTest {
		t.Fatalf("桶数 %d 超过上限 %d", n, maxBucketsForTest)
	}

	// 同一档位、同一 range，换成 30s 间隔 → 2880 桶、有效桶宽 30s（不升档）。
	// 两段放在一起，断言的是「栅格跟着 policy 走」而不是「某个固定值」。
	raw30 := &stubRawQuerier{snap: &agentmetrics.RawSnapshot{}}
	svc30 := NewAgentMetricsQueryService(raw30, nil, nil,
		stubRedisIntervalPolicy{30 * time.Second}, logger.NewNop())
	resp30, err := svc30.Metrics(ctx, 1001, &request.DeviceMetricsQuery{Range: 86400})
	if err != nil {
		t.Fatalf("30s policy 的 24h 查询: %v", err)
	}
	if resp30.ResolutionSeconds != 30 {
		t.Fatalf("policy=30s → resolution_seconds = %d, want 30（2880 桶 ≤ 4000，不升档）",
			resp30.ResolutionSeconds)
	}
	if raw30.gotStep != 30*time.Second {
		t.Fatalf("热层 step = %v, want 30s", raw30.gotStep)
	}
	if n := int64(len(resp30.Buckets)); n > maxBucketsForTest {
		t.Fatalf("桶数 %d 超过上限 %d", n, maxBucketsForTest)
	}
}

// TestMetricsRedisTierFallsBackToDefaultWhenPolicyMissing：policy 为 nil（装配漏参）
// 时必须退化成默认栅格并**留一条 Warn**，且**不得 panic** —— 少接一个装配参数不该
// 变成接口 500，但也绝不能静默（静默退化正是本任务要消除的故障形态）。
func TestMetricsRedisTierFallsBackToDefaultWhenPolicyMissing(t *testing.T) {
	ctx := context.Background()
	raw := &stubRawQuerier{snap: &agentmetrics.RawSnapshot{}}
	log := &queryLogSpy{}
	svc := NewAgentMetricsQueryService(raw, nil, nil, nil, log)

	resp, err := svc.Metrics(ctx, 1001, &request.DeviceMetricsQuery{Range: 86400})
	if err != nil {
		t.Fatalf("policy 缺失不得报错（应退化成默认栅格）: %v", err)
	}
	// 8640 桶 → k=3 → 30s（与 nil policy 之前的旧行为逐字相同）
	if resp.ResolutionSeconds != RedisNativeResolutionSec*3 {
		t.Fatalf("nil policy → resolution_seconds = %d, want %d（默认 10s × Scale=3）",
			resp.ResolutionSeconds, RedisNativeResolutionSec*3)
	}
	if raw.gotStep != time.Duration(RedisNativeResolutionSec*3)*time.Second {
		t.Fatalf("nil policy → 热层 step = %v, want %ds", raw.gotStep, RedisNativeResolutionSec*3)
	}
	if got := log.warnCount(); got != 1 {
		t.Fatalf("nil policy 必须记**一条** Warn（实际 %d 条）：静默退化会让「为什么栅格是 10s」无迹可查", got)
	}
	// 有 policy 时不得误报 Warn（否则这条断言退化成永真）
	raw2 := &stubRawQuerier{snap: &agentmetrics.RawSnapshot{}}
	log2 := &queryLogSpy{}
	svc2 := NewAgentMetricsQueryService(raw2, nil, nil,
		stubRedisIntervalPolicy{10 * time.Second}, log2)
	if _, err := svc2.Metrics(ctx, 1001, &request.DeviceMetricsQuery{Range: 86400}); err != nil {
		t.Fatal(err)
	}
	if got := log2.warnCount(); got != 0 {
		t.Fatalf("policy 已注入时不得记 Warn, got %d 条: %v", got, log2.warns)
	}
}

// TestResourceDrillRedisBucketWidthFollowsConfiguredInterval：下钻走的是**同一个**
// 选档函数，因此栅格也必须跟着 policy 走：reportInterval=30 时，≤24h 下钻的两个
// 样本必须合进同一个 30s 桶（按 10s 会分成两个桶）—— 桶宽取错时下钻曲线同样会与
// 数据错位，且与趋势图用同一条曲线上的两个不同分辨率。
func TestResourceDrillRedisBucketWidthFollowsConfiguredInterval(t *testing.T) {
	ctx := context.Background()
	// 1700000011s 与 1700000021s：10s 桶下分别是 1700000010 与 1700000020，
	// 30s 桶下都是 1700000010（30×56666667 = 1700000010）。
	const t0 = int64(1_700_000_011_000)
	raw := &stubRawQuerier{bucketPts: []agentproto.MetricsSample{
		{T: t0, Disks: []agentproto.DiskMetric{{Mountpoint: "/data", UsedPercent: 10, UsedGB: 10, TotalGB: 100}}},
		{T: t0 + 10_000, Disks: []agentproto.DiskMetric{{Mountpoint: "/data", UsedPercent: 20, UsedGB: 20, TotalGB: 100}}},
	}}
	svc := NewAgentMetricsQueryService(raw, nil, nil,
		stubRedisIntervalPolicy{30 * time.Second}, logger.NewNop())

	resp, err := svc.ResourceMetrics(ctx, 1001, &request.DeviceMetricsQuery{
		Range: 3600, Kind: "disk", Name: "/data"})
	if err != nil {
		t.Fatalf("1h 下钻必须走热层: %v", err)
	}
	if resp.Source != SourceRedis || resp.ResolutionSeconds != 30 {
		t.Fatalf("source=%q resolution=%d, want redis/30（配置 30s 上报）", resp.Source, resp.ResolutionSeconds)
	}
	if len(resp.Buckets) != 1 {
		t.Fatalf("桶数 = %d, want 1（相差 10s 的两个样本在 30s 桶内同桶；按 10s 会分成 2 个）",
			len(resp.Buckets))
	}
	if b := resp.Buckets[0]; b.T != 1700000010 || b.Samples != 2 {
		t.Fatalf("桶 = {t:%d samples:%d}, want {t:1700000010 samples:2}", b.T, b.Samples)
	}
}

// TestMetricsRedisTierClampsIntervalLargerThanWindow：上界夹取的**真实后果**。
//
// 热层的分桶器 metricshistory.AlignBuckets 明确拒绝 `step > window`
// （「metricshistory: step 不得超过 window」），而趋势查询正是把
// step = Resolution×Scale、window = RangeSeconds 交给它。所以「间隔大于窗口」若不
// 加夹取，得到的是一条 **500 内部错误**（不是 0 桶）—— 运维把
// sys.agent.reportInterval 调到比最小查询窗口（1h）还大，就直接打挂整条趋势查询。
//
// 夹到窗口长度后：Resolution == RangeSeconds → 恰好 1 个桶，查询正常返回。
func TestMetricsRedisTierClampsIntervalLargerThanWindow(t *testing.T) {
	ctx := context.Background()
	raw := &stubRawQuerier{snap: &agentmetrics.RawSnapshot{}}
	// 2h 上报（与 1h 的最小窗口错配）
	svc := NewAgentMetricsQueryService(raw, nil, nil,
		stubRedisIntervalPolicy{2 * time.Hour}, logger.NewNop())

	resp, err := svc.Metrics(ctx, 1001, &request.DeviceMetricsQuery{Range: 3600})
	if err != nil {
		t.Fatalf("间隔大于窗口不得让查询失败（热层拒绝 step > window，会变成 500）: %v", err)
	}
	if resp.ResolutionSeconds != 3600 {
		t.Fatalf("resolution_seconds = %d, want 3600（夹到窗口长度）", resp.ResolutionSeconds)
	}
	if raw.gotWindow != time.Hour || raw.gotStep > raw.gotWindow {
		t.Fatalf("热层入参必须满足 step ≤ window, got step=%v window=%v", raw.gotStep, raw.gotWindow)
	}
	// 契约面：任何窗口都至少画出 1 个桶（而不是一条无解释的空曲线）
	if n := SelectTierForTest(t, 3600).BucketCount(); n < 1 {
		t.Fatalf("1h 窗口的桶数 = %d，必须 ≥ 1", n)
	}
}

func TestMetricsRejectsKindNameMismatch(t *testing.T) {
	svc := &AgentMetricsQueryService{}
	// 只给 kind 不给 name → 必须 400
	_, err := svc.Metrics(context.Background(), 1001, &request.DeviceMetricsQuery{Range: 86400, Kind: "disk"})
	assertBadRequest(t, "只给 kind 不给 name", err)

	// 只给 name 不给 kind → 必须 400（成对出现，反向同样成立）
	_, err = svc.Metrics(context.Background(), 1001, &request.DeviceMetricsQuery{Range: 86400, Name: "/"})
	assertBadRequest(t, "只给 name 不给 kind", err)

	// kind+name 同时给 → 走资源下钻，整机趋势接口必须拒绝
	_, err = svc.Metrics(context.Background(), 1001, &request.DeviceMetricsQuery{Range: 86400, Kind: "disk", Name: "/"})
	assertBadRequest(t, "整机趋势收到 kind/name", err)
}

func TestMetricsRejectsNilQuery(t *testing.T) {
	svc := &AgentMetricsQueryService{}
	_, err := svc.Metrics(context.Background(), 1001, nil)
	assertBadRequest(t, "nil 查询参数", err)
}

func TestMetricsDefaultsRangeTo24h(t *testing.T) {
	raw := &stubRawQuerier{snap: &agentmetrics.RawSnapshot{}}
	svc := NewAgentMetricsQueryService(raw, nil, nil, nil, logger.NewNop())
	resp, err := svc.Metrics(context.Background(), 1001, &request.DeviceMetricsQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.RangeSeconds != 24*3600 {
		t.Fatalf("缺省 range → RangeSeconds = %d, want 86400", resp.RangeSeconds)
	}
	if resp.Source != SourceRedis {
		t.Fatalf("缺省 range 走 Redis, got %q", resp.Source)
	}
	if raw.gotWindow != 24*time.Hour {
		t.Fatalf("热层窗口 = %v, want 24h", raw.gotWindow)
	}
}

func TestMetricsUsesRawStoreWithin24h(t *testing.T) {
	raw := &stubRawQuerier{snap: &agentmetrics.RawSnapshot{
		WindowSeconds: 86400, StepSeconds: 30,
		Buckets: []agentmetrics.TrendPoint{{T: 1234, CPUUsedPercent: f64p(11.5)}},
	}}
	svc := NewAgentMetricsQueryService(raw, nil, nil, nil, logger.NewNop())
	resp, err := svc.Metrics(context.Background(), 1001,
		&request.DeviceMetricsQuery{Range: 86400, Metrics: "cpu_used_percent"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Source != SourceRedis {
		t.Fatalf("source = %q, want redis", resp.Source)
	}
	if resp.ResolutionSeconds != 30 {
		t.Fatalf("resolution_seconds = %d, want 30（8640 桶 → k=3）", resp.ResolutionSeconds)
	}
	if raw.gotDevice != 1001 || raw.gotWindow != 24*time.Hour || raw.gotStep != 30*time.Second {
		t.Fatalf("热层入参 = device=%d window=%v step=%v", raw.gotDevice, raw.gotWindow, raw.gotStep)
	}
	if len(resp.Buckets) != 1 || resp.Buckets[0].T != 1234 {
		t.Fatalf("buckets 未归一: %+v", resp.Buckets)
	}
	if resp.Buckets[0].CPUUsedPercent == nil || *resp.Buckets[0].CPUUsedPercent != 11.5 {
		t.Fatalf("cpu_used_percent = %v, want 11.5", resp.Buckets[0].CPUUsedPercent)
	}
	if resp.Buckets[0].Load1 != nil {
		t.Fatalf("热层未给的列必须保持 nil（缺 ≠ 0）, got %v", *resp.Buckets[0].Load1)
	}
}

func TestMetricsUsesFiveMinuteTableForSevenDays(t *testing.T) {
	reader := &stubMetricReader{trend: []agentmetrics.TrendPoint{{T: 300, CPUUsedPercent: f64p(3)}}}
	svc := NewAgentMetricsQueryService(nil, reader, nil, nil, logger.NewNop())
	resp, err := svc.Metrics(context.Background(), 1001, &request.DeviceMetricsQuery{Range: 7 * 24 * 3600})
	if err != nil {
		t.Fatal(err)
	}
	if reader.gotTable != "device_metric_5m" {
		t.Fatalf("7d 必须查 _5m, got %q", reader.gotTable)
	}
	if resp.Source != SourceDB || resp.ResolutionSeconds != 300 {
		t.Fatalf("source=%q resolution=%d, want db/300", resp.Source, resp.ResolutionSeconds)
	}
	if reader.gotDevice != 1001 {
		t.Fatalf("仓储入参 deviceID = %d, want 1001", reader.gotDevice)
	}
	if reader.gotFrom != reader.gotTo-7*24*3600 {
		t.Fatalf("查询窗口 = [%d, %d]，跨度不是 7d", reader.gotFrom, reader.gotTo)
	}
	if !slices.Equal(reader.gotCols, defaultMetricColumns) {
		t.Fatalf("缺省投影列 = %v, want %v", reader.gotCols, defaultMetricColumns)
	}
	if len(resp.Buckets) != 1 || resp.Buckets[0].T != 300 {
		t.Fatalf("buckets 未归一: %+v", resp.Buckets)
	}
}

func TestMetricsUsesHourlyTableBeyondThirtyDays(t *testing.T) {
	reader := &stubMetricReader{}
	svc := NewAgentMetricsQueryService(nil, reader, nil, nil, logger.NewNop())
	resp, err := svc.Metrics(context.Background(), 1001, &request.DeviceMetricsQuery{Range: 40 * 24 * 3600})
	if err != nil {
		t.Fatal(err)
	}
	if reader.gotTable != "device_metric_1h" {
		t.Fatalf("40d 必须整体查 _1h, got %q", reader.gotTable)
	}
	if resp.ResolutionSeconds != 3600 {
		t.Fatalf("40d resolution_seconds = %d, want 3600", resp.ResolutionSeconds)
	}
}

func TestMetricsPropagatesStoreError(t *testing.T) {
	reader := &stubMetricReader{err: errors.New("db down")}
	svc := NewAgentMetricsQueryService(nil, reader, nil, nil, logger.NewNop())
	_, err := svc.Metrics(context.Background(), 1001, &request.DeviceMetricsQuery{Range: 7 * 24 * 3600})
	if err == nil {
		t.Fatal("仓储报错必须向上抛")
	}
	var ae *apperror.AppError
	if !errors.As(err, &ae) {
		t.Fatalf("必须是 apperror.AppError, got %T", err)
	}
}

// ---------- 下钻 ----------

func TestResourceMetricsRejectsIncompleteOrUnknownKind(t *testing.T) {
	svc := NewAgentMetricsQueryService(nil, nil, &stubResourceResolver{}, nil, logger.NewNop())
	ctx := context.Background()
	assertBadRequest(t, "下钻只给 kind",
		mustErr(svc.ResourceMetrics(ctx, 1001, &request.DeviceMetricsQuery{Range: 86400, Kind: "disk"})))
	assertBadRequest(t, "下钻只给 name",
		mustErr(svc.ResourceMetrics(ctx, 1001, &request.DeviceMetricsQuery{Range: 86400, Name: "/"})))
	assertBadRequest(t, "下钻 nil 参数",
		mustErr(svc.ResourceMetrics(ctx, 1001, nil)))
	assertBadRequest(t, "不支持的 kind",
		mustErr(svc.ResourceMetrics(ctx, 1001, &request.DeviceMetricsQuery{Range: 86400, Kind: "gpu", Name: "0"})))
}

// mustErr 把 (resp, err) 折成 err，便于表驱动断言 400。
func mustErr[T any](_ T, err error) error { return err }

func TestResourceMetricsRejectsRangeBeyondThirtyDays(t *testing.T) {
	ctx := context.Background()
	svc := NewAgentMetricsQueryService(nil, nil, &stubResourceResolver{id: 2001}, nil, logger.NewNop())
	// 子表只有 5min 档：>30d 落到 1h 档 → 400（不得静默返回空曲线）
	assertBadRequest(t, "40d 资源下钻",
		mustErr(svc.ResourceMetrics(ctx, 1001, &request.DeviceMetricsQuery{
			Range: 40 * 24 * 3600, Kind: "disk", Name: "/"})))
	assertBadRequest(t, "半年资源下钻",
		mustErr(svc.ResourceMetrics(ctx, 1001, &request.DeviceMetricsQuery{
			Range: request.DeviceRangeMax, Kind: "nic", Name: "eth0"})))

	// 30d 边界本身合法（仍是 5min 档），且必须解析出 resource_id 后按子表查
	reader := &stubMetricReader{resourceRows: []map[string]any{{"t": int64(600)}}}
	resolver := &stubResourceResolver{id: 2001}
	svc2 := NewAgentMetricsQueryService(nil, reader, resolver, nil, logger.NewNop())
	resp, err := svc2.ResourceMetrics(ctx, 1001, &request.DeviceMetricsQuery{
		Range: 30 * 24 * 3600, Kind: "disk", Name: "/"})
	if err != nil {
		t.Fatalf("30d 资源下钻必须合法: %v", err)
	}
	if reader.gotTable != "device_metric_disk" || reader.gotResourceID != 2001 {
		t.Fatalf("下钻入参 table=%q resourceID=%d, want device_metric_disk/2001", reader.gotTable, reader.gotResourceID)
	}
	if resolver.gotKind != "disk" || resolver.gotName != "/" {
		t.Fatalf("ResolveID 入参 kind=%q name=%q", resolver.gotKind, resolver.gotName)
	}
	if resp.ResourceKind != "disk" || resp.Name != "/" || len(resp.Buckets) != 1 {
		t.Fatalf("下钻响应不完整: %+v", resp)
	}
}

func TestResourceMetricsMapsEveryKindToItsSubTable(t *testing.T) {
	// 注：range 用 7d（DB 档）—— ≤24h 的下钻按 D7 走 Redis 原始，不再碰子表，
	// 本测试要锁的是「kind → 子表」的映射，故必须落在 DB 档（Task 8b 的夹具修正）。
	want := map[string]string{
		"disk": "device_metric_disk", "disk_io": "device_metric_diskio",
		"nic": "device_metric_nic", "sensor": "device_metric_sensor",
	}
	for kind, table := range want {
		reader := &stubMetricReader{}
		svc := NewAgentMetricsQueryService(nil, reader, &stubResourceResolver{id: 7}, nil, logger.NewNop())
		if _, err := svc.ResourceMetrics(context.Background(), 1001,
			&request.DeviceMetricsQuery{Range: 7 * 24 * 3600, Kind: kind, Name: "x"}); err != nil {
			t.Fatalf("kind=%s 下钻报错: %v", kind, err)
		}
		if reader.gotTable != table {
			t.Fatalf("kind=%s → 表 %q, want %q", kind, reader.gotTable, table)
		}
	}
}

func TestResourceMetricsUnknownResourceReturnsEmptyNot404(t *testing.T) {
	// 设备可能刚卸载该资源：ResolveID 未命中必须返回**空结果**，不得 404、不得报错。
	//
	// 注：range=6h 属 ≤24h（D7 → Redis 原始），热层下钻**不查 device_resource**
	// （刚挂载的资源可能还没落库），故「资源不存在」在这里表现为「热层没有该资源的样本」。
	// DB 档的同一语义由 TestResourceDrillDBPathUnknownResourceReturnsEmpty 覆盖。
	resolver := &stubResourceResolver{err: repository.ErrNotFound}
	reader := &stubMetricReader{err: errors.New("未命中资源时不得查子表")}
	raw := &stubRawQuerier{} // 热层里没有该资源的样本
	svc := NewAgentMetricsQueryService(raw, reader, resolver, nil, logger.NewNop())
	resp, err := svc.ResourceMetrics(context.Background(), 1001,
		&request.DeviceMetricsQuery{Range: 6 * 3600, Kind: "disk", Name: "/data"})
	if err != nil {
		t.Fatalf("资源不存在必须返回空结果而不是错误: %v", err)
	}
	if reader.calls != 0 {
		t.Fatalf("未命中资源时不得查子表（calls=%d）", reader.calls)
	}
	if resp == nil {
		t.Fatal("必须返回响应体（空 buckets），不能 nil")
	}
	if resp.Buckets == nil || len(resp.Buckets) != 0 {
		t.Fatalf("buckets 必须是空切片而不是 nil/有值: %#v", resp.Buckets)
	}
	if resp.ResourceKind != "disk" || resp.Name != "/data" {
		t.Fatalf("回显 kind/name 缺失: %+v", resp)
	}
	if resp.Source != SourceRedis {
		t.Fatalf("6h 下钻 source = %q, want redis 档位语义", resp.Source)
	}
}

// ---------- 白名单 / 归一 ----------

// newQueryTestRepo 起内存 sqlite + 生产同款雪花回调，并 AutoMigrate 指标表。
func newQueryTestRepo(t *testing.T) *repository.DeviceMetricRepo {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	node, err := snowflake.New(1, logger.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	database.NewCallbacks(node).Register(db)
	if err := db.AutoMigrate(
		&entity.DeviceMetricWide{}, &entity.DeviceMetricDisk{},
		&entity.DeviceMetricDiskIO{}, &entity.DeviceMetricNIC{}, &entity.DeviceMetricSensor{},
	); err != nil {
		t.Fatal(err)
	}
	if err := db.Table(entity.TableNameMetric1h).AutoMigrate(&entity.DeviceMetricWide{}); err != nil {
		t.Fatal(err)
	}
	return repository.NewDeviceMetricRepository(db)
}

func TestMetricsProjectionGoesThroughRepositoryWhitelist(t *testing.T) {
	repo := newQueryTestRepo(t)
	svc := NewAgentMetricsQueryService(nil, repo, nil, nil, logger.NewNop())
	ctx := context.Background()

	// 非白名单列（内含注入载荷）必须被仓储白名单拦下，不得静默通过
	_, err := svc.Metrics(ctx, 1001, &request.DeviceMetricsQuery{
		Range: 7 * 24 * 3600, Metrics: "cpu_used_percent,load1); DROP TABLE device_metric_5m;--"})
	if err == nil {
		t.Fatal("非白名单列必须报错（拒绝任意列名拼接）")
	}
	var ae *apperror.AppError
	if !errors.As(err, &ae) {
		t.Fatalf("必须是 apperror.AppError, got %T", err)
	}

	// 注入未生效：正常投影查询仍然可用
	now := time.Now().Unix()
	if err := repo.WriteBucket(ctx, &entity.DeviceMetricWide{
		DeviceID: 1001, BucketTS: now - 300, CPUUsedPercent: f64p(7.5), Load1: f64p(0.5), Samples: 30,
	}, repository.MetricSubRows{}); err != nil {
		t.Fatal(err)
	}
	// 同一个桶上另一个设备的行不得串进来
	if err := repo.WriteBucket(ctx, &entity.DeviceMetricWide{
		DeviceID: 1002, BucketTS: now - 300, CPUUsedPercent: f64p(99), Samples: 30,
	}, repository.MetricSubRows{}); err != nil {
		t.Fatal(err)
	}

	resp, err := svc.Metrics(ctx, 1001, &request.DeviceMetricsQuery{
		Range: 7 * 24 * 3600, Metrics: "bucket_ts,cpu_used_percent"})
	if err != nil {
		t.Fatalf("注入尝试后正常查询必须仍然可用: %v", err)
	}
	if len(resp.Buckets) != 1 {
		t.Fatalf("buckets = %d, want 1（只回本设备）", len(resp.Buckets))
	}
	if resp.Buckets[0].T != now-300 {
		t.Fatalf("t = %d, want %d（bucket_ts 必须映射到 TrendPoint.T）", resp.Buckets[0].T, now-300)
	}
	if resp.Buckets[0].CPUUsedPercent == nil || *resp.Buckets[0].CPUUsedPercent != 7.5 {
		t.Fatalf("cpu_used_percent = %v, want 7.5", resp.Buckets[0].CPUUsedPercent)
	}
	if resp.Buckets[0].Load1 != nil {
		t.Fatalf("未请求的列必须保持 nil（白名单投影）, got %v", *resp.Buckets[0].Load1)
	}
}

func TestToMetricPointsPreservesNil(t *testing.T) {
	got := toMetricPoints([]agentmetrics.TrendPoint{
		{T: 60, CPUUsedPercent: f64p(12.5), Samples: 3},
		{T: 120}, // 全空桶：t 必须保留，其余列省略
	})
	if len(got) != 2 {
		t.Fatalf("点数 = %d, want 2", len(got))
	}
	if got[0].T != 60 || got[0].Samples != 3 || got[0].CPUUsedPercent == nil || *got[0].CPUUsedPercent != 12.5 {
		t.Fatalf("有值桶映射错误: %+v", got[0])
	}
	if got[0].MemUsedPercent != nil || got[0].NICRXBytesSec != nil {
		t.Fatal("缺 ≠ 0：未采集的列必须保持 nil")
	}
	if got[1].T != 120 {
		t.Fatalf("空桶必须保留 t（稀疏桶靠 t 定位）, got %d", got[1].T)
	}
	if got[1].CPUUsedPercent != nil {
		t.Fatal("空桶不得被填 0")
	}
}

// ---------- 契约缺口闭合（Task 8b，逐条对齐 spec §8）----------

// TestMetricsAllExpandsToWholeTableColumns：D4 —— `metrics=*` 回该档的**可用列集**。
//
// 缺陷背景（实测）：`sanitizeColumns(table, nil)` 的语义是「只要 bucket_ts」，
// 而 service 把 `*` 直接交给仓储（返回 nil）→ `metrics=*` 只回 bucket_ts、
// **没有任何值列**，与 spec §8 明文「metrics=* 回全量」相反。
//
// S3 修正：`*` 的**取值范围**从「该表全部值列（5m 档 46 列）」收窄为
// 「该表 ∩ TrendPoint 实际承接的列」—— 表里有约 20 列没有任何响应字段去装，
// 声明成「可用」会让消费方继续把「该档不产该列」误读成「未采集/agent 挂了」，
// 而 available_metrics 的设立理由正是消除这种误读。
func TestMetricsAllExpandsToWholeTableColumns(t *testing.T) {
	ctx := context.Background()
	tableCols, err := repository.MetricQueryColumns(entity.TableNameMetric5m)
	if err != nil {
		t.Fatal(err)
	}

	reader := &stubMetricReader{}
	svc := NewAgentMetricsQueryService(nil, reader, nil, nil, logger.NewNop())
	resp, err := svc.Metrics(ctx, 1001, &request.DeviceMetricsQuery{
		Range: 7 * 24 * 3600, Metrics: request.MetricsAll})
	if err != nil {
		t.Fatalf("metrics=* 必须可用: %v", err)
	}
	if len(resp.AvailableMetrics) <= 8 {
		t.Fatalf("metrics=* 的可用列只有 %d 个（%v）—— 修前实测只回 bucket_ts，即 1 个",
			len(resp.AvailableMetrics), resp.AvailableMetrics)
	}
	if !slices.Contains(resp.AvailableMetrics, "nic_rx_bytes_sec") {
		t.Fatalf("metrics=* 必须含速率列 nic_rx_bytes_sec, got %v", resp.AvailableMetrics)
	}

	// 与「该档可用列集 + bucket_ts」逐列相等（不多不少）
	wantAvail := tierAvailableColumnsForTest(t, SelectTierForTest(t, 7*24*3600))
	if len(resp.AvailableMetrics) != len(wantAvail)+1 {
		t.Fatalf("可用列数 = %d, want %d（可用值列 %d + bucket_ts）: %v",
			len(resp.AvailableMetrics), len(wantAvail)+1, len(wantAvail), resp.AvailableMetrics)
	}
	for _, c := range wantAvail {
		if !slices.Contains(resp.AvailableMetrics, c) {
			t.Fatalf("metrics=* 漏列 %q（%v）", c, resp.AvailableMetrics)
		}
	}
	// S3 的核心：声明出来的列必须**真的装得下** —— 不许再有恒为 nil 的列
	for _, dead := range []string{"tcp_time_wait", "tcp_close_wait", "udp_total",
		"disk_io_read_ops_sec", "nic_rx_packets_sec", "nic_rx_errors_sec",
		"agent_collect_duration_ms", "agent_last_report_error", "agent_uptime_sec"} {
		if slices.Contains(resp.AvailableMetrics, dead) {
			t.Fatalf("列 %q 没有任何 TrendPoint 字段承接（恒为 nil），不得声明为可用列: %v",
				dead, resp.AvailableMetrics)
		}
	}
	// 收窄必须是**真的**收窄：可用列严格少于该表的值列（否则 S3 没生效）
	if len(wantAvail) >= len(tableCols) {
		t.Fatalf("可用列 %d 个 vs 该表值列 %d 个 —— 没有收窄，S3 未生效",
			len(wantAvail), len(tableCols))
	}

	// 展开必须**真下推**到仓储投影（只把 available_metrics 写大是假修）
	if !slices.Equal(reader.gotCols, resp.AvailableMetrics) {
		t.Fatalf("投影列 = %v, 响应可用列 = %v —— 必须一致", reader.gotCols, resp.AvailableMetrics)
	}

	// 1h 档：* 只回**该档位产出**的列。
	// 为什么：spec §8 要求 available_metrics 让前端区分「该档位无此指标」；
	// 若把 1h 档恒为 nil 的 5min-only 列也报成可用，就退回了「半年视图上 tcp_time_wait
	// 与 agent_* 恒显示 —，被误读成 agent 挂了」的老问题。同时也不得直接把 * 拒成 400
	// （那会让文档化的 metrics=* 在半年档完全不可用）。
	reader1h := &stubMetricReader{}
	svc1h := NewAgentMetricsQueryService(nil, reader1h, nil, nil, logger.NewNop())
	resp1h, err := svc1h.Metrics(ctx, 1001, &request.DeviceMetricsQuery{
		Range: 40 * 24 * 3600, Metrics: request.MetricsAll})
	if err != nil {
		t.Fatalf("1h 档的 metrics=* 必须可用（不得 400）: %v", err)
	}
	for _, dead := range []string{"tcp_time_wait", "tcp_close_wait", "agent_ws_reconnect_count", "agent_last_report_error"} {
		if slices.Contains(resp1h.AvailableMetrics, dead) {
			t.Fatalf("1h 档不产出 %q，不得报成可用列: %v", dead, resp1h.AvailableMetrics)
		}
	}
	if !slices.Contains(resp1h.AvailableMetrics, "nic_rx_bytes_sec") || len(resp1h.AvailableMetrics) <= 8 {
		t.Fatalf("1h 档的 * 仍必须回该档的值列: %v", resp1h.AvailableMetrics)
	}
}

// SelectTierForTest / tierAvailableColumnsForTest 是测试侧的薄封装：
// 期望值必须来自**被测的同一套档位推导**（而不是测试里第二份表名/列集常量），
// 否则「响应收窄」与「期望收窄」会各写一份、双双漂移。
func SelectTierForTest(t *testing.T, rangeSec int64) TierSelection {
	t.Helper()
	// 第二参传**默认值常量**：调用方（表名/列集的结构性断言）只关心档位与表，
	// 传默认值使「Redis 档的 Resolution」与接配置之前逐字相同。
	sel, err := SelectTier(rangeSec, RedisNativeResolutionSec)
	if err != nil {
		t.Fatalf("SelectTier(%d): %v", rangeSec, err)
	}
	return sel
}

func tierAvailableColumnsForTest(t *testing.T, sel TierSelection) []string {
	t.Helper()
	cols, err := tierAvailableColumns(sel)
	if err != nil {
		t.Fatalf("tierAvailableColumns: %v", err)
	}
	return cols
}

// TestExplicitMetricsRejectsColumnsTheResponseModelCannotCarry：S3 的另一半 ——
// 「表里有、响应模型装不下」的列被**显式请求**时必须 400 并说明原因，不得静默给 nil。
//
// 为什么必须报错而不是「照常返回 nil」：这类列的取值**恒为空**，与「该桶未采集」
// 在线上完全不可区分（正是 available_metrics 要消除的误读）。同一参数以前返回
// 200 + 一条恒空的曲线，消费方会当成 agent 故障去排查。
func TestExplicitMetricsRejectsColumnsTheResponseModelCannotCarry(t *testing.T) {
	ctx := context.Background()
	// 这些列在 _5m（以及 Redis 档的同构 schema）里**存在**，但 TrendPoint 不承接
	orphans := []string{"tcp_time_wait", "tcp_close_wait", "udp_total",
		"disk_io_read_ops_sec", "nic_rx_packets_sec", "agent_uptime_sec"}
	allowed, err := repository.MetricQueryColumns(entity.TableNameMetric5m)
	if err != nil {
		t.Fatal(err)
	}
	for _, col := range orphans {
		if !slices.Contains(allowed, col) {
			t.Fatalf("测试前提失效：%q 应当是 _5m 的真实列", col)
		}
	}

	for _, tc := range []struct {
		name     string
		rangeSec int64
	}{
		{"5min 档", 7 * 24 * 3600},
		{"Redis 档", 6 * 3600},
	} {
		for _, col := range orphans {
			reader := &stubMetricReader{}
			raw := &stubRawQuerier{snap: &agentmetrics.RawSnapshot{}}
			svc := NewAgentMetricsQueryService(raw, reader, nil, nil, logger.NewNop())
			_, err := svc.Metrics(ctx, 1001, &request.DeviceMetricsQuery{Range: tc.rangeSec, Metrics: col})
			assertBadRequest(t, tc.name+"显式请求 "+col, err)
			if reader.calls != 0 || raw.calls != 0 {
				t.Fatalf("必须在查库/查热层之前拒绝（不白跑一次查询）: col=%s db=%d raw=%d",
					col, reader.calls, raw.calls)
			}
			// 错误信息必须说清「这列有数据、只是本接口不返回它」，而不是含糊的
			// 「列非法」—— 含糊会让调用方以为是自己拼错了列名，去改一个没拼错的参数。
			// 同时不得为了「说清楚」而把响应模型名、表名、spec 章节号写进消息：它会被
			// 前端原样显示在页面上（见 TestUserFacingErrorsSpeakHuman）。
			var ae *apperror.AppError
			if errors.As(err, &ae) {
				if !strings.Contains(ae.Message, "不返回") {
					t.Fatalf("400 的说明必须点明原因（该列有数据但接口不返回它）, got %q", ae.Message)
				}
				if strings.Contains(ae.Message, "不是有效的指标列") {
					t.Fatalf("400 的说明退回了「列非法」的含糊说法, got %q", ae.Message)
				}
			}
		}
	}

	// 反向：真正装得下的列在同样两档必须照常可用
	for _, tc := range []struct {
		name     string
		rangeSec int64
	}{
		{"5min 档", 7 * 24 * 3600},
		{"Redis 档", 6 * 3600},
	} {
		for _, col := range []string{"cpu_used_percent", "tcp_total", "disk_used_percent", "max_temperature_c", "samples"} {
			reader := &stubMetricReader{}
			raw := &stubRawQuerier{snap: &agentmetrics.RawSnapshot{}}
			svc := NewAgentMetricsQueryService(raw, reader, nil, nil, logger.NewNop())
			if _, err := svc.Metrics(ctx, 1001, &request.DeviceMetricsQuery{Range: tc.rangeSec, Metrics: col}); err != nil {
				t.Fatalf("%s 请求 %q 必须可用: %v", tc.name, col, err)
			}
		}
	}
}

// TestExplicitMetricsWhitelistOnBothTiers：S4 —— 显式 `metrics` 的校验**两档同口径**。
//
// 缺陷背景：Redis 档（≤24h）此前完全不校验列 —— 任意列名都返回 200 且被写进
// `available_metrics`（前端据此画一条恒空曲线）；同一个参数在 DB 档则会撞上仓储
// 白名单变成 **500**。修后两档都是 **400**（参数错误），与下钻的错误口径一致。
func TestExplicitMetricsWhitelistOnBothTiers(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name     string
		rangeSec int64
		metrics  string
	}{
		{"Redis 档-不存在的列", 6 * 3600, "not_a_column"},
		{"Redis 档-注入载荷", 6 * 3600, "cpu_used_percent); DROP TABLE device_metric_5m;--"},
		{"5min 档-不存在的列", 7 * 24 * 3600, "not_a_column"},
		{"1h 档-不存在的列", 40 * 24 * 3600, "not_a_column"},
		{"Redis 档-大小写不符", 6 * 3600, "CPU_USED_PERCENT"},
		{"Redis 档-CSV 里夹带非法列", 6 * 3600, "cpu_used_percent,not_a_column"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader := &stubMetricReader{}
			raw := &stubRawQuerier{snap: &agentmetrics.RawSnapshot{}}
			svc := NewAgentMetricsQueryService(raw, reader, nil, nil, logger.NewNop())
			_, err := svc.Metrics(ctx, 1001, &request.DeviceMetricsQuery{Range: tc.rangeSec, Metrics: tc.metrics})
			// 统一 400：不得是 200（静默放行）、也不得是 500（把参数错误当服务故障）
			assertBadRequest(t, tc.name, err)
		})
	}

	// Redis 档的 available_metrics **不得**出现任何未被校验的列名
	raw := &stubRawQuerier{snap: &agentmetrics.RawSnapshot{}}
	svc := NewAgentMetricsQueryService(raw, nil, nil, nil, logger.NewNop())
	resp, err := svc.Metrics(ctx, 1001, &request.DeviceMetricsQuery{
		Range: 6 * 3600, Metrics: "cpu_used_percent,load1"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(resp.AvailableMetrics, []string{"bucket_ts", "cpu_used_percent", "load1"}) {
		t.Fatalf("Redis 档 available_metrics = %v, want 请求列 + bucket_ts", resp.AvailableMetrics)
	}
}

// TestBucketTSAlwaysProjectedEvenWhenMetricsNamed：D5 —— `t` 是契约的一部分。
//
// 缺陷背景（实测）：显式 `metrics=cpu_used_percent`（不含 bucket_ts）时投影里没有
// bucket_ts，扫出来的**每个点 t 都是 0**，前端按 t 定位时间全落在 1970。
func TestBucketTSAlwaysProjectedEvenWhenMetricsNamed(t *testing.T) {
	ctx := context.Background()

	// 投影列侧：bucket_ts 必须被恒补后下推到仓储
	reader := &stubMetricReader{}
	svc := NewAgentMetricsQueryService(nil, reader, nil, nil, logger.NewNop())
	if _, err := svc.Metrics(ctx, 1001, &request.DeviceMetricsQuery{
		Range: 7 * 24 * 3600, Metrics: "cpu_used_percent"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(reader.gotCols, "bucket_ts") {
		t.Fatalf("显式 metrics 未含 bucket_ts 时必须恒补（D5）, got 投影列 %v", reader.gotCols)
	}

	// 端到端侧：真库投影 → 桶里的 t 必须非 0
	repo := newQueryTestRepo(t)
	now := time.Now().Unix()
	if err := repo.WriteBucket(ctx, &entity.DeviceMetricWide{
		DeviceID: 1001, BucketTS: now - 300, CPUUsedPercent: f64p(7.5), Load1: f64p(0.5), Samples: 30,
	}, repository.MetricSubRows{}); err != nil {
		t.Fatal(err)
	}
	svcDB := NewAgentMetricsQueryService(nil, repo, nil, nil, logger.NewNop())
	resp, err := svcDB.Metrics(ctx, 1001, &request.DeviceMetricsQuery{
		Range: 7 * 24 * 3600, Metrics: "cpu_used_percent"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Buckets) != 1 {
		t.Fatalf("桶数 = %d, want 1", len(resp.Buckets))
	}
	if resp.Buckets[0].T != now-300 {
		t.Fatalf("t = %d, want %d —— 显式 metrics 时投影缺了 bucket_ts（t 恒 0）",
			resp.Buckets[0].T, now-300)
	}
	if resp.Buckets[0].CPUUsedPercent == nil || *resp.Buckets[0].CPUUsedPercent != 7.5 {
		t.Fatalf("cpu_used_percent = %v, want 7.5", resp.Buckets[0].CPUUsedPercent)
	}
	if resp.Buckets[0].Load1 != nil {
		t.Fatalf("未请求的列必须保持 nil（白名单投影语义不变）, got %v", *resp.Buckets[0].Load1)
	}
}

// TestTierRejectsFiveMinOnlyColumnsOnOneHourTier：D6 —— 1h 档不存在的列被**显式请求** → 400。
//
// spec §8 明文：「range > 2592000 且 metrics 含 5min-only 列（tcp_time_wait/
// tcp_close_wait/agent_*）时返回 BadRequest，**不静默剔除**（与 kind+name 的校验同构）」。
func TestTierRejectsFiveMinOnlyColumnsOnOneHourTier(t *testing.T) {
	ctx := context.Background()
	for _, col := range []string{"tcp_time_wait", "tcp_close_wait", "agent_ws_reconnect_count", "agent_last_report_error"} {
		reader := &stubMetricReader{}
		svc := NewAgentMetricsQueryService(nil, reader, nil, nil, logger.NewNop())
		_, err := svc.Metrics(ctx, 1001, &request.DeviceMetricsQuery{Range: 40 * 24 * 3600, Metrics: col})
		assertBadRequest(t, "1h 档显式请求 "+col, err)
		if reader.calls != 0 {
			t.Fatalf("必须在查库之前拒绝（不静默剔除、也不白跑一次查询）: col=%s calls=%d", col, reader.calls)
		}
	}

	// 同档位、不含 5min-only 列 → 正常
	reader := &stubMetricReader{}
	svc := NewAgentMetricsQueryService(nil, reader, nil, nil, logger.NewNop())
	if _, err := svc.Metrics(ctx, 1001, &request.DeviceMetricsQuery{
		Range: 40 * 24 * 3600, Metrics: "cpu_used_percent,tcp_total"}); err != nil {
		t.Fatalf("1h 档请求该档存在的列不得报错: %v", err)
	}
	if reader.gotTable != "device_metric_1h" {
		t.Fatalf("40d 必须整体走 _1h, got %q", reader.gotTable)
	}

	// 反向对照（**S3 修正**）：这些列在 5min 档与 Redis 档的**表里**确实存在，
	// 但没有任何 TrendPoint 字段承接它们 —— 旧断言要求「必须可用」是错的：
	// 那时它们返回 200 且值恒为 nil（消费方无法与「未采集」区分）。
	// 现在两档都必须 400 并说明原因（见 TestExplicitMetricsRejectsColumnsTheResponseModelCannotCarry）。
	reader5m := &stubMetricReader{}
	svc5m := NewAgentMetricsQueryService(nil, reader5m, nil, nil, logger.NewNop())
	_, err := svc5m.Metrics(ctx, 1001, &request.DeviceMetricsQuery{
		Range: 30 * 24 * 3600, Metrics: "tcp_time_wait"})
	assertBadRequest(t, "5min 档显式请求响应装不下的 tcp_time_wait", err)
	raw := &stubRawQuerier{snap: &agentmetrics.RawSnapshot{}}
	svcRaw := NewAgentMetricsQueryService(raw, nil, nil, nil, logger.NewNop())
	_, err = svcRaw.Metrics(ctx, 1001, &request.DeviceMetricsQuery{
		Range: 24 * 3600, Metrics: "tcp_time_wait"})
	assertBadRequest(t, "Redis 档显式请求响应装不下的 tcp_time_wait", err)

	// 而**真正**存在于 5min/Redis 档且装得下的列，两档都不得被误拒
	for _, tc := range []struct {
		name     string
		rangeSec int64
	}{
		{"5min 档", 30 * 24 * 3600},
		{"Redis 档", 24 * 3600},
	} {
		for _, col := range []string{"tcp_total", "tcp_established", "cpu_iowait", "samples"} {
			reader := &stubMetricReader{}
			rawQ := &stubRawQuerier{snap: &agentmetrics.RawSnapshot{}}
			svc := NewAgentMetricsQueryService(rawQ, reader, nil, nil, logger.NewNop())
			if _, err := svc.Metrics(ctx, 1001, &request.DeviceMetricsQuery{Range: tc.rangeSec, Metrics: col}); err != nil {
				t.Fatalf("%s 请求 %q 必须可用: %v", tc.name, col, err)
			}
		}
	}
}

// TestResourceDrillUsesSubTableColumns：D2+D3 —— 默认下钻（只传 kind+name）必须成功，
// 且可用列/值列都是**子表自己的列**。
//
// 缺陷背景（实测）：不传 metrics 时用的是**宽表**默认列 → 被子表白名单拒绝 → 500
// （console 的默认下钻必然失败）；即使换了列名，子表列名与 TrendPoint 字段也不同名，
// 类型化扫描会让桶的**值列全为 nil**（GORM 映射不上不报错）。
func TestResourceDrillUsesSubTableColumns(t *testing.T) {
	ctx := context.Background()
	want, ok := repository.ResourceTableColumns("device_metric_disk")
	if !ok {
		t.Fatal("device_metric_disk 必须登记下钻列集")
	}

	repo := newQueryTestRepo(t)
	now := time.Now().Unix()
	if err := repo.WriteBucket(ctx, &entity.DeviceMetricWide{
		DeviceID: 1001, BucketTS: now - 300, Samples: 30,
	}, repository.MetricSubRows{Disks: []entity.DeviceMetricDisk{{
		ResourceID: 2001, BucketTS: now - 300,
		UsedPercent: f64p(62), UsedGB: f64p(620), TotalGB: f64p(1000), InodesUsedPercent: f64p(12),
	}}}); err != nil {
		t.Fatal(err)
	}

	svc := NewAgentMetricsQueryService(nil, repo, &stubResourceResolver{id: 2001}, nil, logger.NewNop())
	resp, err := svc.ResourceMetrics(ctx, 1001, &request.DeviceMetricsQuery{
		Range: 7 * 24 * 3600, Kind: "disk", Name: "/"})
	if err != nil {
		t.Fatalf("只传 kind+name 的默认下钻必须成功（console 默认路径）: %v", err)
	}
	if len(resp.AvailableMetrics) != len(want) {
		t.Fatalf("available_metrics = %v, want 子表列 %v", resp.AvailableMetrics, want)
	}
	for _, c := range want {
		if !slices.Contains(resp.AvailableMetrics, c) {
			t.Fatalf("available_metrics 漏列 %q: %v", c, resp.AvailableMetrics)
		}
	}
	if slices.Contains(resp.AvailableMetrics, "cpu_used_percent") {
		t.Fatalf("下钻的可用列不得是宽表列: %v", resp.AvailableMetrics)
	}
	if len(resp.Buckets) != 1 {
		t.Fatalf("桶数 = %d, want 1: %+v", len(resp.Buckets), resp.Buckets)
	}
	b := resp.Buckets[0]
	if b.T != now-300 {
		t.Fatalf("t = %d, want %d", b.T, now-300)
	}
	for col, wantVal := range map[string]float64{
		"used_percent": 62, "used_gb": 620, "total_gb": 1000, "inodes_used_percent": 12,
	} {
		v, ok := b.Values[col]
		if !ok || v == nil {
			t.Fatalf("values 缺列 %q（修前实测：下钻桶的值列全为 nil）: %v", col, b.Values)
		}
		if *v != wantVal {
			t.Fatalf("values[%q] = %v, want %v", col, *v, wantVal)
		}
	}
}

// TestResourceDrillWithin24hUsesHotLayer：D7 —— ≤24h 走 Redis 原始 + AggregateResource，
// DB 子表**不得被调用**（spec §7.2 选档表）。
func TestResourceDrillWithin24hUsesHotLayer(t *testing.T) {
	ctx := context.Background()
	const t0 = int64(1_700_000_000_000) // 对齐到 10s 桶（1700000000s 与 1700000004s 同桶）
	raw := &stubRawQuerier{bucketPts: []agentproto.MetricsSample{
		{T: t0, Disks: []agentproto.DiskMetric{
			{Mountpoint: "/data", UsedPercent: 10, UsedGB: 10, TotalGB: 100, InodesUsedPercent: 8},
		}},
		{T: t0 + 4000, Disks: []agentproto.DiskMetric{
			{Mountpoint: "/", UsedPercent: 99, UsedGB: 99, TotalGB: 100}, // 不得串进 "/data"
			{Mountpoint: "/data", UsedPercent: 20, UsedGB: 20, TotalGB: 100, InodesUsedPercent: 12},
		}},
	}}
	// DB 侧一律报错：一旦下钻 ≤24h 还去打子表，本测试必红（同时证明「未被调用」）
	reader := &stubMetricReader{err: errors.New("下钻 ≤24h 不得落 DB 子表")}
	resolver := &stubResourceResolver{id: 2001}
	svc := NewAgentMetricsQueryService(raw, reader, resolver, nil, logger.NewNop())

	resp, err := svc.ResourceMetrics(ctx, 1001, &request.DeviceMetricsQuery{
		Range: 3600, Kind: "disk", Name: "/data"})
	if err != nil {
		t.Fatalf("1h 下钻必须走热层: %v", err)
	}
	if raw.bucketCalls != 1 {
		t.Fatalf("热层 Bucket 调用次数 = %d, want 1", raw.bucketCalls)
	}
	if reader.calls != 0 {
		t.Fatalf("下钻 ≤24h 不得查 DB 子表（calls=%d）", reader.calls)
	}
	if resolver.calls != 0 {
		t.Fatalf("热层下钻不需要 device_resource 解析（刚挂载的资源可能还没落库）, calls=%d", resolver.calls)
	}
	if resp.Source != SourceRedis || resp.ResolutionSeconds != 10 {
		t.Fatalf("source=%q resolution=%d, want redis/10", resp.Source, resp.ResolutionSeconds)
	}
	if raw.gotDevice != 1001 || raw.gotToMs-raw.gotFromMs != 3600*1000 {
		t.Fatalf("热层入参 device=%d 窗口=[%d, %d]，跨度不是 1h", raw.gotDevice, raw.gotFromMs, raw.gotToMs)
	}
	// 两档的可用列集必须逐字一致（前端切档不丢列）
	wantCols, _ := repository.ResourceTableColumns("device_metric_disk")
	if len(resp.AvailableMetrics) != len(wantCols) {
		t.Fatalf("热层 available_metrics = %v, want 子表列 %v", resp.AvailableMetrics, wantCols)
	}
	if len(resp.Buckets) != 1 {
		t.Fatalf("桶数 = %d, want 1（两个样本同 10s 桶）: %+v", len(resp.Buckets), resp.Buckets)
	}
	b := resp.Buckets[0]
	if b.T != t0/1000 {
		t.Fatalf("桶时间 = %d, want %d（按 bucketSec 对齐）", b.T, t0/1000)
	}
	if b.Samples != 2 {
		t.Fatalf("samples = %d, want 2（命中该资源的样本数）", b.Samples)
	}
	for col, wantVal := range map[string]float64{
		"used_percent": 15, "used_gb": 15, "total_gb": 100, "inodes_used_percent": 10,
	} {
		v, ok := b.Values[col]
		if !ok || v == nil {
			t.Fatalf("热层 values 缺列 %q: %v", col, b.Values)
		}
		if *v != wantVal {
			t.Fatalf("热层 values[%q] = %v, want %v（只聚合 name 命中的资源）", col, *v, wantVal)
		}
	}
}

// TestResourceDrillFiltersValuesByExplicitMetrics：下钻的显式 metrics 只回该子集；
// 子表明细列集之外的列 → 400（不得静默回空曲线）。
func TestResourceDrillFiltersValuesByExplicitMetrics(t *testing.T) {
	ctx := context.Background()
	want, _ := repository.ResourceTableColumns("device_metric_disk")

	raw := &stubRawQuerier{bucketPts: []agentproto.MetricsSample{{T: 1_700_000_000_000,
		Disks: []agentproto.DiskMetric{
			{Mountpoint: "/data", UsedPercent: 10, UsedGB: 10, TotalGB: 100, InodesUsedPercent: 8},
		}}}}
	svc := NewAgentMetricsQueryService(raw, nil, nil, nil, logger.NewNop())

	resp, err := svc.ResourceMetrics(ctx, 1001, &request.DeviceMetricsQuery{
		Range: 3600, Kind: "disk", Name: "/data", Metrics: "used_percent,used_gb"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(resp.AvailableMetrics, []string{"used_percent", "used_gb"}) {
		t.Fatalf("available_metrics = %v, want 请求的子集", resp.AvailableMetrics)
	}
	if len(resp.Buckets) != 1 || len(resp.Buckets[0].Values) != 2 {
		t.Fatalf("values 必须过滤到请求的列: %+v", resp.Buckets)
	}
	if _, ok := resp.Buckets[0].Values["inodes_used_percent"]; ok {
		t.Fatal("未请求的列不得出现在 values 里")
	}

	// "*" → 子表全量列（与趋势路径的语义一致）
	respAll, err := svc.ResourceMetrics(ctx, 1001, &request.DeviceMetricsQuery{
		Range: 3600, Kind: "disk", Name: "/data", Metrics: request.MetricsAll})
	if err != nil {
		t.Fatal(err)
	}
	if len(respAll.AvailableMetrics) != len(want) {
		t.Fatalf("下钻的 metrics=* 必须回子表全量列: %v", respAll.AvailableMetrics)
	}

	// 子表列集之外的列 → 400，且必须在读热层之前拒绝
	before := raw.bucketCalls
	assertBadRequest(t, "下钻请求宽表列", mustErr(svc.ResourceMetrics(ctx, 1001,
		&request.DeviceMetricsQuery{Range: 3600, Kind: "disk", Name: "/data", Metrics: "cpu_used_percent"})))
	if raw.bucketCalls != before {
		t.Fatal("非法列必须在读热层之前拒绝")
	}
}

// TestResourceDrillDBPathUnknownResourceReturnsEmpty：>24h 的 DB 档保留
// 「资源解析不到 → 空结果而非 404」的语义（≤24h 热层档没有这一步，见 D7）。
func TestResourceDrillDBPathUnknownResourceReturnsEmpty(t *testing.T) {
	// 未命中用**仓储哨兵**（真仓储的 ResolveID 就返回它）：service 只把
	// repository.ErrNotFound 当「资源不存在 → 空结果」，其它错误一律 500。
	resolver := &stubResourceResolver{err: repository.ErrNotFound}
	reader := &stubMetricReader{err: errors.New("未命中资源时不得查子表")}
	svc := NewAgentMetricsQueryService(nil, reader, resolver, nil, logger.NewNop())
	resp, err := svc.ResourceMetrics(context.Background(), 1001,
		&request.DeviceMetricsQuery{Range: 7 * 24 * 3600, Kind: "disk", Name: "/gone"})
	if err != nil {
		t.Fatalf("资源不存在必须返回空结果而不是错误: %v", err)
	}
	if reader.calls != 0 {
		t.Fatalf("未命中资源时不得查子表（calls=%d）", reader.calls)
	}
	if resp.Buckets == nil || len(resp.Buckets) != 0 {
		t.Fatalf("buckets 必须是空切片而不是 nil/有值: %#v", resp.Buckets)
	}
	if resp.Source != SourceDB {
		t.Fatalf("7d 下钻 source = %q, want db", resp.Source)
	}
	want, _ := repository.ResourceTableColumns("device_metric_disk")
	if len(resp.AvailableMetrics) != len(want) {
		t.Fatalf("available_metrics = %v, want 子表列 %v", resp.AvailableMetrics, want)
	}
}

// TestResourceDrillColumnsMatchHotLayerAggregation：漂移守卫（D2/D7）。
//
// 同一次下钻在 1h（热层聚合）与 7d（子表读取）必须给出**同一套键**：否则前端
// 在切档的瞬间会丢列（available_metrics 与实际 keys 不一致）。三层必须同源：
// service 的 kind→表、repository 的子表列集、agentmetrics 的聚合产出。
func TestResourceDrillColumnsMatchHotLayerAggregation(t *testing.T) {
	const ts = int64(1_700_000_000_000)
	names := map[string]string{"disk": "/", "disk_io": "sda", "nic": "eth0", "sensor": "coretemp"}
	samples := map[string]agentproto.MetricsSample{
		"disk": {T: ts, Disks: []agentproto.DiskMetric{
			{Mountpoint: "/", UsedPercent: 1, UsedGB: 1, TotalGB: 1, InodesUsedPercent: 1}}},
		"disk_io": {T: ts, DiskIO: []agentproto.DiskIOMetric{
			{Name: "sda", ReadBytesPerSec: 1, WriteBytesPerSec: 1, ReadOpsPerSec: 1, WriteOpsPerSec: 1, IOTimePercent: 1}}},
		"nic": {T: ts, NICs: []agentproto.NICMetric{
			{Name: "eth0", RXBytesPerSec: 1, TXBytesPerSec: 1, RXPacketsPerSec: 1, TXPacketsPerSec: 1,
				RXErrorsPerSec: 1, TXErrorsPerSec: 1, RXDroppedPerSec: 1}}},
		"sensor": {T: ts, Sensors: []agentproto.SensorMetric{{Name: "coretemp", TemperatureC: f64p(1)}}},
	}

	for kind, s := range samples {
		table, ok := resourceTable(kind)
		if !ok {
			t.Fatalf("kind %q 未映射到子表", kind)
		}
		want, ok := repository.ResourceTableColumns(table)
		if !ok {
			t.Fatalf("%s 未登记下钻列集", table)
		}
		got := agentmetrics.AggregateResource(kind, names[kind], []agentproto.MetricsSample{s}, 300)
		if len(got) != 1 {
			t.Fatalf("%s: 聚合桶数 = %d, want 1", kind, len(got))
		}
		keys := make([]string, 0, len(got[0].Values))
		for k, v := range got[0].Values {
			if v == nil {
				t.Fatalf("%s: 键 %q 的值为 nil（缺 ≠ 0 应当不出现）", kind, k)
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		wantSorted := append([]string(nil), want...)
		sort.Strings(wantSorted)
		if !slices.Equal(keys, wantSorted) {
			t.Fatalf("%s: 热层聚合键集 = %v, 子表登记列集 = %v", kind, keys, wantSorted)
		}
		if !slices.Contains(agentmetrics.ResourceKinds(), kind) {
			t.Fatalf("热层不认识 kind %q（三层枚举必须同源）", kind)
		}
	}
	if len(agentmetrics.ResourceKinds()) != 4 {
		t.Fatalf("资源种类数 = %d, want 4", len(agentmetrics.ResourceKinds()))
	}
	if got := agentmetrics.AggregateResource("gpu", "0", nil, 300); len(got) != 0 {
		t.Fatalf("未知 kind 必须返回空而不是 panic, got %+v", got)
	}
}

// TestToFloat64PtrNormalisesDriverShapes：下钻的列值只有**一个**归一入口。
//
// 子表列名没有对应的 Go 结构体（D2），逐字段类型断言是脆弱的主要来源 ——
// 驱动对同一张表的不同列可能给出 int64 / float64 / []byte / string / nil。
// 归一必须全部覆盖，且无法解析时返回 nil（**绝不能**当成 0）。
func TestToFloat64PtrNormalisesDriverShapes(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want *float64
	}{
		{"nil（NULL）", nil, nil},
		{"float64", 62.5, f64p(62.5)},
		{"int64", int64(62), f64p(62)},
		{"int", 62, f64p(62)},
		{"int32", int32(62), f64p(62)},
		{"uint64", uint64(62), f64p(62)},
		{"float32", float32(62.5), f64p(62.5)},
		{"[]byte（驱动文本形态）", []byte("62.5"), f64p(62.5)},
		{"string", "62.5", f64p(62.5)},
		{"*float64", f64p(62.5), f64p(62.5)},
		{"nil *float64", (*float64)(nil), nil},
		{"空 []byte（MySQL 空串形态）", []byte(""), nil},
		{"不可解析的串", []byte("abc"), nil},
		{"NaN", math.NaN(), nil},
		{"+Inf", math.Inf(1), nil},
		{"不支持的类型", true, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := toFloat64Ptr(c.in)
			if (got == nil) != (c.want == nil) {
				t.Fatalf("toFloat64Ptr(%#v) = %v, want %v", c.in, got, c.want)
			}
			if got != nil && *got != *c.want {
				t.Fatalf("toFloat64Ptr(%#v) = %v, want %v", c.in, *got, *c.want)
			}
		})
	}
}

// ---------- 测试替身 ----------

type stubRawQuerier struct {
	snap *agentmetrics.RawSnapshot
	err  error

	// 下钻热层（RawStore.Bucket）的夹具与调用记录（D7）
	bucketPts []agentproto.MetricsSample
	bucketErr error

	calls       int
	gotDevice   uint64
	gotWindow   time.Duration
	gotStep     time.Duration
	bucketCalls int
	gotFromMs   int64
	gotToMs     int64
}

func (s *stubRawQuerier) Query(_ context.Context, deviceID uint64, window, step time.Duration) (*agentmetrics.RawSnapshot, error) {
	s.calls++
	s.gotDevice, s.gotWindow, s.gotStep = deviceID, window, step
	if s.err != nil {
		return nil, s.err
	}
	// 替身，但**语义是真的**：真实热层（metricshistory.Window.Query）第一件事就是
	// AlignBuckets(window, step)，而它明确拒绝 step > window（buckets.go）。若替身
	// 只记录入参不校验，一条「step 大于窗口」的错误会被静默吞掉 —— 那正是本任务
	// 上界夹取要防的 500（见 TestMetricsRedisTierClampsIntervalLargerThanWindow）。
	if _, err := metricshistory.AlignBuckets(window, step); err != nil {
		return nil, err
	}
	return s.snap, nil
}

// Bucket 实现 AgentRawBucketReader：记录调用次数，供「热层被调用 / DB 未被调用」断言。
func (s *stubRawQuerier) Bucket(_ context.Context, deviceID uint64, fromMs, toMs int64) ([]agentproto.MetricsSample, error) {
	s.bucketCalls++
	s.gotDevice, s.gotFromMs, s.gotToMs = deviceID, fromMs, toMs
	if s.bucketErr != nil {
		return nil, s.bucketErr
	}
	return s.bucketPts, nil
}

type stubMetricReader struct {
	trend []agentmetrics.TrendPoint
	// resourceRows 是下钻的开放形状夹具（SELECT * + t 别名）。
	resourceRows []map[string]any
	err          error

	calls         int
	gotTable      string
	gotDevice     uint64
	gotResourceID uint64
	gotFrom       int64
	gotTo         int64
	gotCols       []string
}

func (s *stubMetricReader) ReadTrendPoints(_ context.Context, table string, deviceID uint64,
	from, to int64, columns []string) ([]agentmetrics.TrendPoint, error) {
	s.calls++
	s.gotTable, s.gotDevice, s.gotFrom, s.gotTo, s.gotCols = table, deviceID, from, to, columns
	if s.err != nil {
		return nil, s.err
	}
	return s.trend, nil
}

func (s *stubMetricReader) ReadResourceRows(_ context.Context, table string, resourceID uint64,
	from, to int64) ([]map[string]any, error) {
	s.calls++
	s.gotTable, s.gotResourceID, s.gotFrom, s.gotTo = table, resourceID, from, to
	if s.err != nil {
		return nil, s.err
	}
	return s.resourceRows, nil
}

type stubResourceResolver struct {
	id  uint64
	err error

	calls     int
	gotDevice uint64
	gotKind   string
	gotName   string
}

func (s *stubResourceResolver) ResolveID(_ context.Context, deviceID uint64, kind, name string) (uint64, error) {
	s.calls++
	s.gotDevice, s.gotKind, s.gotName = deviceID, kind, name
	if s.err != nil {
		return 0, s.err
	}
	return s.id, nil
}

// ---------- S2：升档必须落到查询栅格上 ----------

// TestMetricsTierLiftMergesDbRowsToEffectiveGrid：S2 —— `Scale > 1` 时响应必须
// **真的**归并到有效栅格上。
//
// 缺陷背景（实测）：`SelectTier` 算了 Scale（30d → 3），但 `ReadTrendPoints`
// 既没有 step 参数也没有 GROUP BY —— 30d 请求照样返回 **8640 行**，而
// `resolution_seconds` 报 900。消费方按 `t` 定位时看到的步长与声明不符，
// 4000 桶上限也形同虚设。
//
// 这里用桩返回「数据库里的原生 300s 栅格行」（8640 行），断言响应被收敛到
// ≤ MaxBuckets、每桶 t 对齐 900s、且值按 samples **加权**（不是简单平均）。
func TestMetricsTierLiftMergesDbRowsToEffectiveGrid(t *testing.T) {
	ctx := context.Background()
	const rangeSec = 30 * 24 * 3600
	sel := SelectTierForTest(t, rangeSec)
	if sel.Scale != 3 || sel.Resolution*sel.Scale != 900 {
		t.Fatalf("测试前提失效：30d 应为 Scale=3 / 有效桶宽 900, got Scale=%d step=%d",
			sel.Scale, sel.Resolution*sel.Scale)
	}

	// 造「DB 行」：30 天里每 300s 一行（8640 行），t 对齐原生栅格。
	now := time.Now().Unix()
	native := make([]agentmetrics.TrendPoint, 0, 9000)
	for i := int64(0); i < rangeSec/sel.Resolution; i++ {
		ts := now - rangeSec + i*sel.Resolution
		ts = ts / sel.Resolution * sel.Resolution
		// cpu 与 samples 都刻意不规律：等权时「加权平均 == 简单平均」，
		// 无法证明加权语义（fixture 必须让两者可区分）。
		cpu := float64((i * 37) % 100)
		samples := 30
		if i%3 == 1 {
			samples = 90
		}
		native = append(native, agentmetrics.TrendPoint{
			T: ts, Samples: samples, CPUUsedPercent: f64p(cpu), UptimeSec: i64p(i),
			MaxTemperatureC: f64p(cpu / 2),
		})
	}
	if len(native) < int(maxBucketsForTest) {
		t.Fatalf("测试前提失效：原生行数 %d 必须超过 %d 才能证明升档生效", len(native), maxBucketsForTest)
	}

	// 期望值**由数据算出**（不假设「最后一组恰好 3 行」或「range 末尾整除步长」：
	// 那类假设会让测试随取数时刻漂移）。分组只由 t 决定，与实现同一条规则。
	stepWant := sel.Resolution * sel.Scale
	groupSum := map[int64]float64{}
	groupPlain := map[int64]float64{} // 简单平均的分子（用于证明加权 ≠ 简单平均）
	groupWeight := map[int64]float64{}
	groupSamples := map[int64]int{}
	groupRows := map[int64]int{}
	for _, p := range native {
		k := p.T / stepWant * stepWant
		w := float64(p.Samples)
		if w <= 0 {
			w = 1
		}
		groupSum[k] += *p.CPUUsedPercent * w
		groupPlain[k] += *p.CPUUsedPercent
		groupWeight[k] += w
		groupSamples[k] += p.Samples
		groupRows[k]++
	}
	// 选目标组：必须**稳在窗口内**（range 两端各有一个可能被 from/to 裁掉的残缺组）
	// 且「加权 ≠ 简单平均」（否则断言无法区分两种语义）。选差值最大的一组，
	// 断言因此不随取数时刻漂移。
	var targetKey int64
	var bestGap float64 = -1
	for k, rows := range groupRows {
		if rows < 2 || k < now-rangeSec+2*stepWant || k > now-2*stepWant {
			continue
		}
		gap := math.Abs(groupSum[k]/groupWeight[k] - groupPlain[k]/float64(rows))
		if gap > bestGap {
			bestGap, targetKey = gap, k
		}
	}
	if bestGap <= 1e-9 {
		t.Fatalf("测试前提失效：窗口内没有任何「加权 ≠ 简单平均」的组（bestGap=%v）", bestGap)
	}
	wantCPU := groupSum[targetKey] / groupWeight[targetKey]
	wantSamples := groupSamples[targetKey]

	reader := &stubMetricReader{trend: native}
	svc := NewAgentMetricsQueryService(nil, reader, nil, nil, logger.NewNop())
	resp, err := svc.Metrics(ctx, 1001, &request.DeviceMetricsQuery{Range: rangeSec})
	if err != nil {
		t.Fatalf("30d 查询: %v", err)
	}

	if resp.ResolutionSeconds != 900 {
		t.Fatalf("resolution_seconds = %d, want 900", resp.ResolutionSeconds)
	}
	step := resp.ResolutionSeconds
	if len(resp.Buckets) > int(maxBucketsForTest) {
		t.Fatalf("30d 返回 %d 个桶，超过上限 %d（升档没落到数据上）", len(resp.Buckets), maxBucketsForTest)
	}
	if len(resp.Buckets) >= len(native) {
		t.Fatalf("返回桶数 %d 与原生行数 %d 相同 —— 根本没有归并", len(resp.Buckets), len(native))
	}
	for i, b := range resp.Buckets {
		if b.T%step != 0 {
			t.Fatalf("第 %d 桶 t=%d 未对齐到 %d 栅格（resolution_seconds 与数据必须一致）", i, b.T, step)
		}
	}
	// 加权语义：该 900s 组内全部原生行按 samples 加权
	var target *response.DeviceMetricPoint
	for i := range resp.Buckets {
		if resp.Buckets[i].T == targetKey {
			target = &resp.Buckets[i]
			break
		}
	}
	if target == nil {
		t.Fatalf("必须产出对齐 %d 的归并桶（归并到同一 900s 组的行落进同一个桶）", targetKey)
	}
	if target.CPUUsedPercent == nil {
		t.Fatal("目标桶的 cpu 不得为 nil")
	}
	if math.Abs(*target.CPUUsedPercent-wantCPU) > 0.01 {
		t.Fatalf("桶 t=%d 的 cpu = %v, want %v（该组 %d 行按 samples 加权）",
			targetKey, *target.CPUUsedPercent, wantCPU, groupRows[targetKey])
	}
	if target.Samples != wantSamples {
		t.Fatalf("桶 t=%d 的 samples = %d, want %d（组内求和）", targetKey, target.Samples, wantSamples)
	}
	// 反向对照：本组的**简单平均**与期望值不同（证明上面比的是加权平均，
	// 而不是碰巧等于简单平均）。若实现退化成简单平均，这条会立刻红灯。
	plain := groupPlain[targetKey] / float64(groupRows[targetKey])
	if math.Abs(wantCPU-plain) <= 1e-9 {
		t.Fatal("测试前提失效：本组的加权均值与简单均值相同")
	}
	if math.Abs(*target.CPUUsedPercent-plain) <= 1e-9 {
		t.Fatalf("桶 t=%d 的 cpu = %v 等于**简单平均**（差值 %v 的那一版才是加权）",
			targetKey, *target.CPUUsedPercent, bestGap)
	}
	// 加权只有在 samples 进了投影时才成立：默认列集不含 samples（它是完整度信号，
	// 不是图表系列），故升档时 service 必须**隐式补投影** samples；
	// 而 available_metrics 仍只回请求的那套列（不得把内部补的列谎报成响应可用列）。
	if !slices.Contains(reader.gotCols, "samples") {
		t.Fatalf("升档时必须隐式补投影 samples（否则加权退化成等权）: got %v", reader.gotCols)
	}
	if slices.Contains(resp.AvailableMetrics, "samples") {
		t.Fatalf("available_metrics 只回请求列（默认列集不含 samples）: %v", resp.AvailableMetrics)
	}
}

// TestMetricsTierLiftMergeEndToEnd：S2 的端到端版（真库 → 服务 → 响应）。
// 与上一条互补：这条证明「投影列 → GORM 扫描 → Go 侧归并 → 响应」整条链路
// 的栅格与语义一致（桩测不到列投影与扫描阶段的偏差）。
func TestMetricsTierLiftMergeEndToEnd(t *testing.T) {
	ctx := context.Background()
	repo := newQueryTestRepo(t)
	now := time.Now().Unix()
	// 三个原生 300s 桶，落在**同一个** 900s 有效桶里；cpu 10/20/30、samples 30/30/60
	base := (now - 600) / 900 * 900
	for i, cpu := range []float64{10, 20, 30} {
		samples := 30
		if i == 2 {
			samples = 60
		}
		if err := repo.WriteBucket(ctx, &entity.DeviceMetricWide{
			DeviceID: 1001, BucketTS: base + int64(i)*300,
			CPUUsedPercent: f64p(cpu), Samples: samples,
		}, repository.MetricSubRows{}); err != nil {
			t.Fatal(err)
		}
	}

	svc := NewAgentMetricsQueryService(nil, repo, nil, nil, logger.NewNop())
	resp, err := svc.Metrics(ctx, 1001, &request.DeviceMetricsQuery{Range: 30 * 24 * 3600})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ResolutionSeconds != 900 {
		t.Fatalf("resolution_seconds = %d, want 900", resp.ResolutionSeconds)
	}
	var merged *response.DeviceMetricPoint
	for i := range resp.Buckets {
		if resp.Buckets[i].T == base {
			merged = &resp.Buckets[i]
		}
	}
	if merged == nil {
		t.Fatalf("必须产出对齐 %d 的归并桶, got %+v", base, resp.Buckets)
	}
	if merged.T%900 != 0 {
		t.Fatalf("归并桶 t=%d 未对齐 900", merged.T)
	}
	// (10*30 + 20*30 + 30*60) / 120 = 22.5（简单平均会是 20）
	if merged.CPUUsedPercent == nil {
		t.Fatal("归并桶的 cpu 不得为 nil")
	}
	if math.Abs(*merged.CPUUsedPercent-22.5) > 0.01 {
		t.Fatalf("归并 cpu = %v, want 22.5（(300+600+1800)/120 加权；简单平均 20 是错的）",
			*merged.CPUUsedPercent)
	}
	if merged.Samples != 120 {
		t.Fatalf("归并 samples = %d, want 120", merged.Samples)
	}
}

// TestStepLiftScaleIsMinimal：H5 —— 升档倍数必须是**最小**的整数倍
// （既不越上限，也不多升一档把数据抹平）。
func TestStepLiftScaleIsMinimal(t *testing.T) {
	for _, r := range []int64{3600, 86400, 86401, 604800, 2592000, 2592001, 7776000, 15552000} {
		sel := SelectTierForTest(t, r)
		if n := sel.BucketCount(); n > maxBucketsForTest {
			t.Fatalf("range=%d: 桶数 %d 超过上限 %d", r, n, maxBucketsForTest)
		}
		if sel.Scale <= 1 {
			continue
		}
		// 少升一档就必须越上限，否则本可以返回更细的栅格
		prev := sel
		prev.Scale--
		if prev.BucketCount() <= maxBucketsForTest {
			t.Fatalf("range=%d: Scale=%d 不是最小整数倍（Scale=%d 时桶数 %d 已在上限 %d 内）",
				r, sel.Scale, prev.Scale, prev.BucketCount(), maxBucketsForTest)
		}
	}
}

// ---------- C3：表名的单一枚举源 ----------

// TestServiceTableNamesMatchEntityConstants：C3 —— service 用到的表名必须**等于**
// entity.TableNameMetric* 常量集合（不是「看起来一样」，是同一个值）。
func TestServiceTableNamesMatchEntityConstants(t *testing.T) {
	want := map[string]bool{
		entity.TableNameMetric5m:     true,
		entity.TableNameMetric1h:     true,
		entity.TableNameMetricDisk:   true,
		entity.TableNameMetricDiskIO: true,
		entity.TableNameMetricNIC:    true,
		entity.TableNameMetricSensor: true,
	}

	got := map[string]bool{}
	// 选档：三档的表名
	for _, r := range []int64{3600, 86401, 2592001} {
		sel := SelectTierForTest(t, r)
		if sel.Table == "" {
			continue // Redis 档没有表
		}
		got[sel.Table] = true
		if !want[sel.Table] {
			t.Fatalf("SelectTier(%d).Table = %q 不是 entity 的指标表常量", r, sel.Table)
		}
	}
	// 列集解析用的表（含 Redis 档回落到 _5m）
	if tb := trendColumnsTable(SelectTierForTest(t, 3600)); tb != entity.TableNameMetric5m {
		t.Fatalf("Redis 档的列集表名 = %q, want %q", tb, entity.TableNameMetric5m)
	}
	// 下钻：4 张子表
	for _, kind := range agentmetrics.ResourceKinds() {
		tb, ok := resourceTable(kind)
		if !ok {
			t.Fatalf("kind %q 未映射到子表", kind)
		}
		got[tb] = true
		if !want[tb] {
			t.Fatalf("resourceTable(%q) = %q 不是 entity 的指标表常量", kind, tb)
		}
	}
	// 集合相等：6 张表全部被 service 用到（少一张说明映射漏了，多一张说明越界）
	if len(got) != len(want) {
		t.Fatalf("service 用到的表名集合 = %v（%d 个）, want %v（%d 个）", got, len(got), want, len(want))
	}
}

// TestServiceHasNoHardcodedMetricTableNames：C3 —— service 的**生产文件**里不得再
// 出现引号包裹的表名字面量（注释与测试文件不算：注释里引用表名是文档，测试里
// 出现表名是夹具）。
//
// 为什么值得一条守卫：本文件原来自称「表名常量（唯一枚举源，禁止在别处硬编码
// 表名）」，却在同一文件里硬编码了 6 张表、约 10 处 —— 注释与代码互相矛盾，
// 且任何表名变更都会漏改。守卫扫描的是**同一目录下的生产 .go 文件**，
// 针（needle）由 entity 常量现算，故守卫自身也不含表名字面量。
func TestServiceHasNoHardcodedMetricTableNames(t *testing.T) {
	tables := []string{
		entity.TableNameMetric5m, entity.TableNameMetric1h, entity.TableNameMetricDisk,
		entity.TableNameMetricDiskIO, entity.TableNameMetricNIC, entity.TableNameMetricSensor,
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读取包目录: %v", err)
	}
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("读取 %s: %v", name, err)
		}
		scanned++
		for _, tb := range tables {
			if strings.Contains(string(b), `"`+tb+`"`) {
				t.Fatalf("%s 里出现硬编码表名 %q —— 必须改用 entity 常量（C3）", name, tb)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("没有扫描到任何生产文件，守卫会空转通过（vacuous）")
	}
	t.Logf("已扫描 %d 个生产文件，无硬编码表名", scanned)
}

// TestUserFacingErrorsSpeakHuman 守卫：这几条 400 的 msg 会被前端**原样**显示在
// 页面上（uni_console/src/modules/device/utils/error.ts 把 badRequest 的 msg 当
// 标题展示），故消息里不得出现参数名、原始秒数、表名与响应模型名 —— 那些是实现的
// 语言，而读者要的是「能看多远、怎么改」。
//
// 与前端 `__tests__/copy-no-internals.test.ts` 同一口径：那边扫页面文案，这边扫
// 后端消息文本；两处都留一条守卫，是因为文案会**从两边长出来**（前端的提示与后端
// 的报错最终显示在同一个位置）。
func TestUserFacingErrorsSpeakHuman(t *testing.T) {
	forbidden := []string{"range", "秒之间", "device_metric", "5min", "TrendPoint", "spec §", "白名单"}

	assertHuman := func(what, msg string) {
		t.Helper()
		for _, bad := range forbidden {
			if strings.Contains(msg, bad) {
				t.Fatalf("%s 的消息「%s」含内部术语 %q（该消息会原样出现在页面上）", what, msg, bad)
			}
		}
	}

	// 出口 1：时间范围越界（消息里写「1 小时 ~ 180 天」，不写 [3600, 15552000] 秒）
	_, err := SelectTier(1, 30)
	assertBadRequest(t, "range=1 越界", err)
	assertHuman("时间范围越界", err.Error())

	// 出口 2～4：列不可用的三条分支（5min-only 列落在 1h 档 / 表里有但响应装不下 /
	// 列名非法）。四个列名覆盖三条分支：前两个只存在于 5min 档、cpu_used_percent 在
	// 两档的表里都有、not_a_column 谁都没有。
	oneHour := TierSelection{Source: SourceDB, Table: entity.TableNameMetric1h, RangeSeconds: 40 * 86400}
	fiveMin := TierSelection{Source: SourceDB, Table: entity.TableNameMetric5m, RangeSeconds: 7 * 86400}
	for _, col := range []string{"tcp_time_wait", "agent_mem_resident_mb", "cpu_used_percent", "not_a_column"} {
		assertHuman("1h 档的列 "+col, unavailableColumnReason(oneHour, col))
		assertHuman("5min 档的列 "+col, unavailableColumnReason(fiveMin, col))
	}

	// humanSpan 必须与前端 formatDurationText 同口径：两边写法不一致（「1 小时」与
	// 「3600 秒」）会让人以为是两个不同的限制。
	for _, c := range []struct {
		sec  int64
		want string
	}{{3600, "1 小时"}, {180 * 86400, "180 天"}, {900, "15 分钟"}, {45, "45 秒"}} {
		if got := humanSpan(c.sec); got != c.want {
			t.Fatalf("humanSpan(%d) = %q，期望 %q", c.sec, got, c.want)
		}
	}
}
