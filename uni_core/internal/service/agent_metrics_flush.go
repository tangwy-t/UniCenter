package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"

	goredis "github.com/redis/go-redis/v9"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agentmetrics"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
)

// ── 消费方窄接口（仓库既有约定：接口定义在消费方）──────────────

// FlushRawReader 是热层原始窗 flush 需要的能力面。
type FlushRawReader interface {
	// Index 返回活跃设备（枚举源）。
	Index(ctx context.Context) ([]uint64, error)
	// Bucket 读取 [fromMs, toMs) 内的原始样本，按 T 升序。
	Bucket(ctx context.Context, deviceID uint64, fromMs, toMs int64) ([]agentproto.MetricsSample, error)
}

// DeviceMetricWriter 是 flush 写宽表+子表的唯一出口。
//
// 刻意**不含** WriteHour：1h 由 Task 5 的 rollup 负责，二者消费同一族游标但互不阻塞
// （Plan 2A 的「两段提交」）。把 WriteHour 放进这个接口等于给「共用事务」留缝。
type DeviceMetricWriter interface {
	WriteBucket(ctx context.Context, w *entity.DeviceMetricWide, subs repository.MetricSubRows) error
}

// FlushResourceRepo 是资源维度的读写能力面（name→id 解析）。
type FlushResourceRepo interface {
	UpsertSeen(ctx context.Context, rows []entity.DeviceResource, at time.Time) error
	ResolveID(ctx context.Context, deviceID uint64, kind, name string) (uint64, error)
}

// 配置键与文件名常量。
const (
	// configAgentReportInterval 是 agent 上报间隔（秒），CloseGrace 由它推导。
	configAgentReportInterval = "sys.agent.reportInterval"
	// defaultAgentReportIntervalSec 取 v008 种子的值（10s）。
	defaultAgentReportIntervalSec = 10
)

// FlushStats 是一轮的统计（任务与测试都读它）。
type FlushStats struct {
	// DevicesScanned 是本轮枚举到的活跃设备数。
	DevicesScanned int
	// BucketsWritten 是真正落库的桶数。
	BucketsWritten int
	// BucketsSkipped 是空桶数（无样本 → 不写行，但照常推进游标）。
	BucketsSkipped int
	// ResourcesUpserted 是本轮 upsert 的资源维度行数（跨设备累计）。
	ResourcesUpserted int
	// Errors 是本轮失败次数（每个失败桶/失败设备各 +1）。
	Errors int
}

// AgentMetricsFlushService 把 Redis 热层落成 5min 指标行。
//
// 只写 5m（`WriteBucket` 一个事务）；1h 由 Task 5 的 rollup 写（另一个事务），
// 二者消费**同一族游标**（`cursor_5m` / `cursor_1h`）但互不阻塞 —— 这正是
// 「两段提交」的意义：1h 是可推导数据，绝不能让它失败回滚 5m（唯一真值来源）。
type AgentMetricsFlushService struct {
	raw       FlushRawReader
	metrics   DeviceMetricWriter
	resources FlushResourceRepo
	rdb       goredis.Cmdable
	cfg       AgentConfigGetter
	log       logger.LoggerInterface
	// now 可注入（测试用固定时钟，区间直接可算）。
	now func() time.Time
}

func NewAgentMetricsFlushService(raw FlushRawReader, metrics DeviceMetricWriter,
	resources FlushResourceRepo, cfg AgentConfigGetter, log logger.LoggerInterface) *AgentMetricsFlushService {
	return &AgentMetricsFlushService{
		raw: raw, metrics: metrics, resources: resources, cfg: cfg, log: log, now: time.Now,
	}
}

// WithCursorStore 注入水位游标使用的 Redis 客户端。
//
// 游标与热层原始窗**必须落在同一个 Redis**（都是「agent 上报链路」的键族），
// 故 wireup 传的是同一个客户端；显式注入而不是从 raw 反查，是为了让游标这一层
// 不依赖 FlushRawReader 的具体实现（接口只要 Index/Bucket 两个能力）。
func (s *AgentMetricsFlushService) WithCursorStore(rdb goredis.Cmdable) *AgentMetricsFlushService {
	s.rdb = rdb
	return s
}

// WithClock 注入时钟（测试用；生产走 time.Now）。
func (s *AgentMetricsFlushService) WithClock(now func() time.Time) *AgentMetricsFlushService {
	if now != nil {
		s.now = now
	}
	return s
}

