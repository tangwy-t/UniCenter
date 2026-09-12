import { describe, expect, it } from 'vitest'
import { ApiStatus, BizCode } from './status'

// P2-2:HTTP 状态码与业务码是两个不相交的命名空间,混用会让比较恒为 false。
//
// 背景:后端对每个错误**同时**给出两者 —— app.Error 用 AppError.HTTPStatus
// 作 HTTP 状态、AppError.Code 作信封 code;成功路径恒为 HTTP 200 + code 0
// (app.Success)。因此:
//   业务码由 response.data.code 读取 → 与 BizCode.* 比较
//   HTTP 状态由 error.response.status 读取 → 与 ApiStatus.* 比较
//
// 本套断言的价值在于:一旦有人把两个命名空间混用(例如把成功判定写成
// `code === ApiStatus.unauthorized`),下面的"交集为空"与"取值边界"断言
// 会直接指出这是不可能成立的比较。

describe('ApiStatus / BizCode 命名空间隔离', () => {
  it('ApiStatus 不再包含业务码 success', () => {
    // success 是业务码(0),属于 BizCode;留在 ApiStatus 会诱使
    // 调用方拿 HTTP 状态去比业务码。
    expect(Object.keys(ApiStatus)).not.toContain('success')
  })

  it('BizCode.ok 是 0,与后端 apperror.CodeOK 一致', () => {
    expect(BizCode.ok).toBe(0)
  })

  it('BizCode 各值与后端 apperror 常量逐一对应', () => {
    // 若后端调整 Code* 常量,这里会失败,提示同步更新前端。
    expect(BizCode.unauthorized).toBe(10001)
    expect(BizCode.forbidden).toBe(10002)
    expect(BizCode.tokenExpired).toBe(10003)
    expect(BizCode.captchaRequired).toBe(10004)
    expect(BizCode.accountLocked).toBe(10008)
    expect(BizCode.badRequest).toBe(40000)
    expect(BizCode.notFound).toBe(40400)
    expect(BizCode.conflict).toBe(40900)
    expect(BizCode.internal).toBe(50000)
  })

  it('两个命名空间不重叠(业务码 >= 10001,HTTP 状态 <= 599)', () => {
    const httpValues = Object.values(ApiStatus).filter((v): v is number => typeof v === 'number')
    const bizValues = Object.values(BizCode).filter((v): v is number => typeof v === 'number')
    for (const h of httpValues) {
      expect(h).toBeLessThanOrEqual(599)
    }
    for (const b of bizValues) {
      if (b === 0) continue
      expect(b).toBeGreaterThanOrEqual(10001)
    }
    // 交集必须为空 —— 这正是"混用即 bug"的根因。
    const overlap = httpValues.filter((h) => bizValues.includes(h))
    expect(overlap).toEqual([])
  })

  it('平台无关的关键用例:HTTP 401 与业务码 10001 不相等', () => {
    // 历史 bug 的具体形态:成功判定被写成 `code === ApiStatus.<401 成员>`。
    // 后端未授权业务码是 10001,该比较永不成立 —— 结果是每个成功响应
    // (code 0)都会被抛成错误。此断言锁定两者不可互换。
    expect(ApiStatus.unauthorized).toBe(401)
    expect(BizCode.unauthorized).toBe(10001)
    expect(ApiStatus.unauthorized).not.toBe(BizCode.unauthorized)
  })

  it('成功判定必须用 BizCode.ok,而不是任何 HTTP 状态', () => {
    // 直接对"成功信封"(HTTP 200 + code 0)做判定,断言只有 BizCode.ok 命中。
    const successEnvelopeCode = 0
    expect(successEnvelopeCode === BizCode.ok).toBe(true)

    const httpStatuses = Object.values(ApiStatus).filter((v): v is number => typeof v === 'number')
    for (const status of httpStatuses) {
      expect(successEnvelopeCode === status).toBe(false)
    }
  })
})
