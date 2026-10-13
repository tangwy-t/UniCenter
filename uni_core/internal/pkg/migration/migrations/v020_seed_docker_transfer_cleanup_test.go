package migrations

import (
	"strings"
	"testing"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/migration"
	"github.com/tangwy-t/UniCenter/uni_core/internal/task"
	"github.com/tangwy-t/UniCenter/uni_core/internal/task/tasks"
)

// 本文件守卫 v020 的三条硬约束：注册、名字查得到、行唯一稳定。
//
// 它们都属于「破了不报错、只表现为任务静默不执行或重复执行」的那一类 ——
// 编译、vet 与其余测试全绿，只有这几条断言能拦住。

func TestV020IsRegistered(t *testing.T) {
	found := false
	for _, m := range migration.All() {
		if m.Version == 20 {
			found = true
			if m.Up == nil {
				t.Fatal("v020 的 Up 不能为空")
			}
		}
	}
	if !found {
		t.Fatal("v020 未注册（新增迁移必须在 init() 中 migration.Register）")
	}
}

// TestV020InvokeTargetIsRegistered：种子名 ↔ 真实任务名的双向守卫（照 v009 的口径）。
//
// 调度器按 invoke_target 去 task.Registry 精确查找（scheduler/executor.go），
// 查不到就只写一条 job_log 错误、任务静默不执行 —— 而这条任务不执行的现象是
// 「agent 的盘被上传产物慢慢占满」，几周之后才会被注意到。
func TestV020InvokeTargetIsRegistered(t *testing.T) {
	const want = "docker-transfer-cleanup"
	seeded := map[string]bool{}
	for _, j := range dockerTransferCleanupJobDefinitions {
		seeded[j.Invoke] = true
	}
	if !seeded[want] {
		t.Fatalf("v020 必须种下 %s 这一条 invoke_target", want)
	}
	registered := false
	for _, tk := range tasks.All(tasks.Deps{}) {
		if tk.Name() != want {
			continue
		}
		registered = true
		if tk.DisplayName() == "" {
			t.Fatal("任务展示名不能为空（任务管理页显示它）")
		}
	}
	if !registered {
		t.Fatalf("task/tasks 里没有 %s 任务 —— v020 种下的目标查不到，回收永远不会跑", want)
	}
	// 再用真实注册表走一遍调度器的查找路径：名字对得上还不等于「查得到」。
	reg := task.NewRegistry(tasks.All(tasks.Deps{})...)
	for _, j := range dockerTransferCleanupJobDefinitions {
		if _, ok := reg.Get(j.Invoke); !ok {
			t.Fatalf("调度器按 invoke_target %q 查 task.Registry 落空 —— 该任务永远不会被执行", j.Invoke)
		}
	}
	// 任务表的展示名与任务的 DisplayName 逐字一致：任务列表上两个地方显示同一个任务，
	// 口径分岔会让人以为是两条不同的任务。
	for _, tk := range tasks.All(tasks.Deps{}) {
		if tk.Name() == want && tk.DisplayName() != dockerTransferCleanupJobDefinitions[0].Name {
			t.Fatalf("种子的展示名 %q 与任务的 DisplayName %q 不一致",
				dockerTransferCleanupJobDefinitions[0].Name, tk.DisplayName())
		}
	}
}

