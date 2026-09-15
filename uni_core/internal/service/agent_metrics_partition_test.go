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
func newPartitionSvc(schema PartitionSchemaRepo, cfg AgentConfigGetter, locker PartitionLocker,
	log logger.LoggerInterface) *AgentMetricsPartitionService {

	return NewAgentMetricsPartitionService(schema, cfg, locker, log).WithClock(func() time.Time {
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
