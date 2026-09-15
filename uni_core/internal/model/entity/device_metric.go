package entity

// 6 张指标表的表名。**这 6 张表不进 MigrateAll**（AutoMigrate 只能建普通表，
// 而 PG 不允许把普通表转成分区表），由迁移 v008 注册的 pre-AutoMigrate 钩子
// 以分区形态建表 —— 见 internal/pkg/migration/migration.go 的 RegisterPreMigrate。
const (
	TableNameMetric5m     = "device_metric_5m"
	TableNameMetric1h     = "device_metric_1h"
	TableNameMetricDisk   = "device_metric_disk"
	TableNameMetricDiskIO = "device_metric_diskio"
	TableNameMetricNIC    = "device_metric_nic"
	TableNameMetricSensor = "device_metric_sensor"
)

// DeviceMetricWide 是**整机宽表**的一行。
//
// _5m 与 _1h 列集完全相同，故只有一个结构体（一套结构两处表名）：
// TableName() 返回 _5m，仓储用 db.Table(TableNameMetric1h) 切到 1h。
// 1h 行里 5min-only 的列（tcp_time_wait/tcp_close_wait/agent_*）按约定置 NULL。
//
// 可空列一律用指针：nil = 未采集，绝不与 0 混淆。
type DeviceMetricWide struct {
	DeviceID uint64 `gorm:"column:device_id;primaryKey;autoIncrement:false" json:"deviceId,string"`
	BucketTS int64  `gorm:"column:bucket_ts;primaryKey;autoIncrement:false" json:"bucketTs"`

	CPUUsedPercent  *float64 `gorm:"column:cpu_used_percent"  json:"cpuUsedPercent,omitempty"`
	CPUIOWait       *float64 `gorm:"column:cpu_iowait"        json:"cpuIowait,omitempty"`
	Load1           *float64 `gorm:"column:load1"             json:"load1,omitempty"`
	Load5           *float64 `gorm:"column:load5"             json:"load5,omitempty"`
	Load15          *float64 `gorm:"column:load15"            json:"load15,omitempty"`
	MemUsedPercent  *float64 `gorm:"column:mem_used_percent"  json:"memUsedPercent,omitempty"`
	MemUsedMB       *float64 `gorm:"column:mem_used_mb"       json:"memUsedMb,omitempty"`
	MemAvailableMB  *float64 `gorm:"column:mem_available_mb"  json:"memAvailableMb,omitempty"`
	SwapUsedPercent *float64 `gorm:"column:swap_used_percent" json:"swapUsedPercent,omitempty"`
	SwapUsedMB      *float64 `gorm:"column:swap_used_mb"      json:"swapUsedMb,omitempty"`

	TCPTotal       *int64 `gorm:"column:tcp_total"       json:"tcpTotal,omitempty"`
	TCPEstablished *int64 `gorm:"column:tcp_established" json:"tcpEstablished,omitempty"`
	TCPListen      *int64 `gorm:"column:tcp_listen"      json:"tcpListen,omitempty"`
	TCPTimeWait    *int64 `gorm:"column:tcp_time_wait"   json:"tcpTimeWait,omitempty"`
	TCPCloseWait   *int64 `gorm:"column:tcp_close_wait"  json:"tcpCloseWait,omitempty"`
	UDPTotal       *int64 `gorm:"column:udp_total"       json:"udpTotal,omitempty"`
	ProcCount      *int64 `gorm:"column:proc_count"      json:"procCount,omitempty"`
	UptimeSec      *int64 `gorm:"column:uptime_sec"      json:"uptimeSec,omitempty"`

	// Samples 是桶内样本数，1h 加权回滚与「完整度信号」都依赖它，故非空。
	Samples int `gorm:"column:samples;not null" json:"samples"`

	// 整机合计（由同桶子表明细聚合而来）。
	DiskTotalGB         *float64 `gorm:"column:disk_total_gb"            json:"diskTotalGb,omitempty"`
	DiskUsedGB          *float64 `gorm:"column:disk_used_gb"             json:"diskUsedGb,omitempty"`
	DiskUsedPercent     *float64 `gorm:"column:disk_used_percent"        json:"diskUsedPercent,omitempty"`
	DiskIOReadBytesSec  *float64 `gorm:"column:disk_io_read_bytes_sec"   json:"diskIoReadBytesSec,omitempty"`
	DiskIOWriteBytesSec *float64 `gorm:"column:disk_io_write_bytes_sec"  json:"diskIoWriteBytesSec,omitempty"`
	DiskIOReadOpsSec    *float64 `gorm:"column:disk_io_read_ops_sec"     json:"diskIoReadOpsSec,omitempty"`
	DiskIOWriteOpsSec   *float64 `gorm:"column:disk_io_write_ops_sec"    json:"diskIoWriteOpsSec,omitempty"`
	NICRXBytesSec       *float64 `gorm:"column:nic_rx_bytes_sec"         json:"nicRxBytesSec,omitempty"`
	NICTXBytesSec       *float64 `gorm:"column:nic_tx_bytes_sec"         json:"nicTxBytesSec,omitempty"`
	NICRXPacketsSec     *float64 `gorm:"column:nic_rx_packets_sec"       json:"nicRxPacketsSec,omitempty"`
	NICTXPacketsSec     *float64 `gorm:"column:nic_tx_packets_sec"       json:"nicTxPacketsSec,omitempty"`
	NICRXErrorsSec      *float64 `gorm:"column:nic_rx_errors_sec"        json:"nicRxErrorsSec,omitempty"`
	NICTXErrorsSec      *float64 `gorm:"column:nic_tx_errors_sec"        json:"nicTxErrorsSec,omitempty"`
	NICRXDroppedSec     *float64 `gorm:"column:nic_rx_dropped_sec"       json:"nicRxDroppedSec,omitempty"`
	MaxTemperatureC     *float64 `gorm:"column:max_temperature_c"        json:"maxTemperatureC,omitempty"`

	// agent 自监控（RED）；1h 行这些列为 NULL。
	AgentCollectDurationMs   *float64 `gorm:"column:agent_collect_duration_ms"    json:"agentCollectDurationMs,omitempty"`
	AgentReportSuccessCount  *int64   `gorm:"column:agent_report_success_count"   json:"agentReportSuccessCount,omitempty"`
	AgentReportErrorCount    *int64   `gorm:"column:agent_report_error_count"     json:"agentReportErrorCount,omitempty"`
	AgentWSReconnectCount    *int64   `gorm:"column:agent_ws_reconnect_count"     json:"agentWsReconnectCount,omitempty"`
	AgentLastReportError     *string  `gorm:"column:agent_last_report_error;size:1024" json:"agentLastReportError,omitempty"`
	AgentMemResidentMB       *float64 `gorm:"column:agent_mem_resident_mb"        json:"agentMemResidentMb,omitempty"`
	AgentPendingBacklog      *int64   `gorm:"column:agent_pending_backlog"        json:"agentPendingBacklog,omitempty"`
	AgentLastReportLatencyMs *float64 `gorm:"column:agent_last_report_latency_ms" json:"agentLastReportLatencyMs,omitempty"`
	AgentReportDropCount     *int64   `gorm:"column:agent_report_drop_count"      json:"agentReportDropCount,omitempty"`
	AgentUptimeSec           *int64   `gorm:"column:agent_uptime_sec"             json:"agentUptimeSec,omitempty"`
}

