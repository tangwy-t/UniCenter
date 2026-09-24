package migrations

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/database"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/migration"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/snowflake"
	"github.com/tangwy-t/UniCenter/uni_core/internal/task"
	"github.com/tangwy-t/UniCenter/uni_core/internal/task/tasks"
)

// agentMetricPrefix 是设备指标任务 **invoke_target 的公共前缀**。
//
// 用前缀而不是在断言里列 4 遍字面量：本守卫要回答的问题是
// 「种子里这 4 条任务，与 task/tasks 里真实注册的 4 个同名任务，是不是同一组」，
// 而两边的名字**都必须来自各自的生产源**（种子定义 / 任务实例）。
const agentMetricPrefix = "agent-metrics-"

func TestV009IsRegistered(t *testing.T) {
	found := false
	for _, m := range migration.All() {
		if m.Version == 9 {
			found = true
			if m.Up == nil {
				t.Fatal("v009 的 Up 不能为空")
			}
		}
	}
	if !found {
		t.Fatal("v009 未注册（新增迁移必须在 init() 中 migration.Register）")
	}
}

// ─ Step 1.2：任务名 ↔ invoke_target 双向集合相等 ─────────
//
// 这是**唯一**能拦住「任务改名 / 种子改名」脱节的断言：
// 调度器按 invoke_target 去 task.Registry 找目标（scheduler/executor.go:100），
// 找不到就只写一条 job_log 错误、任务静默不执行 —— 指标链路整段断掉，
// 而编译、go vet、其余测试全绿。
func TestV009InvokeTargetsMatchTaskNames(t *testing.T) {
	seeded := map[string]bool{}
	for _, j := range agentJobDefinitions {
		seeded[j.Invoke] = true
	}
	registered := map[string]bool{}
	for _, tk := range tasks.All(tasks.Deps{}) {
		if strings.HasPrefix(tk.Name(), agentMetricPrefix) {
			registered[tk.Name()] = true
		}
	}

	for _, name := range sortedKeys(registered) {
		if !seeded[name] {
			t.Fatalf("任务 %q 已注册但 v009 种子里没有对应 invoke_target —— 这个任务永远不会被调度", name)
		}
	}
	for _, target := range sortedKeys(seeded) {
		if !registered[target] {
			t.Fatalf("v009 种子的 invoke_target %q 在 task/tasks 里没有同名任务 —— 调度器找不到目标，任务静默不执行", target)
		}
	}
	if len(seeded) != 4 || len(registered) != 4 {
		t.Fatalf("设备指标任务数 = 种子 %d / 任务 %d, want 4/4", len(seeded), len(registered))
	}

	// 再用**真实注册表**走一遍调度器的查找路径（scheduler/executor.go:100 的
	// registry.Get(job.InvokeTarget)）：集合相等还不等于「查得到」，
	// 注册表是 map、查找走的是精确字符串，这一步把「名字对不对」变成
	// 「任务能不能被执行」。
	reg := task.NewRegistry(tasks.All(tasks.Deps{})...)
	for _, j := range agentJobDefinitions {
		if _, ok := reg.Get(j.Invoke); !ok {
			t.Fatalf("调度器按 invoke_target %q 查 task.Registry 落空 —— 该任务永远不会被执行", j.Invoke)
		}
	}
}

// ── 种子的其它契约 ──────────────────────────────────────

