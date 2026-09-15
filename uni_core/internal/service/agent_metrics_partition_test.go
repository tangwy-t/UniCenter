package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agentmetrics"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/database"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/snowflake"
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
)

// ── 夹具 ──────────────────────────────────────────────

// partitionNow 是固定时钟（周三 12:00 UTC）：周分区/月分区边界都由它推导，测试可复算。
var partitionNow = time.Date(2027, 3, 10, 12, 0, 0, 0, time.UTC)

// partitionLockKeyLiteral 是**契约字面量**：不复用实现里的常量，否则键名写错也自洽。
const partitionLockKeyLiteral = "agent:partition:reconcile"

// partitionConfig 是 AgentConfigGetter 的替身：按 key 精确给值（未给则回落调用方默认值）。
//
// 不能用 flush/rollup 测试里的 fakeConfig：它对**任何** key 都返回同一个 reportSec，
// 而本服务要读 4 把不同的配置键（两个超时键 + 两把保留期键），串味会让断言失去意义。
type partitionConfig struct{ vals map[string]int }

func (c partitionConfig) GetString(_ context.Context, _ string, def string) string { return def }

func (c partitionConfig) GetInt(_ context.Context, key string, def int) int {
	if v, ok := c.vals[key]; ok {
		return v
	}
	return def
}

// partitionLogSpy 记录 Warn/Error 的文案（「只记日志」这类断言必须能看见日志本身）。
type partitionLogSpy struct {
	mu     sync.Mutex
	warns  []string
	errs   []string
	infos  []string
	debugs []string
}

func (l *partitionLogSpy) add(dst *[]string, msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	*dst = append(*dst, msg)
}
func (l *partitionLogSpy) Debug(msg string, _ ...zap.Field) { l.add(&l.debugs, msg) }
func (l *partitionLogSpy) Info(msg string, _ ...zap.Field)  { l.add(&l.infos, msg) }
func (l *partitionLogSpy) Warn(msg string, _ ...zap.Field)  { l.add(&l.warns, msg) }
func (l *partitionLogSpy) Error(msg string, _ ...zap.Field) { l.add(&l.errs, msg) }
func (l *partitionLogSpy) IsDebug() bool                    { return false }

func (l *partitionLogSpy) hasWarn(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, m := range l.warns {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}

// hasError 看 Error 级日志（「审计写入失败不得静默」这类断言必须能看见日志本身，
// 而不是只看 error 返回值 —— 两者是两条独立的可观测路径）。
func (l *partitionLogSpy) hasError(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, m := range l.errs {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}

// spyLocker 是 PartitionLocker 的替身，但**语义是真的**（真正的互斥）：
// 并发测试要断言「同一时刻只有一个执行」，用一个只会返回 true 的替身等于没测。
type spyLocker struct {
	mu        sync.Mutex
	held      map[string]string
	tryCalls  int
	unlocks   int
	lastKey   string
	lastOwner string
	lastTTL   time.Duration
	tryErr    error
}

func newSpyLocker() *spyLocker { return &spyLocker{held: map[string]string{}} }

func (l *spyLocker) TryLock(_ context.Context, key, owner string, ttl time.Duration) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.tryCalls++
	l.lastKey, l.lastOwner, l.lastTTL = key, owner, ttl
	if l.tryErr != nil {
		return false, l.tryErr
	}
	if _, ok := l.held[key]; ok {
		return false, nil
	}
	l.held[key] = owner
	return true, nil
}

func (l *spyLocker) Unlock(_ context.Context, key, owner string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.unlocks++
	if l.held[key] == owner {
		delete(l.held, key)
	}
	return nil
}

func (l *spyLocker) heldCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.held)
}

// spySchema 是 mysql 方言的仓储替身：分区现状可注入、执行的 DDL 逐条记账。
//
// 用它的理由（而不是 sqlite 真仓储）：sqlite **没有分区**，`AddPartitionDDL`/`InlineClause`
// 在它上面全是降级空实现，于是「幂等」「hasSentinel → REORGANIZE」「hold → truncate/drop」
// 这些本任务的全部价值在 sqlite 上**一条都验证不了**。真仓储那条路径另行覆盖（降级语义、
// 建表、并发锁），两条路径的断言互不冒充。
type spySchema struct {
	dialect     string
	existing    map[string][]string
	existingErr map[string]error
	ensureErr   error

	ensureCalls int
	ensureNow   time.Time
	execStmts   []string
	execErrOn   int // 第 N 条语句（1-based）执行失败；0 = 从不失败
}

func newSpySchema() *spySchema {
	return &spySchema{dialect: "mysql", existing: map[string][]string{}, existingErr: map[string]error{}}
}

func (s *spySchema) Dialect() string { return s.dialect }

func (s *spySchema) EnsureSchema(_ context.Context, _ string, now time.Time) error {
	s.ensureCalls++
	s.ensureNow = now
	return s.ensureErr
}

func (s *spySchema) ExistingPartitions(_ context.Context, table string) ([]string, error) {
	if err := s.existingErr[table]; err != nil {
		return nil, err
	}
	// 返回副本：调用方（或测试）改动切片不得影响下一次调用。
	return append([]string(nil), s.existing[table]...), nil
}

func (s *spySchema) ExecDDL(_ context.Context, stmt string) error {
	s.execStmts = append(s.execStmts, stmt)
	if s.execErrOn > 0 && len(s.execStmts) == s.execErrOn {
		return errors.New("injected DDL failure")
	}
	return nil
}

// sqliteSchemaFixture 是「sqlite + 真仓储」那一半：真表、真 EnsureSchema、真 Dialect。
type sqliteSchemaFixture struct {
	db   *gorm.DB
	repo *repository.DeviceMetricSchemaRepo
}

func newSQLiteSchemaFixture(t *testing.T) *sqliteSchemaFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	// 生产同款雪花回调（与 flush/rollup 测试同一条注册路径）。
	node, err := snowflake.New(1, logger.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	database.NewCallbacks(node).Register(db)
	t.Cleanup(func() {
		if sqlDB, derr := db.DB(); derr == nil {
			_ = sqlDB.Close()
		}
	})
	return &sqliteSchemaFixture{db: db, repo: repository.NewDeviceMetricSchemaRepository(db)}
}

// tableNames 读回 sqlite 里实际存在的表名。
func (f *sqliteSchemaFixture) tableNames(t *testing.T) map[string]bool {
	t.Helper()
	var names []string
	if err := f.db.Raw("SELECT name FROM sqlite_master WHERE type = 'table'").Scan(&names).Error; err != nil {
		t.Fatalf("读 sqlite_master: %v", err)
	}
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out
}

// blockingSchema 让**第一次** ExistingPartitions 卡住，直到测试放行：
// 只有这样才能确定地制造「一轮执行中、另一轮来抢」的时序（sleep 是碰运气的）。
// 为什么不用 sync.Once 就地阻塞：`Once.Do` 会在执行期间**持住内部互斥**，于是第二次调用
// 会排在 Do 上而不是穿过去 —— 那正好把「另一轮并发进来」这件事挡成了死锁（去掉锁的变异
// 探针最初就是这样挂住的：红灯变成了超时而不是断言失败）。故这里用显式标志：
// 只有**第一个**调用者阻塞，其余调用者直接穿到真仓储。
type blockingSchema struct {
	PartitionSchemaRepo
	entered  chan struct{}
	release  chan struct{}
	released sync.Once

	mu      sync.Mutex
	blocked bool
	queries int
}

func newBlockingSchema(inner PartitionSchemaRepo) *blockingSchema {
	return &blockingSchema{
		PartitionSchemaRepo: inner,
		entered:             make(chan struct{}),
		release:             make(chan struct{}),
	}
}

func (b *blockingSchema) ExistingPartitions(ctx context.Context, table string) ([]string, error) {
	b.mu.Lock()
	first := !b.blocked
	b.blocked = true
	b.mu.Unlock()
	if first {
		close(b.entered)
		<-b.release
	}
	b.mu.Lock()
	b.queries++
	b.mu.Unlock()
	return b.PartitionSchemaRepo.ExistingPartitions(ctx, table)
}

// releaseFirst 放行被卡住的那一轮（幂等：断言失败提前返回时也会由 t.Cleanup 放行，
// 否则第一轮的 goroutine 会一直挂在 <-release 上）。
func (b *blockingSchema) releaseFirst() {
	b.released.Do(func() { close(b.release) })
}

func (b *blockingSchema) queryCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.queries
}

// expiredSchema 只对一张表伪造「现状里有过期分区 + 哨兵」，其余表走真仓储。
//
// 为什么需要伪造：sqlite 真仓储的 ExistingPartitions 恒返回空，于是 `PlanFrom` 永远
// 算不出 Truncate/Drop —— 没有回收请求时 `ReclaimDDL` **不会**报错（「没事可做」是合法状态），
// 那样就永远触发不了「降级方言拒绝回收」这条路径，测试会变成一条恒绿的假断言。
type expiredSchema struct {
	PartitionSchemaRepo
	table    string
	existing []string

	mu      sync.Mutex
	queried []string
}

func (e *expiredSchema) ExistingPartitions(ctx context.Context, table string) ([]string, error) {
	e.mu.Lock()
	e.queried = append(e.queried, table)
	e.mu.Unlock()
	if table == e.table {
		return append([]string(nil), e.existing...), nil
	}
	return e.PartitionSchemaRepo.ExistingPartitions(ctx, table)
}

func (e *expiredSchema) queriedTables() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.queried...)
}

// slowSchema 让一张表的现状查询一直等到自己的 ctx 截止（模拟 DDL 卡在 MDL 等待上）。
type slowSchema struct {
	PartitionSchemaRepo
	table string
}

