package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"

	"go.uber.org/zap"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agentmetrics"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
)

// 数据源与档位常量。
//
// 表名**不在本文件里字面量出现**（C3）：单一枚举源是 model/entity 的
// entity.TableNameMetric*（Plan 2A 建立，与 DDL、分区策略同源）。
// 守卫见 TestServiceHasNoHardcodedMetricTableNames（扫描本包生产文件里
// 是否还有引号包裹的表名字面量）以及 TestServiceTableNamesMatchEntityConstants。
const (
	SourceRedis = "redis"
	SourceDB    = "db"

	// MaxBuckets 是响应桶数上限；超过时按整数倍升 step（不跨档、不混档）。
	MaxBuckets int64 = 4000

	// RedisNativeResolutionSec 是 Redis 档原生栅格的**缺省值**（秒）—— 仅当
	// RedisIntervalPolicy 缺失（装配漏参）时使用；**实际栅格以 policy 为准**
	// （policy 的值来自配置项 sys.agent.reportInterval，与 RawStore 的 Step 同源）。
	//
	// 本常量**不得**再被 SelectTier 直接读取：SelectTier 的原生栅格是入参，
	// 读常量会让「改配置」对查询侧无效 —— 那正是本任务修掉的缺陷（运维把
	// reportInterval 改成 5s/30s 后桶变稀疏且不报错）。
	RedisNativeResolutionSec int64 = 10

	// minRedisNativeResolutionSec 是 spec 规定的上报间隔下限（秒）。
	// 低于它的配置值（含 0/负值）一律夹到这里：栅格 0 会让 BucketCount 除零/Inf。
	minRedisNativeResolutionSec int64 = 2
)

// TierSelection 是「range → 数据源 + 表 + 分辨率」的选档结果。
//
// 把它做成**单一函数**且只允许一个调用点，是为了让「跨档混用」在代码层面无法表达
// （spec §7.2）：range 是单值，选档是纯函数，不存在拼接两条不同分辨率曲线的路径。
type TierSelection struct {
	Source string
	// Table 仅在 Source == SourceDB 时有意义（6 张指标表之一）。
	Table string
	// Resolution 是**原生**桶宽（秒）：Redis 用 reportInterval，DB 用 300/3600。
	Resolution int64
	// Scale 是升档倍数（>=1）；有效桶宽 = Resolution × Scale。
	Scale int64
	// RangeSeconds 是实际窗口。
	RangeSeconds int64
}

// BucketCount 返回升档后的桶数。
func (t TierSelection) BucketCount() int64 {
	step := t.Resolution * t.Scale
	if step <= 0 {
		return 0
	}
	return int64(math.Ceil(float64(t.RangeSeconds) / float64(step)))
}

// SelectTier 把 range（秒）映射到单档数据源。
//
// 规则（spec §7.2）：
//   - range ≤ 24h          → Redis 原始（原生栅格 = redisNativeSec，即配置的上报间隔）
//   - 24h < range ≤ 30d    → device_metric_5m（300s）
//   - 30d < range ≤ 180d   → device_metric_1h（3600s）
//   - 越界                  → BadRequest
//
// 跨档**绝不混用**：40d 整体走 1h 档，而不是「前 30d 用 5min + 后 10d 用 1h」。
//
// redisNativeSec 只影响 Redis 档：DB 两档的栅格由**表结构**决定（5min 表一行就是
// 5 分钟、1h 表一行就是 1 小时），把配置值渗进 DB 档会把冷层的真实栅格也报错 ——
// 不可查询却可校验的值。
func SelectTier(rangeSec int64, redisNativeSec int64) (TierSelection, error) {
	if rangeSec < request.DeviceRangeMin || rangeSec > request.DeviceRangeMax {
		return TierSelection{}, apperror.BadRequest(fmt.Sprintf("range 必须在 [%d, %d] 秒之间",
			request.DeviceRangeMin, request.DeviceRangeMax))
	}

	const day = int64(24 * 3600)
	sel := TierSelection{RangeSeconds: rangeSec, Scale: 1}

	switch {
	case rangeSec <= day:
		sel.Source = SourceRedis
		sel.Resolution = clampRedisNativeResolution(redisNativeSec, rangeSec)
	case rangeSec <= 30*day:
		sel.Source = SourceDB
		sel.Table = entity.TableNameMetric5m
		sel.Resolution = 300
	default:
		sel.Source = SourceDB
		sel.Table = entity.TableNameMetric1h
		sel.Resolution = 3600
	}

	// 桶数超上限 → 整数倍升 step（仍是原生 step 的整数倍，桶边界仍对齐）。
	//
	// 直算而非 while 自增（H5）：满足 ceil(Range/(Resolution×Scale)) ≤ MaxBuckets
	// 的最小整数倍就是 ceil(Range / (Resolution × MaxBuckets)) —— 与原来的循环
	// 等价（对整数上限 M：ceil(x) > M ⟺ x > M），但边界一眼可见、无循环。
	if k := ceilDiv(sel.RangeSeconds, sel.Resolution*MaxBuckets); k > 1 {
		sel.Scale = k
	}
	return sel, nil
}

