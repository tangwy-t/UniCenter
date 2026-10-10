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
	// Group 按主机分组**精确**匹配（form 值 = 完整分组名），空 = 不过滤。
	//
	// 语义裁决：**单值精确**，不做「逗号分隔包含任一分组」。理由：分组名本身
	// 允许逗号（自由填写），按逗号拆分会把「一个含逗号的分组名」误当成多值；
	// 且多选可在前端并发多次请求或在客户端按返回结果合并，无需在契约里约定
	// 拆分口径。精确匹配也天然防了「a 命中 ab」的子串误命中。
	//
	// 未分组设备的筛选不在此列（group 空 = 不过滤，无法表达「只要未分组」）：
	// 那需要一个哨兵值，而哨兵会与「真的有个叫这个名字的分组」在命名空间上
	// 相互污染；未分组设备走前端本地过滤即可。
	Group string `form:"group"`
	// SortBy 排序列，仓储白名单收敛（lastSeenAt 默认 / hostname / agentVersion / group）。
	// 水位列（CPU/内存/磁盘）是 Redis 组装、非 DB 列，**不可** SQL 排序。
	// 非法值在仓储层静默回落默认列（不 400，避免旧客户端断）。
	SortBy string `form:"sortBy"`
	// SortOrder 仅 asc / desc（其它值回落 desc）；与 SortBy 的默认组合
	// = lastSeenAt desc，与引入排序前的硬编码 `last_seen_at DESC, id DESC`
	// 等价 —— 不传排序参数时行为不变。
	SortOrder string `form:"sortOrder"`
}

// DeviceGroupUpdate 是 PATCH /devices/:id 的入参：主机分组与标签的**部分更新**。
//
// 两个字段都是指针，用于区分「未提供（不动）」与「提供空串（清空）」：
//   - group / tags 都为 nil → 两者都不改（请求体空对象是合法 no-op）；
//   - 提供 "" → 清空该字段（未分组 / 无标签）；
//   - 提供非空 → 覆盖（先 trim 两端空白）。
//
// 长度由 service 校验（group ≤64、tags ≤255，与列宽一致），超长返回 400
// —— 这是**用户输入校验**，显式报错；与排序白名单「非法值静默回落」的取向
// 不同（排序非法值等价于「不排序」，越界的分组名没有合理的兜底解释）。
type DeviceGroupUpdate struct {
	// Group 是主机分组名（可选）。提供即更新；空串 = 清空（未分组）。长度 ≤64。
	Group *string `json:"group"`
	// Tags 是设备标签，逗号分隔（可选）。提供即更新；空串 = 清空。长度 ≤255。
	Tags *string `json:"tags"`
}