// Bootstrap 初始化缺失的水位：从 `now−24h` **对齐到 5min 边界**起步。
//
// 为什么是 24h 而不是 0：raw 点只在 Redis 保留 24h（spec §7.1），更早的桶**必然**
// 是空的 —— 从 0 起步会白扫 1970 年以来的 6_000_000+ 个桶（每设备每轮），
// 而结果与从 24h 起步完全一致。
//
// 为什么**不覆写**已有游标：游标是「已成功落库到（含）哪个桶」的水位，
// 覆写会让最近 24h 的桶被重复写一遍（虽然 UPSERT 幂等，但会无谓地重算并覆盖
// 已被 rollup 消费过的窗口边界）。故只在键缺失时写。
func (s *AgentMetricsFlushService) Bootstrap(ctx context.Context) error {
	devices, err := s.raw.Index(ctx)
	if err != nil {
		return fmt.Errorf("agentmetrics flush bootstrap: 枚举活跃设备失败: %w", err)
	}
	start := s.bootstrapStart()
	for _, deviceID := range devices {
		key := agentmetrics.CursorKey(deviceID, agentmetrics.Resolution5m)
		if err := s.rdb.Get(ctx, key).Err(); err == nil {
			continue // 已有水位：不动
		} else if !errors.Is(err, goredis.Nil) {
			return fmt.Errorf("agentmetrics flush bootstrap: 读水位 %s: %w", key, err)
		}
		if err := s.writeCursor(ctx, deviceID, start); err != nil {
			return err
		}
	}
	return nil
}

// FlushOnce 跑一轮：枚举活跃设备 → 逐设备把已闭桶落成 5min 行。
//
// 返回的 error 是「本轮有失败」的汇总（sentinel：ErrFlushPartial），用于让任务层
// 记日志/告警；**游标语义不受它影响** —— 失败设备的水位留在原处，下轮重试同一批桶。
func (s *AgentMetricsFlushService) FlushOnce(ctx context.Context) (FlushStats, error) {
	var stats FlushStats
	now := s.now()

	devices, err := s.raw.Index(ctx)
	if err != nil {
		return stats, fmt.Errorf("agentmetrics flush: 枚举活跃设备失败: %w", err)
	}
	stats.DevicesScanned = len(devices)

	grace := s.closeGrace()
	upper := alignDown(now.Add(-grace).Unix(), resolutionSeconds(agentmetrics.Resolution5m))

	failures := 0
	for _, deviceID := range devices {
		written, skipped, upserted, ferr := s.flushDevice(ctx, deviceID, upper)
		stats.BucketsWritten += written
		stats.BucketsSkipped += skipped
		stats.ResourcesUpserted += upserted
		if ferr != nil {
			failures++
			stats.Errors++
			s.log.Error("agentmetrics flush: 设备本轮落库失败，水位留在原处待下轮重试",
				zap.Uint64("deviceId", deviceID), zap.Error(ferr))
		}
	}
	if failures > 0 {
		return stats, fmt.Errorf("%w: %d/%d 台设备落库失败（水位未推进，下轮重试）",
			ErrFlushPartial, failures, stats.DevicesScanned)
	}
	return stats, nil
}

// ErrFlushPartial 表示本轮有设备/桶失败（游标未推进，下轮重试同一批桶）。
var ErrFlushPartial = errors.New("agentmetrics flush: 本轮部分落库失败")

// ─ 单设备 ─────────────────────────────────────────────

