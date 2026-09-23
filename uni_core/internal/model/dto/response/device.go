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

	// PrimaryIP 是**服务端观测到**的 agent 来源 IP（详见 entity.Device.PrimaryIP）。
	//
	// 放在 DeviceResp 而**不**放 DeviceListItem：列表页已有主机名/OS/架构/在线/
	// 启停/水位等列，再加 IP 会挤压操作列；而 IP 的价值场景（定位设备、SSH）
	// 都在详情页发生。列表若需要，后续按需再提列即可。
	//
	// omitempty：未观测到时整个字段不出现（老设备在升级本版本前 enroll 的行、
	// 或测试态未接管 socket）—— 与水位三字段同一约定，前端显示「—」，
	// **不臆造空串**。
	PrimaryIP string `json:"primaryIp,omitempty"`

	// OfflineThresholdSec 是**后端实际生效**的离线判定阈值（秒），来自
	// sys.agent.offlineThreshold（可热更）。
	//
	// 为什么必须由后端给出、而不是前端写死 30：阈值可热更，前端硬编码会在
	// 后端热更后与之**静默矛盾** —— UI 说「30 秒内未上报即离线」，而判定
	// 其实已按 60 秒算。这比不显示阈值更糟：它看起来是个可信的数字。
	// 与 onlineSince 同源下发，保证「响应里的 online」与「UI 文案」永远同口径。
	OfflineThresholdSec int `json:"offlineThresholdSec,omitempty"`
}

// DeviceMetricPoint 是趋势/下钻的一个桶。
//
// 列名与 spec §5.5 一致；`buckets` 是**稀疏的**：空桶**不出现**在 buckets 中
// （§7.1「空桶跳过不写行」），因此 `t` 有洞。前端**禁止按「下标 = 时间」推算**，
// 必须用 `t` 定位（spec §8）。
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

// DeviceResourcePoint 是**下钻**的一个桶。
//
// 为什么下钻不复用 DeviceMetricPoint（D2）：子表的列名与宽表列名**不同名**
// （`used_percent` vs `disk_used_percent`），且 `TrendPoint` 只镜像 spec §5.5 的 25 列，
// 装不下 `inodes_used_percent` / `io_time_percent` / `rx_errors_per_sec` 等 ——
// 硬套会让这些列**静默消失**（实测：下钻返回的桶值列全为 nil）。
// 下钻的可用列**就是子表自己的列**，故值用「列名 → 值」的开放形状：
//
//   - 键与 `available_metrics` 逐字一致（子表的 snake_case 列名）；
//   - 值为 nil 的列**不出现**在 map 里（缺 ≠ 0）；
//   - `t` 是桶起始 unix 秒：`buckets` 稀疏（空桶不产行），必须用 `t` 定位。
type DeviceResourcePoint struct {
	T int64 `json:"t"`
	// Samples 是命中该资源的样本数（热层档 = 该桶内出现该资源的样本条数；
	// 子表**没有** samples 列，DB 档恒为 0 —— 完整度信号只在整机宽表上）。
	Samples int                 `json:"samples"`
	Values  map[string]*float64 `json:"values,omitempty"`
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
	//
	// 取值 = **该表的值列 ∩ agentmetrics.TrendPoint 实际承接的列**（再按档位
	// 减去 5min-only 列）。为什么不直接给「该表全部列」：表里有约 20 列
	// （tcp_time_wait / udp_total / *_ops_sec / agent_* …）没有任何响应字段去装，
	// 把它们声明成「可用」会让消费方继续误读成「agent 挂了」—— 那正是
	// available_metrics 要消除的误读。
	AvailableMetrics []string `json:"available_metrics"`
	// Buckets 稀疏：空桶**不产行**（§7.1），t 有洞；只能用 t 定位，禁止按下标推算。
	Buckets []DeviceMetricPoint `json:"buckets"`
}

// DeviceResourceResp 是单资源下钻的趋势（per 磁盘/网卡/设备/传感器）。
//
// Buckets 的元素类型是 DeviceResourcePoint（**不是** DeviceMetricPoint）：
// 下钻的列随 kind 而变，见 DeviceResourcePoint 的说明（D2）。
type DeviceResourceResp struct {
	RangeSeconds      int64  `json:"range_seconds"`
	ResolutionSeconds int64  `json:"resolution_seconds"`
	Source            string `json:"source"`
	ResourceKind      string `json:"resource_kind"` // disk|disk_io|nic|sensor
	Name              string `json:"name"`          // 挂载点 / 设备名 / 网卡名 / 传感器名
	// AvailableMetrics 是**这次响应真实的列集**（= 子表的列，或按 metrics 过滤后的子集）。
	// 它不含 bucket_ts：桶时间走 `t`，不是 values 里的一个键。
	AvailableMetrics []string              `json:"available_metrics"`
	Buckets          []DeviceResourcePoint `json:"buckets"`
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
