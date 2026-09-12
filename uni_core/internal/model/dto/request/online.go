package request

import (
	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

// OnlineUserQuery 在线用户列表查询参数(keyword 匹配 用户名/姓名/IP)。
type OnlineUserQuery struct {
	app.PageRequest
	Keyword string `form:"keyword"`
}

// KickSessionReq 强制下线请求:uid 为用户 ID(强制下线该用户全部会话)。
// UserID 用 util.JsonUint64 以兼容前端 snowflake 字符串。
type KickSessionReq struct {
	UserID util.JsonUint64 `json:"uid" binding:"required"`
}
