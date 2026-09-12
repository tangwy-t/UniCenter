package handler

import (
	"context"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"
	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"

	"github.com/gin-gonic/gin"
)

// ConfigServiceInterface 由 handler/interfaces.go 迁移至此:接口定义在消费方(handler),
// 按需包含本 handler 实际调用的方法集,不再维护中央镜像文件。
type ConfigServiceInterface interface {
	FindPage(ctx context.Context, query *request.ConfigQuery) (*app.PageResponse, error)
	FindByID(ctx context.Context, id uint64) (*response.ConfigResp, error)
	GetByKey(ctx context.Context, key string) (string, error)
	Create(ctx context.Context, req *request.CreateConfigReq) (uint64, error)
	Update(ctx context.Context, id uint64, req *request.UpdateConfigReq) error
	Delete(ctx context.Context, id uint64) error
}

type ConfigHandler struct {
	svc ConfigServiceInterface
}

func NewConfigHandler(svc ConfigServiceInterface) *ConfigHandler {
	return &ConfigHandler{svc: svc}
}

// List handles GET /api/v1/configs — paginated config listing.
// @Summary      参数配置列表
// @Description  分页查询参数配置列表，支持按配置键筛选
// @Tags         参数配置
// @Accept       json
// @Produce      json
// @Param        page       query  int     false  "页码"           default(1)
// @Param        pageSize   query  int     false  "每页条数"       default(10)
// @Param        configKey  query  string  false  "配置键(模糊查询)"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=app.PageResponse}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /configs [get]
func (h *ConfigHandler) List(c *gin.Context) {
	var query request.ConfigQuery
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

// GetByID handles GET /api/v1/configs/:id — get a single config.
// @Summary      参数配置详情
// @Description  根据配置 ID 查询参数配置详细信息
// @Tags         参数配置
// @Accept       json
// @Produce      json
// @Param        id   path      uint64  true  "配置ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.ConfigResp}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "参数配置不存在"
// @Router       /configs/{id} [get]
func (h *ConfigHandler) GetByID(c *gin.Context) {
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

// GetByKey handles GET /api/v1/configs/key/:key — get config value by key.
// @Summary      按键获取配置
// @Description  根据配置键直接获取配置值
// @Tags         参数配置
// @Accept       json
// @Produce      json
// @Param        key  path      string  true  "配置键"
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=string}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /configs/key/{key} [get]
func (h *ConfigHandler) GetByKey(c *gin.Context) {
	key := c.Param("key")
	val, err := h.svc.GetByKey(c.Request.Context(), key)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, val)
}

// Create handles POST /api/v1/configs — create a config.
// @Summary      创建参数配置
// @Description  创建新的参数配置
// @Tags         参数配置
// @Accept       json
// @Produce      json
// @Param        req  body      request.CreateConfigReq  true  "创建参数配置请求"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "创建成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /configs [post]
func (h *ConfigHandler) Create(c *gin.Context) {
	var req request.CreateConfigReq
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

// Update handles PUT /api/v1/configs/:id — update a config.
// @Summary      更新参数配置
// @Description  更新指定参数配置的信息
// @Tags         参数配置
// @Accept       json
// @Produce      json
// @Param        id   path      uint64                 true  "配置ID"
// @Param        req  body      request.UpdateConfigReq  true  "更新参数配置请求"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "更新成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "参数配置不存在"
// @Router       /configs/{id} [put]
func (h *ConfigHandler) Update(c *gin.Context) {
	id, ok := app.Uint64Param(c, "id")
	if !ok {
		return
	}
	var req request.UpdateConfigReq
	if err := c.ShouldBindJSON(&req); err != nil {
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}
	if err := h.svc.Update(c.Request.Context(), id, &req); err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, nil)
}

// Delete handles DELETE /api/v1/configs/:id — delete a config.
// @Summary      删除参数配置
// @Description  软删除指定参数配置
// @Tags         参数配置
// @Accept       json
// @Produce      json
// @Param        id   path      uint64  true  "配置ID"
// @Security     BearerAuth
// @Success      200  {object}  app.Response  "删除成功"
// @Failure      400  {object}  app.Response  "参数错误"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "参数配置不存在"
// @Router       /configs/{id} [delete]
func (h *ConfigHandler) Delete(c *gin.Context) {
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