// cron 与「是否开机跑」都按计划逐字钉住：flush 与 rollup 错开半分钟是为了
// 两者不抢同一批 5m 桶的 DB 连接（flush 写事务 A、rollup 写事务 B）。
func TestV009JobDefinitionsContract(t *testing.T) {
	want := map[string]struct {
		cron    string
		params  string
		startup bool
	}{
		"agent-metrics-flush":     {cron: "0 */5 * * * *"},
		"agent-metrics-rollup":    {cron: "30 */5 * * * *"},
		"agent-metrics-backfill":  {cron: "0 15 * * * *", params: `{"hours":24}`},
		"agent-metrics-partition": {cron: "0 0 4 * * *"},
	}
	if len(agentJobDefinitions) != len(want) {
		t.Fatalf("种子任务数 = %d, want %d", len(agentJobDefinitions), len(want))
	}
	for _, j := range agentJobDefinitions {
		w, ok := want[j.Invoke]
		if !ok {
			t.Fatalf("种子里的 invoke_target %q 不在计划约定的 4 个任务里", j.Invoke)
		}
		if j.Cron != w.cron {
			t.Fatalf("%s 的 cron = %q, want %q", j.Invoke, j.Cron, w.cron)
		}
		if j.Params != w.params {
			t.Fatalf("%s 的 params = %q, want %q", j.Invoke, j.Params, w.params)
		}
		if j.Startup != w.startup {
			t.Fatalf("%s 的 Startup = %v, want %v（计划未要求开机跑；启动期对齐由 wireup 的 bootstrap 负责）",
				j.Invoke, j.Startup, w.startup)
		}
		if j.Name == "" {
			t.Fatalf("%s 的展示名不能为空", j.Invoke)
		}
		if strings.TrimSpace(j.Invoke) != j.Invoke || j.Invoke == "" {
			t.Fatalf("invoke_target %q 不能有空白（调度器按精确字符串查找）", j.Invoke)
		}
	}

	// backfill 的 params 必须是**合法 JSON** 且能解析出 hours：
	// 它是「显式回溯窗口」的唯一入口，一个畸形的 invoke_params 会让调度器
	// 每轮都拿 json.RawMessage 原文去执行（executor.go:136 不做校验）。
	for _, j := range agentJobDefinitions {
		if j.Params == "" {
			continue
		}
		var p struct {
			Hours *int `json:"hours"`
		}
		if err := json.Unmarshal([]byte(j.Params), &p); err != nil {
			t.Fatalf("%s 的 params 不是合法 JSON: %v", j.Invoke, err)
		}
		if p.Hours == nil || *p.Hours != 24 {
			t.Fatalf("%s 的 params.hours = %v, want 24（raw 点只保留 24h，更长的窗口全是空桶）", j.Invoke, p.Hours)
		}
	}
}

// 新增种子绝不能复用既有任务名：v007 已种下 5 个系统任务，若 v009 用同一个
// invoke_target，会变成同一个任务被两个 job 行各调度一次（重复执行）。
func TestV009InvokeTargetsDoNotCollideWithExistingSeeds(t *testing.T) {
	existing := map[string]bool{}
	for _, j := range jobDefinitions {
		existing[j.Invoke] = true
	}
	for _, j := range agentJobDefinitions {
		if existing[j.Invoke] {
			t.Fatalf("invoke_target %q 与 v007 已种下的任务重名 —— 同一个任务会被调度两次", j.Invoke)
		}
	}
}

// ─ Step 1.5：v009 幂等（真实库可能已有同名任务）────────

func TestV009SeedIsIdempotent(t *testing.T) {
	db := newAgentSeedTestDB(t)

	if err := seedAgentJobs(db); err != nil {
		t.Fatalf("首次 seedAgentJobs: %v", err)
	}
	first := readJobs(t, db)
	if len(first) != len(agentJobDefinitions) {
		t.Fatalf("首次种子写入了 %d 条任务, want %d", len(first), len(agentJobDefinitions))
	}
	assertNoDuplicateTargets(t, db)

	if err := seedAgentJobs(db); err != nil {
		t.Fatalf("二次 seedAgentJobs: %v", err)
	}
	second := readJobs(t, db)
	if len(second) != len(first) {
		t.Fatalf("二次种子后任务数 = %d, want %d（按 invoke_target 去重，不得重复插入）",
			len(second), len(first))
	}
	for target, job := range first {
		again, ok := second[target]
		if !ok {
			t.Fatalf("二次种子后 %s 消失了", target)
		}
		if again.ID != job.ID {
			t.Fatalf("%s 的 id 变了（%d → %d）：重跑种子不得删旧行再插新行", target, job.ID, again.ID)
		}
		if again.CronExpression != job.CronExpression {
			t.Fatalf("%s 的 cron 被重写（%q → %q）", target, job.CronExpression, again.CronExpression)
		}
	}
	assertNoDuplicateTargets(t, db)

	// 配置键同样幂等（真实库里这两个键可能已被手工补过）。
	var configs int64
	if err := db.Model(&entity.SysConfig{}).
		Where("config_key IN ?", agentJobConfigKeys()).Count(&configs).Error; err != nil {
		t.Fatalf("count configs: %v", err)
	}
	if configs != int64(len(agentJobConfigDefinitions)) {
		t.Fatalf("配置键条数 = %d, want %d", configs, len(agentJobConfigDefinitions))
	}
}

