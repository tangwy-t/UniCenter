package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"go.uber.org/zap"

	goredis "github.com/redis/go-redis/v9"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agentmetrics"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
)

// ── 消费方窄接口（仓库既有约定：接口定义在消费方）──────────────

// RollupDeviceSource 是回滚的设备枚举源：返回「可能有 5m 行」的设备。
//
// 为什么只要 Index（而不是复用 FlushRawReader）：rollup 的输入是**库里的 5m 行**，
// 它不读热层原始窗；把 Bucket 也挂进这个接口，等于在签名上给「rollup 去 Redis 取数」
// 留一条缝，而那条路径会让 1h 行依赖一个只有 24h 保留期的数据源。
type RollupDeviceSource interface {
	Index(ctx context.Context) ([]uint64, error)
}

// RollupMetricRepo 是 rollup 的读写能力面（唯二两个方法，都在仓储里）。
//
// 刻意**不含** WriteBucket：rollup 只写 1h（`WriteHour` 是**独立事务 B**），
// 5m 由 flush 写、且是唯一真值来源 —— Plan 2A 的「两段提交」在这里表现为
// 「rollup 的手上根本没有能碰 5m 的方法」。
type RollupMetricRepo interface {
	// ReadWideRows 读回某设备在某区间的宽表行（按 bucket_ts 升序，**闭区间**）。
	ReadWideRows(ctx context.Context, table string, deviceID uint64, from, to int64) ([]entity.DeviceMetricWide, error)
	// WriteHour UPSERT 一行 1h 宽表行。
	WriteHour(ctx context.Context, w *entity.DeviceMetricWide) error
	// ExistingBucketTimestamps 返回某设备在某张宽表上 `[from,to]` 内已有行的 bucket_ts（升序，闭区间）。
	//
	// 它是「按需重扫」做集合差的那条读（见 rescanDevice）：只要时间戳，不要值列、
	// 也不要行数 —— 视图层的理由写在仓储的同名方法注释里（`SELECT bucket_ts` 一条）。
	ExistingBucketTimestamps(ctx context.Context, table string, deviceID uint64, from, to int64) ([]int64, error)
}

// ── 统计 ───────────────────────────────────────────────

// RollupStats 是一轮的统计（任务与测试都读它）。
type RollupStats struct {
	// HoursScanned 是本轮被检查的小时数（普通游标区间 + repair 集合），空小时也计入。
	//
	// 「按需重扫」（第 3 步）**不**计入这里：它是按「集合差」记账的（见 HoursRescanned），
	// 窗口里已齐全的小时只被两条集合查询看见、没有任何逐小时的工作。本字段的语义与既有
	// 断言绑定，不因为多出一条路径而被摊薄。
	HoursScanned int
	// HoursWritten 是成功写出 1h 行的小时数（**含** repair 重算与重扫补出的那些）。
	HoursWritten int
	// HoursRepaired 是本轮从 repair 集合里重算并成功写出的小时数（HoursWritten 的子集）。
	HoursRepaired int
	// HoursSkipped 是没有 5m 行、故不写行的小时数。其中**仍处在等待窗口内**的空小时
	// （见 emptyHourGrace）会让水位停在它之前、下一轮重试它；已超出窗口的空小时
	// 才照常让水位越过（否则一台离线设备会把水位永久卡死）。
	HoursSkipped int
	// HoursRescanned 是本轮「按需重扫」**实际送去重算**的小时数 —— 即「有 5m 行却缺 1h 行」
	// 的差集大小（已与本轮的 repair 集合、普通区间去重）。窗口内**已齐全**的小时不计入：
	// 它们只被两条集合查询看见，不产生任何重算与写入。
	HoursRescanned int
	// HoursBackfilled 是本轮重扫真正**补出** 1h 行的小时数（HoursRescanned 与 HoursWritten
	// 的子集）。正常的一轮它是 0（集合差为空 = 没有空洞）；只有迟到落库的小时才会让它非零。
	HoursBackfilled int
	// Errors 是本轮失败次数（每台失败设备 +1）。
	Errors int
}

// AgentMetricsRollupService 把 5min 指标行回滚成 1h 指标行，并负责 repair 重算。
//
// 与 flush 的关系：flush 写 5m（事务 A，唯一真值来源）、rollup 写 1h（事务 B，**可推导**数据），
// 二者消费同一族游标（`cursor_5m` / `cursor_1h`）但互不阻塞 —— 这正是两段提交的意义：
// 1h 写失败绝不能回滚 5m，1h 行缺失也绝不能阻塞 5m 的落库。
//
// 为什么要 repair（而不是「小时关了就只算一次」）：某个小时的 5m 行可能在小时关闭**之后**
// 才落库（flush 滞后、分区修复后重试、限速回填、同桶 UPSERT 覆盖），若不再重算，
// 该小时的 1h 行会**永久停在残缺值且完全不可见**。故：不完整的小时先写出去（宁可残缺，
// 不要空洞），同时把「小时桶起点」记进该设备的 repair 集合，下一轮**优先重算**它。
type AgentMetricsRollupService struct {
	metrics RollupMetricRepo
	devices RollupDeviceSource
	// rdb 只服务 repair 集合（SMembers/SAdd/SRem 一族）；水位游标一律走 cursors。
	rdb goredis.Cmdable
	// cursors 是与 flush 共用的水位游标存取实现（键构造 + Lua 原子比较-写）。
	cursors *agentmetrics.CursorStore
	cfg     AgentConfigGetter
	log     logger.LoggerInterface
	// now 可注入（测试用固定时钟，区间直接可算）。
	now func() time.Time
	// maxRepairHours 是 repair 集合的上限（默认 defaultMaxRepairHours）。
	maxRepairHours int
	// emptyHourGrace 是「空小时」的等待窗口（默认 defaultEmptyHourGrace，见 emptyHourWithinGrace）。
	emptyHourGrace time.Duration
}