// ceilDiv 是正整数的向上取整除法（a、b 均 > 0）。
func ceilDiv(a, b int64) int64 {
	if b <= 0 {
		return 1
	}
	return (a + b - 1) / b
}

// clampRedisNativeResolution 把「配置来的上报间隔」夹到可用的原生栅格区间。
//
// 两条夹取规则的理由（均来自**实测可验证的后果**，不是风格偏好）：
//
//  1. `< 2 → 2`：spec 规定上报间隔最小 2s。低于它的值（0、负值、以及被截成 0 的
//     亚秒配置）必须夹到 2 —— 栅格 0 会让 `BucketCount()` 的 `step <= 0` 分支直接
//     返回 0 桶，热层也会按 0 步长分桶：响应是一条恒空的曲线，而整条链路上没有
//     任何报错。
//
//  2. `> rangeSec → rangeSec`：栅格不得大于时间窗。热层的分桶器
//     `metricshistory.AlignBuckets(window, step)` **明确拒绝** `step > window`
//     （buckets.go：「metricshistory: step 不得超过 window」），而趋势查询正是把
//     `step = Resolution×Scale`、`window = RangeSeconds` 交给它。所以不夹取时，
//     「配置间隔 > 查询窗口」会以 `apperror.Internal` 变成一条 **500 内部错误**
//     （实测：`AlignBuckets(1h, 2h)` 返回的正是上面那条错误），整条趋势查询直接
//     不可用 —— 不是「0 桶」。夹到窗口长度后 `Resolution == RangeSeconds` →
//     恰好 1 个桶：查询正常返回，且 `BucketCount() >= 1` 恒成立。
//
// 两条规则都**只**作用于 Redis 档：DB 档的 300/3600 由表结构决定，不走本函数。
func clampRedisNativeResolution(redisNativeSec, rangeSec int64) int64 {
	if redisNativeSec < minRedisNativeResolutionSec {
		return minRedisNativeResolutionSec
	}
	if redisNativeSec > rangeSec {
		return rangeSec
	}
	return redisNativeSec
}

// AgentMetricsQueryService 提供趋势与下钻查询。
type AgentMetricsQueryService struct {
	// raw 是整机趋势用的热层能力面（聚合查询）。
	raw AgentRawQuerier
	// rawBucket 是下钻用的热层能力面（原始样本直读，D7）—— 刻意与 raw 分成两个
	// 窄接口：下钻只需要「按时间窗取原始样本」，不需要聚合查询。
	rawBucket AgentRawBucketReader
	metrics   DeviceMetricReader
	resources DeviceResourceResolver
	// policy 提供 Redis 档的原生栅格（= agent 的上报节奏）。可为 nil：装配漏参时
	// 退化成 RedisNativeResolutionSec 并记 Warn（见 redisIntervalSec）。
	policy RedisIntervalPolicy
	log    logger.LoggerInterface
}

// RedisIntervalPolicy 是本服务需要的上报节奏能力面（接口定义在**消费方**）。
//
// 为什么只声明 ReportInterval：Redis 档的原生栅格就是 agent 的上报间隔本身
// —— RawStore 的 Step 也取自同一配置（sys.agent.reportInterval）。查询侧按别的
// 粒度聚合就是「栅格与数据错位」：桶变稀疏、且不报错。
//
// 为什么不直接收 AgentConfigGetter：那会把配置**键名与缺省值**的知识复制到查询
// 服务里（还有「非正值怎么退化」的第二套规则）。键名/缺省/退化是适配器的职责，
// 查询服务只需要「秒数」这一个事实。
type RedisIntervalPolicy interface {
	ReportInterval() time.Duration
}

// AgentRawQuerier 是本服务用到的热层聚合能力面。
type AgentRawQuerier interface {
	Query(ctx context.Context, deviceID uint64, window, step time.Duration) (*agentmetrics.RawSnapshot, error)
}

// AgentRawBucketReader 是本服务用到的热层原始读能力面（下钻 ≤24h 用，D7）。
type AgentRawBucketReader interface {
	Bucket(ctx context.Context, deviceID uint64, fromMs, toMs int64) ([]agentproto.MetricsSample, error)
}

