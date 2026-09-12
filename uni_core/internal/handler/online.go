package handler

import (
	"context"
	"strings"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"

	"github.com/gin-gonic/gin"
)

// OnlineServiceInterface 定义在消费方:在线用户管理的 HTTP 语义。
type OnlineServiceInterface interface {
	List(ctx context.Context, req *request.OnlineUserQuery) (*app.PageResponse, error)
	Kick(ctx context.Context, req *request.KickSessionReq, currentToken string) error
}

// OnlineHandler exposes HTTP handlers for online-user management.
type OnlineHandler struct {
	svc OnlineServiceInterface
}

func NewOnlineHandler(svc OnlineServiceInterface) *OnlineHandler {
	return &OnlineHandler{svc: svc}
}

// List handles GET /api/v1/monitor/online.
// @Summary      在线用户列表
// @Description  聚合当前在线会话(按用户+设备),支持关键字过滤与分页,受 DataScope 约束
// @Tags         在线用户
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        req   query     request.OnlineUserQuery  true  "查询参数"
// @Success      200   {object}  app.Response{data=app.PageResponse}  "查询成功"
// @Failure      500   {object}  app.Response  "服务内部错误"
// @Router       /monitor/online [get]
func (h *OnlineHandler) List(c *gin.Context) {
	var req request.OnlineUserQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	resp, err := h.svc.List(c.Request.Context(), &req)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// Kick handles POST /api/v1/monitor/online/kick.
// @Summary      强制下线
// @Description  强制下线一个在线会话(吊销其访问令牌并注销刷新令牌,禁止踢当前会话)
// @Tags         在线用户
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        req   body      request.KickSessionReq  true  "下线参数"
// @Success      200   {object}  app.Response  "下线成功"
// @Failure      400   {object}  app.Response  "参数错误/不能下线当前会话"
// @Failure      403   {object}  app.Response  "无权操作该用户会话"
// @Failure      404   {object}  app.Response  "会话不存在或已下线"
// @Failure      500   {object}  app.Response  "服务内部错误"
// @Router       /monitor/online/kick [post]
func (h *OnlineHandler) Kick(c *gin.Context) {
	var req request.KickSessionReq
	if err := c.ShouldBindJSON(&req); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	token := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
	if err := h.svc.Kick(c.Request.Context(), &req, token); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}
