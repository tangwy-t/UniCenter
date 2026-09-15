package tasks

import (
	"context"
	"encoding/json"

	"go.uber.org/zap"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
)

// AgentMetricsPartitionTask 跑 6 张指标表的分区对账：建表 → 逐表补缺 → 回收过期分区。
//
// 它是**唯一**的 DDL 入口（不碰任何一行指标数据，回收走 TRUNCATE/DROP PARTITION，
// 因此不与 flush 的 UPSERT 争行锁）。为什么必须是「每轮全量对账」而不是增量记账：
// 分区状态是外部可变的（恢复旧备份、人工 DDL、主从切换），任何进程内的记忆都会漂移。
type AgentMetricsPartitionTask struct {
	svc AgentPartitionService
	log logger.LoggerInterface
}

// NewAgentMetricsPartitionTask 创建任务实例（log 为 nil 时退化成 Nop）。
func NewAgentMetricsPartitionTask(svc AgentPartitionService, log logger.LoggerInterface) *AgentMetricsPartitionTask {
	if log == nil {
		log = logger.NewNop()
	}
	return &AgentMetricsPartitionTask{svc: svc, log: log}
}

func (t *AgentMetricsPartitionTask) Name() string        { return "agent-metrics-partition" }
func (t *AgentMetricsPartitionTask) DisplayName() string { return "设备指标分区维护" }

// Execute 跑一轮对账，忽略 invoke_params（对账的输入是「数据库当前的分区集合」，
// 不存在「只对账某张表」的安全用法：漏掉的表下一轮照样要对）。非空 params 记 Warn。
//
// 拿不到锁（别的实例在跑）时服务返回 Skipped=1 且 err==nil —— 这不是失败，
// 是**设计中的互斥结果**，任务照常记 Info 并带上 skipped 字段（日志里能区分
// 「没抢到锁」与「什么都没做」）。
func (t *AgentMetricsPartitionTask) Execute(ctx context.Context, params json.RawMessage) error {
	if len(params) > 0 {
		t.log.Warn("agent-metrics-partition: 忽略 params（分区对账恒为 6 张表的全量对账）",
			zap.String("params", string(params)))
	}

	stats, err := t.svc.Reconcile(ctx)
	fields := []zap.Field{
		zap.Int("tablesScanned", stats.TablesScanned),
		zap.Int("partitionsCreated", stats.PartitionsCreated),
		zap.Int("partitionsTruncated", stats.PartitionsTruncated),
		zap.Int("partitionsDropped", stats.PartitionsDropped),
		zap.Int("skipped", stats.Skipped),
	}
	if err != nil {
		t.log.Error("agent-metrics-partition: 本轮对账失败（缺分区会让指标写入失败，下一轮会重试）",
			append(fields, zap.Error(err))...)
		return err
	}
	if stats.Skipped > 0 {
		t.log.Info("agent-metrics-partition: 未抢到分区锁，本轮跳过（另一实例正在对账）", fields...)
		return nil
	}
	t.log.Info("agent-metrics-partition: 本轮完成", fields...)
	return nil
}
