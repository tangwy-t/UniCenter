package router

import (
	_ "embed"
	"github.com/tangwy-t/webmanager-server/internal/pkg/datascope"
	"github.com/tangwy-t/webmanager-server/internal/pkg/limiter"
	"net/http"

	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"github.com/tangwy-t/webmanager-server/docs"
	"github.com/tangwy-t/webmanager-server/internal/handler"
	"github.com/tangwy-t/webmanager-server/internal/middleware"
	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
	"github.com/tangwy-t/webmanager-server/internal/pkg/config"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"github.com/tangwy-t/webmanager-server/internal/pkg/permission"
	"github.com/tangwy-t/webmanager-server/internal/pkg/session"
	"github.com/tangwy-t/webmanager-server/internal/pkg/version"
	wsPkg "github.com/tangwy-t/webmanager-server/internal/pkg/ws"
	"github.com/tangwy-t/webmanager-server/internal/service"
	"go.uber.org/zap"
)

//go:embed scalar.html
var scalarHTML string

// HTTP 动词约定(新增路由请遵守):
//   GET    读取(列表/详情/导出)
//   POST   创建 + 一切"命令"——状态迁移(enable/disable/pause/resume/
//          publish/revoke/unlock)、触发(run)、校验型动作(改密/重置)。
//          命令端点通常无请求体,不承诺幂等,这正是它与 PUT 的分界。
//   PUT    整体更新资源内容(UpdateUserInfo/UpdateRole 等表单回写)
//   DELETE 删除
//
// 此前状态命令一半 PUT 一半 POST(users/enable 用 PUT、notices/publish
// 用 POST)。统一为 POST:PUT 的 HTTP 契约是"以请求体替换该 URI 的资源",
// 无请求体的命令不满足该契约;业界(GitHub API、Google AIP-136 自定义
// 方法)对状态迁移也统一用 POST。

// ── Sub-structs grouped by route module ─────────────────────────────

// InfraDeps holds infrastructure dependencies shared across routes.
type InfraDeps struct {
	AuthSvc       *service.AuthService
	SessionStore  *session.SessionStore
	ConfigProv    *service.ConfigService
	Cfg           *config.Config
	OpLogSvc      *service.OperationLogService
	Logger        *logger.Logger
	Hub           *wsPkg.Hub
	ScopeResolver *datascope.ScopeResolver
	RateLimiter   *limiter.RateLimiter
	PermGuard     *middleware.PermissionGuard
}

// AuthDeps holds authentication handler dependencies.
type AuthDeps struct {
	AuthHdl *handler.AuthHandler
}

// SystemDeps holds user/role/menu/dept handler dependencies.
type SystemDeps struct {
	UserHdl *handler.UserHandler
	RoleHdl *handler.RoleHandler
	MenuHdl *handler.MenuHandler
	DeptHdl *handler.DeptHandler
}

// DictDeps holds dictionary type and data handler dependencies.
type DictDeps struct {
	DictTypeHdl *handler.DictTypeHandler
	DictDataHdl *handler.DictDataHandler
	DictCodeHdl *handler.DictCodeHandler
}

// MonitorDeps holds operation log, login log, notice, server monitor, cache, SQL monitor, and pprof handler dependencies.
type MonitorDeps struct {
	OpLogHdl         *handler.OperationLogHandler
	LoginLogHdl      *handler.LoginLogHandler
	NoticeHdl        *handler.NoticeHandler
	ServerMonitorHdl *handler.ServerMonitorHandler
	CacheHdl         *handler.CacheHandler
	SqlMonitorHdl    *handler.SQLMonitorHandler
	PprofHdl         *handler.PprofHandler
	OnlineHdl        *handler.OnlineHandler
}

// JobDeps holds job handler dependencies.
type JobDeps struct {
	JobHdl *handler.JobHandler
}

// ConfigDeps holds config handler dependencies.
type ConfigDeps struct {
	ConfigHdl *handler.ConfigHandler
}

// FileDeps holds file management handler dependencies.
type FileDeps struct {
	FileHdl *handler.FileHandler
}

// Dependencies holds all injected dependencies for router setup, grouped by module.
type Dependencies struct {
	Infra   InfraDeps
	Auth    AuthDeps
	System  SystemDeps
	Dict    DictDeps
	Monitor MonitorDeps
	Job     JobDeps
	Config  ConfigDeps
	File    FileDeps
}