func (DeviceMetricWide) TableName() string { return TableNameMetric5m }

// DeviceMetricDisk 是每挂载点的磁盘使用明细（仅 5min 档）。
type DeviceMetricDisk struct {
	ResourceID        uint64   `gorm:"column:resource_id;primaryKey;autoIncrement:false" json:"resourceId,string"`
	BucketTS          int64    `gorm:"column:bucket_ts;primaryKey;autoIncrement:false"   json:"bucketTs"`
	UsedPercent       *float64 `gorm:"column:used_percent"        json:"usedPercent,omitempty"`
	UsedGB            *float64 `gorm:"column:used_gb"             json:"usedGb,omitempty"`
	TotalGB           *float64 `gorm:"column:total_gb"            json:"totalGb,omitempty"`
	InodesUsedPercent *float64 `gorm:"column:inodes_used_percent" json:"inodesUsedPercent,omitempty"`
}

func (DeviceMetricDisk) TableName() string { return TableNameMetricDisk }

// DeviceMetricDiskIO 是每块设备的 IO 速率明细（仅 5min 档）。
type DeviceMetricDiskIO struct {
	ResourceID       uint64   `gorm:"column:resource_id;primaryKey;autoIncrement:false" json:"resourceId,string"`
	BucketTS         int64    `gorm:"column:bucket_ts;primaryKey;autoIncrement:false"   json:"bucketTs"`
	ReadBytesPerSec  *float64 `gorm:"column:read_bytes_per_sec"  json:"readBytesPerSec,omitempty"`
	WriteBytesPerSec *float64 `gorm:"column:write_bytes_per_sec" json:"writeBytesPerSec,omitempty"`
	ReadOpsPerSec    *float64 `gorm:"column:read_ops_per_sec"    json:"readOpsPerSec,omitempty"`
	WriteOpsPerSec   *float64 `gorm:"column:write_ops_per_sec"   json:"writeOpsPerSec,omitempty"`
	IOTimePercent    *float64 `gorm:"column:io_time_percent"     json:"ioTimePercent,omitempty"`
}