// flushDevice 处理单台设备的已闭桶区间，返回 (落库桶数, 空桶数, 资源行数, 错误)。
//
// 区间的两个端点（与计划 Step 3 逐字一致；upper 由 FlushOnce 一次算好、全设备共用）：
//
//	下界 = cursor + 300   —— cursor 是「已成功落库到（含）」的桶起点
//	上界 = alignDown(now − CloseGrace, 300) —— 半开区间，不含上界
//
// 上界必须留 CloseGrace（= reportInterval×2）：**正在收数据**的桶里，10s 点还在
// 陆续到达，此刻落库会把不完整的桶写成权威真值，而 5m 是唯一真值来源。
//
// **水位只在「本轮所有桶都成功」后才前移**（用户的硬约束，逐条测试校验）：
// 本轮任一步失败 → 水位**留在轮初的值**，下轮从同一个桶重来。因此一旦出错就
// 立即返回、绝不继续往后写：继续写会让后面那些桶的数据在下一轮被**重复**写，
// 而水位又停在轮初 —— 同一批桶被反复重放，这是「部分成功 + 水位不动」的固有代价，
// 5m 行是 UPSERT 所以结果正确，但没必要在本轮多做无用的写。
func (s *AgentMetricsFlushService) flushDevice(ctx context.Context, deviceID uint64,
	upper int64) (written, skipped, upserted int, err error) {

	bucketSec := resolutionSeconds(agentmetrics.Resolution5m)

	cursor, err := s.readCursor(ctx, deviceID)
	if err != nil {
		return 0, 0, 0, err
	}

	from := cursor + bucketSec
	if from >= upper {
		// 没有新的已闭桶（正常：两次 flush 间隔小于 5min）。水位无需动（本就没前进）。
		return 0, 0, 0, nil
	}

	lastBucketed := cursor
	for b := from; b < upper; b += bucketSec {
		pts, berr := s.raw.Bucket(ctx, deviceID, b*1000, (b+bucketSec)*1000)
		if berr != nil {
			// 读失败**立即返回**：水位留在轮初（下面那句 writeCursor 不会被执行）。
			return written, skipped, upserted, fmt.Errorf("读桶 device=%d bucket=%d: %w", deviceID, b, berr)
		}
		wide, subs, ok := agentmetrics.Downsample(deviceID, b, pts)
		if !ok {
			// 空桶**照常推进**（它确实没有数据，算「处理成功」）：把空桶当失败
			// 会让一台长期没有明细上报的设备永远卡在第一个空桶上，水位再也不动。
			lastBucketed = b
			skipped++
			continue
		}
		n, uerr := s.writeBucket(ctx, deviceID, b, wide, subs)
		if uerr != nil {
			// 写失败同样立即返回：水位留在轮初，下轮重试同一批桶。
			return written, skipped, upserted, uerr
		}
		lastBucketed = b
		written++
		upserted += n
	}

	// 走到这里说明**本轮每个桶都成功**（空桶也算成功），才前移水位。
	if err := s.writeCursor(ctx, deviceID, lastBucketed); err != nil {
		return written, skipped, upserted, err
	}
	return written, skipped, upserted, nil
}

// writeBucket 把纯逻辑层的 Wide/Subs 解析成 id 化行并落库，返回 upsert 的资源行数。
func (s *AgentMetricsFlushService) writeBucket(ctx context.Context, deviceID uint64, bucketTS int64,
	wide agentmetrics.Wide, subs agentmetrics.Subs) (int, error) {

	resRows := resourceRows(deviceID, subs)
	if len(resRows) > 0 {
		// **at 用桶时间，不是 now**：`last_seen_at` 的语义是「资源最近一次被观测到的
		// 时间」。补跑/重放历史桶时（Bootstrap 的 24h 窗口、或故障后重试积压的桶）
		// 用 now 会把 last_seen_at 往前推 —— 于是 /resources 的 stale 标记被抹掉，
		// 早就卸掉的挂载点看起来「刚刚还在」，运维据此排障会得到反向结论。
		// 桶时间才是真正的观测时刻。
		if err := s.resources.UpsertSeen(ctx, resRows, time.Unix(bucketTS, 0)); err != nil {
			return 0, fmt.Errorf("upsert 资源维度 device=%d bucket=%d: %w", deviceID, bucketTS, err)
		}
	}

	idSubs, err := s.resolveSubs(ctx, deviceID, bucketTS, subs)
	if err != nil {
		return 0, err
	}

	row, err := repository.EntityFromWide(wide)
	if err != nil {
		return 0, fmt.Errorf("宽行转换 device=%d bucket=%d: %w", deviceID, bucketTS, err)
	}
	if err := s.metrics.WriteBucket(ctx, row, idSubs); err != nil {
		return 0, fmt.Errorf("写 5m 桶 device=%d bucket=%d: %w", deviceID, bucketTS, err)
	}
	return len(resRows), nil
}

// ── 资源 name→id ───────────────────────────────────────

// resourceRows 把 4 张子表的 name 键明细翻成资源维度行（去重，顺序稳定）。
//
// 顺序稳定很重要：UpsertSeen 是 CreateInBatches，行序影响雪花 id 的分配顺序
// （id 不参与任何业务语义，但稳定顺序让重放测试可复现）。
func resourceRows(deviceID uint64, subs agentmetrics.Subs) []entity.DeviceResource {
	out := make([]entity.DeviceResource, 0, len(subs.Disks)+len(subs.DiskIO)+len(subs.NICs)+len(subs.Sensors))
	seen := map[string]bool{}
	add := func(kind, name, fsType string) {
		if name == "" {
			return
		}
		key := kind + "\x00" + name
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, entity.DeviceResource{DeviceID: deviceID, Kind: kind, Name: name, FSType: fsType})
	}
	for _, d := range subs.Disks {
		add(agentmetrics.ResourceKindDisk, d.Name, d.FSType)
	}
	for _, d := range subs.DiskIO {
		add(agentmetrics.ResourceKindDiskIO, d.Name, "")
	}
	for _, n := range subs.NICs {
		add(agentmetrics.ResourceKindNIC, n.Name, "")
	}
	for _, sn := range subs.Sensors {
		add(agentmetrics.ResourceKindSensor, sn.Name, "")
	}
	return out
}

