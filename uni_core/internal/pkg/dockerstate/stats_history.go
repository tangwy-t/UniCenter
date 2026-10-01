package dockerstate

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── 容器 stats 历史留存（P2「昨晚为什么慢」的最后一块）────────────────────
//
// 抽屉曲线此前只有打开后的实时流（GET /docker/hosts/:id/cmds/:ref/stats），
// 打开之前的读数无处可看。本存储在**快照 ingest** 时把每容器的 CPU/内存投影
// 进环形序列：30s 快照节奏 × 60 样本 ≈ 30 分钟窗口 —— 不追求长历史（主机级
// 趋势已有设备监控覆盖；spec §2 的「历史趋势价值低」结论对容器粒度同样成立），
// 只补「打开抽屉就能看到刚过去半小时」这一段。
//
// 键设计（spec §4.3.2 键清单之外的新键，与 CmdDeadlineKey 同族要写清理由）：
//
//	docker:stats:history:<deviceID>               SET  —— 按主机一桶：桶成员是
//	                                                  有留存的容器 ID（Purge 的
//	                                                  精确枚举入口）
//	docker:stats:history:<deviceID>:<containerID> LIST —— 桶内按容器一条环形序列
//	                                                  （LPUSH 头部 = 最新样本）
//
// 为什么是「SET 桶 + 每容器一个 LIST」而不是一个大 HASH（field=容器）：
//   - HASH 的 TTL 只能挂在键上（整个主机一份），容器消失后它的 field 会跟着
//     主机一直续命 —— 字段随容器增删无限累积；Redis 7.4 的 HEXPIRE 才有字段级
//     TTL，把部署钉在 7.4+ 不值得。
//   - 每容器独立 LIST 让「序列冻结后 TTL 兜底清理」落在**容器粒度**：容器消失
//     （被删/停）→ 它的键不再被写 → TTL 到期自然消失，别的容器不受影响。
//     LIST 的 LPUSH+LTRIM 还是原生环形（裁剪不必读改写，容量账不靠调用方自觉
//     —— 与 docker:cmd:recent 的容量裁剪同一纪律，那边因为 ZSET 裁剪的负 rank
//     语义坑才用 Lua，LIST 没有这个坑）。
//
// 体积账（60×N 容器的量级，紧凑形态是刻意的）：
//   - 每样本一个 LIST 元素，内容是数组 `[t,cpu,mem,limit]` 而不是逐样本 JSON
//     对象 —— 键名（"cpu_percent" 等 ~30B）在 60 样本里重复 60 遍纯属浪费。
//     实测一条 `[1730000000123,3.14,256.5,512]` ≈ 32–36B；逐对象形态约
//     `{"t":…,"cpu_percent":…,"mem_usage_mb":…,"mem_limit_mb":…}` ≈ 75–85B，
//     同窗口体积约 2.2×。
//   - 每容器：60 样本 ≈ 2.1KB + 键开销（键名 ~50B + LIST 元素头）≈ 2.2KB。
//   - 一台 50 容器的主机 ≈ 110KB；全部可管主机（个位数）合计在数百 KB 量级
//     —— 对 Redis 是零头，但若用逐对象形态 + 网络字段就会翻到 MB 级，故从
//     第一天起就压紧。
//   - **不含网络字段**（net_rx/tx_bytes_sec）：30 分钟窗口的排障叙事里 CPU/
//     内存是主情节（饱和、泄漏、抖动一眼可读）；网络速率是差分值，历史片段
//     单看缺上下文，而两个速率字段会把每样本再抬 ~40%。取舍：实时流里它们
//     照常有（那里带宽不是问题），留存里不留。
const (
	// StatsHistoryKeyPrefix 是留存键族前缀：桶（SET）与序列（LIST）共用，
	// 运维按 `docker:stats:history:<id>` 一眼定位一台主机的全部留存。
	StatsHistoryKeyPrefix = "docker:stats:history:"
	// StatsHistoryKeep 是每容器环形序列的容量（条数上限）。
	//
	// 60 而不是更大：30s 节奏 × 60 = 30 分钟，恰好覆盖「注意到慢 → 打开抽屉」
	// 的排障时差；再长的窗口意味着翻倍的体积，而那个时差之外的需求属于设备
	// 监控的容器粒度趋势（本域明确不做）。
	StatsHistoryKeep = 60
	// StatsHistoryTTL 是留存键的存活窗口：**每写一次续一次**。
	//
	// 取 30 分钟（与窗口同长）而不是更短：容器停了/被删了之后，冻结的序列
	// 还要撑住「它死前发生了什么」的排障窗口 —— 键在最后一次写入后 30 分钟
	// 自然过期，与「数据本身的价值上限」同数量级。主机侧同理：停报后 30 分钟
	// 键消失（届时陈旧度早已把它标成 stale，读面照样离线可看快照）。只要快照
	// 周期（协议下限 10s、常态 30s）远小于 TTL，活跃序列就永不过期。
	StatsHistoryTTL = 30 * time.Minute
)