// AgentRawReader 是构造参数面：热层实现（*agentmetrics.RawStore）同时具备两面。
// 用组合而不是把两个方法塞进一个接口，是为了让两条路径各自只看得见自己需要的面。
// （名字不能叫 AgentRawStore —— 那个已被 agent_ingest.go 的写入面占用。）
type AgentRawReader interface {
	AgentRawQuerier
	AgentRawBucketReader
}

// DeviceMetricReader 是冷层读取面（列白名单由仓储内部校验）。
//
// 下钻只保留 ReadResourceRows（开放形状）：子表列名与 TrendPoint 字段不同名，
// 类型化扫描会让下钻的值列静默全为 nil（D2）。仓储里旧的 ReadResourceTrendPoints
// 已删除（H2）：它在下钻改用 ReadResourceRows 后没有任何生产调用方，
// 留着只会让「下钻该走哪条读取路径」出现两个似是而非的答案。
type DeviceMetricReader interface {
	ReadTrendPoints(ctx context.Context, table string, deviceID uint64, from, to int64, columns []string) ([]agentmetrics.TrendPoint, error)
	ReadResourceRows(ctx context.Context, table string, resourceID uint64, from, to int64) ([]map[string]any, error)
}

// DeviceResourceResolver 是资源维度面。
type DeviceResourceResolver interface {
	ResolveID(ctx context.Context, deviceID uint64, kind, name string) (uint64, error)
}

func NewAgentMetricsQueryService(raw AgentRawReader, metrics DeviceMetricReader,
	resources DeviceResourceResolver, policy RedisIntervalPolicy,
	log logger.LoggerInterface) *AgentMetricsQueryService {
	// 同一实现拆成两个窄面（raw 为 nil 时两面都为 nil）。
	var q AgentRawQuerier = raw
	var b AgentRawBucketReader = raw
	return &AgentMetricsQueryService{
		raw: q, rawBucket: b, metrics: metrics, resources: resources, policy: policy, log: log,
	}
}

// redisIntervalSec 返回 Redis 档的原生栅格（秒）—— 来源是**配置**的上报间隔
// （policy.ReportInterval()），不再是硬编码。
//
// policy 为 nil（装配漏参）时退化成 RedisNativeResolutionSec 并记一条 Warn：
// 少接一个构造参数不该把接口打成 500（不 panic），但也绝不能静默 ——
// 「配置改了、栅格没变」正是本任务要消灭的那种无迹可查的错位。
//
// 非法值在此**不**夹取：夹取统一在 SelectTier（clampRedisNativeResolution）里做，
// 这样「什么值算合法栅格」只有一个定义点，本函数只负责「取到配置的秒数」。
func (s *AgentMetricsQueryService) redisIntervalSec() int64 {
	if s.policy == nil {
		if s.log != nil {
			s.log.Warn("agent metrics query: RedisIntervalPolicy 未注入，Redis 档栅格退化为默认值",
				zap.Int64("defaultResolutionSec", RedisNativeResolutionSec))
		}
		return RedisNativeResolutionSec
	}
	return int64(s.policy.ReportInterval() / time.Second)
}

// defaultMetricColumns 是趋势默认投影列（图表默认系列，约 10 列）。
// 45 列全量扫描有 ~4~5× 读放大，白名单是**默认行为**而非可选优化（spec §7.2）。
var defaultMetricColumns = []string{
	"bucket_ts", "cpu_used_percent", "load1", "mem_used_percent",
	"disk_used_percent", "nic_rx_bytes_sec", "nic_tx_bytes_sec", "max_temperature_c",
}

