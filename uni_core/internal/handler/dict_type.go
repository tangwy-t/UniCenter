package handler

import (
	"context"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"

	"github.com/gin-gonic/gin"
)

// DictTypeServiceInterface 由 handler/interfaces.go 迁移至此:接口定义在消费方(handler),
// 按需包含本 handler 实际调用的方法集,不再维护中央镜像文件。
type DictTypeServiceInterface interface {
	FindPage(ctx context.Context, query *request.DictTypeQuery) (*app.PageResponse, error)
	FindByID(ctx context.Context, id uint64) (*response.DictTypeResp, error)
	CreateType(ctx context.Context, req *request.CreateDictTypeReq) (uint64, error)
	UpdateType(ctx context.Context, id uint64, req *request.UpdateDictTypeReq) error
	DeleteType(ctx context.Context, id uint64) error
}

// DictTypeHandler exposes HTTP handlers for dictionary type endpoints.
type DictTypeHandler struct {
	svc DictTypeServiceInterface
}

// NewDictTypeHandler creates a new DictTypeHandler.
func NewDictTypeHandler(svc DictTypeServiceInterface) *DictTypeHandler {
	return &DictTypeHandler{svc: svc}
}

// List handles GET /api/v1/dict/types — paginated dictionary type listing.
// @Summary      字典类型列表
// @Description  分页查询字典类型列表，支持按名称、编码、状态筛选
// @Tags         字典管理
// @Accept       json
// @Produce      json
// @Param        page      query     int     false  "页码"           default(1)
// @Param        pageSize  query     int     false  "每页条数"       default(10)
// @Param        name      query     string  false  "字典类型名称(模糊查询)"
// @Param        code      query     string  false  "字典类型编码(模糊查询)"
// @Param        status    query     int     false  "状态(0=禁用 1=启用)"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=app.PageResponse}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /dict/types [get]
func (h *DictTypeHandler) List(c *gin.Context) {
	var query request.DictTypeQuery
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

// GetByID handles GET /api/v1/dict/types/:id — get a single dictionary type.
// @Summary      字典类型详情
// @Description  根据字典类型 ID 查询详细信息
// @Tags         字典管理
// @Accept       json
// @Produce      json
// @Param        id   path      uint64  true  "字典类型ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.DictTypeResp}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "字典类型不存在"
// @Router       /dict/types/{id} [get]
func (h *DictTypeHandler) GetByID(c *gin.Context) {
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

// Create handles POST /api/v1/dict/types — create a dictionary type.
// @Summary      创建字典类型
// @Description  创建新的字典类型
// @Tags         字典管理
// @Accept       json
// @Produce      json
// @Param        req  body      request.CreateDictTypeReq  true  "创建字典类型请求"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "创建成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /dict/types [post]
func (h *DictTypeHandler) Create(c *gin.Context) {
	var req request.CreateDictTypeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	id, err := h.svc.CreateType(c.Request.Context(), &req)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, response.IDResp{ID: id})
}

// Update handles PUT /api/v1/dict/types/:id — update a dictionary type.
// @Summary      更新字典类型
// @Description  更新指定字典类型的信息
// @Tags         字典管理
// @Accept       json
// @Produce      json
// @Param        id   path      uint64                  true  "字典类型ID"
// @Param        req  body      request.UpdateDictTypeReq  true  "更新字典类型请求"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "更新成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "字典类型不存在"
// @Router       /dict/types/{id} [put]
func (h *DictTypeHandler) Update(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	var req request.UpdateDictTypeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	if err := h.svc.UpdateType(c.Request.Context(), id, &req); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}

// Delete handles DELETE /api/v1/dict/types/:id — delete a dictionary type.
// @Summary      删除字典类型
// @Description  软删除指定字典类型
// @Tags         字典管理
// @Accept       json
// @Produce      json
// @Param        id   path      uint64  true  "字典类型ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "删除成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "字典类型不存在"
// @Router       /dict/types/{id} [delete]
func (h *DictTypeHandler) Delete(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	if err := h.svc.DeleteType(c.Request.Context(), id); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}
