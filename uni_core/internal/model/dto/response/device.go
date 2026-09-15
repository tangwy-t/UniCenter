package response

// DeviceListItem 是设备列表的一行（含由 Redis latest 拼出的水位）。
//
// **绝不包含** token_hash / agent_token —— 设备响应里不出现任何凭据。
type DeviceListItem struct {
	ID           uint64 `json:"id,string"`
	Hostname     string `json:"hostname"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	AgentVersion string `json:"agentVersion"`
	Status       int8   `json:"status"`
	// Online 由 last_seen_at 与 sys.agent.offlineThreshold 推导（不落库）。
	Online     bool   `json:"online"`
	LastSeenAt *int64 `json:"lastSeenAt,omitempty"` // unix 秒

	// 以下来自 Redis latest（缺值省略 → 前端显示「—」）
	CPUUsedPercent  *float64 `json:"cpuUsedPercent,omitempty"`
	MemUsedPercent  *float64 `json:"memUsedPercent,omitempty"`
	DiskUsedPercent *float64 `json:"diskUsedPercent,omitempty"`
	// WatermarkAt 是水位采样时刻（unix 秒）。
	WatermarkAt *int64 `json:"watermarkAt,omitempty"`
}

// DeviceResp 是设备详情。
type DeviceResp struct {
	DeviceListItem

	Platform    string  `json:"platform,omitempty"`
	PlatformVer string  `json:"platformVer,omitempty"`
	Kernel      string  `json:"kernel,omitempty"`
	CPUModel    string  `json:"cpuModel,omitempty"`
	CPUCores    int     `json:"cpuCores,omitempty"`
	MemTotalMB  float64 `json:"memTotalMb,omitempty"`
	BootTime    int64   `json:"bootTime,omitempty"`
	CreatedAt   int64   `json:"createdAt,omitempty"` // unix 秒
}

// DeviceMetricPoint 是趋势/下钻的一个桶。
//
// 列名与 spec §5.5 一致；**空桶也出现在 buckets 中**（所有列省略），
// 前端靠 t 定位时间、不得按下标推算（buckets 是稀疏的）。
type DeviceMetricPoint struct {
	T int64 `json:"t"` // 桶起始 unix 秒

	CPUUsedPercent  *float64 `json:"cpu_used_percent,omitempty"`
	CPUIOWait       *float64 `json:"cpu_iowait,omitempty"`
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
	NICRXBytesSec       *float64 `json:"nic_rx_bytes_sec,omitempty"`
	NICTXBytesSec       *float64 `json:"nic_tx_bytes_sec,omitempty"`
	MaxTemperatureC     *float64 `json:"max_temperature_c,omitempty"`

	Samples int `json:"samples"`
}

// DeviceMetricsResp 是整机趋势响应（console 图表直接消费）。
type DeviceMetricsResp struct {
	// RangeSeconds 是实际窗口（秒）。前端按它决定轴刻度与 tooltip 粒度。
	RangeSeconds int64 `json:"range_seconds"`
	// ResolutionSeconds 是**实际生效**的桶宽（可能因 4000 上限被升档，如 30/900/7200）。
	// 前端只信本字段，不得按「1h/24h/7d…」按钮名猜粒度。
	ResolutionSeconds int64 `json:"resolution_seconds"`
	// Source 是数据源（redis / db），仅供调试与观测，业务不依赖。
	Source string `json:"source"`
	// AvailableMetrics 是本响应的**可用列集**：用于区分「该列未采集（值为空）」
	// 与「该档位不产该列（列不存在）」。半年视图上的 agent_* / tcp_time_wait
	// 属于后者，前端应据此显示「该档位无此指标」而不是「—」。
	AvailableMetrics []string `json:"available_metrics"`
	// Buckets 稀疏：空桶仍出现但所有列省略。
	Buckets []DeviceMetricPoint `json:"buckets"`
}

// DeviceResourceResp 是单资源下钻的趋势（per 磁盘/网卡/设备/传感器）。
type DeviceResourceResp struct {
	RangeSeconds      int64               `json:"range_seconds"`
	ResolutionSeconds int64               `json:"resolution_seconds"`
	Source            string              `json:"source"`
	ResourceKind      string              `json:"resource_kind"` // disk|disk_io|nic|sensor
	Name              string              `json:"name"`          // 挂载点 / 设备名 / 网卡名 / 传感器名
	AvailableMetrics  []string            `json:"available_metrics"`
	Buckets           []DeviceMetricPoint `json:"buckets"`
}

// DeviceResourceItem 是资源枚举的一项（drill 下拉的数据源）。
type DeviceResourceItem struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	// LastSeenAt 是最近一次被观测到的时间（unix 秒）；Stale 为 true 时前端应标注「已消失」。
	LastSeenAt int64 `json:"lastSeenAt"`
	Stale      bool  `json:"stale"`
}

// DeviceResourcesResp 是资源枚举响应。
type DeviceResourcesResp struct {
	List []DeviceResourceItem `json:"list"`
}
