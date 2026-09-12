package handler

import (
	"bytes"
	"context"
	"math"
	"net/http/pprof"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"
	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	pproftext "github.com/tangwy-t/webmanager-server/internal/pkg/pproftext"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// PprofConfigInterface 由 handler/interfaces.go 迁移至此:接口定义在消费方(handler),
// 按需包含本 handler 实际调用的方法集,不再维护中央镜像文件。
type PprofConfigInterface interface {
	SetBool(ctx context.Context, key string, value bool) error
	GetBool(ctx context.Context, key string, defaultVal bool) bool
	GetInt(ctx context.Context, key string, defaultVal int) int
}

// PprofHandler 管理 pprof 的动态启停,并提供 UI 渲染所需的
// 状态概览与火焰图数据接口。
type PprofHandler struct {
	configProv PprofConfigInterface
	logger     logger.LoggerInterface
	mu         sync.Mutex
	timer      *time.Timer
	offAt      time.Time // 自动关闭截止时刻;零值表示无自动关闭排程
}

// NewPprofHandler 创建 PprofHandler 实例。
// autoOffSeconds 在 HandleEnable 时现读,不在构造期快照:配置热更后
// 无需重启即生效。
func NewPprofHandler(configProv PprofConfigInterface, logger logger.LoggerInterface) *PprofHandler {
	return &PprofHandler{
		configProv: configProv,
		logger:     logger,
	}
}

// HandleEnable 处理 POST /api/v1/monitor/pprof/enable 请求，启用 pprof。
// @Summary      启用pprof
// @Description  动态启用 pprof 采集；若配置了 autoOffSeconds，将在超时后自动关闭
// @Tags         pprof
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=map[string]interface{}}  "启用成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /monitor/pprof/enable [post]
func (h *PprofHandler) HandleEnable(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// 持久化开关(真实状态源是配置键,不在内存)
	if err := h.configProv.SetBool(context.Background(), "sys.pprof.enabled", true); err != nil {
		h.logger.Error("pprof enable: persist failed", zap.Error(err))
		app.Error(c, err)
		return
	}

	// 启动 auto-off timer(时长现读,配置热更后下次 enable 即生效)
	autoOffSec := h.configProv.GetInt(context.Background(), "sys.pprof.autoOffSeconds", 0)
	if autoOffSec > 0 {
		if h.timer != nil {
			h.timer.Stop()
		}
		h.offAt = time.Now().Add(time.Duration(autoOffSec) * time.Second)
		h.timer = time.AfterFunc(time.Duration(autoOffSec)*time.Second, func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			if err := h.configProv.SetBool(context.Background(), "sys.pprof.enabled", false); err != nil {
				h.logger.Error("pprof auto-off: persist failed, pprof stays enabled", zap.Error(err))
				return
			}
			h.timer = nil
			h.offAt = time.Time{}
			h.logger.Info("pprof auto-disabled by timer", zap.Int("timeoutSec", autoOffSec))
		})
	} else {
		h.offAt = time.Time{}
	}

	h.logger.Info("pprof enabled via API")
	app.Success(c, gin.H{"enabled": true})
}

// HandleDisable 处理 POST /api/v1/monitor/pprof/disable 请求，禁用 pprof。
// @Summary      禁用pprof
// @Description  动态禁用 pprof 采集，并停止自动关闭计时器
// @Tags         pprof
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=map[string]interface{}}  "禁用成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /monitor/pprof/disable [post]
// AdaptPprof 将 pprof.Index 适配为 gin.HandlerFunc,修正标准库分派
// 前缀不匹配问题。Go pprof.Index 仅当 URL.Path 以 "/debug/pprof/" 开头
// 才分派 profile handler(cutPrefix 判定)。挂载于自定义前缀时
// (如 /api/v1/monitor/debug/pprof)所有子路径一律回落索引 HTML,
// heap/goroutine/profile 等数据完全不可达。此适配器在运行时扫描 Path 中
// "/pprof" 位置并重写为 /debug/pprof/<name>,再委托给标准库。
//
// 另注意 Go 1.26 起 net/http/pprof.Handler 只按 runtime profile 名
// 查 pprof.Lookup,不再特殊分派 cmdline/profile/symbol/trace——
// Index 的动态分派对这 4 个端点会落入 Lookup 兜底返回
// 404 "Unknown profile"。因此这里显式调用标准库的专用处理器。
func AdaptPprof() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		if idx := strings.Index(path, "/pprof"); idx >= 0 {
			c.Request.URL.Path = "/debug/pprof" + path[idx+len("/pprof"):]
		}
		if name, ok := strings.CutPrefix(c.Request.URL.Path, "/debug/pprof/"); ok {
			switch name {
			case "cmdline":
				pprof.Cmdline(c.Writer, c.Request)
				return
			case "profile":
				pprof.Profile(c.Writer, c.Request)
				return
			case "symbol":
				pprof.Symbol(c.Writer, c.Request)
				return
			case "trace":
				pprof.Trace(c.Writer, c.Request)
				return
			}
		}
		pprof.Index(c.Writer, c.Request)
	}
}

