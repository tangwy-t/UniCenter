// Package wireup 集中管理依赖注入（DI）组装，将 main() 中的 DI 逻辑外移。
// 不引入外部 DI 框架，纯手写方式保持零新增依赖。
package wireup

import (
	"context"
	"fmt"
	"github.com/tangwy-t/webmanager-server/internal/handler"
	"github.com/tangwy-t/webmanager-server/internal/middleware"
	"github.com/tangwy-t/webmanager-server/internal/pkg/captcha"
	"github.com/tangwy-t/webmanager-server/internal/pkg/config"
	"github.com/tangwy-t/webmanager-server/internal/pkg/database"
	"github.com/tangwy-t/webmanager-server/internal/pkg/datascope"
	"github.com/tangwy-t/webmanager-server/internal/pkg/lifecycle"
	"github.com/tangwy-t/webmanager-server/internal/pkg/limiter"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"github.com/tangwy-t/webmanager-server/internal/pkg/redis/cache"
	"github.com/tangwy-t/webmanager-server/internal/pkg/redis/lock"
	"github.com/tangwy-t/webmanager-server/internal/pkg/redis/pubsub"
	"github.com/tangwy-t/webmanager-server/internal/pkg/serverstats"
	"github.com/tangwy-t/webmanager-server/internal/pkg/session"
	"github.com/tangwy-t/webmanager-server/internal/pkg/sqlhistory"
	"github.com/tangwy-t/webmanager-server/internal/pkg/storage"
	"github.com/tangwy-t/webmanager-server/internal/pkg/tracing"
	wsPkg "github.com/tangwy-t/webmanager-server/internal/pkg/ws"
	"github.com/tangwy-t/webmanager-server/internal/repository"
	"github.com/tangwy-t/webmanager-server/internal/router"
	"github.com/tangwy-t/webmanager-server/internal/scheduler"
	"github.com/tangwy-t/webmanager-server/internal/service"
	"github.com/tangwy-t/webmanager-server/internal/task"
	"github.com/tangwy-t/webmanager-server/internal/task/tasks"

	goredis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// Init 完成所有 repo/service/handler/scheduler 的构造和组装。
// 接受基础设施依赖，返回装配完成的 router.Dependencies。
// 直接返回 router 的结构体:此前 wireup.Deps 与 router.Dependencies
// 逐字段镜像,main 里还要手工抄送 30 个字段 —— 新增 handler 需要改三处,
// 漏一处即静默空指针。初始化失败(如 tracer/scheduler 启动失败)返回
// error 由 main 统一退出,避免带病启动后静默失效。
func Init(db *gorm.DB, sqlStats *database.SQLStats, redis goredis.UniversalClient, log *logger.Logger, lc *lifecycle.Manager, cfg *config.Config) (*router.Dependencies, error) {
	// ── Focused Stores & Broker ────────────────────────────────────────
	cacheStore := cache.NewStore(redis)
	sessionStore := session.NewSession(cacheStore)
	locker := lock.NewLocker(redis)
	broker := pubsub.NewBroker(redis, log, lc)

	// ── OpenTelemetry TracerProvider ─────────────────────────────────
	tp, err := tracing.NewTracer(&cfg.Observability.Tracing, lc, log)
	if err != nil {
		return nil, fmt.Errorf("wireup: init tracer: %w", err)
	}
	_ = tp // TracerProvider 已全局注册到 otel，此处仅持有引用便于未来扩展

	// ── Repositories ───────────────────────────────────────────────────
	userRepo := repository.NewUserRepository(db)
	roleRepo := repository.NewRoleRepository(db)
	menuRepo := repository.NewMenuRepository(db)
	deptRepo := repository.NewDeptRepository(db)
	authRepo := repository.NewAuthRepository(db)
	opLogRepo := repository.NewOperationLogRepository(db)
	loginLogRepo := repository.NewLoginLogRepository(db)
	dictTypeRepo := repository.NewDictTypeRepository(db)
	dictDataRepo := repository.NewDictDataRepository(db)
	noticeRepo := repository.NewNoticeRepository(db)
	configRepo := repository.NewConfigRepository(db)
	jobRepo := repository.NewJobRepository(db)
	jobLogRepo := repository.NewJobLogRepository(db)
	fileRepo := repository.NewFileRepository(db)

	// ── DictService ────────────────────────────────────────────────────
	dictSvc := service.NewDictService(dictTypeRepo, dictDataRepo, cacheStore, log)

	// ── ConfigService (early: all downstream services inject ConfigGetterInterface) ──
	configSvc := service.NewConfigService(configRepo, cacheStore, broker, log)

	// Security: 检出已知的默认 JWT 密钥（v006 种子值）时立即轮换为随机值。
	// 必须先于任何读取/快照该密钥的组件（hub、登录）执行。
	configSvc.RotateDefaultJWTSecret(context.Background())

	captchaPkg := captcha.NewCaptcha(cacheStore, configSvc, log)

	// ── Rate Limiter ──────────────────────────────────────────────────
	rateLimiter := limiter.NewRateLimiter(cacheStore, configSvc, log)

	// ── WebSocket Hub ──────────────────────────────────────────────────
	// sessionStore 用于 WS 认证时校验 access token 是否仍在会话白名单，
	// 否则登出/吊销后的 token 仍可维持已建立的 WebSocket 连接。
	// configSvc 作为 SecretGetter 注入:WS 认证实时读取密钥,运行期
	// 轮换 sys.jwt.secret 后无需重启即可生效(与 HTTP 认证同源)。
	hub := wsPkg.NewHub(broker, configSvc, sessionStore, log, nil)

	// Register hub for graceful shutdown in drain phase.
	lc.RegisterTo("drain", "ws-hub", func(context.Context) error {
		hub.Stop()
		return nil
	})

	// ── Scope Resolver ──(前移到 AuthService 之前:登录/刷新时的访问解析
	// resolveUserAccess 依赖它构造 ScopeContext;依赖只需 authRepo/deptRepo/log)
	deptResolver := datascope.NewDeptDimensionResolver(authRepo, deptRepo)
	roleResolver := datascope.NewRoleDimensionResolver(authRepo)
	selfResolver := datascope.NewSelfDimensionResolver()
	scopeResolver := datascope.NewScopeResolver([]datascope.DimensionResolver{deptResolver, selfResolver, roleResolver}, log)

	// ── Services ───────────────────────────────────────────────────────
	userSvc := service.NewUserService(userRepo, log, sessionStore, configSvc)
	roleSvc := service.NewRoleService(roleRepo, log, sessionStore)
	menuSvc := service.NewMenuService(menuRepo, sessionStore, log)
	deptSvc := service.NewDeptService(deptRepo, log)
	loginLogSvc := service.NewLoginLogService(loginLogRepo, log)
	// ── Password Service ───────────────────────────────────────────────
	// 密码域(改密/锁屏校验/强度策略)独立于认证登录流程;AuthService 仅委托。
	passwordSvc := service.NewPasswordService(configSvc, authRepo, sessionStore, log)

	authSvc := service.NewAuthService(configSvc, authRepo, log, sessionStore, loginLogSvc, captchaPkg, cfg.Server.APIPrefix, scopeResolver, passwordSvc)
	noticeSvc := service.NewNoticeService(noticeRepo, hub, log)
	opLogSvc := service.NewOperationLogService(opLogRepo, userRepo, log)

	// ── File Service ────────────────────────────────────────────────────
	// sys.file.upload.* 配置经 ConfigService 读取,支持运行期热更。
	// 存储后端默认本地盘;配置 storage.backend=s3 时构造 S3 兼容后端(新上传走
	// 对象存储,历史 local 文件仍按 StorageType 路由到本地盘读取)。
	fileSvc := service.NewFileService(fileRepo, configSvc, log)
	if cfg.Storage.Backend == "s3" {
		s3Backend, err := storage.NewS3(storage.S3Options{
			Endpoint:  cfg.Storage.S3.Endpoint,
			AccessKey: cfg.Storage.S3.AccessKey,
			SecretKey: cfg.Storage.S3.SecretKey,
			Bucket:    cfg.Storage.S3.Bucket,
			Region:    cfg.Storage.S3.Region,
			UseSSL:    cfg.Storage.S3.UseSSL,
			PathStyle: cfg.Storage.S3.PathStyle,
		})
		if err != nil {
			return nil, fmt.Errorf("wireup: init storage backend: %w", err)
		}
		fileSvc = service.NewFileServiceWithRemoteS3(fileRepo, configSvc, log, s3Backend)
	}

	// ── Task Registry ──────────────────────────────────────────────────
	// 任务清单由 tasks.All 维护(与任务实现同包),此处只提供依赖。
	taskRegistry := task.NewRegistry(tasks.All(tasks.Deps{
		OpLogRepo:    opLogRepo,
		LoginLogRepo: loginLogRepo,
		JobLogRepo:   jobLogRepo,
		ConfigRepo:   configRepo,
		DictTypeRepo: dictTypeRepo,
		DictDataRepo: dictDataRepo,
		ConfigSvc:    configSvc,
		CacheStore:   cacheStore,
	})...)

	// ── Scheduler ──────────────────────────────────────────────────────
	jobScheduler, err := scheduler.NewScheduler(taskRegistry, jobRepo, jobLogRepo, locker, broker, log, configSvc, lc)
	if err != nil {
		return nil, fmt.Errorf("wireup: init scheduler: %w", err)
	}

	// ── Job Service ────────────────────────────────────────────────────
	jobSvc := service.NewJobService(jobRepo, jobLogRepo, jobScheduler, taskRegistry, log)

	// ── Handlers ───────────────────────────────────────────────────────
	userHdl := handler.NewUserHandler(userSvc, captchaPkg)
	roleHdl := handler.NewRoleHandler(roleSvc)
	menuHdl := handler.NewMenuHandler(menuSvc)
	deptHdl := handler.NewDeptHandler(deptSvc)
	authHdl := handler.NewAuthHandler(authSvc, captchaPkg, log)
	opLogHdl := handler.NewOperationLogHandler(opLogSvc)
	loginLogHdl := handler.NewLoginLogHandler(loginLogSvc)
	dictTypeHdl := handler.NewDictTypeHandler(dictSvc)
	dictDataHdl := handler.NewDictDataHandler(dictSvc)
	dictCodeHdl := handler.NewDictCodeHandler(dictSvc)
	noticeHdl := handler.NewNoticeHandler(noticeSvc)
	configHdl := handler.NewConfigHandler(configSvc)
	jobHdl := handler.NewJobHandler(jobSvc)
	fileHdl := handler.NewFileHandler(fileSvc)
	serverMonitorHdl := handler.NewServerMonitorHandler(log, serverstats.NewHistoryStore(redis))

	// 服务器监控的采样协程随进程退出:drain 阶段优雅停止(幂等 Close)。
	lc.RegisterTo("drain", "server-monitor", func(context.Context) error {
		serverMonitorHdl.Close()
		return nil
	})

	// ── SQL Monitor Service & Handler ──────────────────────────────────
	// 历史走 Redis 滚动窗口(3s 采样、24h 保留);采样协程随进程退出:
	// drain 阶段优雅停止(幂等 Close)。
	sqlMonitorSvc := service.NewSQLMonitorService(sqlStats, sqlhistory.NewHistoryStore(redis), log)
	sqlMonitorSvc.Start()
	lc.RegisterTo("drain", "sql-monitor", func(context.Context) error {
		sqlMonitorSvc.Close()
		return nil
	})
	sqlMonitorHdl := handler.NewSQLMonitorHandler(sqlMonitorSvc)

	// ── Cache Admin Store & Handler ──────────────────────────────────
	cacheSvc := service.NewCacheService(cacheStore, log)
	cacheHdl := handler.NewCacheHandler(cacheSvc)

	// ── Pprof Handler ──────────────────────────────────────────────
	pprofHdl := handler.NewPprofHandler(configSvc, log)

	// ── Online User Handler ─────────────────────────────────────────
	onlineSvc := service.NewOnlineUserService(sessionStore, userRepo, configSvc, hub, log)
	onlineHdl := handler.NewOnlineHandler(onlineSvc)

	hub.SetOnUserOnline(noticeSvc.GetUnreadNotices)

	// ScopeResolver 上移到 AuthService 构造之前(登录/刷新访问解析依赖注入)。

	// ── Permission Guard ─────────────────────────────────────────────────
	permGuard := middleware.NewPermissionGuard(authSvc, sessionStore, configSvc, log)

	return &router.Dependencies{
		Infra: router.InfraDeps{
			Hub:           hub,
			SessionStore:  sessionStore,
			ConfigProv:    configSvc,
			AuthSvc:       authSvc,
			OpLogSvc:      opLogSvc,
			ScopeResolver: scopeResolver,
			RateLimiter:   rateLimiter,
			PermGuard:     permGuard,
			Cfg:           cfg,
			Logger:        log,
		},
		Auth: router.AuthDeps{
			AuthHdl: authHdl,
		},
		System: router.SystemDeps{
			UserHdl: userHdl,
			RoleHdl: roleHdl,
			MenuHdl: menuHdl,
			DeptHdl: deptHdl,
		},
		Dict: router.DictDeps{
			DictTypeHdl: dictTypeHdl,
			DictDataHdl: dictDataHdl,
			DictCodeHdl: dictCodeHdl,
		},
		Monitor: router.MonitorDeps{
			OpLogHdl:         opLogHdl,
			LoginLogHdl:      loginLogHdl,
			NoticeHdl:        noticeHdl,
			ServerMonitorHdl: serverMonitorHdl,
			CacheHdl:         cacheHdl,
			SqlMonitorHdl:    sqlMonitorHdl,
			PprofHdl:         pprofHdl,
			OnlineHdl:        onlineHdl,
		},
		Job: router.JobDeps{
			JobHdl: jobHdl,
		},
		Config: router.ConfigDeps{
			ConfigHdl: configHdl,
		},
		File: router.FileDeps{
			FileHdl: fileHdl,
		},
	}, nil
}
