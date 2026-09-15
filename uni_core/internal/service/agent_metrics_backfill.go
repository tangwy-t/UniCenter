package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agentmetrics"
)

// BackfillStats 是本轮「回溯重放」的读数。
type BackfillStats struct {
	// DevicesScanned 是本轮从活跃索引里枚举到的设备数（**过滤前**的，与
	// FlushStats.DevicesScanned 同源）。device_ids 只缩小回退范围，不缩小枚举。
	DevicesScanned int
	// CursorsRewound 是**实际被回退**的 5m 水位个数（已经不比目标更旧的、不在
	// device_ids 里的、游标键缺失而刚被 Bootstrap 初始化的，都不计入）。
	CursorsRewound int
	// WindowHours 是实际生效的回溯窗口（小时，已按 raw 保留期夹取）。
	// 它**不是**入参的原样回显：入参 0 = 用满保留期、入参 100 会夹到 24。
	// 运维只能靠它判断「这一轮到底重放了多少历史」。
	WindowHours int
	// Batches 是本轮切出的设备批数（spec §7.1 的「按设备分批」）。
	// 它与 PacingWaits 一起让「限速真的生效了吗」可观测 —— 否则限速是个只在 DB 侧
	// 体现的隐变量（日志里看不出本轮是 1 批还是 25 批，也就无从判断它有没有退化成
	// 一次性突发）。
	Batches int
	// PacingWaits 是实际发生的**批间停顿次数**（= Batches − 1，没有设备时为 0）。
	PacingWaits int
	// Flush 是回退之后那一轮全量落库的读数（补齐的产出就是落库，故直接复用）。
	Flush FlushStats
}

// 回溯窗口的边界：**就是 raw 点的 Redis 保留期**。
//
// 为什么上界是保留期而不是一个更大的数：比保留期更早的桶在 Redis 里**必然**
// 是空的（spec §7.1），重放它们只会得到 288×N 次空读，写不出一行数据。
// 为什么下界也是保留期：省略 hours 时「能补多少补多少」的正确答案就是保留期，
// 任务侧因此不必复制这个常量（任务传 0，这里决定实际值）。
const (
	defaultBackfillHours = int(bootstrapWindow / time.Hour) // 24
	maxBackfillHours     = defaultBackfillHours
)

// 回填限速的两个默认参数（spec §7.1 的「按设备分批 + 批间节奏」）。
//
// 取值依据（保守，但别让 24h 全量回填跑太久）：
//
//   - **批大小 50 台**：单台满窗口 = 24h/5min = 288 个桶，一批 = 50×288 ≈
//     **14400 次桶处理**（每次 1 次 Redis 读 + 至多 1 个 UPSERT 事务）。按单桶
//     1~3ms 的量级估，一批约 15~45s —— 这是一个「单批的 DB 影响有界、且远小于
//     一轮 flush 的 300s 周期」的尺度。取更小（如 10 台）不会更安全多少，
//     只会把批数乘 5、把总时长推上去；取更大（如 500 = 全量）等于没分批。
//   - **批间 2s**：一批 15~45s 的工作之后停顿 2s，让**实时 flush**（每 5 分钟一轮）
//     有一个确定的插入窗口，而不是排在回填后面等锁；500 台 = 10 批 = 9 次停顿
//     = **18s**，相对满窗口回放的总时长可以忽略。
//   - 两个默认值都可被 WithBackfillPacing 覆盖（节奏），批大小是常量：
//     它是**正确性无关**的限速参数，可调的价值远低于「多一个能配错的旋钮」。
const (
	// defaultBackfillBatchDevices 是每批的设备数。
	defaultBackfillBatchDevices = 50
	// defaultBackfillPacing 是批间停顿。
	defaultBackfillPacing = 2 * time.Second
)

