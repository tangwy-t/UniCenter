package handler

import (
	"context"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"time"

	"github.com/gin-gonic/gin"
)

// OperationLogServiceInterface 由 handler/interfaces.go 迁移至此:接口定义在消费方(handler),
// 按需包含本 handler 实际调用的方法集,不再维护中央镜像文件。
type OperationLogServiceInterface interface {
	FindPage(ctx context.Context, query *request.OperationLogQuery) (*app.PageResponse, error)
	DeleteBefore(ctx context.Context, before time.Time) (int64, error)
}

// OperationLogHandler exposes HTTP handlers for operation log endpoints.
type OperationLogHandler struct {
	svc OperationLogServiceInterface
}

// NewOperationLogHandler creates a new OperationLogHandler.
func NewOperationLogHandler(svc OperationLogServiceInterface) *OperationLogHandler {
	return &OperationLogHandler{svc: svc}
}

// FindPage handles GET /api/v1/logs/operation — paginated operation log listing.
// @Summary      操作日志列表
// @Description  分页查询操作日志，支持按用户名、模块、操作类型、结果码、时间范围筛选
// @Tags         操作日志
// @Accept       json
// @Produce      json
// @Param        page          query     int     false  "页码"           default(1)
// @Param        pageSize      query     int     false  "每页条数"       default(10)
// @Param        username      query     string  false  "用户名(模糊查询)"
// @Param        module        query     string  false  "操作模块"
// @Param        operationType query     string  false  "操作类型(新增/修改/删除)"
// @Param        code          query     int     false  "结果码(0=成功,10001=未登录,10002=无权限,40000=参数错误,40400=资源不存在,40900=操作冲突,50000=服务器内部错误) 完整取值见字典 sys_opt_result_code"
// @Param        startTime     query     string  false  "开始时间(YYYY-MM-DD)"
// @Param        endTime       query     string  false  "结束时间(YYYY-MM-DD)"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=app.PageResponse}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /logs/operation [get]
func (h *OperationLogHandler) FindPage(c *gin.Context) {
	var query request.OperationLogQuery
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

// DeleteBefore handles DELETE /api/v1/logs/operation — clean operation logs before a given date.
// @Summary      清理操作日志
// @Description  删除指定日期之前的操作日志记录
// @Tags         操作日志
// @Accept       json
// @Produce      json
// @Param        before  query     string  true  "截止日期(YYYY-MM-DD)"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=map[string]int64}  "清理成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /logs/operation [delete]
func (h *OperationLogHandler) DeleteBefore(c *gin.Context) {
	var req request.DeleteOperationLogReq
	if err := c.ShouldBindQuery(&req); err != nil {
		app.Error(c, apperror.BadRequest("参数错误，请提供 before=YYYY-MM-DD"))
		return
	}
	count, err := h.svc.DeleteBefore(c.Request.Context(), req.Before)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, gin.H{"deleted": count})
}
