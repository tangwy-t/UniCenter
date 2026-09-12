package tasks

import (
	"context"
	"encoding/json"
	"time"
)

// OpLogCleanupTask 操作日志清理定时任务。
// 根据配置的保留天数，删除 oper_time 早于当前时间减去保留天数的操作日志记录。
type OpLogCleanupTask struct {
	repo    DeleteBeforeRepo
	cfgProv ConfigProvider
}

// NewOpLogCleanupTask 创建操作日志清理任务实例。
func NewOpLogCleanupTask(repo DeleteBeforeRepo, cfgProv ConfigProvider) *OpLogCleanupTask {
	return &OpLogCleanupTask{repo: repo, cfgProv: cfgProv}
}

func (t *OpLogCleanupTask) Name() string        { return "op-log-cleanup" }
func (t *OpLogCleanupTask) DisplayName() string { return "操作日志清理" }

func (t *OpLogCleanupTask) Execute(ctx context.Context, _ json.RawMessage) error {
	before := time.Now().AddDate(0, 0, -t.cfgProv.GetInt(ctx, "sys.log.retentionDays", 30))
	_, err := t.repo.DeleteBefore(ctx, before)
	return err
}
