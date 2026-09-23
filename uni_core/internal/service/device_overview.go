package service

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agentmetrics"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
)

// 本文件实现「设备监控总览」：**一次请求**返回 N 台设备 × 若干指标类的聚合形状。
//
// ── 为什么这是一个**新接口**而不是前端多打几次既有接口 ──────────────────
//
// 总览页要「所有设备 × 各类指标同屏」，而已有的每台设备取数入口只有
// `GET /devices/:id/metrics`。它一次只能取**一台**设备，N 台设备需要 N 次请求
// 且必须串行铺开（每台 ~176 KB / range=3600 全列，实测）。100 台 ≈ 17 MB
// 与 100 次往返 —— 这在浏览器上是不可用的，且失败面从「一次」变成 N 次。
//
// 故总览走**批量聚合**：拆成「一次设备列表查询 + N 次列投影趋势查询」，
// 由后端串行/受控并发地取回，再转置成列式 JSON。与既有契约的关系：
//   - **不改任何既有接口**的入参/出参形状（列表页、详情页、下钻页均不受影响）；
//   - 复用**同一套**选档（SelectTier）、列白名单（resolveTrendColumns）、
//     归并（MergeTrendPoints）与在线判定（offlineThresholdSec），故总览与
//     详情页对同一个 range / 同一个 metrics 的解释必然一致 —— 这是刻意的：
//     两处各写一套必然漂移，而「同一个 session 在两张页面上看到不同的 CPU 曲线」
//     是最难被发现的缺陷。

// DeviceOverviewRepository 是总览需要的设备仓储能力。
//
// 与 DeviceService 的 DeviceRepository 分开声明（而不是复用同一个接口）：
// 两个消费方需要的方法集不同 —— 总览不需要 SetStatus/Delete，列表不需要
// FindForOverview。窄接口让「总览页不可能误删设备」成为编译期事实，
// 也与本包既有的每消费方一接口的做法一致。
type DeviceOverviewRepository interface {
	FindForOverview(ctx context.Context, q *request.DeviceOverviewQuery,
		ids []uint64, onlineSince time.Time, limit int) ([]entity.Device, bool, error)
}

// DeviceOverviewLatestReader 读一批设备的水位（快照图块的数据源）。
type DeviceOverviewLatestReader interface {
	GetMany(ctx context.Context, deviceIDs []uint64) (map[uint64]*agentmetrics.LatestSummary, error)
}

// DeviceOverviewTrendReader 读一批设备的整机趋势。
//
// 刻意比 DeviceMetricReader 更窄（只要 ReadTrendPoints，不要 ReadResourceRows）：
// 总览画的是整机指标，不需要资源下钻的开放形状读取能力。窄接口让
// 「总览误用下钻读取路径」在编译期不可能发生。
type DeviceOverviewTrendReader interface {
	ReadTrendPoints(ctx context.Context, table string, deviceID uint64, from, to int64, columns []string) ([]agentmetrics.TrendPoint, error)
}

// 热层读能力复用 agent_metrics_query.go 的窄接口 AgentRawQuerier
// （只有 Query）。**不**用 AgentRawReader：那个面还带着下钻用的 Bucket，
// 而总览不需要原始样本直读。

// DeviceOverviewService 组装总览响应。
type DeviceOverviewService struct {
	devices DeviceOverviewRepository
	latest  DeviceOverviewLatestReader
	trend   DeviceOverviewTrendReader
	raw     AgentRawQuerier
	// policy 提供 Redis 档原生栅格（与指标查询服务同一个实现，故同一份配置）。
	policy RedisIntervalPolicy
	// offlineThreshold 读离线阈值；**复用 DeviceService 的同一实现**
	// （同一配置键、同一缺省与钳制），避免两页对「在线」给出不同答案。
	cfg interface {
		GetInt(ctx context.Context, key string, def int) int
	}
	log logger.LoggerInterface
}

func NewDeviceOverviewService(devices DeviceOverviewRepository, latest DeviceOverviewLatestReader,
	trend DeviceOverviewTrendReader, raw AgentRawQuerier, policy RedisIntervalPolicy,
	cfg interface {
		GetInt(ctx context.Context, key string, def int) int
	}, log logger.LoggerInterface) *DeviceOverviewService {
	return &DeviceOverviewService{
		devices: devices, latest: latest, trend: trend, raw: raw,
		policy: policy, cfg: cfg, log: log,
	}
}

// redisIntervalSec 与 AgentMetricsQueryService 的同名方法语义一致（含缺省与告警）。
func (s *DeviceOverviewService) redisIntervalSec() int64 {
	if s.policy == nil {
		if s.log != nil {
			s.log.Warn("device overview: RedisIntervalPolicy 未注入，Redis 档栅格退化为默认值")
		}
		return RedisNativeResolutionSec
	}
	return int64(s.policy.ReportInterval() / time.Second)
}