func (h *PprofHandler) HandleDisable(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if err := h.configProv.SetBool(context.Background(), "sys.pprof.enabled", false); err != nil {
		h.logger.Error("pprof disable: persist failed", zap.Error(err))
		app.Error(c, err)
		return
	}

	if h.timer != nil {
		h.timer.Stop()
		h.timer = nil
	}
	h.offAt = time.Time{}

	h.logger.Info("pprof disabled via API")
	app.Success(c, gin.H{"enabled": false})
}

// HandleStatus 处理 GET /api/v1/monitor/pprof/status 请求,返回启用状态、
// 自动关闭倒计时与 profile 概览,供前端页头/控制区渲染。
// 该接口始终可访问(仅报告状态);profile 数据接口另行受 enabled 开关保护。
// @Summary      查询pprof状态
// @Description  返回启用状态、自动关闭倒计时与各 profile 概览
// @Tags         pprof
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  app.Response{data=response.PprofStatusResponse}  "查询成功"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Router       /monitor/pprof/status [get]
func (h *PprofHandler) HandleStatus(c *gin.Context) {
	enabled := h.configProv.GetBool(context.Background(), "sys.pprof.enabled", false)
	autoOffSec := h.configProv.GetInt(context.Background(), "sys.pprof.autoOffSeconds", 0)

	resp := response.PprofStatusResponse{
		Enabled:        enabled,
		AutoOffSeconds: autoOffSec,
		Profiles:       pproftext.StatusEntries(),
	}
	if enabled {
		h.mu.Lock()
		if !h.offAt.IsZero() && h.offAt.After(time.Now()) {
			resp.RemainingSeconds = int(math.Ceil(time.Until(h.offAt).Seconds()))
		}
		h.mu.Unlock()
	}
	app.Success(c, resp)
}

// HandleProfileFlame 处理 GET /api/v1/monitor/pprof/profile/:name 请求,
// 解析指定 profile 的快照文本,返回火焰图树与热点函数表(top)。
// 该路由组挂载 PprofGuard:pprof 未启用时返回 404,与原始数据端点行为一致。
// @Summary      解析pprof时profile数据
// @Description  返回指定 profile 的火焰图树与热点函数表,支持 top 参数(默认15,5~50)
// @Tags         pprof
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        name   path      string  true  "profile 名:goroutine/heap/allocs/block/mutex/threadcreate"
// @Param        top    query     int     false "热点函数行数(默认15)"
// @Success      200  {object}  app.Response{data=response.PprofProfileResponse}  "解析成功"
// @Failure      400  {object}  app.Response  "不支持的 profile"
// @Failure      401  {object}  app.Response  "未登录"
// @Failure      403  {object}  app.Response  "无权限"
// @Failure      404  {object}  app.Response  "pprof 未启用"
// @Router       /monitor/pprof/profile/{name} [get]
func (h *PprofHandler) HandleProfileFlame(c *gin.Context) {
	name := strings.TrimSpace(c.Param("name"))
	def, ok := pproftext.DefFor(name)
	if !ok {
		app.Error(c, apperror.BadRequest("不支持的 profile: "+name))
		return
	}
	if def.Category != "snapshot" {
		app.Error(c, apperror.BadRequest("不支持的 profile: "+name+
			"(仅快照类支持火焰图解析;采集类请 GET /api/v1/monitor/debug/pprof/"+name+"?seconds=N 下载原始数据)"))
		return
	}
	if def.Lookup == "" {
		app.Error(c, apperror.BadRequest("不支持的 profile: "+name))
		return
	}

	top := 15
	if raw := c.Query("top"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			top = min(max(n, 5), 50)
		}
	}

	p := pproftext.Lookup(def.Lookup)
	if p == nil {
		app.Error(c, apperror.BadRequest("profile 不存在: "+name))
		return
	}

	var buf bytes.Buffer
	if err := p.WriteTo(&buf, 1); err != nil {
		h.logger.Error("pprof profile dump failed", zap.String("name", name), zap.Error(err))
		app.Error(c, err)
		return
	}
	app.Success(c, pproftext.BuildProfile(def, buf.Bytes(), top))
}
