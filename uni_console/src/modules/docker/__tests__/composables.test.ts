/**
 * 指令通道 composable 的测试：受理分类、轮询终态、成功后重拉、行级 pending。
 *
 * 这一层是二期所有写页面的公共依赖（2B 计划 D3），它的行为错了会让每个页面各错一遍，
 * 所以这里把「成功/失败/超时/受理被拒/轮询失败」五种终态与「重拉时机」都钉住。
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { HttpError } from '@/utils/http/error'

const mocks = vi.hoisted(() => ({ send: vi.fn(), result: vi.fn() }))
vi.mock('../api', () => ({
  sendDockerCmd: mocks.send,
  fetchDockerCmdResult: mocks.result
}))

import { classifyAcceptError, runErrorMessage, useDockerCmds } from '../composables/useDockerCmds'

// 每个用例重置共享 mock：漏了会让「不该被调用」的断言读到上一个用例的调用记录（假失败）。
beforeEach(() => {
  mocks.send.mockReset()
  mocks.result.mockReset()
})

const flush = () => new Promise<void>((r) => setTimeout(r, 0))

function makeCmds(overrides: Partial<Parameters<typeof useDockerCmds>[0]> = {}) {
  return useDockerCmds({
    hostId: () => 'h1',
    sleep: async () => {},
    maxPollAttempts: 2,
    settleMs: 0,
    ...overrides
  })
}

describe('classifyAcceptError · 受理期错误分类', () => {
  it.each([
    [403, 'forbidden'],
    [409, 'conflict'],
    [400, 'invalid'],
    [500, 'unavailable'],
    [503, 'unavailable']
  ])('HTTP %i → %s', (code, kind) => {
    const f = classifyAcceptError(new HttpError('服务端结论句', code))
    expect(f.kind).toBe(kind)
    expect(f.status).toBe(code)
    // 服务端的结论句优先：它比前端编的准
    expect(f.message).toBe('服务端结论句')
  })

  it('非 HttpError（网络异常）→ unknown，且不抛错', () => {
    const f = classifyAcceptError(new Error(''))
    expect(f).toMatchObject({ kind: 'unknown', status: undefined })
    expect(f.message.length).toBeGreaterThan(0)
  })

  it('缺 msg 时按 kind 回退到可读结论句', () => {
    const f = classifyAcceptError(new HttpError('', 409))
    expect(f.message).toContain('在执行')
  })
})

describe('useDockerCmds · 五种终态', () => {
  it('成功的完整链路：受理 → 轮询 → 立即重拉 + 落定重拉', async () => {
    mocks.send.mockResolvedValue({ ref: 'r1' })
    mocks.result.mockResolvedValue({ status: 'succeeded', detail: '容器已启动' })
    const refresh = vi.fn()
    const cmds = makeCmds({ refresh })

    const res = await cmds.run({ action: 'container:start', target: 'mysql' })
    expect(res).toMatchObject({ ok: true, outcome: 'succeeded', detail: '容器已启动' })
    expect(refresh).toHaveBeenCalledTimes(1) // 立即重拉
    await flush()
    expect(refresh).toHaveBeenCalledTimes(2) // 落定重拉（settleMs=0）
  })

  it('agent 结论为失败时不重拉（列表不该因为一次失败的操作跳动）', async () => {
    mocks.send.mockResolvedValue({ ref: 'r1' })
    mocks.result.mockResolvedValue({ status: 'failed', error: '容器受保护，需要强制操作' })
    const refresh = vi.fn()
    const cmds = makeCmds({ refresh })

    const res = await cmds.run({ action: 'container:remove', target: 'mysql' })
    expect(res).toMatchObject({ ok: false, outcome: 'failed', error: '容器受保护，需要强制操作' })
    await flush()
    expect(refresh).not.toHaveBeenCalled()
  })

  it('image:save 第一段的 alreadyExists 原样透传（页面据此弹第二段）', async () => {
    mocks.send.mockResolvedValue({ ref: 'r1' })
    mocks.result.mockResolvedValue({ status: 'failed', error: '产物文件已存在', alreadyExists: true })
    const res = await makeCmds().run({
      action: 'image:save',
      target: 'uni-center-core:latest',
      options: { filename: 'core.tar' }
    })
    expect(res.alreadyExists).toBe(true)
  })

  it('受理被拒：分类进 accept，且不进入轮询', async () => {
    mocks.send.mockRejectedValue(new HttpError('没有执行该操作的权限', 403))
    const res = await makeCmds().run({ action: 'image:prune' })
    expect(res).toMatchObject({ ok: false, outcome: 'not-accepted' })
    expect(res.accept).toMatchObject({ kind: 'forbidden', status: 403 })
    expect(mocks.result).not.toHaveBeenCalled()
  })

  it('轮询超时：到上限即停，给可读结论句', async () => {
    mocks.send.mockResolvedValue({ ref: 'r1' })
    mocks.result.mockResolvedValue({ status: 'pending' })
    const res = await makeCmds().run({ action: 'compose:up', target: 'uni-center' })
    expect(res).toMatchObject({ ok: false, outcome: 'timeout' })
    expect(res.error).toContain('超时')
  })

  it('轮询请求本身失败 → poll-error（与「指令失败」区分）', async () => {
    mocks.send.mockResolvedValue({ ref: 'r1' })
    mocks.result.mockRejectedValue(new HttpError('记录已过期', 404))
    const res = await makeCmds().run({ action: 'container:stop', target: 'nginx' })
    expect(res).toMatchObject({ ok: false, outcome: 'poll-error' })
    expect(res.error).toBe('记录已过期')
  })
})

describe('useDockerCmds · 行级 pending', () => {
  it('run 期间 pendingId = 该行键，结束后清空（防重复提交）', async () => {
    let release: (v: unknown) => void = () => {}
    mocks.send.mockImplementation(
      () =>
        new Promise((r) => {
          release = r
        })
    )
    const cmds = makeCmds()
    const p = cmds.run({ action: 'container:restart', target: 'redis' })
    expect(cmds.pendingId.value).toBe('redis')
    expect(cmds.busy.value).toBe(true)

    release({ ref: 'r1' })
    mocks.result.mockResolvedValue({ status: 'succeeded' })
    await p
    expect(cmds.pendingId.value).toBeNull()
    expect(cmds.busy.value).toBe(false)
  })

  it('无 target 的动作以 action 作为行键（如 image:prune）', async () => {
    let release: (v: unknown) => void = () => {}
    mocks.send.mockImplementation(
      () =>
        new Promise((r) => {
          release = r
        })
    )
    const cmds = makeCmds()
    const p = cmds.run({ action: 'image:prune' })
    expect(cmds.pendingId.value).toBe('image:prune')
    release({ ref: 'r1' })
    mocks.result.mockResolvedValue({ status: 'succeeded' })
    await p
  })
})

describe('runErrorMessage · 展示口径', () => {
  it('结果的 error 优先，其次受理分类，最后回退句', () => {
    expect(
      runErrorMessage(
        { ok: false, outcome: 'failed', status: 'failed', error: 'agent 的结论句' },
        '操作失败'
      )
    ).toBe('agent 的结论句')
    expect(
      runErrorMessage(
        {
          ok: false,
          outcome: 'not-accepted',
          status: '',
          accept: { kind: 'conflict', message: '该目标上已有同一条操作在执行' }
        },
        '操作失败'
      )
    ).toBe('该目标上已有同一条操作在执行')
    expect(runErrorMessage({ ok: false, outcome: 'timeout', status: 'timeout' }, '操作失败')).toBe(
      '操作失败'
    )
  })
})