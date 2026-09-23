package response

// 本文件是**设备总览页**（单页看全部设备 × 各类指标）的线格契约。
//
// 为什么需要一组新 DTO 而不是复用列表 + 趋势接口：
//   - `GET /devices` 的列表项只承接 3 个水位字段（cpu/mem/disk 百分比），
//     而水位里其实已有 9 个字段（含 load1、mem_used_mb、disk_total_gb、
//     disk_used_gb、nic_rx/tx_bytes_sec）。逐台调趋势接口补全，在 N 台设备时
//     是 N 次请求、每次 ~176 KB（实测 range=3600 全列 175901 B）—— 100 台
//     就是 17 MB，且瀑布式往返。
//   - 故总览页需要**一次请求**拿到「N 台设备 × 若干指标」的聚合形状。
//
// 编码形状的几个刻意选择（都是为了让载荷随设备数**线性且系数很小**增长）：
//
//  1. **列式**（`DeviceOverviewSeries.Values` 是一列值，而不是每桶一个对象）。
//     同一份数据，行式（每桶一个 `{t, cpu, mem, …}`）会为每个桶重复 24 个键名；
//     列式只写一次键名 + 一串数字。实测同一 range 下行式 ~176 KB → 列式后
//     主要成本退化为纯数字。
//  2. **时间轴共享**（`DeviceOverviewResp.Axis`）：所有设备、所有指标共用一条
//     `axis`。设备之间桶边界同栅格（SelectTier 决定唯一的 resolution），故共享
//     是**正确**的而不是压缩出来的——但消费方仍必须按值判空，不能按下标推时间。
//  3. **缺值写 null**（`*float64`）：这是全项目最硬的一条口径——「没采集到」与
//     「采集到 0」必须可区分（见 DeviceMetricPoint 的注释）。用 0 填充会让
//     一张「传感器不存在」的温度图看起来像「温度真为 0℃」。
//  4. **每设备独立成败**（`DeviceOverviewItem.Error`）：单台设备的趋势取数失败
//     不该把整页打成 500 —— 总览页的价值恰恰在于「大多数设备是好的」。故失败
//     降级为该设备自己的 `Error`（有数据的水位照常返回），不抛出。

// DeviceOverviewResp 是设备总览的聚合响应。
//
// 一次请求同时给出三样东西（对应总览页的三个信息层级）：
//   - `Summary` —— 页面级概览（总数 / 在线 / 离线 / 停用 / 水位过期）
//   - `Devices[].Watermark*` —— 每台设备的最新快照（快照图表块的数据源）
//   - `Devices[].Series` + `Axis` —— 每台设备各类指标的趋势（趋势图表块的数据源）
type DeviceOverviewResp struct {
	// RangeSeconds 是本次趋势窗口（秒）。
	RangeSeconds int64 `json:"range_seconds"`
	// ResolutionSeconds 是**原始**桶宽（秒，来自选档），非抽样后的步长。
	ResolutionSeconds int64 `json:"resolution_seconds"`
	// Source 是数据来源档位：redis（≤24h，热层）| db（>24h，冷层）。
	Source string `json:"source"`

	// AvailableMetrics 是本次实际可用的**整机宽表列名**（snake_case），
	// 剔除 bucket_ts。消费方据此决定「哪些图表有数据」，而**不得**硬编码列名
	// （与设备详情页同口径：有没有这一列由后端决定）。
	AvailableMetrics []string `json:"available_metrics"`

	// Axis 是所有设备共享的时间轴（桶起始 unix 秒，升序）。
	// 每条 series 的 Values 与 Axis 等长且一一对应。
	Axis []int64 `json:"axis"`

	// AxisStepSeconds 是抽样步长（秒）：1 表示未抽样，>1 表示每隔 N 个原始桶取 1 个。
	AxisStepSeconds int64 `json:"axis_step_seconds"`
	// Downsampled 为真时必须让用户看见（UI 应标注「已抽样」）——
	// 抽样会**丢失尖峰**，静默抽样会让用户以为看到的是全部细节。
	Downsampled bool `json:"downsampled"`

	Summary DeviceOverviewSummary `json:"summary"`

	Devices []DeviceOverviewItem `json:"devices"`

	// Truncated 为真表示设备数超过 MaxDevices，只返回了前 MaxDevices 台。
	Truncated bool `json:"truncated"`
	// MaxDevices 是本次生效的设备数上限（随响应下发，供 UI 说明）。
	MaxDevices int `json:"max_devices"`
	// DeviceTotal 是过滤后命中的设备总数（未截断前）。
	DeviceTotal int `json:"device_total"`
}

