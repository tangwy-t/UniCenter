/**
 * HTTP 请求封装模块（对接 Go 后端统一契约）
 *
 * - 成功：HTTP 200 + `{ code: 0, msg, data }`
 * - 失败：HTTP 非 2xx + `{ code: apperror 业务码, msg, data }`
 * - 鉴权：`Authorization: Bearer <accessToken>`
 * - 401（未授权 / token 过期）自动 refresh 并重放（单飞）
 */
import axios, { AxiosRequestConfig, AxiosResponse, InternalAxiosRequestConfig } from 'axios'
import { useUserStore } from '@/store/modules/user'
import { ApiStatus, BizCode } from './status'
import { HttpError, handleError, showError, showSuccess } from './error'
import { BaseResponse } from '@/types'

/** 请求配置常量 */
const REQUEST_TIMEOUT = 15000
const LOGOUT_DELAY = 500
const UNAUTHORIZED_DEBOUNCE_TIME = 3000

/** 401 防抖状态 */
let isUnauthorizedErrorShown = false
let unauthorizedTimer: NodeJS.Timeout | null = null

/** 扩展 AxiosRequestConfig */
interface ExtendedAxiosRequestConfig extends AxiosRequestConfig {
  showErrorMessage?: boolean
  showSuccessMessage?: boolean
}

const { VITE_API_URL, VITE_WITH_CREDENTIALS, VITE_API_PREFIX } = import.meta.env

const AUTH_REFRESH_PATH = `${VITE_API_PREFIX}/refresh`

/** token 刷新单飞 */
let isRefreshing = false
let pendingQueue: Array<{ resolve: (token: string) => void; reject: (e: unknown) => void }> = []

/** Axios 实例 */
const axiosInstance = axios.create({
  timeout: REQUEST_TIMEOUT,
  baseURL: VITE_API_URL,
  withCredentials: VITE_WITH_CREDENTIALS === 'true',
  validateStatus: (status) => status >= 200 && status < 300,
  transformResponse: [
    (data, headers) => {
      const contentType = headers['content-type']
      if (typeof contentType === 'string' && contentType.includes('application/json')) {
        try {
          return JSON.parse(data)
        } catch {
          return data
        }
      }
      return data
    }
  ]
})

/** 请求拦截器 */
axiosInstance.interceptors.request.use(
  (request: InternalAxiosRequestConfig) => {
    const { accessToken } = useUserStore()
    if (accessToken) request.headers.set('Authorization', `Bearer ${accessToken}`)

    if (request.data && !(request.data instanceof FormData) && !request.headers['Content-Type']) {
      request.headers.set('Content-Type', 'application/json')
      request.data = JSON.stringify(request.data)
    }

    return request
  },
  (error) => {
    showError(createHttpError('请求配置错误', ApiStatus.error))
    return Promise.reject(error)
  }
)

/** 响应拦截器 */
axiosInstance.interceptors.response.use(
  (response: AxiosResponse<BaseResponse>) => {
    // blob 下载走原始二进制数据,不做 {code,msg,data} 信封解包
    if (response.config.responseType === 'blob') return response
    const { code, msg } = response.data
    // 成功判定:后端 app.Success 恒返回 HTTP 200 + 信封 code 0,
    // 所以这里比较的是**业务码** BizCode.ok(=0),而非 HTTP 状态码。
    //
    // 注意区分两个命名空间(见 utils/http/status.ts):
    //   - 业务码由 response.data.code 读取 → 与 BizCode.* 比较
    //   - HTTP 状态由 error.response.status 读取 → 与 ApiStatus.* 比较
    // 把 ApiStatus(HTTP)用于业务码比较会恒为 false:
    // 后端未授权业务码是 10001,永不等于 HTTP 401。
    if (code === BizCode.ok) return response
    throw createHttpError(msg || '请求失败', code)
  },
  async (error) => {
    if (error.response?.status === ApiStatus.unauthorized) {
      const config = error.config as InternalAxiosRequestConfig & { _retried?: boolean }
      // 仅在已登录（存在 refreshToken）且未重放过时刷新重放；
      // 登录/验证码等无 token 场景直接报错。_retried 防死循环:
      // refresh 成功但新 token 立即又被判 401(时钟偏差/极短 TTL)时,
      // 重放请求再次进入此处会因 _retried 直接放弃,而非无限递归。
      if (useUserStore().refreshToken && !config._retried) {
        config._retried = true
        const newToken = await refreshAndReplay(config)
        if (newToken) return axiosInstance.request(config)
        handleUnauthorizedError(error.response?.data?.msg || '未授权访问，请重新登录')
      }
    }
    return Promise.reject(handleError(error))
  }
)

