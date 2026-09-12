// Package logger provides a structured logging wrapper around zap with
// configurable output formats, log rotation, and a testable interface.
package logger

import (
	"context"
	"github.com/tangwy-t/webmanager-server/internal/pkg/config"
	"github.com/tangwy-t/webmanager-server/internal/pkg/contextkeys"
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

// LoggerInterface defines the contract for structured logging,
// enabling dependency inversion and testability.
type LoggerInterface interface {
	Debug(msg string, fields ...zap.Field)
	Info(msg string, fields ...zap.Field)
	Warn(msg string, fields ...zap.Field)
	Error(msg string, fields ...zap.Field)
	IsDebug() bool
}

// Logger is a structured logger backed by zap.
type Logger struct {
	zap *zap.Logger
}

// contextLogger is a Logger wrapper that auto-attaches traceId from context.
type contextLogger struct {
	zap     *zap.Logger
	traceID string
}

// New creates a new Logger from the provided LogConfig.
func New(cfg *config.LogConfig) (*Logger, error) {
	// Encoder configuration
	encoderCfg := zap.NewProductionEncoderConfig()
	encoderCfg.EncodeTime = zapcore.ISO8601TimeEncoder
	if cfg.Format == "console" {
		encoderCfg = zap.NewDevelopmentEncoderConfig()
		if cfg.Output != "file" {
			encoderCfg.EncodeLevel = zapcore.CapitalColorLevelEncoder
		}
	}

	var encoder zapcore.Encoder
	if cfg.Format == "console" {
		encoder = zapcore.NewConsoleEncoder(encoderCfg)
	} else {
		encoder = zapcore.NewJSONEncoder(encoderCfg)
	}

	// Output target
	var writeSyncer zapcore.WriteSyncer
	if cfg.Output == "file" {
		// File output with log rotation
		rotator := &lumberjack.Logger{
			Filename:   cfg.FilePath,
			MaxSize:    cfg.MaxSize,
			MaxBackups: cfg.MaxBackups,
			MaxAge:     cfg.MaxAge,
			Compress:   cfg.Compress,
		}
		// Write to both stdout and file
		writeSyncer = zapcore.NewMultiWriteSyncer(
			zapcore.AddSync(rotator),
			zapcore.AddSync(zapcore.Lock(os.Stdout)),
		)
	} else {
		writeSyncer = zapcore.AddSync(zapcore.Lock(os.Stdout))
	}

	core := zapcore.NewCore(encoder, writeSyncer, zap.NewAtomicLevelAt(parseLevel(cfg.Level)))

	opts := []zap.Option{zap.AddCallerSkip(1)}
	if cfg.Caller {
		opts = append(opts, zap.AddCaller())
	}
	if cfg.Development {
		opts = append(opts, zap.Development())
	}

	zapLog := zap.New(core, opts...)

	return &Logger{zap: zapLog}, nil
}

// NewWithCore creates a Logger from an existing zapcore.Core, primarily for testing.
func NewWithCore(core zapcore.Core) (*Logger, error) {
	opts := []zap.Option{zap.AddCallerSkip(1)}
	return &Logger{zap: zap.New(core, opts...)}, nil
}

// NewNop creates a Logger that discards all output, useful for testing.
func NewNop() *Logger {
	return &Logger{zap: zap.NewNop()}
}

// Level returns the configured log level string.
func (l *Logger) Level() string {
	return l.zap.Level().String()
}

// IsDebug returns true if the logger is configured at debug level.
func (l *Logger) IsDebug() bool {
	return l.zap.Core().Enabled(zapcore.DebugLevel)
}

func parseLevel(level string) zapcore.Level {
	switch level {
	case "debug":
		return zap.DebugLevel
	case "info":
		return zap.InfoLevel
	case "warn":
		return zap.WarnLevel
	case "error":
		return zap.ErrorLevel
	default:
		return zap.InfoLevel
	}
}

// Debug logs a message at debug level.
func (l *Logger) Debug(msg string, fields ...zap.Field) { l.zap.Debug(msg, fields...) }

// Info logs a message at info level.
func (l *Logger) Info(msg string, fields ...zap.Field) { l.zap.Info(msg, fields...) }

// Warn logs a message at warn level.
func (l *Logger) Warn(msg string, fields ...zap.Field) { l.zap.Warn(msg, fields...) }

// Error logs a message at error level.
func (l *Logger) Error(msg string, fields ...zap.Field) { l.zap.Error(msg, fields...) }

// Fatal logs a message at fatal level and calls os.Exit(1).
func (l *Logger) Fatal(msg string, fields ...zap.Field) { l.zap.Fatal(msg, fields...) }

// Sync flushes any buffered log entries.
func (l *Logger) Sync() { l.zap.Sync() }

// GetZap returns the underlying *zap.Logger for use by GORM logger bridge.
func (l *Logger) GetZap() *zap.Logger {
	return l.zap
}

// WithContext 从 context 提取 traceId，返回带 traceId 自动附加的 contextLogger。
func (l *Logger) WithContext(ctx context.Context) *contextLogger {
	traceID, _ := contextkeys.TraceIDFromCtx(ctx)
	return &contextLogger{zap: l.zap, traceID: traceID}
}

// Debug 输出 Debug 级别日志，自动附加 traceId（如果存在）。
func (cl *contextLogger) Debug(msg string, fields ...zap.Field) {
	if cl.traceID != "" {
		fields = append(fields, zap.String("traceId", cl.traceID))
	}
	cl.zap.Debug(msg, fields...)
}

// Info 输出 Info 级别日志，自动附加 traceId（如果存在）。
func (cl *contextLogger) Info(msg string, fields ...zap.Field) {
	if cl.traceID != "" {
		fields = append(fields, zap.String("traceId", cl.traceID))
	}
	cl.zap.Info(msg, fields...)
}

// Warn 输出 Warn 级别日志，自动附加 traceId（如果存在）。
func (cl *contextLogger) Warn(msg string, fields ...zap.Field) {
	if cl.traceID != "" {
		fields = append(fields, zap.String("traceId", cl.traceID))
	}
	cl.zap.Warn(msg, fields...)
}

// Error 输出 Error 级别日志，自动附加 traceId（如果存在）。
func (cl *contextLogger) Error(msg string, fields ...zap.Field) {
	if cl.traceID != "" {
		fields = append(fields, zap.String("traceId", cl.traceID))
	}
	cl.zap.Error(msg, fields...)
}
