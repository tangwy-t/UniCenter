package handler

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"
	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/database"
)

// SQLMonitorServiceInterface 由 handler/interfaces.go 迁移至此:接口定义在消费方(handler),
// 按需包含本 handler 实际调用的方法集,不再维护中央镜像文件。
type SQLMonitorServiceInterface interface {
	GetStats() *database.StatsSnapshot
	GetStatsWindow(window time.Duration) *database.StatsSnapshot
	GetHistory(ctx context.Context, window, step time.Duration) (*response.SQLHistorySnapshot, error)
}

// SQLMonitorHandler 暴露 SQL 监控 HTTP 端点。
type SQLMonitorHandler struct {
	svc SQLMonitorServiceInterface
}

// NewSQLMonitorHandler 创建 SQLMonitorHandler 实例。
func NewSQLMonitorHandler(svc SQLMonitorServiceInterface) *SQLMonitorHandler {
	return &SQLMonitorHandler{svc: svc}
}

// GetStats 处理 GET /api/v1/monitor/sql/stats
// 可选的 window 查询参数：1m, 5m, 15m, 1h 等
// @Summary      SQL监控统计
// @Description  返回 SQL 执行统计；可选 window 参数按时间窗口(如 5m,1h)聚合，留空返回累计统计
// @Tags         SQL监控
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        window  query     string  false  "时间窗口，如 5m,1h；留空返回累计统计"
// @Success      200     {object}  app.Response{data=response.SQLStatsSnapshot}  "查询成功"
// @Failure      400     {object}  app.Response  "window 格式无效"
// @Failure      401     {object}  app.Response  "未登录"
// @Failure      403     {object}  app.Response  "无权限"
// @Router       /monitor/sql/stats [get]
func (h *SQLMonitorHandler) GetStats(c *gin.Context) {
	windowStr := c.Query("window")

	if windowStr == "" {
		// 累计模式
		app.Success(c, toSQLStatsResponse(h.svc.GetStats()))
		return
	}

	window, err := time.ParseDuration(windowStr)
	if err != nil {
		app.Error(c, apperror.BadRequest("参数错误: window 格式无效，示例: 5m, 1h"))
		return
	}
	if window <= 0 {
		app.Error(c, apperror.BadRequest("参数错误: window 必须为正数"))
		return
	}

	app.Success(c, toSQLStatsResponse(h.svc.GetStatsWindow(window)))
}

// toSQLStatsResponse 把 database 内部统计快照映射为对外 DTO。
//
// 字段一一对应、无转换逻辑;映射存在的原因见 response/sql_stats.go:
// apigen 只解析 internal/model/dto/response,直接返回 database.StatsSnapshot
// 会让该接口的响应结构永远进不了 api.generated.d.ts。
func toSQLStatsResponse(snap *database.StatsSnapshot) *response.SQLStatsSnapshot {
	if snap == nil {
		return nil
	}
	out := &response.SQLStatsSnapshot{
		Global: response.SQLGlobalStats{
			Count:           snap.Global.Count,
			AvgMs:           snap.Global.AvgMs,
			MaxMs:           snap.Global.MaxMs,
			MinMs:           snap.Global.MinMs,
			P50Ms:           snap.Global.P50Ms,
			P95Ms:           snap.Global.P95Ms,
			P99Ms:           snap.Global.P99Ms,
			ErrorCount:      snap.Global.ErrorCount,
			SlowCount:       snap.Global.SlowCount,
			SlowThresholdMs: snap.Global.SlowThresholdMs,
		},
		ByTable:     toSQLDimStatsMap(snap.ByTable),
		ByOperation: toSQLDimStatsMap(snap.ByOperation),
		SlowQueries: make([]response.SQLQueryEntry, 0, len(snap.SlowQueries)),
	}
	for _, q := range snap.SlowQueries {
		out.SlowQueries = append(out.SlowQueries, response.SQLQueryEntry{
			Timestamp:  time.Time(q.Timestamp).Format("2006-01-02 15:04:05"),
			DurationMs: q.DurationMs,
			SQL:        q.SQL,
			Table:      q.Table,
			Operation:  q.Operation,
			IsError:    q.IsError,
			IsSlow:     q.IsSlow,
		})
	}
	return out
}

func toSQLDimStatsMap(in map[string]database.DimSnapshot) map[string]response.SQLDimStats {
	out := make(map[string]response.SQLDimStats, len(in))
	for k, v := range in {
		out[k] = response.SQLDimStats{
			Count: v.Count,
			AvgMs: v.AvgMs,
			MaxMs: v.MaxMs,
			MinMs: v.MinMs,
			P50Ms: v.P50Ms,
			P95Ms: v.P95Ms,
			P99Ms: v.P99Ms,
		}
	}
	return out
}

// GetHistory 处理 GET /api/v1/monitor/sql/history
// @Summary      SQL监控时序数据
// @Description  返回指定时间窗口内按 step 聚合的时序指标(QPS/平均耗时/P50/P95/P99/最大耗时/错误/慢查询)；数据来自 Redis 滚动窗口(3s 采样、保留 24h、跨重启),Redis 不可用时回退进程内统计
// @Tags         SQL监控
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        window  query     string  false  "时间窗口，如 5m,1h（默认 15m，范围 1m~24h）"
// @Param        step    query     string  false  "桶粒度，如 3s,10s（默认 window/180，最小 1s）"
// @Success      200     {object}  app.Response{data=response.SQLHistorySnapshot}  "查询成功"
// @Failure      400     {object}  app.Response  "参数无效"
// @Failure      401     {object}  app.Response  "未登录"
// @Failure      403     {object}  app.Response  "无权限"
// @Router       /monitor/sql/history [get]
func (h *SQLMonitorHandler) GetHistory(c *gin.Context) {
	windowStr := c.Query("window")
	var window time.Duration
	if windowStr == "" {
		window = 15 * time.Minute
	} else {
		parsed, err := time.ParseDuration(windowStr)
		if err != nil {
			app.Error(c, apperror.BadRequest("参数错误: window 格式无效，示例: 5m, 1h"))
			return
		}
		window = parsed
	}
	if window < time.Minute || window > 24*time.Hour {
		app.Error(c, apperror.BadRequest("参数错误: window 取值范围为 1m ~ 24h"))
		return
	}

	stepStr := c.Query("step")
	var step time.Duration
	if stepStr == "" {
		step = window / 180
		if step < time.Second {
			step = time.Second
		}
	} else {
		parsed, err := time.ParseDuration(stepStr)
		if err != nil || parsed < time.Second || parsed > window {
			app.Error(c, apperror.BadRequest("参数错误: step 格式无效，取值范围为 1s ~ window"))
			return
		}
		step = parsed
	}

	snap, err := h.svc.GetHistory(c.Request.Context(), window, step)
	if err != nil {
		app.Error(c, err)
		return
	}
	app.Success(c, snap)
}
