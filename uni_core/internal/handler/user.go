package handler

import (
	"context"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"

	"github.com/gin-gonic/gin"
)

// UserServiceInterface 由 handler/interfaces.go 迁移至此:接口定义在消费方(handler),
// 按需包含本 handler 实际调用的方法集,不再维护中央镜像文件。
type UserServiceInterface interface {
	FindPage(ctx context.Context, query *request.UserQuery) (*app.PageResponse, error)
	FindByID(ctx context.Context, id uint64) (*response.UserResp, error)
	Create(ctx context.Context, req *request.CreateUserReq) (uint64, error)
	UpdateUserInfo(ctx context.Context, req *request.UpdateUserReq) error
	AssignRoles(ctx context.Context, id uint64, roleIDs []uint64) error
	Delete(ctx context.Context, id uint64) error
	Enable(ctx context.Context, id uint64) error
	Disable(ctx context.Context, id uint64) error
	ResetPassword(ctx context.Context, id uint64, req *request.ResetPasswordReq) error
	AddRoleUsers(ctx context.Context, roleID uint64, userIDs []uint64) error
	RemoveRoleUsers(ctx context.Context, roleID uint64, userIDs []uint64) error
}

// UserHandler exposes HTTP handlers for user management endpoints.
type UserHandler struct {
	svc        UserServiceInterface
	captchaSvc CaptchaInterface
}

// NewUserHandler creates a new UserHandler.
func NewUserHandler(svc UserServiceInterface, captchaSvc CaptchaInterface) *UserHandler {
	return &UserHandler{svc: svc, captchaSvc: captchaSvc}
}

