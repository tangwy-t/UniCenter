package tasks

import (
	"context"
	"encoding/json"
	"time"
)

// DockerTaskCleanupTask 任务中心历史清理定时任务（8d）。
//
// 按配置的保留天数删除 docker_task_history 里受理时刻过早的行 —— 与三类日志清理
// （job/login/operation log）是同一个形状、同一个配置惯例（sys.*RetentionDays，
// 缺省值内联在任务里、可运维热改）。
type DockerTaskCleanupTask struct {
	repo    DeleteBeforeRepo
	cfgProv ConfigProvider
}

// defaultDockerTaskRetentionDays 是保留天数缺省值（天）。
//
// 取 30 而**不是**审计的 90（sys.log.retentionDays）的理由：本表服务的是任务中心
// 这个**排障面**（「刚才/最近那次为什么失败」），不是合规审计 —— 谁在什么时候执行
// 了什么，长期口径已经由 sys_operation_log（90 天）承载；任务历史再来一份 90 天
// 只是同一事实存两遍。30 天覆盖「本周谁动过这台机器」的全部日常排障窗口，量级
// 仍是每台设备每天几十行的规模（单行 ~200 字节）。
const defaultDockerTaskRetentionDays = 30

// NewDockerTaskCleanupTask 创建任务中心历史清理任务实例。
func NewDockerTaskCleanupTask(repo DeleteBeforeRepo, cfgProv ConfigProvider) *DockerTaskCleanupTask {
	return &DockerTaskCleanupTask{repo: repo, cfgProv: cfgProv}
}

func (t *DockerTaskCleanupTask) Name() string        { return "docker-task-cleanup" }
func (t *DockerTaskCleanupTask) DisplayName() string { return "任务中心历史清理" }

func (t *DockerTaskCleanupTask) Execute(ctx context.Context, _ json.RawMessage) error {
	days := t.cfgProv.GetInt(ctx, "sys.docker.taskRetentionDays", defaultDockerTaskRetentionDays)
	if days < 1 {
		// 配置页允许任何整数，而 0/负数会让 before=now —— 一轮 tick 就把整张历史
		// 表清空（且不可逆：清理是删除，没有回收站）。宁可把误配当成缺省值，
		// 也不要出一个「清空全部历史」的静默按钮。
		days = defaultDockerTaskRetentionDays
	}
	before := time.Now().AddDate(0, 0, -days)
	_, err := t.repo.DeleteBefore(ctx, before)
	return err
}
