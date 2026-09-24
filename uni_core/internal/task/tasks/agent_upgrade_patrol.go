package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"go.uber.org/zap"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
)

// AgentUpgradePatrolTask 是 Agent 升级的**巡检任务**：把「已开工但长时间没动静」
// 的升级尝试判超时（设计 §7）。
//
// 为什么必须有它：升级的推进靠设备上报，而设备可能以各种方式静默 —— 卡在校验、
// 掉电、被拔网线、换了台机器。没有巡检的话，这些尝试会**永远**停在
// 「升级中」，任务列表也永远收不了口；运维看到的是一个不会变的状态，且无从判断
// 「该等还是该处理」。巡检把「超时」变成一个明确的终态（带原因码），
// 失败可重试、可解释。
//
// 阈值取 `staleAfter`（服务侧常量，15 分钟）：它必须**显著大于**任何一个正常阶段
// 的时长（下载秒级、替换毫秒级、试用期 3 分钟），否则会把慢设备误判成卡住。
type AgentUpgradePatrolTask struct {
	svc AgentUpgradeSweeper
	log logger.LoggerInterface
}

// NewAgentUpgradePatrolTask 创建任务实例（log 为 nil 时退化成 Nop）。
func NewAgentUpgradePatrolTask(svc AgentUpgradeSweeper, log logger.LoggerInterface) *AgentUpgradePatrolTask {
	if log == nil {
		log = logger.NewNop()
	}
	return &AgentUpgradePatrolTask{svc: svc, log: log}
}

// Name 是任务唯一标识（**必须与迁移种子的 invoke_target 逐字一致**，
// 由 v014 的守卫测试钉住）。
func (t *AgentUpgradePatrolTask) Name() string { return "agent-upgrade-patrol" }

// DisplayName 是任务管理页展示名。
func (t *AgentUpgradePatrolTask) DisplayName() string { return "Agent 升级巡检" }

// Execute 执行一轮巡检。参数为空（阈值与批量上限都是服务侧常量）。
func (t *AgentUpgradePatrolTask) Execute(ctx context.Context, _ json.RawMessage) error {
	if t.svc == nil {
		return errors.New("agent-upgrade-patrol: 服务未装配")
	}
	n, err := t.svc.SweepStale(ctx, 0, 200)
	if err != nil {
		return err
	}
	if n > 0 {
		// 只在真的有超时行时记 Info：每分钟一条「0 条超时」会把日志淹掉。
		t.log.Info("agent upgrade patrol swept stale attempts", zap.Int("count", n))
	}
	return nil
}

var _ = time.Second
