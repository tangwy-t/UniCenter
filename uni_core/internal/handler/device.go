package handler

import (
	"context"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/app"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
)

// DeviceServiceInterface 是设备**管理**域的消费方接口（由 service.DeviceService 实现）。
// 仓库约定：接口定义在消费方（见 handler/user.go 的 UserServiceInterface）。
type DeviceServiceInterface interface {
	List(ctx context.Context, q *request.DeviceQuery) (*app.PageResponse, error)
	GetByID(ctx context.Context, id uint64) (*response.DeviceResp, error)
	Resources(ctx context.Context, id uint64, kind string) (*response.DeviceResourcesResp, error)
	Enable(ctx context.Context, id uint64) error
	Disable(ctx context.Context, id uint64) error
	Delete(ctx context.Context, id uint64) error
}

// DeviceMetricsServiceInterface 是设备**指标查询**域的消费方接口
// （由 service.AgentMetricsQueryService 实现）。
//
// 为什么拆成两个接口、构造时注入两个依赖，而不是合并成一个大接口：
// 管理域与查询域的实现体不同（前者查 DB 的设备表，后者查热层/指标表），
// 合并后**没有任何一个服务能实现它** —— 那会逼着引入一层纯转发的门面
// （Middle Man），既没带来解耦，又让 wireup 多一层间接。
type DeviceMetricsServiceInterface interface {
	Metrics(ctx context.Context, id uint64, q *request.DeviceMetricsQuery) (*response.DeviceMetricsResp, error)
	ResourceMetrics(ctx context.Context, id uint64, q *request.DeviceMetricsQuery) (*response.DeviceResourceResp, error)
}

// DeviceHandler exposes HTTP handlers for the /devices* endpoints.
type DeviceHandler struct {
	svc     DeviceServiceInterface
	metrics DeviceMetricsServiceInterface
}

// NewDeviceHandler 注入管理域与查询域两个依赖（二者由不同服务实现，不得混用）。
func NewDeviceHandler(svc DeviceServiceInterface, metrics DeviceMetricsServiceInterface) *DeviceHandler {
	return &DeviceHandler{svc: svc, metrics: metrics}
}

// deviceID 解析路径参数 :id（雪花 id 是 uint64，路径上按十进制字符串传）。
func deviceID(c *gin.Context) (uint64, error) {
	raw := c.Param("id")
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		return 0, apperror.BadRequest("设备 ID 非法")
	}
	return id, nil
}

// List GET /devices
func (h *DeviceHandler) List(c *gin.Context) {
	var query request.DeviceQuery
	if err := c.ShouldBindQuery(&query); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	resp, err := h.svc.List(c.Request.Context(), &query)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// GetByID GET /devices/:id
func (h *DeviceHandler) GetByID(c *gin.Context) {
	id, err := deviceID(c)
	if err != nil {
		app.Error(c, err)
		return
	}
	resp, err := h.svc.GetByID(c.Request.Context(), id)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// Metrics GET /devices/:id/metrics?range=&metrics=&kind=&name=
//
// 一个端点两种语义：kind+name 同时存在 → 资源下钻；否则 → 整机趋势。
// 两条分支各自 return，**不做**「先调一次再调一次」的兜底（那是死代码：
// 后一次调用会覆盖前一次的结果，还会白白多打一次 DB/Redis）。
//
// 为什么下钻复用同一端点：挂载点含 "/"、网卡名含 "."/" "，做 path segment
// 需要双重转义，且根挂载点 "/" 会撞上 Gin 的尾斜杠路由（spec §8）。
func (h *DeviceHandler) Metrics(c *gin.Context) {
	id, err := deviceID(c)
	if err != nil {
		app.Error(c, err)
		return
	}
	var q request.DeviceMetricsQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	ctx := c.Request.Context()

	if q.Kind != "" || q.Name != "" {
		resp, err := h.metrics.ResourceMetrics(ctx, id, &q)
		if err != nil {
			app.Error(c, err)
			return
		}
		app.Success(c, resp)
		return
	}

	resp, err := h.metrics.Metrics(ctx, id, &q)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// Resources GET /devices/:id/resources?kind=
func (h *DeviceHandler) Resources(c *gin.Context) {
	id, err := deviceID(c)
	if err != nil {
		app.Error(c, err)
		return
	}
	resp, err := h.svc.Resources(c.Request.Context(), id, c.Query("kind"))
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// Enable POST /devices/:id/enable
func (h *DeviceHandler) Enable(c *gin.Context) {
	id, err := deviceID(c)
	if err != nil {
		app.Error(c, err)
		return
	}
	if err := h.svc.Enable(c.Request.Context(), id); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}

// Disable POST /devices/:id/disable
func (h *DeviceHandler) Disable(c *gin.Context) {
	id, err := deviceID(c)
	if err != nil {
		app.Error(c, err)
		return
	}
	if err := h.svc.Disable(c.Request.Context(), id); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}

// Delete DELETE /devices/:id
func (h *DeviceHandler) Delete(c *gin.Context) {
	id, err := deviceID(c)
	if err != nil {
		app.Error(c, err)
		return
	}
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}
