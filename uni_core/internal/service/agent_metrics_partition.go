package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agentmetrics"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
)

// ── 消费方窄接口（仓库既有约定：接口定义在消费方）──────────────

// PartitionSchemaRepo 是分区协调器需要的仓储能力面，也是**指标表 DDL 的唯一出口**。
//
// 为什么接口上只有「单条 Exec」而**没有** Transaction：DDL 是隐式提交语句
// （MySQL 的 ALTER/CREATE/DROP TABLE 都会隐式提交），把它包进 `db.Transaction` 会让
// 「事务 + 版本占坑」这套跨实例互斥**静默失效**（占坑记录被 DDL 一起提交掉；
// PG 则相反：DDL 事务性会把 ACCESS EXCLUSIVE 持到整个迁移提交）—— 见 spec §6.1。
// 于是本服务**根本没有**拿到事务句柄的途径：错路在类型层面就被封死，而不是靠注释提醒。
type PartitionSchemaRepo interface {
	// Dialect 返回数据库方言（mysql / postgres / sqlite / …），与迁移侧同源。
	Dialect() string
	// EnsureSchema 以**分区形态**建 6 张指标表（幂等：表已存在时 CREATE TABLE IF NOT EXISTS 是 no-op）。
	EnsureSchema(ctx context.Context, dialect string, now time.Time) error
	// ExistingPartitions 返回某张表已有的分区名（MySQL 走 information_schema；其它方言返回空）。
	ExistingPartitions(ctx context.Context, table string) ([]string, error)
	// ExecDDL 执行**一条** DDL 语句（调用方逐条调用；本方法不开事务，也不聚合多语句）。
	ExecDDL(ctx context.Context, stmt string) error
}

// PartitionLocker 是协调器的分布式互斥能力面（消费方定义，由 `*lock.RedisLocker` 满足）。
//
// 为什么**不复用** SysJob 的锁：调度器的锁是「每 job 一把、TTL = 全局 maxExec + 30s」，
// 而 maxExec（`sys.scheduler.maxExecutionTime`）与 lockTTL 都是**全局**配置 —— 把分区维护
// 挂在那把锁/租约上，等于「改业务任务的执行时长上限会顺手改掉 DDL 的互斥租约」，反过来
// 给 DDL 加长租约也会击穿业务任务的互斥（spec §7.3：「分区协调器不走 SysJob」）。
// 故本服务自带锁键、自带 TTL、自带**每表**超时，与调度器完全解耦。
type PartitionLocker interface {
	TryLock(ctx context.Context, key string, owner string, ttl time.Duration) (bool, error)
	Unlock(ctx context.Context, key string, owner string) error
}

// ── 配置与常量 ─────────────────────────────────────────

