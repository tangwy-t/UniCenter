import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { AxiosAdapter, AxiosRequestConfig } from 'axios'

// 与 interceptor.test.ts 相同的隔离:本测试只关心 HttpError 的属性语义。
const storeState = {
  accessToken: '',
  refreshToken: '',
  setToken: vi.fn()
}
vi.mock('@/store/modules/user', () => ({
  useUserStore: () => storeState
}))
vi.mock('element-plus', () => ({
  ElMessage: { success: vi.fn(), error: vi.fn(), warning: vi.fn() }
}))

import api from './index'
import { HttpError } from './error'
import { ApiStatus, BizCode } from './status'

function respondWith(status: number, body: unknown): AxiosAdapter {
  return async (config: AxiosRequestConfig) => {
    const response = {
      data: body,
      status,
      statusText: '',
      headers: {},
      config: config as never
    }
    if (status >= 200 && status < 300) return response
    throw Object.assign(new Error('Request failed'), {
      isAxiosError: true,
      response,
      config
    })
  }
}

/**
 * 回归测试:HttpError 的两个编号必须各归其位。
 *
 * 历史缺陷(本次修复):
 *   - `error.code !== ApiStatus.unauthorized` 恒为 true
 *   - `error.code === ApiStatus.unauthorized` 恒为 false(401 分支成死代码)
 * 根因是 `HttpError.code` 承载 HTTP 状态,却被拿去与 HTTP 枚举比较时
 * 用错了命名空间 —— 但两者都是 number,类型系统不会报错。
 *
 * 本套断言锁定 `code = HTTP 状态` / `bizCode = 业务码` 的赋值来源,
 * 使上述混用一旦复发立即失败。
 */
describe('HttpError:HTTP 状态与业务码分属两个命名空间', () => {
  beforeEach(() => {
    storeState.refreshToken = ''
    storeState.accessToken = ''
    vi.clearAllMocks()
  })

  it('HTTP 401 信封:code 是 401(HTTP),bizCode 是 10001(业务)', async () => {
    const adapter = respondWith(401, { code: 10001, msg: '未登录或 token 已过期', data: null })
    const error = await api
      .get({ url: '/x', adapter } as never)
      .then(() => null)
      .catch((e: unknown) => e)

    expect(error).toBeInstanceOf(HttpError)
    const httpError = error as HttpError

    // code 来自 HTTP 状态行
    expect(httpError.code).toBe(ApiStatus.unauthorized)
    expect(httpError.isHttpStatus(ApiStatus.unauthorized)).toBe(true)
    // bizCode 来自信封
    expect(httpError.bizCode).toBe(BizCode.unauthorized)
    expect(httpError.isBiz(BizCode.unauthorized)).toBe(true)

    // 关键:交叉比较必须为假 —— 这正是历史 bug 的形态
    expect(httpError.isHttpStatus(BizCode.unauthorized as never)).toBe(false)
    expect(httpError.isBiz(ApiStatus.unauthorized as never)).toBe(false)
  })

  it('HTTP 403 且业务码 10002:两值不同却都可判定', async () => {
    const adapter = respondWith(403, { code: 10002, msg: '无操作权限', data: null })
    const error = (await api
      .get({ url: '/x', adapter } as never)
      .catch((e: unknown) => e)) as HttpError

    expect(error.isHttpStatus(ApiStatus.forbidden)).toBe(true)
    expect(error.isBiz(BizCode.forbidden)).toBe(true)
    expect(error.code).not.toBe(error.bizCode)
  })

  it('HTTP 500 但信封缺失:code 有值,bizCode 缺席,isBiz 返回 false', async () => {
    const adapter = respondWith(500, {})
    const error = (await api
      .get({ url: '/x', adapter } as never)
      .catch((e: unknown) => e)) as HttpError

    expect(error.isHttpStatus(ApiStatus.internalServerError)).toBe(true)
    // 信封缺失时不能误判为任何一个业务码
    expect(error.bizCode).toBeUndefined()
    expect(error.isBiz(BizCode.internal)).toBe(false)
  })
})
