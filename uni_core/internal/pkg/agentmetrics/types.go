// Package agentmetrics 承载设备指标存储的**纯逻辑**：分区边界与维护计划、
// 原始点/宽行的数据结构，以及后续计划(2B)要用的下采样与回滚。
//
// 边界纪律：本包**不 import model/entity**，与持久化的交接面是普通 Go 结构体，
// 互转由 repository 负责。这样全部逻辑可脱离 GORM/DB 做单测。
package agentmetrics

import "time"

// Granularity 是分区周期粒度。
type Granularity int

const (
	// GranularityWeek 以 UTC 自然周（周一为起点）为分区周期。
	GranularityWeek Granularity = iota
	// GranularityMonth 以 UTC 自然月为分区周期。
	GranularityMonth
)

func (g Granularity) String() string {
	if g == GranularityMonth {
		return "month"
	}
	return "week"
}

// Spec 描述一张指标表的分区策略。
type Spec struct {
	Table       string
	Granularity Granularity
	Retention   time.Duration
}

// Bound 是一个分区的时间区间 [Lower, Upper)，单位 unix 秒。
type Bound struct {
	Name  string
	Lower int64
	Upper int64
}

// Plan 是分区维护计划。现状满足期望时三段均为空（幂等）。
type Plan struct {
	// Create 是缺失、需要新补的分区（按上界升序）。
	Create []Bound
	// Truncate 是过期但**保留边界**的分区（最老的若干；仅清空行，瞬时完成）。
	Truncate []string
	// Drop 是过期且可彻底释放空间的分区。
	Drop []string
}

const (
	// GuardPartitionName 是**永不回收的下界守卫分区**。
	// 必须存在的原因：MySQL 的 RANGE 分区是 if/elseif 链，只有上界没有下界；
	// 一旦回收掉最左分区，比它更老的值不再报 1526 而是**静默落进下一个分区**，
	// 「缺分区=硬失败」这条兜底会在第一次回收后永久失效（spec §7.3）。
	GuardPartitionName = "p_min"
	// SentinelPartitionName 是**永不回收的上界哨兵分区**。
	// 漏建未来分区时插入会退化落进哨兵（分区变粗），而不是直接 ERROR 1526
	// 让 flush 整体失败。
	SentinelPartitionName = "p_max"
)

// TableSpecs 返回 6 张指标表分区策略的**单一来源**。
//
// 保留期取 spec 的默认值；协调器在运行时用 sys.agent.* 配置覆盖（见 2C）。
// 粒度选择的依据是「最坏保留期 = 保留期 + 一个分区周期」：30d 保留若用月分区，
// 最坏会留 62 天（行数 2.07×），故 30d 的表用周分区。
func TableSpecs() []Spec {
	const day = 24 * time.Hour
	return []Spec{
		{Table: "device_metric_5m", Granularity: GranularityWeek, Retention: 30 * day},
		{Table: "device_metric_1h", Granularity: GranularityMonth, Retention: 180 * day},
		{Table: "device_metric_disk", Granularity: GranularityWeek, Retention: 30 * day},
		{Table: "device_metric_diskio", Granularity: GranularityWeek, Retention: 30 * day},
		{Table: "device_metric_nic", Granularity: GranularityWeek, Retention: 30 * day},
		{Table: "device_metric_sensor", Granularity: GranularityWeek, Retention: 30 * day},
	}
}