// 真实库里可能**已经有**同名 invoke_target 的任务行（人工建的、或 v009 曾被
// 手工重跑过）。此时种子必须**跳过**而不是覆写：覆写会把运维调过的 cron 打回默认值，
// 且这种回退完全不可观测（下次谁把凌晨 4 点的分区维护改成业务高峰，就会被无声改回去）。
func TestV009SeedPreservesExistingJobRow(t *testing.T) {
	db := newAgentSeedTestDB(t)

	custom := entity.SysJob{
		Name:           "运维自建：指标落库",
		JobGroup:       "system",
		CronExpression: "0 0 1 * * *",
		InvokeTarget:   "agent-metrics-flush",
	}
	if err := db.Create(&custom).Error; err != nil {
		t.Fatalf("预置同目标任务: %v", err)
	}

	if err := seedAgentJobs(db); err != nil {
		t.Fatalf("seedAgentJobs: %v", err)
	}
	got := readJobs(t, db)
	if len(got) != len(agentJobDefinitions) {
		t.Fatalf("任务数 = %d, want %d（已存在的目标不重复插入）", len(got), len(agentJobDefinitions))
	}
	if row := got["agent-metrics-flush"]; row.ID != custom.ID || row.CronExpression != "0 0 1 * * *" {
		t.Fatalf("已存在的同名任务被覆写了（id=%d cron=%q），want（id=%d cron=0 0 1 * * *）",
			row.ID, row.CronExpression, custom.ID)
	}
	for _, j := range agentJobDefinitions {
		if _, ok := got[j.Invoke]; !ok {
			t.Fatalf("缺 %s：种子必须补齐「还没有的」任务，而不是因为有一条同名就整批跳过", j.Invoke)
		}
	}
}

func TestV009ConfigDefinitionsContract(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range agentJobConfigDefinitions {
		if seen[c.Key] {
			t.Fatalf("配置键重复：%s", c.Key)
		}
		seen[c.Key] = true
		if !strings.HasPrefix(c.Key, "sys.agent.") {
			t.Fatalf("配置键 %q 必须使用 sys.agent. 前缀", c.Key)
		}
		if c.Type != "N" {
			t.Fatalf("配置键 %q 的 Type = %q, want \"N\"（两者都是秒数）", c.Key, c.Type)
		}
		if c.Name == "" || c.Remark == "" {
			t.Fatalf("配置键 %q 的名称/说明不能为空（配置页直接展示）", c.Key)
		}
		if !c.Enabled {
			t.Fatalf("配置键 %q 必须落库为启用：分区协调器每轮都读它", c.Key)
		}
	}

	// 值与 service/agent_metrics_partition.go 的两个默认常量必须一致：
	// 种子写 600/60，服务侧 defaultPartitionLockTTLSec/defaultPartitionTableTimeoutSec
	// 也是 600/60。（那两处是未导出常量，故此处按字面量钉住 + 注释交叉指向。）
	want := map[string]string{
		"sys.agent.partitionLockTTL":      "600",
		"sys.agent.partitionTableTimeout": "60",
	}
	if len(agentJobConfigDefinitions) != len(want) {
		t.Fatalf("配置键条数 = %d, want %d", len(agentJobConfigDefinitions), len(want))
	}
	for _, c := range agentJobConfigDefinitions {
		if w, ok := want[c.Key]; !ok || c.Value != w {
			t.Fatalf("配置键 %q 的默认值 = %q, want %q", c.Key, c.Value, w)
		}
	}
}

// ─ 端到端：走真实 runner ───────────────────────────────

