package tasks

import (
	"context"
	"encoding/json"
	"fmt"

	"go.uber.org/zap"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
)

// AgentMetricsBackfillTask 补齐游标已经越过、但当时**没有数据**的那批已闭桶。
//
// 为什么必须有一个独立任务（而不是「flush 会自己补」）：
//
//	flush 的水位只能向前推进（flushDevice 的区间是 `[cursor+300, now−grace)`），
//	因此两类场景的处置完全不同：
//	  · 服务停机几小时 → 游标**落后** → flush 下一轮自己就补齐了（V009 的
//	    backfill 不重复做这件事，见 service.BackfillOnce 的注释）；
//	  · 桶当时是空的（agent 断网后补传、Redis 从旧备份恢复、
//	    分区被回收后要求重放）→ 游标**已经推过去了** → flush 永远读不到它们，
//	    这些桶就此永久缺失。
//	本任务处理的就是第二类：把水位**回退**到显式窗口起点，重放一遍。
//
// 因此它不是空壳，也**不是**「多跑一次 flush」：它的输入是一个回溯窗口
// （`{"hours":N}`，缺省 = 用满 raw 保留期 24h），产出是「回退了多少个水位 +
// 重放写回了多少桶」的读数。
type AgentMetricsBackfillTask struct {
	svc AgentFlushService
	log logger.LoggerInterface
}

// NewAgentMetricsBackfillTask 创建任务实例（log 为 nil 时退化成 Nop）。
func NewAgentMetricsBackfillTask(svc AgentFlushService, log logger.LoggerInterface) *AgentMetricsBackfillTask {
	if log == nil {
		log = logger.NewNop()
	}
	return &AgentMetricsBackfillTask{svc: svc, log: log}
}

func (t *AgentMetricsBackfillTask) Name() string        { return "agent-metrics-backfill" }
func (t *AgentMetricsBackfillTask) DisplayName() string { return "设备指标补齐重放" }

// AgentMetricsBackfillParams 是本任务的 invoke_params 结构（4 个指标任务里唯一带参数的一个）。
type AgentMetricsBackfillParams struct {
	// Hours 是回溯窗口（小时）。省略 = 用满 raw 保留期（服务侧夹取，当前 24h）；
	// 本任务**不复制**「24」这个常量 —— 它的依据是 Redis 里 raw 点的保留期，
	// 属于存储事实，只该有一个来源。
	Hours *int `json:"hours"`
	// DeviceIDs 限定**回退哪些设备的水位**（空 = 全部活跃设备）。
	// 它不缩小本轮落库范围：重放走的就是标准的那一轮 FlushOnce
	// （全量、按 (device_id, bucket_ts) 幂等 UPSERT），单设备写路径不在这里另造。
	DeviceIDs []uint64 `json:"device_ids"`
}

// Execute 先对齐缺失的起点，再带窗口回放一轮。
//
// 参数非法时**不触碰服务**并直接报错：一个写错的 params（例如 hours=0）如果被
// 静默当成缺省，就会每 15 分钟把 24h 的桶重放一遍，而日志上看不出任何异常。
func (t *AgentMetricsBackfillTask) Execute(ctx context.Context, params json.RawMessage) error {
	hours, deviceIDs, err := parseAgentMetricsBackfillParams(params)
	if err != nil {
		t.log.Error("agent-metrics-backfill: params 非法，本轮不执行", zap.Error(err))
		return err
	}

	// 起点对齐必须先于回溯：Redis 刚恢复时整族水位键可能都缺失，
	// Bootstrap 按 now−保留期 写回起点（只补缺失的键，不覆写已有水位）。
	//
	// 实话：这一步在功能上是**冗余**的 —— readCursor 遇到缺失键会做同样的
	// 初始化（见 service/agent_metrics_flush.go 的 readCursor），故单独跑它
	// 不会多补出任何水位。保留它有两个可检验的理由：
	//  1. Redis 不可用时失败被**归属到独立的一步**（日志能直接读出「起点都没
	//     对齐」，而不是把 Redis 故障混进「重放写失败」里）；
	//  2. 计划把「先 Bootstrap 对齐起点，再回放」写成了显式契约，
	//     而它确实是窗口的语义前提（水位「缺失」与水位「落后」是两回事）。
	if err := t.svc.Bootstrap(ctx); err != nil {
		t.log.Error("agent-metrics-backfill: 起点对齐失败，本轮不重放", zap.Error(err))
		return err
	}

	stats, err := t.svc.BackfillOnce(ctx, deviceIDs, hours)
	fields := []zap.Field{
		zap.Int("windowHours", stats.WindowHours),
		zap.Int("devicesScanned", stats.DevicesScanned),
		zap.Int("cursorsRewound", stats.CursorsRewound),
		zap.Int("bucketsWritten", stats.Flush.BucketsWritten),
		zap.Int("bucketsSkipped", stats.Flush.BucketsSkipped),
		zap.Int("resourcesUpserted", stats.Flush.ResourcesUpserted),
		zap.Int("errors", stats.Flush.Errors),
	}
	if err != nil {
		t.log.Error("agent-metrics-backfill: 本轮部分失败（失败设备的水位未推进，下轮重试同一批桶）",
			append(fields, zap.Error(err))...)
		return err
	}
	t.log.Info("agent-metrics-backfill: 本轮完成", fields...)
	return nil
}

// parseAgentMetricsBackfillParams 解析 invoke_params。
//
// 返回的 hours == 0 表示「没给窗口」= 用满 raw 保留期（由服务夹取）；
// hours 一律为正或 0，负数/0 显式出现在 params 里属于配置错误，直接报错。
func parseAgentMetricsBackfillParams(params json.RawMessage) (int, []uint64, error) {
	if len(params) == 0 {
		return 0, nil, nil
	}
	var p AgentMetricsBackfillParams
	if err := json.Unmarshal(params, &p); err != nil {
		return 0, nil, fmt.Errorf("agent-metrics-backfill: invoke_params 不是合法 JSON: %w", err)
	}

	hours := 0
	if p.Hours != nil {
		if *p.Hours <= 0 {
			return 0, nil, fmt.Errorf(
				"agent-metrics-backfill: hours=%d 非法（必须为正；省略该字段即用满 raw 保留期）", *p.Hours)
		}
		hours = *p.Hours
	}
	for _, id := range p.DeviceIDs {
		if id == 0 {
			return 0, nil, fmt.Errorf("agent-metrics-backfill: device_ids 里含 0（不是合法设备号）")
		}
	}
	return hours, p.DeviceIDs, nil
}
