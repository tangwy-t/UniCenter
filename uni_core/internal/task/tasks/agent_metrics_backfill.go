package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"go.uber.org/zap"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agentmetrics"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/service"
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
//
// `resolution` 让同一个「回退水位」的能力作用在两个档位上，且**缺省严格等于既有行为**：
//
//	{"hours":24}                     → 回退 cursor_5m + 重放 5m（与引入 resolution 之前逐字相同）
//	{"hours":720,"resolution":"1h"}   → 只回退 cursor_1h，**本轮不重放**
//
// 1h 那一支为什么只回退、不重放（这是它与 5m 支的本质差别）：1h 是**可推导**数据，
// 重算它的入口是 rollup（读库里的 5m 行 → 写 1h 行），而本任务手上是 flush 服务 ——
// 它没有、也不该有能写 1h 行的方法。回退之后，**下一轮 rollup** 的普通区间
// `[cursor_1h + 3600, upper)` 自然覆盖被退回的那段小时，把空洞补出来。
// 这条路径的意义：rollup 的按需重扫窗口默认只有 24h，而 5m 行保留 30 天 ——
// 于是「24h 之前的 1h 空洞」在引入本支之前只能靠运维手工改 Redis 键。
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
	// Resolution 选择**回退哪个档位的水位**：`"5m"`（缺省）或 `"1h"`。
	//
	// 为什么要这一维：两个档位的水位是两族键（cursor_5m / cursor_1h）、两个消费方，
	// 而它们的**自愈范围完全不同** —— 5m 的重放读 Redis 原始点（保留 24h），
	// 1h 的重算读库里的 5m 行（保留 30 天）。同一句「回退水位」作用在哪一族键上，
	// 决定了能补回多久以前的数据，所以它必须是一个显式维度而不是隐含在 hours 里
	// （hours=720 在 5m 档会被夹到 24，在 1h 档才是真的 720 —— 靠 hours 猜档位必然猜错）。
	//
	// 缺省（省略或空串）= 5m = 本任务的既有行为；非法值也**回落 5m** 而不是报错
	// （理由见 parseAgentMetricsBackfillParams）。
	Resolution string `json:"resolution"`
}

// Execute 先对齐缺失的起点，再按 `resolution` 决定「回退 + 重放」（5m，缺省）
// 还是「只回退」（1h）。
//
// 参数非法时**不触碰服务**并直接报错：一个写错的 params（例如 hours=0）如果被
// 静默当成缺省，就会每 15 分钟把 24h 的桶重放一遍，而日志上看不出任何异常。
func (t *AgentMetricsBackfillTask) Execute(ctx context.Context, params json.RawMessage) error {
	req, err := parseAgentMetricsBackfillParams(params)
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
	//
	// 1h 那一支同样先跑它（Bootstrap 只碰**缺失的** cursor_5m 键，不会动 cursor_1h）：
	// 让两个分支的前置契约逐字相同，读的人不必去分别推理「哪一支才需要先对齐」。
	if err := t.svc.Bootstrap(ctx); err != nil {
		t.log.Error("agent-metrics-backfill: 起点对齐失败，本轮不重放", zap.Error(err))
		return err
	}

	if req.resolution == agentmetrics.Resolution1h {
		return t.rewind1h(ctx, req)
	}
	if req.resolutionFallback {
		// 给了 resolution 却认不出来 → 已回落 5m。这条日志是回落的**唯一**可见性：
		// 不报错（见 parse 的取向）就意味着必须说清楚「你写的那串我没认出来，按 5m 跑了」。
		t.log.Warn("agent-metrics-backfill: resolution 无法识别，已回落 5m（回退 cursor_5m 并重放）",
			zap.String("resolutionGiven", req.resolutionGiven))
	}

	stats, err := t.svc.BackfillOnce(ctx, req.deviceIDs, req.hours)
	fields := []zap.Field{
		zap.String("resolution", agentmetrics.Resolution5m.String()),
		zap.Int("windowHours", stats.WindowHours),
		zap.Int("devicesScanned", stats.DevicesScanned),
		zap.Int("cursorsRewound", stats.CursorsRewound),
		zap.Int("bucketsWritten", stats.Flush.BucketsWritten),
		zap.Int("bucketsSkipped", stats.Flush.BucketsSkipped),
		zap.Int("resourcesUpserted", stats.Flush.ResourcesUpserted),
		zap.Int("errors", stats.Flush.Errors),
	}
	if err != nil {
		if errors.Is(err, service.ErrMetricPartitionMissing) {
			// 回放的写路径就是 flush 的写路径，故缺分区在这里的处置完全相同：
			// P1 + 中止（重放会写回旧桶，而旧桶正是最可能落在「已被回收/未预建」的
			// 分区上的那一批 —— spec §7.3 的「过去方向」）。
			t.log.Error("agent-metrics-backfill: P1 指标分区缺失，本轮已中止 —— 先跑分区对账(agent-metrics-partition)再重放",
				append(fields, zap.Error(err))...)
			return err
		}
		t.log.Error("agent-metrics-backfill: 本轮部分失败（失败设备的水位未推进，下轮重试同一批桶）",
			append(fields, zap.Error(err))...)
		return err
	}
	t.log.Info("agent-metrics-backfill: 本轮完成", fields...)
	return nil
}

