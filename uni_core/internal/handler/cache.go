package handler

import (
	"context"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"
	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/redis/cache"

	"github.com/gin-gonic/gin"
)

// CacheServiceInterface 定义在消费方:HTTP 语义翻译与日志已下沉
// service.CacheService,handler 只做参数绑定与响应写出。
type CacheServiceInterface interface {
	ListKeys(ctx context.Context, req *request.ListKeysRequest) (*response.ListKeysResponse, error)
	GetKeyValue(ctx context.Context, req *request.GetKeyValueRequest) (*response.CacheValuePage, error)
	DeleteKeys(ctx context.Context, req *request.DeleteKeysRequest) (*response.DeleteKeysResponse, error)
	GetStats(ctx context.Context) (*cache.Stats, error)
}

// CacheHandler exposes HTTP handlers for Redis cache management.
type CacheHandler struct {
	svc CacheServiceInterface
}

// NewCacheHandler creates a CacheHandler backed by the cache service.
func NewCacheHandler(svc CacheServiceInterface) *CacheHandler {
	return &CacheHandler{svc: svc}
}

// ── ListKeys: GET /monitor/cache/keys ─────────────────────────────────

// ListKeys handles GET /api/v1/monitor/cache/keys.
// @Summary      缓存key列表
// @Description  按 prefix 模糊扫描 Redis key，支持游标分页
// @Tags         缓存管理
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        req   query     request.ListKeysRequest  true  "查询参数"
// @Success      200   {object}  app.Response{data=response.ListKeysResponse}  "查询成功"
// @Failure      400   {object}  app.Response  "参数错误"
// @Failure      500   {object}  app.Response  "服务内部错误"
// @Router       /monitor/cache/keys [get]
func (h *CacheHandler) ListKeys(c *gin.Context) {
	var req request.ListKeysRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		// 不回传 validator 原始错误：其中包含 Go 字段路径等内部结构。
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}

	resp, err := h.svc.ListKeys(c.Request.Context(), &req)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// ── GetKeyValue: GET /monitor/cache/keys/value ────────────────────────

// GetKeyValue handles GET /api/v1/monitor/cache/keys/value.
// @Summary      缓存key值
// @Description  按 value 类型分页查询 key 的值与 TTL：list/zset 用 offset+limit 分页，
// @Description  set/hash 用 cursor+limit 游标分页，string 用 limit 作字节窗；TTL=-2 表示 key 不存在
// @Tags         缓存管理
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        key      query     string  true   "目标缓存 key"
// @Param        offset   query     int64   false  "list/zset 起始下标、string 起始字节"  minimum(0)
// @Param        limit    query     int64   false  "list/zset/set/hash 每页条数(≤200)、string 字节窗(≤4MB)"  minimum(1) maximum(4194304)
// @Param        cursor   query     uint64  false  "set/hash 的 SCAN 游标，0 表示从头扫，续扫传上页 next_cursor"  minimum(0)
// @Success      200   {object}  app.Response{data=response.CacheValuePage}  "查询成功"
// @Failure      400   {object}  app.Response  "参数错误"
// @Failure      404   {object}  app.Response  "key 不存在"
// @Failure      500   {object}  app.Response  "服务内部错误"
// @Router       /monitor/cache/keys/value [get]
func (h *CacheHandler) GetKeyValue(c *gin.Context) {
	req := request.GetKeyValueRequest{Limit: 100}
	if err := c.ShouldBindQuery(&req); err != nil {
		// 不回传 validator 原始错误：其中包含 Go 字段路径等内部结构。
		app.Error(c, apperror.BadRequest("参数错误"))
		return
	}

	resp, err := h.svc.GetKeyValue(c.Request.Context(), &req)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// ── DeleteKeys: DELETE /monitor/cache/keys ────────────────────────────

// DeleteKeys handles DELETE /api/v1/monitor/cache/keys.
// @Summary      批量删除缓存key
// @Description  按 prefix 模式批量删除 Redis key，受 maxCount 限制（上限 10000）
// @Tags         缓存管理
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        req   query     request.DeleteKeysRequest  true  "删除参数"
// @Success      200   {object}  app.Response{data=response.DeleteKeysResponse}  "删除成功"
// @Failure      400   {object}  app.Response  "参数错误"
// @Failure      500   {object}  app.Response  "服务内部错误"
// @Router       /monitor/cache/keys [delete]
func (h *CacheHandler) DeleteKeys(c *gin.Context) {
	var req request.DeleteKeysRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		app.Error(c, apperror.BadRequest("参数错误: prefix 不能为空"))
		return
	}

	resp, err := h.svc.DeleteKeys(c.Request.Context(), &req)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, resp)
}

// ── GetStats: GET /monitor/cache/stats ────────────────────────────────

// GetStats handles GET /api/v1/monitor/cache/stats.
// @Summary      缓存统计
// @Description  返回 Redis 连接与内存等统计信息
// @Tags         缓存管理
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Success      200   {object}  app.Response{data=cache.Stats}  "查询成功"
// @Failure      500   {object}  app.Response  "服务内部错误"
// @Router       /monitor/cache/stats [get]
func (h *CacheHandler) GetStats(c *gin.Context) {
	stats, err := h.svc.GetStats(c.Request.Context())
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, stats)
}
