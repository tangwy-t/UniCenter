package service

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agentmetrics"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
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
	raw       AgentRawQuerier
	metrics   DeviceMetricReader
	resources DeviceResourceResolver
	log       logger.LoggerInterface
}

// AgentRawQuerier 是本服务用到的热层能力面。
type AgentRawQuerier interface {
	Query(ctx context.Context, deviceID uint64, window, step time.Duration) (*agentmetrics.RawSnapshot, error)
}

// DeviceMetricReader 是冷层读取面（列白名单由仓储内部校验）。
type DeviceMetricReader interface {
	ReadTrendPoints(ctx context.Context, table string, deviceID uint64, from, to int64, columns []string) ([]agentmetrics.TrendPoint, error)
	ReadResourceTrendPoints(ctx context.Context, table string, resourceID uint64, from, to int64, columns []string) ([]agentmetrics.TrendPoint, error)
}

// DeviceResourceResolver 是资源维度面。
type DeviceResourceResolver interface {
	ResolveID(ctx context.Context, deviceID uint64, kind, name string) (uint64, error)
}

func NewAgentMetricsQueryService(raw AgentRawQuerier, metrics DeviceMetricReader,
	resources DeviceResourceResolver, log logger.LoggerInterface) *AgentMetricsQueryService {
	return &AgentMetricsQueryService{raw: raw, metrics: metrics, resources: resources, log: log}
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
	cols := resolveColumns(q.Metrics)

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

// ResourceMetrics 返回单资源（磁盘/网卡/IO 设备/传感器）的下钻趋势。
func (s *AgentMetricsQueryService) ResourceMetrics(ctx context.Context, deviceID uint64,
	q *request.DeviceMetricsQuery) (*response.DeviceResourceResp, error) {
	if q == nil || q.Kind == "" || q.Name == "" {
		return nil, apperror.BadRequest("下钻必须同时提供 kind 与 name")
	}
	table, ok := resourceTable(q.Kind)
	if !ok {
		return nil, apperror.BadRequest("不支持的资源种类: " + q.Kind)
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

	resourceID, err := s.resources.ResolveID(ctx, deviceID, q.Kind, q.Name)
	if err != nil {
		// 资源不存在 → 返回空结果而不是 404：设备可能刚被卸载该资源
		return &response.DeviceResourceResp{
			RangeSeconds: sel.RangeSeconds, ResolutionSeconds: sel.Resolution * sel.Scale,
			Source: sel.Source, ResourceKind: q.Kind, Name: q.Name,
			AvailableMetrics: resolveColumns(q.Metrics), Buckets: []response.DeviceMetricPoint{},
		}, nil
	}

	now := time.Now().Unix()
	rows, err := s.metrics.ReadResourceTrendPoints(ctx, table, resourceID, now-sel.RangeSeconds, now, resolveColumns(q.Metrics))
	if err != nil {
		return nil, apperror.Internal("内部错误", err)
	}
	return &response.DeviceResourceResp{
		RangeSeconds:      sel.RangeSeconds,
		ResolutionSeconds: sel.Resolution * sel.Scale,
		Source:            sel.Source,
		ResourceKind:      q.Kind,
		Name:              q.Name,
		AvailableMetrics:  resolveColumns(q.Metrics),
		Buckets:           toMetricPoints(rows),
	}, nil
}

// resourceTable 把资源种类映射到明细子表（**唯一枚举源**）。
func resourceTable(kind string) (string, bool) {
	switch kind {
	case "disk":
		return "device_metric_disk", true
	case "disk_io":
		return "device_metric_diskio", true
	case "nic":
		return "device_metric_nic", true
	case "sensor":
		return "device_metric_sensor", true
	default:
		return "", false
	}
}

// resolveColumns 把请求里的 metrics 参数解析为投影列白名单。
// 空 → 默认列集；"*" → 全量（由 repository 的白名单校验兜底）。
func resolveColumns(metrics string) []string {
	if metrics == "" {
		return defaultMetricColumns
	}
	if metrics == request.MetricsAll {
		return nil // 交给 repository 判全量
	}
	return splitCSV(metrics)
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