func (s *slowSchema) ExistingPartitions(ctx context.Context, table string) ([]string, error) {
	if table == s.table {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return s.PartitionSchemaRepo.ExistingPartitions(ctx, table)
}

// ── 计划侧的工具（测试自己算期望，不复用实现的私有函数之外的东西）──

// desiredNamesFor 返回某张表在 `now` 时刻的完整期望分区名（含 p_min 与 p_max）。
func desiredNamesFor(spec agentmetrics.Spec, now time.Time) []string {
	bounds := agentmetrics.Desired(spec, now, horizonFor(spec))
	out := make([]string, 0, len(bounds))
	for _, b := range bounds {
		out = append(out, b.Name)
	}
	return out
}

// satisfyAllTables 让 spySchema 的**每张表**都处于「现状 == 期望」的满意态。
// 单表测试必须先用它铺底：否则未铺底的那 5 张表会把它们全部期望分区都算成「缺失」，
// 于是 PartitionsCreated 里混进 50+ 条与断言无关的语句（第一版就踩了这个坑）。
func satisfyAllTables(schema *spySchema) {
	for _, spec := range agentmetrics.TableSpecs() {
		schema.existing[spec.Table] = desiredNamesFor(spec, partitionNow)
	}
}

// specByTable 从 TableSpecs 里取一张表（测试断言用的 spec 必须与生产同源）。
func specByTable(t *testing.T, table string) agentmetrics.Spec {
	t.Helper()
	for _, s := range agentmetrics.TableSpecs() {
		if s.Table == table {
			return s
		}
	}
	t.Fatalf("TableSpecs 里没有表 %s", table)
	return agentmetrics.Spec{}
}

// businessNamesInPast 造一批**确定过期**的分区名（按目标表的粒度，且能按名字反解）。
// 用 Desired 在一年前的窗口上取中间的业务分区名，比手写 "p_2019_w01" 更不容易写错粒度。
func businessNamesInPast(spec agentmetrics.Spec, n int) []string {
	past := partitionNow.AddDate(0, 0, -400)
	out := make([]string, 0, n)
	for _, b := range agentmetrics.Desired(spec, past, 1) {
		if b.Name == agentmetrics.GuardPartitionName || b.Name == agentmetrics.SentinelPartitionName {
			continue
		}
		out = append(out, b.Name)
		if len(out) == n {
			break
		}
	}
	return out
}

// newPartitionSvc 装配被测服务（spyLocker / 固定时钟 / 可注入配置）。
//
// 审计仓储用 recordingAuditor（成功、只记账）：审计是**必填**协作者
// （nil = 装配错误，见 NewAgentMetricsPartitionService 的说明），故这里的既有断言
// 必须在一个审计可用的装配下运行 —— 这不是放宽，而是让既有断言与新契约一致。
// 真正落库/失败/降级那几条路径由本文件下半部分的审计测试用真仓储覆盖。
func newPartitionSvc(schema PartitionSchemaRepo, cfg AgentConfigGetter, locker PartitionLocker,
	log logger.LoggerInterface) *AgentMetricsPartitionService {

	return newPartitionSvcWithAuditor(schema, cfg, locker, &recordingAuditor{}, log)
}

// newPartitionSvcWithAuditor 同上，但审计仓储由调用方给（真仓储 / 注入失败的替身 / nil）。
func newPartitionSvcWithAuditor(schema PartitionSchemaRepo, cfg AgentConfigGetter, locker PartitionLocker,
	auditor PartitionAuditor, log logger.LoggerInterface) *AgentMetricsPartitionService {

	return NewAgentMetricsPartitionService(schema, cfg, locker, auditor, log).WithClock(func() time.Time {
		return partitionNow
	})
}

// ── Step 1 的 4 条断言 ─────────────────────────────────

// 1a. 幂等（sqlite + 真仓储）：连续两轮 → 两轮都扫 6 张表、都不执行任何分区 DDL，
// 且第一轮结束后 6 张指标表**确实存在**（sqlite 降级为普通表 —— 这一点由仓储的
// EnsureSchema 负责，协调器只负责调它）。
func TestPartitionReconcileIdempotentWithRealSQLiteRepo(t *testing.T) {
	f := newSQLiteSchemaFixture(t)
	locker := newSpyLocker()
	log := &partitionLogSpy{}
	svc := newPartitionSvc(f.repo, partitionConfig{}, locker, log)

	for round := 1; round <= 2; round++ {
		stats, err := svc.Reconcile(context.Background())
		if err != nil {
			t.Fatalf("第 %d 轮 Reconcile: %v", round, err)
		}
		if stats.TablesScanned != 6 {
			t.Fatalf("第 %d 轮 TablesScanned = %d, want 6", round, stats.TablesScanned)
		}
		// sqlite 没有分区：AddPartitionDDL 返回空 → 「执行了 0 条建分区语句」是正确读数，
		// 不是失败（真实的分区 DDL 语义由 spySchema 那条路径覆盖）。
		if stats.PartitionsCreated != 0 || stats.PartitionsTruncated != 0 || stats.PartitionsDropped != 0 {
			t.Fatalf("第 %d 轮 stats = %+v, want 三段全 0（sqlite 无分区概念）", round, stats)
		}
		if stats.Skipped != 0 {
			t.Fatalf("第 %d 轮 Skipped = %d, want 0", round, stats.Skipped)
		}
	}
	if f.repo.Dialect() != "sqlite" {
		t.Fatalf("Dialect() = %q, want sqlite", f.repo.Dialect())
	}
	if exist, err := f.repo.ExistingPartitions(context.Background(), repository.MetricTables()[0]); err != nil || exist != nil {
		t.Fatalf("sqlite 下 ExistingPartitions = %v/%v, want 空且无错", exist, err)
	}

	names := f.tableNames(t)
	for _, table := range repository.MetricTables() {
		if !names[table] {
			t.Fatalf("第一轮 reconcile 后 %s 不存在：EnsureSchema 没有被调到（启动期断言会失效）", table)
		}
	}
	if locker.heldCount() != 0 {
		t.Fatal("锁未释放：第二轮会被自己的锁挡住")
	}
	if locker.lastKey != partitionLockKeyLiteral {
		t.Fatalf("锁键 = %q, want %q（必须与 SysJob 的 job:lock:<id> 分离）",
			locker.lastKey, partitionLockKeyLiteral)
	}
}

// 1b. 幂等（mysql 方言 + 现状 == 期望）：计划三段全空 → 一条语句都不执行。
//
// 这是「按确定性分区名求差集」的正面断言：现状满足期望时 `PlanFrom` 返回空计划，
// 因此恢复旧备份/重复执行都不会反复下发同一条 DDL。
func TestPartitionReconcileNoopWhenExistingMatchesDesired(t *testing.T) {
	schema := newSpySchema()
	for _, spec := range agentmetrics.TableSpecs() {
		schema.existing[spec.Table] = desiredNamesFor(spec, partitionNow)
	}
	locker := newSpyLocker()
	svc := newPartitionSvc(schema, partitionConfig{}, locker, &partitionLogSpy{})

	for round := 1; round <= 2; round++ {
		stats, err := svc.Reconcile(context.Background())
		if err != nil {
			t.Fatalf("第 %d 轮 Reconcile: %v", round, err)
		}
		if stats.TablesScanned != 6 || stats.PartitionsCreated != 0 ||
			stats.PartitionsTruncated != 0 || stats.PartitionsDropped != 0 {
			t.Fatalf("第 %d 轮 stats = %+v, want 6 表 / 三段全 0（现状满足期望 = 空计划）", round, stats)
		}
	}
	if len(schema.execStmts) != 0 {
		t.Fatalf("执行了 %d 条语句（%v），want 0：现状满足期望时不得下发任何 DDL",
			len(schema.execStmts), schema.execStmts)
	}
	if schema.ensureCalls != 2 {
		t.Fatalf("EnsureSchema 调用 %d 次, want 每轮一次（2）", schema.ensureCalls)
	}
}

// 1c. 建分区：有哨兵走 REORGANIZE、没有哨兵走 ADD PARTITION，且**逐条** Exec（一条语句一次调用）。
func TestPartitionAddDDLUsesSentinelAndExecutesPerStatement(t *testing.T) {
	spec := specByTable(t, entity.TableNameMetric5m)

	// （a）现状含 p_max：缺 3 个未来分区 → 3 条 REORGANIZE（每条拆一次哨兵）。
	full := desiredNamesFor(spec, partitionNow)
	business := make([]string, 0, len(full))
	for _, n := range full {
		if n != agentmetrics.GuardPartitionName && n != agentmetrics.SentinelPartitionName {
			business = append(business, n)
		}
	}
	missing := business[len(business)-3:] // 最后 3 个业务分区
	existing := make([]string, 0, len(full))
	for _, n := range full {
		skip := false
		for _, m := range missing {
			if n == m {
				skip = true
			}
		}
		if !skip {
			existing = append(existing, n)
		}
	}
	// 哨兵必须在场，否则下面断言的不是「有哨兵」那条分支。
	hasSentinel := false
	for _, n := range existing {
		if n == agentmetrics.SentinelPartitionName {
			hasSentinel = true
		}
	}
	if !hasSentinel {
		t.Fatal("前置不成立：existing 里没有 p_max")
	}

	schema := newSpySchema()
	satisfyAllTables(schema)
	schema.existing[spec.Table] = existing
	svc := newPartitionSvc(schema, partitionConfig{}, newSpyLocker(), &partitionLogSpy{})
	stats, err := svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if stats.PartitionsCreated != 3 {
		t.Fatalf("PartitionsCreated = %d, want 3（3 个缺失分区）", stats.PartitionsCreated)
	}
	// 逐条 Exec：语句条数必须等于「计划里的缺失分区数」，也就是 ExecDDL 被调用 3 次；
	// 语句里不得出现分号（分号 = 把多条 DDL 拼成一次调用 = 放弃逐条执行）。
	if len(schema.execStmts) != 3 {
		t.Fatalf("ExecDDL 被调用 %d 次, want 3（必须逐条执行，不得聚合）", len(schema.execStmts))
	}
	for _, stmt := range schema.execStmts {
		if !strings.Contains(stmt, "REORGANIZE PARTITION "+agentmetrics.SentinelPartitionName) {
			t.Fatalf("语句 %q 没有用 REORGANIZE 拆哨兵（MySQL 只能高端追加，直接 ADD 会报 1463）", stmt)
		}
		if strings.Contains(stmt, ";") {
			t.Fatalf("语句 %q 含分号：多条 DDL 被拼成一次执行", stmt)
		}
	}

	// （b）现状**没有**哨兵：缺 1 个分区 → 走 ADD PARTITION。
	schema2 := newSpySchema()
	withoutSentinel := make([]string, 0, len(existing))
	for _, n := range existing {
		if n != agentmetrics.SentinelPartitionName {
			withoutSentinel = append(withoutSentinel, n)
		}
	}
	satisfyAllTables(schema2)
	schema2.existing[spec.Table] = withoutSentinel
	svc2 := newPartitionSvc(schema2, partitionConfig{}, newSpyLocker(), &partitionLogSpy{})
	stats2, err := svc2.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile(无哨兵): %v", err)
	}
	// 缺 p_max 本身 + 3 个业务分区 = 4 条，且全部走 ADD PARTITION。
	if stats2.PartitionsCreated != 4 {
		t.Fatalf("PartitionsCreated = %d, want 4（3 个业务分区 + p_max 自身）", stats2.PartitionsCreated)
	}
	for _, stmt := range schema2.execStmts {
		if !strings.Contains(stmt, "ADD PARTITION") {
			t.Fatalf("语句 %q 不是 ADD PARTITION（无哨兵时必须走追加，而非 REORGANIZE）", stmt)
		}
	}
}

