package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/tangwy-t/UniCenter/uni_core/internal/middleware"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/app"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerevents"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/jwt"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/permission"
	"github.com/tangwy-t/UniCenter/uni_core/internal/service"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// Docker 管理的 HTTP 入口：主机清单 / 快照（读面）+ 指令受理与轮询（下发面）。
//
// 与设备 handler 不同的一处关键差异：**指令面的权限码按 action 变化**
//（spec §4.3.1 总表：container:logs→docker:inspect、image:remove→docker:delete…），
// 因此这两条路由在 router 上**没有静态 perm**，而在处理器内拿到 action 之后
// 用 PermissionGuard.Ensure 判定 —— 此时 body 已经解完，不必与处理器抢 reader。

// PermChecker 是处理器内做一次权限校验的能力面（由 middleware.PermissionGuard 实现）。
//
// 声明成接口而直接持有 *middleware.PermissionGuard：handler 只用到 Ensure 这一个方法，
// 窄接口让测试能用「按权限列表判定」的替身构造出一条**不依赖 session store / 配置 /
// singleflight** 的判定路径（那些依赖与「403 还是 202」这个结论无关）。
type PermChecker interface {
	Ensure(c *gin.Context, requiredPerm string) bool
}

// DockerHandler 是 Docker 管理的 HTTP 入口。
type DockerHandler struct {
	svc   *service.DockerService
	cmds  *service.DockerCmdService
	guard PermChecker
	// streams 是流通道面（三期）：结果签票据、日志 NDJSON、终端 WebSocket。
	// nil = 未装配：流端点整体不可用（路由仍在，处理器给出 500 语义），
	// 结果轮询不带 streamTicket —— 与「三期未交付」表现一致。
	streams *service.DockerStreamService
	// events 是事件流常驻管理器（六期）：聚合端点直接向它订阅/退订（引用计数）。
	// nil = 未装配：/docker/events 整体不可用（路由仍在，处理器给出 500 语义）。
	events *dockerevents.Manager
	// tasks 是任务面（6b）：任务中心的最近指令列表。
	// nil = 未装配：/docker/tasks 整体不可用（路由仍在，处理器给出 500 语义）。
	tasks *service.DockerTaskService
	// buildCtx 是构建上下文上传中转（v1.3）：nil = 未装配（语义同 tasks）。
	buildCtx *service.DockerBuildContextService
	log      logger.LoggerInterface
}

// NewDockerHandler 构造 handler。
func NewDockerHandler(svc *service.DockerService, cmds *service.DockerCmdService,
	streams *service.DockerStreamService, guard PermChecker, log logger.LoggerInterface) *DockerHandler {
	return &DockerHandler{svc: svc, cmds: cmds, streams: streams, guard: guard, log: log}
}

// WithEvents 注入事件流常驻管理器（六期；装配在 wireup 一处完成，测试装配替身）。
func (h *DockerHandler) WithEvents(m *dockerevents.Manager) *DockerHandler {
	h.events = m
	return h
}

// WithTasks 注入任务面（6b；装配在 wireup 一处完成，测试装配替身）。
func (h *DockerHandler) WithTasks(t *service.DockerTaskService) *DockerHandler {
	h.tasks = t
	return h
}

// WithBuildContext 注入构建上下文上传中转（v1.3；装配在 wireup 一处完成）。
func (h *DockerHandler) WithBuildContext(s *service.DockerBuildContextService) *DockerHandler {
	h.buildCtx = s
	return h
}

