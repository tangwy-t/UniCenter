package migrations

import (
	"fmt"

	"gorm.io/gorm"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/migration"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/util"
)

// v014 只为**一个定时任务**而存在：Agent 升级巡检（把卡住的升级尝试判超时）。
//
// ── 为什么它必须独立成一个迁移（而不是塞进 v013）───────────────
//
// v013（升级菜单/权限/配置键）是本特性的一部分，但它**已经在本机开发库执行过**；
// 迁移框架只跑「版本 > 当前最大版本」的迁移，往 v013 里追加定义对已执行过的库
// **完全无效**（v012 的注释把这条讲透了）。任务种子必须走新版本号。
//
// ── 为什么这个任务不能省 ──────────────────────────────────────
//
// 升级的推进靠设备上报，而设备可能以各种方式静默（卡在校验、掉电、被拔网线）。
// 没有巡检，这些尝试永远停在「升级中」，任务列表永远收不了口 —— 运维看到一个
// 不会变的状态，且无从判断该等还是该处理。巡检把「超时」变成明确终态。
func init() {
	migration.Register(migration.Migration{
		Version:     14,
		Description: "新增 Agent 升级巡检任务",
		Up:          seedAgentUpgradePatrol,
	})
}

// agentUpgradePatrolJobDefinitions 是本迁移唯一的种子。
//
// cron `0 * * * * *` = 每分钟一轮。为什么是每分钟而不是每 5 分钟：巡检只做一次
// 「按 last_report_at 筛出卡住的行」的索引查询（无数据时开销可忽略），而判超时的
// 精度直接决定运维等多久才能看到「失败」这个结论 —— 每 5 分钟一轮意味着最坏情况
// 多等 5 分钟，换不来任何收益。
//
// Name/Invoke 必须与 task/tasks 里任务实例的 Name() **逐字一致**
// （不一致会让调度器按 invoke_target 找不到目标、任务静默不执行）——
// 由本文件的守卫测试与 v009 同款（拿 tasks 注册表求集合相等）。
var agentUpgradePatrolJobDefinitions = []jobDef{
	{Name: "Agent 升级巡检", Cron: "0 * * * * *", Invoke: "agent-upgrade-patrol"},
}

// seedAgentUpgradePatrol 按 invoke_target 去重地补齐任务（与 v009 同款取向：
// 已存在的行一律跳过 —— 覆写会把运维调过的 cron/状态打回默认值且不可观测）。
func seedAgentUpgradePatrol(tx *gorm.DB) error {
	for _, def := range agentUpgradePatrolJobDefinitions {
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
			// 巡检**不允许重入**：上一轮还没跑完就再来一轮，只会让同一批行被扫两次
			//（第二次会被非终结守卫挡掉，但白耗一轮查询）。
			Concurrent:   util.Ptr[int8](entity.JobConcurrentDisallowed),
			Status:       util.Ptr[int8](entity.JobStatusEnabled),
			RunAtStartup: util.Ptr[int8](entity.JobRunAtStartupNo),
		}
		if def.Params != "" {
			job.InvokeParams = util.Ptr(def.Params)
		}
		if err := tx.Create(&job).Error; err != nil {
			return fmt.Errorf("v014 创建任务 %s: %w", def.Name, err)
		}
	}
	return nil
}
