// Package redis provides Redis client initialization supporting single, cluster, and sentinel modes.
package redis

import (
	"context"
	"fmt"
	"github.com/tangwy-t/webmanager-server/internal/pkg/config"
	"github.com/tangwy-t/webmanager-server/internal/pkg/lifecycle"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"time"

	"go.uber.org/zap"

	goredis "github.com/redis/go-redis/v9"
)

// New creates a Redis UniversalClient based on the configured mode (single,
// cluster, or sentinel). Connection timeouts are taken from cfg; if zero,
// sensible defaults are applied. The client's Close is registered with the
// provided lifecycle manager as "redis".
func New(cfg *config.RedisConfig, logger logger.LoggerInterface, lc lifecycle.ManagerInterface) (goredis.UniversalClient, error) {
	var client goredis.UniversalClient
	logger.Debug("initializing redis", zap.String("mode", cfg.Mode))

	dialTimeout := time.Duration(cfg.DialTimeout) * time.Millisecond
	if cfg.DialTimeout <= 0 {
		dialTimeout = 5 * time.Second
	}
	readTimeout := time.Duration(cfg.ReadTimeout) * time.Millisecond
	if cfg.ReadTimeout <= 0 {
		readTimeout = 3 * time.Second
	}
	writeTimeout := time.Duration(cfg.WriteTimeout) * time.Millisecond
	if cfg.WriteTimeout <= 0 {
		writeTimeout = 3 * time.Second
	}

	switch cfg.Mode {
	case "cluster":
		client = goredis.NewClusterClient(&goredis.ClusterOptions{
			Addrs:        cfg.Addrs,
			Password:     cfg.Password,
			DialTimeout:  dialTimeout,
			ReadTimeout:  readTimeout,
			WriteTimeout: writeTimeout,
		})
	case "sentinel":
		// Sentinel mode: DB selection is subject to sentinel configuration.
		client = goredis.NewFailoverClient(&goredis.FailoverOptions{
			MasterName:      cfg.MasterName,
			SentinelAddrs:   cfg.Addrs,
			Password:        cfg.Password,
			DB:              cfg.DB,
			DialTimeout:     dialTimeout,
			ReadTimeout:     readTimeout,
			WriteTimeout:    writeTimeout,
			MinRetryBackoff: 100 * time.Millisecond,
			MaxRetryBackoff: 500 * time.Millisecond,
			MaxRetries:      3,
		})
	default: // single
		client = goredis.NewClient(&goredis.Options{
			Addr:         cfg.Addr,
			Password:     cfg.Password,
			DB:           cfg.DB,
			DialTimeout:  dialTimeout,
			ReadTimeout:  readTimeout,
			WriteTimeout: writeTimeout,
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		logger.Error("failed to connect redis", zap.Error(err), zap.String("mode", cfg.Mode))
		client.Close()
		return nil, fmt.Errorf("failed to connect redis: %w", err)
	}
	logger.Info("Redis connected", zap.String("mode", cfg.Mode))
	// 注册 OpenTelemetry TracingHook
	client.AddHook(NewTracingHook())
	lc.RegisterTo("cleanup", "redis", func(context.Context) error { return client.Close() })
	return client, nil
}