// TestV020SeedIsSingleAndStable：恰一行、字段口径、幂等且**不覆写运维改过的行**。
//
// 「唯一稳定」在这里有两层意思，各由一条断言钉住：
//   - 同一份种子跑两遍不得产生第二行（去重按 invoke_target，且不删旧行再插新行）；
//   - 库里已存在同名任务时**跳过**（运维调过的 cron 不得被打回默认值）。
func TestV020SeedIsSingleAndStable(t *testing.T) {
	if len(dockerTransferCleanupJobDefinitions) != 1 {
		t.Fatalf("本迁移应恰有 1 个任务定义，实得 %d", len(dockerTransferCleanupJobDefinitions))
	}
	def := dockerTransferCleanupJobDefinitions[0]
	if def.Invoke == "" || strings.TrimSpace(def.Invoke) != def.Invoke {
		t.Fatalf("invoke_target %q 不能有空白（调度器按精确字符串查找）", def.Invoke)
	}
	// cron 逐字钉住：它是「什么时候跑」的唯一事实源，**每天 04:30** —— 落在
	// 03:00（三类日志清理 + 字典）、03:30（任务历史清理）、04:00（分区对账 DDL）
	// 那一串之后，不参与整点堆（理由写在迁移文件的注释里）。
	if def.Cron != "0 30 4 * * *" {
		t.Fatalf("%s 的 cron = %q, want %q（每天 04:30，避开既有的整点堆）", def.Invoke, def.Cron, "0 30 4 * * *")
	}
	if def.Name == "" {
		t.Fatal("展示名不能为空")
	}

	db := newAgentSeedTestDB(t)
	if err := seedDockerTransferCleanup(db); err != nil {
		t.Fatalf("首次 seedDockerTransferCleanup: %v", err)
	}
	first := readJobs(t, db)
	if len(first) != len(dockerTransferCleanupJobDefinitions) {
		t.Fatalf("首次种子写入了 %d 条任务, want %d", len(first), len(dockerTransferCleanupJobDefinitions))
	}
	row, ok := first[def.Invoke]
	if !ok {
		t.Fatalf("首次种子后 %s 不在库里", def.Invoke)
	}
	// 落库的那一行必须与种子逐字一致，且是「启用 + 不允许重入 + 不开机跑」形态：
	// 重入会让上一轮还在收结果时就再派发一轮（同一批主机重复下发）。
	if row.Name != def.Name || row.CronExpression != def.Cron || row.JobGroup != "system" {
		t.Fatalf("落库行与种子不一致: %+v", row)
	}
	if row.Concurrent == nil || *row.Concurrent != entity.JobConcurrentDisallowed {
		t.Fatalf("不允许重入（上一轮还没跑完就再来一轮会重复派发），实得 %v", row.Concurrent)
	}
	if row.Status == nil || *row.Status != entity.JobStatusEnabled {
		t.Fatalf("种子应默认启用，实得 %v", row.Status)
	}
	if row.RunAtStartup == nil || *row.RunAtStartup != entity.JobRunAtStartupNo {
		t.Fatalf("不开机立即跑（回收是对昨日产物的兜底，没有「启动就必须立刻做」的语义），实得 %v", row.RunAtStartup)
	}
	assertNoDuplicateTargets(t, db)

	if err := seedDockerTransferCleanup(db); err != nil {
		t.Fatalf("二次 seedDockerTransferCleanup: %v", err)
	}
	again := readJobs(t, db)
	if len(again) != len(first) {
		t.Fatalf("二次种子后任务数 = %d, want %d（按 invoke_target 去重）", len(again), len(first))
	}
	if again[def.Invoke].ID != row.ID {
		t.Fatalf("%s 的 id 变了（%d → %d）：重跑种子不得删旧行再插新行",
			def.Invoke, row.ID, again[def.Invoke].ID)
	}
	assertNoDuplicateTargets(t, db)
}

// 真实库里可能**已经有**同名 invoke_target 的行（人工建的、或本迁移被手工重跑过）：
// 此时种子必须跳过而不是覆写 —— 覆写会把运维调过的 cron 打回默认值，且这种回退
// 完全不可观测（今天把回收挪到别处，明天启动就被无声改回去）。
func TestV020SeedPreservesExistingJobRow(t *testing.T) {
	db := newAgentSeedTestDB(t)
	custom := entity.SysJob{
		Name:           "运维自建：产物回收",
		JobGroup:       "system",
		CronExpression: "0 0 5 * * *",
		InvokeTarget:   dockerTransferCleanupJobDefinitions[0].Invoke,
	}
	if err := db.Create(&custom).Error; err != nil {
		t.Fatalf("预置同名任务: %v", err)
	}
	if err := seedDockerTransferCleanup(db); err != nil {
		t.Fatalf("seedDockerTransferCleanup: %v", err)
	}
	rows := readJobs(t, db)
	got, ok := rows[custom.InvokeTarget]
	if !ok {
		t.Fatal("已存在的同名任务被删掉了")
	}
	if got.ID != custom.ID || got.CronExpression != custom.CronExpression || got.Name != custom.Name {
		t.Fatalf("种子覆写了已存在的行: %+v", got)
	}
	if len(rows) != 1 {
		t.Fatalf("同名任务只应有一行，实得 %d 行", len(rows))
	}
}
