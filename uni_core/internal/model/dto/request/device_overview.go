package request

// DeviceOverviewQuery 是设备总览查询参数。
//
// 设计要点（与 DeviceMetricsQuery 的取舍一致，故逐条对齐）：
//
//   - Range 用**秒**（int64）而非 Go duration 串：避免解析歧义，且与响应里的
//     `range_seconds` 对称。越界（<1h 或 >180d）由 service 返回 400，
//     复用与趋势接口**同一个** SelectTier —— 不在这里另定一套区间，
//     否则总览与详情会对同一个 range 给出不同结论。
//
//   - Metrics 是逗号分隔的列白名单；为空时用该档位的默认列集，`*` 取全量。
//     语义与趋势接口**逐字相同**（复用 resolveTrendColumns），因为总览页
//     要拆成多个指标类别的图表，需要能按类别分别取列。
//
//   - 过滤条件是**页面级**的（一次请求内所有设备用同一套），故直接复用
//     DeviceQuery 的三个过滤字段（hostname/status/online）的形态，
//     而不是每台设备一个参数 —— 总览页的过滤是「筛出哪些设备进图」，
//     不是「每台设备查什么」。
type DeviceOverviewQuery struct {
	Range   int64  `form:"range"`
	Metrics string `form:"metrics"`

	// Hostname 模糊匹配（LIKE %v%）。
	Hostname string `form:"hostname"`
	// Status 精确匹配启停态；nil = 不过滤。语义与列表页逐字一致。
	Status *int8 `form:"status"`
	// Online 在线过滤；nil = 不过滤。在线判定同样由 service 依
	// sys.agent.offlineThreshold 折算成 onlineSince 后交给仓储。
	Online *bool `form:"online"`

	// Ids 是显式设备白名单（逗号分隔的设备 ID）——「只看勾选的这几台」。
	//
	// 为什么需要它而不是让前端把设备列表过滤一遍：总览页的「对比」语义要求
	// 图表里出现的设备集合与用户勾选**完全一致**；若前端先拉全量再本地过滤，
	// 趋势数据（每设备一列）也必须全量拉回来，等于把最贵的那部分白白拉了一遍。
	// 交给后端在 SQL 里筛，代价是 WHERE id IN (...)。
	//
	// 空 = 不限制（由其它过滤器决定）。非法 ID（非数字/为 0）返回 400
	// 而不是静默忽略：静默忽略会让用户以为「筛了但没生效」。
	Ids string `form:"ids"`
}

// OverviewMaxDevicesDefault / OverviewMaxDevicesLimit 是总览一次返回的设备数上限。
//
// 为什么必须有上限：总览的成本随设备数**线性增长**（每台设备一条趋势取数 +
// 一组列式数组）。没有上限时，一台万台规模的部署会让这个接口成为自我 DoS：
// 单请求打满 DB 连接与内存，且响应体大到前端无法解析。
//
// 为什么落在 200 而不是更长：200 台 × 24 列 × 344 桶 ≈ 1.6M 个数值点，
// 列式 JSON 下大致是几 MB 量级——已是「一页图表能真正被读懂」的上限
// （超过这个数，图表里的折线会糊成一片，人眼无法逐条比较，那正是总览页的用途）。
// 超过时**截断并置 truncated**，而不是报错：部分可用好于整页不可用。
const (
	OverviewMaxDevicesDefault = 200
	OverviewMaxDevicesLimit   = 500
)
