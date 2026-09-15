package service

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agentmetrics"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
)

// 数据源与表名常量（**唯一枚举源**，禁止在别处硬编码表名）。
const (
	SourceRedis = "redis"
	SourceDB    = "db"

	// MaxBuckets 是响应桶数上限；超过时按整数倍升 step（不跨档、不混档）。
	MaxBuckets int64 = 4000
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
//   - range ≤ 24h          → Redis 原始（10s）
//   - 24h < range ≤ 30d    → device_metric_5m（300s）
//   - 30d < range ≤ 180d   → device_metric_1h（3600s）
//   - 越界                  → BadRequest
//
// 跨档**绝不混用**：40d 整体走 1h 档，而不是「前 30d 用 5min + 后 10d 用 1h」。
func SelectTier(rangeSec int64) (TierSelection, error) {
	if rangeSec < request.DeviceRangeMin || rangeSec > request.DeviceRangeMax {
		return TierSelection{}, apperror.BadRequest(fmt.Sprintf("range 必须在 [%d, %d] 秒之间",
			request.DeviceRangeMin, request.DeviceRangeMax))
	}

	const day = int64(24 * 3600)
	sel := TierSelection{RangeSeconds: rangeSec, Scale: 1}

	switch {
	case rangeSec <= day:
		sel.Source = SourceRedis
		sel.Resolution = 10 // Redis 原始层按 10s 上报节奏取点；service 会用配置覆盖
	case rangeSec <= 30*day:
		sel.Source = SourceDB
		sel.Table = "device_metric_5m"
		sel.Resolution = 300
	default:
		sel.Source = SourceDB
		sel.Table = "device_metric_1h"
		sel.Resolution = 3600
	}

	// 桶数超上限 → 整数倍升 step（仍是原生 step 的整数倍，桶边界仍对齐）
	for sel.BucketCount() > MaxBuckets {
		sel.Scale++
	}
	return sel, nil
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
	log       logger.LoggerInterface
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
// 已无生产调用方，见任务回报。
type DeviceMetricReader interface {
	ReadTrendPoints(ctx context.Context, table string, deviceID uint64, from, to int64, columns []string) ([]agentmetrics.TrendPoint, error)
	ReadResourceRows(ctx context.Context, table string, resourceID uint64, from, to int64) ([]map[string]any, error)
}

// DeviceResourceResolver 是资源维度面。
type DeviceResourceResolver interface {
	ResolveID(ctx context.Context, deviceID uint64, kind, name string) (uint64, error)
}

func NewAgentMetricsQueryService(raw AgentRawReader, metrics DeviceMetricReader,
	resources DeviceResourceResolver, log logger.LoggerInterface) *AgentMetricsQueryService {
	// 同一实现拆成两个窄面（raw 为 nil 时两面都为 nil）。
	var q AgentRawQuerier = raw
	var b AgentRawBucketReader = raw
	return &AgentMetricsQueryService{raw: q, rawBucket: b, metrics: metrics, resources: resources, log: log}
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
	sel, err := SelectTier(rangeSec)
	if err != nil {
		return nil, err
	}

	now := time.Now().Unix()
	from := now - sel.RangeSeconds

	cols, err := s.resolveTrendColumns(trendColumnsTable(sel), q.Metrics)
	if err != nil {
		return nil, err
	}
	// D6：1h 档不存在的列被**显式请求** → 400，不静默剔除（spec §8 明文）。
	if err := validateTierColumns(sel, cols); err != nil {
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
//   - 空   → 该档位的默认列集
//   - "*"  → 该表的**全部值列**（D4：spec §8 明文「metrics=* 回全量」）
//   - CSV  → 逐列（白名单校验由仓储兜底）
//
// **恒补 bucket_ts**（D5）：`t` 是契约的一部分（spec §8「必须用 t 定位」），
// 不是可选列 —— 显式只请求 cpu_used_percent 时若不补 bucket_ts，投影里就没有
// bucket_ts，扫出来的每个点 t 都是 0（实测），前端按 t 定位时间全落在 1970。
func (s *AgentMetricsQueryService) resolveTrendColumns(table, metrics string) ([]string, error) {
	switch metrics {
	case "":
		return withBucketTS(defaultMetricColumns), nil
	case request.MetricsAll:
		all, err := repository.MetricQueryColumns(table)
		if err != nil {
			return nil, apperror.Internal("内部错误", err)
		}
		return withBucketTS(tierValueColumns(table, all)), nil
	default:
		return withBucketTS(splitCSV(metrics)), nil
	}
}

// trendColumnsTable 返回该档位用来解析列集的表名。
//
// Redis 档（≤24h）没有表名：热层原始样本的列集与 _5m 相同（_1h 与 _5m 本就是
// 「一套 schema 两处表名」，见 MetricColumnDDL），故一律按 _5m 解析。
func trendColumnsTable(sel TierSelection) string {
	if sel.Table == "" {
		return "device_metric_5m"
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
// 显式**点名**这些列仍然是 400（见 validateTierColumns）—— 这正是 D6 的语义。
func tierValueColumns(table string, cols []string) []string {
	if table != "device_metric_1h" {
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

// validateTierColumns 校验「1h 档 + 显式请求 5min-only 列」→ 400，**不静默剔除**。
//
// 与 `kind`+`name` 的校验同构（spec §8 明文）：参数非法就报错，不返回一条
// 恒为空、看起来像「agent 挂了」的曲线。
func validateTierColumns(sel TierSelection, cols []string) error {
	if sel.Source != SourceDB || sel.Table != "device_metric_1h" {
		return nil
	}
	for _, c := range cols {
		if isFiveMinOnlyColumn(c) {
			return apperror.BadRequest(fmt.Sprintf(
				"列 %q 只存在于 5min 档（range > 30d 走 1h 档，该档不产出此列），请把 range 缩短到 30 天以内", c))
		}
	}
	return nil
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
	sel, err := SelectTier(rangeSec)
	if err != nil {
		return nil, err
	}
	if sel.Source == SourceDB && sel.Table != "device_metric_5m" {
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
		// 资源不存在 → 返回空结果而不是 404：设备可能刚被卸载该资源
		return resp, nil
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

// resourceTable 把资源种类映射到明细子表（**唯一枚举源**）。
func resourceTable(kind string) (string, bool) {
	switch kind {
	case agentmetrics.ResourceKindDisk:
		return "device_metric_disk", true
	case agentmetrics.ResourceKindDiskIO:
		return "device_metric_diskio", true
	case agentmetrics.ResourceKindNIC:
		return "device_metric_nic", true
	case agentmetrics.ResourceKindSensor:
		return "device_metric_sensor", true
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
