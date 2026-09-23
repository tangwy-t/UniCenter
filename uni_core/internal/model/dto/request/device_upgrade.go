package request

import (
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/app"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/util"
)

// AgentUpgradeTaskQuery 是「升级任务」列表的查询参数。
//
// 与设备列表同款：仓储直接消费 request DTO，分页走 app.PageRequest。
type AgentUpgradeTaskQuery struct {
	app.PageRequest

	// TargetVersion 精确匹配目标版本；空 = 不过滤。
	TargetVersion string `form:"targetVersion"`
	// Source 过滤来源（manual/batch/filter/global）；空 = 不过滤。
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

// DeviceUpgradeTargetRequest 是单台下发的请求体（也用于「升级到…」对话框）。
type DeviceUpgradeTargetRequest struct {
	// Version 必须是合法 semver；**允许低于当前版本**（那就是回滚）——
	// 服务端只校验形态，不校验方向：回滚不是错误操作。
	Version string `json:"version" binding:"required"`
	// Pin 为 true 时表示「固定在当前版本」：即使版本与当前相同也**写下设备级目标**，
	// 使它不再跟随全站目标。
	//
	// 为什么必须显式传而不是从「版本 == 当前」推出来：一次普通的升级下发里，
	// 「已经在该版本上」的设备应当**保持跟随全站**（不写设备级目标）—— 否则
	// 一次全站升级会把每台设备都打上设备级目标，「跟随全站」的比例悄悄变成 0，
	// 下一次改全站目标时谁都跟不上。固定是一个**独立意图**，必须由调用方明说。
	Pin *bool `json:"pin"`
}

// DeviceBatchUpgradeRequest 是批量下发：**二选一**给 ids 或 filter。
//
// 两类入口共用同一个请求体而不是两个端点：
//   - ids：多选（控制台勾选的那几台）；
//   - filter：按筛选全量（复用列表页同一套筛选字段），此时**必须**带
//     expectedCount（preview 的命中台数），服务端重新求值后比对 ——
//     不一致说明两次之间设备集合变了，直接 409 让人重新确认，
//     而不是静默地把影响面扩大或缩小。
type DeviceBatchUpgradeRequest struct {
	Version string               `json:"version" binding:"required"`
	IDs     util.JsonUint64Slice `json:"ids"`
	Filter  *DeviceQuery         `json:"filter"`

	// ExpectedCount 只在 filter 路径生效（ids 路径的影响面由 ids 自身确定）。
	ExpectedCount *int64 `json:"expectedCount"`
}

// DeviceUpgradeGlobalRequest 是设置/清除全站目标版本。
//
// Version 为空 = **关闭全站升级**（语义明确，不做「用 null 表示关闭」那种二义写法）。
type DeviceUpgradeGlobalRequest struct {
	Version string `json:"version"`
}

// AgentReleaseUploadRequest 是发布物上传的表单字段（文件本身走 multipart）。
//
// 用 form 标签：它是 multipart/form-data，不走 JSON 体。
type AgentReleaseUploadRequest struct {
	Version string `form:"version" binding:"required"`
	OS      string `form:"os" binding:"required"`
	Arch    string `form:"arch" binding:"required"`
	Notes   string `form:"notes"`
}
