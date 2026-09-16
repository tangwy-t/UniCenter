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
		// 配额截断与回退追平：这两个读数只有落在**任务层这一行**才会被别人看见。
		//
		// hoursDeferred = 本轮因小时配额用尽而尚未处理的小时数（0 当且仅当本轮没被截断）。
		// 它回答的是「这一轮是被配额切了一刀，还是真的把积压走完了」—— 只看 hoursWritten
		// 分不出「写了 48 个小时」是配额的上限还是全部工作量；而 HoursDeferred 的口径
		// （见 service.RollupStats）让两者互斥。服务侧还有一条逐设备的 Info 说明是哪台
		// 设备被截断（配额是每设备独立的），本字段是**本轮全部设备的合计**。
		zap.Int("hoursDeferred", stats.HoursDeferred),
		// rewindsCaughtUp = 本轮追平并清掉「回退待追平」标记的设备数。运维执行过一次
		// RewindHours 之后，**唯一**能证明它已经生效的读数就是它（逐设备的「回退目标已追平」
		// Info 带设备号与目标，见 service.catchUpRewindMarker；这里给的是本轮的合计）。
		// 两者不重复：服务侧回答「哪台设备、追平到哪个目标」，本行回答「这一轮有没有发生追平」。
		zap.Int("rewindsCaughtUp", stats.RewindsCaughtUp),
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