const (
	// configPartitionLockTTL 是本服务**自己的**锁租约（秒）。
	//
	// 与 SysJob 的 `sys.scheduler.lockTTL` 是两个键：这一把锁只保护分区对账，
	// 它的租约应该按「一轮 reconcile 的上界」（6 张表 × 每表超时 + 一次建表）来定，
	// 而不是按业务任务的最大执行时长来定。
	configPartitionLockTTL = "sys.agent.partitionLockTTL"
	// defaultPartitionLockTTLSec 取 600s。
	//
	// 依据是**一轮的上界**而不是手感：6 张表 × 每表 60s + 一次建表 = 420s，600s 是它的
	// 约 1.4 倍。租约短于一轮上界的后果见 lockTTL()（慢轮的锁会在执行中过期 →
	// 另一轮 TryLock 成功 → 两轮 DDL 并发，同一张表上互相等 MDL）。
	defaultPartitionLockTTLSec = 600
	// configPartitionTableTimeout 是**每张表**一次对账（查询分区 + 逐条 DDL）的超时（秒）。
	//
	// 每表一个独立超时（而不是整轮一个）：一张表的 DDL 卡住（例如 MySQL 上的
	// `TRUNCATE PARTITION` 等待 MDL）不得让其余 5 张表陪着一起超时 ——
	// 分区维护的可用性远比「一轮要么全做要么全不做」重要，DDL 本身是逐条幂等的。
	configPartitionTableTimeout = "sys.agent.partitionTableTimeout"
	// defaultPartitionTableTimeoutSec 取 60s（单条分区 DDL 在 MySQL 上是元数据级操作，
	// 实测空哨兵 REORGANIZE 约 0.86s；60s 是它的约 70 倍）。
	defaultPartitionTableTimeoutSec = 60

	// partitionLockKey 是协调器的互斥键（与 SysJob 的 `job:lock:<id>` 完全分离）。
	partitionLockKey = "agent:partition:reconcile"

	// partitionHold 是「最老的 hold 个过期分区只 TRUNCATE 不 DROP」的个数。
	//
	// 取 2（spec §7.3 的「最老 1~2 个分区用 TRUNCATE PARTITION」）：MySQL 的 RANGE 分区
	// 只有上界没有下界，DROP 掉最左分区后更老的值会**静默落进下一个分区**，
	// 「缺分区 = 硬失败 1526」这条兜底在第一次回收后就永久失效。保留 2 个已 TRUNCATE
	// （边界在、行已空）的老分区，既释放了空间，又让迟到/回填的老数据仍有自己的边界。
	partitionHold = 2

	// partitionHorizonWeeks / partitionHorizonMonths 是预建视界（单位 = 分区周期）。
	//
	// 取 spec §7.3 的「三段式 [now − (保留期+1 周期), now + N]：周分区表 N=5 周、
	// 月分区表 N=3 个月」——它保证 `partition_future_days`（6 张表取 min，
	// <21 天告警）不会被自己预建的视界压到阈值以下：周分区表的视界落在 28~35 天。
	//
	// 注意（与计划的一处不一致，见提交说明）：`repository.EnsureSchema` 里的预建视界是
	// 写死的 3，故**首次**建表后周分区表还差 2 个未来分区，由本协调器的第一轮补齐
	// （这是协调器的正常职责：EnsureSchema 只负责「表以分区形态存在」，补/回收由本服务负责）。
	partitionHorizonWeeks  = 5
	partitionHorizonMonths = 3

	// 保留期的运行期覆盖键（`TableSpecs` 的注释写明「协调器在运行时用 sys.agent.* 覆盖」）。
	// 两把键都是 v008 已种下的，默认值与 spec §10 一致。
	configAgentHistoryRetentionDays    = "sys.agent.historyRetentionDays"
	defaultAgentHistoryRetentionDays   = 30
	configAgentMetrics1hRetentionDays  = "sys.agent.metrics1hRetentionDays"
	defaultAgentMetrics1hRetentionDays = 180
)

// ErrPartitionPartial 表示本轮有表对账失败（建表/查询分区/建分区 DDL 失败）。
//
// **不包含**「回收失败」：降级方言（sqlite 等）下 `ReclaimDDL` 必定返回错误（Plan 2A 的
// F2 修复：拒绝生成无界 DELETE），那是**常态**而不是故障，只记日志（见 reconcileTable）。
var ErrPartitionPartial = errors.New("agentmetrics partition: 本轮部分表对账失败")

// PartitionStats 是一轮 reconcile 的统计（任务与测试都读它）。
type PartitionStats struct {
	// TablesScanned 是本轮**看过**的指标表数（含查询分区失败的表）。正常情况下 = 6。
	TablesScanned int
	// PartitionsCreated 是**成功执行**的新建分区 DDL 条数（不是计划里的条数：
	// 降级方言上 AddPartitionDDL 返回空，故 sqlite 下它恒为 0，这是正确读数而不是失败）。
	PartitionsCreated int
	// PartitionsTruncated 是成功执行的 TRUNCATE PARTITION 条数（保边界、清空行）。
	PartitionsTruncated int
	// PartitionsDropped 是成功执行的 DROP PARTITION 条数（彻底释放空间）。
	PartitionsDropped int
	// Skipped 是本轮**整体**被跳过的次数（0 或 1）：拿不到锁时为 1，此时
	// TablesScanned 等一律为 0。它不是「跳过的表数」—— 表级失败由返回的 error 表达，
	// 一个字段兼两种读法会让两个调用方各自理解成不同的东西。
	Skipped int
}

