package handler

import (
	"context"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"
	"time"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"

	"github.com/gin-gonic/gin"
)

// JobServiceInterface 由 handler/interfaces.go 迁移至此:接口定义在消费方(handler),
// 按需包含本 handler 实际调用的方法集,不再维护中央镜像文件。
type JobServiceInterface interface {
	FindPage(ctx context.Context, query *request.JobQuery) (*app.PageResponse, error)
	FindByID(ctx context.Context, id uint64) (*response.JobResp, error)
	Create(ctx context.Context, req *request.CreateJobReq) (uint64, error)
	Update(ctx context.Context, id uint64, req *request.UpdateJobReq) error
	Delete(ctx context.Context, id uint64) error
	Pause(ctx context.Context, id uint64) error
	Resume(ctx context.Context, id uint64) error
	RunOnce(ctx context.Context, id uint64) error
	FindLogPage(ctx context.Context, query *request.JobLogQuery) (*app.PageResponse, error)
	DeleteLogsBefore(ctx context.Context, before time.Time) error
	GetTargets() []response.JobTargetResp
	GetHealth() *response.JobHealthResp
}

// JobHandler 定时任务管理 HTTP 处理器。
type JobHandler struct {
	svc JobServiceInterface
}

// NewJobHandler 创建 JobHandler 实例。
func NewJobHandler(svc JobServiceInterface) *JobHandler {
	return &JobHandler{svc: svc}
}

// List handles GET /api/v1/jobs — 分页查询任务列表。
// @Summary      定时任务列表
// @Description  分页查询定时任务列表，支持按任务名称、分组、状态筛选
// @Tags         定时任务管理
// @Accept       json
// @Produce      json
// @Param        page      query  int     false  "页码"           default(1)
// @Param        pageSize  query  int     false  "每页条数"       default(10)
// @Param        name      query  string  false  "任务名称(模糊查询)"
// @Param        jobGroup  query  string  false  "任务分组"
// @Param        status    query  int     false  "状态(0=暂停 1=启用)"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=app.PageResponse}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /jobs [get]
func (h *JobHandler) List(c *gin.Context) {
	var query request.JobQuery
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

// GetByID handles GET /api/v1/jobs/:id — 查询单个任务。
// @Summary      定时任务详情
// @Description  根据任务 ID 查询定时任务详细信息
// @Tags         定时任务管理
// @Accept       json
// @Produce      json
// @Param        id   path      uint64  true  "任务ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.JobResp}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "任务不存在"
// @Router       /jobs/{id} [get]
func (h *JobHandler) GetByID(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	resp, err := h.svc.FindByID(c.Request.Context(), id)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// Create handles POST /api/v1/jobs — 创建任务。
// @Summary      创建定时任务
// @Description  创建新的定时任务
// @Tags         定时任务管理
// @Accept       json
// @Produce      json
// @Param        req  body      request.CreateJobReq  true  "创建定时任务请求"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "创建成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /jobs [post]
func (h *JobHandler) Create(c *gin.Context) {
	var req request.CreateJobReq
	if err := c.ShouldBindJSON(&req); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	id, err := h.svc.Create(c.Request.Context(), &req)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, response.IDResp{ID: id})
}

// Update handles PUT /api/v1/jobs/:id — 更新任务。
// @Summary      更新定时任务
// @Description  更新指定定时任务的信息
// @Tags         定时任务管理
// @Accept       json
// @Produce      json
// @Param        id   path      uint64               true  "任务ID"
// @Param        req  body      request.UpdateJobReq  true  "更新定时任务请求"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "更新成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "任务不存在"
// @Router       /jobs/{id} [put]
func (h *JobHandler) Update(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	var req request.UpdateJobReq
	if err := c.ShouldBindJSON(&req); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	if err := h.svc.Update(c.Request.Context(), id, &req); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}

// Delete handles DELETE /api/v1/jobs/:id — 删除任务。
// @Summary      删除定时任务
// @Description  软删除指定定时任务
// @Tags         定时任务管理
// @Accept       json
// @Produce      json
// @Param        id   path      uint64  true  "任务ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "删除成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "任务不存在"
// @Router       /jobs/{id} [delete]
func (h *JobHandler) Delete(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}

// Pause handles POST /api/v1/jobs/:id/pause — 暂停任务。
// @Summary      暂停定时任务
// @Description  暂停指定定时任务
// @Tags         定时任务管理
// @Accept       json
// @Produce      json
// @Param        id   path      uint64  true  "任务ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "操作成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "任务不存在"
// @Router       /jobs/{id}/pause [post]
func (h *JobHandler) Pause(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	if err := h.svc.Pause(c.Request.Context(), id); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}

// Resume handles POST /api/v1/jobs/:id/resume — 恢复任务。
// @Summary      恢复定时任务
// @Description  恢复指定暂停的定时任务
// @Tags         定时任务管理
// @Accept       json
// @Produce      json
// @Param        id   path      uint64  true  "任务ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "操作成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "任务不存在"
// @Router       /jobs/{id}/resume [post]
func (h *JobHandler) Resume(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	if err := h.svc.Resume(c.Request.Context(), id); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}

// RunOnce handles POST /api/v1/jobs/:id/run — 手动立即执行一次。
// @Summary      手动执行一次
// @Description  手动立即触发指定定时任务执行一次
// @Tags         定时任务管理
// @Accept       json
// @Produce      json
// @Param        id   path      uint64  true  "任务ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "执行成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "任务不存在"
// @Router       /jobs/{id}/run [post]
func (h *JobHandler) RunOnce(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	// 手动执行不绑定客户端连接生命周期:客户端断连会取消
	// c.Request.Context(),任务被中途取消且 executor 的 defer Unlock(ctx)
	// 用已取消的 ctx 必然失败,锁滞留至 TTL 自然过期。
	if err := h.svc.RunOnce(context.Background(), id); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}

// GetTargets handles GET /api/v1/jobs/targets — 获取可用任务目标列表。
// @Summary      可用任务目标
// @Description  获取所有可注册为定时任务的调用目标列表
// @Tags         定时任务管理
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=[]string}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /jobs/targets [get]
func (h *JobHandler) GetTargets(c *gin.Context) {
	targets := h.svc.GetTargets()
	app.Success(c, targets)
}