// BackfillOnce 跑一轮「带显式回溯窗口的补齐重放」：把落在窗口内的 5m 水位
// **回退**到窗口起点，再按标准写路径（分批 + 限速）重放该窗口，于是那些已经越过、
// 但当时 Redis 里没有数据（或被改写/丢失）的桶被重新读一遍并 UPSERT 回库。
//
// 它为什么与 FlushOnce 不是同一件事（这是本方法存在的全部理由）：
//
//	flushDevice 的区间是 `[cursor+300, now−grace)` —— 一个**只能向前**的水位。
//	因此「游标已经越过某个桶」之后，那个桶就再也不会被读第二次：
//	  · 服务停机几小时 → 游标落后 → **flush 自己就会补齐**（无需 backfill）；
//	  · Redis 丢水位键 → readCursor 按 Bootstrap 语义初始化到 now−24h → 也会补齐；
//	  · 但「桶当时是空的（agent 断网后补传、Redis 从旧备份恢复、分区被回收后
//	    要求重放）」→ 游标**已经推过去了**，flush 永远救不回来。
//	backfill 处理的就是最后一类：它是**回退水位**的能力，不是「多跑一次 flush」。
//
// deviceIDs 的语义是「**只回退这些设备的游标**」（空 = 全部活跃设备）；它不改变
// 本轮落库的范围 —— 重放走的就是标准的那一轮写路径（幂等 UPSERT），
// 单设备写路径不在这里另造一条（造了就等于把 5m 的唯一真值来源分叉成两条）。
//
// 限速（spec §7.1「回填必须限速」）：按设备切成每批 defaultBackfillBatchDevices 台，
// 每批之间停顿 defaultBackfillPacing（可被 WithBackfillPacing 覆盖）。切批**不改变**
// 落库范围与语义：分批只是把同一条写路径切成若干段跑，不是「只处理一部分」——
// 每一台被枚举到的设备都在某一批里被重放（device_ids 只影响**回退**，不影响重放）。
// 批数与停顿次数记在 BackfillStats.Batches / PacingWaits 里（可观测，见它们的注释）。
//
// 安全性（为什么回退是幂等且不破坏既有数据）：
//   - 5m 行按 (device_id, bucket_ts) UPSERT，重放同一桶覆盖同一行；
//   - 资源的 `last_seen_at` 用**桶时间**而不是 now 写（见 writeBucket），
//     故重放一个旧桶写回的是它当年那个观测时刻，不会把「早就卸掉的挂载点」
//     刷成刚刚还在；
//   - 回退**绝不上移**水位：目标起点比当前游标更新时直接跳过（上移会永久跳过
//     中间那段桶，那是 5m 唯一真值的一部分）。
//
// 已知边界（写清而不是假装没有）：1h 行是可推导数据，由 rollup 按 `cursor_1h`
// 消费；重放 5m 不会自动重算**已经被 rollup 消费过的**小时（那要走 rollup 的
// repair 集合）。故回溯窗口应与运维想修的范围一致，且 1h 的修正另有 repair 路径。
func (s *AgentMetricsFlushService) BackfillOnce(ctx context.Context, deviceIDs []uint64, hours int) (BackfillStats, error) {
	stats := BackfillStats{WindowHours: clampBackfillHours(hours)}

	devices, err := s.raw.Index(ctx)
	if err != nil {
		return stats, fmt.Errorf("agentmetrics backfill: 枚举活跃设备失败: %w", err)
	}
	stats.DevicesScanned = len(devices)

	scope := deviceScope(deviceIDs)
	target := s.backfillTarget(stats.WindowHours)
	upper := s.closedBucketUpper()

	// 分批 + 批间节奏（spec §7.1「回填必须限速」）：满窗口回填是「288 桶/设备 × N 台」
	// 的读+写放大，一次性压上去会与实时 flush 争抢 DB 连接与缓冲池。这里按设备切成
	// 定长批，每批之间**停顿**一个可注入的间隔（默认见 defaultBackfillPacing）。
	//
	// 每个批次 = 「先回退该批的水位（只回退被点名的设备）→ 再按标准写路径重放该批」，
	// 于是节奏同时作用在两段上：Redis 与 DB 都不会出现一次性的突发。
	//
	// 为什么以「设备」而不是「桶」为批边界：桶是每台设备各自 288 个的**顺序**循环
	// （flushDevice 内部），在它中间插停顿等于把单台设备的区间拖长到跨过好几个闭桶边界
	// ——水位与上界的关系会变得依赖停顿次数，正确性推理直接崩掉。设备之间本来就是
	// 相互独立的（各自的事务、各自的游标），切在这里既限速又不改变任何语义。
	batches := splitDevices(devices, s.backfillBatchDevices)
	stats.Batches = len(batches)

	// flushErr 累积「本批重放失败」的哨兵（nil = 全成功）。具体计数在 stats.Flush.Errors 里；
	// 这里只保留「有没有失败」这一个事实，用于收尾时决定要不要上抛 ErrFlushPartial。
	var flushErr error

	for i, batch := range batches {
		if i > 0 {
			// 批间停顿：用可注入的 sleeper（测试注入观测器，不真 sleep）。
			s.sleep(s.pacing)
			stats.PacingWaits++
		}

		// 1) 本批的定向回退（device_ids 之外的设备不回退，但它们照样参与重放）。
		for _, deviceID := range batch {
			if scope != nil && !scope[deviceID] {
				continue
			}
			cursor, cerr := s.readCursor(ctx, deviceID)
			if cerr != nil {
				// 读失败即上抛：静默跳过会让这台设备的水位永远不回退，
				// 而回退正是本方法的全部职责（下一轮会从同一个起点重试）。
				return stats, cerr
			}
			if cursor <= target {
				// 已经不比目标更新：**不回退**。注意这里也覆盖了「游标键刚缺失、
				// readCursor 按 Bootstrap 语义初始化」的情形（初始化值就是 target）。
				continue
			}
			// 回退走 Lua 的「只后退」原子写：Go 侧的 `cursor > target` 判定之后、
			// 写之前仍可能被并发者改动（另一个 backfill 回退得更深），
			// 只有 Redis 侧的比较才能保证「绝不写下比当前更大的值」。
			applied, werr := s.rewindCursor(ctx, deviceID, target)
			if werr != nil {
				return stats, werr
			}
			if applied {
				stats.CursorsRewound++
			}
		}

		// 2) 本批的标准落库（幂等 UPSERT，写路径与常规 flush 逐字相同）。
		part, perr := s.flushDeviceSet(ctx, batch, upper)
		stats.Flush = stats.Flush.accrue(part)
		if perr != nil {
			if errors.Is(perr, ErrMetricPartitionMissing) {
				// 缺分区：中止整轮（含其余批次）——故障域是整个集群的写入。
				return stats, perr
			}
			flushErr = perr
		}
	}

	if stats.CursorsRewound > 0 {
		s.log.Info("agentmetrics backfill: 5m 水位已回退，本轮将重放该窗口",
			zap.Int("cursorsRewound", stats.CursorsRewound),
			zap.Int("devicesScanned", stats.DevicesScanned),
			zap.Int("windowHours", stats.WindowHours),
			zap.Int64("targetBucket", target))
	}

	// 某批失败不阻断其余批次（与 FlushOnce 跨设备的取向一致：失败设备的水位留在原处，
	// 下轮重试同一批桶），但汇总成一个哨兵上抛给任务层。
	if flushErr != nil {
		return stats, fmt.Errorf("%w: %d/%d 台设备重放失败（水位未推进，下轮重试）",
			ErrFlushPartial, stats.Flush.Errors, stats.Flush.DevicesScanned)
	}
	return stats, nil
}

