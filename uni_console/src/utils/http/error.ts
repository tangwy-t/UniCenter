/**
 * HTTP 错误处理模块
 *
 * 提供统一的 HTTP 请求错误处理机制
 *
 * ## 主要功能
 *
 * - 自定义 HttpError 错误类，封装错误信息、状态码、时间戳等
 * - 错误拦截和转换，将 Axios 错误转换为标准的 HttpError
 * - 错误消息处理，根据状态码返回对应的中文提示
 * - 错误日志记录，便于问题追踪和调试
 * - 错误和成功消息的统一展示
 * - 类型守卫函数，用于判断错误类型
 *
 * ## 使用场景
 *
 * - HTTP 请求拦截器中统一处理错误
 * - 业务代码中捕获和处理特定错误
 * - 错误日志收集和上报
 *
 * @module utils/http/error
 * @author Art Design Pro Team
 */
import { AxiosError } from 'axios'
import { ApiStatus, BizCode } from './status'

// 错误响应接口
export interface ErrorResponse {
  /** 错误状态码 */
  code: number
  /** 错误消息 */
  msg: string
  /** 错误附加数据 */
  data?: unknown
}

// 错误日志数据接口
export interface ErrorLogData {
  /** 错误状态码 */
  code: number
  /** 错误消息 */
  message: string
  /** 错误附加数据 */
  data?: unknown
  /** 错误发生时间戳 */
  timestamp: string
  /** 请求 URL */
  url?: string
  /** 请求方法 */
  method?: string
  /** 错误堆栈信息 */
  stack?: string
}

// 自定义 HttpError 类
export class HttpError extends Error {
  public readonly code: number
  public readonly bizCode?: number
  public readonly data?: unknown
  public readonly timestamp: string
  public readonly url?: string
  public readonly method?: string

  constructor(
    message: string,
    code: number,
    options?: {
      data?: unknown
      bizCode?: number
      url?: string
      method?: string
    }
  ) {
    super(message)
    this.name = 'HttpError'
    this.code = code
    this.bizCode = options?.bizCode
    this.data = options?.data
    this.timestamp = new Date().toISOString()
    this.url = options?.url
    this.method = options?.method
  }

  /**
   * 判定 **HTTP 状态**。参数必须来自 `ApiStatus`。
   *
   * 与 isBiz 分开提供,是为了让两个命名空间在**类型/调用点**上就无法混用:
   * `code` 与 `bizCode` 都是 number,直接比较不会报错,只会静默恒真/恒假
   * (见本文件顶部说明)。经语义化访问器调用后,"HTTP 状态"与"业务码"
   * 在调用点显式可见,评审与检索都能定位。
   */
  public isHttpStatus(status: ApiStatus): boolean {
    return this.code === status
  }

  /**
   * 判定 **业务码**。参数必须来自 `BizCode`。
   * 后端未返回信封时为 undefined,故此处用严格相等并允许 bizCode 缺失。
   */
  public isBiz(code: BizCode): boolean {
    return this.bizCode === code
  }

  public toLogData(): ErrorLogData {
    return {
      code: this.code,
      message: this.message,
      data: this.data,
      timestamp: this.timestamp,
      url: this.url,
      method: this.method,
      stack: this.stack
    }
  }
}

/**
 * 获取错误消息
 * @param status 错误状态码
 * @returns 错误消息
 */
const getErrorMessage = (status: number): string => {
  const errorMap: Record<number, string> = {
    [ApiStatus.unauthorized]: '未授权访问，请重新登录',
    [ApiStatus.forbidden]: '禁止访问该资源',
    [ApiStatus.notFound]: '请求的资源不存在',
    [ApiStatus.methodNotAllowed]: '请求方法不允许',
    [ApiStatus.requestTimeout]: '请求超时，请稍后重试',
    [ApiStatus.internalServerError]: '服务器内部错误，请稍后重试',
    [ApiStatus.badGateway]: '网关错误，请稍后重试',
    [ApiStatus.serviceUnavailable]: '服务暂时不可用，请稍后重试',
    [ApiStatus.gatewayTimeout]: '网关超时，请稍后重试'
  }

  return errorMap[status] || '服务器内部错误，请稍后重试'
}

/**
 * 处理错误
 * @param error 错误对象
 * @returns 错误对象
 */
export function handleError(error: AxiosError<ErrorResponse>): never {
  // 处理取消的请求
  if (error.code === 'ERR_CANCELED') {
    console.warn('Request cancelled:', error.message)
    throw new HttpError('请求已取消', ApiStatus.error)
  }

  const statusCode = error.response?.status
  const bizCode = error.response?.data?.code
  const errorMessage = error.response?.data?.msg || error.message
  const requestConfig = error.config

  // 处理网络错误
  if (!error.response) {
    throw new HttpError('网络连接异常，请检查网络连接', ApiStatus.error, {
      url: requestConfig?.url,
      method: requestConfig?.method?.toUpperCase()
    })
  }

  // 优先展示后端返回的 msg，缺失时回退到通用中文提示
  const message = errorMessage || (statusCode ? getErrorMessage(statusCode) : '请求失败')
  throw new HttpError(message, statusCode || ApiStatus.error, {
    data: error.response.data,
    bizCode,
    url: requestConfig?.url,
    method: requestConfig?.method?.toUpperCase()
  })
}

/**
 * 显示错误消息
 * @param error 错误对象
 * @param showMessage 是否显示错误消息
 */
export function showError(error: HttpError, showMessage: boolean = true): void {
  if (showMessage) {
    ElMessage.error(error.message)
  }
  // 记录错误日志
  console.error('[HTTP Error]', error.toLogData())
}

/**
 * 显示成功消息
 * @param message 成功消息
 * @param showMessage 是否显示消息
 */
export function showSuccess(message: string, showMessage: boolean = true): void {
  if (showMessage) {
    ElMessage.success(message)
  }
}

/**
 * 判断是否为 HttpError 类型
 * @param error 错误对象
 * @returns 是否为 HttpError 类型
 */
export const isHttpError = (error: unknown): error is HttpError => {
  return error instanceof HttpError
}