// List handles GET /api/v1/users — paginated user listing.
// @Summary      用户列表
// @Description  分页查询用户列表，支持按用户名、真实姓名、手机号、邮箱、状态、部门筛选
// @Tags         用户管理
// @Accept       json
// @Produce      json
// @Param        page      query     int     false  "页码"           default(1)
// @Param        pageSize  query     int     false  "每页条数"       default(10)
// @Param        username  query     string  false  "用户名(模糊查询)"
// @Param        realName  query     string  false  "真实姓名(模糊查询)"
// @Param        phone     query     string  false  "手机号(模糊查询)"
// @Param        email     query     string  false  "邮箱(模糊查询)"
// @Param        status    query     int     false  "状态(0=禁用 1=启用)"
// @Param        deptId    query     int     false  "部门ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=app.PageResponse}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /users [get]
func (h *UserHandler) List(c *gin.Context) {
	var query request.UserQuery
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

// GetByID handles GET /api/v1/users/:id — single user detail.
// @Summary      用户详情
// @Description  根据用户 ID 查询用户详细信息，包含部门和角色信息
// @Tags         用户管理
// @Accept       json
// @Produce      json
// @Param        id   path      uint64  true  "用户ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.UserResp}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "用户不存在"
// @Router       /users/{id} [get]
func (h *UserHandler) GetByID(c *gin.Context) {
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

// Create handles POST /api/v1/users — create a new user.
// @Summary      创建用户
// @Description  创建新用户，可同时关联角色
// @Tags         用户管理
// @Accept       json
// @Produce      json
// @Param        req  body      request.CreateUserReq  true  "创建用户请求"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "创建成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /users [post]
func (h *UserHandler) Create(c *gin.Context) {
	var req request.CreateUserReq
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

// UpdateUserInfo handles PUT /api/v1/users/:id — update a user's basic info.
// @Summary      更新用户信息
// @Description  仅更新用户基本信息（不含角色与状态；角色走 /users/:id/roles，状态走 enable/disable）
// @Tags         用户管理
// @Accept       json
// @Produce      json
// @Param        id   path      uint64               true  "用户ID"
// @Param        req  body      request.UpdateUserReq  true  "更新用户请求"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "更新成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "用户不存在"
// @Router       /users/{id} [put]
func (h *UserHandler) UpdateUserInfo(c *gin.Context) {
	var req request.UpdateUserReq
	if err := c.ShouldBindJSON(&req); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	req.ID = id
	if err := h.svc.UpdateUserInfo(c.Request.Context(), &req); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}

// AssignRoles handles PUT /api/v1/users/:id/roles — replace a user's roles.
// @Summary      分配用户角色
// @Description  替换目标用户的全部角色（不可修改自己的角色）
// @Tags         用户管理
// @Accept       json
// @Produce      json
// @Param        id    path      int                     true  "用户ID"
// @Param        req   body      request.AssignRolesReq  true  "角色 ID 列表"
// @Security     BearerAuth
// @Success      200   {object}  app.Response  "分配成功"
// @Failure      400   {object}  app.Response  "参数错误/不能修改自己的角色"
// @Failure      401   {object}  app.Response  "未登录"
// @Failure      403   {object}  app.Response  "无权限"
// @Failure      404   {object}  app.Response  "用户不存在"
// @Router       /users/{id}/roles [put]
func (h *UserHandler) AssignRoles(c *gin.Context) {
	var req request.AssignRolesReq
	if err := c.ShouldBindJSON(&req); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	if err := h.svc.AssignRoles(c.Request.Context(), id, []uint64(req.RoleIDs)); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}

// Delete handles DELETE /api/v1/users/:id — soft-delete a user.
// @Summary      删除用户
// @Description  软删除指定用户
// @Tags         用户管理
// @Accept       json
// @Produce      json
// @Param        id   path      uint64  true  "用户ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "删除成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "用户不存在"
// @Router       /users/{id} [delete]
func (h *UserHandler) Delete(c *gin.Context) {
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

// Enable handles POST /api/v1/users/:id/enable — enable a user.
// @Summary      启用用户
// @Description  启用指定用户
// @Tags         用户管理
// @Accept       json
// @Produce      json
// @Param        id   path      uint64  true  "用户ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "启用成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "用户不存在"
// @Router       /users/{id}/enable [post]
func (h *UserHandler) Enable(c *gin.Context) {
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

// Disable handles POST /api/v1/users/:id/disable — disable a user.
// @Summary      停用用户
// @Description  停用指定用户
// @Tags         用户管理
// @Accept       json
// @Produce      json
// @Param        id   path      uint64  true  "用户ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "停用成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "用户不存在"
// @Router       /users/{id}/disable [post]
func (h *UserHandler) Disable(c *gin.Context) {
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

// ResetPassword handles POST /api/v1/users/:id/password/reset — reset a user's password.
// @Summary      重置密码
// @Description  管理员重置指定用户的密码
// @Tags         用户管理
// @Accept       json
// @Produce      json
// @Param        id   path      uint64                    true  "用户ID"
// @Param        req  body      request.ResetPasswordReq  true  "重置密码请求"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "重置成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "用户不存在"
// @Router       /users/{id}/password/reset [post]
func (h *UserHandler) ResetPassword(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	var req request.ResetPasswordReq
	if err := c.ShouldBindJSON(&req); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	if err := h.svc.ResetPassword(c.Request.Context(), id, &req); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}

// Unlock handles POST /api/v1/users/:id/unlock — clear login lock for a user.
// @Summary      解锁用户
// @Description  清除指定用户的登录失败计数和锁定状态
// @Tags         用户管理
// @Accept       json
// @Produce      json
// @Param        id   path      uint64  true  "用户ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "解锁成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /users/{id}/unlock [post]
func (h *UserHandler) Unlock(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	if err := h.captchaSvc.Unlock(c.Request.Context(), id); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}

// ── 角色管理-分配用户(挂载在 /roles/:id/users,见 router 注册) ─────────

// ListByRole handles GET /api/v1/roles/:id/users — paginated user listing
// for a single role, reused by the role page's「分配用户」drawer.
// @Summary      角色的用户列表
// @Description  分页查询已分配指定角色的用户，支持按用户名筛选
// @Tags         角色管理
// @Accept       json
// @Produce      json
// @Param        id        path      uint64  true  "角色ID"
// @Param        page      query     int     false "页码"           default(1)
// @Param        pageSize  query     int     false "每页条数"       default(10)
// @Param        username  query     string  false "用户名(模糊查询)"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=app.PageResponse}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /roles/{id}/users [get]
func (h *UserHandler) ListByRole(c *gin.Context) {
	roleID, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	var query request.UserQuery
	if err := c.ShouldBindQuery(&query); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	query.RoleID = &roleID
	resp, err := h.svc.FindPage(c.Request.Context(), &query)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// AddRoleUsers handles POST /api/v1/roles/:id/users — add users to a role.
// @Summary      分配用户到角色
// @Description  将指定用户批量分配到角色（已分配的用户自动跳过）
// @Tags         角色管理
// @Accept       json
// @Produce      json
// @Param        id    path      uint64                true  "角色ID"
// @Param        req   body      request.RoleUsersReq  true  "用户ID列表"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "分配成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "角色不存在"
// @Router       /roles/{id}/users [post]
func (h *UserHandler) AddRoleUsers(c *gin.Context) {
	roleID, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	var req request.RoleUsersReq
	if err := c.ShouldBindJSON(&req); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	if err := h.svc.AddRoleUsers(c.Request.Context(), roleID, []uint64(req.UserIDs)); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}

// RemoveRoleUsers handles DELETE /api/v1/roles/:id/users — remove users from a role.
// @Summary      取消角色的用户分配
// @Description  批量移除角色的指定用户
// @Tags         角色管理
// @Accept       json
// @Produce      json
// @Param        id    path      uint64                true  "角色ID"
// @Param        req   body      request.RoleUsersReq  true  "用户ID列表"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "移除成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "角色不存在"
// @Router       /roles/{id}/users [delete]
func (h *UserHandler) RemoveRoleUsers(c *gin.Context) {
	roleID, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	var req request.RoleUsersReq
	if err := c.ShouldBindJSON(&req); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	if err := h.svc.RemoveRoleUsers(c.Request.Context(), roleID, []uint64(req.UserIDs)); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}