// defaultMaxRepairHours 是 repair 集合的上限（720 = 30 天的小时数，与计划一致）。
//
// 为什么必须有上限：repair 成员是「修不完就留着」的，一台长期缺行的设备（例如每天只上报
// 半天）会持续产生不完整小时；没有上限时集合会无限增长，每轮的重算代价也跟着无限增长。
const defaultMaxRepairHours = 720

// defaultEmptyHourGrace 是空小时的等待窗口（2 小时）。
//
// **它的存在是为了闭合「5m 迟到 → 1h 永久空洞」这个缺口**：没有它时，一个「一行 5m 都没有」
// 的小时会被当成「确实没有数据」，于是水位**越过**它；等该小时的 5m 行稍后落库
// （服务重启后 flush 从 24h 前补齐、而 rollup 从自己的游标继续；或 flush 落后 rollup 一轮），
// 普通区间（下界恒为 cursor+3600）永远不会再回头看它 —— 而 >30 天的窗口**只有 1h 表可查**
// （5m 只留 30 天），那就是一个静默的数据空洞（在 5m 保留期内它靠重扫与本文件的
// RewindHours 还能补回来，见下一条注释；超出保留期才是真的补不回来）。
//
// 为什么恰好是 2 小时：flush 是每 5 分钟一轮的定时任务（`0 */5 * * * *`），服务重启后的补齐
// 从 24h 前开始；故「5m 迟到」的正常上界是「一轮 5 分钟 + 一个小时的闭桶宽限（CloseGrace）」，
// 满打满算十几分钟。2 小时是它的约 10 倍余量（够覆盖一次慢查询、一次 Redis 抖动、一次
// 部署重启），同时又是**有界**的：设备真的离线时，水位最多推迟 2 小时才越过那个空小时，
// 不会让游标永久卡死在第一个空小时上（那是「等一下」与「永久等待」的区别）。
//
// 必须 > closeGrace（2×reportInterval，默认 20 秒），否则等待窗口比「小时什么时候算闭」
// 还短，等于没等 —— 默认值之间差了三个数量级，不存在这个风险。
const defaultEmptyHourGrace = 2 * time.Hour

// configAgentRollupRescanHours 是「按需重扫」窗口小时数的配置键。
//
// 为什么做成配置而不是常量：窗口长度是运维取舍（「愿意为迟到数据兜底多久」），
// 与保留期（sys.agent.historyRetentionDays / metrics1hRetentionDays）、上报间隔一样
// 属于随部署环境变化的参数。
//
// 注意：本键**没有**迁移种子（v011 已存在，新增 v012 会影响其它任务的编号约定）。
// 缺键时 GetInt 回落 defaultRollupRescanHours —— 与 v008/v009 种子的 sys.agent.* 键
// 同一用法：配置面板里加一条即可覆盖，不加就是默认 24h。
const configAgentRollupRescanHours = "sys.agent.rollupRescanHours"

// defaultRollupRescanHours 是重扫窗口的缺省小时数（24 = 一天）。
//
// 为什么是 24：① flush 是每 5 分钟一轮、服务重启后从 **24h 前**开始补齐
// （bootstrapWindow），故「5m 迟到」的正常上界就是一天；② 窗口越大，集合差那两条查询
// 要读回的时间戳越多（每设备每小时 5m 12 个 + 1h 1 个），24h 是「兜住一整天的迟到」
// 与「查询代价有界」的折中。
//
// 更老的空洞是**明确的能力边界**（有断言钉住，不是 bug）：重扫窗口之外的自愈要运维介入
// —— 用 `agent-metrics-backfill` 的 `{"hours":N,"resolution":"1h"}` 回退 `cursor_1h`
// （窗口受 5m 保留期约束，默认 30 天），下一轮本服务的普通区间就会重新走到那些小时。
// 也就是说「>24h 的 1h 空洞」现在有入口，**不再需要手工改 Redis 键**；超出 5m 保留期
// （重算的输入已被回收）才是永久缺失。
//
// 为什么 1h 的重算能走这么远（而 5m 只能走 24h）：本服务的输入是**库里的 5m 行**
// （rollupHour → ReadWideRows），不是 Redis 里的原始点。
const defaultRollupRescanHours = 24

// rescanWindowHours 返回重扫窗口的小时数（配置缺键/非法值 → 默认 24）。
//
// 非法值（<=0）回落默认而不是「关掉重扫」：与 WithMaxRepairHours / WithEmptyHourGrace
// 同一取向 —— 传 0 最可能的意思是「忘了填」，而静默关掉重扫等于退回「1h 空洞不自愈」
// 的老缺陷（那正是按需重扫存在的唯一理由）。
func (s *AgentMetricsRollupService) rescanWindowHours() int {
	n := s.cfg.GetInt(context.Background(), configAgentRollupRescanHours, defaultRollupRescanHours)
	if n <= 0 {
		n = defaultRollupRescanHours
	}
	return n
}

// NewAgentMetricsRollupService 装配回滚服务。
//
// raw 是设备枚举源（生产传 `*agentmetrics.RawStore`）；metrics 是 5m 读 + 1h 写的出口
// （生产传 `*repository.DeviceMetricRepo`）。游标与 repair 集合所在的 Redis 由
// WithCursorStore 注入。
func NewAgentMetricsRollupService(metrics RollupMetricRepo, raw RollupDeviceSource,
	cfg AgentConfigGetter, log logger.LoggerInterface) *AgentMetricsRollupService {
	return &AgentMetricsRollupService{
		metrics: metrics, devices: raw, cfg: cfg, log: log,
		now: time.Now, maxRepairHours: defaultMaxRepairHours,
		emptyHourGrace: defaultEmptyHourGrace,
	}
}

