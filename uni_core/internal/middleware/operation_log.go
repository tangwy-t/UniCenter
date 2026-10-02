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

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/contextkeys"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/datascope"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// MaxLogBodyBytes is the maximum number of bytes of request/response body to store in the operation log.
// Bodies exceeding this limit are truncated to prevent storage bloat from large payloads.
//
// 导出（原名 maxLogBodyBytes）：服务侧的任务审计挂钩（service/docker_audit.go，6b）
// 与中间件共用同一截断口径 —— 两处各写一个 4096 会在一边调口径时静默漂移。
const MaxLogBodyBytes = 4096

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

// binaryUploadPlaceholders 声明「请求体是二进制流、绝不进 body 捕获」的端点与其
// 审计占位文案。匹配按路由模板的**后缀**（gin 的 FullPath 带可配置的 APIPrefix，
// 后缀匹配让声明不随部署前缀漂移）。
//
// 为什么在 octet-stream 兜底之外还要端点声明：给占位一个**具体**的文案
// （「二进制构建上下文」而非泛化的「二进制请求体」），审计读者一眼知道这条
// 记录搬的是什么。今后新端点要专属文案就往这里加一行；不加也照样被
// octet-stream 兜底罩住（见 binaryUploadExempt）。
var binaryUploadPlaceholders = []struct{ suffix, label string }{
	{"/docker/hosts/:id/build-context", "二进制构建上下文"},
}

// binaryUploadExempt 判定一次请求是否豁免 body 捕获；豁免时返回审计占位文案。
//
// 两条腿，各司其职：
//   - 端点声明（服务端事实）：命中即豁免 —— 路由是我们注册的，声明不会漏；
//   - Content-Type octet-stream（客户端声明）：兜底腿，也是「不易漏」的那条 ——
//     今后任何二进制/流式端点只要按本仓库惯例声明 octet-stream（raw body
//     端点的统一形态，构建上下文处理器还以此判形执法），就自动豁免，不需要
//     记得回来改这个文件。
//
// 为什么豁免（QA P0-3）：本中间件对 POST /docker/hosts/:id/build-context 的
// body 捕获把 gzip 原始字节截样落进 sys_operation_log.request_params —— 审计表
// 收二进制垃圾，且 gzip 流的可解压前缀（tar 头部：Dockerfile、.env 之类）
// 是**不可控的泄漏面**（脱敏只认 JSON 键，二进制正文零掩码）。豁免后审计仍
// 记录这条操作（谁/何时/对哪个主机/成败/耗时），只是正文换成占位 —— 审计要的
// 是「谁做了什么」，不是被搬运的字节本身。
func binaryUploadExempt(c *gin.Context) (string, bool) {
	for _, ep := range binaryUploadPlaceholders {
		if strings.HasSuffix(c.FullPath(), ep.suffix) {
			return binaryBodyPlaceholder(c, ep.label), true
		}
	}
	if ct := c.ContentType(); strings.HasPrefix(ct, "application/octet-stream") {
		return binaryBodyPlaceholder(c, "二进制请求体"), true
	}
	return "", false
}

// binaryBodyPlaceholder 拼占位文案：正文不采集，只记搬运的量级（chunked 无
// Content-Length 时说「长度未知」—— 审计不编数字）。
func binaryBodyPlaceholder(c *gin.Context, label string) string {
	if n := c.Request.ContentLength; n >= 0 {
		return fmt.Sprintf("%s %d 字节", label, n)
	}
	return label + "（长度未知）"
}

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
		//
		// 二进制上传端点豁免（P0-3，见 binaryUploadExempt）：豁免路径**零接触**
		// body —— 不预读、不 MultiReader 拼回（拼回语义上无损，但上传通道的
		// body 是一根要原样流进 handler 的水管，core 中转零落盘、背压沿栈传导；
		// 「唯一读者是 handler」让 MaxBytesReader 的次序与背压链保持单一事实源）。
		// 审计里落的是「二进制…N 字节」占位，不是正文。
		var requestParams string
		if placeholder, exempt := binaryUploadExempt(c); exempt {
			requestParams = placeholder
		} else if c.Request.Body != nil {
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
		truncated := truncateString(responseBody, MaxLogBodyBytes)
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
				truncated := truncateString(responseBody, MaxLogBodyBytes)
				errorMsg = &truncated
			}
		}
	} else if writer.statusCode >= http.StatusBadRequest {
		code = apperror.CodeInternal
		if responseBody != "" {
			truncated := truncateString(responseBody, MaxLogBodyBytes)
			errorMsg = &truncated
		}
	}

	var requestParamsPtr *string
	if requestParams != "" {
		truncated := truncateString(requestParams, MaxLogBodyBytes)
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

// Unwrap 把这一层包装对 http.ResponseController 透明（连接级 deadline 的接管
// 需要一路穿到 net/http 原始 writer）。必须显式写出来：内嵌的是 gin.ResponseWriter
// **接口**，而接口的方法集里没有 Unwrap（只有 gin 的具体类型 responseWriter 有），
// 所以「内嵌接口」的包装不会自动继承它 —— 少了这一行，middleware.LongLived 在
// 带本包装的 POST 端点（构建上下文上传）上会拿到 ErrNotSupported 而静默失效。
func (w *bodyCaptureWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

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

// DesensitizeJSON 是脱敏入口的导出包装（原名 desensitizeJSON，服务侧任务审计挂钩
// 6b 复用）：中间件与审计共享同一份 sensitiveFieldSet —— 敏感字段集合是治理口径的
// 单一事实源，复制一份就会在中间件加新字段名时让审计侧静默落伍。非 JSON 文本原样
// 返回（结论句是普通句子时脱敏是空操作，但「照样过一遍」这条纪律保证：结论句若
// 恰为 JSON/含敏感键，也走同一套遮蔽）。
func DesensitizeJSON(raw string) string { return desensitizeJSON(raw) }

// TruncateString 是截断的导出包装（服务侧审计挂钩与中间件共用同一截断形态：
// 尾部 "...(truncated)" 标记是读者识别「被截断」的显式信号）。
func TruncateString(s string, maxLen int) string { return truncateString(s, maxLen) }

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
