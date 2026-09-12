/**
 * 接口状态码
 *
 * ## 两个命名空间,不要混用
 *
 * 本项目同时存在两套编号,它们**互不相交**:
 *
 * 1. **HTTP 状态码**(400/401/403/404/500...)—— 传输层语义,
 *    通过 `error.response.status` 读取,对应 `HttpError.code`。
 * 2. **业务码**(0=成功,10001=未授权,10002=无权限,40000=参数错误,
 *    40400=资源不存在,50000=服务器内部错误...)—— 见后端
 *    `internal/pkg/apperror` 的 Code* 常量,通过响应信封 `{code,msg,data}`
 *    的 `code` 字段读取,对应 `HttpError.bizCode`。
 *
 * 后端对每个错误**同时**给出两者:`app.Error` 用 AppError.HTTPStatus 作为
 * HTTP 状态、AppError.Code 作为信封 code;成功路径恒为 HTTP 200 + code 0
 * (见 `app.Success`)。消费方按手上的字段选择对应命名空间即可。
 *
 * 本枚举此前把两者混在一起(`success = 0` 是业务码,而
 * `unauthorized = 401` 是 HTTP 状态),使得 `code === ApiStatus.unauthorized`
 * 之类的比较恒为 false —— 后端业务码是 10001,永不等于 401。
 * 现拆为 ApiStatus(仅 HTTP)与 BizCode(仅业务码)两个枚举。
 */
export enum ApiStatus {
  /** 通用错误占位(无具体状态可用时) */
  error = 400,
  unauthorized = 401, // HTTP 状态码:未授权
  forbidden = 403, // 禁止访问
  notFound = 404, // 未找到
  methodNotAllowed = 405, // 方法不允许
  requestTimeout = 408, // 请求超时
  internalServerError = 500, // 服务器错误
  notImplemented = 501, // 未实现
  badGateway = 502, // 网关错误
  serviceUnavailable = 503, // 服务不可用
  gatewayTimeout = 504, // 网关超时
  httpVersionNotSupported = 505 // HTTP版本不支持
}

/**
 * 业务码 —— 与后端 `internal/pkg/apperror` 的 Code* 常量一一对应。
 *
 * 仅在读取响应信封的 `code` 字段(即 `HttpError.bizCode`)时使用;
 * 不要拿 HTTP 状态码与之比较。
 */
export enum BizCode {
  /** 成功(后端 apperror.CodeOK) */
  ok = 0,
  /** 未授权,需重新登录 */
  unauthorized = 10001,
  /** 无权限 */
  forbidden = 10002,
  /** 令牌过期 */
  tokenExpired = 10003,
  /** 需要验证码 */
  captchaRequired = 10004,
  /** 验证码错误 */
  captchaIncorrect = 10005,
  /** 验证码过期 */
  captchaExpired = 10006,
  /** 触发限流 */
  rateLimited = 10007,
  /** 账号被锁定或禁用 */
  accountLocked = 10008,
  /** 参数错误 */
  badRequest = 40000,
  /** 资源不存在 */
  notFound = 40400,
  /** 资源冲突 */
  conflict = 40900,
  /** 服务器内部错误 */
  internal = 50000
}
