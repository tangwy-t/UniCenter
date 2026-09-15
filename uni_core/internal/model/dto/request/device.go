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
}