/** 统一创建 HttpError */
function createHttpError(message: string, code: number) {
  return new HttpError(message, code)
}

/** 处理 401 错误（refresh 失败后登出并提示） */
function handleUnauthorizedError(message?: string): never {
  const error = createHttpError(message || '未授权访问，请重新登录', ApiStatus.unauthorized)

  if (!isUnauthorizedErrorShown) {
    isUnauthorizedErrorShown = true
    logOut()

    unauthorizedTimer = setTimeout(resetUnauthorizedError, UNAUTHORIZED_DEBOUNCE_TIME)

    showError(error, true)
  }

  throw error
}

/** 重置 401 防抖状态 */
function resetUnauthorizedError() {
  isUnauthorizedErrorShown = false
  if (unauthorizedTimer) clearTimeout(unauthorizedTimer)
  unauthorizedTimer = null
}

/** 退出登录函数 */
function logOut() {
  setTimeout(() => {
    useUserStore().logOut()
  }, LOGOUT_DELAY)
}

/** 刷新访问令牌。
 *
 *  刻意使用**独立的 axios 实例**而非 axiosInstance:
 *  refresh 请求若走 axiosInstance,其 401 会再次进入响应拦截器,
 *  与正在进行的刷新互相递归(拦截器内已有 /refresh 路径保护,
 *  但那是"碰巧"依赖 URL 判断,不如从实例层面隔离彻底)。
 *
 *  与 axiosInstance 的三点差异都必须显式补齐,否则行为静默退化:
 *  1. 不继承请求拦截器 —— 本请求本来就要用旧 refreshToken 换新令牌,
 *     不带 Authorization 头是正确的;
 *  2. 不继承响应拦截器 —— 信封 {code,msg,data} 需自行解包,
 *     否则非 2xx 的 HTTP 状态不会抛错(旧实现用默认 validateStatus,
 *     恰好能抛,但依赖的是"裸 axios 的默认行为"这一巧合);
 *  3. 失败必须抛 HttpError 而非裸 Error —— 调用方据此判断,
 *     且旧实现 `throw new Error('refresh failed')` 丢掉了服务端 msg
 *     (例如"账号已被禁用,请联系管理员"),用户只能看到笼统提示。 */
const refreshClient = axios.create({
  timeout: REQUEST_TIMEOUT,
  baseURL: VITE_API_URL,
  withCredentials: VITE_WITH_CREDENTIALS === 'true',
  validateStatus: (status) => status >= 200 && status < 300
})

async function refreshAccessToken(): Promise<string> {
  const { refreshToken } = useUserStore()
  if (!refreshToken) throw createHttpError('登录状态已失效，请重新登录', ApiStatus.unauthorized)

  let fresh: AxiosResponse<BaseResponse<Api.Auth.TokenResp>>
  try {
    fresh = await refreshClient.post<BaseResponse<Api.Auth.TokenResp>>(AUTH_REFRESH_PATH, {
      refreshToken
    })
  } catch (error) {
    // 非 2xx(含 401):保留服务端 msg,便于用户理解"为什么需要重新登录"。
    const msg = (error as { response?: { data?: { msg?: string } } })?.response?.data?.msg
    throw createHttpError(msg || '登录状态已失效，请重新登录', ApiStatus.unauthorized)
  }

  if (fresh.data.code === BizCode.ok && fresh.data.data) {
    const { accessToken, refreshToken: nextRefresh } = fresh.data.data
    useUserStore().setToken(accessToken, nextRefresh)
    return accessToken
  }
  // 2xx 但业务码非成功:同样保留服务端 msg。
  throw createHttpError(
    fresh.data.msg || '登录状态已失效，请重新登录',
    // 业务码缺失时兜底为未授权业务码(而非 HTTP 401):
    // 这里构造的是 HttpError 的 code,下游按业务码语义使用。
    fresh.data.code ?? BizCode.unauthorized
  )
}

