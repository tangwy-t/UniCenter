package handler

import (
	"context"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"

	"github.com/gin-gonic/gin"
)

// MenuServiceInterface 由 handler/interfaces.go 迁移至此:接口定义在消费方(handler),
// 按需包含本 handler 实际调用的方法集,不再维护中央镜像文件。
type MenuServiceInterface interface {
	FindTree(ctx context.Context, query request.MenuQuery) ([]response.MenuResp, error)
	FindByID(ctx context.Context, id uint64) (*response.MenuResp, error)
	Create(ctx context.Context, req *request.CreateMenuReq) (uint64, error)
	Update(ctx context.Context, req *request.UpdateMenuReq) error
	Delete(ctx context.Context, id uint64) error
	UpdateSort(ctx context.Context, req *request.UpdateMenuSortReq) error
}

// MenuHandler exposes HTTP handlers for menu management endpoints.
type MenuHandler struct {
	svc MenuServiceInterface
}

// NewMenuHandler creates a new MenuHandler.
func NewMenuHandler(svc MenuServiceInterface) *MenuHandler {
	return &MenuHandler{svc: svc}
}

// FindTree handles GET /api/v1/menus — the menu tree.
// 白名单接口(登录即可):路由守卫(backend 模式下全员调用)与角色授权表单
// 均消费此树,不能挂菜单管理权限码。可见范围由数据范围自动收缩——
// sys_menu 的 role 维度规则使 admin 得全树、普通用户仅得角色已分配菜单
// (等价若依 selectMenuTreeByUserId/treeselect,登录即可)。写操作仍挂权限码。
// @Summary      菜单树
// @Description  获取菜单树结构（目录→菜单→按钮三级，按当前用户数据范围过滤）
// @Tags         菜单管理
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=[]response.MenuResp}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Router       /menus [get]
func (h *MenuHandler) FindTree(c *gin.Context) {
	var query request.MenuQuery
	if err := c.ShouldBindQuery(&query); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	tree, err := h.svc.FindTree(c.Request.Context(), query)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, tree)
}

// GetByID handles GET /api/v1/menus/:id — single menu detail.
// @Summary      菜单详情
// @Description  根据菜单 ID 查询菜单详细信息
// @Tags         菜单管理
// @Accept       json
// @Produce      json
// @Param        id   path      uint64  true  "菜单ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.MenuResp}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "菜单不存在"
// @Router       /menus/{id} [get]
func (h *MenuHandler) GetByID(c *gin.Context) {
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

// Create handles POST /api/v1/menus — create a new menu.
// @Summary      创建菜单
// @Description  创建新菜单（目录、菜单或按钮），支持三级树形结构
// @Tags         菜单管理
// @Accept       json
// @Produce      json
// @Param        req  body      request.CreateMenuReq  true  "创建菜单请求"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "创建成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /menus [post]
func (h *MenuHandler) Create(c *gin.Context) {
	var req request.CreateMenuReq
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

// Update handles PUT /api/v1/menus/:id — update an existing menu.
// @Summary      更新菜单
// @Description  更新指定菜单的信息
// @Tags         菜单管理
// @Accept       json
// @Produce      json
// @Param        id   path      uint64               true  "菜单ID"
// @Param        req  body      request.UpdateMenuReq  true  "更新菜单请求"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "更新成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "菜单不存在"
// @Router       /menus/{id} [put]
func (h *MenuHandler) Update(c *gin.Context) {
	var req request.UpdateMenuReq
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

// Delete handles DELETE /api/v1/menus/:id — soft-delete a menu.
// @Summary      删除菜单
// @Description  软删除指定菜单（不允许删除有子节点的菜单）
// @Tags         菜单管理
// @Accept       json
// @Produce      json
// @Param        id   path      uint64  true  "菜单ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "删除成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "菜单不存在"
// @Router       /menus/{id} [delete]
func (h *MenuHandler) Delete(c *gin.Context) {
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

// UpdateSort handles PUT /api/v1/menus/sort — batch save sort values.
// @Summary      保存排序
// @Description  批量提交菜单排序值（直接覆盖赋值，不触发精确落位的兄弟搬移）
// @Tags         菜单管理
// @Accept       json
// @Produce      json
// @Param        req  body      request.UpdateMenuSortReq  true  "排序请求"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "保存成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /menus/sort [put]
func (h *MenuHandler) UpdateSort(c *gin.Context) {
	var req request.UpdateMenuSortReq
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
