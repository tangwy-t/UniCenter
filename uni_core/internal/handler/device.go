package handler

import (
	"context"

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
// DeviceOverviewServiceInterface 是**总览页**的批量聚合能力面。
//
// 单列成第三个接口而不是并进 DeviceMetricsServiceInterface：总览是唯一
// 需要「设备仓储 + 水位 + 趋势」三者的入口（它必须先知道有哪几台设备），
// 而指标查询服务**刻意**不持有设备仓储（只管单台设备的指标表/Redis）。
// 若并进同一个接口，装配处就会被迫给指标查询服务注入设备仓储，
// 让「查询域不查设备表」这条分层约束失效 —— 那条约束正是两个服务分开的理由。
type DeviceOverviewServiceInterface interface {
	Overview(ctx context.Context, q *request.DeviceOverviewQuery) (*response.DeviceOverviewResp, error)
}

type DeviceHandler struct {
	svc      DeviceServiceInterface
	metrics  DeviceMetricsServiceInterface
	overview DeviceOverviewServiceInterface
}

// NewDeviceHandler 注入管理域与查询域两个依赖（二者由不同服务实现，不得混用）。
// NewDeviceHandler 注入三个依赖（管理域 / 单台指标查询 / 总览聚合）。
//
// 三者不合并成一个大接口：管理域不查指标表、查询域不查设备表、总览需要两者 ——
// 合成一个接口会让每个调用点都看得见自己不该用的能力。
func NewDeviceHandler(svc DeviceServiceInterface, metrics DeviceMetricsServiceInterface,
	overview DeviceOverviewServiceInterface) *DeviceHandler {
	return &DeviceHandler{svc: svc, metrics: metrics, overview: overview}
}

// 路径参数 :id 的解析统一走 app.Uint64Param（C2）。
//
// 原先这里自造了一个 deviceID()（strconv.ParseUint + "设备 ID 非法"），而
// internal/pkg/app/param.go 的 app.Uint64Param 已被 11 个 handler、50 处使用：
// 两套解析的失败信息与行为必然漂移（自造版额外把 id==0 也判成 400）。
// 以既有工具为准 ⇒ id=0 不再在 handler 层挡住，而是交给 service 走「设备不存在」
// 的正常路径（雪花 id 永不为 0，故这只是一个语义更一致的边界，不是能力缺失）。

// List handles GET /api/v1/devices — paginated device listing.
// @Summary      设备列表
// @Description  分页查询设备列表，支持按主机名、启停态、在线状态筛选；每行附带 Redis 水位
// @Tags         设备监控
// @Accept       json
// @Produce      json
// @Param        page      query  int     false  "页码"           default(1)
// @Param        pageSize  query  int     false  "每页条数"       default(10)
// @Param        hostname  query  string  false  "主机名(模糊查询)"
// @Param        status    query  int     false  "启停态(0=停用 1=启用)"
// @Param        online    query  bool    false  "在线状态(true=在线 false=离线)"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=app.PageResponse}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /devices [get]
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

// GetByID handles GET /api/v1/devices/:id — device detail with the latest watermark.
// @Summary      设备详情
// @Description  按设备 ID 查询详情（含 Redis 最新水位与在线状态）
// @Tags         设备监控
// @Accept       json
// @Produce      json
// @Param        id   path      uint64  true  "设备ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.DeviceResp}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "设备不存在"
// @Router       /devices/{id} [get]
func (h *DeviceHandler) GetByID(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	resp, err := h.svc.GetByID(c.Request.Context(), id)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// Metrics handles GET /api/v1/devices/:id/metrics — whole-machine trend or per-resource drill.
//
// @Summary      设备指标趋势 / 资源下钻
// @Description  整机趋势：range 选档（≤24h Redis 原始 / ≤30d 5min 表 / >30d 1h 表），metrics 为逗号白名单（* 表示该档全部可用列）
// @Description  资源下钻：kind+name 成对出现（kind ∈ disk/disk_io/nic/sensor，name 为挂载点/设备名/网卡名/传感器名）
// @Tags         设备监控
// @Accept       json
// @Produce      json
// @Param        id      path   uint64  true   "设备ID"
// @Param        range   query  int     false  "时间窗口(秒)"        default(86400)
// @Param        metrics query  string  false  "逗号分隔的指标列白名单，* 表示该档全部可用列"
// @Param        kind    query  string  false  "资源种类(disk/disk_io/nic/sensor)，与 name 成对"
// @Param        name    query  string  false  "资源名(挂载点/设备名/网卡名/传感器名)，与 kind 成对"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.DeviceMetricsResp}  "整机趋势"
// @Success      200  {object}  app.Response{data=response.DeviceResourceResp}  "资源下钻"
// @Failure      400  {object}  app.Response  "参数错误(range 越界 / kind+name 不配对 / 列不在该档可用列集)"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /devices/{id}/metrics [get]
//
// 一个端点两种语义：kind+name 同时存在 → 资源下钻；否则 → 整机趋势。
// 两条分支各自 return，**不做**「先调一次再调一次」的兜底（那是死代码：
// 后一次调用会覆盖前一次的结果，还会白白多打一次 DB/Redis）。
//
// 为什么下钻复用同一端点：挂载点含 "/"、网卡名含 "."/" "，做 path segment
// 需要双重转义，且根挂载点 "/" 会撞上 Gin 的尾斜杠路由（spec §8）。
func (h *DeviceHandler) Metrics(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
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

// Resources handles GET /api/v1/devices/:id/resources — the device's resource inventory.
// @Summary      设备资源清单
// @Description  枚举该设备的资源（drill 下拉数据源），含 last_seen_at 与 stale（超过 90 天未出现不再枚举）
// @Tags         设备监控
// @Accept       json
// @Produce      json
// @Param        id    path   uint64  true   "设备ID"
// @Param        kind  query  string  false  "资源种类(disk/disk_io/nic/sensor)，空表示全部"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.DeviceResourcesResp}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "设备不存在"
// @Router       /devices/{id}/resources [get]
func (h *DeviceHandler) Resources(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	resp, err := h.svc.Resources(c.Request.Context(), id, c.Query("kind"))
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// Enable handles POST /api/v1/devices/:id/enable — enable a device.
// @Summary      启用设备
// @Description  把设备的启停态置为启用(1)；与在线状态正交
// @Tags         设备监控
// @Accept       json
// @Produce      json
// @Param        id   path      uint64  true  "设备ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "启用成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "设备不存在"
// @Router       /devices/{id}/enable [post]
func (h *DeviceHandler) Enable(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	if err := h.svc.Enable(c.Request.Context(), id); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}

// Disable handles POST /api/v1/devices/:id/disable — disable a device.
// @Summary      停用设备
// @Description  把设备的启停态置为停用(0)；停用后 agent 上报不再被接受，且重新 enroll 不会自动启用
// @Tags         设备监控
// @Accept       json
// @Produce      json
// @Param        id   path      uint64  true  "设备ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "停用成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "设备不存在"
// @Router       /devices/{id}/disable [post]
func (h *DeviceHandler) Disable(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	if err := h.svc.Disable(c.Request.Context(), id); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}

// Delete handles DELETE /api/v1/devices/:id — soft-delete a device and purge its Redis data.
// @Summary      删除设备
// @Description  软删设备，并连带清理 Redis 原始窗/水位键/资源维度行与子表行（spec §7.3）
// @Tags         设备监控
// @Accept       json
// @Produce      json
// @Param        id   path      uint64  true  "设备ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "删除成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "设备不存在"
// @Router       /devices/{id} [delete]
func (h *DeviceHandler) Delete(c *gin.Context) {
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

// Overview handles GET /api/v1/devices/overview — 设备监控总览（单页看全部设备 × 各类指标）。
//
// @Summary      设备监控总览
// @Description  一次请求返回 N 台设备的最新快照（水位全字段）与多列趋势（列式，共享时间轴），按指标类别分组绘图即可得到「所有设备 × 各类指标」的总览视图
// @Description  range 选档与 /devices/{id}/metrics **完全同口径**（≤24h Redis / ≤30d 5min / >30d 1h）；metrics 为逗号白名单，* 表示该档全部可用列
// @Description  ids 可显式指定设备白名单（「只对比勾选的这几台」）；hostname/status/online 为页面级过滤
// @Description  设备数超过上限时**截断并置 truncated**（不报错），单台设备的趋势取数失败降级为该设备的 error 字段（不影响其余设备）
// @Tags         设备监控
// @Accept       json
// @Produce      json
// @Param        range    query  int     false  "时间窗口(秒)"     default(86400)
// @Param        metrics  query  string  false  "逗号分隔的指标列白名单，* 表示该档全部可用列"
// @Param        ids      query  string  false  "设备ID白名单(逗号分隔)，用于只看选定设备"
// @Param        hostname query  string  false  "主机名模糊匹配"
// @Param        status   query  int     false  "启停状态(0停用/1启用)"
// @Param        online   query  bool    false  "是否在线(在线判定依 sys.agent.offlineThreshold)"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.DeviceOverviewResp}  "总览数据"
// @Failure      400  {object}  app.Response  "参数错误(range 越界 / 列不在该档可用列集 / ids 含非法设备ID)"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /devices/overview [get]
//
// 路由形态说明（**重要**）：本路径与 `GET /devices/:id` 是**兄弟**关系，
// 在 gin 的路由树上 /devices/overview 是静态段、/:id 是参数段。gin v1.12 的
// httprouter 支持这种共存（静态优先匹配），已实测确认不会 panic 也不会误配。
// 之所以不复用 `/devices` + 特殊 query 参数，是为了让「总览」在路由表里
// **可见**：运维从访问日志里能一眼区分总览流量与列表流量。
func (h *DeviceHandler) Overview(c *gin.Context) {
	var query request.DeviceOverviewQuery
	if err := c.ShouldBindQuery(&query); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	resp, err := h.overview.Overview(c.Request.Context(), &query)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}