// WithCursorStore 注入水位游标与 repair 集合使用的 Redis 客户端。
//
// 显式注入而不是从 metrics 反查：游标/repair 是「agent 上报链路的键族」，
// 与它们所在的 Redis 必须和 flush 用的是**同一个**（否则两个档位的水位会分裂到两处）。
func (s *AgentMetricsRollupService) WithCursorStore(rdb goredis.Cmdable) *AgentMetricsRollupService {
	s.rdb = rdb
	s.cursors = agentmetrics.NewCursorStore(rdb)
	return s
}

// WithClock 注入时钟（测试用；生产走 time.Now）。
func (s *AgentMetricsRollupService) WithClock(now func() time.Time) *AgentMetricsRollupService {
	if now != nil {
		s.now = now
	}
	return s
}

// WithMaxRepairHours 覆盖 repair 集合的上限（非正数 = 保持默认，不静默变成 0）。
func (s *AgentMetricsRollupService) WithMaxRepairHours(n int) *AgentMetricsRollupService {
	if n > 0 {
		s.maxRepairHours = n
	}
	return s
}

// WithEmptyHourGrace 覆盖空小时的等待窗口（非正数 = 保持默认，同 WithMaxRepairHours 的先例：
// 「传 0」最可能的意思是「忘了填」，静默把窗口关掉会让本服务退回到「1h 永久空洞」的老缺陷，
// 故只接受正数）。
func (s *AgentMetricsRollupService) WithEmptyHourGrace(d time.Duration) *AgentMetricsRollupService {
	if d > 0 {
		s.emptyHourGrace = d
	}
	return s
}

// ErrRollupPartial 表示本轮有设备失败（下轮重试同一批小时）。
//
// 水位语义：普通区间/repair 失败 ⇒ 该设备水位留在轮初（下轮重算同一批小时）；
// **只有按需重扫失败**时水位已经按普通区间的结果前移了 —— 重扫排在乐观 CAS 之后、
// 不参与水位判定，而它没补上的那些小时会被下一轮的集合差重新发现（重试语义不变）。
var ErrRollupPartial = errors.New("agentmetrics rollup: 本轮部分回滚失败")

// RollupOnce 跑一轮：枚举设备 → 逐设备回滚已闭小时 → 重算 repair 集合里的小时 → 按需重扫补空洞。
//
// 返回的 error 是「本轮有失败」的汇总（sentinel：ErrRollupPartial），用于让任务层
// 记日志/告警；**游标语义不受它影响** —— 失败设备的水位留在原处（唯一的例外是「只有按需重扫
// 失败」：它排在普通区间的乐观 CAS **之后**，水位已经按普通区间前移；见 rollupDevice 的第 3 步）。
func (s *AgentMetricsRollupService) RollupOnce(ctx context.Context) (RollupStats, error) {
	var stats RollupStats

	devices, err := s.devices.Index(ctx)
	if err != nil {
		return stats, fmt.Errorf("agentmetrics rollup: 枚举设备失败: %w", err)
	}
	upper := s.closedHourUpper()

	failures := 0
	for _, deviceID := range devices {
		part, derr := s.rollupDevice(ctx, deviceID, upper)
		stats.HoursScanned += part.HoursScanned
		stats.HoursWritten += part.HoursWritten
		stats.HoursRepaired += part.HoursRepaired
		stats.HoursSkipped += part.HoursSkipped
		stats.HoursRescanned += part.HoursRescanned
		stats.HoursBackfilled += part.HoursBackfilled
		if derr != nil {
			failures++
			stats.Errors++
			s.log.Error("agentmetrics rollup: 设备本轮回滚失败，水位留在原处待下轮重试",
				zap.Uint64("deviceId", deviceID), zap.Error(derr))
		}
	}
	if failures > 0 {
		return stats, fmt.Errorf("%w: %d/%d 台设备回滚失败（普通区间与 repair 失败的水位未推进，下轮重试；"+
			"重扫失败的已按普通区间前移水位，但那个空洞会在下一轮被集合差重新发现）",
			ErrRollupPartial, failures, len(devices))
	}
	return stats, nil
}

// ─ 单设备 ─────────────────────────────────────────────

