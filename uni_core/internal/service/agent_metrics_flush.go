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
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/database"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
)

// ── 消费方窄接口（仓库既有约定：接口定义在消费方）──────────────

// FlushRawReader 是热层原始窗 flush 需要的能力面。
type FlushRawReader interface {
	// Index 返回活跃设备（枚举源）。
	Index(ctx context.Context) ([]uint64, error)
	// BucketRange 读取 [fromMs, toMs) 内的原始样本一次（按 T 升序返回）。
	//
	// 面是「整段」而不是「单桶」：flushDevice 读一次覆盖整轮待处理区间、再在内存里切桶
	// （见那里的注释）。查询服务的资源下钻用的是另一条面（agentmetrics.RawStore.Bucket，
	// 单窗口），两处接口各自独立。
	BucketRange(ctx context.Context, deviceID uint64, fromMs, toMs int64) ([]agentproto.MetricsSample, error)
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

// accrue 把一批的读数并进累计值（回填按批跑时必须逐批累加，否则 stats 只反映最后一批）。
//
// 为什么不做成导出方法：它是两个内部读数的合并规则，不是给外部调用的能力面。
// 逐字段相加而不是「取最大」：DevicesScanned/BucketsWritten 等在多批之间是**不相交**
// 的集合（同一台设备只属于一批），相加才是全轮的真实读数。
func (st FlushStats) accrue(part FlushStats) FlushStats {
	st.DevicesScanned += part.DevicesScanned
	st.BucketsWritten += part.BucketsWritten
	st.BucketsSkipped += part.BucketsSkipped
	st.ResourcesUpserted += part.ResourcesUpserted
	st.Errors += part.Errors
	return st
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
	// cursors 是水位游标的唯一存取实现（键构造 + Lua 原子比较-写，见
	// agentmetrics.CursorStore）。服务侧不再自己拼键、也不再自己 SET。
	cursors *agentmetrics.CursorStore
	cfg     AgentConfigGetter
	log     logger.LoggerInterface
	// now 可注入（测试用固定时钟，区间直接可算）。
	now func() time.Time
	// backfillBatchDevices / pacing / sleep 是回填限速的三个参数（spec §7.1）：
	// 批大小是常量（见 defaultBackfillBatchDevices 的依据），节奏可注入
	// （WithBackfillPacing），sleep 可注入是为了让断言**观测**停顿而不是真的等它
	// （照 WithClock 的先例：时间相关的东西必须能被替身观测）。
	backfillBatchDevices int
	pacing               time.Duration
	sleep                func(time.Duration)
}

func NewAgentMetricsFlushService(raw FlushRawReader, metrics DeviceMetricWriter,
	resources FlushResourceRepo, cfg AgentConfigGetter, log logger.LoggerInterface) *AgentMetricsFlushService {
	return &AgentMetricsFlushService{
		raw: raw, metrics: metrics, resources: resources, cfg: cfg, log: log, now: time.Now,
		backfillBatchDevices: defaultBackfillBatchDevices,
		pacing:               defaultBackfillPacing,
		sleep:                time.Sleep,
	}
}

// WithCursorStore 注入水位游标使用的 Redis 客户端。
//
// 游标与热层原始窗**必须落在同一个 Redis**（都是「agent 上报链路」的键族），
// 故 wireup 传的是同一个客户端；显式注入而不是从 raw 反查，是为了让游标这一层
// 不依赖 FlushRawReader 的具体实现（接口只要 Index/BucketRange 两个能力）。
func (s *AgentMetricsFlushService) WithCursorStore(rdb goredis.Cmdable) *AgentMetricsFlushService {
	s.cursors = agentmetrics.NewCursorStore(rdb)
	return s
}

// WithClock 注入时钟（测试用；生产走 time.Now）。
func (s *AgentMetricsFlushService) WithClock(now func() time.Time) *AgentMetricsFlushService {
	if now != nil {
		s.now = now
	}
	return s
}

// WithBackfillPacing 覆盖回填的**批间停顿**（非正数 = 保持默认，同
// WithEmptyHourGrace 的先例：「传 0」最可能的意思是「忘了填」，静默把节奏关掉
// 等于让限速退化成一次性突发，那正是 spec §7.1 要避免的）。
//
// 批大小不在这里暴露：它是常量（defaultBackfillBatchDevices），理由见那里的注释。
func (s *AgentMetricsFlushService) WithBackfillPacing(d time.Duration) *AgentMetricsFlushService {
	if d > 0 {
		s.pacing = d
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
		// Init 是 SETNX 语义（键缺失才写）+ 回读生效值：已有水位时**绝不覆写**，
		// 且「两个实例同时发现键缺失」时以先写下的那个值为准（不回写自己的候选值）。
		if _, err := s.cursors.Init(ctx, deviceID, agentmetrics.Resolution5m, start); err != nil {
			return fmt.Errorf("agentmetrics flush bootstrap: %w", err)
		}
	}
	return nil
}

// FlushOnce 跑一轮：枚举活跃设备 → 逐设备把已闭桶落成 5min 行。
//
// 返回的 error 是「本轮有失败」的汇总（sentinel：ErrFlushPartial），用于让任务层
// 记日志/告警；**游标语义不受它影响** —— 失败设备的水位留在原处，下轮重试同一批桶。
// 唯一的例外是「命中缺分区」（ErrMetricPartitionMissing）：那时本轮**立即中止**，
// 因为故障域是整个集群的写入，继续跑只会把同一个错误刷 N 遍（spec §7.3）。
func (s *AgentMetricsFlushService) FlushOnce(ctx context.Context) (FlushStats, error) {
	devices, err := s.raw.Index(ctx)
	if err != nil {
		return FlushStats{}, fmt.Errorf("agentmetrics flush: 枚举活跃设备失败: %w", err)
	}
	return s.flushDeviceSet(ctx, devices, s.closedBucketUpper())
}

// closedBucketUpper 返回已闭 5m 桶的半开上界：`alignDown(now − CloseGrace, 300)`。
//
// 一次算好、全设备共用（同一次刷新里所有设备看到的「现在已经闭到哪」必须一致，
// 否则同一轮的设备之间会出现一个桶的相位差）。
func (s *AgentMetricsFlushService) closedBucketUpper() int64 {
	// 宽限的推导与 rollup 共用同一个函数（agentCloseGrace，见 agent_metrics_timing.go）：
	// 两个档位的「当前桶何时算闭」是同一条数据链的上下游，绝不能各写一份。
	return alignDown(s.now().Add(-agentCloseGrace(s.cfg)).Unix(),
		resolutionSeconds(agentmetrics.Resolution5m))
}

// flushDeviceSet 是按**给定设备集合**跑一轮落库：常规 flush 与分批回填共用它。
//
// 为什么要抽出这个入口（而不是让 backfill 自己写循环调 flushDevice）：5m 是唯一真值
// 来源，写路径只能有一条（见 backfill 的注释）。回填需要的是「按批、带节奏地跑标准写
// 路径」，而不是另造一条写路径 —— 这个函数就是那个「标准写路径」的集合版本，
// 批次边界由调用方决定，服务内部没有任何「回填专用」的行为。
func (s *AgentMetricsFlushService) flushDeviceSet(ctx context.Context, devices []uint64,
	upper int64) (FlushStats, error) {

	var stats FlushStats
	stats.DevicesScanned = len(devices)

	failures := 0
	for _, deviceID := range devices {
		written, skipped, upserted, ferr := s.flushDevice(ctx, deviceID, upper)
		stats.BucketsWritten += written
		stats.BucketsSkipped += skipped
		stats.ResourcesUpserted += upserted
		if ferr == nil {
			continue
		}
		failures++
		stats.Errors++
		if errors.Is(ferr, ErrMetricPartitionMissing) {
			// P1：缺分区是整个集群的写入故障（同一分钟里所有设备一起失败），
			// 不是某一台设备的问题 —— 继续跑其余设备只会把同一个错误刷 N 遍，
			// 而每一遍都是必然失败的徒劳重试（spec §7.3「中止本轮，不做 500 次徒劳重试」）。
			// 立即返回哨兵：任务层据此按 P1 告警，而不是当成「部分失败、下轮重试」。
			s.log.Error("agentmetrics flush: 命中「分区缺失」（P1，整个集群的写入都受影响），本轮立即中止",
				zap.Int("devicesScanned", stats.DevicesScanned),
				zap.Int("devicesDone", stats.DevicesScanned-stats.Errors),
				zap.Error(ferr))
			return stats, ferr
		}
		s.log.Error("agentmetrics flush: 设备本轮落库失败，水位留在原处待下轮重试",
			zap.Uint64("deviceId", deviceID), zap.Error(ferr))
	}
	if failures > 0 {
		return stats, fmt.Errorf("%w: %d/%d 台设备落库失败（水位未推进，下轮重试）",
			ErrFlushPartial, failures, stats.DevicesScanned)
	}
	return stats, nil
}

// ErrFlushPartial 表示本轮有设备/桶失败（游标未推进，下轮重试同一批桶）。
var ErrFlushPartial = errors.New("agentmetrics flush: 本轮部分落库失败")

// ErrMetricPartitionMissing 表示写入命中了「没有任何分区能收下这一行」
// （MySQL 1526 / PG 23514，spec §7.3 的「缺分区是硬失败」）。
//
// 为什么要有这个哨兵（而不是把驱动错误原样上抛）：两种失败的**处置完全不同** ——
// 普通写失败是「这台设备下轮重试」，缺分区是「整个集群的写入都写不进去，
// 必须 P1 告警 + 本轮立即中止 + 先跑分区对账」。任务层只有靠 errors.Is 才能区分它们，
// 而按错误串匹配驱动报码是注定要漂移的（见 database.IsMissingPartition 的注释）。
var ErrMetricPartitionMissing = errors.New("agentmetrics flush: 指标分区缺失（P1）")

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

	// 读一次覆盖**整段**待处理区间的原始点，再在内存里切桶。
	//
	// 为什么不能逐桶读（Plan 2E Task 1）：fetch 由「最新点离请求起点多远」推出
	// （见 agentmetrics.BucketRange 的注释）——逐桶读时每个桶都要为「自己离最新点多远」
	// 付一次满额读取，积压越久每桶读得越多：积压 24h 的首轮 288 个桶最坏各读 MaxPoints
	// （10368）条 ≈ 2.5M 条 JSON，且其中绝大多数点被重复读 288 遍。整段读一次时 fetch
	// 只由**最老**的那个桶推出，天然覆盖后面每个桶：读多少与「有几个桶」解耦。
	pts, rerr := s.raw.BucketRange(ctx, deviceID, from*1000, upper*1000)
	if rerr != nil {
		// 读失败**立即返回**：水位留在轮初（下面那句 writeCursor 不会被执行）。
		return written, skipped, upserted, fmt.Errorf("读区间 device=%d [%d,%d): %w",
			deviceID, from, upper, rerr)
	}
	byBucket := groupByBucket(pts, bucketSec)

	lastBucketed := cursor
	for b := from; b < upper; b += bucketSec {
		wide, subs, ok := agentmetrics.Downsample(deviceID, b, byBucket[b])
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
			//
			// 但「缺分区」是**另一类**失败：它的故障域是整个集群（同一分钟里所有设备
			// 一起失败），重试同一批桶一万次也不会成功。故这里把它摘成哨兵上抛，
			// 由 flushDeviceSet 判 P1 并**中止整轮**（spec §7.3）。
			if database.IsMissingPartition(uerr) {
				return written, skipped, upserted, fmt.Errorf("%w: device=%d bucket=%d: %w",
					ErrMetricPartitionMissing, deviceID, b, uerr)
			}
			return written, skipped, upserted, uerr
		}
		lastBucketed = b
		written++
		upserted += n
	}

	// 走到这里说明**本轮每个桶都成功**（空桶也算成功），才前移水位。
	//
	// 前移是**乐观 CAS**：只在「水位仍是轮初读到的 cursor」且「lastBucketed > cursor」
	// 时写入（agentmetrics.CursorStore.Advance）。这一条是「水位只在全成功后前移」
	// 在**实例层**的落点 —— 若本轮跑的过程中有人把水位回退过（backfill 正在发起重放），
	// 我们就**不写**：本轮并没有处理回退出来的那段桶，写上 lastBucketed 等于用一次
	// 合法的前移把那段桶永久跳过（回退就此丢失）。让位只会让下轮重读水位后继续。
	if err := s.writeCursor(ctx, deviceID, cursor, lastBucketed); err != nil {
		return written, skipped, upserted, err
	}
	return written, skipped, upserted, nil
}

