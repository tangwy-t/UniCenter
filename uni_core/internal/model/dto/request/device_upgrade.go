package request

import "github.com/tangwy-t/UniCenter/uni_core/internal/pkg/app"

// AgentUpgradeTaskQuery 是「升级任务」列表的查询参数。
//
// 与设备列表同款：仓储直接消费 request DTO，分页走 app.PageRequest。
type AgentUpgradeTaskQuery struct {
	app.PageRequest

	// TargetVersion 精确匹配目标版本；空 = 不过滤。
	TargetVersion string `form:"targetVersion"`
	// Source 过滤来源（manual/batch/filter/global/rollback）；空 = 不过滤。
	Source string `form:"source"`
	// Running 只看还没收口的任务（存在未终结明细）；nil = 不过滤。
	Running *bool `form:"running"`
}

// AgentUpgradeAttemptQuery 是任务明细 / 设备升级记录的查询参数。
type AgentUpgradeAttemptQuery struct {
	app.PageRequest

	// Filter 是明细的预置筛选：空 = 全部；active = 只看进行中（未终结）；
	// failed = 只看失败 / 已回滚 / 超时（三种「需要人看」的终态）。
	//
	// 用受限枚举而不是两个 bool：bool 组合会出现「只能看进行中且失败」这种
	// 自相矛盾的状态，而它没有任何语义。
	Filter string `form:"filter"`
	// DeviceID 只看某台设备的明细（>0 生效）。
	DeviceID uint64 `form:"deviceId"`
}

// 明细筛选取值（与 AgentUpgradeAttemptQuery.Filter 对应）。
const (
	AttemptFilterActive = "active"
	AttemptFilterFailed = "failed"
)
