package tasks

import (
	"context"
	"encoding/json"
	"time"
)

// LoginLogCleanupTask 登录日志清理定时任务。
// 根据配置的保留天数，删除 login_time 早于当前时间减去保留天数的登录日志记录。
type LoginLogCleanupTask struct {
	repo    DeleteBeforeRepo
	cfgProv ConfigProvider
}

// NewLoginLogCleanupTask 创建登录日志清理任务实例。
func NewLoginLogCleanupTask(repo DeleteBeforeRepo, cfgProv ConfigProvider) *LoginLogCleanupTask {
	return &LoginLogCleanupTask{repo: repo, cfgProv: cfgProv}
}

func (t *LoginLogCleanupTask) Name() string        { return "login-log-cleanup" }
func (t *LoginLogCleanupTask) DisplayName() string { return "登录日志清理" }

func (t *LoginLogCleanupTask) Execute(ctx context.Context, _ json.RawMessage) error {
	before := time.Now().AddDate(0, 0, -t.cfgProv.GetInt(ctx, "sys.log.retentionDays", 30))
	_, err := t.repo.DeleteBefore(ctx, before)
	return err
}
