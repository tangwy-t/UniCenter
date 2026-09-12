package migrations

import (
	"gorm.io/gorm"

	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/migration"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

func init() {
	migration.Register(migration.Migration{
		Version:     7,
		Description: "初始化定时任务",
		Up:          seedJob,
	})
}

// jobDef 描述一个定时任务；Startup 表示是否开机立即执行一次。
type jobDef struct {
	Name    string
	Cron    string
	Invoke  string
	Startup bool
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
