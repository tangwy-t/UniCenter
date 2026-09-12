// @title           RBAC Admin API
// @version         1.0.0
// @description     企业级 RBAC 权限管理系统后端 API
// @termsOfService  http://swagger.io/terms/

// @contact.name   API Support
// @contact.email  support@example.com

// @license.name  MIT
// @license.url   https://opensource.org/licenses/MIT

// @BasePath  /api/v1
// 注:@host/@BasePath/@version 注解仅是 swag init 生成的静态默认值;
// 运行时会在 router.Setup 中用实际配置与构建信息覆盖(见 router.go),
// 修改监听端口/前缀不需要同步这里的注解。

// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
// @description Type "Bearer" followed by a space and JWT token.

package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	_ "github.com/tangwy-t/webmanager-server/docs" // 注册 swagger spec，供 gin-swagger 读取
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/config"
	"github.com/tangwy-t/webmanager-server/internal/pkg/database"
	"github.com/tangwy-t/webmanager-server/internal/pkg/datascope"
	"github.com/tangwy-t/webmanager-server/internal/pkg/lifecycle"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"github.com/tangwy-t/webmanager-server/internal/pkg/migration"
	_ "github.com/tangwy-t/webmanager-server/internal/pkg/migration/migrations" // 触发所有迁移 init() 注册
	redisPkg "github.com/tangwy-t/webmanager-server/internal/pkg/redis"
	"github.com/tangwy-t/webmanager-server/internal/pkg/snowflake"
	"github.com/tangwy-t/webmanager-server/internal/router"
	"github.com/tangwy-t/webmanager-server/internal/wireup"
)

func main() {
	// ── Config ─────────────────────────────────────────────────────
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	log, err := logger.New(&cfg.Log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to init logger: %v\n", err)
		os.Exit(1)
	}
	defer log.Sync()

	log.Info("server starting",
		zap.Int("port", cfg.Server.Port),
		zap.String("mode", cfg.Server.Mode),
		zap.String("apiPrefix", cfg.Server.APIPrefix))

	gin.SetMode(cfg.Server.Mode)

	sfNode, err := snowflake.New(cfg.Snowflake.WorkerID, log)
	if err != nil {
		log.Error("failed to init snowflake", zap.Error(err))
		os.Exit(1)
	}

	// ── Infrastructure ─────────────────────────────────────────────
	lc := lifecycle.New(log)

	lc.DefinePhases(
		lifecycle.Phase{Name: "drain", Timeout: time.Duration(cfg.Server.DrainTimeout) * time.Second},
		lifecycle.Phase{Name: "cleanup", Timeout: time.Duration(cfg.Server.CleanupTimeout) * time.Second},
	)

	db, sqlStats, err := database.New(&cfg.Database, sfNode, log, lc)
	if err != nil {
		log.Error("failed to init database", zap.Error(err))
		os.Exit(1)
	}

	redisClient, err := redisPkg.New(&cfg.Redis, log, lc)
	if err != nil {
		log.Error("failed to init redis", zap.Error(err))
		os.Exit(1)
	}

	// ── Migration ───────────────────────────────────────────────────
	if err := migration.Run(db, log); err != nil {
		log.Error("migration failed", zap.Error(err))
		os.Exit(1)
	}

	// ── Data Scope Auto-Injection ──────────────────────────────────
	// 受控实体清单见 entity.ScopeEntities:注册随实体定义走,
	// 新增实体不再需要改 main。
	scopePlugin := datascope.NewScopePlugin()
	scopePlugin.RegisterPlugin(db)
	scopePlugin.RegisterEntities(entity.ScopeEntities...)

	// ── Dependency Injection ───────────────────────────────────────
	deps, err := wireup.Init(db, sqlStats, redisClient, log, lc, cfg)
	if err != nil {
		log.Error("failed to init dependencies", zap.Error(err))
		os.Exit(1)
	}
	// ── Router ─────────────────────────────────────────────────────
	// wireup.Init 已返回装配完成的 router.Dependencies,直接使用。
	engine := router.Setup(*deps)

	// ── HTTP Server ────────────────────────────────────────────────
	httpServer := &http.Server{
		Addr:           fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:        engine,
		ReadTimeout:    time.Duration(cfg.Server.ReadTimeout) * time.Second,
		WriteTimeout:   time.Duration(cfg.Server.WriteTimeout) * time.Second,
		IdleTimeout:    time.Duration(cfg.Server.IdleTimeout) * time.Second,
		MaxHeaderBytes: cfg.Server.MaxHeaderBytes,
	}
	// stdlib *http.Server is constructed here, so it is registered in main.
	// Registered last => LIFO runs it first, draining in-flight requests while
	// db/redis/scheduler/hub are still alive.
	lc.RegisterTo("drain", "http-server", httpServer.Shutdown)

	go func() {
		log.Info("HTTP server listening", zap.String("addr", httpServer.Addr))
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", zap.Error(err))
		}
	}()

	// ── Graceful Shutdown ──────────────────────────────────────────
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info("shutting down server...",
		zap.Int("drainTimeout", cfg.Server.DrainTimeout),
		zap.Int("cleanupTimeout", cfg.Server.CleanupTimeout))

	if err := lc.ShutdownStaged(); err != nil {
		log.Error("shutdown completed with errors", zap.Error(err))
	}

	log.Info("server exited gracefully")
}
