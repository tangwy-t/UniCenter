// Package redis provides Redis client initialization supporting single, cluster, and sentinel modes.
package redis

import (
	"context"
	"net"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// TracingHook 实现 redis.Hook 接口，为每个 Redis 命令创建 OpenTelemetry Span。
type TracingHook struct{}

// NewTracingHook 创建 TracingHook 实例。
func NewTracingHook() *TracingHook {
	return &TracingHook{}
}

// DialHook 在建立连接时调用，直接透传，不创建 Span。
func (h *TracingHook) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return next(ctx, network, addr)
	}
}

// ProcessHook 在 Redis 命令执行前后创建 Span。
func (h *TracingHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		cmdName := cmd.FullName()
		if cmdName == "" {
			return next(ctx, cmd)
		}

		ctx, span := otel.Tracer("github.com/tangwy-t/webmanager-server/internal/pkg/redis").Start(ctx, "REDIS "+cmdName,
			trace.WithSpanKind(trace.SpanKindClient),
		)
		defer span.End()

		span.SetAttributes(
			attribute.String("db.system", "redis"),
			attribute.String("db.operation", cmdName),
		)

		err := next(ctx, cmd)
		if err != nil {
			span.SetStatus(codes.Error, err.Error())
			span.RecordError(err)
		}

		return err
	}
}

// ProcessPipelineHook 在 Pipeline 执行前后创建 Span。
func (h *TracingHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		ctx, span := otel.Tracer("github.com/tangwy-t/webmanager-server/internal/pkg/redis").Start(ctx, "REDIS PIPELINE",
			trace.WithSpanKind(trace.SpanKindClient),
		)
		defer span.End()

		span.SetAttributes(
			attribute.String("db.system", "redis"),
			attribute.Int("db.redis.pipeline_cmds", len(cmds)),
		)

		err := next(ctx, cmds)
		if err != nil {
			span.SetStatus(codes.Error, err.Error())
			span.RecordError(err)
		}

		return err
	}
}