// StatsSample 是留存序列里的一个读数点。
//
// 字段与快照 DockerContainer / stats 流 DockerStatsSample 的同名字段**同一
// 口径**（单位、round2 舍入一致）：抽屉里「历史曲线、实时曲线、表格读数」
// 三处必须能对上，两套口径会变成永久疑问。
type StatsSample struct {
	// T 是 core **收到**该快照帧的时刻（unix 毫秒）。与 Envelope.ReceivedAt
	// 同源：agent 时钟可能偏（.106 实测快 8 小时），历史的时间轴若用 agent
	// 时钟会画出「来自未来的读数」。代价：与 stats 实时流（agent 时钟的 t）
	// 在时钟偏斜的主机上有绝对刻度差 —— 前端衔接按「历史在前、流在后」拼接，
	// 时序不乱；时钟正常的主机（绝大多数）两条曲线无缝。
	T          int64
	CPUPercent float64
	MemUsageMB float64
	MemLimitMB float64
}

// StatsHistoryStore 读写容器 stats 留存序列。
type StatsHistoryStore struct {
	rdb goredis.UniversalClient
}

// NewStatsHistoryStore 构造留存存储。
func NewStatsHistoryStore(rdb goredis.UniversalClient) *StatsHistoryStore {
	return &StatsHistoryStore{rdb: rdb}
}

// statsBucketKey 返回一台主机的桶键（SET：成员 = 有留存的容器 ID）。
func statsBucketKey(deviceID uint64) string {
	return StatsHistoryKeyPrefix + strconv.FormatUint(deviceID, 10)
}

// StatsRingKey 返回一台主机上一个容器的序列键。
func StatsRingKey(deviceID uint64, containerID string) string {
	return statsBucketKey(deviceID) + ":" + containerID
}

// Record 把一帧快照的每容器读数压进各自的环形序列（快照 ingest 的观察者入口）。
//
// 三条写入纪律：
//   - **只记 running 容器**：非运行容器本就没有读数（Docker 不为已停止容器
//     返回 stats，agent 的 collectStats 也跳过它们，快照里是零值）。把零值
//     写进序列只会给每个停着的容器养一条永久零线，还要每 30s 白写一次；
//     跳过它们恰好让「容器停止」= 序列冻结在最后一次真实读数上 —— 这正是
//     排障要的「死前发生了什么」窗口。
//   - **每帧一写、不做去重节流**：30s 快照节奏天然限频（快照里「读数没变」
//     也是事实，跳过会人为制造时间轴空洞）；容量由 LTRIM 收口，与
//     docker:cmd:recent 的「容量约束必须原子」同一句话 —— 这里整帧收进一条
//     TxPipeline（MULTI/EXEC），帧内要么全落要么全不落。
//   - **桶每帧重建**：桶（Purge 的枚举入口）不随历史容器增删无限增长，纪律
//     与取舍见写入处的内联注释（冻结序列的键不进桶）。
func (s *StatsHistoryStore) Record(ctx context.Context, deviceID uint64, st *agentproto.DockerState, at time.Time) error {
	if st == nil {
		return nil
	}
	bucket := statsBucketKey(deviceID)
	tms := at.UnixMilli()
	pipe := s.rdb.TxPipeline()
	recorded := make([]string, 0, len(st.Containers))
	for i := range st.Containers {
		c := &st.Containers[i]
		if c.State != "running" {
			continue
		}
		key := StatsRingKey(deviceID, c.ID)
		pipe.LPush(ctx, key, encodeStatsSample(StatsSample{
			T: tms, CPUPercent: c.CPUPercent, MemUsageMB: c.MemUsageMB, MemLimitMB: c.MemLimitMB,
		}))
		pipe.LTrim(ctx, key, 0, StatsHistoryKeep-1)
		pipe.Expire(ctx, key, StatsHistoryTTL)
		recorded = append(recorded, c.ID)
	}
	if len(recorded) > 0 {
		// 桶每帧重建（DEL+SADD 本帧在记的容器）：桶只用于 Purge 的精确枚举，
		// 重建让它的成员集恒等于「最近一帧在记的容器」，不随历史容器增删无限
		// 增长；冻结序列的键不进桶，靠自己的 TTL 到期消失 —— Purge 清得掉
		// 活跃的，TTL 兜住冻结的，两条路都是有限的。全停帧不重建（桶留着
		// 上一帧的成员），其 TTL 在 30 分钟无写入后自然收口。
		pipe.Del(ctx, bucket)
		members := make([]any, 0, len(recorded))
		for _, id := range recorded {
			members = append(members, id)
		}
		pipe.SAdd(ctx, bucket, members...)
		pipe.Expire(ctx, bucket, StatsHistoryTTL)
	}
	_, err := pipe.Exec(ctx)
	return err
}

