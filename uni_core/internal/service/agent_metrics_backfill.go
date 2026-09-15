package service

import (
	"context"
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

// BackfillOnce 跑一轮「带显式回溯窗口的补齐重放」：把落在窗口内的 5m 水位
// **回退**到窗口起点，再跑一轮全量 flush，于是那些已经越过、但当时 Redis 里
// 没有数据（或被改写/丢失）的桶被重新读一遍并 UPSERT 回库。
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
// 本轮落库的范围 —— 重放走的就是标准的那一轮 FlushOnce（全量、幂等 UPSERT），
// 单设备写路径不在这里另造一条（造了就等于把 5m 的唯一真值来源分叉成两条）。
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

	for _, deviceID := range devices {
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
	if stats.CursorsRewound > 0 {
		s.log.Info("agentmetrics backfill: 5m 水位已回退，本轮将重放该窗口",
			zap.Int("cursorsRewound", stats.CursorsRewound),
			zap.Int("devicesScanned", stats.DevicesScanned),
			zap.Int("windowHours", stats.WindowHours),
			zap.Int64("targetBucket", target))
	}

	// 回退之后立刻跑一轮标准落库：它是幂等 UPSERT，失败语义与水位的处理
	// 与常规 flush 逐字相同（失败设备的水位留在原处、下轮重试同一批桶）。
	stats.Flush, err = s.FlushOnce(ctx)
	return stats, err
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
