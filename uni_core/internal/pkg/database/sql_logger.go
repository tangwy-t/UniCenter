package database

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	gormlogger "gorm.io/gorm/logger"

	"github.com/tangwy-t/webmanager-server/internal/pkg/contextkeys"
)

var (
	reString = regexp.MustCompile(`'[^']*'`)
	reNumber = regexp.MustCompile(`\b\d+\b`)
)

// ZapGormLogger 实现 gormlogger.Interface，桥接 GORM 日志到 Zap。
type ZapGormLogger struct {
	zap           *zap.Logger
	slowThreshold time.Duration
	slowEnabled   bool
	stats         *SQLStats
	logLevel      gormlogger.LogLevel
}

// NewZapGormLogger 创建 ZapGormLogger 实例。
func NewZapGormLogger(zap *zap.Logger, slowThreshold time.Duration, slowEnabled bool, stats *SQLStats) *ZapGormLogger {
	return &ZapGormLogger{
		zap:           zap,
		slowThreshold: slowThreshold,
		slowEnabled:   slowEnabled,
		stats:         stats,
		logLevel:      gormlogger.Info,
	}
}

// LogMode 返回带指定日志级别的副本,不修改接收者。GORM 约定 LogMode
// 应返回新实例(如 db.Debug() 调用 Session(&gorm.Session{Logger:
// db.Logger.LogMode(...)})):此前原地改共享实例并返回自身,任何
// db.Debug() 会全局且不可逆地打开 SQL 日志。
func (l *ZapGormLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	clone := *l
	clone.logLevel = level
	return &clone
}

// Info 输出 Info 级别日志。
func (l *ZapGormLogger) Info(ctx context.Context, msg string, data ...interface{}) {
	if l.logLevel >= gormlogger.Info {
		l.zap.Info(fmt.Sprintf(msg, data...))
	}
}

// Warn 输出 Warn 级别日志。
func (l *ZapGormLogger) Warn(ctx context.Context, msg string, data ...interface{}) {
	if l.logLevel >= gormlogger.Warn {
		l.zap.Warn(fmt.Sprintf(msg, data...))
	}
}

// Error 输出 Error 级别日志。
func (l *ZapGormLogger) Error(ctx context.Context, msg string, data ...interface{}) {
	if l.logLevel >= gormlogger.Error {
		l.zap.Error(fmt.Sprintf(msg, data...))
	}
}

// Trace 记录 SQL 执行日志、统计和慢查询检测。
func (l *ZapGormLogger) Trace(ctx context.Context, begin time.Time, fc func() (sql string, rowsAffected int64), err error) {
	elapsed := time.Since(begin)

	sql, rows := fc()

	// 提取操作类型和表名
	op := extractOp(sql)
	table := extractTable(sql)

	// ── OpenTelemetry SQL Span ──────────────────────────────────
	ctx, span := otel.Tracer("github.com/tangwy-t/webmanager-server/internal/pkg/database").Start(ctx, "SQL "+op+" "+table,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithTimestamp(begin),
	)
	defer span.End(trace.WithTimestamp(time.Now()))

	span.SetAttributes(
		attribute.String("db.system", "mysql"),
		attribute.String("db.operation", op),
		attribute.String("db.sql.table", table),
		attribute.Int64("db.rows_affected", rows),
	)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
	}
	// ── End OTel Span ───────────────────────────────────────────

	// 提取 traceId
	traceID, _ := contextkeys.TraceIDFromCtx(ctx)

	// 慢查询检测
	isSlow := l.slowEnabled && elapsed > l.slowThreshold

	// 统计记录（含慢查询标记，热路径一次写入）
	l.stats.Record(table, op, elapsed, sql, err != nil, isSlow)

	if isSlow {
		l.zap.Warn("slow query",
			zap.String("traceId", traceID),
			zap.String("sql", parameterize(sql)),
			zap.Duration("elapsed", elapsed),
			zap.Int64("rows", rows),
			zap.String("table", table),
			zap.String("op", op),
		)
	}

	// Debug 级别 SQL trace
	if l.logLevel >= gormlogger.Info {
		switch {
		case err != nil:
			l.zap.Error("SQL trace",
				zap.String("traceId", traceID),
				zap.String("sql", sql),
				zap.Duration("elapsed", elapsed),
				zap.Int64("rows", rows),
				zap.Error(err),
			)
		default:
			l.zap.Debug("SQL trace",
				zap.String("traceId", traceID),
				zap.String("sql", sql),
				zap.Duration("elapsed", elapsed),
				zap.Int64("rows", rows),
			)
		}
	}
}

// extractOp 从 SQL 语句中提取操作类型。
func extractOp(sql string) string {
	sql = strings.TrimSpace(sql)
	if len(sql) == 0 {
		return "OTHER"
	}
	parts := strings.Fields(sql)
	if len(parts) == 0 {
		return "OTHER"
	}
	return strings.ToUpper(parts[0])
}

// reTable 匹配 FROM/JOIN/INTO/UPDATE 后面的第一个表名。
// GORM 生成的 SQL 表名会被反引号(或双引号)包裹,故词前允许一个可选的
// 引号字符;词前还允许子查询左括号(如 FROM (SELECT ...) t);表名支持
// 库名限定形式(如 information_schema.columns)以完整呈现系统库查询。
// 包级编译:extractTable 在每条 SQL 的热路径上调用,不能每次重新编译。
var reTable = regexp.MustCompile(`(?i)(?:FROM|JOIN|INTO|UPDATE)\s+[\(\s]*[\x60"]?(\w+(?:[\x60"]*\.\s*[\x60"]?\w+)*)`)

// reSubqueryKeyword 判断候选表名是否其实是 SQL 关键字:
// 子查询 / UNION 等结构会把 FROM 后的第一个词变成关键字而非表名。
var reSubqueryKeyword = regexp.MustCompile(`(?i)^(SELECT|INSERT|UPDATE|DELETE|UNION|WITH|EXISTS|VALUES|SET)$`)

// extractTable 从 SQL 语句中提取表名。
// 依次检查所有 FROM/JOIN/INTO/UPDATE 候选,跳过子查询等关键字误匹配,
// 兼容反引号/双引号包裹的表名(MySQL 常见写法),返回值剥离全部引号。
func extractTable(sql string) string {
	for _, m := range reTable.FindAllStringSubmatchIndex(sql, -1) {
		if len(m) < 4 {
			continue
		}
		name := sql[m[2]:m[3]]
		name = strings.ReplaceAll(strings.ReplaceAll(name, "`", ""), `"`, "")
		if name == "" || reSubqueryKeyword.MatchString(name) {
			// 空捕获或子查询关键字,继续找下一个候选
			continue
		}
		return name
	}
	return ""
}

// parameterize 将 SQL 中的字符串和数字字面量替换为占位符。
func parameterize(sql string) string {
	sql = reString.ReplaceAllString(sql, "?")
	sql = reNumber.ReplaceAllString(sql, "?")
	return sql
}