// rewind1h 只回退 `cursor_1h`，**本轮到此为止**（见类型注释里「为什么不重放」）。
//
// 失败时同样记录 stats（哪怕是零值）：`cursorsRewound:0` 本身就是一条有用的读数
// （「水位已经不比目标更旧」与「一台设备都没枚举到」是两种完全不同的运维结论），
// 而 Redis 故障下它至少能说明「枚举是成功的」。
func (t *AgentMetricsBackfillTask) rewind1h(ctx context.Context, req agentBackfillRequest) error {
	stats, err := t.svc.RewindHours(ctx, req.deviceIDs, agentmetrics.Resolution1h, req.hours)
	fields := []zap.Field{
		zap.String("resolution", agentmetrics.Resolution1h.String()),
		zap.Int("windowHours", stats.WindowHours),
		zap.Int("devicesScanned", stats.DevicesScanned),
		zap.Int("cursorsRewound", stats.CursorsRewound),
		// 布尔字段把「本轮不重放」写进日志本身，而不是只写在文案里：
		// 只看字段的人也要能读出「bucketsWritten 之类的键**本支没有**」，
		// 否则一条没有 bucketsWritten 的完成任务日志看起来像「重放写了 0 个桶」。
		zap.Bool("replayed", false),
	}
	if err != nil {
		t.log.Error("agent-metrics-backfill: cursor_1h 回退失败（本轮不重放；下一轮 rollup 仍从原水位继续）",
			append(fields, zap.Error(err))...)
		return err
	}
	t.log.Info("agent-metrics-backfill: cursor_1h 已回退，本轮不重放 —— 被退回的小时由下一轮 rollup 重算补出",
		fields...)
	return nil
}

// agentBackfillRequest 是 parseAgentMetricsBackfillParams 的产出。
type agentBackfillRequest struct {
	// hours == 0 表示「没给窗口」= 用满该档位的保留期（由服务夹取）。
	hours int
	// deviceIDs 为空 = 全部活跃设备。
	deviceIDs []uint64
	// resolution 是解析后的档位（缺省与非法值都回落 5m）。
	resolution agentmetrics.Resolution
	// resolutionFallback 为真表示「params 里给了 resolution，但认不出来」——
	// 此时 resolution 已回落 5m，且**必须**留下一条 Warn（回落不许是静默的）。
	// 它与「字段缺省」（认得出、就是 5m）是两回事：缺省是本任务的正常用法，
	// 不该每轮刷一条 Warn。
	resolutionFallback bool
	// resolutionGiven 是 params 里**原样**出现的 resolution（"" = 没给这个字段）。
	// 保留它只为让那条 Warn 能说出「你写的是 1H」。
	resolutionGiven string
}

// parseAgentMetricsBackfillParams 解析 invoke_params。
//
// 返回的 hours == 0 表示「没给窗口」= 用满该档位的保留期（由服务夹取）；
// hours 一律为正或 0，负数/0 显式出现在 params 里属于配置错误，直接报错。
//
// `resolution` 的取向与 hours **刻意相反**：hours 非法（<=0）大声报错，而 resolution
// 认不出来时**回落 5m**。理由是两者的失败后果不对称：
//   - 一个错的 hours 会让「重放多少历史」这个量级失控（写放大）；
//   - 而 5m（回退 cursor_5m + 重放 5m）是本任务**自 v009 起的既有行为**，也是缺省行为 ——
//     回落到它不会产生任何新的写入面，只是「这一轮没做你想做的 1h 回退」，
//     下一轮（运维改对 params 之后）就能补上。反过来，把 `"1H"` 这种大小写笔误变成
//     P1 报错会打断每小时的定时任务，代价远大于收益。
//
// 因此「非法 → 回落」必须配一条 Warn 日志（见 Execute），否则它就是静默的降级。
func parseAgentMetricsBackfillParams(params json.RawMessage) (agentBackfillRequest, error) {
	if len(params) == 0 {
		return agentBackfillRequest{}, nil
	}
	var p AgentMetricsBackfillParams
	if err := json.Unmarshal(params, &p); err != nil {
		return agentBackfillRequest{}, fmt.Errorf("agent-metrics-backfill: invoke_params 不是合法 JSON: %w", err)
	}

	hours := 0
	if p.Hours != nil {
		if *p.Hours <= 0 {
			return agentBackfillRequest{}, fmt.Errorf(
				"agent-metrics-backfill: hours=%d 非法（必须为正；省略该字段即用满 raw 保留期）", *p.Hours)
		}
		hours = *p.Hours
	}
	for _, id := range p.DeviceIDs {
		if id == 0 {
			return agentBackfillRequest{}, fmt.Errorf("agent-metrics-backfill: device_ids 里含 0（不是合法设备号）")
		}
	}
	res, recognized := parseBackfillResolution(p.Resolution)
	return agentBackfillRequest{
		hours:              hours,
		deviceIDs:          p.DeviceIDs,
		resolution:         res,
		resolutionFallback: !recognized,
		resolutionGiven:    p.Resolution,
	}, nil
}

// parseBackfillResolution 把 params 里的档位字符串翻成 Resolution；无法识别 → (5m, false)。
//
// 只认 `Resolution.String()` 的那两个字面量（"5m" / "1h"），并且**不复用 String() 做比较**
// （同 agentmetrics.CursorKey 对键名契约的处理）：String() 是给人看的显示名，
// 而 invoke_params 是**调度器里的存量字符串**（v009 种子、运维手工改过的行）——
// 拿显示名当解析入口，未来改一次显示名就会让所有存量 params 静默回落 5m。
// 这里用字面量、并用断言钉住两个字面量与 String() 一致。
//
// 第二个返回值是「认出来了吗」，它只用于决定要不要留一条 Warn：`""`（字段缺省）
// 是**认得出的** 5m，不是回落 —— 两者都会走同一条 5m 路径，但只有后者需要被运维看见。
func parseBackfillResolution(s string) (agentmetrics.Resolution, bool) {
	switch s {
	case "", "5m":
		return agentmetrics.Resolution5m, true
	case "1h":
		return agentmetrics.Resolution1h, true
	default:
		return agentmetrics.Resolution5m, false
	}
}