// Metrics 返回整机趋势。
func (s *AgentMetricsQueryService) Metrics(ctx context.Context, deviceID uint64,
	q *request.DeviceMetricsQuery) (*response.DeviceMetricsResp, error) {
	if q == nil {
		return nil, apperror.BadRequest("缺少查询参数")
	}
	if (q.Kind == "") != (q.Name == "") {
		return nil, apperror.BadRequest("kind 与 name 必须成对出现")
	}
	if q.Kind != "" {
		return nil, apperror.BadRequest("整机趋势不接受 kind/name，请用资源下钻接口")
	}
	rangeSec := q.Range
	if rangeSec == 0 {
		rangeSec = 24 * 3600
	}
	sel, err := SelectTier(rangeSec, s.redisIntervalSec())
	if err != nil {
		return nil, err
	}

	now := time.Now().Unix()
	from := now - sel.RangeSeconds

	cols, err := resolveTrendColumns(sel, q.Metrics)
	if err != nil {
		return nil, err
	}

	if sel.Source == SourceRedis {
		snap, err := s.raw.Query(ctx, deviceID, time.Duration(sel.RangeSeconds)*time.Second,
			time.Duration(sel.Resolution*sel.Scale)*time.Second)
		if err != nil {
			return nil, apperror.Internal("内部错误", err)
		}
		return &response.DeviceMetricsResp{
			RangeSeconds:      sel.RangeSeconds,
			ResolutionSeconds: sel.Resolution * sel.Scale,
			Source:            SourceRedis,
			AvailableMetrics:  cols,
			Buckets:           toMetricPoints(snap.Buckets),
		}, nil
	}

	// 升档（Scale > 1）必须在 Go 侧**真正落到数据栅格上**（S2）：
	// DB 里的行是**原生**步长（30d@5min = 8640 行），若原样返回，响应就同时
	// 违反了两条契约 —— resolution_seconds 报 900 而数据是 300 的栅格，
	// 且 4000 桶上限形同虚设。逐列聚合语义不同（加权均值 / LAST / MAX），
	// 故不在 SQL 里 GROUP BY，而是用纯函数归并（见 agentmetrics.MergeTrendPoints）。
	if sel.Scale > 1 {
		// samples 是加权平均的权重来源，但它是**完整度信号**而不是图表系列：
		// 默认列集（趋势图默认系列）不含它，消费方通常也不请求它。若投影里没有
		// samples，Samples 一律扫成 0 → 归并只能退化成等权平均（与「按 samples
		// 加权」的契约不符）。故升档时**隐式补投影** samples：它是白名单内的真实
		// 列，投影不越界；而 available_metrics 仍只回请求的那套列（samples 不是
		// 本次响应的「可用指标」语义，除非调用方显式请求了它）。
		readCols := cols
		if !slices.Contains(cols, "samples") {
			readCols = append(append([]string(nil), cols...), "samples")
		}
		rows, err := s.metrics.ReadTrendPoints(ctx, sel.Table, deviceID, from, now, readCols)
		if err != nil {
			return nil, apperror.Internal("内部错误", err)
		}
		rows = agentmetrics.MergeTrendPoints(rows, sel.Resolution*sel.Scale)
		return &response.DeviceMetricsResp{
			RangeSeconds:      sel.RangeSeconds,
			ResolutionSeconds: sel.Resolution * sel.Scale,
			Source:            SourceDB,
			AvailableMetrics:  cols,
			Buckets:           toMetricPoints(rows),
		}, nil
	}

	rows, err := s.metrics.ReadTrendPoints(ctx, sel.Table, deviceID, from, now, cols)
	if err != nil {
		return nil, apperror.Internal("内部错误", err)
	}
	return &response.DeviceMetricsResp{
		RangeSeconds:      sel.RangeSeconds,
		ResolutionSeconds: sel.Resolution * sel.Scale,
		Source:            SourceDB,
		AvailableMetrics:  cols,
		Buckets:           toMetricPoints(rows),
	}, nil
}

// resolveTrendColumns 解析整机趋势的投影列（spec §8）。
//   - 空   → 该档位的默认列集（defaultMetricColumns）
//   - "*"  → 该档位的**可用列集**（`tierAvailableColumns`，D4 的收窄版）
//   - CSV  → 逐列白名单校验，非法列一律 **400**（**两档同口径**，S4）
//
// **恒补 bucket_ts**（D5）：`t` 是契约的一部分（spec §8「必须用 t 定位」），
// 不是可选列 —— 显式只请求 cpu_used_percent 时若不补 bucket_ts，投影里就没有
// bucket_ts，扫出来的每个点 t 都是 0（实测），前端按 t 定位时间全落在 1970。
//
// 为什么显式列也必须在此校验（S4）：Redis 档此前**完全不校验**——任意列名都返回
// 200 并写进 `available_metrics`，前端会据此画一条恒为空的曲线；同一参数在 DB 档
// 则被仓储白名单拒成 **500**。两档口径必须一致，且都是**400（参数错误）**——
// 与下钻（`resolveDrillColumns`）的错误口径相同。
// resolveTrendColumns 解析趋势的列投影。
//
// **自由函数**（而非 AgentMetricsQueryService 的方法）：总览服务
// （device_overview.go）必须用**逐字相同**的解析规则，否则「详情页能查的列
// 总览查不了」或反之 —— 这类漂移的根源总是「同一个决策被实现了两遍」。
// 本函数不读接收者上的任何状态（只读 sel），故提升为自由函数无副作用。
//
//   - 空   → 该档位的默认列集（defaultMetricColumns）
//   - "*"  → 该档位的可用列集（tierAvailableColumns）
//   - CSV  → 逐列白名单校验，非法列一律 400（两档同口径）
//
// 返回值恒含 bucket_ts（键列，见 D5），且恒在首位。
func resolveTrendColumns(sel TierSelection, metrics string) ([]string, error) {
	switch metrics {
	case "":
		return withBucketTS(defaultMetricColumns), nil
	case request.MetricsAll:
		available, err := tierAvailableColumns(sel)
		if err != nil {
			return nil, err
		}
		return withBucketTS(available), nil
	default:
		cols := splitCSV(metrics)
		available, err := tierAvailableColumns(sel)
		if err != nil {
			return nil, err
		}
		for _, c := range cols {
			if c == "bucket_ts" {
				continue // 键列：t 是契约的一部分，恒补且总是合法
			}
			if slices.Contains(available, c) {
				continue
			}
			return nil, apperror.BadRequest(unavailableColumnReason(sel, c))
		}
		return withBucketTS(cols), nil
	}
}

