package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/contextkeys"
	"github.com/tangwy-t/webmanager-server/internal/pkg/datascope"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// maxLogBodyBytes is the maximum number of bytes of request/response body to store in the operation log.
// Bodies exceeding this limit are truncated to prevent storage bloat from large payloads.
const maxLogBodyBytes = 4096

// maxBodyReadSize is the maximum number of bytes to buffer from the request body for logging.
// Requests exceeding this limit are still passed through to downstream handlers in full,
// but only the first maxBodyReadSize bytes are logged.
const maxBodyReadSize = 64 * 1024

// sensitiveFieldSet contains field names (lowercased) whose values should be masked in operation logs.
var sensitiveFieldSet = map[string]struct{}{
	"password":      {},
	"passwd":        {},
	"pwd":           {},
	"newpassword":   {},
	"oldpassword":   {},
	"new_password":  {},
	"old_password":  {},
	"token":         {},
	"accesstoken":   {},
	"access_token":  {},
	"refreshtoken":  {},
	"refresh_token": {},
	"secret":        {},
	"apikey":        {},
	"api_key":       {},
	"privatekey":    {},
	"private_key":   {},
	"secretkey":     {},
	"secret_key":    {},
	"credential":    {},
	"credentials":   {},
	"authorization": {},
}

// CtxOperationModule is the gin context key under which the operation module name is stored.
const CtxOperationModule = "operationModule"

// SetModuleName returns a middleware that injects the given module name into the gin context.
// It must be applied before OperationLogMiddleware so the module name is available.
func SetModuleName(name string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(CtxOperationModule, name)
		c.Next()
	}
}

// OperationLogMiddleware returns a middleware that asynchronously logs POST/PUT/DELETE
// operations to the database via the provided OperationLogService.
// 本中间件仅挂载于鉴权路由组:公共路由(/health、/swagger、/scalar)
// 永远不会经过,无需路径排除。
func OperationLogMiddleware(svc OperationLogServiceInterface, logger logger.LoggerInterface) gin.HandlerFunc {
	return func(c *gin.Context) {
		method := c.Request.Method
		if method != http.MethodPost && method != http.MethodPut && method != http.MethodDelete {
			c.Next()
			return
		}

		// Read and desensitize request body, then restore it for downstream handlers.
		// Use LimitReader to cap memory consumption from large request bodies.
		var requestParams string
		if c.Request.Body != nil {
			bodyBytes, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBodyReadSize))
			if err == nil {
				requestParams = desensitizeJSON(string(bodyBytes))
				// Restore the full body: buffered prefix + remaining stream
				c.Request.Body = io.NopCloser(io.MultiReader(bytes.NewReader(bodyBytes), c.Request.Body))
			}
		}

		startTime := time.Now()

		// Wrap response writer to capture status code and body.
		writer := &bodyCaptureWriter{
			ResponseWriter: c.Writer,
			body:           &bytes.Buffer{},
			statusCode:     http.StatusOK,
		}
		c.Writer = writer

		// 落库动作放在 defer 中执行:handler 无论正常返回、Abort 还是 panic,
		// 都保证记录。panic 时先补记 500 状态与 panic 信息,再重新抛出,
		// 由外层全局 Recovery 中间件继续完成向客户端的 500 响应。
		defer func() {
			if r := recover(); r != nil {
				if writer.statusCode < http.StatusBadRequest {
					writer.statusCode = http.StatusInternalServerError
				}
				if writer.body.Len() == 0 {
					fmt.Fprintf(writer.body, "panic: %v", r)
				}
				saveOperationLog(c, writer, startTime, requestParams, svc, logger)
				panic(r)
			}
			saveOperationLog(c, writer, startTime, requestParams, svc, logger)
		}()

		c.Next()
	}
}

// saveOperationLog builds the log entry from the gin context and captured
// response data, then persists it asynchronously. The entry must be built
// synchronously (gin.Context is not goroutine-safe), while persistence uses
// context.Background() because the request context is cancelled once the
// response has been sent.
//
// 异步落库必须**显式重建**请求上下文中的两个值:TraceID 与 ScopeContext。
// 用裸 context.Background() 会丢掉 ScopeContext,而 sys_operation_log 与
// sys_user 都是 datascope 注册实体:
//   - OperationLogService.Create 会用该 ctx 调 userRepo.FindByID 反查用户名,
//     失去 scope 后这次查询不再受数据权限约束(跨部门读取);
//   - 审计链路与运行时请求的过滤口径不一致,审计结果无法互相印证。
//
// 这里在 goroutine 外同步取出(gin.Context 非并发安全),再注入新 ctx。
func saveOperationLog(c *gin.Context, writer *bodyCaptureWriter, startTime time.Time, requestParams string, svc OperationLogServiceInterface, logger logger.LoggerInterface) {
	// Extract values before the goroutine (gin.Context is not goroutine-safe).
	reqCtx := c.Request.Context()
	traceID, _ := contextkeys.TraceIDFromCtx(reqCtx)
	scopeCtx, _ := datascope.ScopeContextFromCtx(reqCtx)
	entry := buildLogEntry(c, writer, startTime, requestParams)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("operation log middleware panic",
					zap.Any("panic", r),
					zap.String("traceId", traceID),
				)
			}
		}()
		// Use context.Background() because the request context may be cancelled
		// after the response is sent — but carry over traceId 与 scope,
		// 二者都是数据而非取消信号,与请求生命周期无关。
		ctx := contextkeys.WithTraceID(context.Background(), traceID)
		if scopeCtx != nil {
			ctx = datascope.WithScopeContext(ctx, scopeCtx)
		}
		if err := svc.Create(ctx, entry); err != nil {
			logger.Warn("failed to save operation log",
				zap.Error(err),
				zap.String("traceId", traceID),
			)
		}
	}()
}