// rollupDevice 处理单台设备：先重算 repair 集合里的已闭小时，再走普通游标区间，最后按需重扫。
//
// 三个区间：
//
//	普通区间 = [cursor + 3600, upper)   —— cursor 是「已成功回滚到（含）」的小时
//	repair   = 集合里 < upper 的小时    —— 它们的起点在游标**之前**，普通区间永远不会再碰它们
//	重扫     = [alignDown(now,3600) − rescanHours×3600, upper) —— 见第 3 步（只补行，不碰水位）
//
// **水位只在「本轮所有（普通区间内的）小时都成功」后才前移**：任一步失败 → 水位留在
// 轮初的值，下轮从同一个小时重来；因此一旦出错就立即返回、绝不继续往后写（继续写只会让
// 后面那些小时在下一轮被重复写一遍，而水位又停在轮初）。空小时分两种（这是本修复的核心）：
//   - 仍在等待窗口内（emptyHourWithinGrace）→ 水位**不越过它**，下一轮重试同一小时
//     （它的 5m 行可能只是迟到，越过它就等于制造 1h 表的永久空洞）；
//   - 已超出等待窗口 → 视为确实没有数据（设备离线等），照常让水位越过它，
//     否则一台离线设备会把水位永久卡死在第一个空小时上。
//
// 唯一不参与水位判定的失败是第 3 步（按需重扫，排在乐观 CAS 之后）。
func (s *AgentMetricsRollupService) rollupDevice(ctx context.Context, deviceID uint64,
	upper int64) (RollupStats, error) {

	var stats RollupStats
	hourSec := resolutionSeconds(agentmetrics.Resolution1h)

	cursor, err := s.readCursor(ctx, deviceID, upper)
	if err != nil {
		return stats, err
	}

	// already 记「本轮已经算过的小时」（repair 集合 ∪ 普通区间）：第 3 步按集合差挑出待补的
	// 小时后先用它去重 —— 同一小时一轮只写一次（写虽是幂等 UPSERT，但重复写是白费的代价，
	// 也会让 HoursWritten / HoursRescanned 的计数失真）。
	already := make(map[int64]bool)

	// 1) repair 优先：这些小时的水位已经在游标之前，不主动重算就永远不会被更新。
	//
	// 空小时的等待窗口（emptyHourGrace）在这里**不适用**：repair 里的小时都在游标之前，
	// 普通区间已经越过它们，扣住它们拦不住任何东西（水位不会再前移）；而那条路径上的
	// 「空」有另一种明确含义 —— 该小时的 5m 行已被保留期回收，再也修不好了。
	repairs, err := s.repairHours(ctx, deviceID, upper)
	if err != nil {
		return stats, err
	}
	for _, h := range repairs {
		already[h] = true
		stats.HoursScanned++
		written, skipped, herr := s.rollupHour(ctx, deviceID, h, true)
		if herr != nil {
			// 与普通区间同理：失败立即返回，水位留在轮初。
			return stats, herr
		}
		switch {
		case written:
			stats.HoursWritten++
			stats.HoursRepaired++
		case skipped:
			stats.HoursSkipped++
		}
	}

	// 2) 普通游标区间 [cursor+3600, upper)
	//
	// 水位是**单一**游标，语义是「已成功回滚到（含）的小时」，因此它只能落在一个
	// **连续成功前缀**的末尾：一旦遇到「没有 5m 行、但还没过等待窗口」的小时
	// （emptyHourWithinGrace），水位就停在它之前，绝不越过它。
	//
	// 被扣住之后**仍然继续处理后续小时**（不是 break）：后续小时若已有 5m 行就照常写出
	// 1h 行 —— 同一取向「宁可先有残缺值也不要空着」，且写路径是 UPSERT，下一轮重扫到
	// 它们只是重写一遍（代价 ≤ 等待窗口，即最多几个 1h 行）。若在这里 break，设备离线
	// 一小时的期间，后面那些**有数据**的小时也要陪着空等满 2 小时才写出 1h 行。
	// 唯一的差别是：被扣住之后的小时**不再带动水位**（`last` 冻结）。
	last := cursor
	blockedAt := int64(-1) // 本轮第一个被扣住的小时（-1 = 没有）
	for h := cursor + hourSec; h < upper; h += hourSec {
		already[h] = true
		stats.HoursScanned++
		written, skipped, herr := s.rollupHour(ctx, deviceID, h, false)
		if herr != nil {
			return stats, herr
		}
		switch {
		case written:
			stats.HoursWritten++
			if blockedAt < 0 {
				last = h
			}
		case skipped:
			stats.HoursSkipped++
			if s.emptyHourWithinGrace(h) {
				if blockedAt < 0 {
					blockedAt = h
					s.log.Debug("agentmetrics rollup: 空小时仍在等待窗口内，水位停在它之前待下轮重试",
						zap.Uint64("deviceId", deviceID), zap.Int64("hour", h),
						zap.Duration("grace", s.emptyHourGrace))
				}
				continue
			}
			if blockedAt < 0 {
				last = h
			}
		}
	}

	// 走到这里说明普通区间里每个小时都成功（空小时也算成功），才前移水位。
	//
	// 前移走同一族游标的乐观 CAS（expected = 轮初读到的 cursor）：与 flush 同一个
	// 原语、同一条理由 —— 1h 水位不会被回退（repair 是另一族键），但复用同一实现
	// 能让「游标只能被原子地比较-写」这条不变量在两个档位上一致成立，
	// 未来给 1h 加任何回退/重放能力时不会再长出第二份读-比-写。
	if last != cursor {
		if err := s.writeCursor(ctx, deviceID, cursor, last); err != nil {
			return stats, err
		}
	}

	// 3) 按需重扫：把**迟到落库**的 5m 行补成 1h 行（只补行，不碰水位）。
	//
	// 为什么排在**水位 CAS 之后**：硬约束是「重扫只补行，水位由普通区间与 repair 管」。
	// CAS 在前意味着水位是**普通区间结果的纯函数** —— 重扫无论成功、失败、还是什么都没补，
	// 都不可能让它动一格。（若排在 CAS 之前，重扫失败会让本该前移的水位停住，水位就又变成
	// 三条路径共同决定的量了。）
	//
	// 代价（刻意接受、且已写入 ErrRollupPartial 的注释与文案）：只有重扫失败时，本轮会以
	// ErrRollupPartial 上报，而水位已按普通区间前移 —— 重扫只补历史空洞，不影响「已回滚到
	// 哪个小时」的记账；它没补上的小时会被下一轮的集合差重新发现。
	rescanned, backfilled, rerr := s.rescanDevice(ctx, deviceID, upper, already)
	stats.HoursRescanned += rescanned
	stats.HoursBackfilled += backfilled
	stats.HoursWritten += backfilled
	if rescanned > 0 {
		s.log.Debug("agentmetrics rollup: 按需重扫发现迟到落库的 1h 空洞",
			zap.Uint64("deviceId", deviceID), zap.Int("hoursRescanned", rescanned),
			zap.Int("hoursBackfilled", backfilled))
	}
	if rerr != nil {
		return stats, rerr
	}
	return stats, nil
}

// emptyHourWithinGrace 判断一个「一行 5m 都没有」的小时是否还处在等待窗口内
// （true = 数据可能还没到齐，水位不得越过它）。
//
// 判据用「小时**结束**时刻距今多久」（age = now − (hour+3600)）而不是「小时开始时刻」：
// 小时是**闭**的（h < upper = alignDown(now − CloseGrace)），所以从「小时结束」算起才是
// 「这一小时的数据已经等多久了」—— 迟到的那部分正是发生在它关闭**之后**。
//
// 边界取严格小于（age < emptyHourGrace）：恰好等于窗口时**不再等**。这样 grace 只有一个
// 含义（「最多等这么久」），且不会出现「grace 是 2h 却等了 2h+1s」这种说不清的值。
func (s *AgentMetricsRollupService) emptyHourWithinGrace(hour int64) bool {
	end := hour + resolutionSeconds(agentmetrics.Resolution1h)
	age := time.Duration(s.now().Unix()-end) * time.Second
	return age < s.emptyHourGrace
}

