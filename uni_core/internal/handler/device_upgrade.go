package handler

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/app"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/contextkeys"
)

// DeviceUpgradeServiceInterface 是升级域对控制台暴露的能力面
// （由 service.DeviceUpgradeService 实现）。
//
// 接口定义在消费方（仓库约定）。发起人从**鉴权上下文**取（此处是用户 ID，
// 服务层解析成用户名快照）——「谁发起的」是服务端观测到的事实，不由请求体传入。
type DeviceUpgradeServiceInterface interface {
	SetDeviceTarget(ctx context.Context, deviceID uint64, version string, pin bool, actorID uint64) (*response.DeviceUpgradeDispatchResp, error)
	ClearDeviceTarget(ctx context.Context, deviceID uint64) error
	Preview(ctx context.Context, req *request.DeviceBatchUpgradeRequest) (*response.DeviceUpgradePreviewResp, error)
	Dispatch(ctx context.Context, req *request.DeviceBatchUpgradeRequest, source string, actorID uint64) (*response.DeviceUpgradeDispatchResp, error)
	SetGlobalTarget(ctx context.Context, version string, actorID uint64) (*response.DeviceUpgradeGlobalResp, error)
	Summary(ctx context.Context) (*response.DeviceUpgradeSummaryResp, error)
	TaskList(ctx context.Context, q *request.AgentUpgradeTaskQuery) (*app.PageResponse, error)
	TaskDetail(ctx context.Context, taskID uint64, q *request.AgentUpgradeAttemptQuery) (*response.AgentUpgradeTaskDetailResp, error)
	DeviceRecords(ctx context.Context, deviceID uint64, limit int) ([]response.DeviceUpgradeRecord, error)
}

// DeviceUpgradeHandler 暴露 /devices 下的升级命令与 /devices/upgrade-* 查询端点。
type DeviceUpgradeHandler struct {
	svc DeviceUpgradeServiceInterface
}

func NewDeviceUpgradeHandler(svc DeviceUpgradeServiceInterface) *DeviceUpgradeHandler {
	return &DeviceUpgradeHandler{svc: svc}
}

