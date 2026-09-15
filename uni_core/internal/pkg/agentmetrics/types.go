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

// TrendPoint 是**宽表趋势点**：热层（Redis 原始）与冷层（DB 5min/1h）产出的查询结果
// 必须形状一致，前端才能无感切换数据源。字段与 spec §5.5 的 MetricsSample 同名，
// 但语义是「桶内聚合结果」而非单次采样。
//
// 可空列一律指针：nil = 该桶未采集到该指标（图表显示「—」），与 0 区分。
//
// 少数字段带 `gorm:"column:..."`：TrendPoint 现在是仓储的**扫描目标**
// （repository.ReadTrendPoints 直接 Find 到它）。GORM 的 snake_case 推导对
// 「连续大写缩写」会漏下划线（实测：NICRXBytesSec→nicrx_bytes_sec、
// NICTXBytesSec→nictx_bytes_sec、CPUIOWait→cpu_io_wait），而映射失败时 GORM
// **不报错**，字段只是静默留 nil → 曲线永远为空。故这三列显式给出列名；
// 全字段守卫见 repository.TestTrendPointFieldsMatchMetricColumns
// （tag 只是字符串，本包仍不 import gorm）。
type TrendPoint struct {
	T int64 `json:"t"` // 桶起始 unix 秒

	CPUUsedPercent  *float64 `json:"cpu_used_percent,omitempty"`
	CPUIOWait       *float64 `gorm:"column:cpu_iowait" json:"cpu_iowait,omitempty"`
	Load1           *float64 `json:"load1,omitempty"`
	Load5           *float64 `json:"load5,omitempty"`
	Load15          *float64 `json:"load15,omitempty"`
	MemUsedPercent  *float64 `json:"mem_used_percent,omitempty"`
	MemUsedMB       *float64 `json:"mem_used_mb,omitempty"`
	MemAvailableMB  *float64 `json:"mem_available_mb,omitempty"`
	SwapUsedPercent *float64 `json:"swap_used_percent,omitempty"`
	SwapUsedMB      *float64 `json:"swap_used_mb,omitempty"`

	TCPTotal       *int64 `json:"tcp_total,omitempty"`
	TCPEstablished *int64 `json:"tcp_established,omitempty"`
	TCPListen      *int64 `json:"tcp_listen,omitempty"`
	ProcCount      *int64 `json:"proc_count,omitempty"`
	UptimeSec      *int64 `json:"uptime_sec,omitempty"`

	DiskTotalGB         *float64 `json:"disk_total_gb,omitempty"`
	DiskUsedGB          *float64 `json:"disk_used_gb,omitempty"`
	DiskUsedPercent     *float64 `json:"disk_used_percent,omitempty"`
	DiskIOReadBytesSec  *float64 `json:"disk_io_read_bytes_sec,omitempty"`
	DiskIOWriteBytesSec *float64 `json:"disk_io_write_bytes_sec,omitempty"`
	NICRXBytesSec       *float64 `gorm:"column:nic_rx_bytes_sec" json:"nic_rx_bytes_sec,omitempty"`
	NICTXBytesSec       *float64 `gorm:"column:nic_tx_bytes_sec" json:"nic_tx_bytes_sec,omitempty"`
	MaxTemperatureC     *float64 `json:"max_temperature_c,omitempty"`

	Samples int `json:"samples"`
}

// RawSnapshot 是热层查询结果。字段名与 spec §8 的响应契约一致。
type RawSnapshot struct {
	WindowSeconds int64        `json:"range_seconds"`
	StepSeconds   int64        `json:"resolution_seconds"`
	Buckets       []TrendPoint `json:"buckets"`
}

// RawOptions 装配每设备原始滚动窗。
type RawOptions struct {
	// Step 是采样节奏，用于 metricshistory 的取点启发式（= agent 的 reportInterval）。
	Step time.Duration
	// MaxPoints 是窗口容量（条数）；24h / Step × 1.2 的余量。
	MaxPoints int64
	// QueryTTL 是查询结果缓存 TTL；<=0 时取 metricshistory 默认 1s。
	QueryTTL time.Duration
}

