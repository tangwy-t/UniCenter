package handler

import (
	"context"
	"strings"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"
	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"

	"github.com/gin-gonic/gin"
)

// DictCodeServiceInterface 由 handler/interfaces.go 迁移至此:接口定义在消费方(handler),
// 按需包含本 handler 实际调用的方法集,不再维护中央镜像文件。
type DictCodeServiceInterface interface {
	FindDataByCode(ctx context.Context, code string) ([]response.DictItem, error)
	FindDataByCodes(ctx context.Context, codes []string) (map[string][]response.DictItem, error)
}

// DictCodeHandler exposes HTTP handlers for dictionary code consumption.
type DictCodeHandler struct {
	svc DictCodeServiceInterface
}

// NewDictCodeHandler creates a new DictCodeHandler.
func NewDictCodeHandler(svc DictCodeServiceInterface) *DictCodeHandler {
	return &DictCodeHandler{svc: svc}
}

// GetByCode handles GET /api/v1/dict/codes/:code
// Returns all enabled dictionary items for the given type code.
// @Summary      按编码查字典项
// @Description  根据字典类型编码返回该类型下所有启用的字典项
// @Tags         字典查询
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        code  path      string  true  "字典类型编码"
// @Success      200   {object}  app.Response{data=[]response.DictItem}  "查询成功"
// @Failure      400   {object}  app.Response  "字典编码不能为空"
// @Failure      500   {object}  app.Response  "服务内部错误"
// @Router       /dict/codes/{code} [get]
func (h *DictCodeHandler) GetByCode(c *gin.Context) {
	code := c.Param("code")
	if code == "" {
		app.Error(c, apperror.BadRequest("字典编码不能为空"))
		return
	}
	items, err := h.svc.FindDataByCode(c.Request.Context(), code)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, items)
}

// GetByCodes handles GET /api/v1/dict/codes?codes=a,b
// Returns items for multiple type codes at once.
// @Summary      批量按编码查字典项
// @Description  传入多个字典类型编码(逗号分隔)，一次性返回各类型对应的字典项映射
// @Tags         字典查询
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        codes  query     string  true  "字典类型编码，逗号分隔(如 user_type,order_status)"
// @Success      200    {object}  app.Response{data=map[string]interface{}}  "查询成功"
// @Failure      400    {object}  app.Response  "codes 参数不能为空"
// @Failure      500    {object}  app.Response  "服务内部错误"
// @Router       /dict/codes [get]
func (h *DictCodeHandler) GetByCodes(c *gin.Context) {
	codesParam := c.Query("codes")
	if codesParam == "" {
		app.Error(c, apperror.BadRequest("codes 参数不能为空"))
		return
	}
	codes := strings.Split(codesParam, ",")
	filtered := make([]string, 0, len(codes))
	for _, code := range codes {
		trimmed := strings.TrimSpace(code)
		if trimmed != "" {
			filtered = append(filtered, trimmed)
		}
	}
	if len(filtered) == 0 {
		app.Error(c, apperror.BadRequest("codes 参数不能为空"))
		return
	}
	result, err := h.svc.FindDataByCodes(c.Request.Context(), filtered)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, result)
}