// offlineThresholdSec 返回生效的离线阈值（秒）。
//
// 与 DeviceService.offlineThresholdSec **逐字同规则**（<=0 回落 30）：
// 阈值 0 会让 `last_seen_at >= now` 恒不成立，等于把所有设备判成离线。
func (s *DeviceOverviewService) offlineThresholdSec(ctx context.Context) int {
	sec := 30
	if s.cfg != nil {
		sec = s.cfg.GetInt(ctx, ConfigOfflineThreshold, 30)
	}
	if sec <= 0 {
		sec = 30
	}
	return sec
}

// Overview 组装设备监控总览。
func (s *DeviceOverviewService) Overview(ctx context.Context,
	q *request.DeviceOverviewQuery) (*response.DeviceOverviewResp, error) {
	if q == nil {
		return nil, apperror.BadRequest("缺少查询参数")
	}
	rangeSec := q.Range
	if rangeSec == 0 {
		rangeSec = 24 * 3600
	}
	sel, err := SelectTier(rangeSec, s.redisIntervalSec())
	if err != nil {
		return nil, err
	}

	// 列解析复用指标查询的**同一个**函数：未知列一律 400，
	// 且返回的 cols 里恒含 bucket_ts（键列）。
	cols, err := resolveTrendColumns(sel, q.Metrics)
	if err != nil {
		return nil, err
	}
	valueCols := withoutBucketTS(cols)

	ids, err := parseOverviewIDs(q.Ids)
	if err != nil {
		return nil, err
	}

	threshold := s.offlineThresholdSec(ctx)
	now := time.Now()
	onlineSince := now.Add(-time.Duration(threshold) * time.Second)

	limit := request.OverviewMaxDevicesDefault
	devices, truncated, err := s.devices.FindForOverview(ctx, q, ids, onlineSince, limit)
	if err != nil {
		return nil, apperror.Internal("内部错误", err)
	}

	// 集合字段一律初始化为**非 nil 空切片**：Go 的 nil slice 会序列化成 JSON
	// `null`，而生成的 TS 类型把它们声明成数组（`axis: number[]`）。二者不一致
	// 时，消费方一行 `resp.axis.length` 就会在空结果上抛 TypeError ——
	// 而空结果恰恰是最需要稳妥渲染的一种状态（新装系统、筛选无命中）。
	// 这条契约（集合恒为数组、永不为 null）由 TestOverviewEmptyCollectionsAreArrays 守卫。
	resp := &response.DeviceOverviewResp{
		RangeSeconds:      sel.RangeSeconds,
		ResolutionSeconds: sel.Resolution * sel.Scale,
		Source:            sel.Source,
		AvailableMetrics:  valueCols,
		MaxDevices:        limit,
		DeviceTotal:       len(devices),
		Truncated:         truncated,
		AxisStepSeconds:   1,
		Axis:              []int64{},
		Devices:           []response.DeviceOverviewItem{},
		Summary:           response.DeviceOverviewSummary{OfflineThresholdSec: threshold},
	}
	if len(devices) == 0 {
		// 0 台设备是**合法的空态**（不是错误）：返回带 summary 的空响应，
		// 让前端渲染「暂无设备」而不是一个 404 或错误页。
		return resp, nil
	}

	deviceIDs := make([]uint64, 0, len(devices))
	for i := range devices {
		deviceIDs = append(deviceIDs, devices[i].ID)
	}

	// 水位一次性批量取（GetMany 内部走 pipeline，见 LatestStore.GetMany）。
	watermarks, err := s.latest.GetMany(ctx, deviceIDs)
	if err != nil {
		// 水位是快照图块的唯一来源，全取失败则整页都空。
		// 但设备列表自身仍有效 —— 报 500 会让用户连「有哪几台设备」都看不到。
		// 故降级：水位留空（UI 显示「—」），照常返回设备与趋势。
		if s.log != nil {
			s.log.Warn("device overview: 读取水位失败，降级为无快照")
		}
		watermarks = nil
	}

	from := now.Unix() - sel.RangeSeconds
	to := now.Unix()

	// 时间轴：先用第一台**取数成功**的设备建立，再按桶时间定位其余设备的取值槽位。
	//
	// 为什么以「实际桶」而非「理论区间」建轴：Redis 档的桶由 agent 上报节奏决定，
	// 首桶可能因设备刚上线而缺席；DB 档则可能因整表短暂为空而更少。若按理论区间
	// 生成轴，会把「设备还没上线」画成一段空白而不是「该设备无数据」。
	var axis []int64
	for i := range devices {
		d := &devices[i]
		pts, ferr := s.readTrend(ctx, sel, d.ID, from, to, cols)
		if ferr != nil {
			// 单台失败不影响其余设备（总览的价值在于「大多数设备是好的」）。
			resp.Devices = append(resp.Devices,
				s.buildItem(d, watermarks, nil, threshold, ferr.Error(), now))
			continue
		}
		if axis == nil {
			axis = make([]int64, 0, len(pts))
			for _, p := range pts {
				axis = append(axis, p.T)
			}
		}
		series := buildOverviewSeries(pts, valueCols, axis)
		resp.Devices = append(resp.Devices, s.buildItem(d, watermarks, series, threshold, "", now))
	}

	if axis == nil {
		axis = []int64{}
	}
	resp.Axis = axis
	resp.AxisStepSeconds = sel.Scale

	// 抽样/降采说明：桶数越界时 SelectTier 已经用整数倍升档解决（Scale > 1），
	// 故这里 Scale > 1 即「已抽样」。升档用的是加权归并而非跳点采样
	// （MergeTrendPoints），信息损失比抽点小，但**仍然**会削平尖峰 ——
	// 故必须显式告知，不能静默。
	resp.Downsampled = sel.Scale > 1

	s.fillSummary(resp, devices, watermarks, onlineSince, threshold)

	return resp, nil
}

