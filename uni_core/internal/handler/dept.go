package handler

import (
	"context"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"

	"github.com/gin-gonic/gin"
)

// DeptServiceInterface 由 handler/interfaces.go 迁移至此:接口定义在消费方(handler),
// 按需包含本 handler 实际调用的方法集,不再维护中央镜像文件。
type DeptServiceInterface interface {
	FindTree(ctx context.Context, query request.DeptQuery) ([]response.DeptResp, error)
	FindByID(ctx context.Context, id uint64) (*response.DeptResp, error)
	Create(ctx context.Context, req *request.CreateDeptReq) (uint64, error)
	Update(ctx context.Context, req *request.UpdateDeptReq) error
	Delete(ctx context.Context, id uint64) error
	UpdateSort(ctx context.Context, req *request.UpdateDeptSortReq) error
}

// DeptHandler exposes HTTP handlers for department management endpoints.
type DeptHandler struct {
	svc DeptServiceInterface
}

// NewDeptHandler creates a new DeptHandler.
func NewDeptHandler(svc DeptServiceInterface) *DeptHandler {
	return &DeptHandler{svc: svc}
}

// FindTree handles GET /api/v1/depts — returns the full department tree.
// @Summary      部门树
// @Description  获取完整的部门树结构（支持名称模糊、状态精确筛选）
// @Tags         部门管理
// @Accept       json
// @Produce      json
// @Param        name    query     string  false  "部门名称（模糊）"
// @Param        status  query     int8    false  "状态（0停用 1启用）"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=[]response.DeptResp}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /depts [get]
func (h *DeptHandler) FindTree(c *gin.Context) {
	var query request.DeptQuery
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

// GetByID handles GET /api/v1/depts/:id — single department detail.
// @Summary      部门详情
// @Description  根据部门 ID 查询部门详细信息
// @Tags         部门管理
// @Accept       json
// @Produce      json
// @Param        id   path      uint64  true  "部门ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.DeptResp}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "部门不存在"
// @Router       /depts/{id} [get]
func (h *DeptHandler) GetByID(c *gin.Context) {
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

// Create handles POST /api/v1/depts — create a new department.
// @Summary      创建部门
// @Description  创建新部门，自动维护 ancestors 字段
// @Tags         部门管理
// @Accept       json
// @Produce      json
// @Param        req  body      request.CreateDeptReq  true  "创建部门请求"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "创建成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /depts [post]
func (h *DeptHandler) Create(c *gin.Context) {
	var req request.CreateDeptReq
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

// Update handles PUT /api/v1/depts/:id — update an existing department.
// @Summary      更新部门
// @Description  更新指定部门的信息，移动部门时自动更新 ancestors
// @Tags         部门管理
// @Accept       json
// @Produce      json
// @Param        id   path      uint64               true  "部门ID"
// @Param        req  body      request.UpdateDeptReq  true  "更新部门请求"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "更新成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "部门不存在"
// @Router       /depts/{id} [put]
func (h *DeptHandler) Update(c *gin.Context) {
	var req request.UpdateDeptReq
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

// Delete handles DELETE /api/v1/depts/:id — soft-delete a department.
// @Summary      删除部门
// @Description  软删除指定部门（不允许删除有子部门或关联用户的部门）
// @Tags         部门管理
// @Accept       json
// @Produce      json
// @Param        id   path      uint64  true  "部门ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "删除成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "部门不存在"
// @Router       /depts/{id} [delete]
func (h *DeptHandler) Delete(c *gin.Context) {
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

// UpdateSort handles PUT /api/v1/depts/sort — batch save sort values.
// @Summary      保存排序
// @Description  批量提交部门排序值（直接覆盖赋值，不触发精确落位的兄弟搬移）
// @Tags         部门管理
// @Accept       json
// @Produce      json
// @Param        req  body      request.UpdateDeptSortReq  true  "排序请求"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "保存成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "部门不存在"
// @Router       /depts/sort [put]
func (h *DeptHandler) UpdateSort(c *gin.Context) {
	var req request.UpdateDeptSortReq
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
