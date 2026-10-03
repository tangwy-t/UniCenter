package migrations

import (
	"fmt"

	"gorm.io/gorm"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/migration"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/util"
)

func init() {
	migration.Register(migration.Migration{
		Version:     18,
		Description: "初始化任务中心历史清理任务与保留配置",
		Up:          seedDockerTaskHistory,
	})
}

// dockerTaskJobDefinitions 是任务中心历史（8d）的内置定时任务。
//
// Name 必须与 tasks 里 DockerTaskCleanupTask.Name() 逐字一致（由守卫测试
// TestV018InvokeTargetsMatchTaskNames 与真实注册表双向钉住）。
//
// cron 取 03:30：三类日志清理都在 03:00、指标分区维护在 04:00 —— 本任务放在两者
// 之间的空档，不与任何一批清理抢 DB。
var dockerTaskJobDefinitions = []jobDef{
	{Name: "任务中心历史清理", Cron: "0 30 3 * * *", Invoke: "docker-task-cleanup"},
}

// dockerTaskConfigDefinitions 是清理任务的保留期配置（单一来源）。
//
// 默认 30 天，与 task/tasks.DockerTaskCleanupTask 里 defaultDockerTaskRetentionDays
// 同值（那处是未导出常量，由 v018 的测试按字面量交叉钉住，与 v009 的分区配置同款）。
var dockerTaskConfigDefinitions = []configDef{
	{Key: "sys.docker.taskRetentionDays", Value: "30", Type: "N", Name: "任务中心历史保留天数",
		Remark: "任务历史（docker_task_history）按受理时刻保留的天数；<1 视为缺省 30", Enabled: true},
}

// seedDockerTaskHistory 写入清除任务与保留配置。
//
// 与 v009 同一形态：按 invoke_target / config_key **去重、跳过既存行** ——
// 真实库里这条任务/配置可能已被运维建过或调过（例如按自己的排障节奏把保留期改成
// 7 天），种子存在的意义是「别让它缺失」，不是「让它回到默认值」；覆写会造成
// 完全不可观测的回退。
//
// 为什么是独立迁移而不是并进 v007/v015：开发期库早已越过那两个版本，
// 并进去对它们没有任何效果（迁移框架按版本账本跳过），会出现「fresh 库有、
// 已跑过的库没有」的分裂。零兼容令针对的是不做兼容层，不是允许已发布的迁移
// 在事后改变语义（框架教义：永不回头修改已发布的迁移文件）。
func seedDockerTaskHistory(tx *gorm.DB) error {
	for _, def := range dockerTaskJobDefinitions {
		exists, err := jobInvokeTargetExists(tx, def.Invoke)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		job := entity.SysJob{
			Name:           def.Name,
			JobGroup:       "system",
			CronExpression: def.Cron,
			InvokeTarget:   def.Invoke,
			Concurrent:     util.Ptr[int8](entity.JobConcurrentAllowed),
			Status:         util.Ptr[int8](entity.JobStatusEnabled),
			RunAtStartup:   util.Ptr[int8](entity.JobRunAtStartupNo),
		}
		if def.Params != "" {
			job.InvokeParams = util.Ptr(def.Params)
		}
		if err := tx.Create(&job).Error; err != nil {
			return fmt.Errorf("v018 写入任务 %s: %w", def.Invoke, err)
		}
	}
	for _, def := range dockerTaskConfigDefinitions {
		var n int64
		if err := tx.Model(&entity.SysConfig{}).
			Where("config_key = ?", def.Key).Count(&n).Error; err != nil {
			return fmt.Errorf("v018 查询配置 %s: %w", def.Key, err)
		}
		if n > 0 {
			continue
		}
		row := entity.SysConfig{
			Name:        def.Name,
			ConfigKey:   def.Key,
			ConfigValue: def.Value,
			ConfigType:  def.Type,
			Remark:      util.Ptr(def.Remark),
			Status:      util.Ptr[int8](boolToInt8(def.Enabled)),
		}
		if err := tx.Create(&row).Error; err != nil {
			return fmt.Errorf("v018 写入配置 %s: %w", def.Key, err)
		}
	}
	return nil
}