// buildLogEntry constructs an SysOperationLog from the gin context and captured response data.
// 成功/失败以响应信封的业务码 code 为准(本框架业务错误与成功一样返回 HTTP 200,
// 信封 code 才是真实结果:0=成功、40000=参数错误、50000=服务器内部错误等,
// 全部取值见字典 sys_opt_result_code)。非信封响应(如 panic 捕获)按 HTTP 状态兜底。
// Extracted as a pure function for testability.
func buildLogEntry(c *gin.Context, writer *bodyCaptureWriter, startTime time.Time, requestParams string) *entity.SysOperationLog {
	costTime := int(time.Since(startTime).Milliseconds())
	userID, _ := contextkeys.UserIDFromCtx(c.Request.Context())
	moduleVal, _ := c.Get(CtxOperationModule)
	moduleStr, _ := moduleVal.(string)

	responseBody := writer.body.String()
	var responseResult *string
	if responseBody != "" {
		truncated := truncateString(responseBody, maxLogBodyBytes)
		responseResult = &truncated
	}

	// 业务结果码与错误信息:优先取信封 code/msg;非信封响应且 HTTP 报错时
	// (如 panic 捕获的正文)统一记 50000,错误信息取响应正文。
	code := apperror.CodeOK
	var errorMsg *string
	if envCode, envMsg, ok := parseResponseEnvelope(writer.body.Bytes()); ok {
		code = envCode
		if envCode != apperror.CodeOK {
			if envMsg != "" {
				errorMsg = &envMsg
			} else if responseBody != "" {
				truncated := truncateString(responseBody, maxLogBodyBytes)
				errorMsg = &truncated
			}
		}
	} else if writer.statusCode >= http.StatusBadRequest {
		code = apperror.CodeInternal
		if responseBody != "" {
			truncated := truncateString(responseBody, maxLogBodyBytes)
			errorMsg = &truncated
		}
	}

	var requestParamsPtr *string
	if requestParams != "" {
		truncated := truncateString(requestParams, maxLogBodyBytes)
		requestParamsPtr = &truncated
	}

	requestMethod := c.Request.Method
	requestURL := c.Request.URL.String()
	clientIP := c.ClientIP()

	return &entity.SysOperationLog{
		UserID:         userID,
		Module:         moduleStr,
		OperationType:  methodToOpType(c.Request.Method),
		RequestMethod:  &requestMethod,
		RequestURL:     &requestURL,
		RequestParams:  requestParamsPtr,
		ResponseResult: responseResult,
		CostTime:       &costTime,
		IP:             &clientIP,
		Code:           code,
		ErrorMsg:       errorMsg,
		OperTime:       time.Now(),
	}
}

// parseResponseEnvelope extracts the business code and message from the
// standard JSON response envelope {"code":N,"msg":"...","data":...}.
// Returns ok=false when the body is not an envelope (e.g. panic text, streams).
func parseResponseEnvelope(body []byte) (code int, msg string, ok bool) {
	var env struct {
		Code *int   `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(body, &env); err != nil || env.Code == nil {
		return 0, "", false
	}
	return *env.Code, env.Msg, true
}

// bodyCaptureWriter wraps gin.ResponseWriter to capture the response body and status code.
type bodyCaptureWriter struct {
	gin.ResponseWriter
	body       *bytes.Buffer
	statusCode int
}

func (w *bodyCaptureWriter) Write(b []byte) (int, error) {
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}

func (w *bodyCaptureWriter) WriteHeader(code int) {
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}

// desensitizeJSON replaces sensitive field values (passwords, tokens, secrets, keys, etc.)
// with "***" in a JSON string. If the input is not valid JSON, it is returned unchanged.
func desensitizeJSON(raw string) string {
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return raw
	}
	desensitizeMap(data)
	result, err := json.Marshal(data)
	if err != nil {
		return raw
	}
	return string(result)
}

func desensitizeMap(m map[string]interface{}) {
	for k, v := range m {
		if _, sensitive := sensitiveFieldSet[strings.ToLower(k)]; sensitive {
			m[k] = "***"
			continue
		}
		if nested, ok := v.(map[string]interface{}); ok {
			desensitizeMap(nested)
		}
	}
}

// truncateString truncates s to at most maxLen bytes, appending a truncation marker if truncated.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "...(truncated)"
}

// methodToOpType maps an HTTP method to a Chinese operation type label.
func methodToOpType(method string) string {
	switch method {
	case http.MethodPost:
		return "新增"
	case http.MethodPut:
		return "修改"
	case http.MethodDelete:
		return "删除"
	default:
		return "其他"
	}
}