// Setup registers all middleware and routes on a new gin.Engine.
// All dependencies are injected via the Dependencies struct — no global state.
func Setup(deps Dependencies) *gin.Engine {
	r := gin.New()

	// Gin 默认信任所有代理：ClientIP 会被 X-Forwarded-For 伪造，进而绕过
	// 按 IP 的登录限流与验证码频控。仅信任配置中声明的代理；未配置时
	// 不信任任何代理。
	if len(deps.Infra.Cfg.Server.TrustedProxies) == 0 {
		_ = r.SetTrustedProxies(nil)
	} else if err := r.SetTrustedProxies(deps.Infra.Cfg.Server.TrustedProxies); err != nil {
		deps.Infra.Logger.Error("failed to set trusted proxies", zap.Error(err))
	}

	// 未分类错误（非 AppError）统一落日志，否则线上 500 无法归因。
	app.SetLogger(deps.Infra.Logger)

	// ── Global middleware ──────────────────────────────────────────
	r.Use(middleware.CORS(nil, deps.Infra.Logger))
	r.Use(deps.Infra.RateLimiter.GlobalRateLimit())
	r.Use(middleware.Recovery(deps.Infra.Logger))
	r.Use(middleware.TraceInit()) // 新增：traceId 注入
	r.Use(middleware.RequestLogger(deps.Infra.Logger))

	api := r.Group(deps.Infra.Cfg.Server.APIPrefix)

	// ── Public routes (no auth required) ───────────────────────────
	{
		// Health check
		// 以下端点有意不收录进 swagger(swagger 只描述业务 API):
		//   /health               探活
		//   /ws                   WebSocket 升级
		//   /monitor/debug/pprof/*  gin 原生 pprof 包装
		//   /swagger/*any、/scalar  API 文档自身(仅非 release 模式)
		// 均无业务语义。
		api.GET("/health", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"status": "ok"})
		})
		// API 文档仅开发/测试模式暴露;release 生产环境暴露完整接口
		// 面与参数结构是免费的侦察材料。
		if deps.Infra.Cfg.Server.Mode != "release" {
			// Swagger spec 元数据:docs.go 里的静态默认值由 swag init 生成,
			// 这里在运行时用真实配置/构建信息覆盖,无需人工保持注解与配置同步:
			//   Host     置空 → Swagger UI/Scalar 回退到当前访问域名(含端口),
			//            本地直连、反代、任意端口均自动适配;
			//   BasePath 跟随 server.apiPrefix 配置;
			//   Version  跟随 -ldflags 注入的构建版本(见 Makefile LDFLAGS)。
			basePath := deps.Infra.Cfg.Server.APIPrefix
			if basePath == "" {
				basePath = "/"
			}
			docs.SwaggerInfo.Host = ""
			docs.SwaggerInfo.BasePath = basePath
			docs.SwaggerInfo.Version = version.Version

			// Swagger JSON spec (generated by swag init)
			api.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
			// Scalar UI — modern API documentation viewer
			api.GET("/scalar", func(c *gin.Context) {
				c.Header("Content-Type", "text/html; charset=utf-8")
				c.String(http.StatusOK, scalarHTML)
			})
		}

		api.POST("/login", deps.Infra.RateLimiter.LoginRateLimit(), deps.Auth.AuthHdl.Login)
		api.POST("/refresh", deps.Infra.RateLimiter.RefreshRateLimit(), deps.Auth.AuthHdl.RefreshToken)
		api.POST("/captcha/generate", deps.Auth.AuthHdl.CaptchaGenerate)
		// 头像图片输出:<img> 无法携带 Authorization 头,故公开(仅头像类低敏文件)。
		api.GET("/user/avatar/:id", deps.Auth.AuthHdl.GetAvatar)
	}

	// ── Authenticated routes ───────────────────────────────────────
	auth := api.Group("")
	auth.Use(middleware.Auth(deps.Infra.ConfigProv, deps.Infra.SessionStore, deps.Infra.Logger))
	auth.Use(middleware.OperationLogMiddleware(deps.Infra.OpLogSvc, deps.Infra.Logger))
	auth.Use(middleware.ScopeResolverHandler(deps.Infra.ScopeResolver))
	{
		auth.POST("/logout", middleware.SetModuleName("认证"), deps.Auth.AuthHdl.Logout)

		// 个人中心:自助资料/头像/密码/总览(与管理端 /users「用户管理」区分)。
		// 操作日志模块名由此分组注入 —— 顶层 auth 直挂路由不经过任何
		// SetModuleName,曾导致这些操作的系统模块记录为空。
		userCenter := auth.Group("/user")
		userCenter.Use(middleware.SetModuleName("个人中心"))
		{
			userCenter.GET("/info", deps.Auth.AuthHdl.GetUserInfo)
			userCenter.GET("/overview", deps.Auth.AuthHdl.GetUserOverview)
			userCenter.PUT("/info", deps.Auth.AuthHdl.UpdateProfile)
			userCenter.POST("/info/avatar", deps.Auth.AuthHdl.UploadAvatar)
			userCenter.POST("/password", deps.Auth.AuthHdl.ChangePassword)
			userCenter.POST("/password/verify", deps.Infra.RateLimiter.LoginRateLimit(), deps.Auth.AuthHdl.VerifyPassword)
		}

		// ── Permission-protected routes ────────────────────────────
		// Each route carries its own required permission code; permission
		// middleware is applied per-route rather than at the group level
		// so that list/create/update/delete are independently gated.
		perm := deps.Infra.PermGuard.Permission

		// 用户管理
		users := auth.Group("/users")
		users.Use(middleware.SetModuleName("用户管理"))
		{
			users.GET("", perm(permission.PermUserList), deps.System.UserHdl.List)
			users.GET("/:id", perm(permission.PermUserQuery), deps.System.UserHdl.GetByID)
			users.POST("", perm(permission.PermUserAdd), deps.System.UserHdl.Create)
			users.PUT("/:id", perm(permission.PermUserEdit), deps.System.UserHdl.UpdateUserInfo)
			users.PUT("/:id/roles", perm(permission.PermUserAssign), deps.System.UserHdl.AssignRoles)
			users.DELETE("/:id", perm(permission.PermUserDelete), deps.System.UserHdl.Delete)
			users.POST("/:id/enable", perm(permission.PermUserEnable), deps.System.UserHdl.Enable)
			users.POST("/:id/disable", perm(permission.PermUserDisable), deps.System.UserHdl.Disable)
			users.POST("/:id/password/reset", perm(permission.PermUserReset), deps.System.UserHdl.ResetPassword)
			users.POST("/:id/unlock", perm(permission.PermUserUnlock), deps.System.UserHdl.Unlock)
		}

		// 角色管理
		roles := auth.Group("/roles")
		roles.Use(middleware.SetModuleName("角色管理"))
		{
			roles.GET("", perm(permission.PermRoleList), deps.System.RoleHdl.List)
			// 角色下拉·白名单(仅登录,响应已按 RoleOptionResp 瘦身不含授权明细):
			// 用户/公告表单均需引用角色列表做选择器。
			roles.GET("/all", deps.System.RoleHdl.GetAll)
			roles.GET("/:id", perm(permission.PermRoleQuery), deps.System.RoleHdl.GetByID)
			roles.POST("", perm(permission.PermRoleAdd), deps.System.RoleHdl.Create)
			roles.PUT("/:id", perm(permission.PermRoleEdit), deps.System.RoleHdl.Update)
			roles.PUT("/sort", perm(permission.PermRoleSort), deps.System.RoleHdl.UpdateSort)
			roles.PUT("/:id/status", perm(permission.PermRoleStatus), deps.System.RoleHdl.UpdateStatus)
			roles.DELETE("/:id", perm(permission.PermRoleDelete), deps.System.RoleHdl.Delete)
			// 分配用户(角色维度):复用 UserHdl,join 表 sys_user_role 归用户域维护
			roles.GET("/:id/users", perm(permission.PermRoleAssign), deps.System.UserHdl.ListByRole)
			roles.POST("/:id/users", perm(permission.PermRoleAssign), deps.System.UserHdl.AddRoleUsers)
			roles.DELETE("/:id/users", perm(permission.PermRoleAssign), deps.System.UserHdl.RemoveRoleUsers)
		}

		// 菜单管理
		menus := auth.Group("/menus")
		menus.Use(middleware.SetModuleName("菜单管理"))
		{
			// 全树只读·白名单(仅登录):前端路由守卫在 backend 模式下为每一位
			// 登录用户调用,不能挂 system:menu:list,否则普通用户路由构建 403→500。
			// 无需服务端二次裁剪:sys_menu 已声明 role 维度数据范围,datascope
			// 插件对查询自动注入过滤 —— admin 角色(ScopeAll)得全树,普通用户
			// 仅返回其角色已分配的菜单(等价若依 selectMenuTreeByUserId)。
			// 角色授权表单同样消费此树(对齐若依 treeselect,登录即可)。
			menus.GET("", deps.System.MenuHdl.FindTree)
			menus.GET("/:id", perm(permission.PermMenuQuery), deps.System.MenuHdl.GetByID)
			menus.POST("", perm(permission.PermMenuAdd), deps.System.MenuHdl.Create)
			menus.PUT("/sort", perm(permission.PermMenuSort), deps.System.MenuHdl.UpdateSort)
			menus.PUT("/:id", perm(permission.PermMenuEdit), deps.System.MenuHdl.Update)
			menus.DELETE("/:id", perm(permission.PermMenuDelete), deps.System.MenuHdl.Delete)
		}

		// 部门管理
		depts := auth.Group("/depts")
		depts.Use(middleware.SetModuleName("部门管理"))
		{
			// 部门树只读·白名单(仅登录):用户/角色/公告三个表单都要引用部门树。
			// 树内容无需手工裁剪:sys_dept 声明 dept/self 维度数据范围,datascope
			// 自动按当前用户部门范围过滤(只暴露其可见的组织子树)。写操作仍挂权限码。
			depts.GET("", deps.System.DeptHdl.FindTree)
			depts.GET("/:id", perm(permission.PermDeptQuery), deps.System.DeptHdl.GetByID)
			depts.POST("", perm(permission.PermDeptAdd), deps.System.DeptHdl.Create)
			depts.PUT("/sort", perm(permission.PermDeptSort), deps.System.DeptHdl.UpdateSort)
			depts.PUT("/:id", perm(permission.PermDeptEdit), deps.System.DeptHdl.Update)
			depts.DELETE("/:id", perm(permission.PermDeptDelete), deps.System.DeptHdl.Delete)
		}

		// 操作日志
		opLogs := auth.Group("/logs/operation")
		opLogs.Use(middleware.SetModuleName("操作日志"))
		{
			opLogs.GET("", perm(permission.PermLogOperationList), deps.Monitor.OpLogHdl.FindPage)
			opLogs.DELETE("", perm(permission.PermLogOperationDelete), deps.Monitor.OpLogHdl.DeleteBefore)
		}

		// 登录日志
		loginLogs := auth.Group("/logs/login")
		loginLogs.Use(middleware.SetModuleName("登录日志"))
		{
			loginLogs.GET("", perm(permission.PermLogLoginList), deps.Monitor.LoginLogHdl.FindPage)
		}

		// 字典管理
		dictTypes := auth.Group("/dict/types")
		dictTypes.Use(middleware.SetModuleName("字典管理"))
		{
			dictTypes.GET("", perm(permission.PermDictTypeList), deps.Dict.DictTypeHdl.List)
			dictTypes.GET("/:id", perm(permission.PermDictTypeQuery), deps.Dict.DictTypeHdl.GetByID)
			dictTypes.POST("", perm(permission.PermDictTypeAdd), deps.Dict.DictTypeHdl.Create)
			dictTypes.PUT("/:id", perm(permission.PermDictTypeEdit), deps.Dict.DictTypeHdl.Update)
			dictTypes.DELETE("/:id", perm(permission.PermDictTypeDelete), deps.Dict.DictTypeHdl.Delete)
			dictTypes.GET("/:id/data", perm(permission.PermDictDataList), deps.Dict.DictDataHdl.ListByType)
			dictTypes.GET("/:id/data/:dataId", perm(permission.PermDictDataQuery), deps.Dict.DictDataHdl.GetByID)
			dictTypes.POST("/:id/data", perm(permission.PermDictDataAdd), deps.Dict.DictDataHdl.Create)
			dictTypes.PUT("/:id/data/:dataId", perm(permission.PermDictDataEdit), deps.Dict.DictDataHdl.Update)
			dictTypes.DELETE("/:id/data/:dataId", perm(permission.PermDictDataDelete), deps.Dict.DictDataHdl.Delete)
		}

		// 字典消费查询（仅需登录，无需权限码）
		dictCodes := auth.Group("/dict/codes")
		dictCodes.Use(middleware.SetModuleName("字典查询"))
		{
			dictCodes.GET("/:code", deps.Dict.DictCodeHdl.GetByCode)
			dictCodes.GET("", deps.Dict.DictCodeHdl.GetByCodes)
		}

		// 通知公告
		notices := auth.Group("/notices")
		notices.Use(middleware.SetModuleName("通知公告"))
		{
			notices.GET("", perm(permission.PermNoticeList), deps.Monitor.NoticeHdl.List)
			notices.GET("/target-users", perm(permission.PermNoticeList), deps.Monitor.NoticeHdl.TargetUsers)
			// 用户公告收件箱·白名单(仅登录,服务端按接收范围过滤,对齐若依 listTop):
			// 与 /notices/:id/read 白名单配套,普通用户也能拉取可见公告与未读数。
			notices.GET("/my", deps.Monitor.NoticeHdl.MyNotices)
			// 批量已读·白名单(与 /my、/:id/read 配套):幂等可重复调用。
			notices.POST("/read-all", deps.Monitor.NoticeHdl.MarkAllRead)
			notices.GET("/:id", perm(permission.PermNoticeQuery), deps.Monitor.NoticeHdl.GetByID)
			notices.POST("", perm(permission.PermNoticeAdd), deps.Monitor.NoticeHdl.Create)
			notices.PUT("/:id", perm(permission.PermNoticeEdit), deps.Monitor.NoticeHdl.Update)
			notices.DELETE("/:id", perm(permission.PermNoticeDelete), deps.Monitor.NoticeHdl.Delete)
			notices.POST("/:id/publish", perm(permission.PermNoticePublish), deps.Monitor.NoticeHdl.Publish)
			notices.POST("/:id/revoke", perm(permission.PermNoticePublish), deps.Monitor.NoticeHdl.Revoke)
			notices.POST("/:id/read", deps.Monitor.NoticeHdl.MarkRead)
			notices.GET("/:id/read-users", perm(permission.PermNoticeList), deps.Monitor.NoticeHdl.ReadUsers)
		}

		// 服务监控
		monitor := auth.Group("/monitor")
		{
			// 服务器监控
			server := monitor.Group("/server")
			server.Use(middleware.SetModuleName("服务器监控"))
			{
				server.GET("/stats", perm(permission.PermServerList), deps.Monitor.ServerMonitorHdl.ServeHTTP)
				server.GET("/history", perm(permission.PermServerList), deps.Monitor.ServerMonitorHdl.GetHistory)
			}

			// 缓存管理
			cache := monitor.Group("/cache")
			cache.Use(middleware.SetModuleName("缓存管理"))
			{
				cache.GET("/keys", perm(permission.PermCacheList), deps.Monitor.CacheHdl.ListKeys)
				cache.GET("/keys/value", perm(permission.PermCacheQuery), deps.Monitor.CacheHdl.GetKeyValue)
				cache.DELETE("/keys", perm(permission.PermCacheDelete), deps.Monitor.CacheHdl.DeleteKeys)
				cache.GET("/stats", perm(permission.PermCacheList), deps.Monitor.CacheHdl.GetStats)
			}

			// SQL监控
			sql := monitor.Group("/sql")
			sql.Use(middleware.SetModuleName("SQL监控"))
			{
				sql.GET("/stats", perm(permission.PermSqlList), deps.Monitor.SqlMonitorHdl.GetStats)
				sql.GET("/history", perm(permission.PermSqlList), deps.Monitor.SqlMonitorHdl.GetHistory)
			}

			// pprof 动态启停 + UI 渲染数据接口
			pprofGroup := monitor.Group("/pprof")
			pprofGroup.Use(middleware.SetModuleName("pprof性能分析"))
			{
				// 状态概览始终可访问(仅报告开关/倒计时/profile 清单)
				pprofGroup.GET("/status", perm(permission.PermPprofList), deps.Monitor.PprofHdl.HandleStatus)
				pprofGroup.POST("/enable", perm(permission.PermPprofEnable), deps.Monitor.PprofHdl.HandleEnable)
				pprofGroup.POST("/disable", perm(permission.PermPprofDisable), deps.Monitor.PprofHdl.HandleDisable)
				// 火焰图数据与原始端点同样受 enabled 开关保护
				pprofGroup.GET("/profile/:name", middleware.PprofGuard(deps.Infra.ConfigProv), perm(permission.PermPprofList), deps.Monitor.PprofHdl.HandleProfileFlame)
			}
			pprofDebug := monitor.Group("/debug/pprof")
			pprofDebug.Use(middleware.PprofGuard(deps.Infra.ConfigProv))
			pprofDebug.GET("/*any", perm(permission.PermPprofList), handler.AdaptPprof())

			// 在线用户
			online := monitor.Group("/online")
			online.Use(middleware.SetModuleName("在线用户"))
			{
				online.GET("", perm(permission.PermOnlineList), deps.Monitor.OnlineHdl.List)
				online.POST("/kick", perm(permission.PermOnlineKick), deps.Monitor.OnlineHdl.Kick)
			}
		}

		// 参数配置
		configs := auth.Group("/configs")
		configs.Use(middleware.SetModuleName("参数配置"))
		{
			configs.GET("", perm(permission.PermConfigList), deps.Config.ConfigHdl.List)
			configs.GET("/:id", perm(permission.PermConfigQuery), deps.Config.ConfigHdl.GetByID)
			configs.POST("", perm(permission.PermConfigAdd), deps.Config.ConfigHdl.Create)
			configs.PUT("/:id", perm(permission.PermConfigEdit), deps.Config.ConfigHdl.Update)
			configs.DELETE("/:id", perm(permission.PermConfigDelete), deps.Config.ConfigHdl.Delete)
			configs.GET("/key/:key", perm(permission.PermConfigQuery), deps.Config.ConfigHdl.GetByKey)
		}

		// 定时任务管理
		jobs := auth.Group("/jobs")
		jobs.Use(middleware.SetModuleName("定时任务管理"))
		{
			// 固定路径必须在 :id 之前注册！
			jobs.GET("/targets", perm(permission.PermJobList), deps.Job.JobHdl.GetTargets)
			jobs.GET("/health", perm(permission.PermJobList), deps.Job.JobHdl.GetHealth)
			// 日志
			jobs.GET("/logs", perm(permission.PermJobLogList), deps.Job.JobHdl.FindLogs)
			jobs.DELETE("/logs", perm(permission.PermJobLogDelete), deps.Job.JobHdl.DeleteLogs)
			// CRUD
			jobs.GET("", perm(permission.PermJobList), deps.Job.JobHdl.List)
			jobs.GET("/:id", perm(permission.PermJobQuery), deps.Job.JobHdl.GetByID)
			jobs.POST("", perm(permission.PermJobAdd), deps.Job.JobHdl.Create)
			jobs.PUT("/:id", perm(permission.PermJobEdit), deps.Job.JobHdl.Update)
			jobs.DELETE("/:id", perm(permission.PermJobDelete), deps.Job.JobHdl.Delete)
			// Status
			jobs.POST("/:id/pause", perm(permission.PermJobPause), deps.Job.JobHdl.Pause)
			jobs.POST("/:id/resume", perm(permission.PermJobExecute), deps.Job.JobHdl.Resume)
			// RunOnce
			jobs.POST("/:id/run", perm(permission.PermJobOnce), deps.Job.JobHdl.RunOnce)
		}

		// 文件管理
		files := auth.Group("/files")
		files.Use(middleware.SetModuleName("文件管理"))
		{
			files.GET("", perm(permission.PermFileList), deps.File.FileHdl.List)
			files.GET("/stats", perm(permission.PermFileList), deps.File.FileHdl.Stats)
			files.POST("", perm(permission.PermFileUpload), deps.File.FileHdl.Upload)
			files.PUT("/:id", perm(permission.PermFileEdit), deps.File.FileHdl.Rename)
			files.DELETE("", perm(permission.PermFileDelete), deps.File.FileHdl.DeleteMany)
			files.GET("/:id/thumbnail", perm(permission.PermFileList), deps.File.FileHdl.Thumbnail)
			files.GET("/:id/download", perm(permission.PermFileDownload), deps.File.FileHdl.Download)
			files.GET("/:id/preview", perm(permission.PermFileDownload), deps.File.FileHdl.Preview)
		}
	}

	// WebSocket
	api.GET("/ws", wsPkg.HandleUpgrade(deps.Infra.Hub, deps.Infra.Logger))

	return r
}
