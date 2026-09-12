package handler

import (
	"context"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"

	"github.com/gin-gonic/gin"
)

// LoginLogServiceInterface 由 handler/interfaces.go 迁移至此:接口定义在消费方(handler),
// 按需包含本 handler 实际调用的方法集,不再维护中央镜像文件。
type LoginLogServiceInterface interface {
	FindPage(ctx context.Context, query *request.LoginLogQuery) (*app.PageResponse, error)
	// RecordLogin/RecordLogout 的消费方是 AuthService(登录/登出埋点),
	// LoginLogHandler 只消费分页查询,按需包含原则不在此声明。
}

// LoginLogHandler exposes HTTP handlers for login log endpoints.
type LoginLogHandler struct {
	svc LoginLogServiceInterface
}

// NewLoginLogHandler creates a new LoginLogHandler.
func NewLoginLogHandler(svc LoginLogServiceInterface) *LoginLogHandler {
	return &LoginLogHandler{svc: svc}
}

// FindPage handles GET /api/v1/logs/login — paginated login log listing.
// @Summary      登录日志列表
// @Description  分页查询登录日志，支持按用户名、IP、结果码、时间范围筛选
// @Tags         登录日志
// @Accept       json
// @Produce      json
// @Param        page      query     int     false  "页码"           default(1)
// @Param        pageSize  query     int     false  "每页条数"       default(10)
// @Param        username  query     string  false  "用户名(模糊查询)"
// @Param        ip        query     string  false  "登录IP"
// @Param        code      query     int     false  "结果码(0=成功,10001=用户名或密码错误,10005=验证码错误,10008=账号已锁定等) 完整取值见字典 sys_opt_result_code"
// @Param        startTime query     string  false  "开始时间(YYYY-MM-DD)"
// @Param        endTime   query     string  false  "结束时间(YYYY-MM-DD)"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=app.PageResponse}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /logs/login [get]
func (h *LoginLogHandler) FindPage(c *gin.Context) {
	var query request.LoginLogQuery
	if err := c.ShouldBindQuery(&query); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	resp, err := h.svc.FindPage(c.Request.Context(), &query)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}