// tierAvailableColumns 返回该档位**真正产得出值**的值列集：
//
//	该表的可查询列（repository.MetricQueryColumns）
//	  ∩ TrendPoint 实际承接的列（agentmetrics.TrendPointColumns）
//	  − 该档位刻意留 NULL 的 5min-only 列（1h 档）
//
// 收窄到 TrendPoint 承接列这一步是 S3 的核心：`available_metrics` 的设立理由
// （spec §8）就是消除「该列未采集」与「该档位根本不产该列」的误读，而此前它被
// 直接取成「该表的全部列」（5m 档 46 列），其中约 20 列（`tcp_time_wait`、
// `udp_total`、`*_ops_sec`、`nic_*_errors_sec`、9 个 `agent_*` …）**没有任何
// TrendPoint 字段去装**，恒为 nil —— 消费方仍然分不清两者，声明反而成了误导源。
//
// Redis 档按 _5m 解析（两者本就是「一套 schema 两处表名」）。
func tierAvailableColumns(sel TierSelection) ([]string, error) {
	table := trendColumnsTable(sel)
	all, err := repository.MetricQueryColumns(table)
	if err != nil {
		return nil, apperror.Internal("内部错误", err)
	}
	carried := make(map[string]bool, len(agentmetrics.TrendPointColumns()))
	for _, c := range agentmetrics.TrendPointColumns() {
		carried[c] = true
	}
	out := make([]string, 0, len(all))
	for _, c := range all {
		if !carried[c] {
			continue
		}
		out = append(out, c)
	}
	return tierValueColumns(table, out), nil
}

// unavailableColumnReason 解释某列为何不可用（S3/S4/D6），返回给客户端的 400 说明。
//
// 三种不可用的原因各自说清楚，**绝不静默给 nil、也绝不静默剔除**：
//  1. 该列只存在于 5min 档而被 1h 档请求（D6，spec §8 明文要求 400）；
//  2. 表里有该列、但响应模型（TrendPoint/DeviceMetricPoint）装不下它 —— 此前
//     显式请求它会得到一条**恒为空**的曲线，消费方无法与「未采集」区分（S3）；
//  3. 该表根本没有这个列（列名写错）——两档同口径 400（S4）。
func unavailableColumnReason(sel TierSelection, col string) string {
	table := trendColumnsTable(sel)
	if allowed, err := repository.MetricQueryColumns(table); err == nil {
		if sel.Source == SourceDB && sel.Table == entity.TableNameMetric1h && isFiveMinOnlyColumn(col) {
			return fmt.Sprintf(
				"列 %q 只存在于 5min 档（range > 30d 走 1h 档，该档不产出此列），请把 range 缩短到 30 天以内", col)
		}
		if slices.Contains(allowed, col) {
			return fmt.Sprintf(
				"列 %q 在 %s 中存在，但响应模型（agentmetrics.TrendPoint / DeviceMetricPoint）不承接该列，"+
					"显式请求只会得到恒为空的曲线 —— 本接口按 spec §8 的口径返回 400 而不是静默给 nil；"+
					"请从 metrics 中移除该列", col, table)
		}
	}
	return fmt.Sprintf("列 %q 不在 %s 的白名单内（列名非法或该表无此列）", col, table)
}

// trendColumnsTable 返回该档位用来解析列集的表名。
//
// Redis 档（≤24h）没有表名：热层原始样本的列集与 _5m 相同（_1h 与 _5m 本就是
// 「一套 schema 两处表名」，见 MetricColumnDDL），故一律按 _5m 解析。
func trendColumnsTable(sel TierSelection) string {
	if sel.Table == "" {
		return entity.TableNameMetric5m
	}
	return sel.Table
}

// withBucketTS 恒补 bucket_ts 并放在首位（D5）。**总是返回新切片**：
// 直接 append(defaultMetricColumns, ...) 会在容量有余时改写共享的默认列集。
func withBucketTS(cols []string) []string {
	out := make([]string, 0, len(cols)+1)
	out = append(out, "bucket_ts")
	for _, c := range cols {
		if c == "bucket_ts" {
			continue
		}
		out = append(out, c)
	}
	return out
}