// Hosts 返回可管主机清单（docker:list）。
//
// @Summary      可管主机清单
// @Description  枚举上报过快照的主机（docker:hosts 集合）及各自的摘要与陈旧结论
// @Tags         Docker 管理
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.DockerHostListResp}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /docker/hosts [get]
func (h *DockerHandler) Hosts(c *gin.Context) {
	resp, err := h.svc.Hosts(c.Request.Context())
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// Overview 返回控制塔总览（跨主机聚合 KPI + 主机清单 + 异常清单）（docker:list）。
//
// @Summary      控制塔总览
// @Description  聚合全部可管主机的容器/镜像/卷/网络/项目计数与异常容器清单（条目与 docker/hosts 同形态；单台快照读失败只降级该主机并如实标注 error）
// @Tags         Docker 管理
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.DockerOverviewResp}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /docker/overview [get]
func (h *DockerHandler) Overview(c *gin.Context) {
	resp, err := h.svc.Overview(c.Request.Context())
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// Workloads 返回跨主机统一工作负载表（docker:list）。
//
// @Summary      跨主机容器统一表
// @Description  全部可管主机的容器并成一张表（每行带 hostId/hostname 归属）；hostId 限定单主机、state 过滤运行态（stopped=一切非 running，与总览 KPI 同口径）、keyword 按容器名或镜像名子串匹配；上限 500 条、total 如实报截断前全量；单台快照读失败跳过该主机（其故障在总览页如实呈现）
// @Tags         Docker 管理
// @Produce      json
// @Param        hostId   query  uint64  false  "限定单主机(缺省=全部可管主机)"
// @Param        state    query  string  false  "运行态过滤(running/stopped)"
// @Param        keyword  query  string  false  "容器名或镜像名子串(大小写不敏感)"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.DockerWorkloadListResp}  "查询成功"
// @Failure      400  {object}  app.Response  "参数错误(state 非 running/stopped)"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /docker/containers [get]
func (h *DockerHandler) Workloads(c *gin.Context) {
	var q request.DockerWorkloadQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		app.Error(c, apperror.BadRequest("请求参数不合法"))
		return
	}
	resp, err := h.svc.Workloads(c.Request.Context(), &q)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// Images 返回跨主机镜像统一表（docker:list）。
//
// @Summary      跨主机镜像统一表
// @Description  全部可管主机的镜像并成一张表（每行带 hostId/hostname 归属）；hostId 限定单主机、keyword 按 repoTag 子串匹配（大小写不敏感）、dangling/unused 三值过滤（缺省=不过滤）；上限 500 条、total 如实报截断前全量；单台快照读失败跳过该主机（其故障在总览页如实呈现）
// @Tags         Docker 管理
// @Produce      json
// @Param        hostId    query  uint64  false  "限定单主机(缺省=全部可管主机)"
// @Param        keyword   query  string  false  "repoTag 子串(大小写不敏感)"
// @Param        dangling  query  bool    false  "悬空过滤(true=仅悬空，false=仅非悬空，缺省=不过滤)"
// @Param        unused    query  bool    false  "未使用过滤(true=仅未使用，false=仅在使用，缺省=不过滤)"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.DockerImageListResp}  "查询成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /docker/images [get]
func (h *DockerHandler) Images(c *gin.Context) {
	var q request.DockerImageQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		app.Error(c, apperror.BadRequest("请求参数不合法"))
		return
	}
	resp, err := h.svc.Images(c.Request.Context(), &q)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// Volumes 返回跨主机卷统一表（docker:list）。
//
// @Summary      跨主机卷统一表
// @Description  全部可管主机的数据卷并成一张表（每行带 hostId/hostname 归属）；hostId 限定单主机、keyword 按卷名子串匹配（大小写不敏感）、unused 三值过滤（缺省=不过滤）；上限 500 条、total 如实报截断前全量；单台快照读失败跳过该主机（其故障在总览页如实呈现）
// @Tags         Docker 管理
// @Produce      json
// @Param        hostId   query  uint64  false  "限定单主机(缺省=全部可管主机)"
// @Param        keyword  query  string  false  "卷名子串(大小写不敏感)"
// @Param        unused   query  bool    false  "未使用过滤(true=仅未使用，false=仅在使用，缺省=不过滤)"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.DockerVolumeListResp}  "查询成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /docker/volumes [get]
func (h *DockerHandler) Volumes(c *gin.Context) {
	var q request.DockerVolumeQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		app.Error(c, apperror.BadRequest("请求参数不合法"))
		return
	}
	resp, err := h.svc.Volumes(c.Request.Context(), &q)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// Networks 返回跨主机网络统一表（docker:list）。
//
// @Summary      跨主机网络统一表
// @Description  全部可管主机的网络并成一张表（每行带 hostId/hostname 归属）；hostId 限定单主机、keyword 按网络名子串匹配（大小写不敏感）、internal 三值过滤（缺省=不过滤）；上限 500 条、total 如实报截断前全量；单台快照读失败跳过该主机（其故障在总览页如实呈现）
// @Tags         Docker 管理
// @Produce      json
// @Param        hostId    query  uint64  false  "限定单主机(缺省=全部可管主机)"
// @Param        keyword   query  string  false  "网络名子串(大小写不敏感)"
// @Param        internal  query  bool    false  "隔离网络过滤(true=仅 internal，false=仅非 internal，缺省=不过滤)"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.DockerNetworkListResp}  "查询成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /docker/networks [get]
func (h *DockerHandler) Networks(c *gin.Context) {
	var q request.DockerNetworkQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		app.Error(c, apperror.BadRequest("请求参数不合法"))
		return
	}
	resp, err := h.svc.Networks(c.Request.Context(), &q)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// Projects 返回跨主机项目统一表（docker:list）。
//
// @Summary      跨主机项目统一表
// @Description  全部可管主机的编排项目并成一张表（每行带 hostId/hostname 归属）；hostId 限定单主机、keyword 按项目名子串匹配（大小写不敏感）、state 过滤项目态（running/stopped，stopped=一切非 running，与容器表同口径）；上限 500 条、total 如实报截断前全量；单台快照读失败跳过该主机（其故障在总览页如实呈现）
// @Tags         Docker 管理
// @Produce      json
// @Param        hostId   query  uint64  false  "限定单主机(缺省=全部可管主机)"
// @Param        keyword  query  string  false  "项目名子串(大小写不敏感)"
// @Param        state    query  string  false  "项目态过滤(running/stopped)"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.DockerProjectListResp}  "查询成功"
// @Failure      400  {object}  app.Response  "参数错误(state 非 running/stopped)"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /docker/projects [get]
func (h *DockerHandler) Projects(c *gin.Context) {
	var q request.DockerProjectQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		app.Error(c, apperror.BadRequest("请求参数不合法"))
		return
	}
	resp, err := h.svc.Projects(c.Request.Context(), &q)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// State 返回一台主机的快照（docker:list）。
//
// @Summary      主机资源快照
// @Description  返回一台主机的容器/镜像/卷/网络/编排项目清单与陈旧结论（离线可读，只标 stale）
// @Tags         Docker 管理
// @Produce      json
// @Param        id   path      uint64  true  "设备ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.DockerStateResp}  "查询成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "设备不存在"
// @Router       /docker/hosts/{id}/state [get]
func (h *DockerHandler) State(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	resp, err := h.svc.State(c.Request.Context(), id)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// StatsHistory 返回一个容器的 stats 留存序列（docker:inspect，与 stats 实时流同档）。
//
// 为什么是静态路由权限而不是像流那样按记录校验：留存不是「接入某条已受理指令的
// 会话」（流要防的是劫持发起人的会话），它是与快照同族的**读面** —— 权限档与
// container:stats 指令（→ docker:inspect）对齐即可，路由上挂静态 perm。
//
// @Summary      容器 stats 历史
// @Description  一台主机上一个容器的 CPU/内存留存读数（30s 快照节奏 × 60 样本 ≈ 30 分钟窗口，按时刻升序）；时刻为 core 收帧时刻；无历史返回空数组；容器消失后序列冻结在最后一次读数
// @Tags         Docker 管理
// @Produce      json
// @Param        id   path      uint64  true  "设备ID"
// @Param        cid  path      string  true  "容器ID（快照条目的完整 ID）"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.DockerStatsHistoryResp}  "查询成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限(docker:inspect)"
// @Failure      404  {object}  app.Response  "设备不存在"
// @Failure      500  {object}  app.Response  "stats 留存未装配 / 内部错误"
// @Router       /docker/hosts/{id}/containers/{cid}/stats-history [get]
func (h *DockerHandler) StatsHistory(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	cid := c.Param("cid")
	if cid == "" {
		app.Error(c, apperror.BadRequest("请求参数不合法"))
		return
	}
	resp, err := h.svc.ContainerStatsHistory(c.Request.Context(), id, cid)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// Tasks 返回任务中心的最近任务（docker:list，路由静态 perm）。
//
// 本切片不做取消动作（见 service/docker_tasks.go 的取消纪律）：前端对拉取类任务
// 复用既有进度流 Abort（断开 /cmds/:ref/pull 即下发 cancel）；非流任务无取消入口。
// 断流是 best-effort 的，与「daemon 恰好干完活」存在竞态 —— 终态由 agent 按拉取的
// 实际结局结算（完成即成功，迟到的 cancel 是 no-op），本端点只如实投影结果。
//
// @Summary      最近任务
// @Description  最近受理的 docker 指令（≤100 条、受理时刻降序、跨主机聚合）；hostId 限定单主机、status 过滤 pending/done、action 过滤动作码；条目含 ref/主机/动作/目标/发起人用户名/受理时刻/终态（pending/succeeded/failed/timeout）/终态结论句；拉取类任务前端凭 action 复用 /cmds/:ref/pull 打开进度流（取消=断开进度流）
// @Tags         Docker 管理
// @Produce      json
// @Param        hostId   query  uint64  false  "限定单主机(缺省=跨主机)"
// @Param        status   query  string  false  "阶段过滤(pending/done)"
// @Param        action   query  string  false  "动作码过滤(如 image:pull)"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.DockerTaskListResp}  "查询成功"
// @Failure      400  {object}  app.Response  "参数错误(status 非 pending/done) / 未知操作"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限(docker:list)"
// @Router       /docker/tasks [get]
func (h *DockerHandler) Tasks(c *gin.Context) {
	if h.tasks == nil {
		// 与 WithEvents 同款「未装配」语义：路由仍在，处理器给出 500（装配错误
		// 早暴露，好过假装收口一张空表）。
		app.Error(c, apperror.Internal("任务服务未装配"))
		return
	}
	var q request.DockerTasksQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		app.Error(c, apperror.BadRequest("请求参数不合法"))
		return
	}
	resp, err := h.tasks.Tasks(c.Request.Context(), &q)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// SendCmd 受理一条指令（权限码**按 action** 校验，见 PermissionGuard.Ensure 的说明）。
//
// 成功返回 **202** + {ref}（不是 200：指令只是**被受理**，结果要靠轮询
// GET /docker/hosts/:id/cmds/:ref）。信封与 app.Success 逐字同形（同一个
// app.Response 结构体），否则前端拦截器认不出这个响应。
//
// 四处顺序不宜调换：
//  1. 先解参数与 body —— 没有 action 就无从知道要校验哪个权限码；
//  2. **权限早于业务**：无权限的请求不该被受理、也不该暴露「这个 action 存不存在」
//     （未知 action 归 400，但它排在权限之后只需一次 403 就能挡住探测）；
//     action 的权限之外，body 里 `options.force=true` 还要再过一次 docker:exec 级
//     权限（保护档的风险等价于 root shell）—— 两关都过了才轮到业务；
//  3. 身份由 middleware 提供，缺了就是 401；
//  4. 受理失败的错误形态由 service 决定（409 在飞 / 400 参数 / 500 设备离线），
//     handler 只负责转达 —— 在这里再判断一次就会多出第二处口径。
//
// @Summary      受理 docker 指令
// @Description  校验 action 对应的权限码后下发；立即返回指令号（ref），结果靠轮询
// @Tags         Docker 管理
// @Accept       json
// @Produce      json
// @Param        id    path      uint64                    true  "设备ID"
// @Param        body  body      request.DockerCmdReq      true  "指令请求"
// @Security     BearerAuth
// @Success      202  {object}  app.Response{data=response.DockerCmdResp}  "已受理（data.ref 为指令号）"
// @Failure      400  {object}  app.Response  "参数错误 / 未知操作"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无操作权限（force=true 时还要求 docker:exec 级）"
// @Failure      409  {object}  app.Response  "该目标上已有同一条指令在执行"
// @Failure      500  {object}  app.Response  "设备离线 / docker 不可用 / 内部错误"
// @Router       /docker/hosts/{id}/cmds [post]
func (h *DockerHandler) SendCmd(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	var req request.DockerCmdReq
	if err := c.ShouldBindJSON(&req); err != nil {
		app.Error(c, apperror.BadRequest("请求参数不合法"))
		return
	}
	// 未知 action 必须在权限之前拒掉：RequiredPerm 对未登记 action 没有答案，
	// 拿不到权限码就无从校验（若放行，等于给未登记 action 开了一条路）。
	perm, ok := h.cmds.RequiredPerm(req.Action)
	if !ok {
		app.Error(c, apperror.BadRequest("未知操作"))
		return
	}
	if !h.guard.Ensure(c, perm) {
		return // Ensure 已写好 403（或 401/500），此处不得再写响应
	}
	// force 是「对受保护目标动手」的开关：保护清单（agent 侧 guard）对受保护目标的
	// 停/删/重建默认拒绝，force=true 是唯一越过它的方式 —— 其风险等价于 root shell
	//（spec §10 保护档，「uni-center 把自己删了」没有第二次机会），故它要求
	// docker:exec 级权限（admin 通配同样放行）。这与「能不能删东西」（docker:delete）
	// 刻意分开授予：持有 docker:delete 的人**不能** force —— 这正是「先停再删」
	// 两步绕过保护清单被堵住的地方。
	if req.Options != nil && req.Options.Force != nil && *req.Options.Force {
		if !h.guard.Ensure(c, permission.PermDockerExec) {
			return // 同上：Ensure 已写好 403
		}
	}
	uid, ok := currentUserID(c)
	if !ok {
		app.Error(c, apperror.Unauthorized("未登录或 token 已过期"))
		return
	}
	ref, err := h.cmds.Send(c.Request.Context(), uid, id, &req)
	if err != nil {
		app.Error(c, err)
		return
	}
	// 202 Accepted：受理语义，不是「已完成」。信封复用 app.Response、
	// 载荷复用 response.DockerCmdResp（C6 为本响应定的 DTO），
	// 因此形状与 app.Success 逐字同形（只有状态码不同），字段名不会在这里写第二遍。
	c.JSON(http.StatusAccepted, app.Response{
		Code: apperror.CodeOK, Message: "success", Data: response.DockerCmdResp{Ref: ref},
	})
}

// CmdResult 轮询指令结果（权限码取自记录，再校验发起人归属与签发流票据）。
//
// 权限**从记录里读**而不是从查询参数里读：记录是服务端写下的，
// 用户改不了它；而「按 action 参数现算」会让同一个 ref 在不同请求上得到不同的判定。
// Lookup 已保证「设备不匹配 = 不存在」（404），故此处不会拿别的主机的记录做判定。
//
// **归属校验**（spec §10.5）：ref 只对发起人可见。这不是锦上添花 —— 三期起结果
// 里带着 streamTicket，若同权限用户 B 能轮询 A 的 ref，就能拿到 A 会话的入场券
// （票据绑的是 A 的 userId），从而劫持 A 的终端。归属在这里拦下，票据才安全。
//
// @Summary      轮询 docker 指令结果
// @Description  按指令号查询状态与载荷（pending/running/succeeded/failed/timeout）；建立了流会话时附带一次性 streamTicket
// @Tags         Docker 管理
// @Produce      json
// @Param        id   path      uint64  true  "设备ID"
// @Param        ref  path      string  true  "指令号"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.DockerCmdResultResp}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无操作权限 / 非发起人"
// @Failure      404  {object}  app.Response  "指令不存在或已过期"
// @Router       /docker/hosts/{id}/cmds/{ref} [get]
func (h *DockerHandler) CmdResult(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	ref := c.Param("ref")
	if ref == "" {
		app.Error(c, apperror.BadRequest("请求参数不合法"))
		return
	}
	rec, err := h.cmds.Lookup(c.Request.Context(), id, ref)
	if err != nil {
		app.Error(c, err)
		return
	}
	if !h.guard.Ensure(c, rec.Perm) {
		return // Ensure 已写好 403
	}
	uid, ok := currentUserID(c)
	if !ok {
		app.Error(c, apperror.Unauthorized("未登录或 token 已过期"))
		return
	}
	if rec.UserID != uid {
		app.Error(c, apperror.Forbidden("无权查看该指令的结果"))
		return
	}
	out := h.cmds.Result(rec)
	// 每次响应签一张**新**票据（plan §0）：前端轮询会取多次，用最新那张接流；
	// 旧票不过期作废（各自 30s），时序上不存在「刚拿到就被顶掉」。
	// 签发失败不阻断结果本身（最坏接流时拿到「票据无效」，页面可重试）。
	if h.streams != nil && rec.SessionID != "" {
		ticket, err := h.streams.IssueTicket(c.Request.Context(), rec)
		if err != nil {
			if h.log != nil {
				h.log.Warn("docker stream ticket issue failed",
					zap.String("ref", rec.Ref), zap.String("session", rec.SessionID), zap.Error(err))
			}
		} else {
			out.StreamTicket = ticket
		}
	}
	app.Success(c, out)
}

// BuildContextUpload 接收浏览器直传的构建上下文 tar.gz（image:build 的输入准备段）。
//
// 传输拓扑：本端点**不做存储** —— 字节流经 core 中转成 WSS 二进制分片直达
// agent 的 transferDir（中间零落盘），全程 sha256 累计，转完由 agent 侧终验。
// 因此它没有「指令记录」，也就没有 CmdResult 那套「按记录校验权限与发起人
// 归属」—— 权限由路由**静态 perm(docker:manage)** 挂住（与 image:build 受理
// 同档：上下文是「即将构建并留痕在镜像里的代码」，与分发同档的输入面），
// 「发起人」语义在此不适用（上传不建立任何按用户归属的账目）。
//
// 响应 200 + {filename}：该值即 image:build 的 options.context（推导名纪律
// 见协议 build_context.go —— 名字由会话号唯一推导，agent 不接受客户端命名）。
//
// @Summary      上传构建上下文
// @Description  直传 gzip 压缩的 tar 构建上下文（octet-stream/chunked）；core 流式中转至 agent 的 transferDir 并逐字节累计 sha256，agent 终验后落名 build-ctx-<会话号>.tar.gz；返回文件名（image:build 的 context 值）
// @Tags         Docker 管理
// @Accept       application/octet-stream
// @Produce      json
// @Param        id   path      uint64  true  "设备ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.DockerBuildContextUploadResp}  "上传完成（filename 即 context 值）"
// @Failure      400  {object}  app.Response{data=response.DockerBuildContextUploadResp}  "参数错误 / 超过 512MB 上限 / 内容不是 gzip tar / 上传为空"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限(docker:manage)"
// @Failure      503  {object}  app.Response  "设备离线 / 通道中断（上传未完成，请重试）"
// @Router       /docker/hosts/{id}/build-context [post]
func (h *DockerHandler) BuildContextUpload(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	if h.buildCtx == nil {
		// 与 WithEvents/WithTasks 同款「未装配」语义：路由仍在，处理器给出 500
		//（装配错误早暴露，好过假装收了一个没人接的字节流）。
		app.Error(c, apperror.Internal("构建上下文上传未装配"))
		return
	}
	// Content-Type 判形：请求体就是 tar 字节流本身，content-type 只是形态声明
	// —— 传了别的形态（multipart 等）说明调用方拿错了端点，明说胜过让 agent
	// 侧魔数/哈希终验去猜。
	if ct := c.ContentType(); !strings.HasPrefix(ct, "application/octet-stream") {
		app.Error(c, apperror.BadRequest("Content-Type 必须是 application/octet-stream"))
		return
	}
	contentLen := c.Request.ContentLength
	// MaxBytesReader：客户端不报 Content-Length（chunked）时，超出 512MB 也会
	// 在读到上限的那一点立即以错误收住读 —— 「即拒不等传完」的第二段
	// （第一段是 service 里 Content-Length>上限的直接 400）。上限与 agent 侧
	// 扫描、505 发布物档同源：协议 MaxDockerBuildContextBytes。
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body,
		agentproto.MaxDockerBuildContextBytes)

	name, err := h.buildCtx.Transfer(c.Request.Context(), id, c.Request.Body, contentLen)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, response.DockerBuildContextUploadResp{FileName: name})
}

// currentUserID 取当前登录用户（身份唯一来源是 middleware.CtxClaims，
// 与 middleware.Auth 写入的是同一个键）。
//
// 刻意不用「取不到就当 0 号用户」的宽松写法（见 actorID）：指令记录要落 user_id，
// 「谁发的这条指令」写错人是审计事故，故取不到就返回 false 由调用方拒掉。
func currentUserID(c *gin.Context) (uint64, bool) {
	raw, ok := c.Get(middleware.CtxClaims)
	if !ok {
		return 0, false
	}
	claims, ok := raw.(*jwt.Claims)
	if !ok || claims == nil {
		return 0, false
	}
	return claims.UserID, true
}
