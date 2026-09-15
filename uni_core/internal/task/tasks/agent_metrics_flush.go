package tasks

import (
	"context"
	"encoding/json"

	"go.uber.org/zap"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
)

// AgentMetricsFlushTask 把 Redis 热层里**已闭**的 5min 桶落成 device_metric_5m 行。
//
// 薄封装：任务层只做「调一次服务 + 把读数记进日志」。聚合口径、资源 name→id 解析、
// 两段提交、水位推进全在 service.AgentMetricsFlushService 里 —— 任务层一旦出现
// 业务规则，同一个口径就有两处实现，漂移时谁都不知道该信哪一个。
type AgentMetricsFlushTask struct {
	svc AgentFlushService
	log logger.LoggerInterface
}

// NewAgentMetricsFlushTask 创建任务实例。
//
// log 允许为 nil（All(tasks.Deps{}) 的零值依赖路径会被测试与 wireup 的早期装配走到）：
// 默认退化成 Nop，而不是让第一个 zap 调用 panic。
func NewAgentMetricsFlushTask(svc AgentFlushService, log logger.LoggerInterface) *AgentMetricsFlushTask {
	if log == nil {
		log = logger.NewNop()
	}
	return &AgentMetricsFlushTask{svc: svc, log: log}
}

func (t *AgentMetricsFlushTask) Name() string        { return "agent-metrics-flush" }
func (t *AgentMetricsFlushTask) DisplayName() string { return "设备指标落库(5min)" }

// Execute 跑一轮落库，**忽略** invoke_params。
//
// 为什么忽略：flush 恒为全量一轮（服务侧 FlushOnce 没有单设备入口）。给任务加一条
// 「只处理某设备」的旁路，等于在 5m 这个「唯一真值来源」上开第二条写路径 ——
// 而按设备的定向处理已经有 agent-metrics-backfill 的 device_ids 覆盖（语义是
// 回退该设备的水位后重放，比「跳过其他人」更符合运维的真实需求）。
// 非空 params 因此记一条 Warn：静默吞掉会让「按 device_id 触发却没生效」
// 变成日志里查不出来的现象。
func (t *AgentMetricsFlushTask) Execute(ctx context.Context, params json.RawMessage) error {
	if len(params) > 0 {
		t.log.Warn("agent-metrics-flush: 忽略 params（flush 恒为全量一轮；定向重放请用 agent-metrics-backfill 的 device_ids）",
			zap.String("params", string(params)))
	}

	stats, err := t.svc.FlushOnce(ctx)
	fields := []zap.Field{
		zap.Int("devicesScanned", stats.DevicesScanned),
		zap.Int("bucketsWritten", stats.BucketsWritten),
		zap.Int("bucketsSkipped", stats.BucketsSkipped),
		zap.Int("resourcesUpserted", stats.ResourcesUpserted),
		zap.Int("errors", stats.Errors),
	}
	if err != nil {
		t.log.Error("agent-metrics-flush: 本轮部分失败（失败设备的水位未推进，下轮重试同一批桶）",
			append(fields, zap.Error(err))...)
		return err
	}
	t.log.Info("agent-metrics-flush: 本轮完成", fields...)
	return nil
}