// 上面的幂等测试直接调 seedAgentJobs（在测试库里裸跑两次）。本测试补上
// **真实执行路径**：migration.Run 会把 Up 包在事务里，并按 sys_migration 的
// 版本账本决定跑不跑。两者都要成立 —— 只测前者的话，「迁移在 runner 里
// 根本跑不起来」（缺表、与 pre-migrate 钩子顺序冲突等）不会被发现。
func TestV009RunsThroughMigrationRunner(t *testing.T) {
	db := newAgentSeedTestDB(t)

	if err := migration.Run(db, logger.NewNop()); err != nil {
		t.Fatalf("migration.Run: %v", err)
	}
	var seeded int64
	if err := db.Model(&entity.SysJob{}).
		Where("invoke_target LIKE ?", agentMetricPrefix+"%").Count(&seeded).Error; err != nil {
		t.Fatalf("count v009 jobs: %v", err)
	}
	if seeded != int64(len(agentJobDefinitions)) {
		t.Fatalf("runner 跑完后设备指标任务数 = %d, want %d", seeded, len(agentJobDefinitions))
	}

	var total int64
	if err := db.Model(&entity.SysJob{}).Count(&total).Error; err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	// 总数 = 各迁移种子的任务之和。**这是个全量账本**：每新增一条任务种子迁移，
	// 这里就要 +1（v014 的升级巡检就是最近一次）—— 它守的是「没有意外多出来的任务」，
	// 而不是某一批的数量。
	wantTotal := int64(len(jobDefinitions) + len(agentJobDefinitions) +
		len(agentUpgradePatrolJobDefinitions)) // v007 的 5 + v009 的 4 + v014 的 1
	if total != wantTotal {
		t.Fatalf("sys_job 总数 = %d, want %d（v007 的 5 个 + v009 的 4 个 + v014 的 1 个）", total, wantTotal)
	}

	// 再跑一次 runner：版本账本让 v009 不再执行，数量必须一动不动
	// （这条断言同时证明重跑不会因为去重逻辑写错而丢行或翻倍）。
	if err := migration.Run(db, logger.NewNop()); err != nil {
		t.Fatalf("二次 migration.Run: %v", err)
	}
	var again int64
	if err := db.Model(&entity.SysJob{}).Count(&again).Error; err != nil {
		t.Fatalf("count jobs again: %v", err)
	}
	if again != total {
		t.Fatalf("二次 runner 后 sys_job 总数 = %d, want %d", again, total)
	}
}

// ── 夹具 ────────────────────────────────────────────────

// newAgentSeedTestDB 起一个内存 sqlite 并注册生产同款雪花回调。
//
// 雪花回调不能省：SysJob.ID 是 `primaryKey;autoIncrement:false`，
// 没有 id:generate 回调时每行 id 都是 0，第二行就撞主键（实测）。
func newAgentSeedTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	node, err := snowflake.New(1, logger.NewNop())
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	database.NewCallbacks(node).Register(db)
	if err := db.AutoMigrate(&entity.SysJob{}, &entity.SysConfig{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	return db
}

// readJobs 按 invoke_target 读回全部任务行。
func readJobs(t *testing.T, db *gorm.DB) map[string]entity.SysJob {
	t.Helper()
	var rows []entity.SysJob
	if err := db.Find(&rows).Error; err != nil {
		t.Fatalf("find jobs: %v", err)
	}
	out := make(map[string]entity.SysJob, len(rows))
	for _, r := range rows {
		if prev, dup := out[r.InvokeTarget]; dup {
			t.Fatalf("invoke_target %q 出现重复行（id=%d 与 id=%d）", r.InvokeTarget, prev.ID, r.ID)
		}
		out[r.InvokeTarget] = r
	}
	return out
}

// assertNoDuplicateTargets 用 SQL 的 GROUP BY 独立复核一次「按 invoke_target 唯一」，
// 不依赖上面那一层 map（map 只是把重复折叠掉了而已）。
func assertNoDuplicateTargets(t *testing.T, db *gorm.DB) {
	t.Helper()
	var dup []string
	if err := db.Model(&entity.SysJob{}).
		Select("invoke_target").
		Group("invoke_target").
		Having("COUNT(*) > 1").
		Pluck("invoke_target", &dup).Error; err != nil {
		t.Fatalf("group by invoke_target: %v", err)
	}
	if len(dup) > 0 {
		t.Fatalf("sys_job 里出现重复的 invoke_target：%v", dup)
	}
}

func agentJobConfigKeys() []string {
	out := make([]string, 0, len(agentJobConfigDefinitions))
	for _, c := range agentJobConfigDefinitions {
		out = append(out, c.Key)
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