// Wide 是宽表一行的**纯数据形状**（不含 GORM 语义）。
//
// **json tag 必须与 entity.DeviceMetricWide 逐字一致** —— 两级保障：
//  1. device_metric_convert_test.go 的 TestWideAndEntityFieldsMirror 守卫「字段名/类型逐一对应」；
//  2. TestEntityFromWideRoundTrip 把**每一个字段都设成互不相同的非零值**做 JSON 往返，
//     任何 tag 拼写错误都会让该字段在往返中丢失，被 DeepEqual 抓住。
//     （只设部分字段的往返测试抓不到 tag 错误，故那条测试必须覆盖全字段。）
type Wide struct {
	DeviceID uint64 `json:"deviceId,string"`
	BucketTS int64  `json:"bucketTs"`

	CPUUsedPercent  *float64 `json:"cpuUsedPercent,omitempty"`
	CPUIOWait       *float64 `json:"cpuIowait,omitempty"`
	Load1           *float64 `json:"load1,omitempty"`
	Load5           *float64 `json:"load5,omitempty"`
	Load15          *float64 `json:"load15,omitempty"`
	MemUsedPercent  *float64 `json:"memUsedPercent,omitempty"`
	MemUsedMB       *float64 `json:"memUsedMb,omitempty"`
	MemAvailableMB  *float64 `json:"memAvailableMb,omitempty"`
	SwapUsedPercent *float64 `json:"swapUsedPercent,omitempty"`
	SwapUsedMB      *float64 `json:"swapUsedMb,omitempty"`

	TCPTotal       *int64 `json:"tcpTotal,omitempty"`
	TCPEstablished *int64 `json:"tcpEstablished,omitempty"`
	TCPListen      *int64 `json:"tcpListen,omitempty"`
	TCPTimeWait    *int64 `json:"tcpTimeWait,omitempty"`
	TCPCloseWait   *int64 `json:"tcpCloseWait,omitempty"`
	UDPTotal       *int64 `json:"udpTotal,omitempty"`
	ProcCount      *int64 `json:"procCount,omitempty"`
	UptimeSec      *int64 `json:"uptimeSec,omitempty"`

	Samples int `json:"samples"`

	DiskTotalGB         *float64 `json:"diskTotalGb,omitempty"`
	DiskUsedGB          *float64 `json:"diskUsedGb,omitempty"`
	DiskUsedPercent     *float64 `json:"diskUsedPercent,omitempty"`
	DiskIOReadBytesSec  *float64 `json:"diskIoReadBytesSec,omitempty"`
	DiskIOWriteBytesSec *float64 `json:"diskIoWriteBytesSec,omitempty"`
	DiskIOReadOpsSec    *float64 `json:"diskIoReadOpsSec,omitempty"`
	DiskIOWriteOpsSec   *float64 `json:"diskIoWriteOpsSec,omitempty"`
	NICRXBytesSec       *float64 `json:"nicRxBytesSec,omitempty"`
	NICTXBytesSec       *float64 `json:"nicTxBytesSec,omitempty"`
	NICRXPacketsSec     *float64 `json:"nicRxPacketsSec,omitempty"`
	NICTXPacketsSec     *float64 `json:"nicTxPacketsSec,omitempty"`
	NICRXErrorsSec      *float64 `json:"nicRxErrorsSec,omitempty"`
	NICTXErrorsSec      *float64 `json:"nicTxErrorsSec,omitempty"`
	NICRXDroppedSec     *float64 `json:"nicRxDroppedSec,omitempty"`
	MaxTemperatureC     *float64 `json:"maxTemperatureC,omitempty"`

	AgentCollectDurationMs   *float64 `json:"agentCollectDurationMs,omitempty"`
	AgentReportSuccessCount  *int64   `json:"agentReportSuccessCount,omitempty"`
	AgentReportErrorCount    *int64   `json:"agentReportErrorCount,omitempty"`
	AgentWSReconnectCount    *int64   `json:"agentWsReconnectCount,omitempty"`
	AgentLastReportError     *string  `json:"agentLastReportError,omitempty"`
	AgentMemResidentMB       *float64 `json:"agentMemResidentMb,omitempty"`
	AgentPendingBacklog      *int64   `json:"agentPendingBacklog,omitempty"`
	AgentLastReportLatencyMs *float64 `json:"agentLastReportLatencyMs,omitempty"`
	AgentReportDropCount     *int64   `json:"agentReportDropCount,omitempty"`
	AgentUptimeSec           *int64   `json:"agentUptimeSec,omitempty"`
}

// 资源明细行按 **name** 键（挂载点 / 磁盘设备名 / 网卡名 / 传感器名）——
// 纯逻辑层不认识 resource_id，name→id 的解析由写路径（2C 的 flush）负责。
type SubRowDisk struct {
	Name              string
	FSType            string
	UsedPercent       *float64
	UsedGB            *float64
	TotalGB           *float64
	InodesUsedPercent *float64
}

type SubRowDiskIO struct {
	Name             string
	ReadBytesPerSec  *float64
	WriteBytesPerSec *float64
	ReadOpsPerSec    *float64
	WriteOpsPerSec   *float64
	IOTimePercent    *float64
}

type SubRowNIC struct {
	Name            string
	RXBytesPerSec   *float64
	TXBytesPerSec   *float64
	RXPacketsPerSec *float64
	TXPacketsPerSec *float64
	RXErrorsPerSec  *float64
	TXErrorsPerSec  *float64
	RXDroppedPerSec *float64
}

type SubRowSensor struct {
	Name         string
	TemperatureC *float64
}

// Subs 是 4 张子表的待写行（按 name 键）。
type Subs struct {
	Disks   []SubRowDisk
	DiskIO  []SubRowDiskIO
	NICs    []SubRowNIC
	Sensors []SubRowSensor
}