// AgentMetricsPartitionService 是 6 张指标表的分区协调器：建表 → 逐表对账（补缺 + 回收）。
//
// 职责边界（与 flush/rollup 的关系）：它只碰**结构**（分区边界），不碰任何一行指标数据；
// 回收走 `TRUNCATE PARTITION` / `DROP PARTITION`，因此不会与 flush 的 UPSERT 竞争行锁。
//
// 为什么是「每轮全量对账」而不是「记住上次做到哪」：分区状态是**外部可变**的
// （恢复旧备份会带回旧的分区集合、人工 DDL、主从切换），任何进程内的记忆都会与实际漂移；
// 而按**确定性分区名**求差集的代价是每表一次 information_schema 查询（6 次/轮），
// 完全付得起。这也是「恢复旧备份后版本驱动的预建不会重跑」的正面解法（spec §7.3）。
type AgentMetricsPartitionService struct {
	schema PartitionSchemaRepo
	cfg    AgentConfigGetter
	locker PartitionLocker
	log    logger.LoggerInterface
	// now 可注入（测试用固定时钟：分区边界与回收判定都由它推导）。
	now func() time.Time
	// owner 是锁的持有者标识：Unlock 是「比对持有者再删」的语义，
	// 故 owner 必须**进程级唯一**，否则 A 实例会误删 B 实例的锁。
	owner string
}

// NewAgentMetricsPartitionService 装配分区协调器。
//
// schema 生产传 `*repository.DeviceMetricSchemaRepo`，locker 传 `*lock.RedisLocker`；
// cfg 用于读本服务**自己的**两个超时键与两把保留期覆盖键（键缺失时一律回落默认值，
// 故不依赖任何迁移种子）。log 只用于记录，不参与任何判断。
func NewAgentMetricsPartitionService(schema PartitionSchemaRepo, cfg AgentConfigGetter,
	locker PartitionLocker, log logger.LoggerInterface) *AgentMetricsPartitionService {

	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		// 拿不到主机名不是致命错（锁仍能工作，只是可读性差一点）：退化成 pid-only。
		// 与 scheduler 的 nodeID 同构（`%s:%d`），便于运维把锁的持有者对上具体实例。
		hostname = "unknown"
	}
	return &AgentMetricsPartitionService{
		schema: schema, cfg: cfg, locker: locker, log: log,
		now:   time.Now,
		owner: fmt.Sprintf("%s:%d", hostname, os.Getpid()),
	}
}

// WithClock 注入时钟（测试用；生产走 time.Now）。
func (s *AgentMetricsPartitionService) WithClock(now func() time.Time) *AgentMetricsPartitionService {
	if now != nil {
		s.now = now
	}
	return s
}

// Reconcile 跑一轮分区对账：拿自己的锁 → 建表（幂等）→ 逐表「补缺 + 回收」。
//
// **可重入且幂等**：启动期（wireup 在调度器 Start 之前同步调一次）与定时任务都会调它，
// 两侧可能重叠 —— 重叠由分布式锁挡掉（拿不到锁的那一轮直接返回 Skipped=1），
// 而即使锁因故失效（TTL 到期、Redis 抖动），重复执行也是无害的：
// 计划由「确定性分区名求差集」得出（存在即跳过），DDL 本身逐条幂等。
//
// 返回值：error 非 nil 表示本轮有表对账失败（sentinel：ErrPartitionPartial）；
// 调用方（wireup）**不应**因此阻断启动，只需在日志里明确「分区未就绪，
// 指标写入可能因缺分区失败」。锁被占用而整体跳过时返回 (Skipped=1, nil)。
func (s *AgentMetricsPartitionService) Reconcile(ctx context.Context) (PartitionStats, error) {
	var stats PartitionStats

	ttl := s.lockTTL()
	ok, err := s.locker.TryLock(ctx, partitionLockKey, s.owner, ttl)
	if err != nil {
		return stats, fmt.Errorf("agentmetrics partition: 获取协调器锁失败: %w", err)
	}
	if !ok {
		// 另一轮（启动期那一轮，或另一个实例）正在跑：分区对账没有「做得更频繁更好」这回事，
		// 直接跳过是本服务唯一合理的并发策略（不是排队 —— 排队会让定时任务堆积）。
		stats.Skipped = 1
		s.log.Info("agentmetrics partition: 已有另一轮对账在跑，本轮跳过",
			zap.String("key", partitionLockKey), zap.String("owner", s.owner))
		return stats, nil
	}
	defer func() {
		// 解锁必须**在 ctx 被取消后仍能执行**：否则锁会悬挂到 TTL 才释放，
		// 期间定时任务每一轮都被跳过（scheduler 的同款理由，见 executor.go）。
		// WithoutCancel 保留 ctx 上的值（trace 等），只剥离取消信号。
		uctx := context.WithoutCancel(ctx)
		if uerr := s.locker.Unlock(uctx, partitionLockKey, s.owner); uerr != nil {
			// 解锁失败只记日志：DDL 已经执行完毕，把一次成功的对账变成 error
			// 会让任务层记成失败；锁会在 TTL 到期后自然释放（下一轮仍可能被跳过，但自愈）。
			s.log.Warn("agentmetrics partition: 释放协调器锁失败（TTL 到期后自动释放）",
				zap.String("key", partitionLockKey), zap.String("owner", s.owner), zap.Error(uerr))
		}
	}()

	dialect := s.schema.Dialect()
	now := s.now()

	// 1) 先确保 6 张表以**分区形态**存在（幂等）。表已存在时它是 no-op；
	//    表不存在时它是唯一能建出「带 p_min 守卫 + p_max 哨兵」的表的地方
	//    （MySQL 只能建表时内联分区、事后转分区是整表 COPY；PG 不允许把普通表转成分区表）。
	//    它失败**不中止**本轮：表很可能已经存在（上一次建表成功、这次只是权限抖动），
	//    逐表对账照常进行 —— 失败会被累加进返回的 error，由调用方决定记日志还是告警。
	failures := make([]string, 0, len(agentmetrics.TableSpecs())+1)
	if err := s.schema.EnsureSchema(ctx, dialect, now); err != nil {
		s.log.Error("agentmetrics partition: 确保指标表存在失败，继续逐表对账",
			zap.String("dialect", dialect), zap.Error(err))
		failures = append(failures, "ensure-schema: "+err.Error())
	}

	// 2) 逐表对账。
	for _, spec := range agentmetrics.TableSpecs() {
		stats.TablesScanned++
		if terr := s.reconcileTable(ctx, spec, dialect, now, &stats); terr != nil {
			s.log.Error("agentmetrics partition: 该表对账失败，继续处理其余表",
				zap.String("table", spec.Table), zap.Error(terr))
			failures = append(failures, spec.Table+": "+terr.Error())
		}
	}
	if len(failures) > 0 {
		return stats, fmt.Errorf("%w: %s", ErrPartitionPartial, strings.Join(failures, "; "))
	}
	return stats, nil
}