// fillSummary 统计页面级计数的各分支。
//
// 抽成独立函数是因为它需要 devices 与 watermarks 两份输入，
// 且四个计数彼此正交（online/offline/disabled/stale/total），
// 混在 Overview 主体里会让主流程的逻辑被计数细节淹没。
func (s *DeviceOverviewService) fillSummary(resp *response.DeviceOverviewResp,
	devices []entity.Device, watermarks map[uint64]*agentmetrics.LatestSummary,
	onlineSince time.Time, threshold int) {
	resp.Summary = response.DeviceOverviewSummary{
		Total:               len(devices),
		OfflineThresholdSec: threshold,
	}
	staleBefore := onlineSince.UnixMilli()
	for i := range devices {
		d := &devices[i]
		if d.Status == 0 {
			resp.Summary.Disabled++
		}
		if d.LastSeenAt != nil && d.LastSeenAt.After(onlineSince) {
			resp.Summary.Online++
		} else {
			resp.Summary.Offline++
		}
		// Stale 与 Offline **不是**同一件事：前者看「水位采样时刻」（指标是否还在
		// 更新），后者看「最后上报时刻」（心跳是否还在）。设备可能心跳正常却
		// 停止上报指标（agent 采集卡住），此时 online=true 但数据是旧的 ——
		// 这正是运维最需要看见的一种故障。
		if w := watermarks[d.ID]; w != nil && w.T > 0 && w.T < staleBefore {
			resp.Summary.Stale++
		}
	}
}

// buildItem 组装单台设备的响应项（含水位快照与已算好的趋势列）。
//
// now 由调用方传入而**不是**在此处取 time.Now()：summary 的 Online/Stale
// 计数与本项字段必须出自**同一个时刻**，否则一次跨过阈值边界的渲染会出现
// 「summary 说 3 台在线、列表里只有 2 台 Online=true」这种自相矛盾的响应。
func (s *DeviceOverviewService) buildItem(d *entity.Device, watermarks map[uint64]*agentmetrics.LatestSummary,
	series []response.DeviceOverviewSeries, threshold int, errMsg string, now time.Time) response.DeviceOverviewItem {
	item := response.DeviceOverviewItem{
		ID:       strconv.FormatUint(d.ID, 10),
		Hostname: d.Hostname,
		Platform: d.Platform,
		OS:       d.OS,
		Status:   d.Status,
		Series:   series,
		Error:    errMsg,
	}
	onlineSince := now.Add(-time.Duration(threshold) * time.Second)
	if d.LastSeenAt != nil {
		v := d.LastSeenAt.Unix()
		item.LastSeenAt = &v
		item.Online = d.LastSeenAt.After(onlineSince)
	}
	w := watermarks[d.ID]
	item.Watermark = agentmetrics.LatestToWatermark(w)
	if w != nil && w.T > 0 {
		sec := w.T / 1000
		item.WatermarkAt = &sec
		// Stale 判定与 summary 同源（同一阈值、同一份水位、同一 now）。
		item.Stale = w.T < onlineSince.UnixMilli()
	}
	return item
}