// 1d. 回收：最老 hold(=2) 个过期分区 TRUNCATE、再往前的 DROP，且计数与顺序都对。
func TestPartitionReclaimHoldsOldestAndCountsTruncateDrop(t *testing.T) {
	if partitionHold != 2 {
		t.Fatalf("partitionHold = %d, want 2（spec §7.3：最老 1~2 个分区只 TRUNCATE）", partitionHold)
	}
	spec := specByTable(t, entity.TableNameMetric5m)
	old := businessNamesInPast(spec, 3)
	if len(old) != 3 {
		t.Fatalf("夹具造出的过期分区 = %v, want 3", old)
	}
	existing := append(desiredNamesFor(spec, partitionNow), old...)

	schema := newSpySchema()
	satisfyAllTables(schema)
	schema.existing[spec.Table] = existing
	svc := newPartitionSvc(schema, partitionConfig{}, newSpyLocker(), &partitionLogSpy{})
	stats, err := svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if stats.PartitionsCreated != 0 {
		t.Fatalf("PartitionsCreated = %d, want 0（现状 == 期望）", stats.PartitionsCreated)
	}
	if stats.PartitionsTruncated != 2 || stats.PartitionsDropped != 1 {
		t.Fatalf("stats = %+v, want 2 truncated / 1 dropped（最老 2 个保边界、其余释放空间）", stats)
	}
	if len(schema.execStmts) != 3 {
		t.Fatalf("ExecDDL 被调用 %d 次, want 3", len(schema.execStmts))
	}
	// ReclaimDDL 的输出顺序是「先 truncate 后 drop」：前 2 条 TRUNCATE、最后 1 条 DROP。
	for i, stmt := range schema.execStmts {
		want := "TRUNCATE PARTITION"
		if i >= 2 {
			want = "DROP PARTITION"
		}
		if !strings.Contains(stmt, want) {
			t.Fatalf("第 %d 条语句 = %q, want 含 %q（顺序：先 truncate 后 drop）", i+1, stmt, want)
		}
	}
	if !strings.Contains(schema.execStmts[2], old[2]) {
		t.Fatalf("DROP 的不是最老的之外那个：语句 = %q, want 含 %s", schema.execStmts[2], old[2])
	}
}

// 2. 降级方言安全：sqlite 下 `ReclaimDDL` 会**明确报错**（Plan 2A 的 F2 修复：拒绝生成
// 无界 DELETE）—— 协调器必须只记日志、不中断整轮，其余 5 张表继续处理。
func TestPartitionReconcileDoesNotAbortOnDegradedReclaim(t *testing.T) {
	f := newSQLiteSchemaFixture(t)
	spec := specByTable(t, "device_metric_5m")
	old := businessNamesInPast(spec, 1)
	if len(old) != 1 {
		t.Fatalf("夹具造出的过期分区 = %v, want 1", old)
	}
	// 现状：守卫 + 一个过期业务分区 + 哨兵 → PlanFrom 会给出 Truncate=[那个过期分区]，
	// 于是 ReclaimDDL("sqlite", …) 必定返回错误。
	schema := &expiredSchema{
		PartitionSchemaRepo: f.repo,
		table:               spec.Table,
		existing:            []string{agentmetrics.GuardPartitionName, old[0], agentmetrics.SentinelPartitionName},
	}
	locker := newSpyLocker()
	log := &partitionLogSpy{}
	svc := newPartitionSvc(schema, partitionConfig{}, locker, log)

	stats, err := svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile = %v, want nil：降级方言「无法回收」是常态，不得上抛（否则调用方每轮都误判「分区未就绪」）", err)
	}
	// 关键断言：那 6 张表**都**被对账过（若遇回收错误即中断，这里会是 1）。
	if stats.TablesScanned != 6 {
		t.Fatalf("TablesScanned = %d, want 6（回收失败不得中断整轮）", stats.TablesScanned)
	}
	if got := len(schema.queriedTables()); got != 6 {
		t.Fatalf("被查询过现状的表数 = %d（%v）, want 6", got, schema.queriedTables())
	}
	if stats.PartitionsTruncated != 0 || stats.PartitionsDropped != 0 || stats.PartitionsCreated != 0 {
		t.Fatalf("stats = %+v, want 三段全 0（sqlite 上回收被拒绝、建分区被降级）", stats)
	}
	if !log.hasWarn("无法生成分区回收语句") {
		t.Fatalf("回收失败必须 log.Warn（只记日志不是不记日志）；warns = %v", log.warns)
	}
	if locker.heldCount() != 0 {
		t.Fatal("锁未释放")
	}
}

// 3. 锁：并发两轮 → 同一时刻只有一个真正执行（第二轮 Skipped=1 且一张表都没查）。
func TestPartitionReconcileConcurrentRunsOnlyOne(t *testing.T) {
	f := newSQLiteSchemaFixture(t)
	schema := newBlockingSchema(f.repo)
	locker := newSpyLocker()
	log := &partitionLogSpy{}
	svc := newPartitionSvc(schema, partitionConfig{}, locker, log)

	type result struct {
		stats PartitionStats
		err   error
	}
	first := make(chan result, 1)
	go func() {
		stats, err := svc.Reconcile(context.Background())
		first <- result{stats, err}
	}()
	// 无论断言在哪一步失败，都要放行第一轮（否则它的 goroutine 会永久挂在 <-release 上，
	// 让「变异探针的红灯」变成超时而不是一条可读的断言失败）。
	t.Cleanup(schema.releaseFirst)
	// 等第一轮真的进到临界区（正在查第一张表的现状），再发第二轮。
	<-schema.entered

	stats2, err2 := svc.Reconcile(context.Background())
	if err2 != nil {
		t.Fatalf("第二轮 Reconcile = %v, want nil（被锁挡住不是错误）", err2)
	}
	if stats2.Skipped != 1 {
		t.Fatalf("第二轮 Skipped = %d, want 1（同一时刻只允许一轮执行）", stats2.Skipped)
	}
	if stats2.TablesScanned != 0 || stats2.PartitionsCreated != 0 {
		t.Fatalf("第二轮 stats = %+v, want 全 0（跳过时不得碰任何表）", stats2)
	}
	if locker.tryCalls != 2 {
		t.Fatalf("TryLock 调用 %d 次, want 2", locker.tryCalls)
	}

	schema.releaseFirst()
	got := <-first
	if got.err != nil {
		t.Fatalf("第一轮 Reconcile = %v", got.err)
	}
	if got.stats.TablesScanned != 6 {
		t.Fatalf("第一轮 TablesScanned = %d, want 6", got.stats.TablesScanned)
	}
	if got.stats.Skipped != 0 {
		t.Fatalf("第一轮 Skipped = %d, want 0（它才是真正执行的那一轮）", got.stats.Skipped)
	}
	if locker.heldCount() != 0 {
		t.Fatal("第一轮的锁未释放：并发 TestPartition 会互相干扰")
	}
	if len(log.infos) == 0 {
		t.Fatal("第二轮被跳过时必须有一条日志（静默跳过无法排查）")
	}
}

// 3b. 调度独立性：锁键/超时/租约全部来自**本服务自己的**配置键，
// 改 SysJob 的全局 maxExec / lockTTL 完全不影响它。
func TestPartitionLockerIndependentFromSchedulerConfig(t *testing.T) {
	f := newSQLiteSchemaFixture(t)
	locker := newSpyLocker()
	// 故意把调度器的全局键设成极端值：它们**不得**对本服务产生任何影响。
	cfg := partitionConfig{vals: map[string]int{
		"sys.scheduler.maxExecutionTime": 99999,
		"sys.scheduler.lockTTL":          99999,
	}}
	svc := newPartitionSvc(f.repo, cfg, locker, &partitionLogSpy{})
	if _, err := svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if locker.lastKey != partitionLockKeyLiteral {
		t.Fatalf("锁键 = %q, want %q", locker.lastKey, partitionLockKeyLiteral)
	}
	// 默认 TTL = 600s（一轮上界 420s 的 ~1.4 倍）；它必须**不**被 sys.scheduler.lockTTL(99999s) 影响。
	if locker.lastTTL != 600*time.Second {
		t.Fatalf("锁 TTL = %v, want 600s（不得复用 SysJob 的全局 lockTTL）", locker.lastTTL)
	}
	if !strings.Contains(locker.lastOwner, ":") {
		t.Fatalf("锁 owner = %q, want `host:pid` 形态（Unlock 是比对持有者的语义）", locker.lastOwner)
	}

	// 每表超时可独立配置；锁租约必须**覆盖整轮上界**（1 次建表 + 6 表 × 每表超时 + 30s 缓冲），
	// 否则慢轮的锁会在执行中过期，另一轮并发进来（scheduler 的同款理由）。
	locker2 := newSpyLocker()
	cfg2 := partitionConfig{vals: map[string]int{
		"sys.agent.partitionTableTimeout": 60,
		"sys.agent.partitionLockTTL":      10, // 故意配得比一轮上界小
	}}
	svc2 := newPartitionSvc(f.repo, cfg2, locker2, &partitionLogSpy{})
	if _, err := svc2.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile(2): %v", err)
	}
	if want := 7*60*time.Second + 30*time.Second; locker2.lastTTL != want {
		t.Fatalf("锁 TTL = %v, want %v（租约必须覆盖 1 次建表 + 6 张表的每表超时）",
			locker2.lastTTL, want)
	}
}

// 3c. 每表一个超时：一张表的现状查询拖到超时，其余 5 张表照样对账完（不陪跑）。
func TestPartitionPerTableTimeoutDoesNotBlockOtherTables(t *testing.T) {
	f := newSQLiteSchemaFixture(t)
	schema := &slowSchema{PartitionSchemaRepo: f.repo, table: "device_metric_nic"}
	cfg := partitionConfig{vals: map[string]int{"sys.agent.partitionTableTimeout": 1}}
	svc := newPartitionSvc(schema, cfg, newSpyLocker(), &partitionLogSpy{})

	start := time.Now()
	stats, err := svc.Reconcile(context.Background())
	elapsed := time.Since(start)

	if err == nil || !errors.Is(err, ErrPartitionPartial) {
		t.Fatalf("err = %v, want ErrPartitionPartial（一张表超时必须记成失败）", err)
	}
	if stats.TablesScanned != 6 {
		t.Fatalf("TablesScanned = %d, want 6（超时的表只影响它自己）", stats.TablesScanned)
	}
	// 1s（超时）+ 另外 5 张表的毫秒级查询：绝不会到「6 × 1s = 6s」。
	if elapsed > 4*time.Second {
		t.Fatalf("整轮耗时 %v：每表超时没有生效（像是整轮一个超时）", elapsed)
	}
	if elapsed < 900*time.Millisecond {
		t.Fatalf("整轮耗时 %v：慢表没有被等到自己的超时", elapsed)
	}
}

