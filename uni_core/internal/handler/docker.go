package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/tangwy-t/UniCenter/uni_core/internal/middleware"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/app"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/jwt"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/service"
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
	log   logger.LoggerInterface
}

// NewDockerHandler 构造 handler。
func NewDockerHandler(svc *service.DockerService, cmds *service.DockerCmdService,
	guard PermChecker, log logger.LoggerInterface) *DockerHandler {
	return &DockerHandler{svc: svc, cmds: cmds, guard: guard, log: log}
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
//  3. 身份由 middleware 提供，缺了就是 401；
//  4. 受理失败的错误形态由 service 决定（409 在飞 / 400 期次闸 / 500 设备离线），
//     handler 只负责转达 —— 在这里再判断一次就会多出第二处口径。
//
// @Summary      受理 docker 指令
// @Description  校验 action 对应的权限码与期次闸后下发；立即返回指令号（ref），结果靠轮询
// @Tags         Docker 管理
// @Accept       json
// @Produce      json
// @Param        id    path      uint64                    true  "设备ID"
// @Param        body  body      request.DockerCmdReq      true  "指令请求"
// @Security     BearerAuth
// @Success      202  {object}  app.Response{data=response.DockerCmdResp}  "已受理（data.ref 为指令号）"
// @Failure      400  {object}  app.Response  "参数错误 / 未知操作 / 该操作尚未开放"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无操作权限"
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

// CmdResult 轮询指令结果（权限码取自记录，再校验一次归属）。
//
// 权限**从记录里读**而不是从查询参数里读：记录是服务端写下的，
// 用户改不了它；而「按 action 参数现算」会让同一个 ref 在不同请求上得到不同的判定。
// Lookup 已保证「设备不匹配 = 不存在」（404），故此处不会拿别的主机的记录做判定。
//
// @Summary      轮询 docker 指令结果
// @Description  按指令号查询状态与载荷（pending/running/succeeded/failed/timeout）
// @Tags         Docker 管理
// @Produce      json
// @Param        id   path      uint64  true  "设备ID"
// @Param        ref  path      string  true  "指令号"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.DockerCmdResultResp}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无操作权限"
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
	app.Success(c, h.cmds.Result(rec))
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