// readTrend 取一台设备的整机趋势（按选档走热层或冷层）。
//
// 热/冷两层在此**归一成同一形状**（[]TrendPoint），故上层 buildOverviewSeries
// 不需要知道数据来自哪一档 —— 这是「前端无感切换数据源」在总览侧的等价保证。
func (s *DeviceOverviewService) readTrend(ctx context.Context, sel TierSelection,
	deviceID uint64, from, to int64, cols []string) ([]agentmetrics.TrendPoint, error) {
	if sel.Source == SourceRedis {
		if s.raw == nil {
			return nil, fmt.Errorf("热层未装配")
		}
		snap, err := s.raw.Query(ctx, deviceID,
			time.Duration(sel.RangeSeconds)*time.Second,
			time.Duration(sel.Resolution*sel.Scale)*time.Second)
		if err != nil {
			return nil, err
		}
		if snap == nil {
			return nil, nil
		}
		return snap.Buckets, nil
	}

	// 升档时隐式补 samples（与 Metrics 同理由：它是加权归并的权重来源）。
	readCols := cols
	if sel.Scale > 1 && !slices.Contains(cols, "samples") {
		readCols = append(append([]string(nil), cols...), "samples")
	}
	rows, err := s.trend.ReadTrendPoints(ctx, sel.Table, deviceID, from, to, readCols)
	if err != nil {
		return nil, err
	}
	if sel.Scale > 1 {
		rows = agentmetrics.MergeTrendPoints(rows, sel.Resolution*sel.Scale)
	}
	return rows, nil
}

// buildOverviewSeries 把「一行 24 列 × N 行」转置成「一列 × N 值」。
//
// 这是整个总览的成本核心：转置让 JSON 只写一次列名（而不是每个桶重复 24 个键名），
// 使载荷随「桶数 × 列数」线性增长而不带键名常数项。
//
// axis 是共享时间轴；pts 里每个点的 T 必须能在 axis 上找到位置。
// 找不到的点（理论上不该发生：轴取自同一次查询的第一台设备，且所有设备的桶
// 栅格由 SelectTier 统一决定）**跳过而非按序塞入** —— 按下标塞会在轴不同步时
// 静默产生错位的时间序列，那比丢一个点危险得多。
func buildOverviewSeries(pts []agentmetrics.TrendPoint, valueCols []string, axis []int64) []response.DeviceOverviewSeries {
	if len(valueCols) == 0 || len(axis) == 0 {
		return nil
	}
	// 桶时间 → 轴下标。
	pos := make(map[int64]int, len(axis))
	for i, t := range axis {
		pos[t] = i
	}

	out := make([]response.DeviceOverviewSeries, 0, len(valueCols))
	for _, col := range valueCols {
		s := response.DeviceOverviewSeries{
			Metric: col,
			Values: make([]*float64, len(axis)),
		}
		var (
			sum     float64
			cnt     int
			mn, mx  float64
			lastIdx int = -1
		)
		for _, p := range pts {
			idx, ok := pos[p.T]
			if !ok {
				continue
			}
			v, carried := agentmetrics.ColumnValue(p, col)
			if !carried || v == nil {
				// 未采集 → 该槽位留 nil（JSON null）→ 前端画空洞、显示「—」。
				// **绝不写 0**：0 是一个合法观测值（CPU 真为 0%、网卡真没流量），
				// 用 0 填充会让「没数据」与「真的空闲」在图上无法区分。
				continue
			}
			val := *v
			s.Values[idx] = &val
			sum += val
			cnt++
			if cnt == 1 || val < mn {
				mn = val
			}
			if cnt == 1 || val > mx {
				mx = val
			}
			lastIdx = idx
		}
		s.Present = cnt
		s.Missing = len(axis) - cnt
		if cnt > 0 {
			avg := sum / float64(cnt)
			last := *s.Values[lastIdx]
			s.Min, s.Max, s.Avg, s.Last = &mn, &mx, &avg, &last
		}
		out = append(out, s)
	}
	return out
}

// parseOverviewIDs 解析逗号分隔的设备 ID 白名单。
//
// 非法 ID（非数字、0、负数）返回 **400** 而不是静默丢弃：
// 静默丢弃会让「筛了 5 台、图上只有 3 台」变成用户无法察觉的静默差异，
// 而用户会照着 3 条线得出错误结论。宁可报错说清是哪个值不合法。
func parseOverviewIDs(raw string) ([]uint64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	out := make([]uint64, 0, len(parts))
	seen := make(map[uint64]bool, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		id, err := strconv.ParseUint(p, 10, 64)
		if err != nil || id == 0 {
			return nil, apperror.BadRequest(fmt.Sprintf("ids 含非法设备 ID: %q", p))
		}
		if seen[id] {
			continue // 同一台设备重复出现只算一次（去重而非报错）
		}
		seen[id] = true
		out = append(out, id)
	}
	// 排序让 SQL 的 IN 列表稳定（也便于测试与缓存命中）。
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}