// 4. WorstCaseRetention 上界（spec §7.3 的量化结论钉成测试）：30d→37d、180d→210d。
func TestPartitionWorstCaseRetentionBounds(t *testing.T) {
	const day = 24 * time.Hour
	type want struct {
		table     string
		gran      agentmetrics.Granularity
		retention time.Duration
		worst     time.Duration
	}
	wants := []want{
		{"device_metric_5m", agentmetrics.GranularityWeek, 30 * day, 37 * day},
		{"device_metric_1h", agentmetrics.GranularityMonth, 180 * day, 210 * day},
		{"device_metric_disk", agentmetrics.GranularityWeek, 30 * day, 37 * day},
		{"device_metric_diskio", agentmetrics.GranularityWeek, 30 * day, 37 * day},
		{"device_metric_nic", agentmetrics.GranularityWeek, 30 * day, 37 * day},
		{"device_metric_sensor", agentmetrics.GranularityWeek, 30 * day, 37 * day},
	}
	specs := agentmetrics.TableSpecs()
	if len(specs) != 6 {
		t.Fatalf("TableSpecs() = %d 张表, want 6", len(specs))
	}
	byTable := make(map[string]agentmetrics.Spec, len(specs))
	for _, s := range specs {
		byTable[s.Table] = s
	}
	metricTables := make(map[string]bool)
	for _, name := range repository.MetricTables() {
		metricTables[name] = true
	}
	for _, w := range wants {
		spec, ok := byTable[w.table]
		if !ok {
			t.Fatalf("%s 不在 TableSpecs 里", w.table)
		}
		if !metricTables[w.table] {
			t.Fatalf("%s 不在 repository.MetricTables() 里（协调器与仓储的表集合必须同源）", w.table)
		}
		if spec.Granularity != w.gran {
			t.Fatalf("%s 粒度 = %v, want %v", w.table, spec.Granularity, w.gran)
		}
		// 保留期取 spec 默认值（运行期可被 sys.agent.* 覆盖，但默认值必须与 §7.3 一致）。
		if spec.Retention != w.retention {
			t.Fatalf("%s 保留期 = %v, want %v", w.table, spec.Retention, w.retention)
		}
		if got := agentmetrics.WorstCaseRetention(spec); got != w.worst {
			t.Fatalf("%s WorstCaseRetention = %v, want %v", w.table, got, w.worst)
		}
		// 上界的定义式：保留期 + **一个**分区周期（这就是「30d 的表不能用月分区」的量化依据）。
		period := 7 * day
		if spec.Granularity == agentmetrics.GranularityMonth {
			period = 30 * day
		}
		if got := agentmetrics.WorstCaseRetention(spec); got > spec.Retention+period {
			t.Fatalf("%s WorstCaseRetention = %v > 保留期 + 一个周期 %v", w.table, got, spec.Retention+period)
		}
	}
}

// 4b. 保留期运行期覆盖：改 sys.agent.historyRetentionDays 会直接改变期望窗口
// （1h 表用另一把键）—— 配置写错（<=0）时回落默认值，不得把分区全判成过期。
func TestPartitionRetentionOverrideAndInvalidFallback(t *testing.T) {
	spec := specByTable(t, "device_metric_5m")
	// 现状：完整期望窗口（用**默认** 30 天保留期算出来）+ 哨兵。
	existing := desiredNamesFor(spec, partitionNow)

	schema := newSpySchema()
	satisfyAllTables(schema)
	schema.existing[spec.Table] = existing
	svc := newPartitionSvc(schema, partitionConfig{vals: map[string]int{
		"sys.agent.historyRetentionDays": 10, // 保留期缩短到 10 天
	}}, newSpyLocker(), &partitionLogSpy{})
	stats, err := svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// 窗口跟着变短 → 老分区滑出期望窗口 → 被回收（而不仅仅是补缺）；同时窗口起点后移，
	// 最老那些周期不再属于期望集合（但也在 existing 里、也一样过期）→ 只回收、不重建。
	if stats.PartitionsTruncated+stats.PartitionsDropped == 0 {
		t.Fatalf("stats = %+v, want 至少有回收：保留期改成 10 天后老分区必须滑出窗口", stats)
	}
	if stats.PartitionsCreated != 0 {
		t.Fatalf("PartitionsCreated = %d, want 0（缩短保留期不会产生缺口）", stats.PartitionsCreated)
	}

	// 非法值（0）：回落默认值 30 天 → 现状 == 期望 → 一条语句都不下发。
	schema2 := newSpySchema()
	satisfyAllTables(schema2)
	schema2.existing[spec.Table] = existing
	svc2 := newPartitionSvc(schema2, partitionConfig{vals: map[string]int{
		"sys.agent.historyRetentionDays": 0,
	}}, newSpyLocker(), &partitionLogSpy{})
	stats2, err := svc2.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile(非法配置): %v", err)
	}
	if stats2.PartitionsCreated != 0 || stats2.PartitionsTruncated != 0 || stats2.PartitionsDropped != 0 {
		t.Fatalf("stats = %+v, want 三段全 0（保留期填 0 必须回落默认值，而不是把分区全判成过期）", stats2)
	}
	if len(schema2.execStmts) != 0 {
		t.Fatalf("下发了 %d 条语句（%v）, want 0", len(schema2.execStmts), schema2.execStmts)
	}

	// 1h 表读的是**另一把键**（sys.agent.metrics1hRetentionDays）：把它改成 30 天，
	// 1h 表的老分区必须滑出窗口被回收。
	spec1h := specByTable(t, entity.TableNameMetric1h)
	existing1h := desiredNamesFor(spec1h, partitionNow)
	schema3 := newSpySchema()
	satisfyAllTables(schema3)
	schema3.existing[spec1h.Table] = existing1h
	svc3 := newPartitionSvc(schema3, partitionConfig{vals: map[string]int{
		"sys.agent.metrics1hRetentionDays": 30,
	}}, newSpyLocker(), &partitionLogSpy{})
	stats3, err := svc3.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile(1h 缩短保留期): %v", err)
	}
	if stats3.PartitionsTruncated+stats3.PartitionsDropped == 0 {
		t.Fatalf("stats = %+v, want 有回收：1h 表必须读 sys.agent.metrics1hRetentionDays", stats3)
	}
	if stats3.PartitionsCreated != 0 {
		t.Fatalf("PartitionsCreated = %d, want 0", stats3.PartitionsCreated)
	}
	// 隔离：这一段配置只该动 1h 表 —— 下发的回收语句必须**全部**指向 device_metric_1h
	//（其余 5 张表的现状 == 期望，本就无事可做）。
	for _, stmt := range schema3.execStmts {
		if !strings.Contains(stmt, entity.TableNameMetric1h) {
			t.Fatalf("语句 %q 不指向 1h 表：5m 档的保留期键串味到了别的表", stmt)
		}
	}

	// 交叉验证：只改 5m 档那把键**不得**影响 1h 表（否则两档保留期会互相串味）。
	// 只铺 1h 表：其余表在 spySchema 里是「一条分区都没有」，会各自补建（那些计数与
	// 本断言无关，故意不铺底 —— 铺底反而会把 5m 档的回收混进来，第一版就踩了这个坑）。
	schema4 := newSpySchema()
	schema4.existing[spec1h.Table] = existing1h
	svc4 := newPartitionSvc(schema4, partitionConfig{vals: map[string]int{
		"sys.agent.historyRetentionDays": 10,
	}}, newSpyLocker(), &partitionLogSpy{})
	stats4, err := svc4.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile(1h + 5m 档配置): %v", err)
	}
	if stats4.PartitionsTruncated != 0 || stats4.PartitionsDropped != 0 {
		t.Fatalf("stats = %+v, want 0 回收（1h 表不得读 5m 档的保留期键）", stats4)
	}
	for _, stmt := range schema4.execStmts {
		if strings.Contains(stmt, entity.TableNameMetric1h) {
			t.Fatalf("语句 %q 动了 1h 的分区：1h 表读到了 5m 档的保留期键", stmt)
		}
	}
}

// 5. 错误路径：EnsureSchema 失败、某表查现状失败、建分区 DDL 失败 → 都记进
// ErrPartitionPartial、都继续处理其余表（best-effort；启动期不能因为一次 DDL 失败就起不来）。
func TestPartitionPartialFailuresAreAggregatedAndContinue(t *testing.T) {
	spec := specByTable(t, "device_metric_5m")

	// （a）EnsureSchema 失败。
	schemaA := newSpySchema()
	schemaA.ensureErr = errors.New("permission denied")
	svcA := newPartitionSvc(schemaA, partitionConfig{}, newSpyLocker(), &partitionLogSpy{})
	statsA, errA := svcA.Reconcile(context.Background())
	if errA == nil || !errors.Is(errA, ErrPartitionPartial) {
		t.Fatalf("EnsureSchema 失败时 err = %v, want ErrPartitionPartial", errA)
	}
	if statsA.TablesScanned != 6 {
		t.Fatalf("TablesScanned = %d, want 6（建表失败仍要逐表对账）", statsA.TablesScanned)
	}

	// （b）一张表查现状失败。
	schemaB := newSpySchema()
	schemaB.existingErr[spec.Table] = errors.New("information_schema timeout")
	svcB := newPartitionSvc(schemaB, partitionConfig{}, newSpyLocker(), &partitionLogSpy{})
	statsB, errB := svcB.Reconcile(context.Background())
	if errB == nil || !errors.Is(errB, ErrPartitionPartial) {
		t.Fatalf("查现状失败时 err = %v, want ErrPartitionPartial", errB)
	}
	if !strings.Contains(errB.Error(), spec.Table) {
		t.Fatalf("err = %v, want 含失败表名 %s（否则运维不知道是哪张表）", errB, spec.Table)
	}
	if statsB.TablesScanned != 6 {
		t.Fatalf("TablesScanned = %d, want 6", statsB.TablesScanned)
	}

	// （c）建分区 DDL 执行失败。
	schemaC := newSpySchema()
	satisfyAllTables(schemaC)
	schemaC.existing[spec.Table] = []string{agentmetrics.GuardPartitionName}
	schemaC.existing["device_metric_1h"] = []string{agentmetrics.GuardPartitionName}
	schemaC.execErrOn = 1
	svcC := newPartitionSvc(schemaC, partitionConfig{}, newSpyLocker(), &partitionLogSpy{})
	statsC, errC := svcC.Reconcile(context.Background())
	if errC == nil || !errors.Is(errC, ErrPartitionPartial) {
		t.Fatalf("DDL 失败时 err = %v, want ErrPartitionPartial", errC)
	}
	// 第 1 条语句被注入失败、那条表随即停止（不再继续下发它的其余语句），
	// 但**其余 5 张表照常**（它们已满足现状、无事可做）：于是计数必须恰好等于
	// 「成功执行的语句数」= 总下发数 − 1。
	if len(schemaC.execStmts) < 1 {
		t.Fatal("前置不成立：没有下发任何语句")
	}
	if statsC.PartitionsCreated != len(schemaC.execStmts)-1 {
		t.Fatalf("PartitionsCreated = %d, 下发 %d 条（第 1 条失败）, want %d（失败的那条不得计数）",
			statsC.PartitionsCreated, len(schemaC.execStmts), len(schemaC.execStmts)-1)
	}
	if statsC.TablesScanned != 6 {
		t.Fatalf("TablesScanned = %d, want 6", statsC.TablesScanned)
	}
}