// resolveSubs 把按 name 键的子表行翻成按 resource_id 键的行（写库形状）。
//
// 每次解析都是「UpsertSeen 之后的回读」，而不是本地缓存 id：这样跨轮语义与
// 「另一个进程刚刚并发插入了同名资源」一致，且不需要在 service 里维护第二份维度缓存
// —— 维度表的 id 只有一个权威来源（DB 的唯一索引 + 回读）。
func (s *AgentMetricsFlushService) resolveSubs(ctx context.Context, deviceID uint64,
	bucketTS int64, subs agentmetrics.Subs) (repository.MetricSubRows, error) {

	var out repository.MetricSubRows
	resolve := func(kind, name string) (uint64, error) {
		id, err := s.resources.ResolveID(ctx, deviceID, kind, name)
		if err != nil {
			return 0, fmt.Errorf("解析 resource_id device=%d kind=%s name=%s: %w", deviceID, kind, name, err)
		}
		if id == 0 {
			return 0, fmt.Errorf("解析 resource_id device=%d kind=%s name=%s: 返回 id=0", deviceID, kind, name)
		}
		return id, nil
	}

	for _, d := range subs.Disks {
		id, err := resolve(agentmetrics.ResourceKindDisk, d.Name)
		if err != nil {
			return out, err
		}
		out.Disks = append(out.Disks, entity.DeviceMetricDisk{
			ResourceID: id, BucketTS: bucketTS,
			UsedPercent: d.UsedPercent, UsedGB: d.UsedGB, TotalGB: d.TotalGB,
			InodesUsedPercent: d.InodesUsedPercent,
		})
	}
	for _, d := range subs.DiskIO {
		id, err := resolve(agentmetrics.ResourceKindDiskIO, d.Name)
		if err != nil {
			return out, err
		}
		out.DiskIO = append(out.DiskIO, entity.DeviceMetricDiskIO{
			ResourceID: id, BucketTS: bucketTS,
			ReadBytesPerSec: d.ReadBytesPerSec, WriteBytesPerSec: d.WriteBytesPerSec,
			ReadOpsPerSec: d.ReadOpsPerSec, WriteOpsPerSec: d.WriteOpsPerSec,
			IOTimePercent: d.IOTimePercent,
		})
	}
	for _, n := range subs.NICs {
		id, err := resolve(agentmetrics.ResourceKindNIC, n.Name)
		if err != nil {
			return out, err
		}
		out.NICs = append(out.NICs, entity.DeviceMetricNIC{
			ResourceID: id, BucketTS: bucketTS,
			RXBytesPerSec: n.RXBytesPerSec, TXBytesPerSec: n.TXBytesPerSec,
			RXPacketsPerSec: n.RXPacketsPerSec, TXPacketsPerSec: n.TXPacketsPerSec,
			RXErrorsPerSec: n.RXErrorsPerSec, TXErrorsPerSec: n.TXErrorsPerSec,
			RXDroppedPerSec: n.RXDroppedPerSec,
		})
	}
	for _, sn := range subs.Sensors {
		id, err := resolve(agentmetrics.ResourceKindSensor, sn.Name)
		if err != nil {
			return out, err
		}
		out.Sensors = append(out.Sensors, entity.DeviceMetricSensor{
			ResourceID: id, BucketTS: bucketTS, TemperatureC: sn.TemperatureC,
		})
	}
	return out, nil
}

// ── 水位游标 ───────────────────────────────────────────

