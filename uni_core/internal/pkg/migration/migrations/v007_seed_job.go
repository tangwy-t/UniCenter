package migrations

import (
	"gorm.io/gorm"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/migration"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/util"
)

func init() {
	migration.Register(migration.Migration{
		Version:     7,
		Description: "初始化定时任务",
		Up:          seedJob,
	})
}

// jobDef 描述一个定时任务；Startup 表示是否开机立即执行一次。
//
// Params 是 invoke_params 的 JSON 原文（空串 = 不写该列）。v007 的 5 个任务
// 都不带参数，故该字段在 v007 的清单里全为空；v009 的 backfill 用它种下
// 显式回溯窗口（`{"hours":24}`）。**v007 的 seedJob 保持原样不读它** ——
// 那个迁移在真实库上早已执行过，改它的行为没有任何效果，只会制造两套语义。
type jobDef struct {
	Name    string
	Cron    string
	Invoke  string
	Startup bool
	Params  string
}

// jobDefinitions 是全部系统内置定时任务的唯一来源（历史 v005 + v008 合并）。
var jobDefinitions = []jobDef{
	{Name: "操作日志清理", Cron: "0 0 3 * * *", Invoke: "op-log-cleanup"},
	{Name: "登录日志清理", Cron: "0 0 3 * * *", Invoke: "login-log-cleanup"},
	{Name: "任务日志清理", Cron: "0 0 3 * * *", Invoke: "job-log-cleanup"},
	{Name: "配置全量同步", Cron: "0 0 2 * * *", Invoke: "config-cache-sync", Startup: true},
	{Name: "字典全量同步", Cron: "0 0 3 * * *", Invoke: "dict-cache-sync", Startup: true},
}

// seedJob 批量写入全部定时任务。
func seedJob(tx *gorm.DB) error {
	jobs := make([]entity.SysJob, 0, len(jobDefinitions))
	for _, j := range jobDefinitions {
		runAtStartup := entity.JobRunAtStartupNo
		if j.Startup {
			runAtStartup = entity.JobRunAtStartupYes
		}
		jobs = append(jobs, entity.SysJob{
			Name:           j.Name,
			JobGroup:       "system",
			CronExpression: j.Cron,
			InvokeTarget:   j.Invoke,
			Concurrent:     util.Ptr[int8](entity.JobConcurrentAllowed),
			Status:         util.Ptr[int8](entity.JobStatusEnabled),
			RunAtStartup:   util.Ptr[int8](runAtStartup),
		})
	}
	return tx.CreateInBatches(jobs, 100).Error
}
