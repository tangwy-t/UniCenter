package tasks

import (
	"context"
	"encoding/json"

	"go.uber.org/zap"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
)

// AgentMetricsRollupTask 把 5min 行回滚成 1h 行，并重算 repair 集合里的小时。
//
// 与 flush 的关系（写在这里是为了让改动任务层的人一眼看到边界）：flush 写事务 A
// （唯一真值），本任务写事务 B（可推导数据）。1h 失败绝不能回滚 5m，故两者是
// **两个独立任务**而不是一个任务里的两步。
type AgentMetricsRollupTask struct {
	svc AgentRollupService
	log logger.LoggerInterface
}

// NewAgentMetricsRollupTask 创建任务实例（log 为 nil 时退化成 Nop）。
func NewAgentMetricsRollupTask(svc AgentRollupService, log logger.LoggerInterface) *AgentMetricsRollupTask {
	if log == nil {
		log = logger.NewNop()
	}
	return &AgentMetricsRollupTask{svc: svc, log: log}
}

func (t *AgentMetricsRollupTask) Name() string        { return "agent-metrics-rollup" }
func (t *AgentMetricsRollupTask) DisplayName() string { return "设备指标1h回滚" }

// Execute 跑一轮回滚，忽略 invoke_params（同 flush：只有一个全量入口，
// 半量入口会让「哪些小时算过」这件事出现第二个真相来源）。非空 params 记 Warn。
func (t *AgentMetricsRollupTask) Execute(ctx context.Context, params json.RawMessage) error {
	if len(params) > 0 {
		t.log.Warn("agent-metrics-rollup: 忽略 params（回滚恒为全量一轮：普通游标区间 + repair 集合）",
			zap.String("params", string(params)))
	}

	stats, err := t.svc.RollupOnce(ctx)
	fields := []zap.Field{
		zap.Int("hoursScanned", stats.HoursScanned),
		zap.Int("hoursWritten", stats.HoursWritten),
		zap.Int("hoursRepaired", stats.HoursRepaired),
		zap.Int("hoursSkipped", stats.HoursSkipped),
		// 按需重扫的两个读数：HoursRescanned = 按集合差送去重算的小时数、
		// HoursBackfilled = 其中真正补出 1h 行的小时数。正常一轮两者都是 0；
		// 它们非零是「5m 行迟到落库、1h 空洞被补上」的唯一可观测信号
		//（1h 行本身只会多出一行，日志里看不出它是补出来的还是当轮滚出来的）。
		zap.Int("hoursRescanned", stats.HoursRescanned),
		zap.Int("hoursBackfilled", stats.HoursBackfilled),
		zap.Int("errors", stats.Errors),
	}
	if err != nil {
		t.log.Error("agent-metrics-rollup: 本轮部分失败（1h 行缺失不阻塞 5m，下轮重试）",
			append(fields, zap.Error(err))...)
		return err
	}
	t.log.Info("agent-metrics-rollup: 本轮完成", fields...)
	return nil
}
