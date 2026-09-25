package request

import "encoding/json"

// DockerCmdReq 是一条 docker 指令的受理请求（形状对齐 spec §4.1）。
//
// target 是顶层字段（与 HTTP 契约一致），而 core 组装协议载荷时把它并进 options ——
// 协议侧 target 属于 options（§4.3.1 总表是 options 归属的唯一事实源）。
type DockerCmdReq struct {
	Action  string               `json:"action" binding:"required"`
	Target  string               `json:"target"`
	Options *DockerCmdOptionsReq `json:"options"`
	// Confirm 是强确认档的确认值（服务端强制校验，前端 MessageBox 只是它的输入界面）。
	Confirm string `json:"confirm"`
}

// DockerCmdOptionsReq 是 options 的**可选参数集**。
//
// 指针用于布尔与数值：`all=false` 与「没传 all」在语义上不同（前者显式要求「只清悬空」，
// 后者用默认值）—— 用值类型会让「显式 false」被默认值悄悄覆盖。
type DockerCmdOptionsReq struct {
	Tail          *int            `json:"tail"`
	Since         *int64          `json:"since"`
	N             *int            `json:"n"`
	Force         *bool           `json:"force"`
	Filename      string          `json:"filename"`
	Overwrite     *bool           `json:"overwrite"`
	All           *bool           `json:"all"`
	RemoveOrphans *bool           `json:"removeOrphans"`
	Volumes       *bool           `json:"volumes"`
	Content       string          `json:"content"`
	BaseHash      string          `json:"baseHash"`
	Src           string          `json:"src"`
	Dst           string          `json:"dst"`
	Patch         json.RawMessage `json:"patch"`
}