// 6. 锁获取失败（Redis 故障）必须上抛：静默当成「拿到了」会让两轮并发，
// 静默当成「被占用」会让分区维护永久停摆而无人知道。
func TestPartitionLockErrorIsReturned(t *testing.T) {
	schema := newSpySchema()
	locker := newSpyLocker()
	locker.tryErr = errors.New("redis down")
	svc := newPartitionSvc(schema, partitionConfig{}, locker, &partitionLogSpy{})

	stats, err := svc.Reconcile(context.Background())
	if err == nil {
		t.Fatal("锁获取失败必须上抛")
	}
	if errors.Is(err, ErrPartitionPartial) {
		t.Fatalf("err = %v：锁故障不是「部分表失败」，不该套 sentinel", err)
	}
	if stats != (PartitionStats{}) {
		t.Fatalf("stats = %+v, want 零值（拿不到锁时什么都没做）", stats)
	}
	if schema.ensureCalls != 0 || len(schema.execStmts) != 0 {
		t.Fatal("拿不到锁时不得执行任何 DDL")
	}
}

// 7. 多实例：owner 不同 → 一个实例拿不到锁时另一实例的锁**不会**被误删
// （Unlock 是「比对持有者再删」的语义，owner 必须进程级唯一）。
func TestPartitionUnlockIsOwnerScoped(t *testing.T) {
	locker := newSpyLocker()
	if ok, err := locker.TryLock(context.Background(), partitionLockKeyLiteral, "a:1", time.Minute); err != nil || !ok {
		t.Fatalf("TryLock(a:1) = %v/%v", ok, err)
	}
	if err := locker.Unlock(context.Background(), partitionLockKeyLiteral, "b:2"); err != nil {
		t.Fatalf("Unlock(b:2): %v", err)
	}
	if locker.heldCount() != 1 {
		t.Fatal("非持有者的 Unlock 删掉了锁：owner 语义被破坏（跨实例误删）")
	}
	svc := newPartitionSvc(newSpySchema(), partitionConfig{}, locker, &partitionLogSpy{})
	if !strings.HasSuffix(svc.owner, fmt.Sprintf(":%d", os.Getpid())) {
		t.Fatalf("owner = %q, want 以 :pid 结尾", svc.owner)
	}
}

// ── spec §7.3 审计表（agent_metric_partition_log）──────────────────
//
// 本节的 8 条断言覆盖计划 Task 3 Step 1 的 6 项（建表/守卫在 migration 包）：
//   1. 一次 Reconcile 写出「N 条 add + M 条 drop」且区间来自计划 → TestPartitionLogWritesAddAndDropRowsWithPlanBounds
//   2. 保留期快照（当时的配置、旧行不被改写）→ TestPartitionLogRetentionSnapshot*
//   3. 两次提交：审计写入不与 DDL 共事务、失败不回滚 DDL、不得静默
//      → TestPartitionLogFailureDoesNotUndoDDLAndIsNotSilent / TestPartitionLogMissingAuditorIsLoud
//   4. 降级方言（ReclaimDDL 报错）不得写 drop 行 → TestPartitionLogNoDropRowsOnDegradedDialect
//   5. 回收 DDL 本身失败时同样不写 drop 行（没有回收发生）→ TestPartitionLogNoDropRowWhenReclaimDDLFails
//   6. 审计表不参与任何回收/建分区 → TestPartitionLogTableIsNotReconciled

// recordingAuditor 是 PartitionAuditor 的替身：**成功**且逐条记账。
//
// 用途是让既有断言（幂等、锁、超时…）在一个「审计可用」的装配下运行 ——
// 审计仓储是必填协作者（nil = 装配错误），不是可选的装饰。
type recordingAuditor struct {
	rows []*entity.AgentPartitionLog
	err  error
}

func (a *recordingAuditor) Insert(_ context.Context, row *entity.AgentPartitionLog) error {
	if a.err != nil {
		return a.err
	}
	a.rows = append(a.rows, row)
	return nil
}

// partitionLogFixture：真 sqlite 审计表 + 真仓储（生产同款雪花回调）。
//
// 为什么这里要真表：审计的整条价值链是「写进去 → 事后能按表读回来做缺口归因」，
// 用记账替身只能证明「服务调了 Insert」，证明不了「行真的落库、NULL 真的还是 NULL」。
// DDL 侧仍然用 spySchema（mysql 方言）：sqlite 没有分区概念，真仓储下
// AddPartitionDDL/ReclaimDDL 全是降级空实现，回收与补缺一条都验不了。
type partitionLogFixture struct {
	db   *gorm.DB
	repo *repository.AgentPartitionLogRepo
}

func newPartitionLogFixture(t *testing.T) *partitionLogFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:audit_"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	node, err := snowflake.New(1, logger.NewNop())
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	database.NewCallbacks(node).Register(db)
	if err := db.AutoMigrate(&entity.AgentPartitionLog{}); err != nil {
		t.Fatalf("AutoMigrate(审计表): %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, derr := db.DB(); derr == nil {
			_ = sqlDB.Close()
		}
	})
	return &partitionLogFixture{db: db, repo: repository.NewAgentPartitionLogRepository(db)}
}

// rows 按表读回审计流水（table 为空 = 全部表），顺序 = 写入顺序。
func (f *partitionLogFixture) rows(t *testing.T, table string) []entity.AgentPartitionLog {
	t.Helper()
	rows, err := f.repo.ListByTable(context.Background(), table, 0)
	if err != nil {
		t.Fatalf("读审计流水: %v", err)
	}
	return rows
}

// desiredBoundsFor 返回某表在 `now` 时刻的完整期望分区清单（含 Bound）。
func desiredBoundsFor(spec agentmetrics.Spec, now time.Time) []agentmetrics.Bound {
	return agentmetrics.Desired(spec, now, horizonFor(spec))
}

// namesOfBounds 取名字清单。
func namesOfBounds(bounds []agentmetrics.Bound) []string {
	out := make([]string, 0, len(bounds))
	for _, b := range bounds {
		out = append(out, b.Name)
	}
	return out
}

// businessBounds 过滤掉守卫与哨兵（只留业务分区）。
func businessBounds(bounds []agentmetrics.Bound) []agentmetrics.Bound {
	out := make([]agentmetrics.Bound, 0, len(bounds))
	for _, b := range bounds {
		if b.Name == agentmetrics.GuardPartitionName || b.Name == agentmetrics.SentinelPartitionName {
			continue
		}
		out = append(out, b)
	}
	return out
}

// pastBounds 造一批**确定过期**的老分区（400 天前），并带上它们的 Bound ——
// 审计断言要与回收判定比对区间，故这里必须连 Bound 一起造（只造名字不够）。
func pastBounds(spec agentmetrics.Spec, now time.Time, n int) []agentmetrics.Bound {
	past := now.AddDate(0, 0, -400)
	out := businessBounds(agentmetrics.Desired(spec, past, 1))
	if len(out) < n {
		panic("夹具造不出足够的过期分区")
	}
	return out[:n]
}

// withoutNames 从名字清单里摘掉给定的几个名字（顺序保持）。
func withoutNames(all []string, drop ...string) []string {
	gone := make(map[string]bool, len(drop))
	for _, d := range drop {
		gone[d] = true
	}
	out := make([]string, 0, len(all))
	for _, n := range all {
		if !gone[n] {
			out = append(out, n)
		}
	}
	return out
}

// auditCase5m 造「5m 表：缺 3 个未来分区 + 3 个过期老分区」的现状，其余 5 张表铺成满意态。
//
// 返回的 existing 就是 spySchema 的现状，missing/past 是测试**独立**算出的期望
// （不复用服务的任何中间量）。
func auditCase5m(t *testing.T, schema *spySchema) (spec agentmetrics.Spec,
	existing []string, missing, past []agentmetrics.Bound) {

	t.Helper()
	spec = specByTable(t, entity.TableNameMetric5m)
	satisfyAllTables(schema)
	desired := desiredBoundsFor(spec, partitionNow)
	business := businessBounds(desired)
	missing = business[len(business)-3:]
	past = pastBounds(spec, partitionNow, 3)
	names := withoutNames(namesOfBounds(desired), namesOfBounds(missing)...)
	existing = append(names, namesOfBounds(past)...)
	schema.existing[spec.Table] = existing
	return spec, existing, missing, past
}

// daysOf 把可空的「保留期天数快照」读成整数（nil → -1）。
//
// 断言消息里必须出现**值**而不是指针：`%v` 打在 *int32 上只会打印一个地址，
// 那种红灯看不出「写成了什么」，等于把断言的价值削掉一半（反向验证时实测过）。
func daysOf(p *int32) int32 {
	if p == nil {
		return -1
	}
	return *p
}

// assertSameLogRow 逐字段比对两行审计流水（「旧行不被改写」必须逐字段成立）。
func assertSameLogRow(t *testing.T, got, want entity.AgentPartitionLog, ctx string) {
	t.Helper()
	if got.ID != want.ID || got.Table != want.Table || got.PartitionName != want.PartitionName ||
		got.Action != want.Action || got.LowerBound != want.LowerBound || got.UpperBound != want.UpperBound {
		t.Fatalf("%s：行被改写了（id %d→%d, %s/%s→%s/%s, %s→%s, [%d,%d)→[%d,%d)）",
			ctx, want.ID, got.ID, want.Table, want.PartitionName, got.Table, got.PartitionName,
			want.Action, got.Action, want.LowerBound, want.UpperBound, got.LowerBound, got.UpperBound)
	}
	if !got.CreatedAt.UTC().Equal(want.CreatedAt.UTC()) {
		t.Fatalf("%s：created_at 被改写（%v→%v）", ctx, want.CreatedAt.UTC(), got.CreatedAt.UTC())
	}
	if (got.DroppedAt == nil) != (want.DroppedAt == nil) {
		t.Fatalf("%s：dropped_at 的 NULL 性被改写（%v→%v）", ctx, want.DroppedAt, got.DroppedAt)
	}
	if got.DroppedAt != nil && !got.DroppedAt.UTC().Equal(want.DroppedAt.UTC()) {
		t.Fatalf("%s：dropped_at 被改写（%v→%v）", ctx, want.DroppedAt.UTC(), got.DroppedAt.UTC())
	}
	if (got.RowCount == nil) != (want.RowCount == nil) {
		t.Fatalf("%s：row_count 的 NULL 性被改写", ctx)
	}
	if (got.RetentionDays == nil) != (want.RetentionDays == nil) {
		t.Fatalf("%s：retention_days 的 NULL 性被改写（%d→%d）", ctx, daysOf(want.RetentionDays), daysOf(got.RetentionDays))
	}
	if got.RetentionDays != nil && *got.RetentionDays != *want.RetentionDays {
		t.Fatalf("%s：retention_days 被改写（%d→%d）—— 历史流水必须保持当时的值",
			ctx, *want.RetentionDays, *got.RetentionDays)
	}
}

