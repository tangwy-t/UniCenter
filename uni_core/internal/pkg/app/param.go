package app

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
)

// Uint64Param 解析路径参数为 uint64 并在失败时写 400 响应。
// 40 处 handler 样板收敛:
//
//	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
//	if err != nil {
//	    app.Error(c, apperror.BadRequest("无效的 ID"))
//	    return
//	}
//
// 收敛为:
//
//	id, ok := app.Uint64Param(c, "id")
//	if !ok {
//	    return
//	}
//
// 失败信息为 "无效的参数 <name>: <raw>" — 参数名 + 实际收到的值,
// 客户端可直接定位发送了什么。调用方短路返回即可,响应已写好。
func Uint64Param(c *gin.Context, name string) (uint64, bool) {
	raw := c.Param(name)
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		Error(c, apperror.BadRequest("无效的参数 "+name+": "+raw))
		return 0, false
	}
	return id, true
}