// SetTarget handles POST /api/v1/devices/:id/upgrade — 单台下发（或固定）。
// @Summary      升级 Agent
// @Description  为单台设备设置目标版本并催办；pin=true 表示「固定在当前版本」（不再跟随全站）
// @Tags         设备监控
// @Accept       json
// @Produce      json
// @Param        id    path  uint64                            true  "设备ID"
// @Param        body  body  request.DeviceUpgradeTargetRequest  true  "目标版本"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.DeviceUpgradeDispatchResp}  "已下发"
// @Failure      400  {object}  app.Response  "版本号格式不正确"
// @Failure      404  {object}  app.Response  "设备不存在"
// @Router       /devices/{id}/upgrade [post]
func (h *DeviceUpgradeHandler) SetTarget(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	var req request.DeviceUpgradeTargetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	pin := req.Pin != nil && *req.Pin
	resp, err := h.svc.SetDeviceTarget(c.Request.Context(), id, req.Version, pin, actorID(c))
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// ClearTarget handles DELETE /api/v1/devices/:id/upgrade — 恢复跟随全站。
// @Summary      取消升级目标
// @Description  清空设备级目标版本（恢复跟随全站目标）
// @Tags         设备监控
// @Accept       json
// @Produce      json
// @Param        id  path  uint64  true  "设备ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "已清空"
// @Failure      404  {object}  app.Response  "设备不存在"
// @Router       /devices/{id}/upgrade [delete]
func (h *DeviceUpgradeHandler) ClearTarget(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	if err := h.svc.ClearDeviceTarget(c.Request.Context(), id); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}

// Preview handles POST /api/v1/devices/upgrade/preview — 影响面预演。
// @Summary      升级影响面预演
// @Description  按设备列表或筛选条件预览本次下发会命中多少台、跳过多少台（下发前确认用）
// @Tags         设备监控
// @Accept       json
// @Produce      json
// @Param        body  body  request.DeviceBatchUpgradeRequest  true  "版本与目标集合"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.DeviceUpgradePreviewResp}  "预演结果"
// @Failure      400  {object}  app.Response  "参数错误"
// @Router       /devices/upgrade/preview [post]
func (h *DeviceUpgradeHandler) Preview(c *gin.Context) {
	var req request.DeviceBatchUpgradeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	resp, err := h.svc.Preview(c.Request.Context(), &req)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// Dispatch handles POST /api/v1/devices/upgrade — 批量下发。
// @Summary      批量升级 Agent
// @Description  对选中的设备或筛选结果下发目标版本；按筛选下发必须带 expectedCount（预演给出的命中台数）
// @Tags         设备监控
// @Accept       json
// @Produce      json
// @Param        body  body  request.DeviceBatchUpgradeRequest  true  "版本与目标集合"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.DeviceUpgradeDispatchResp}  "已下发"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      409  {object}  app.Response  "筛选结果已变化，请重新确认"
// @Router       /devices/upgrade [post]
func (h *DeviceUpgradeHandler) Dispatch(c *gin.Context) {
	var req request.DeviceBatchUpgradeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	// source 由**入口**决定（多选 / 按筛选），不接受客户端传：来源是客观事实。
	source := "batch"
	if req.Filter != nil {
		source = "filter"
	}
	resp, err := h.svc.Dispatch(c.Request.Context(), &req, source, actorID(c))
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// SetGlobalTarget handles POST /api/v1/devices/upgrade/global — 全站目标版本。
// @Summary      设置全站目标版本
// @Description  设置或清除全站目标版本（空 = 关闭全站升级）；只影响跟随全站的设备
// @Tags         设备监控
// @Accept       json
// @Produce      json
// @Param        body  body  request.DeviceUpgradeGlobalRequest  true  "目标版本"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.DeviceUpgradeGlobalResp}  "已设置"
// @Failure      400  {object}  app.Response  "版本号格式不正确"
// @Router       /devices/upgrade/global [post]
func (h *DeviceUpgradeHandler) SetGlobalTarget(c *gin.Context) {
	var req request.DeviceUpgradeGlobalRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	resp, err := h.svc.SetGlobalTarget(c.Request.Context(), req.Version, actorID(c))
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// Summary handles GET /api/v1/devices/upgrade/summary — 当前状态视角。
// @Summary      升级状态汇总
// @Description  按生效目标版本分桶统计（已达成/升级中/待升级/失败/已回滚/不支持/无产物）+ 版本分布
// @Tags         设备监控
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.DeviceUpgradeSummaryResp}  "汇总"
// @Router       /devices/upgrade/summary [get]
func (h *DeviceUpgradeHandler) Summary(c *gin.Context) {
	resp, err := h.svc.Summary(c.Request.Context())
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// TaskList handles GET /api/v1/devices/upgrade/tasks — 任务列表。
// @Summary      升级任务列表
// @Description  分页查询升级任务（每行带明细状态分布）
// @Tags         设备监控
// @Accept       json
// @Produce      json
// @Param        page           query  int     false  "页码"        default(1)
// @Param        pageSize       query  int     false  "每页条数"     default(10)
// @Param        targetVersion  query  string  false  "目标版本"
// @Param        source         query  string  false  "来源(manual/batch/filter/global)"
// @Param        running        query  bool    false  "只看未收口"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=app.PageResponse}  "查询成功"
// @Router       /devices/upgrade/tasks [get]
func (h *DeviceUpgradeHandler) TaskList(c *gin.Context) {
	var q request.AgentUpgradeTaskQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	resp, err := h.svc.TaskList(c.Request.Context(), &q)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// TaskDetail handles GET /api/v1/devices/upgrade/tasks/:id — 任务详情（明细逐台）。
// @Summary      升级任务详情
// @Description  任务信息 + 逐台明细（可只看进行中/失败）
// @Tags         设备监控
// @Accept       json
// @Produce      json
// @Param        id       path   uint64  true   "任务ID"
// @Param        page     query  int     false  "页码"     default(1)
// @Param        pageSize query  int     false  "每页条数"  default(20)
// @Param        filter   query  string  false  "筛选(active=进行中 failed=失败)"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.AgentUpgradeTaskDetailResp}  "查询成功"
// @Failure      404  {object}  app.Response  "任务不存在"
// @Router       /devices/upgrade/tasks/{id} [get]
func (h *DeviceUpgradeHandler) TaskDetail(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	var q request.AgentUpgradeAttemptQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	resp, err := h.svc.TaskDetail(c.Request.Context(), id, &q)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// DeviceRecords handles GET /api/v1/devices/:id/upgrade/records — 该设备升级记录。
// @Summary      设备升级记录
// @Description  该设备最近的升级记录（与任务明细同源）
// @Tags         设备监控
// @Accept       json
// @Produce      json
// @Param        id  path  uint64  true  "设备ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=[]response.DeviceUpgradeRecord}  "查询成功"
// @Failure      404  {object}  app.Response  "设备不存在"
// @Router       /devices/{id}/upgrade/records [get]
func (h *DeviceUpgradeHandler) DeviceRecords(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	// 条数固定为最近 20 条：详情页的「升级记录」是给人扫一眼的，
	// 要翻历史去任务页（那里有分页与筛选）。
	records, err := h.svc.DeviceRecords(c.Request.Context(), id, 20)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, records)
}

// actorID 取当前登录用户 ID（未认证时返回 0；调用方不因此报错 ——
// 写操作本来就在鉴权组内，拿不到 ID 只意味着任务里缺一个显示名）。
func actorID(c *gin.Context) uint64 {
	id, _ := contextkeys.UserIDFromCtx(c.Request.Context())
	return id
}