// withoutBucketTS 去掉键列 bucket_ts，得到**纯值列**清单。
//
// 总览响应的 available_metrics 必须是纯值列：它是「本次响应里可画的系列」，
// 而 bucket_ts 由 axis 统一承载、不是可画系列。若把它混进去，前端会多出一条
// 恒为时间戳的「折线」，且 METRIC_META 里查不到它的中文名（显示为原始列名）。
// 与 withBucketTS 同样**总是返回新切片**（不得改写入参的底层数组）。
func withoutBucketTS(cols []string) []string {
	out := make([]string, 0, len(cols))
	for _, c := range cols {
		if c == "bucket_ts" {
			continue
		}
		out = append(out, c)
	}
	return out
}

// isFiveMinOnlyColumn 判断某列是否**只存在于 5min 档**。
//
// 依据：写路径的 1h 回滚刻意不给这些列赋值（rollup.go：「5min-only 列在 1h 行
// 刻意留 nil（tcp_time_wait / tcp_close_wait / agent_*）」），spec §8 明文列出同一集合。
func isFiveMinOnlyColumn(col string) bool {
	return col == "tcp_time_wait" || col == "tcp_close_wait" || strings.HasPrefix(col, "agent_")
}

// tierValueColumns 把「表的全量值列」收窄到「该档位真正产出的列」。
//
// 为什么 `metrics=*` 在 1h 档要减去 5min-only 列，而不是照样返回（也不是直接 400）：
// spec §8 要求 available_metrics 让前端区分「该列未采集」与「该档位根本不产该列」；
// 若把 1h 档恒为 nil 的 tcp_time_wait / agent_* 也报成「可用」，就退回了
// 「半年视图上这些列恒显示 —，被误读成 agent 挂了」的老问题。
// 而把 `*` 整个拒成 400 会让文档化的 metrics=* 在半年档完全不可用。
// 显式**点名**这些列仍然是 400（见 unavailableColumnReason）—— 这正是 D6 的语义。
//
// 注：经 S3 收窄后（可用列 = 表 ∩ TrendPoint 承接列），5min-only 列本就都不在
// TrendPoint 里，故这一层在当前 schema 下是**冗余的安全网** —— 保留它，
// 是为了 TrendPoint 未来承接这些列时「1h 档不产出」的口径不会失守。
func tierValueColumns(table string, cols []string) []string {
	if table != entity.TableNameMetric1h {
		return cols
	}
	out := make([]string, 0, len(cols))
	for _, c := range cols {
		if isFiveMinOnlyColumn(c) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// ResourceMetrics 返回单资源（磁盘/网卡/IO 设备/传感器）的下钻趋势。
//
// 两条路径（spec §7.2 选档表）：
//   - range ≤ 24h → 热层原始样本（RawStore.Bucket）+ 按 kind/name 过滤聚合（D7）
//   - 24h < range ≤ 30d → 子表（device_metric_disk/_diskio/_nic/_sensor）
//
// >30d → 400：子表只有 5min 档，没有 1h 明细。
func (s *AgentMetricsQueryService) ResourceMetrics(ctx context.Context, deviceID uint64,
	q *request.DeviceMetricsQuery) (*response.DeviceResourceResp, error) {
	if q == nil || q.Kind == "" || q.Name == "" {
		return nil, apperror.BadRequest("下钻必须同时提供 kind 与 name")
	}
	table, ok := resourceTable(q.Kind)
	if !ok {
		return nil, apperror.BadRequest("不支持的资源种类: " + q.Kind)
	}
	// D3：默认列集必须是**子表自己的列**（宽表默认列会被子表白名单拒绝 → 500）。
	cols, err := s.resolveDrillColumns(q.Kind, q.Metrics)
	if err != nil {
		return nil, err
	}
	rangeSec := q.Range
	if rangeSec == 0 {
		rangeSec = 24 * 3600
	}
	sel, err := SelectTier(rangeSec, s.redisIntervalSec())
	if err != nil {
		return nil, err
	}
	if sel.Source == SourceDB && sel.Table != entity.TableNameMetric5m {
		// 子表只有 5min 档；>30d 没有明细可查
		return nil, apperror.BadRequest("资源明细只保留 30 天（5min 档），请把 range 缩短到 30 天以内")
	}

	now := time.Now().Unix()
	resp := &response.DeviceResourceResp{
		RangeSeconds: sel.RangeSeconds, ResolutionSeconds: sel.Resolution * sel.Scale,
		Source: sel.Source, ResourceKind: q.Kind, Name: q.Name,
		AvailableMetrics: cols,
		Buckets:          []response.DeviceResourcePoint{},
	}

	// D7：≤24h 直读 Redis 原始样本并按 kind+name 过滤聚合。
	// 这里**不查 device_resource**：资源行由 flush 落库，刚挂载/刚重装的资源在
	// device_resource 里可能还没有行，但热层里已经有样本 —— 查维度表反而会把
	// 有数据的时间窗判成「资源不存在」。
	if sel.Source == SourceRedis {
		pts, err := s.rawBucket.Bucket(ctx, deviceID, (now-sel.RangeSeconds)*1000, now*1000)
		if err != nil {
			return nil, apperror.Internal("内部错误", err)
		}
		buckets := agentmetrics.AggregateResource(q.Kind, q.Name, pts, sel.Resolution*sel.Scale)
		resp.Buckets = toResourcePoints(buckets, cols)
		return resp, nil
	}

	resourceID, err := s.resources.ResolveID(ctx, deviceID, q.Kind, q.Name)
	if err != nil {
		// 未命中 → 返回空结果而不是 404：设备可能刚被卸载该资源。
		// 其它错误（DB 故障）必须上抛 500 —— 否则一次数据库抖动会显示成
		// 「该资源没有数据」，与 S5 是同一类错误遮蔽（静默的空曲线最难排查）。
		if errors.Is(err, repository.ErrNotFound) {
			return resp, nil
		}
		return nil, apperror.Internal("内部错误", err)
	}

	rows, err := s.metrics.ReadResourceRows(ctx, table, resourceID, now-sel.RangeSeconds, now)
	if err != nil {
		return nil, apperror.Internal("内部错误", err)
	}
	buckets, err := resourcePointsFromRows(rows, cols)
	if err != nil {
		return nil, apperror.Internal("内部错误", err)
	}
	resp.Buckets = buckets
	return resp, nil
}

// resolveDrillColumns 解析下钻的投影列（D3/D4）：
//   - 空 或 "*" → 子表的全部值列
//   - CSV       → 逐列校验（越界列 → 400，不得静默回空曲线）
//
// 为什么越界列是 400 而不是交给仓储报错：下钻读取**不做投影**（SELECT *），
// 仓储那层没有白名单校验的机会 —— 列名打错会静默返回一条空曲线。
func (s *AgentMetricsQueryService) resolveDrillColumns(kind, metrics string) ([]string, error) {
	all, err := defaultDrillColumns(kind)
	if err != nil {
		return nil, err
	}
	if metrics == "" || metrics == request.MetricsAll {
		return all, nil
	}
	allowed := make(map[string]bool, len(all))
	for _, c := range all {
		allowed[c] = true
	}
	cols := splitCSV(metrics)
	for _, c := range cols {
		if !allowed[c] {
			return nil, apperror.BadRequest(fmt.Sprintf("列 %q 不属于 %s 的明细列集", c, kind))
		}
	}
	return cols, nil
}

// defaultDrillColumns 按 kind 返回该子表的**全部值列**（D3）。
func defaultDrillColumns(kind string) ([]string, error) {
	table, ok := resourceTable(kind)
	if !ok {
		return nil, apperror.BadRequest("不支持的资源种类: " + kind)
	}
	cols, ok := repository.ResourceTableColumns(table)
	if !ok {
		return nil, apperror.Internal("内部错误",
			fmt.Errorf("agentmetrics: 子表 %q 未登记下钻列集", table))
	}
	out := make([]string, len(cols))
	copy(out, cols)
	return out, nil
}

// toResourcePoints 把热层聚合结果转成响应形状，并按请求列集过滤 map 键。
// 返回的切片**永不 nil**（空结果必须是 `[]`，前端与测试都依赖这一点）。
func toResourcePoints(in []agentmetrics.ResourcePoint, cols []string) []response.DeviceResourcePoint {
	out := make([]response.DeviceResourcePoint, 0, len(in))
	for _, p := range in {
		dp := response.DeviceResourcePoint{T: p.T, Samples: p.Samples}
		vals := make(map[string]*float64, len(cols))
		for _, c := range cols {
			if v, ok := p.Values[c]; ok && v != nil {
				vals[c] = v
			}
		}
		if len(vals) > 0 {
			dp.Values = vals
		}
		out = append(out, dp)
	}
	return out
}

// resourcePointsFromRows 把子表的开放形状行转成响应形状（D2）。
//
// 归一只有一个入口：toFloat64Ptr(any)。子表列名没有对应的 Go 结构体，
// 驱动对同一张表的不同列可能给出 int64 / float64 / []byte / string / nil，
// 逐字段类型断言正是「下钻值列全为 nil」这类静默故障的来源。
//
// 行的 `t` 缺失/无法解析 → 报错而不是留 0：t 是契约的一部分（spec §8），
// 一排 t=0 的桶会让前端把整条曲线画在 1970。
func resourcePointsFromRows(rows []map[string]any, cols []string) ([]response.DeviceResourcePoint, error) {
	out := make([]response.DeviceResourcePoint, 0, len(rows))
	for _, row := range rows {
		ts := toFloat64Ptr(row["t"])
		if ts == nil {
			return nil, fmt.Errorf("agentmetrics: 下钻行缺少可解析的 t（bucket_ts AS t）: %v", row)
		}
		dp := response.DeviceResourcePoint{T: int64(*ts)}
		vals := make(map[string]*float64, len(cols))
		for _, c := range cols {
			if v := toFloat64Ptr(row[c]); v != nil {
				vals[c] = v
			}
		}
		if len(vals) > 0 {
			dp.Values = vals
		}
		out = append(out, dp)
	}
	return out, nil
}

// toFloat64Ptr 是**唯一**的列值归一函数：把驱动可能返回的各种形态统一成 *float64。
//
// 覆盖 nil / *float64 / float64 / float32 / int64 / int / int32 / 各宽度整数 /
// []byte / string（驱动在文本形态、空串 NULL、以及 sqlite 的动态类型下都会给出
// 这些形态）。**无法解析一律返回 nil**（缺 ≠ 0，绝不能当成 0）；NaN/Inf 同样返回
// nil（不可表示，协议层 checkFinite 也是拒绝口径），否则响应 JSON 会序列化失败。
func toFloat64Ptr(v any) *float64 {
	switch t := v.(type) {
	case nil:
		return nil
	case *float64:
		return t
	case float64:
		return finiteF64(t)
	case float32:
		return finiteF64(float64(t))
	case int64:
		f := float64(t)
		return &f
	case int:
		f := float64(t)
		return &f
	case int32:
		f := float64(t)
		return &f
	case int16:
		f := float64(t)
		return &f
	case int8:
		f := float64(t)
		return &f
	case uint64:
		f := float64(t)
		return &f
	case uint32:
		f := float64(t)
		return &f
	case uint16:
		f := float64(t)
		return &f
	case uint8:
		f := float64(t)
		return &f
	case []byte:
		return parseF64(string(t))
	case string:
		return parseF64(t)
	default:
		return nil
	}
}

func finiteF64(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	out := v
	return &out
}

func parseF64(s string) *float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return nil
	}
	return finiteF64(f)
}