// groupByBucket 把一批原始点按 `T/1000/bucketSec*bucketSec` 归到桶起点上（纯函数，便于单测）。
//
// 为什么按**绝对时间对齐**而不是「相对 from 的偏移」：桶起点活在全局 5min 栅格上
// （水位游标也在同一条栅格上），只有按绝对栅格对齐才能保证「同一个点在逐桶读与整段读里
// 落进同一个桶」——这正是改造后行为等价的前提。
//
// 为什么这里**不再判界**：BucketRange 已按 [fromMs, toMs) 过滤（只有区间内的点会被返回），
// 在这里再写一遍判界等于给同一件事留第二份会漂移的口径。切桶时用到的桶起点 b 一定是
// 区间内的 5min 栅格点（循环本身就是 `from + k×bucketSec`），故 byBucket[b] 不会漏。
//
// 每个切片保持输入顺序：BucketRange 已按 T 升序返回，于是桶内也是升序 ——
// Downsample 的 LAST（桶内 T 最大的样本）正好落在切片末尾。
func groupByBucket(pts []agentproto.MetricsSample, bucketSec int64) map[int64][]agentproto.MetricsSample {
	out := make(map[int64][]agentproto.MetricsSample)
	for _, p := range pts {
		b := p.T / 1000 / bucketSec * bucketSec
		out[b] = append(out[b], p)
	}
	return out
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
//
// 「写回」走 CursorStore.Init（SETNX + 回读生效值）：两个实例同时发现键缺失时，
// 返回值是**先写下的那个值**，故两边本轮用的是同一个区间起点。
func (s *AgentMetricsFlushService) readCursor(ctx context.Context, deviceID uint64) (int64, error) {
	v, ok, err := s.cursors.Read(ctx, deviceID, agentmetrics.Resolution5m)
	if err != nil {
		// Redis 故障**必须上抛**：把它当成「没有游标」会让整轮从 24h 前重算并
		// 回写更小的水位，已落库的桶被重写、且这是不可观测的降级。
		return 0, err
	}
	if ok {
		return v, nil
	}
	return s.cursors.Init(ctx, deviceID, agentmetrics.Resolution5m, s.bootstrapStart())
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

// writeCursor 前移水位：只在「当前值仍是 expected（轮初读到的水位）」且 next > expected
// 时写入（Lua 原子比较-写，见 agentmetrics.CursorStore.Advance）。
//
// 两个参数都不可省：
//   - expected 必须是**本轮真正读到的**值。传推导值、传「上一轮的值」都会让 CAS 失真；
//   - next 是本轮实际成功处理到的最后一个桶。
//
// 旧实现的 `Set(key, lastBucketed, 0)` 是**无条件**写入，而 lastBucketed 是由轮初游标
// 推导出来的：轮初读到 H，期间 backfill 把水位回退到 T（准备重放 [T+300, H]），
// 本轮结束时那句无条件 SET 会把水位推回 now−300 —— 回退出来的桶本轮没被处理，
// 水位却越过了它们，于是「待重放的桶」永远不会被 flush 处理。
func (s *AgentMetricsFlushService) writeCursor(ctx context.Context, deviceID uint64,
	expected, next int64) error {

	if _, err := s.cursors.Advance(ctx, deviceID, agentmetrics.Resolution5m, expected, next); err != nil {
		return err
	}
	return nil
}

// rewindCursor 回退水位（backfill 专用）：仅当 target 比当前值更旧时才写入。
func (s *AgentMetricsFlushService) rewindCursor(ctx context.Context, deviceID uint64, target int64) (bool, error) {
	return s.cursors.Rewind(ctx, deviceID, agentmetrics.Resolution5m, target)
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