// splitDevices 把设备列表切成每批至多 n 台的批次（n <= 0 时整批一批）。
//
// 切批不改顺序：设备顺序来自 Redis 活跃索引（SADD 的写入顺序），保持它让
// 「同一批设备」在不同轮次里尽量稳定 —— 便于把「某一批特别慢」与设备对应起来。
func splitDevices(devices []uint64, n int) [][]uint64 {
	if n <= 0 || len(devices) == 0 {
		if len(devices) == 0 {
			return nil
		}
		return [][]uint64{devices}
	}
	out := make([][]uint64, 0, (len(devices)+n-1)/n)
	for start := 0; start < len(devices); start += n {
		end := start + n
		if end > len(devices) {
			end = len(devices)
		}
		out = append(out, devices[start:end])
	}
	return out
}

// clampBackfillHours 把入参窗口夹到 [1, 保留期]；<=0 视为「用满保留期」。
//
// 不在任务侧夹取：这些边界的依据是 **Redis 里 raw 点的保留期**（一个存储事实），
// 而不是任务参数校验（一个输入事实），放大到任务里会让「为什么是 24」分裂成两处。
func clampBackfillHours(hours int) int {
	if hours <= 0 {
		return defaultBackfillHours
	}
	if hours > maxBackfillHours {
		return maxBackfillHours
	}
	return hours
}

// backfillTarget 返回回溯起点：`now−hours` 对齐到 5min 栅格，并夹到 0 下界。
//
// 对齐不能省：flush 的区间是从 `cursor+300` 起步的，起点若落在半个桶上，
// 第一个桶就是错的（少半桶数据）且错得静默。夹 0 的理由同 bootstrapStart
// （负水位会被真的写进 Redis，且与「从 0 起步」语义无法区分）。
func (s *AgentMetricsFlushService) backfillTarget(hours int) int64 {
	target := alignDown(s.now().Add(-time.Duration(hours)*time.Hour).Unix(),
		resolutionSeconds(agentmetrics.Resolution5m))
	if target < 0 {
		return 0
	}
	return target
}

// deviceScope 把设备号列表翻成集合；空列表返回 nil（= 不限定范围）。
func deviceScope(deviceIDs []uint64) map[uint64]bool {
	if len(deviceIDs) == 0 {
		return nil
	}
	out := make(map[uint64]bool, len(deviceIDs))
	for _, id := range deviceIDs {
		out[id] = true
	}
	return out
}
