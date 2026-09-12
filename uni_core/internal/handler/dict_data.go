package handler

import (
	"context"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"

	"github.com/gin-gonic/gin"
)

// DictDataServiceInterface 由 handler/interfaces.go 迁移至此:接口定义在消费方(handler),
// 按需包含本 handler 实际调用的方法集,不再维护中央镜像文件。
type DictDataServiceInterface interface {
	FindDataByType(ctx context.Context, typeID uint64) ([]response.DictDataResp, error)
	FindDataByID(ctx context.Context, id uint64) (*response.DictDataResp, error)
	CreateData(ctx context.Context, typeID uint64, req *request.CreateDictDataReq) (uint64, error)
	UpdateData(ctx context.Context, id uint64, req *request.UpdateDictDataReq) error
	DeleteData(ctx context.Context, id uint64) error
}

// DictDataHandler exposes HTTP handlers for dictionary data endpoints.
type DictDataHandler struct {
	svc DictDataServiceInterface
}

// NewDictDataHandler creates a new DictDataHandler.
func NewDictDataHandler(svc DictDataServiceInterface) *DictDataHandler {
	return &DictDataHandler{svc: svc}
}

// ListByType handles GET /api/v1/dict/types/:id/data — list data entries for a type.
// @Summary      字典数据列表
// @Description  根据字典类型 ID 查询该类型下的所有字典数据
// @Tags         字典管理
// @Accept       json
// @Produce      json
// @Param        id   path      uint64  true  "字典类型ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=[]response.DictDataResp}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /dict/types/{id}/data [get]
func (h *DictDataHandler) ListByType(c *gin.Context) {
	typeID, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	resp, err := h.svc.FindDataByType(c.Request.Context(), typeID)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// GetByID handles GET /api/v1/dict/types/:id/data/:dataId — get a single data entry.
// @Summary      字典数据详情
// @Description  根据字典数据 ID 查询详细信息
// @Tags         字典管理
// @Accept       json
// @Produce      json
// @Param        id      path  uint64  true  "字典类型ID"
// @Param        dataId  path  uint64  true  "字典数据ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.DictDataResp}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "字典数据不存在"
// @Router       /dict/types/{id}/data/{dataId} [get]
func (h *DictDataHandler) GetByID(c *gin.Context) {
	dataID, ok := app.Uint64Param(c, "dataId")
	if !ok {
		return
	}
	resp, err := h.svc.FindDataByID(c.Request.Context(), dataID)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// Create handles POST /api/v1/dict/types/:id/data — create a data entry.
// @Summary      创建字典数据
// @Description  在指定字典类型下创建新的字典数据
// @Tags         字典管理
// @Accept       json
// @Produce      json
// @Param        id   path      uint64                   true  "字典类型ID"
// @Param        req  body      request.CreateDictDataReq  true  "创建字典数据请求"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "创建成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /dict/types/{id}/data [post]
func (h *DictDataHandler) Create(c *gin.Context) {
	typeID, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	var req request.CreateDictDataReq
	if err := c.ShouldBindJSON(&req); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	id, err := h.svc.CreateData(c.Request.Context(), typeID, &req)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, response.IDResp{ID: id})
}

// Update handles PUT /api/v1/dict/types/:id/data/:dataId — update a data entry.
// @Summary      更新字典数据
// @Description  更新指定字典数据的信息
// @Tags         字典管理
// @Accept       json
// @Produce      json
// @Param        id      path  uint64                   true  "字典类型ID"
// @Param        dataId  path  uint64                   true  "字典数据ID"
// @Param        req     body  request.UpdateDictDataReq  true  "更新字典数据请求"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "更新成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "字典数据不存在"
// @Router       /dict/types/{id}/data/{dataId} [put]
func (h *DictDataHandler) Update(c *gin.Context) {
	dataID, ok := app.Uint64Param(c, "dataId")
	if !ok {
		return
	}
	var req request.UpdateDictDataReq
	if err := c.ShouldBindJSON(&req); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	if err := h.svc.UpdateData(c.Request.Context(), dataID, &req); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}

// Delete handles DELETE /api/v1/dict/types/:id/data/:dataId — delete a data entry.
// @Summary      删除字典数据
// @Description  软删除指定字典数据
// @Tags         字典管理
// @Accept       json
// @Produce      json
// @Param        id      path  uint64  true  "字典类型ID"
// @Param        dataId  path  uint64  true  "字典数据ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "删除成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "字典数据不存在"
// @Router       /dict/types/{id}/data/{dataId} [delete]
func (h *DictDataHandler) Delete(c *gin.Context) {
	dataID, ok := app.Uint64Param(c, "dataId")
	if !ok {
		return
	}
	if err := h.svc.DeleteData(c.Request.Context(), dataID); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}