// bootstrapStart 返回 Bootstrap 的起点：`now−24h` 对齐到 5min 边界，并**夹到 0 下界**。
//
// 为什么必须夹 0：`now−24h` 在「服务首次启动」与「测试里的历史时钟」两种场景下都可能是
// **负数**（unix 秒原点之前）。负水位会被真的写进 Redis（`SET -84600`），下一轮
// `from = cursor+300` 仍为负 → 每轮都从「纪元之前」重扫一遍；更糟的是它与「从 0 起步」
// 在语义上无法区分（都是「扫描全部可得历史」），只是把同一个缺陷换了个符号。
// 5min 栅格在 unix 秒域里以 0 为界，夹到 0 即可（alignDown(0,300)=0）。
func (s *AgentMetricsFlushService) bootstrapStart() int64 {
	start := alignDown(s.now().Add(-bootstrapWindow).Unix(), resolutionSeconds(agentmetrics.Resolution5m))
	if start < 0 {
		return 0
	}
	return start
}

// bootstrapWindow 是补齐窗口（= raw 点的 Redis 保留期，spec §7.1）。
const bootstrapWindow = 24 * time.Hour

// readCursor 读 `cursor_5m`；键缺失 → Bootstrap 语义（写 `now−24h` 对齐后的水位并返回它）。
//
// 为什么缺失时**写回**而不是只在内存里用：写回让「首次 flush」这件事可见且幂等
// （第二次调用读到同一个值），也让运维能直接看到水位。写失败则上抛 —— 否则整轮都在
// 一个「内存里的假水位」上跑，下一轮又从 24h 前重算一遍。
func (s *AgentMetricsFlushService) readCursor(ctx context.Context, deviceID uint64) (int64, error) {
	key := agentmetrics.CursorKey(deviceID, agentmetrics.Resolution5m)
	v, err := s.rdb.Get(ctx, key).Int64()
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, goredis.Nil) {
		// 只有「键不存在」才走 Bootstrap。Redis 故障时**必须上抛**：把它当成
		// 「没有游标」会让整轮从 24h 前重算并**回写更小的水位**，
		// 已落库的桶被重写、且这是不可观测的降级。
		return 0, fmt.Errorf("读水位 %s: %w", key, err)
	}
	start := s.bootstrapStart()
	if werr := s.writeCursor(ctx, deviceID, start); werr != nil {
		return 0, werr
	}
	return start, nil
}

// CursorFor 返回**本轮实际会使用**的起点水位（unix 秒）——缺水位时按 Bootstrap 语义
// 初始化，返回值与 FlushOnce 内部读到的**同一个值**。
//
// 暴露它的理由（可观测性，不是给生产调用的便利方法）：游标起点是「这一轮会处理哪个区间」
// 的唯一决定因素，而它平时只体现在「桶计数」这种**间接**信号上 —— 起点一旦退化成 0，
// 一轮要扫 6_000_000+ 个桶，于是反向验证（临时改回旧行为 → 看断言是否变红）只能得到
// 「测试超时」而不是一条可读的断言失败。有了它，起点可以被**直接**断言（毫秒级）。
func (s *AgentMetricsFlushService) CursorFor(ctx context.Context, deviceID uint64) (int64, error) {
	return s.readCursor(ctx, deviceID)
}

// writeCursor 落水位（unix 秒，无 TTL：水位必须跨天常驻）。
func (s *AgentMetricsFlushService) writeCursor(ctx context.Context, deviceID uint64, sec int64) error {
	key := agentmetrics.CursorKey(deviceID, agentmetrics.Resolution5m)
	if err := s.rdb.Set(ctx, key, sec, 0).Err(); err != nil {
		return fmt.Errorf("写水位 %s: %w", key, err)
	}
	return nil
}

// closeGrace 取 `reportInterval×2`：避免把**还在收数据**的当前桶写坏。
//
// 为什么是 2× 而不是 1×：10s 上报的桶在边界处最多可能有一条样例在途（网络抖动 +
// agent 侧批量缓冲），1× 只留一个上报间隔等于没有余量。2× 是最小安全余量，
// 代价是「当前桶延后一个 5min 才落库」—— 对 5min 栅格的消费方无感。
func (s *AgentMetricsFlushService) closeGrace() time.Duration {
	sec := s.cfg.GetInt(context.Background(), configAgentReportInterval, defaultAgentReportIntervalSec)
	if sec <= 0 {
		sec = defaultAgentReportIntervalSec
	}
	return time.Duration(sec) * 2 * time.Second
}

// resolutionSeconds 返回档位桶宽（秒），供区间计算使用。
func resolutionSeconds(r agentmetrics.Resolution) int64 { return r.BucketSeconds() }

// alignDown 把 unix 秒向下对齐到 step 的整数倍（step<=0 时原样返回）。
func alignDown(sec int64, step int64) int64 {
	if step <= 0 {
		return sec
	}
	m := sec % step
	if m < 0 {
		m += step
	}
	return sec - m
}
