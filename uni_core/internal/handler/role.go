package handler

import (
	"context"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"

	"github.com/gin-gonic/gin"
)

// RoleServiceInterface 由 handler/interfaces.go 迁移至此:接口定义在消费方(handler),
// 按需包含本 handler 实际调用的方法集,不再维护中央镜像文件。
type RoleServiceInterface interface {
	FindPage(ctx context.Context, query *request.RoleQuery) (*app.PageResponse, error)
	FindAll(ctx context.Context) ([]response.RoleResp, error)
	FindByID(ctx context.Context, id uint64) (*response.RoleResp, error)
	Create(ctx context.Context, req *request.CreateRoleReq) (uint64, error)
	Update(ctx context.Context, req *request.UpdateRoleReq) error
	UpdateStatus(ctx context.Context, id uint64, status int8) error
	UpdateSort(ctx context.Context, req *request.UpdateRoleSortReq) error
	Delete(ctx context.Context, id uint64) error
}

// RoleHandler exposes HTTP handlers for role management endpoints.
type RoleHandler struct {
	svc RoleServiceInterface
}

// NewRoleHandler creates a new RoleHandler.
func NewRoleHandler(svc RoleServiceInterface) *RoleHandler {
	return &RoleHandler{svc: svc}
}

// List handles GET /api/v1/roles — paginated role listing.
// @Summary      角色列表
// @Description  分页查询角色列表，支持按名称、编码、状态筛选
// @Tags         角色管理
// @Accept       json
// @Produce      json
// @Param        page      query     int     false  "页码"           default(1)
// @Param        pageSize  query     int     false  "每页条数"       default(10)
// @Param        name      query     string  false  "角色名称(模糊查询)"
// @Param        code      query     string  false  "角色编码(模糊查询)"
// @Param        status    query     int     false  "状态(0=禁用 1=启用)"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=app.PageResponse}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /roles [get]
func (h *RoleHandler) List(c *gin.Context) {
	var query request.RoleQuery
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

// GetAll handles GET /api/v1/roles/all — all roles for dropdown.
// 白名单接口(登录即可):仅返回选择器所需字段,不含菜单/部门授权明细。
// @Summary      全部角色
// @Description  获取所有角色，用于下拉选择(不含授权明细)
// @Tags         角色管理
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=[]response.RoleOptionResp}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Router       /roles/all [get]
func (h *RoleHandler) GetAll(c *gin.Context) {
	list, err := h.svc.FindAll(c.Request.Context())
	if err != nil {
		app.Error(c, err)
		return
	}
	options := make([]response.RoleOptionResp, 0, len(list))
	for _, r := range list {
		options = append(options, response.RoleOptionResp{
			ID:     r.ID,
			Name:   r.Name,
			Code:   r.Code,
			Sort:   r.Sort,
			Status: r.Status,
		})
	}
	app.Success(c, options)
}

// GetByID handles GET /api/v1/roles/:id — single role detail.
// @Summary      角色详情
// @Description  根据角色 ID 查询角色详细信息，包含关联的菜单和部门
// @Tags         角色管理
// @Accept       json
// @Produce      json
// @Param        id   path      uint64  true  "角色ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.RoleResp}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "角色不存在"
// @Router       /roles/{id} [get]
func (h *RoleHandler) GetByID(c *gin.Context) {
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

// Create handles POST /api/v1/roles — create a new role.
// @Summary      创建角色
// @Description  创建新角色，可同时关联菜单和部门权限
// @Tags         角色管理
// @Accept       json
// @Produce      json
// @Param        req  body      request.CreateRoleReq  true  "创建角色请求"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "创建成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /roles [post]
func (h *RoleHandler) Create(c *gin.Context) {
	var req request.CreateRoleReq
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

// Update handles PUT /api/v1/roles/:id — update an existing role.
// @Summary      更新角色
// @Description  更新指定角色的信息，包括菜单和部门权限（全量替换）
// @Tags         角色管理
// @Accept       json
// @Produce      json
// @Param        id   path      uint64               true  "角色ID"
// @Param        req  body      request.UpdateRoleReq  true  "更新角色请求"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "更新成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "角色不存在"
// @Router       /roles/{id} [put]
func (h *RoleHandler) Update(c *gin.Context) {
	var req request.UpdateRoleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	req.ID = id
	if err := h.svc.Update(c.Request.Context(), &req); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}

// UpdateStatus handles PUT /api/v1/roles/:id/status — toggle role status
// from the list page's inline switch.
// @Summary      修改角色状态
// @Description  启用/停用指定角色（内置管理员角色不允许修改）
// @Tags         角色管理
// @Accept       json
// @Produce      json
// @Param        id    path      uint64                      true  "角色ID"
// @Param        req   body      request.UpdateRoleStatusReq true  "状态(0=停用 1=启用)"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "修改成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "角色不存在"
// @Router       /roles/{id}/status [put]
func (h *RoleHandler) UpdateStatus(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	var req request.UpdateRoleStatusReq
	if err := c.ShouldBindJSON(&req); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	if err := h.svc.UpdateStatus(c.Request.Context(), id, *req.Status); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}

// UpdateSort handles PUT /api/v1/roles/sort — batch save sort values.
// @Summary      保存排序
// @Description  批量提交角色排序值(直接覆盖赋值,不触发关联替换)
// @Tags         角色管理
// @Accept       json
// @Produce      json
// @Param        req  body      request.UpdateRoleSortReq  true  "排序请求"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "保存成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /roles/sort [put]
func (h *RoleHandler) UpdateSort(c *gin.Context) {
	var req request.UpdateRoleSortReq
	if err := c.ShouldBindJSON(&req); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	if err := h.svc.UpdateSort(c.Request.Context(), &req); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}

// Delete handles DELETE /api/v1/roles/:id — soft-delete a role.
// @Summary      删除角色
// @Description  软删除指定角色（不允许删除被用户关联的角色）
// @Tags         角色管理
// @Accept       json
// @Produce      json
// @Param        id   path      uint64  true  "角色ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "删除成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "角色不存在"
// @Router       /roles/{id} [delete]
func (h *RoleHandler) Delete(c *gin.Context) {
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