// History 返回一台主机上一个容器的留存序列（按 t **升序**，至多 StatsHistoryKeep 条）。
//
// 无历史（从未记录 / 已被 TTL 清理）返回空切片而非 nil ——「没有历史」是
// 正常答案（刚部署的容器、老 core 升级上来之前），页面显示空曲线而不是错误。
// 单条元素解码失败**跳过该条**（不炸整条序列）：损坏只让曲线缺一个点，
// 把整窗判成错误则是杀了全部数据 —— 与 metricshistory 单条坏数据跳过同款。
func (s *StatsHistoryStore) History(ctx context.Context, deviceID uint64, containerID string) ([]StatsSample, error) {
	out := []StatsSample{}
	if containerID == "" {
		return out, nil
	}
	// LPUSH 头部 = 最新样本：先读头再反转成升序（读尾会拿到最旧的点，
	// 顺序反了前端还得自己翻一遍）。
	raw, err := s.rdb.LRange(ctx, StatsRingKey(deviceID, containerID), 0, StatsHistoryKeep-1).Result()
	if err != nil {
		return nil, err
	}
	for i := len(raw) - 1; i >= 0; i-- {
		if sm, ok := decodeStatsSample(raw[i]); ok {
			out = append(out, sm)
		}
	}
	return out, nil
}

// Purge 是设备删除时的连带清理：桶成员的序列 + 桶本身一次事务删完。
//
// 不做它，删除设备的留存键要等 TTL（≤30 分钟）才消失。冻结序列的键不在桶里
// （见 Record 的桶重建纪律）—— 它们照旧由 TTL 兜底，不在此处枚举（那需要
// SCAN 遍历共享 Redis 的整个键空间，为一次管理操作不值）。
func (s *StatsHistoryStore) Purge(ctx context.Context, deviceID uint64) error {
	bucket := statsBucketKey(deviceID)
	ids, err := s.rdb.SMembers(ctx, bucket).Result()
	if err != nil {
		return err
	}
	pipe := s.rdb.TxPipeline()
	pipe.Del(ctx, bucket)
	for _, cid := range ids {
		pipe.Del(ctx, StatsRingKey(deviceID, cid))
	}
	_, err = pipe.Exec(ctx)
	return err
}

// ── 紧凑编解码 ─────────────────────────────────────────────────────────
//
// 形态：`[t,cpu,mem,limit]`（JSON 数组，无键名无对象壳）。这是 60×N 体积敏感
// 路径上刻意的手工编解码（体积账见键设计处的注释）。

// encodeStatsSample 编码一个样本（strconv 手拼：这是每 30s × N 容器的热路径，
// 走 json.Marshal 的反射一趟要慢一个数量级，而形态只有四个数字）。
func encodeStatsSample(sm StatsSample) string {
	b := make([]byte, 0, 44)
	b = append(b, '[')
	b = strconv.AppendInt(b, sm.T, 10)
	b = append(b, ',')
	b = strconv.AppendFloat(b, sm.CPUPercent, 'f', -1, 64)
	b = append(b, ',')
	b = strconv.AppendFloat(b, sm.MemUsageMB, 'f', -1, 64)
	b = append(b, ',')
	b = strconv.AppendFloat(b, sm.MemLimitMB, 'f', -1, 64)
	b = append(b, ']')
	return string(b)
}

// decodeStatsSample 解码一个样本；形态不对（旧格式残留/损坏）返回 false。
//
// t 走 float64 中转是安全的：unix 毫秒 ≈ 1.7e12，远小于 float64 的整数
// 精确域（2^53 ≈ 9e15），往返无损。
func decodeStatsSample(s string) (StatsSample, bool) {
	var v []float64
	if err := json.Unmarshal([]byte(s), &v); err != nil || len(v) != 4 {
		return StatsSample{}, false
	}
	return StatsSample{T: int64(v[0]), CPUPercent: v[1], MemUsageMB: v[2], MemLimitMB: v[3]}, true
}