// GetHealth handles GET /api/v1/jobs/health — 获取调度器健康状态。
// @Summary      调度器健康状态
// @Description  获取定时任务调度器的运行健康状态
// @Tags         定时任务管理
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=map[string]interface{}}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /jobs/health [get]
func (h *JobHandler) GetHealth(c *gin.Context) {
	health := h.svc.GetHealth()
	app.Success(c, health)
}

// FindLogs handles GET /api/v1/jobs/logs — 分页查询任务执行日志。
// @Summary      任务执行日志
// @Description  分页查询定时任务执行日志，支持按任务ID、状态、时间范围筛选
// @Tags         定时任务管理
// @Accept       json
// @Produce      json
// @Param        page      query  int     false  "页码"           default(1)
// @Param        pageSize  query  int     false  "每页条数"       default(10)
// @Param        jobId     query  int     false  "任务ID"
// @Param        status    query  int     false  "状态(0=执行中 1=成功 2=失败)"
// @Param        startTime query  string  false  "开始时间(YYYY-MM-DD)"
// @Param        endTime   query  string  false  "结束时间(YYYY-MM-DD)"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=app.PageResponse}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /jobs/logs [get]
func (h *JobHandler) FindLogs(c *gin.Context) {
	var query request.JobLogQuery
	if err := c.ShouldBindQuery(&query); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	resp, err := h.svc.FindLogPage(c.Request.Context(), &query)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// DeleteLogs handles DELETE /api/v1/jobs/logs — 清理指定时间之前的日志。
// @Summary      清理任务日志
// @Description  删除指定时间之前的定时任务执行日志
// @Tags         定时任务管理
// @Accept       json
// @Produce      json
// @Param        before  query     string  true  "截止时间(RFC3339格式)"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "清理成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /jobs/logs [delete]
func (h *JobHandler) DeleteLogs(c *gin.Context) {
	// 从 query 参数获取 before 时间
	beforeStr := c.Query("before")
	if beforeStr == "" {
		app.Error(c, apperror.BadRequest("缺少 before 参数"))
		return
	}
	before, err := time.Parse(time.RFC3339, beforeStr)
	if err != nil {
		app.Error(c, apperror.BadRequest("时间格式错误，请使用 RFC3339 格式"))
		return
	}
	if err := h.svc.DeleteLogsBefore(c.Request.Context(), before); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}
