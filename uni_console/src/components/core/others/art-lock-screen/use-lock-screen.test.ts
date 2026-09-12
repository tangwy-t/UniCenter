import { describe, expect, it, vi } from 'vitest'
import { useLockScreen, type LockScreenOptions } from './use-lock-screen'

function makeLock(opts: Partial<LockScreenOptions> = {}) {
  const verify = vi.fn(async () => true satisfies boolean)
  const onExceed = vi.fn()
  const state = useLockScreen({
    verify: opts.verify ?? verify,
    onExceed: opts.onExceed ?? onExceed,
    idleMs: opts.idleMs ?? 300_000,
    maxFails: opts.maxFails ?? 5
  })
  return { state, verify, onExceed }
}

describe('useLockScreen', () => {
  it('lock 进入锁定并清空密码', () => {
    const { state } = makeLock()
    state.password.value = 'x'
    state.lock()
    expect(state.locked.value).toBe(true)
    expect(state.password.value).toBe('')
  })

  it('tryUnlock 成功:解锁、失败计数清零、密码清空', async () => {
    const { state } = makeLock()
    state.lock()
    state.failCount.value = 3
    state.password.value = 'admin123'
    const ok = await state.tryUnlock()
    expect(ok).toBe(true)
    expect(state.locked.value).toBe(false)
    expect(state.failCount.value).toBe(0)
    expect(state.password.value).toBe('')
  })

  it('tryUnlock 失败:计数 +1 并清空密码', async () => {
    const { state, verify } = makeLock()
    verify.mockResolvedValue(false)
    state.lock()
    state.password.value = 'bad'
    const ok = await state.tryUnlock()
    expect(ok).toBe(false)
    expect(state.failCount.value).toBe(1)
    expect(state.password.value).toBe('')
  })

  it('连续达到 maxFails 触发一次 onExceed,其后不再触发', async () => {
    const { state, verify, onExceed } = makeLock({ maxFails: 5 })
    verify.mockResolvedValue(false)
    state.lock()
    for (let i = 0; i < 6; i++) {
      state.password.value = 'bad'
      await state.tryUnlock()
    }
    expect(onExceed).toHaveBeenCalledTimes(1)
  })

  it('autoLockCheck:超阈值锁定;已锁定不再重复触发', () => {
    const { state } = makeLock({ idleMs: 300_000 })
    const now = state.lastActivity.value
    expect(state.autoLockCheck(now + 299_999)).toBe(false)
    expect(state.autoLockCheck(now + 300_000)).toBe(true)
    expect(state.locked.value).toBe(true)
    expect(state.autoLockCheck(now + 999_999)).toBe(false)
  })

  it('touch 刷新活动时间后不触发自动锁定', () => {
    const { state } = makeLock({ idleMs: 300_000 })
    state.touch()
    expect(state.autoLockCheck(state.lastActivity.value + 299_999)).toBe(false)
  })

  it('idleMs 支持 getter:运行中调整阈值即时生效', () => {
    let idle = 300_000
    const { state } = makeLock({ idleMs: () => idle })
    const now = state.lastActivity.value
    expect(state.autoLockCheck(now + 299_999)).toBe(false)
    idle = 60_000
    expect(state.autoLockCheck(now + 120_000)).toBe(true)
    expect(state.locked.value).toBe(true)
  })

  it('idleMs<=0 关闭自动锁定:永不触发', () => {
    const { state } = makeLock({ idleMs: 0 })
    const now = state.lastActivity.value
    expect(state.autoLockCheck(now + 99_999_999)).toBe(false)
    expect(state.locked.value).toBe(false)
  })

  it('未锁定/校验中的守卫调用不改变状态', async () => {
    const { state, verify } = makeLock()
    state.password.value = 'x'
    const ok = await state.tryUnlock() // 未锁定 → 直接 false
    expect(ok).toBe(false)
    expect(verify).not.toHaveBeenCalled()
    expect(state.failCount.value).toBe(0)
  })
})