// 断言 1（计划 Step 1 第 3 项）：一次 Reconcile 里「建了 N 个分区 / 回收了 M 个分区」
// → 审计表恰好 N 条 action=add + M 条 action=drop，区间与 PlanFrom 的 Bound 一致。
func TestPartitionLogWritesAddAndDropRowsWithPlanBounds(t *testing.T) {
	f := newPartitionLogFixture(t)
	schema := newSpySchema()
	spec, existing, missing, past := auditCase5m(t, schema)
	log := &partitionLogSpy{}
	svc := newPartitionSvcWithAuditor(schema, partitionConfig{}, newSpyLocker(), f.repo, log)

	stats, err := svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if stats.PartitionsCreated != 3 || stats.PartitionsTruncated != 2 || stats.PartitionsDropped != 1 {
		t.Fatalf("stats = %+v, want 3 created / 2 truncated / 1 dropped（夹具前置不成立）", stats)
	}
	if stats.AuditFailures != 0 {
		t.Fatalf("AuditFailures = %d, want 0（本用例的审计仓储是好的）", stats.AuditFailures)
	}

	rows := f.rows(t, spec.Table)
	// 先按 action 计数（这一步是「回收了 M 个分区 → M 条 drop」的直接断言，
	// 故放在总数断言之前，好让「漏写 drop 流水」的变异在这里就得到一条明确的红灯）。
	var adds, drops int
	for _, r := range rows {
		switch r.Action {
		case entity.PartitionActionAdd:
			adds++
		case entity.PartitionActionDrop:
			drops++
		default:
			t.Fatalf("未知 action = %q", r.Action)
		}
	}
	if adds != 3 || drops != 3 {
		t.Fatalf("add/drop 计数 = %d/%d, want 3/3（回收了 3 个分区 → 3 条 drop）", adds, drops)
	}
	// 回收 = truncate + drop，两者都记 action=drop（对缺口归因是同一件事）。
	if want := stats.PartitionsCreated + stats.PartitionsTruncated + stats.PartitionsDropped; len(rows) != want {
		t.Fatalf("审计行数 = %d, want %d（= 3 add + 3 drop）", len(rows), want)
	}
	// 写入顺序 = 发生顺序：补缺在前，回收在后（先 TRUNCATE 后 DROP）。
	wantActions := []string{
		entity.PartitionActionAdd, entity.PartitionActionAdd, entity.PartitionActionAdd,
		entity.PartitionActionDrop, entity.PartitionActionDrop, entity.PartitionActionDrop,
	}
	for i, want := range wantActions {
		if rows[i].Action != want {
			t.Fatalf("第 %d 条 action = %q, want %q", i+1, rows[i].Action, want)
		}
	}

	// add 行：区间必须与 PlanFrom 的 Bound **完全一致**（测试独立重算一次计划）。
	plan := agentmetrics.PlanFrom(spec, existing, partitionNow, horizonFor(spec), partitionHold)
	if len(plan.Create) != 3 {
		t.Fatalf("计划里的补缺 = %d, want 3", len(plan.Create))
	}
	for i, b := range plan.Create {
		row := rows[i]
		if row.PartitionName != b.Name || row.LowerBound != b.Lower || row.UpperBound != b.Upper {
			t.Fatalf("第 %d 条 add 行 = %s [%d,%d), want %s [%d,%d)（区间必须取自计划）",
				i+1, row.PartitionName, row.LowerBound, row.UpperBound, b.Name, b.Lower, b.Upper)
		}
		if row.DroppedAt != nil {
			t.Fatalf("add 行的 dropped_at = %v, want NULL（补齐不是「丢数据」）", row.DroppedAt)
		}
	}
	// 第二份、**不复用 PlanFrom** 的期望：夹具自己挑的那 3 个缺失分区。
	for i, b := range missing {
		if rows[i].PartitionName != b.Name {
			t.Fatalf("第 %d 条 add 行 = %q, want %q", i+1, rows[i].PartitionName, b.Name)
		}
	}

	// drop 行：名字与区间来自**回收判定用的同一个反解函数**，且必须落在夹具造的 3 个老分区上。
	if got := append(append([]string{}, plan.Truncate...), plan.Drop...); len(got) != 3 {
		t.Fatalf("计划里的回收 = %v, want 3 条", got)
	} else {
		for i, name := range got {
			if name != past[i].Name {
				t.Fatalf("第 %d 条回收的分区 = %q, want %q（最老的 2 个 TRUNCATE、再往前的 DROP）",
					i+1, name, past[i].Name)
			}
		}
	}
	for i, name := range append(append([]string{}, plan.Truncate...), plan.Drop...) {
		row := rows[3+i]
		b, ok := agentmetrics.BoundFromName(name, spec.Granularity)
		if !ok {
			t.Fatalf("夹具前置不成立：%q 反解不出区间", name)
		}
		if row.PartitionName != name || row.LowerBound != b.Lower || row.UpperBound != b.Upper {
			t.Fatalf("第 %d 条 drop 行 = %s [%d,%d), want %s [%d,%d)",
				i+1, row.PartitionName, row.LowerBound, row.UpperBound, name, b.Lower, b.Upper)
		}
		if row.DroppedAt == nil || !row.DroppedAt.UTC().Equal(partitionNow) {
			t.Fatalf("第 %d 条 drop 行的 dropped_at = %v, want %v（回收发生的时刻）",
				i+1, row.DroppedAt, partitionNow)
		}
	}

	// 口径与快照：RowCount 一律 NULL；RetentionDays = 当时的配置（5m 档默认 30 天）；
	// CreatedAt = 本轮时钟；表名 = 指标表。
	for i, r := range rows {
		if r.RowCount != nil {
			t.Fatalf("第 %d 行 row_count = %d, want NULL（不为了记行数去做全分区 COUNT(*)）", i+1, *r.RowCount)
		}
		if r.RetentionDays == nil || *r.RetentionDays != 30 {
			t.Fatalf("第 %d 行 retention_days = %d, want 30（当时 5m 档的保留期配置）", i+1, daysOf(r.RetentionDays))
		}
		if !r.CreatedAt.UTC().Equal(partitionNow) {
			t.Fatalf("第 %d 行 created_at = %v, want %v（与本轮对账同一个时钟）", i+1, r.CreatedAt.UTC(), partitionNow)
		}
		if r.Table != spec.Table {
			t.Fatalf("第 %d 行 table_name = %q, want %q", i+1, r.Table, spec.Table)
		}
	}
	// 其它 5 张表本轮无事可做 → 一条流水都不该有。
	for _, other := range agentmetrics.TableSpecs() {
		if other.Table == spec.Table {
			continue
		}
		if got := f.rows(t, other.Table); len(got) != 0 {
			t.Fatalf("%s 本轮没有分区改动，却有 %d 条审计流水", other.Table, len(got))
		}
	}
}

// 断言 2a（计划 Step 1 第 4 项）：RetentionDays 记的是**当时**的配置值；
// 改配置后再跑一轮 → 新行的值与旧行不同，**旧行不被改写**。
func TestPartitionLogRetentionSnapshotPerRoundAndOldRowsImmutable(t *testing.T) {
	f := newPartitionLogFixture(t)
	spec := specByTable(t, entity.TableNameMetric5m)
	schema := newSpySchema()
	satisfyAllTables(schema)

	// 现状：30 天窗口 − 最后 2 个业务分区（→ 2 条补缺，足以产生 add 流水）。
	desired := desiredBoundsFor(spec, partitionNow)
	business := businessBounds(desired)
	names := withoutNames(namesOfBounds(desired), business[len(business)-1].Name, business[len(business)-2].Name)
	schema.existing[spec.Table] = names

	cfg := partitionConfig{vals: map[string]int{configAgentHistoryRetentionDays: 30}}
	svc := newPartitionSvcWithAuditor(schema, cfg, newSpyLocker(), f.repo, &partitionLogSpy{})

	stats1, err := svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("第 1 轮 Reconcile: %v", err)
	}
	if stats1.PartitionsCreated != 2 || stats1.PartitionsTruncated != 0 || stats1.PartitionsDropped != 0 {
		t.Fatalf("第 1 轮 stats = %+v, want 2 created / 0 回收（夹具前置不成立）", stats1)
	}
	round1 := f.rows(t, spec.Table)
	if len(round1) != 2 {
		t.Fatalf("第 1 轮审计行数 = %d, want 2", len(round1))
	}
	for i, r := range round1 {
		if r.RetentionDays == nil || *r.RetentionDays != 30 {
			t.Fatalf("第 1 轮第 %d 行 retention_days = %d, want 30", i+1, daysOf(r.RetentionDays))
		}
	}

	// 运维把 5m 档保留期从 30 天改成 10 天（v008 种下的键，运行期热更）。
	cfg.vals[configAgentHistoryRetentionDays] = 10

	stats2, err := svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("第 2 轮 Reconcile: %v", err)
	}
	if stats2.PartitionsTruncated+stats2.PartitionsDropped == 0 {
		t.Fatalf("第 2 轮 stats = %+v, want 有回收（保留期缩短后老分区必须滑出窗口）", stats2)
	}

	all := f.rows(t, spec.Table)
	if len(all)-len(round1) < stats2.PartitionsCreated {
		t.Fatalf("5m 表新增审计行 = %d, want >= %d（本轮的补缺）", len(all)-len(round1), stats2.PartitionsCreated)
	}
	// 全表流水数必须等于「第 1 轮 + 第 2 轮的成功 DDL 条数」。保留期那把键同时管 5 张表，
	// 故第 2 轮的回收不止 5m 一张表 —— 这里按全表对账，避免把别表的行算进 5m 的期望里。
	newTotal := stats2.PartitionsCreated + stats2.PartitionsTruncated + stats2.PartitionsDropped
	if got := f.rows(t, ""); len(got) != len(round1)+newTotal {
		t.Fatalf("全表审计行数 = %d, want %d（第 1 轮 %d + 第 2 轮 %d）",
			len(got), len(round1)+newTotal, len(round1), newTotal)
	}
	// ① 旧行**逐字段**不被改写。
	for i, before := range round1 {
		assertSameLogRow(t, all[i], before, fmt.Sprintf("第 2 轮后第 %d 条旧流水", i+1))
	}
	// ② 新行记的是**新**配置（10 天），旧行仍是 30。
	for i := len(round1); i < len(all); i++ {
		r := all[i]
		if r.RetentionDays == nil || *r.RetentionDays != 10 {
			t.Fatalf("第 %d 条新流水 retention_days = %d, want 10（当时配置）", i+1, daysOf(r.RetentionDays))
		}
	}
	// ③ 同一个分区名上必须能同时看到 30 与 10 两条 —— 这才是「快照」的信息量：
	//    事后去查当前配置只会得到「今天是多少」，答不出「这次回收当时是多少」。
	byName := map[string][]int32{}
	for _, r := range all {
		if r.RetentionDays != nil {
			byName[r.PartitionName] = append(byName[r.PartitionName], *r.RetentionDays)
		}
	}
	foundBoth := false
	for _, vals := range byName {
		has30, has10 := false, false
		for _, v := range vals {
			has30 = has30 || v == 30
			has10 = has10 || v == 10
		}
		if has30 && has10 {
			foundBoth = true
		}
	}
	if !foundBoth {
		t.Fatalf("没有任何分区名同时留下 30 与 10 两条流水：快照没有体现「当时」的语义（%v）", byName)
	}
}

