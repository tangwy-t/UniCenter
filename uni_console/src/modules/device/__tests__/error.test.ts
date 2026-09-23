import { describe, expect, it } from 'vitest'
import { classifyDeviceError } from '../utils/error'
import { ApiStatus, BizCode } from '@/utils/http/status'
import { HttpError } from '@/utils/http/error'

/** 造一个 HttpError（构造签名见 utils/http/error.ts）。 */
function httpErr(msg: string, code: number, bizCode?: number): HttpError {
  // bizCode 走 options（0 视为「无业务码」，与后端非信封响应的形态一致）
  return new HttpError(msg, code, bizCode ? { bizCode } : undefined)
}

/**
 * F-5 的核心断言：**notFound / forbidden 不得带重试按钮**。
 *
 * 「设备不存在」配一个重试按钮是纯粹的误导 —— 用户会反复点，然后怀疑系统。
 */
describe('classifyDeviceError 动作正确性', () => {
  it('设备不存在 → 不可重试，且提示回列表', () => {
    const info = classifyDeviceError(httpErr('设备不存在', ApiStatus.error, BizCode.notFound))
    expect(info.kind).toBe('notFound')
    expect(info.retryable).toBe(false)
    expect(info.hint).toContain('列表')
  })

  it('无权限 → 不可重试，且提示联系管理员', () => {
    const info = classifyDeviceError(httpErr('无权限', ApiStatus.error, BizCode.forbidden))
    expect(info.kind).toBe('forbidden')
    expect(info.retryable).toBe(false)
    expect(info.hint).toContain('管理员')
  })

  it('服务端异常 → 唯一该显示重试的类别', () => {
    const info = classifyDeviceError(httpErr('内部错误', ApiStatus.error, BizCode.internal))
    expect(info.kind).toBe('server')
    expect(info.retryable).toBe(true)
  })
})

describe('业务码优先于 HTTP 状态', () => {
  it('业务码 notFound 即使 HTTP 是 200 也判 notFound', () => {
    // 后端成功/失败都用 HTTP 200 + 信封 code，业务码才是权威
    const info = classifyDeviceError(httpErr('设备不存在', 200, BizCode.notFound))
    expect(info.kind).toBe('notFound')
  })

  it('业务码 10001（未授权）不属于五类，不应误判为 forbidden', () => {
    // 未授权业务码是 10001，永不等于 HTTP 403；若前端按 HTTP 语义硬比就会错判
    const info = classifyDeviceError(httpErr('未登录', ApiStatus.error, BizCode.unauthorized))
    expect(info.kind).not.toBe('forbidden')
    expect(info.kind).not.toBe('notFound')
  })

  it('无业务码时退回 HTTP 状态（网关层错误没有业务码）', () => {
    const info = classifyDeviceError(httpErr('Bad Gateway', ApiStatus.badGateway, 0))
    expect(info.kind).toBe('server')
    expect(info.retryable).toBe(true)
  })

  it('HTTP 403 且无业务码时判 forbidden', () => {
    const info = classifyDeviceError(httpErr('Forbidden', ApiStatus.forbidden, 0))
    expect(info.kind).toBe('forbidden')
  })
})

describe('参数错误保留后端原文', () => {
  it('下钻 range 越界的 msg 应原样展示（它可操作）', () => {
    const msg = '资源明细只保留 30 天，请把时间范围缩短到 30 天以内'
    const info = classifyDeviceError(httpErr(msg, ApiStatus.error, BizCode.badRequest))
    expect(info.kind).toBe('badRequest')
    // 这条 msg 比「请求参数不被接受」有用得多：它直接告诉用户怎么改
    expect(info.title).toBe(msg)
  })

  it('badRequest 不重试（该改条件而不是重试）', () => {
    const info = classifyDeviceError(httpErr('x', ApiStatus.error, BizCode.badRequest))
    expect(info.retryable).toBe(false)
  })
})

describe('非 HttpError 的兜底', () => {
  it('普通 Error → 可重试，且保留原文供排障', () => {
    const info = classifyDeviceError(new Error('Network Error'))
    expect(info.retryable).toBe(true)
    expect(info.raw).toBe('Network Error')
  })

  it('字符串 / null / undefined 不抛异常', () => {
    expect(() => classifyDeviceError('boom')).not.toThrow()
    expect(() => classifyDeviceError(null)).not.toThrow()
    expect(() => classifyDeviceError(undefined)).not.toThrow()
    expect(classifyDeviceError(null).kind).toBe('network')
  })

  it('每类都有非空 title（不能出现空白错误块）', () => {
    const cases = [
      httpErr('a', ApiStatus.error, BizCode.notFound),
      httpErr('b', ApiStatus.error, BizCode.forbidden),
      httpErr('c', ApiStatus.error, BizCode.badRequest),
      httpErr('d', ApiStatus.error, BizCode.internal),
      new Error('e'),
      null
    ]
    for (const c of cases) {
      expect(classifyDeviceError(c).title.length).toBeGreaterThan(0)
    }
  })
})