// DeviceOverviewSummary 是页面级概览计数。
//
// 全部是**客观计数**，不含任何阈值判断（例如「异常设备数」）。
// 刻意不在这里放阈值派生的数字：阈值属于展示口径，前端已有
// `isCriticalUsage`（≥90%）这一处定义；后端再定义一遍就会出现「UI 说 90、
// 后端按 80 算」的静默矛盾。与 `DeviceResp.OfflineThresholdSec` 的取舍相反
// 是**因为**那里的阈值前端无法自行推导（依赖可热更配置），而这里的 90%
// 是纯展示约定。
type DeviceOverviewSummary struct {
	Total int `json:"total"`
	// Online / Offline 由 last_seen_at 与 sys.agent.offlineThreshold 判定，
	// 与列表页 `online` 字段**同源同口径**（同一次配置读取）。
	Online  int `json:"online"`
	Offline int `json:"offline"`
	// Disabled 是管理侧停用态（与在线状态正交）：停用的设备仍可能在线。
	Disabled int `json:"disabled"`
	// Stale 是有水位、但水位采样时刻已超过离线阈值（数据陈旧）的设备数。
	// 它与 Offline 不等价：Offline 看的是「最后上报时间」，Stale 看的是
	// 「最新水位那条样本的时间」——设备可能在上报心跳却停止上报指标。
	Stale int `json:"stale"`
	// OfflineThresholdSec 是本次判定实际使用的离线阈值（秒），随响应下发，
	// 保证 UI 文案与 `Online` 的判定永远同口径（同 DeviceResp 的理由）。
	OfflineThresholdSec int `json:"offline_threshold_sec"`
}

// DeviceOverviewItem 是总览里的一台设备。
type DeviceOverviewItem struct {
	// ID 用 `json:"id"`（**不带** `,string`）：本字段已经是 string，而 Go 的
	// `,string` 选项对 string 类型字段会**再加一层引号**（把 `2100772…`
	// 编码成 `"\"2100772…\""`）。既有 DeviceListItem 之所以能写 `,string`，
	// 是因为它那边是 uint64（需要转成 JSON 字符串以避免 JS 大整数精度丢失）；
	// 这里已在服务层 strconv.FormatUint 转过一次，再带 `,string` 就是双重编码。
	//
	// 实测症状：页面上设备 ID 显示成 `"2100772873982447616"`（带字面引号），
	// 且勾选设备时下发的 ids 也带引号 → 后端 400。
	ID       string `json:"id"`
	Hostname string `json:"hostname"`
	// Platform / OS 用于行内图标（deviceIcon）与次级标识。
	Platform string `json:"platform,omitempty"`
	OS       string `json:"os,omitempty"`

	Online bool  `json:"online"`
	Status int8  `json:"status"`
	Stale  bool  `json:"stale"`
	// LastSeenAt 是最后上报时刻（unix 秒）；从未上报则不出现。
	LastSeenAt *int64 `json:"lastSeenAt,omitempty"`
	// WatermarkAt 是水位（最新样本）的采样时刻（unix 秒）。
	WatermarkAt *int64 `json:"watermarkAt,omitempty"`

	// Watermark 是**水位全字段**快照（键名即整机宽表列名，如
	// `cpu_used_percent`）——直接复用列名是为了让快照图表与趋势图表
	// 用同一套 METRIC_META 元数据（中文名/单位/分组），不需要第二张映射表。
	//
	// 缺值列**不出现在 map 里**（而不是出现且为 null）：水位本身是
	// `omitempty` 投影，没采集到的列压根不落 Redis。
	Watermark map[string]float64 `json:"watermark,omitempty"`

	// Series 是 `AvailableMetrics` 的按列趋势，顺序与请求的列序一致。
	// 取数失败时为空，并置 `Error`。
	Series []DeviceOverviewSeries `json:"series,omitempty"`

	// Error 是**该设备单独**的趋势取数失败原因（已脱敏，面向用户可读）。
	// 有值时该设备的快照（Watermark）仍然有效——故障被局部化。
	Error string `json:"error,omitempty"`
}

// DeviceOverviewSeries 是一台设备某一列指标的趋势（列式）。
type DeviceOverviewSeries struct {
	// Metric 是整机宽表列名（snake_case），与 `AvailableMetrics` 同集合。
	Metric string `json:"metric"`
	// Values 与 `DeviceOverviewResp.Axis` 等长；`null` = 该桶无数据（≠ 0）。
	Values []*float64 `json:"values"`

	// 以下统计量均由**非空值**算出；无任何非空值时全部不出现（omitempty），
	// 消费方据此显示「无数据」而不是 0。
	//
	// 为什么在后端算：这些是「一列数字的聚合」，设备数 × 列数在上限内
	// （见 MaxDevices），后端算一次比让 N 个客户端各算一次更省，且口径唯一。
	Last *float64 `json:"last,omitempty"`
	Min  *float64 `json:"min,omitempty"`
	Max  *float64 `json:"max,omitempty"`
	Avg  *float64 `json:"avg,omitempty"`
	// Present / Missing 是桶计数：两者之和 = len(Axis)。
	// 它们让「覆盖率」可被直接展示（例如「344 桶中 12 桶无数据」），
	// 而不需要前端再遍历一遍。
	Present int `json:"present"`
	Missing int `json:"missing"`
}
