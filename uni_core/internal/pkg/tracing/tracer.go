package tracing

import (
	"context"
	"fmt"
	"time"

	"github.com/tangwy-t/webmanager-server/internal/pkg/config"
	"github.com/tangwy-t/webmanager-server/internal/pkg/lifecycle"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
	"go.uber.org/zap"
)

// NewTracer 初始化 OpenTelemetry TracerProvider。
// 配置 OTLP gRPC exporter 和 TraceIDRatioBased 采样器。
// TracerProvider 的 Shutdown 注册到 lifecycle manager 的 cleanup 阶段。
// 当 cfg.Enabled 为 false 时返回 nil 且不注册任何 exporter。
func NewTracer(cfg *config.TracingConfig, lc lifecycle.ManagerInterface, log logger.LoggerInterface) (*sdktrace.TracerProvider, error) {
	if cfg == nil || !cfg.Enabled {
		log.Info("tracing disabled, skipping initialization")
		return nil, nil
	}

	log.Info("initializing tracer",
		zap.String("endpoint", cfg.Endpoint),
		zap.Float64("sampleRate", cfg.SampleRate))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// WithInsecure:OTLP collector 按部署惯例运行在可信内网(sidecar/
	// 同 VPC),trace 数据不含业务敏感载荷,故不开 TLS。跨不可信网络
	// 部署时应由网关/网格层加密,而不是在此引入证书配置面。
	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(cfg.Endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		log.Error("failed to create OTLP trace exporter", zap.Error(err))
		return nil, fmt.Errorf("failed to create OTLP trace exporter: %w", err)
	}
	log.Info("OTLP trace exporter created", zap.String("endpoint", cfg.Endpoint))

	serviceName := cfg.ServiceName
	if serviceName == "" {
		serviceName = "webmanager-server" // 与模块名一致的回退值
	}
	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(serviceName)),
	)
	if err != nil {
		log.Error("failed to create OTLP resource", zap.Error(err))
		return nil, fmt.Errorf("failed to create OTLP resource: %w", err)
	}

	sampleRate := normalizeSampleRate(cfg.SampleRate)
	if sampleRate != cfg.SampleRate {
		log.Warn("sample rate adjusted",
			zap.Float64("input", cfg.SampleRate),
			zap.Float64("actual", sampleRate))
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(sampleRate))),
	)

	otel.SetTracerProvider(tp)
	log.Info("tracer provider initialized",
		zap.Float64("effectiveSampleRate", sampleRate))

	lc.RegisterTo("cleanup", "tracer-provider", func(ctx context.Context) error {
		shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		log.Info("shutting down tracer provider")
		if err := tp.Shutdown(shutdownCtx); err != nil {
			log.Error("tracer provider shutdown failed", zap.Error(err))
			return err
		}
		log.Info("tracer provider shutdown complete")
		return nil
	})
	log.Debug("tracer provider shutdown registered to cleanup phase")

	return tp, nil
}

// normalizeSampleRate 规范化采样率：<=0 默认为 0.1，>1.0 截断为 1.0。
func normalizeSampleRate(rate float64) float64 {
	if rate <= 0 {
		return 0.1
	}
	if rate > 1.0 {
		return 1.0
	}
	return rate
}
