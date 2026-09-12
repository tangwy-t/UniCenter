import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { AxiosAdapter, AxiosRequestConfig } from 'axios'

// 隔离 user store:本测试只关心响应拦截器对信封的处理,而 store 的导入链
// 会经 pinia persist 触达 localStorage(node 环境无此全局)。
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

/**
 * 用自定义 adapter 替换真实网络层:直接返回后端信封,
 * 让请求真正流过 axiosInstance 的请求/响应拦截器。
 */
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

describe('响应拦截器:成功判定必须基于业务码', () => {
  beforeEach(() => {
    storeState.refreshToken = ''
    storeState.accessToken = ''
    vi.clearAllMocks()
  })

  // 核心回归用例。
  //
  // 成功信封是 HTTP 200 + 信封 code 0。若成功判定误写成
  // `code === ApiStatus.unauthorized`(HTTP 401),则 0 !== 401,
  // 每个成功响应都会被抛成错误 —— 本用例会立即失败。
  it('HTTP 200 + code 0 → resolve 并解包 data', async () => {
    const adapter = respondWith(200, { code: 0, msg: 'success', data: { id: 7 } })
    const out = await api.get<{ id: number }>({ url: '/x', adapter } as never)
    expect(out).toEqual({ id: 7 })
  })

  it('HTTP 200 + code 非 0 → reject(按信封 code 判定,不按 HTTP 状态)', async () => {
    const adapter = respondWith(200, { code: 40000, msg: '参数错误', data: null })
    await expect(api.get({ url: '/x', adapter } as never)).rejects.toBeTruthy()
  })

  it('成功返回字符串型 data 时原样透传', async () => {
    const adapter = respondWith(200, { code: 0, msg: 'success', data: 'ok' })
    const out = await api.get<string>({ url: '/x', adapter } as never)
    expect(out).toBe('ok')
  })

  it('HTTP 400 且信封 code 非 0 → reject', async () => {
    const adapter = respondWith(400, { code: 40000, msg: '参数错误', data: null })
    await expect(api.get({ url: '/x', adapter } as never)).rejects.toBeTruthy()
  })
})