// driftingRetentionConfig 是一个「配置在本轮执行中被改了」的 AgentConfigGetter：
// 调用 flipOnce 之后，读取保留期键一律返回 driftTo。
//
// 为什么需要它：服务读保留期只发生在「算这一轮窗口」那一刻，而审计写入发生在
// DDL 之后 —— 在**同一轮**里这两次读通常拿到同一个值，于是「按本轮快照写」与
// 「写流水那一刻现读配置」这两种实现在测试上无法区分。把配置在本轮执行中改掉，
// 差别立刻显形：正确实现（快照）写出的每一行都必须是**算窗口用的**那个值（30），
// 而「现读当前配置」的实现在下一次读时就会拿到 999。
//
// 5m 是 TableSpecs 的**第一张**表，它的那次读发生在漂移之前，故本表的快照必然是 30；
// 其余 4 张共用同一把键的表会在漂移**之后**读到 999、各自多补出一批未来分区
// （与断言无关的噪声，且只会产生 add 行）—— 所以下面只断言 5m 这个表的行。
type driftingRetentionConfig struct {
	partitionConfig
	key     string
	driftTo int
	drifted bool
}

func (c *driftingRetentionConfig) GetInt(_ context.Context, key string, def int) int {
	if key == c.key && c.drifted {
		return c.driftTo
	}
	return c.partitionConfig.GetInt(context.Background(), key, def)
}

func (c *driftingRetentionConfig) flipOnce() { c.drifted = true }

// driftTriggerAuditor 在**第一次**为受测表写流水时触发配置漂移（先漂移再落库）。
type driftTriggerAuditor struct {
	inner PartitionAuditor
	table string
	fire  func()
	fired bool
}

func (a *driftTriggerAuditor) Insert(ctx context.Context, row *entity.AgentPartitionLog) error {
	if !a.fired && row.Table == a.table {
		a.fired = true
		a.fire()
	}
	return a.inner.Insert(ctx, row)
}

// 断言 2b：审计行的 RetentionDays 必须是**本轮算窗口用的那个值**，
// 而不是「写流水那一刻再读一次配置」得到值。
func TestPartitionLogRetentionSnapshotUsesPlanTimeValue(t *testing.T) {
	f := newPartitionLogFixture(t)
	spec := specByTable(t, entity.TableNameMetric5m)
	schema := newSpySchema()
	satisfyAllTables(schema)

	desired := desiredBoundsFor(spec, partitionNow)
	business := businessBounds(desired)
	names := withoutNames(namesOfBounds(desired), business[len(business)-1].Name, business[len(business)-2].Name)
	schema.existing[spec.Table] = names

	cfg := &driftingRetentionConfig{
		partitionConfig: partitionConfig{vals: map[string]int{configAgentHistoryRetentionDays: 30}},
		key:             configAgentHistoryRetentionDays,
		driftTo:         999,
	}
	auditor := &driftTriggerAuditor{inner: f.repo, table: spec.Table, fire: cfg.flipOnce}
	svc := newPartitionSvcWithAuditor(schema, cfg, newSpyLocker(), auditor, &partitionLogSpy{})

	stats, err := svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if stats.PartitionsCreated < 2 {
		t.Fatalf("stats = %+v, want >= 2 created（夹具前置不成立）", stats)
	}
	if !auditor.fired {
		t.Fatal("夹具前置不成立：漂移没有被触发（没有为受测表写流水）")
	}
	rows := f.rows(t, spec.Table)
	if len(rows) != 2 {
		t.Fatalf("5m 表的审计行数 = %d, want 2（夹具摘掉了 2 个未来分区 → 2 条 add）", len(rows))
	}
	for i, r := range rows {
		if r.Action != entity.PartitionActionAdd {
			t.Fatalf("第 %d 行的 action = %q, want add（夹具前置不成立）", i+1, r.Action)
		}
		if r.RetentionDays == nil || *r.RetentionDays != 30 {
			t.Fatalf("第 %d 行 retention_days = %d, want 30：审计必须记**本轮算窗口用的**保留期，"+
				"而不是写流水那一刻现读的配置（999）", i+1, daysOf(r.RetentionDays))
		}
	}
}

// failingAuditor 注入审计写入失败，并记录**每次调用时已经执行了多少条 DDL**。
//
// ddlAtCall 是「两段提交」的顺序证据：第 i 次审计调用必须发生在第 i 条 DDL 之后
// （Len(execStmts) == i），否则审计就有可能与 DDL 同事务或早于 DDL。
type failingAuditor struct {
	schema    *spySchema
	failFrom  int
	calls     int
	ddlAtCall []int
}

func (a *failingAuditor) Insert(_ context.Context, _ *entity.AgentPartitionLog) error {
	a.calls++
	a.ddlAtCall = append(a.ddlAtCall, len(a.schema.execStmts))
	if a.calls >= a.failFrom {
		return errors.New("injected audit insert failure")
	}
	return nil
}

// 断言 3（计划 Step 1 第 5 项）：审计写入**不与 DDL 共事务**。
//
// 可证的属性（在 sqlite 上「分区还在不在」不可直接观测：sqlite 没有分区 DDL）：
//   - 审计写入发生在对应 DDL **之后**（两段提交的顺序）；
//   - 审计失败**不触发任何补偿动作**：下发的语句与无故障时逐字相同（不多不少、顺序一致），
//     即服务从不下发「撤销 / 重试 / 清理」；
//   - DDL 的计数照常（DDL 已生效，不因审计失败被回滚）；
//   - 失败**不得静默**：log.Error + stats.AuditFailures + 并进 ErrPartitionPartial。
//
// 「DDL 已隐式提交、回滚不了」是 DB 引擎的属性，本测试证的是**服务侧的行为**：
// 它没有任何撤销 DDL 的路径，也没有把审计失败吞掉。
func TestPartitionLogFailureDoesNotUndoDDLAndIsNotSilent(t *testing.T) {
	f := newPartitionLogFixture(t)
	schema := newSpySchema()
	spec, existing, _, _ := auditCase5m(t, schema)
	log := &partitionLogSpy{}
	auditor := &failingAuditor{schema: schema, failFrom: 1} // 每一笔审计写入都失败
	svc := newPartitionSvcWithAuditor(schema, partitionConfig{}, newSpyLocker(), auditor, log)

	stats, err := svc.Reconcile(context.Background())
	if err == nil || !errors.Is(err, ErrPartitionPartial) {
		t.Fatalf("err = %v, want ErrPartitionPartial（审计失败必须被上抛，不得静默）", err)
	}
	if stats.AuditFailures != 6 {
		t.Fatalf("AuditFailures = %d, want 6（3 条 add + 3 条 drop 的审计都失败了）", stats.AuditFailures)
	}
	// DDL 计数照常：审计失败没有回滚、也没有中断任何一条 DDL。
	if stats.PartitionsCreated != 3 || stats.PartitionsTruncated != 2 || stats.PartitionsDropped != 1 {
		t.Fatalf("stats = %+v, want 3/2/1（DDL 已生效，不因审计失败回滚）", stats)
	}
	// 没有任何补偿语句：下发的语句与无故障时**逐字相同**（独立重算一遍期望）。
	plan := agentmetrics.PlanFrom(spec, existing, partitionNow, horizonFor(spec), partitionHold)
	addStmts, aerr := agentmetrics.AddPartitionDDL(schema.Dialect(), spec, plan.Create, true)
	if aerr != nil {
		t.Fatalf("AddPartitionDDL: %v", aerr)
	}
	reclaimStmts, rerr := agentmetrics.ReclaimDDL(schema.Dialect(), spec, plan.Truncate, plan.Drop)
	if rerr != nil {
		t.Fatalf("ReclaimDDL: %v", rerr)
	}
	want := append(append([]string{}, addStmts...), reclaimStmts...)
	if len(schema.execStmts) != len(want) {
		t.Fatalf("下发了 %d 条语句, want %d（审计失败不得引出任何额外/补偿语句）",
			len(schema.execStmts), len(want))
	}
	for i := range want {
		if schema.execStmts[i] != want[i] {
			t.Fatalf("第 %d 条语句 = %q, want %q（顺序与内容都不得因审计失败而变）",
				i+1, schema.execStmts[i], want[i])
		}
	}
	// 顺序：第 i 次审计调用发生在第 i 条 DDL 之后。
	if len(auditor.ddlAtCall) != 6 {
		t.Fatalf("审计调用 %d 次, want 6（每条成功的 DDL 都要尝试留痕）", len(auditor.ddlAtCall))
	}
	for i, n := range auditor.ddlAtCall {
		if n != i+1 {
			t.Fatalf("第 %d 次审计调用时已完成 %d 条 DDL, want %d（审计必须在 DDL 之后单独提交）",
				i+1, n, i+1)
		}
	}
	// 真仓储上确实一行都没写进去（注入的是真失败，不是「假装失败」）。
	if rows := f.rows(t, spec.Table); len(rows) != 0 {
		t.Fatalf("审计失败却写进了 %d 行", len(rows))
	}
	if !log.hasError("审计流水写入失败") {
		t.Fatalf("审计失败必须 log.Error；errs = %v", log.errs)
	}
	if log.hasWarn("无法生成分区回收语句") {
		t.Fatalf("mysql 方言不该走降级路径：warns = %v", log.warns)
	}
}

