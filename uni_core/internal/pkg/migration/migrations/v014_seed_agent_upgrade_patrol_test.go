package migrations

import (
	"testing"

	"github.com/tangwy-t/UniCenter/uni_core/internal/task/tasks"
)

// 守卫：种子的 invoke_target 必须与**真实注册的任务名**集合相等。
//
// 不一致的后果是静默的：调度器按 invoke_target 在注册表里找目标，找不到就
// 什么都不做（不报错、不记录），现象是「任务明明启用了却从不执行」。
// 与 v009 的同款守卫一个机制。
func TestV014InvokeTargetMatchesRegisteredTask(t *testing.T) {
	registered := map[string]bool{}
	for _, tk := range tasks.All(tasks.Deps{}) {
		registered[tk.Name()] = true
	}
	if len(agentUpgradePatrolJobDefinitions) != 1 {
		t.Fatalf("本迁移应恰有 1 个任务定义，实得 %d", len(agentUpgradePatrolJobDefinitions))
	}
	for _, def := range agentUpgradePatrolJobDefinitions {
		if !registered[def.Invoke] {
			t.Fatalf("任务种子的 invoke_target %q 没有对应的已注册任务（调度器会静默不执行）", def.Invoke)
		}
	}
}

// 守卫：巡检的 cron 必须是**每分钟**。
//
// 它决定「判超时的精度」：卡住的尝试最多多等一轮才变成「失败」。改成低频
// （例如每小时）不会报错，只是让运维盯着「升级中」多等一小时 —— 值得一条断言。
func TestV014PatrolCronIsEveryMinute(t *testing.T) {
	for _, def := range agentUpgradePatrolJobDefinitions {
		if def.Cron != "0 * * * * *" {
			t.Fatalf("巡检应每分钟一轮，实得 %q", def.Cron)
		}
	}
}
