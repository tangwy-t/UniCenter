// Package database provides database connection initialization (MySQL / PostgreSQL)
// and context propagation helpers.
package database

import (
	"context"
	"fmt"
	sf "github.com/bwmarrin/snowflake"
	"github.com/tangwy-t/webmanager-server/internal/pkg/config"
	"github.com/tangwy-t/webmanager-server/internal/pkg/lifecycle"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"time"

	"go.uber.org/zap"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// openDialector 依据配置的驱动返回对应的 GORM Dialector。
// 未配置驱动（空串）时 DriverName() 归一为 mysql，兼容既有部署。
func openDialector(cfg *config.DatabaseConfig) (gorm.Dialector, error) {
	switch cfg.DriverName() {
	case "mysql":
		return mysql.Open(cfg.DSN()), nil
	case "postgres":
		return postgres.Open(cfg.DSN()), nil
	default:
		return nil, fmt.Errorf("unsupported database driver %q (supported: mysql, postgres)", cfg.Driver)
	}
}

// New opens a database connection via GORM (driver selected by cfg.Driver), configures the connection pool, and
// starts a background goroutine that logs pool stats every 30 seconds. The
// pool monitor and the database close are registered with the provided
// lifecycle manager as "db-pool-monitor" and "database" respectively, so LIFO
// shutdown stops the monitor before closing the pool.
//
// Returns the GORM DB instance, the SQLStats collector, and any error.
func New(cfg *config.DatabaseConfig, sfNode *sf.Node, log logger.LoggerInterface, lc lifecycle.ManagerInterface) (*gorm.DB, *SQLStats, error) {
	log.Debug("connecting to database",
		zap.String("host", cfg.Host),
		zap.Int("port", cfg.Port),
		zap.String("dbname", cfg.DBName))

	// Determine GORM log level from the application logger.
	logLevel := gormlogger.Silent
	if log.IsDebug() {
		logLevel = gormlogger.Info
	}

	// Slow query threshold default
	threshold := cfg.SlowQueryThreshold
	if threshold <= 0 {
		threshold = 200
	}
	slowThreshold := time.Duration(threshold) * time.Millisecond

	// Create SQLStats
	stats := NewSQLStats(10000, slowThreshold)

	// Stats snapshot interval default
	statsInterval := cfg.StatsInterval
	if statsInterval <= 0 {
		statsInterval = 60
	}

	// Create custom GORM Logger
	zapLogger, ok := log.(*logger.Logger)
	if !ok {
		log.Error("logger is not *logger.Logger, cannot create GORM logger")
		return nil, nil, fmt.Errorf("logger type assertion failed: expected *logger.Logger")
	}
	zapGormLogger := NewZapGormLogger(zapLogger.GetZap(), slowThreshold, cfg.SlowQueryLogEnabled, stats).
		LogMode(logLevel).(*ZapGormLogger)

	dialector, err := openDialector(cfg)
	if err != nil {
		log.Error("unsupported database driver", zap.String("driver", cfg.Driver))
		return nil, nil, err
	}

	db, err := gorm.Open(dialector, &gorm.Config{
		Logger: zapGormLogger,
	})
	if err != nil {
		log.Error("failed to connect database", zap.Error(err))
		return nil, nil, fmt.Errorf("failed to connect database: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		log.Error("failed to get sql.DB", zap.Error(err))
		return nil, nil, fmt.Errorf("failed to get sql.DB: %w", err)
	}
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetConnMaxLifetime(time.Duration(cfg.MaxLifetime) * time.Second)

	log.Info("database connected",
		zap.String("driver", cfg.DriverName()),
		zap.Int("maxIdleConns", cfg.MaxIdleConns),
		zap.Int("maxOpenConns", cfg.MaxOpenConns))

	// Register GORM callbacks: snowflake ID generation + audit fields.
	NewCallbacks(sfNode).Register(db)

	// Create stop channels BEFORE registering hooks, so closures
	// always capture initialized channels.
	stopCh := make(chan struct{})
	stopStatsCh := make(chan struct{})

	// Register shutdown hooks BEFORE starting goroutines, so lifecycle
	// cleanup can always stop them even if a panic occurs later.
	// LIFO order: db-pool-monitor stops before database closes the pool.
	lc.RegisterTo("cleanup", "database", func(context.Context) error { return sqlDB.Close() })
	lc.RegisterTo("cleanup", "db-pool-monitor", func(context.Context) error { close(stopCh); return nil })
	lc.RegisterTo("cleanup", "db-stats-monitor", func(context.Context) error { close(stopStatsCh); return nil })

	// Start connection pool health monitoring goroutine.
	go func() {
		// 监控 goroutine panic 不应拖垮进程:吞掉并记录。
		defer func() {
			if r := recover(); r != nil {
				log.Error("db pool monitor panic", zap.Any("panic", r))
			}
		}()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				poolStats := sqlDB.Stats()
				log.Info("DB connection pool stats",
					zap.Int("maxOpenConns", poolStats.MaxOpenConnections),
					zap.Int("openConns", poolStats.OpenConnections),
					zap.Int("inUse", poolStats.InUse),
					zap.Int("idle", poolStats.Idle),
					zap.Int64("waitCount", poolStats.WaitCount),
					zap.Duration("waitDuration", poolStats.WaitDuration),
				)
			case <-stopCh:
				return
			}
		}
	}()

	// Start periodic SQL stats snapshot goroutine.
	go func() {
		// 监控 goroutine panic 不应拖垮进程:吞掉并记录。
		defer func() {
			if r := recover(); r != nil {
				log.Error("sql stats monitor panic", zap.Any("panic", r))
			}
		}()
		ticker := time.NewTicker(time.Duration(statsInterval) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				snap := stats.Snapshot()
				log.Info("SQL stats snapshot",
					zap.Int64("totalCount", snap.Global.Count),
					zap.Float64("avgMs", snap.Global.AvgMs),
					zap.Float64("maxMs", snap.Global.MaxMs),
					zap.Int64("errorCount", snap.Global.ErrorCount),
					zap.Int64("slowCount", snap.Global.SlowCount),
				)
			case <-stopStatsCh:
				return
			}
		}
	}()

	return db, stats, nil
}
