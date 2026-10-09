package request

import "github.com/tangwy-t/UniCenter/uni_core/internal/pkg/app"

// DeviceQuery 是设备列表查询参数。
// 沿用既有约定：仓储直接消费 request DTO（见 repository/notice.go 的 FindPage）。
type DeviceQuery struct {
	app.PageRequest

	// Hostname 模糊匹配（LIKE %v%）。
	Hostname string `form:"hostname"`
	// Status 精确匹配启停态；nil = 不过滤。
	Status *int8 `form:"status"`
	// Online 在线过滤；nil = 不过滤。在线判定由 service 依
	// sys.agent.offlineThreshold 折算成 onlineSince 后交给仓储。
	Online *bool `form:"online"`
	// PrimaryIp 模糊匹配（LIKE %v%）：定位某台设备时按 IP 前缀/片段搜。
	PrimaryIp string `form:"primaryIp"`
	// AgentVersion **精确**匹配（版本是精确串，0.6 与 0.6.0 是不同版本；
	// 模糊匹配会把两者混进同一结果，反而丢掉「找同一版本的一批设备」）。
	AgentVersion string `form:"agentVersion"`
	// SortBy 排序列，仓储白名单收敛（lastSeenAt 默认 / hostname / agentVersion）。
	// 水位列（CPU/内存/磁盘）是 Redis 组装、非 DB 列，**不可** SQL 排序。
	// 非法值在仓储层静默回落默认列（不 400，避免旧客户端断）。
	SortBy string `form:"sortBy"`
	// SortOrder 仅 asc / desc（其它值回落 desc）；与 SortBy 的默认组合
	// = lastSeenAt desc，与引入排序前的硬编码 `last_seen_at DESC, id DESC`
	// 等价 —— 不传排序参数时行为不变。
	SortOrder string `form:"sortOrder"`
}
