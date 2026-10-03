package migrations

import (
	"testing"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/migration"
	"github.com/tangwy-t/UniCenter/uni_core/internal/task"
	"github.com/tangwy-t/UniCenter/uni_core/internal/task/tasks"
)

func TestV018IsRegistered(t *testing.T) {
	found := false
	for _, m := range migration.All() {
		if m.Version == 18 {
			found = true
			if m.Up == nil {
				t.Fatal("v018 的 Up 不能为空")
			}
		}
	}
	if !found {
		t.Fatal("v018 未注册（新增迁移必须在 init() 中 migration.Register）")
	}
}

// TestV018InvokeTargetsMatchTaskNames 是任务名 ↔ invoke_target 的双向守卫：
// 调度器按 invoke_target 去 task.Registry 精确查找（scheduler/executor.go），
// 名字脱节会让任务**静默不执行** —— 编译、vet、其余测试全绿，只有这条守卫能拦住。
func TestV018InvokeTargetsMatchTaskNames(t *testing.T) {
	seeded := map[string]bool{}
	for _, j := range dockerTaskJobDefinitions {
		seeded[j.Invoke] = true
	}
	const cleanupName = "docker-task-cleanup"
	if !seeded[cleanupName] {
		t.Fatalf("v018 必须种下 %s 这一条 invoke_target", cleanupName)
	}
	registered := map[string]bool{}
	for _, tk := range tasks.All(tasks.Deps{}) {
		if tk.Name() == cleanupName {
			registered[cleanupName] = true
			if tk.DisplayName() == "" {
				t.Fatal("任务展示名不能为空（任务管理页显示它）")
			}
		}
	}
	if !registered[cleanupName] {
		t.Fatalf("task/tasks 里没有 %s 任务 —— v018 种下的目标查不到，清理永远不会跑", cleanupName)
	}
	// 再用真实注册表走一遍调度器的查找路径：集合相等还不等于「查得到」。
	reg := task.NewRegistry(tasks.All(tasks.Deps{})...)
	for _, j := range dockerTaskJobDefinitions {
		if _, ok := reg.Get(j.Invoke); !ok {
			t.Fatalf("调度器按 invoke_target %q 查 task.Registry 落空 —— 该任务永远不会被执行", j.Invoke)
		}
	}
}

// TestV018RetentionDefaultMatchesTask 交叉钉住缺省保留天数：种子里的值（配置页显示的
// 初始值）与任务侧的未导出常量必须同值，否则「配置页写着 30、实际缺省是别的」会
// 成为一个只有运维才能发现的静默偏差（与 v009 的分区配置同一手法：按字面量交叉钉）。
func TestV018RetentionDefaultMatchesTask(t *testing.T) {
	if len(dockerTaskConfigDefinitions) != 1 {
		t.Fatalf("v018 只该种一个配置键, got %d", len(dockerTaskConfigDefinitions))
	}
	def := dockerTaskConfigDefinitions[0]
	if def.Key != "sys.docker.taskRetentionDays" {
		t.Fatalf("配置键名 = %q, want sys.docker.taskRetentionDays（清理任务按它读）", def.Key)
	}
	if def.Value != "30" {
		t.Fatalf("缺省保留天数 = %q, want 30（与 tasks.defaultDockerTaskRetentionDays 同值）", def.Value)
	}
	if def.Type != "N" {
		t.Fatalf("保留天数必须是数值型配置, got %q", def.Type)
	}
}

// TestV018RunsThroughMigrationRunner 走真实执行路径：runner 跑完后任务与配置都在场，
// 再跑一次（模拟「恢复旧备份后重启」的重放）不产生第二行 —— 去重按 invoke_target /
// config_key，与 v009 同一形态。
func TestV018RunsThroughMigrationRunner(t *testing.T) {
	db := newAgentSeedTestDB(t)
	if err := migration.Run(db, logger.NewNop()); err != nil {
		t.Fatalf("migration.Run: %v", err)
	}

	jobs := readJobs(t, db)
	job, ok := jobs["docker-task-cleanup"]
	if !ok {
		t.Fatal("runner 跑完后 docker-task-cleanup 不在 sys_job 里")
	}
	if job.CronExpression != "0 30 3 * * *" || job.Status == nil || *job.Status != entity.JobStatusEnabled {
		t.Fatalf("清理任务的 cron/启用态不符: %+v", job)
	}

	var cfg entity.SysConfig
	if err := db.Where("config_key = ?", "sys.docker.taskRetentionDays").First(&cfg).Error; err != nil {
		t.Fatalf("runner 跑完后保留配置不在 sys_config 里: %v", err)
	}
	if cfg.ConfigValue != "30" {
		t.Fatalf("保留配置值 = %q, want 30", cfg.ConfigValue)
	}

	// 重放（把版本账本退回去再跑一次同一个迁移是做不到的 —— 框架按版本跳过；
	// 这里直接重跑 Up，钉住它的**幂等**：真实库里这条 Up 会被「恢复旧备份」再触发）。
	if err := seedDockerTaskHistory(db); err != nil {
		t.Fatalf("重放 seedDockerTaskHistory: %v", err)
	}
	var jobCount, cfgCount int64
	if err := db.Model(&entity.SysJob{}).Where("invoke_target = ?", "docker-task-cleanup").Count(&jobCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&entity.SysConfig{}).Where("config_key = ?", "sys.docker.taskRetentionDays").Count(&cfgCount).Error; err != nil {
		t.Fatal(err)
	}
	if jobCount != 1 || cfgCount != 1 {
		t.Fatalf("重放不得翻倍: jobs=%d configs=%d, want 1/1", jobCount, cfgCount)
	}
}