// rollupHour 回滚单个小时：读 5m 行 → RollupToHour → 写 1h 行（+ 维护 repair 集合）。
//
// 返回 (是否写出, 是否空小时, 错误)。fromRepair 标记这个小时来自 repair 集合。
//
// **repair 集合不变式**：1h 行写成功后，该小时在集合里的成员资格 == RollupInfo.NeedRepair。
// 于是「修好了就自动出队」「还没修好就留在队里下轮再来」，而集合永远不会被
// 「已经完整的完成项」占满（那是用户点名的失效模式）。
func (s *AgentMetricsRollupService) rollupHour(ctx context.Context, deviceID uint64,
	hour int64, fromRepair bool) (written, skipped bool, err error) {

	// 区间是**闭区间** [hour, hour+3300]：一个整小时正好 12 个 5m 行，端点必须含在内
	// （ReadWideRows 的注释写明了这一点；少读一个端点会让每个小时都判成不完整）。
	to := hour + resolutionSeconds(agentmetrics.Resolution1h) - resolutionSeconds(agentmetrics.Resolution5m)
	rows, err := s.metrics.ReadWideRows(ctx, entity.TableNameMetric5m, deviceID, hour, to)
	if err != nil {
		return false, false, fmt.Errorf("读 5m 行 device=%d hour=%d: %w", deviceID, hour, err)
	}
	if len(rows) == 0 {
		// 空小时不写行（RollupToHour 的契约：rows 为空 → 调用方跳过写路径）。
		//
		// 但**在 repair 路径上**，空小时意味着这个小时的 5m 行已经不存在了
		// （分区保留期回收、设备被删除连带清理）：它再也修不好了，留在集合里只会
		// 永久占位。故从集合里摘掉 —— 但已写出的 1h 行**不删**（残缺值好过空洞）。
		if fromRepair {
			if derr := s.dropRepairHour(ctx, deviceID, hour); derr != nil {
				return false, true, derr
			}
		}
		return false, true, nil
	}

	wides := make([]agentmetrics.Wide, 0, len(rows))
	for i := range rows {
		w, cerr := repository.WideFromEntity(&rows[i])
		if cerr != nil {
			return false, false, fmt.Errorf("宽行转换 device=%d hour=%d: %w", deviceID, hour, cerr)
		}
		wides = append(wides, w)
	}

	// 聚合口径全部由 agentmetrics.RollupToHour 提供（Plan 2B 已定：均值按 samples 加权、
	// 单调量 LAST、温度 max、5min-only 列置 NULL、比值由 Σ 重算）—— 这里只负责喂数据与写库，
	// 绝不在这层重新实现聚合。
	wide, info := agentmetrics.RollupToHour(hour, wides)

	row, cerr := repository.EntityFromWide(wide)
	if cerr != nil {
		return false, false, fmt.Errorf("宽行转换 device=%d hour=%d: %w", deviceID, hour, cerr)
	}
	if werr := s.metrics.WriteHour(ctx, row); werr != nil {
		return false, false, fmt.Errorf("写 1h 行 device=%d hour=%d: %w", deviceID, hour, werr)
	}

	if info.NeedRepair {
		if aerr := s.addRepairHour(ctx, deviceID, hour); aerr != nil {
			return true, false, aerr
		}
	} else {
		if !info.NeedRepair && info.SampleSum > 0 {
			// 「12 行齐但 Σsamples 偏低」是一个真实异常（桶里丢样本、agent 上报间隔被改大），
			// 但它**不**作为 repair 触发条件：reportInterval 是可热更配置，若按
			// Σsamples != ExpectedSamplesPerHour 判 repair，一台按 30s 上报的设备
			// 每个小时都会被判成待修，repair 集合会被「永远修不完的完成项」占满。
			// 故只把它变成一条可见的告警（每个小时只会告警一次：游标会把已写过的小时抛在后面）。
			if expected := s.expectedSamplesPerHour(); expected > 0 && info.SampleSum < expected {
				s.log.Warn("agentmetrics rollup: 12 个 5m 行齐但 Σsamples 低于完整小时应有值（样本偏薄，不入 repair）",
					zap.Uint64("deviceId", deviceID), zap.Int64("hour", hour),
					zap.Int("sampleSum", info.SampleSum), zap.Int("expected", expected))
			}
		}
		if fromRepair {
			if derr := s.dropRepairHour(ctx, deviceID, hour); derr != nil {
				return true, false, derr
			}
		}
	}
	return true, false, nil
}

// ─ 按需重扫（让 1h 空洞自愈）────────────────────────────