func (DeviceMetricDiskIO) TableName() string { return TableNameMetricDiskIO }

// DeviceMetricNIC 是每张网卡的收发速率明细（仅 5min 档）。
type DeviceMetricNIC struct {
	ResourceID      uint64   `gorm:"column:resource_id;primaryKey;autoIncrement:false" json:"resourceId,string"`
	BucketTS        int64    `gorm:"column:bucket_ts;primaryKey;autoIncrement:false"   json:"bucketTs"`
	RXBytesPerSec   *float64 `gorm:"column:rx_bytes_per_sec"   json:"rxBytesPerSec,omitempty"`
	TXBytesPerSec   *float64 `gorm:"column:tx_bytes_per_sec"   json:"txBytesPerSec,omitempty"`
	RXPacketsPerSec *float64 `gorm:"column:rx_packets_per_sec" json:"rxPacketsPerSec,omitempty"`
	TXPacketsPerSec *float64 `gorm:"column:tx_packets_per_sec" json:"txPacketsPerSec,omitempty"`
	RXErrorsPerSec  *float64 `gorm:"column:rx_errors_per_sec"  json:"rxErrorsPerSec,omitempty"`
	TXErrorsPerSec  *float64 `gorm:"column:tx_errors_per_sec"  json:"txErrorsPerSec,omitempty"`
	RXDroppedPerSec *float64 `gorm:"column:rx_dropped_per_sec" json:"rxDroppedPerSec,omitempty"`
}

func (DeviceMetricNIC) TableName() string { return TableNameMetricNIC }

// DeviceMetricSensor 是每个温度传感器的读数明细（仅 5min 档）。
// temperature_c 为 NULL 表示读不到（允许负值）。
type DeviceMetricSensor struct {
	ResourceID   uint64   `gorm:"column:resource_id;primaryKey;autoIncrement:false" json:"resourceId,string"`
	BucketTS     int64    `gorm:"column:bucket_ts;primaryKey;autoIncrement:false"   json:"bucketTs"`
	TemperatureC *float64 `gorm:"column:temperature_c" json:"temperatureC,omitempty"`
}

func (DeviceMetricSensor) TableName() string { return TableNameMetricSensor }