function flushPending(token: string) {
  pendingQueue.forEach((p) => p.resolve(token))
  pendingQueue = []
}

function failPending(err: unknown) {
  pendingQueue.forEach((p) => p.reject(err))
  pendingQueue = []
}

/** 401 后刷新并重放原请求；失败返回 null */
async function refreshAndReplay(config?: InternalAxiosRequestConfig): Promise<string | null> {
  if (!config || config.url?.includes('/refresh')) return null
  if (isRefreshing) {
    return new Promise<string>((resolve, reject) => pendingQueue.push({ resolve, reject }))
  }
  isRefreshing = true
  try {
    const token = await refreshAccessToken()
    flushPending(token)
    return token
  } catch (e) {
    failPending(e)
    return null
  } finally {
    isRefreshing = false
  }
}

/** 请求函数。
 *
 *  说明:此处曾有一整套重试机制(shouldRetry / retryRequest / RETRY_DELAY),
 *  但 MAX_RETRIES 恒为 0 且无任何调用方覆盖,整段代码不可达 ——
 *  保留它会让后续维护者误以为请求具备重试能力。已移除;
 *  如确需重试,应在确认后端接口幂等后重新引入,并配套重试上限与退避。
 */
async function request<T = any>(config: ExtendedAxiosRequestConfig): Promise<T> {
  // POST | PUT 参数自动填充
  if (
    ['POST', 'PUT'].includes(config.method?.toUpperCase() || '') &&
    config.params &&
    !config.data
  ) {
    config.data = config.params
    config.params = undefined
  }

  try {
    const res = await axiosInstance.request<BaseResponse<T>>(config)

    if (config.showSuccessMessage && res.data.msg) {
      showSuccess(res.data.msg)
    }

    return res.data.data as T
  } catch (error) {
    if (error instanceof HttpError && !error.isHttpStatus(ApiStatus.unauthorized)) {
      const showMsg = config.showErrorMessage !== false
      showError(error, showMsg)
    }
    return Promise.reject(error)
  }
}

/** 下载二进制文件（携带鉴权头,返回 Blob）。
 *  适用于 pprof 原始 profile/trace 等非 JSON 信封的接口。
 *
 *  错误处理必须与 request() 同契约:此前 download 直接返回
 *  axiosInstance.request 的 promise,既不弹错误提示也不归一化为
 *  HttpError —— pprof 导出失败(如 profile 未启用返回 403)时调用方
 *  只拿到一个裸 axios 错误,用户看不到任何提示,且无法用
 *  isHttpError(error) / error.bizCode 判断原因。
 *
 *  注意 blob 响应不解信封(拦截器已按 responseType === 'blob' 跳过),
 *  因此这里只处理失败路径。 */
async function download(config: ExtendedAxiosRequestConfig): Promise<Blob> {
  try {
    const res = await axiosInstance.request<Blob>({
      ...config,
      responseType: 'blob'
    })
    return res.data
  } catch (error) {
    // 与 request() 保持一致:401 由拦截器统一触发登出,此处不重复提示。
    if (error instanceof HttpError && !error.isHttpStatus(ApiStatus.unauthorized)) {
      const showMsg = config.showErrorMessage !== false
      showError(error, showMsg)
    }
    return Promise.reject(error)
  }
}

/** API 方法集合 */
const api = {
  download,
  get<T>(config: ExtendedAxiosRequestConfig) {
    return request<T>({ ...config, method: 'GET' })
  },
  post<T>(config: ExtendedAxiosRequestConfig) {
    return request<T>({ ...config, method: 'POST' })
  },
  put<T>(config: ExtendedAxiosRequestConfig) {
    return request<T>({ ...config, method: 'PUT' })
  },
  del<T>(config: ExtendedAxiosRequestConfig) {
    return request<T>({ ...config, method: 'DELETE' })
  },
  request<T>(config: ExtendedAxiosRequestConfig) {
    return request<T>(config)
  }
}

export default api