// reconcileTable 对账单张表：查现状 → 求计划 → 逐条执行建分区 DDL → 逐条执行回收 DDL。
//
// DDL **逐条 Exec**、绝不聚合、绝不放进事务（见 PartitionSchemaRepo 的说明）。
// 每张表有自己的 `context.WithTimeout`：一张表的 DDL 卡住不影响其余 5 张。
//
// 失败语义：建表/查询/建分区失败 → 返回 error（计入 ErrPartitionPartial）；
// **回收失败只记日志并返回 nil** —— 降级方言上「无法回收」是常态（ReclaimDDL 会明确
// 拒绝生成无界 DELETE），把它上抛会让调用方每一轮都把正常对账误判成「分区未就绪」。
func (s *AgentMetricsPartitionService) reconcileTable(ctx context.Context, spec agentmetrics.Spec,
	dialect string, now time.Time, stats *PartitionStats) error {

	tctx, cancel := context.WithTimeout(ctx, s.tableTimeout())
	defer cancel()

	// 保留期允许运行期覆盖（sys.agent.*）：改小保留期后，滑出窗口的老分区会在同一轮
	// 被 PlanFrom 的 boundFromName 路径捞出来回收，不需要重启。
	spec = s.withConfiguredRetention(spec)

	existing, err := s.schema.ExistingPartitions(tctx, spec.Table)
	if err != nil {
		return fmt.Errorf("查询分区现状失败: %w", err)
	}
	plan := agentmetrics.PlanFrom(spec, existing, now, horizonFor(spec), partitionHold)

	// hasSentinel：现状里含 p_max 时才为真。有哨兵**必须**走 REORGANIZE 拆哨兵
	// （MySQL 只能把分区追加在高端，而哨兵在最高位 → 直接 ADD PARTITION 报 1463）。
	hasSentinel := false
	for _, n := range existing {
		if n == agentmetrics.SentinelPartitionName {
			hasSentinel = true
			break
		}
	}

	// 1) 补缺。plan.Create 按上界升序，故多条 REORGANIZE 的顺序是正确的：
	//    每条都把「当时最新的哨兵」拆成「一个新分区 + 一个新哨兵」，下一条继续拆新哨兵。
	addStmts, err := agentmetrics.AddPartitionDDL(dialect, spec, plan.Create, hasSentinel)
	if err != nil {
		return fmt.Errorf("生成建分区语句失败: %w", err)
	}
	for _, stmt := range addStmts {
		if eerr := s.schema.ExecDDL(tctx, stmt); eerr != nil {
			return fmt.Errorf("执行建分区 DDL 失败: %w", eerr)
		}
		stats.PartitionsCreated++
	}

	// 2) 回收。ReclaimDDL 的输出顺序是「先 truncate 后 drop」（partition.go 的两个方言
	//    分支都如此），故按 plan.Truncate 的长度切分计数。
	reclaimStmts, rerr := agentmetrics.ReclaimDDL(dialect, spec, plan.Truncate, plan.Drop)
	if rerr != nil {
		s.log.Warn("agentmetrics partition: 该方言无法生成分区回收语句，跳过回收（不中断本轮）",
			zap.String("table", spec.Table), zap.String("dialect", dialect),
			zap.Strings("truncate", plan.Truncate), zap.Strings("drop", plan.Drop), zap.Error(rerr))
		return nil
	}
	for i, stmt := range reclaimStmts {
		if eerr := s.schema.ExecDDL(tctx, stmt); eerr != nil {
			s.log.Warn("agentmetrics partition: 回收 DDL 执行失败，跳过该表回收（不中断本轮）",
				zap.String("table", spec.Table), zap.String("stmt", stmt), zap.Error(eerr))
			return nil
		}
		if i < len(plan.Truncate) {
			stats.PartitionsTruncated++
		} else {
			stats.PartitionsDropped++
		}
	}
	return nil
}