// 断言 3 的另一半：**nil 审计仓储（装配漏注入）也必须响亮**。
//
// 静默跳过的症状是「分区照常维护、审计表永远是空的」——一个不会自己冒出来的
// 静默缺口，正是 §7.3 用整张审计表要消灭的东西。故按审计失败处理（而不是跳过）。
func TestPartitionLogMissingAuditorIsLoud(t *testing.T) {
	schema := newSpySchema()
	satisfyAllTables(schema)
	spec := specByTable(t, entity.TableNameMetric5m)
	desired := desiredBoundsFor(spec, partitionNow)
	business := businessBounds(desired)
	schema.existing[spec.Table] = withoutNames(namesOfBounds(desired), business[len(business)-1].Name)

	log := &partitionLogSpy{}
	svc := newPartitionSvcWithAuditor(schema, partitionConfig{}, newSpyLocker(), nil, log)

	stats, err := svc.Reconcile(context.Background())
	if err == nil || !errors.Is(err, ErrPartitionPartial) {
		t.Fatalf("err = %v, want ErrPartitionPartial（审计仓储缺失是装配错误，不得静默）", err)
	}
	if stats.AuditFailures != 1 {
		t.Fatalf("AuditFailures = %d, want 1", stats.AuditFailures)
	}
	if stats.PartitionsCreated != 1 {
		t.Fatalf("stats = %+v, want 1 created（DDL 照常执行：审计缺失不该拖住分区维护）", stats)
	}
	if !log.hasError("审计仓储未注入") {
		t.Fatalf("必须 log.Error 指明装配问题；errs = %v", log.errs)
	}
}

// 断言 4（计划 Step 3 第 4 项 / spec §7.3 降级路径）：降级方言（sqlite）上
// ReclaimDDL 返回错误 → **没有**任何实际回收发生 → 不得写 `action=drop` 审计行。
//
// 为什么这条断言的方向不能反：审计表是缺口归因的**唯一**依据。写一条 drop 就是
// 声称「这个分区的数据在此时被清空了」，而事实是一行都没动 —— 归因会把
// 「数据仍在」误判成「数据已丢」，比少一条留痕严重得多。
func TestPartitionLogNoDropRowsOnDegradedDialect(t *testing.T) {
	f := newSQLiteSchemaFixture(t)
	if err := f.db.AutoMigrate(&entity.AgentPartitionLog{}); err != nil {
		t.Fatalf("AutoMigrate(审计表): %v", err)
	}
	auditRepo := repository.NewAgentPartitionLogRepository(f.db)

	spec := specByTable(t, entity.TableNameMetric5m)
	old := businessNamesInPast(spec, 1)
	if len(old) != 1 {
		t.Fatalf("夹具造出的过期分区 = %v, want 1", old)
	}
	// 现状：守卫 + 一个过期业务分区 + 哨兵 → PlanFrom 给出 Truncate=[那个过期分区]，
	// 于是 ReclaimDDL("sqlite", …) 必定返回错误（拒绝生成无界 DELETE）。
	schema := &expiredSchema{
		PartitionSchemaRepo: f.repo,
		table:               spec.Table,
		existing:            []string{agentmetrics.GuardPartitionName, old[0], agentmetrics.SentinelPartitionName},
	}
	log := &partitionLogSpy{}
	svc := newPartitionSvcWithAuditor(schema, partitionConfig{}, newSpyLocker(), auditRepo, log)

	stats, err := svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile = %v, want nil（降级方言「无法回收」是常态，不得上抛）", err)
	}
	if stats.PartitionsTruncated != 0 || stats.PartitionsDropped != 0 || stats.PartitionsCreated != 0 {
		t.Fatalf("stats = %+v, want 三段全 0（sqlite 上回收被拒绝、建分区被降级）", stats)
	}
	if stats.AuditFailures != 0 {
		t.Fatalf("AuditFailures = %d, want 0（本轮没有审计写入失败）", stats.AuditFailures)
	}
	if rows := f.rowsAll(t); len(rows) != 0 {
		t.Fatalf("降级方言下没有任何回收发生，却写入了 %d 条审计流水：%+v", len(rows), rows)
	}
	if !log.hasWarn("无法生成分区回收语句") {
		t.Fatalf("回收被拒绝必须 log.Warn；warns = %v", log.warns)
	}
	// 这 0 行不是「审计没接上」：同一份审计仓储在 mysql 方言夹具下写出了 3 条 add + 3 条 drop
	// （TestPartitionLogWritesAddAndDropRowsWithPlanBounds），且本轮 err == nil、AuditFailures == 0。
}

// rowsAll 读回审计表的全部流水（table 为空 = 不过滤）。
func (f *sqliteSchemaFixture) rowsAll(t *testing.T) []entity.AgentPartitionLog {
	t.Helper()
	rows, err := repository.NewAgentPartitionLogRepository(f.db).ListByTable(context.Background(), "", 0)
	if err != nil {
		t.Fatalf("读审计流水: %v", err)
	}
	return rows
}

// 断言 5：回收 DDL **执行失败**时同样不得写 drop 行 —— 那条语句没有生效，
// 没有回收发生（与降级方言同一个方向）。
func TestPartitionLogNoDropRowWhenReclaimDDLFails(t *testing.T) {
	f := newPartitionLogFixture(t)
	spec := specByTable(t, entity.TableNameMetric5m)
	past := pastBounds(spec, partitionNow, 3)
	schema := newSpySchema()
	satisfyAllTables(schema)
	schema.existing[spec.Table] = append(namesOfBounds(desiredBoundsFor(spec, partitionNow)), namesOfBounds(past)...)
	schema.execErrOn = 1 // 第 1 条回收语句（TRUNCATE）失败

	log := &partitionLogSpy{}
	svc := newPartitionSvcWithAuditor(schema, partitionConfig{}, newSpyLocker(), f.repo, log)

	stats, err := svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile = %v, want nil（回收失败只记日志、不中断本轮）", err)
	}
	if stats.PartitionsTruncated != 0 || stats.PartitionsDropped != 0 || stats.PartitionsCreated != 0 {
		t.Fatalf("stats = %+v, want 三段全 0（第 1 条回收就失败了）", stats)
	}
	if rows := f.rows(t, spec.Table); len(rows) != 0 {
		t.Fatalf("回收 DDL 失败却写了 %d 条 drop 流水（该分区并没有被清空）", len(rows))
	}
	if !log.hasWarn("回收 DDL 执行失败") {
		t.Fatalf("回收 DDL 失败必须 log.Warn；warns = %v", log.warns)
	}
}

// 断言 6（计划 Step 1 第 6 项）：审计表**不参与任何回收/建分区**，也不被当成指标表。
//
// 三个方向：
//  1. 表集合：协调器的表清单（agentmetrics.TableSpecs）与分区建表钩子的表清单
//     （repository.MetricTables）都不含审计表 —— 这是「不分区」的结构性保证
//     （审计表若是分区表，它自己就会缺分区并触发 1526）；
//  2. 行为：让 6 张表**全都**有改动，下发的每一条 DDL 都不得提到审计表；
//  3. 记账：审计流水只记 6 张指标表，且 6 张都出现过。
func TestPartitionLogTableIsNotReconciled(t *testing.T) {
	auditTable := entity.TableNameAgentPartitionLog
	for _, s := range agentmetrics.TableSpecs() {
		if s.Table == auditTable {
			t.Fatalf("%s 出现在 TableSpecs 里：协调器会去回收/补建审计表自己的分区", auditTable)
		}
	}
	for _, n := range repository.MetricTables() {
		if n == auditTable {
			t.Fatalf("%s 出现在 MetricTables 里：分区建表钩子会把它建成分区表（spec 明文「不分区」）", auditTable)
		}
	}

	f := newPartitionLogFixture(t)
	schema := newSpySchema() // 现状全空 → 6 张表全都有补缺（每张都会下发 DDL 与流水）
	svc := newPartitionSvcWithAuditor(schema, partitionConfig{}, newSpyLocker(), f.repo, &partitionLogSpy{})

	stats, err := svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if stats.PartitionsCreated == 0 || len(schema.execStmts) == 0 {
		t.Fatal("夹具前置不成立：本轮没有下发任何 DDL")
	}
	for _, stmt := range schema.execStmts {
		if strings.Contains(stmt, auditTable) {
			t.Fatalf("语句 %q 指向审计表：审计表不参与回收/建分区", stmt)
		}
	}
	rows := f.rows(t, "")
	if len(rows) == 0 {
		t.Fatal("夹具前置不成立：本轮没有写出任何审计流水")
	}
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.Table] = true
		if r.Table == auditTable {
			t.Fatalf("审计流水把审计表当成指标表维护了：%+v", r)
		}
	}
	if len(seen) != len(agentmetrics.TableSpecs()) {
		t.Fatalf("审计流水覆盖了 %d 张表, want %d（%v）", len(seen), len(agentmetrics.TableSpecs()), seen)
	}
	for _, s := range agentmetrics.TableSpecs() {
		if !seen[s.Table] {
			t.Fatalf("%s 本轮有改动却没有审计流水", s.Table)
		}
	}
}

// 早退路径不得吞掉审计失败：本表先有「补缺 DDL 成功但审计写失败」，随后回收 DDL 又失败
// —— 两条失败必须同时出现在返回的 error 里（早退只有一次返回机会，丢掉一条就是静默缺口）。
func TestPartitionLogReclaimFailureStillSurfacesEarlierAuditFailures(t *testing.T) {
	f := newPartitionLogFixture(t)
	schema := newSpySchema()
	spec, _, _, _ := auditCase5m(t, schema)
	schema.execErrOn = 4 // 前 3 条是补缺 DDL，第 4 条是第一条回收 DDL（TRUNCATE）
	log := &partitionLogSpy{}
	auditor := &failingAuditor{schema: schema, failFrom: 1}
	svc := newPartitionSvcWithAuditor(schema, partitionConfig{}, newSpyLocker(), auditor, log)

	stats, err := svc.Reconcile(context.Background())
	if err == nil || !errors.Is(err, ErrPartitionPartial) {
		t.Fatalf("err = %v, want ErrPartitionPartial", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "回收 DDL 执行失败") {
		t.Fatalf("err = %v, want 含回收失败（早退的那件事）", err)
	}
	if !strings.Contains(msg, "审计流水写入失败") {
		t.Fatalf("err = %v, want 含审计写入失败（早退不得把它吞掉）", err)
	}
	if stats.PartitionsCreated != 3 || stats.PartitionsTruncated != 0 || stats.PartitionsDropped != 0 {
		t.Fatalf("stats = %+v, want 3 created / 0 回收（第 4 条语句失败）", stats)
	}
	if stats.AuditFailures != 3 {
		t.Fatalf("AuditFailures = %d, want 3（3 条补缺的审计都失败了）", stats.AuditFailures)
	}
	if rows := f.rows(t, ""); len(rows) != 0 {
		t.Fatalf("审计全部失败却写进了 %d 行", len(rows))
	}
	if !log.hasError("审计流水写入失败") || !log.hasWarn("回收 DDL 执行失败") {
		t.Fatalf("两条失败都必须出现在日志里：errs=%v warns=%v", log.errs, log.warns)
	}
	if spec.Table != entity.TableNameMetric5m {
		t.Fatalf("夹具前置不成立：受测表 = %s", spec.Table)
	}
}