// rescanDevice 是「按需重扫」：把**迟到落库**的 5m 行补成 1h 行。只补行，**绝不碰水位**。
//
// 为什么必须有它（这是本方法存在的全部理由）：普通区间对「当时一行 5m 都没有」的小时是
// 「在 emptyHourGrace 内等，等不到就**越过它**」—— 那一步是必要的（否则一台离线设备会把
// 水位永久卡死在第一个空小时上）。可那些 5m 行**可能后来才落库**（flush 因故停顿数小时后
// 补上、积压的原始点首轮才落库，都晚于 2 小时的等待窗口）：普通区间的下界恒为
// `cursor+3600`，永远不会回头看它；repair 集合也兜不住它（repair 只装「写出过残缺行」的
// 小时）。于是那个小时在 1h 表里**永久缺失** —— 而 >30 天的窗口**只有 1h 表可查**，
// 那段时间在控制台上就是一片静默的空洞。
//
// 为什么用「集合差」而不是「无脑重扫窗口里的 K 个小时」：无脑重扫每轮对每设备写 K 个
// 1h 行（500 台 × 24 小时 = **12000 次 UPSERT / 5 分钟**，全是把已经正确的值原样写回去），
// 而集合差只要**两次读**（_5m 与 _1h 各一条 `SELECT bucket_ts`），且只为**真正缺失**的小时
// 写 —— 完全正常的一轮是 0 次写。
//
// 窗口 = `[alignDown(now,3600) − rescanHours×3600, alignDown(now,3600))`（rescanHours 见
// configAgentRollupRescanHours，默认 24）：上界就是**当前正在填充的那个小时**，它不在窗口里。
// 「只补**已闭合**的小时」这件事由 missingHours 的 `h >= upper` 过滤保证 —— 那里是**唯一**的
// 守卫（纯函数、有单测、端到端也有断言）：在「当前小时的头 CloseGrace 秒」里
// alignDown(now,3600) 会比 upper 大 1 小时，那 1 个小时正是靠那条过滤挡掉的；否则重扫会为
// 仍在宽限期、随时可能再收到数据的小时写出一个残缺的 1h 行。
//
// 返回 (实际送去重算的小时数, 其中补出 1h 行的小时数, 错误)。
func (s *AgentMetricsRollupService) rescanDevice(ctx context.Context, deviceID uint64,
	upper int64, already map[int64]bool) (rescanned, backfilled int, err error) {

	if upper <= 0 {
		return 0, 0, nil
	}
	hourSec := resolutionSeconds(agentmetrics.Resolution1h)
	from := alignDown(s.now().Unix(), hourSec) - int64(s.rescanWindowHours())*hourSec
	// 仓储是**闭区间** [from,to]，故 to 取「窗口上界 − 1」把半开区间 [from, alignDown(now,3600))
	// 还原出来（少减这 1 秒会把正好落在上界的那一批行也读回来）。
	to := alignDown(s.now().Unix(), hourSec) - 1
	if to < from {
		return 0, 0, nil
	}

	// 两条集合查询（不 COUNT(*)、不读整行）：一边是「有 5m 行的小时」，一边是「已有 1h 行的小时」。
	fiveMin, err := s.metrics.ExistingBucketTimestamps(ctx, entity.TableNameMetric5m, deviceID, from, to)
	if err != nil {
		return 0, 0, fmt.Errorf("重扫读 5m 桶集合 device=%d: %w", deviceID, err)
	}
	if len(fiveMin) == 0 {
		// 窗口里一条 5m 行都没有 → 差集必然为空：省掉第二条查询。
		// 真离线/新设备（没有任何 5m 行）走的就是这条早退，每轮代价只有一次索引区间扫描。
		return 0, 0, nil
	}
	oneHour, err := s.metrics.ExistingBucketTimestamps(ctx, entity.TableNameMetric1h, deviceID, from, to)
	if err != nil {
		return 0, 0, fmt.Errorf("重扫读 1h 桶集合 device=%d: %w", deviceID, err)
	}

	for _, h := range missingHours(fiveMin, oneHour, upper, already) {
		rescanned++
		// fromRepair=false：这些小时**不在** repair 集合里（在的话第 1 步已经算过、已被 already
		// 排除）。传 false 让 rollupHour 不去动集合 —— 重扫只补行，repair 集合的增删仍只由
		// 「写侧发现残缺」与「repair 侧重算」决定。
		written, skipped, herr := s.rollupHour(ctx, deviceID, h, false)
		if herr != nil {
			return rescanned, backfilled, fmt.Errorf("重扫补 1h 行 device=%d hour=%d: %w", deviceID, h, herr)
		}
		switch {
		case written:
			backfilled++
		case skipped:
			// 差集说这个小时有 5m 行，rollupHour 却读回空：只可能是两次读之间那批行被删了
			// （分区保留期回收、设备被清理）。这不是错误 —— 下一轮的集合差自然不再包含它。
			s.log.Debug("agentmetrics rollup: 重扫的小时按集合差应有 5m 行，读回却是空（可能是保留期回收）",
				zap.Uint64("deviceId", deviceID), zap.Int64("hour", h))
		}
	}
	return rescanned, backfilled, nil
}