// withConfiguredRetention 用现有配置覆盖 `TableSpecs` 的保留期默认值。
//
// 两把键都是 v008 已种下的（spec §10）：`_1h` 用 1h 档保留期，其余 5 张同属 5min 档
// （5m 宽表 + 4 张资源子表，保留期口径一致），共用 5m 档保留期。非法值（<=0）回落默认值：
// 保留期被填成 0 会让 Desired 的窗口收缩到只剩「当前周期」，于是每一轮都把
// 几乎全部分区判成过期 —— 那不是「清空数据」的授权，只是配置写错了。
func (s *AgentMetricsPartitionService) withConfiguredRetention(spec agentmetrics.Spec) agentmetrics.Spec {
	key, def := configAgentHistoryRetentionDays, defaultAgentHistoryRetentionDays
	if spec.Table == entity.TableNameMetric1h {
		key, def = configAgentMetrics1hRetentionDays, defaultAgentMetrics1hRetentionDays
	}
	days := s.cfg.GetInt(context.Background(), key, def)
	if days <= 0 {
		days = def
	}
	spec.Retention = time.Duration(days) * 24 * time.Hour
	return spec
}

// horizonFor 返回预建视界（单位 = 该表的分区周期数）。
func horizonFor(spec agentmetrics.Spec) int {
	if spec.Granularity == agentmetrics.GranularityMonth {
		return partitionHorizonMonths
	}
	return partitionHorizonWeeks
}

// tableTimeout 返回每表对账的超时。
func (s *AgentMetricsPartitionService) tableTimeout() time.Duration {
	sec := s.cfg.GetInt(context.Background(), configPartitionTableTimeout, defaultPartitionTableTimeoutSec)
	if sec <= 0 {
		sec = defaultPartitionTableTimeoutSec
	}
	return time.Duration(sec) * time.Second
}

// lockTTL 返回锁租约，并保证它覆盖「一轮 reconcile 的上界」。
//
// 为什么必须有这个下界（与 scheduler 的 `lockTTL = maxExec + 30s` 同款理由，但用的是
// 本服务**自己的**超时）：若 TTL < 一轮的执行上界，慢轮的锁会在执行中过期，
// 另一轮 TryLock 成功 → 两轮 DDL 并发（同一张表上并发 ALTER 会互相等 MDL，
// 并且在 PG 上会争 ACCESS EXCLUSIVE）。上界 = 1 次建表 + 6 张表 × 每表超时。
func (s *AgentMetricsPartitionService) lockTTL() time.Duration {
	ttl := time.Duration(s.cfg.GetInt(context.Background(), configPartitionLockTTL, defaultPartitionLockTTLSec)) * time.Second
	if ttl <= 0 {
		ttl = defaultPartitionLockTTLSec * time.Second
	}
	upper := s.tableTimeout() * (time.Duration(len(agentmetrics.TableSpecs())) + 1)
	if minTTL := upper + 30*time.Second; ttl < minTTL {
		ttl = minTTL
	}
	return ttl
}
