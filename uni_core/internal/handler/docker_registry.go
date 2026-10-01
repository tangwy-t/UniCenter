package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/app"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	"github.com/tangwy-t/UniCenter/uni_core/internal/service"
)

// DockerRegistryHandler 是私有仓库凭据管理的 HTTP 入口（4c）。
//
// 权限是**静态 perm docker:config**（挂在 router 上）：凭据是「分发」支柱的
// 密钥材料，管理档与配置编辑同档（六档里的最高管理档）。与指令面不同 ——
// 这里的 action 恒是「管凭据」，权限不会随 action 变化，不需要处理器内判定。
//
// 审计纪律：写请求的 body 含 password 字段 —— 操作日志中间件的敏感字段表
// 已有 password 掩码（见 middleware/operation_log.go 的 sensitiveFieldSet），
// 受理审计里存的是「***」；本处理器**不做任何额外 body 日志**。
type DockerRegistryHandler struct {
	svc *service.DockerRegistryService
}

// NewDockerRegistryHandler 构造 handler。
func NewDockerRegistryHandler(svc *service.DockerRegistryService) *DockerRegistryHandler {
	return &DockerRegistryHandler{svc: svc}
}

// List 返回全部凭据（列表里的密码恒为掩码「****」）。
//
// @Summary      仓库凭据列表
// @Description  枚举全部私有仓库凭据（registry/用户名/备注/掩码密码/创建时间；密码任何读路径都不回明文）
// @Tags         Docker 管理
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.DockerRegistryListResp}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /docker/registries [get]
func (h *DockerRegistryHandler) List(c *gin.Context) {
	resp, err := h.svc.List(c.Request.Context())
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// Create 新建一条凭据（密码加密后落库，响应不回任何密码）。
//
// @Summary      新建仓库凭据
// @Description  新建私有仓库凭据（registry 唯一键；密码 AES-256-GCM 加密落库，一切读路径掩码）
// @Tags         Docker 管理
// @Accept       json
// @Produce      json
// @Param        body  body      request.DockerRegistrySaveReq  true  "凭据请求"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.DockerRegistryItem}  "创建成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      409  {object}  app.Response  "该仓库地址已有凭据"
// @Router       /docker/registries [post]
func (h *DockerRegistryHandler) Create(c *gin.Context) {
	var req request.DockerRegistrySaveReq
	if err := c.ShouldBindJSON(&req); err != nil {
		app.Error(c, apperror.BadRequest("请求参数不合法"))
		return
	}
	resp, err := h.svc.Create(c.Request.Context(), &req)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// Update 更新既有凭据（registry 是定位键；密码必须重输 —— 不接受读回再提交）。
//
// @Summary      更新仓库凭据
// @Description  按 registry 更新凭据的用户名/密码/备注（密码必须重输，凭据管理没有读回旧密码的路径）
// @Tags         Docker 管理
// @Accept       json
// @Produce      json
// @Param        body  body      request.DockerRegistrySaveReq  true  "凭据请求"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.DockerRegistryItem}  "更新成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "该仓库没有凭据记录"
// @Router       /docker/registries [put]
func (h *DockerRegistryHandler) Update(c *gin.Context) {
	var req request.DockerRegistrySaveReq
	if err := c.ShouldBindJSON(&req); err != nil {
		app.Error(c, apperror.BadRequest("请求参数不合法"))
		return
	}
	resp, err := h.svc.Update(c.Request.Context(), &req)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// Delete 删除一条凭据（删除即失效：带该 registry 的后续拉取在受理处即被拒）。
//
// @Summary      删除仓库凭据
// @Description  按仓库地址删除凭据（删除即失效，不带任何对账周期）
// @Tags         Docker 管理
// @Produce      json
// @Param        registry  path      string  true  "仓库地址"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "删除成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "该仓库没有凭据记录"
// @Router       /docker/registries/{registry} [delete]
func (h *DockerRegistryHandler) Delete(c *gin.Context) {
	registry := c.Param("registry")
	if registry == "" {
		app.Error(c, apperror.BadRequest("请求参数不合法"))
		return
	}
	if err := h.svc.Delete(c.Request.Context(), registry); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}