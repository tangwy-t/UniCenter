package tasks

import (
	"context"
	"encoding/json"
	"time"
)

// JobLogCleanupTask 任务日志清理定时任务。
// 根据配置的保留天数，删除 start_time 早于当前时间减去保留天数的任务执行日志记录。
type JobLogCleanupTask struct {
	repo    DeleteBeforeRepo
	cfgProv ConfigProvider
}

// NewJobLogCleanupTask 创建任务日志清理实例。
func NewJobLogCleanupTask(repo DeleteBeforeRepo, cfgProv ConfigProvider) *JobLogCleanupTask {
	return &JobLogCleanupTask{repo: repo, cfgProv: cfgProv}
}

func (t *JobLogCleanupTask) Name() string        { return "job-log-cleanup" }
func (t *JobLogCleanupTask) DisplayName() string { return "任务日志清理" }

func (t *JobLogCleanupTask) Execute(ctx context.Context, _ json.RawMessage) error {
	before := time.Now().AddDate(0, 0, -t.cfgProv.GetInt(ctx, "sys.log.retentionDays", 30))
	_, err := t.repo.DeleteBefore(ctx, before)
	return err
}