// resourceTable 把资源种类映射到明细子表。
//
// 表名取自 entity 的单一枚举源（C3）：本包任何地方都不得再出现表名字面量
// （包括本文件的注释之外的字符串），漂移由 TestServiceHasNoHardcodedMetricTableNames
// 与 TestServiceTableNamesMatchEntityConstants 守卫。
func resourceTable(kind string) (string, bool) {
	switch kind {
	case agentmetrics.ResourceKindDisk:
		return entity.TableNameMetricDisk, true
	case agentmetrics.ResourceKindDiskIO:
		return entity.TableNameMetricDiskIO, true
	case agentmetrics.ResourceKindNIC:
		return entity.TableNameMetricNIC, true
	case agentmetrics.ResourceKindSensor:
		return entity.TableNameMetricSensor, true
	default:
		return "", false
	}
}

func splitCSV(s string) []string {
	out := make([]string, 0, 8)
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	return out
}

// toMetricPoints 把热层与冷层的结果统一成响应形状。
// **实测**：两者都必须在同一函数里归一，前端才无感切换数据源。
func toMetricPoints(in []agentmetrics.TrendPoint) []response.DeviceMetricPoint {
	out := make([]response.DeviceMetricPoint, 0, len(in))
	for _, b := range in {
		out = append(out, response.DeviceMetricPoint{
			T: b.T, CPUUsedPercent: b.CPUUsedPercent, CPUIOWait: b.CPUIOWait,
			Load1: b.Load1, Load5: b.Load5, Load15: b.Load15,
			MemUsedPercent: b.MemUsedPercent, MemUsedMB: b.MemUsedMB, MemAvailableMB: b.MemAvailableMB,
			SwapUsedPercent: b.SwapUsedPercent, SwapUsedMB: b.SwapUsedMB,
			TCPTotal: b.TCPTotal, TCPEstablished: b.TCPEstablished, TCPListen: b.TCPListen,
			ProcCount: b.ProcCount, UptimeSec: b.UptimeSec,
			DiskTotalGB: b.DiskTotalGB, DiskUsedGB: b.DiskUsedGB, DiskUsedPercent: b.DiskUsedPercent,
			DiskIOReadBytesSec: b.DiskIOReadBytesSec, DiskIOWriteBytesSec: b.DiskIOWriteBytesSec,
			NICRXBytesSec: b.NICRXBytesSec, NICTXBytesSec: b.NICTXBytesSec,
			MaxTemperatureC: b.MaxTemperatureC, Samples: b.Samples,
		})
	}
	return out
}