// missingHours 返回「有 5m 行、却没有 1h 行」的小时（升序）—— 重扫的**差集**。
//
// 做成未导出的纯函数：它把「该怎么算缺失」这件事从 I/O 里摘出来，四条过滤规则因此
// 可以被逐条钉住（而不是只能靠一个端到端场景间接覆盖）。
//   - `alignDown(ts, 3600)`：5m 行的 bucket_ts 是 300 的倍数，必须落到小时上；
//     1h 行本就是小时起点，对齐是无害的归一（两张表共用同一段区间查询）。
//   - `h >= upper` 丢弃：只补**已闭合**的小时（与普通区间/repair 同一口径）。这是
//     「不碰未闭合小时」的**唯一**守卫（窗口上界是 alignDown(now,3600)，在「当前小时的头
//     CloseGrace 秒」里它比 upper 大 1 小时），故它必须留在本函数里、不依赖调用方的区间算术。
//   - `already[h]` 丢弃：本轮已经算过的小时（repair 集合 ∪ 普通区间）绝不重复写。
//   - `seen[h]`：同一小时的多行 5m 只产出一个小时（差集是**小时的集合**，不是行集合）。
func missingHours(fiveMin, oneHour []int64, upper int64, already map[int64]bool) []int64 {
	hourSec := resolutionSeconds(agentmetrics.Resolution1h)
	have := make(map[int64]bool, len(oneHour))
	for _, ts := range oneHour {
		have[alignDown(ts, hourSec)] = true
	}
	seen := make(map[int64]bool, len(fiveMin))
	out := make([]int64, 0, len(fiveMin))
	for _, ts := range fiveMin {
		h := alignDown(ts, hourSec)
		if h >= upper || seen[h] || have[h] || already[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// ─ repair 集合 ───────────────────────────────────────

// repairMember 是 repair 集合的一个成员（成员 = 小时桶起点的十进制字符串）。
type repairMember struct {
	raw     string
	hour    int64
	parseOK bool
}

// repairMembersByAge 把成员按「最老在前」排序。
//
// 无法解析的成员排在最前（它们不是本服务写的，属于异常残留）：上限的收敛优先 ——
// 让这些垃圾先被丢弃，而不是永久占着一个名额、把真正要修的小时挤出去。
func repairMembersByAge(members []string) []repairMember {
	out := make([]repairMember, 0, len(members))
	for _, m := range members {
		h, err := strconv.ParseInt(m, 10, 64)
		out = append(out, repairMember{raw: m, hour: h, parseOK: err == nil})
	}
	sort.SliceStable(out, func(i, j int) bool { return repairAge(out[i]) < repairAge(out[j]) })
	return out
}

// repairAge 给「按年龄排序」用的键（解析失败的成员视为最老）。
func repairAge(m repairMember) int64 {
	if !m.parseOK {
		return math.MinInt64
	}
	return m.hour
}

// repairHours 返回该设备 repair 集合里**已闭**的小时（升序）。
//
// 还没闭的小时（>= upper）留在集合里但不处理：它们的水位还在后面，普通区间迟早会写到，
// 现在重算只会算出一个残缺值。
func (s *AgentMetricsRollupService) repairHours(ctx context.Context, deviceID uint64,
	upper int64) ([]int64, error) {

	key := agentmetrics.RepairSetKey(deviceID)
	members, err := s.rdb.SMembers(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("读 repair 集合 %s: %w", key, err)
	}
	out := make([]int64, 0, len(members))
	for _, m := range repairMembersByAge(members) {
		if !m.parseOK {
			// 读路径**不删**成员：读操作失败/异常不应改变集合的状态（清理统一在上限收敛那步做）。
			s.log.Debug("agentmetrics rollup: repair 集合里有无法解析的成员，忽略",
				zap.Uint64("deviceId", deviceID), zap.String("member", m.raw))
			continue
		}
		if m.hour >= upper {
			continue
		}
		out = append(out, m.hour)
	}
	return out, nil
}

// addRepairHour 把小时记进 repair 集合，并在超过上限时**丢弃最老的**（含 log.Warn）。
func (s *AgentMetricsRollupService) addRepairHour(ctx context.Context, deviceID uint64, hour int64) error {
	key := agentmetrics.RepairSetKey(deviceID)
	if err := s.rdb.SAdd(ctx, key, hour).Err(); err != nil {
		return fmt.Errorf("写 repair 集合 %s: %w", key, err)
	}
	n, err := s.rdb.SCard(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("读 repair 集合基数 %s: %w", key, err)
	}
	if int(n) <= s.maxRepairHours {
		return nil
	}
	members, err := s.rdb.SMembers(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("读 repair 集合 %s: %w", key, err)
	}
	oldest := repairMembersByAge(members)
	drop := int(n) - s.maxRepairHours
	if drop > len(oldest) {
		drop = len(oldest)
	}
	victims := make([]any, 0, drop)
	hours := make([]int64, 0, drop)
	for _, m := range oldest[:drop] {
		victims = append(victims, m.raw)
		hours = append(hours, m.hour)
	}
	if len(victims) == 0 {
		return nil
	}
	if err := s.rdb.SRem(ctx, key, victims...).Err(); err != nil {
		return fmt.Errorf("裁剪 repair 集合 %s: %w", key, err)
	}
	s.log.Warn("agentmetrics rollup: repair 集合超过上限，丢弃最老的小时",
		zap.Uint64("deviceId", deviceID), zap.Int("max", s.maxRepairHours),
		zap.Int("dropped", len(victims)), zap.Int64s("droppedHours", hours),
		zap.Int64("newestHour", hour))
	return nil
}

// dropRepairHour 把小时移出 repair 集合（成员不存在时是无害的空操作）。
func (s *AgentMetricsRollupService) dropRepairHour(ctx context.Context, deviceID uint64, hour int64) error {
	key := agentmetrics.RepairSetKey(deviceID)
	if err := s.rdb.SRem(ctx, key, hour).Err(); err != nil {
		return fmt.Errorf("清理 repair 集合 %s: %w", key, err)
	}
	return nil
}

// ── 水位游标 ───────────────────────────────────────────

// readCursor 读 `cursor_1h`；键缺失 → Bootstrap 语义（算出起点、写回并返回它）。
//
// 为什么缺失时**写回**而不是只在内存里用：写回让「首次回滚」这件事可见且幂等
// （第二次调用读到同一个值），也让运维能直接看到水位。写失败则上抛 —— 否则整轮都在
// 一个「内存里的假水位」上跑，下一轮又从同一个起点重算一遍。
func (s *AgentMetricsRollupService) readCursor(ctx context.Context, deviceID uint64, upper int64) (int64, error) {
	v, ok, err := s.cursors.Read(ctx, deviceID, agentmetrics.Resolution1h)
	if err != nil {
		// Redis 故障**必须上抛**：把它当成「没有游标」会让整轮从 180d 前重算并
		// 回写更小的水位，已回滚的小时被重写，且这是不可观测的降级。
		return 0, err
	}
	if ok {
		return v, nil
	}
	start, serr := s.bootstrapCursor(ctx, deviceID, upper)
	if serr != nil {
		return 0, serr
	}
	return s.cursors.Init(ctx, deviceID, agentmetrics.Resolution1h, start)
}

// bootstrapCursor 返回缺省的水位值：**第一个要处理的小时** =
// max(now−180d, 该设备最早的 5m 行) 对齐到小时边界。
//
// 为什么要减 1 小时：游标的语义是「已成功回滚到（**含**）的小时」，而普通区间的下界是
// `cursor + 3600`。若把「起点」直接当水位写下去，那个最早的小时会被下界**排除**，
// 它的 1h 行永远不会被写 —— 而 5m 只保留 30 天，超出 30 天的窗口**只有 1h 表可查**，
// 那就是一个永久空洞。故水位取「起点 − 1 小时」，让起点成为**第一个被处理**的小时。
//
// 为什么是 180d 而不是 0（flush 是 24h，两者不同源、各有理由）：raw 热层只留 24h，
// 故 flush 从 24h 起算；而 rollup 的输入是库里的 5m 行（保留 30d），从 0 起步会白扫
// 1970 年以来 6_000_000+ 个小时 —— 而这个 180d 下界只是兜底（正常情况下
// max() 会选中「最早的 5m 行」，即真正的数据起点，不多扫一个小时的空区间）。
func (s *AgentMetricsRollupService) bootstrapCursor(ctx context.Context, deviceID uint64,
	upper int64) (int64, error) {

	hourSec := resolutionSeconds(agentmetrics.Resolution1h)
	start := alignDown(s.now().Add(-rollupBootstrapWindow).Unix(), hourSec)
	if start < 0 {
		start = 0
	}
	earliest, ok, err := s.earliestFiveMinTS(ctx, deviceID, upper)
	if err != nil {
		return 0, err
	}
	if ok {
		if h := alignDown(earliest, hourSec); h > start {
			start = h
		}
	}
	if start <= hourSec {
		// 起点落在 unix 秒原点附近（现实中不存在：最早的数据也在 2001 年之后）：
		// 夹到 0，避免写出一个**负**水位（负水位与「从 0 起步」在语义上无法区分，
		// 只是把同一个缺陷换了个符号），也避免 cursor 变成负数。
		return 0, nil
	}
	return start - hourSec, nil
}

// rollupBootstrapWindow 是缺省起点的兜底下界（= 1h 表的保留期，spec §7.3）。
const rollupBootstrapWindow = 180 * 24 * time.Hour

// earliestFiveMinTS 返回该设备最早的 5m 行时间；没有任何 5m 行时返回 (0, false)。
//
// 为什么读 [0, upper] 整段而不是「只查一行」：仓储是**唯一**允许对指标表查询的地方，
// 而它给的能力是「按区间读、升序返回」（没有单行/投影变体）。这个代价是有界的：
// 5m 表保留 30d（agentmetrics.TableSpecs），一台设备最多 30d/5min = 8640 行；
// 而且这次读只在**游标缺失**时发生一次（游标随即写回 Redis）。
func (s *AgentMetricsRollupService) earliestFiveMinTS(ctx context.Context, deviceID uint64,
	upper int64) (int64, bool, error) {

	if upper <= 0 {
		return 0, false, nil
	}
	rows, err := s.metrics.ReadWideRows(ctx, entity.TableNameMetric5m, deviceID, 0, upper)
	if err != nil {
		return 0, false, fmt.Errorf("探测最早 5m 行 device=%d: %w", deviceID, err)
	}
	if len(rows) == 0 {
		return 0, false, nil
	}
	return rows[0].BucketTS, true, nil
}

// CursorFor 返回**本轮实际会使用**的起点水位（unix 秒）——缺水位时按 Bootstrap 语义
// 初始化，返回值与 RollupOnce 内部读到的**同一个值**。
//
// 暴露它的理由（可观测性，不是给生产调用的便利方法）：水位是「这一轮会处理哪些小时」的
// 唯一决定因素，而它平时只体现在「小时计数」这种**间接**信号上 —— 水位一旦退化成
// `now−180d` 对齐值，一台 30 天前才开始上报的设备一次要扫 4320 个空小时，于是反向验证
// （临时改回旧行为 → 看断言是否变红）只能得到「测试变慢」而不是一条可读的断言失败。
// 有了它，起点可以被**直接**断言（毫秒级）。
func (s *AgentMetricsRollupService) CursorFor(ctx context.Context, deviceID uint64) (int64, error) {
	return s.readCursor(ctx, deviceID, s.closedHourUpper())
}

// writeCursor 前移 `cursor_1h`：只在「当前值仍是 expected（轮初读到的水位）」且
// next > expected 时写入（Lua 原子比较-写，见 agentmetrics.CursorStore.Advance）。
//
// 与 flush 共用同一个原语：水位是「已成功处理到（含）哪个桶/小时」的记账，
// 唯一的正确写法就是「在原子上确认水位没被别人动过，然后只往前走」。
func (s *AgentMetricsRollupService) writeCursor(ctx context.Context, deviceID uint64,
	expected, next int64) error {

	if _, err := s.cursors.Advance(ctx, deviceID, agentmetrics.Resolution1h, expected, next); err != nil {
		return err
	}
	return nil
}

// ── 配置 ───────────────────────────────────────────────

// closedHourUpper 返回已闭小时的半开上界：`alignDown(now − CloseGrace, 3600)`。
func (s *AgentMetricsRollupService) closedHourUpper() int64 {
	return alignDown(s.now().Add(-agentCloseGrace(s.cfg)).Unix(),
		resolutionSeconds(agentmetrics.Resolution1h))
}

// expectedSamplesPerHour 返回一个完整小时应有的原始样本数（用于「样本偏薄」告警）。
//
// 上报间隔与服务侧宽限共用同一处推导（agentReportInterval / agentCloseGrace，
// 见 agent_metrics_timing.go）：这里不再有第二份「读配置 + 回落默认值」的实现。
func (s *AgentMetricsRollupService) expectedSamplesPerHour() int {
	return agentmetrics.ExpectedSamplesPerHour(agentReportInterval(s.cfg))
}
