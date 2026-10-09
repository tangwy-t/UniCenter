/**
 * 守卫：设备域「pin / 升级任务轮询」两处易用缺陷的回归测试。
 *
 * ── 为什么是源码扫描 + 纯函数两条腿 ───────────────────────────────
 *
 * 本仓库的测试环境是 node（无 jsdom / @vue/test-utils），无法挂载组件，
 * 也拿不到 ElMessageBox.confirm 的真实弹窗。故：
 *  - 「已固定状态可见性」与「进度条占比」下沉到 utils/upgrade 的纯函数，直接断言；
 *  - 「pin 下发前有确认」「每个轮询回调带 document.hidden 守卫」这类**源码级
 *    不变量**用源码扫描断言（与 copy-no-internals.test.ts、根目录 check-permissions.mjs
 *    同思路）—— 它们本质是「某段代码必须在某处出现」，正是扫描最擅长的形态。
 */
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { isDevicePinned, taskProgress } from '../utils/upgrade'

const readSrc = (rel: string): string => readFileSync(new URL(rel, import.meta.url), 'utf8')

/** 从 `openIdx`（一个括号字符的下标）起，取到与之匹配的闭合括号（含）。 */
function sliceBalanced(src: string, openIdx: number): string {
  const open = src[openIdx]
  const close = open === '(' ? ')' : '}'
  let depth = 0
  for (let i = openIdx; i < src.length; i++) {
    if (src[i] === open) depth++
    else if (src[i] === close) {
      depth--
      if (depth === 0) return src.slice(openIdx, i + 1)
    }
  }
  return src.slice(openIdx)
}

/** 取某个函数声明（`signature` 子串）的 `{…}` 函数体。 */
function bodyOf(src: string, signature: string): string {
  const idx = src.indexOf(signature)
  if (idx < 0) return ''
  const braceIdx = src.indexOf('{', idx)
  if (braceIdx < 0) return ''
  return sliceBalanced(src, braceIdx)
}

/** 抽出源码里每个 `setInterval(…)` 调用的实参块。 */
function intervalCalls(src: string): string[] {
  const out: string[] = []
  let i = src.indexOf('setInterval(')
  while (i >= 0) {
    out.push(sliceBalanced(src, i + 'setInterval'.length))
    i = src.indexOf('setInterval(', i + 1)
  }
  return out
}

describe('pin：下发前有确认（源码级不变量）', () => {
  it('detail 的 pinCurrent 先 ElMessageBox.confirm 再下发 upgradeDevice', () => {
    const body = bodyOf(readSrc('../views/detail.vue'), 'function pinCurrent')
    expect(body, 'pinCurrent 函数体未找到').not.toBe('')
    expect(body, 'pin 缺少确认弹窗').toContain('ElMessageBox.confirm')
    // 顺序：确认必须在下发之前（否则确认成了摆设）。
    expect(body.indexOf('ElMessageBox.confirm')).toBeLessThan(body.indexOf('upgradeDevice('))
  })
})

describe('升级任务页：轮询有 document.hidden 守卫（源码级不变量）', () => {
  it('列表与详情的两个 setInterval 回调都带隐藏守卫', () => {
    const calls = intervalCalls(readSrc('../views/upgrade-task.vue'))
    // 先自证非空转：至少两个定时器（列表 + 详情）。
    expect(calls.length).toBeGreaterThanOrEqual(2)
    for (const cb of calls) {
      expect(cb, `轮询回调缺少 document.hidden 守卫：${cb.slice(0, 60)}…`).toContain(
        'document.hidden'
      )
    }
  })
})

describe('已固定状态可见性（isDevicePinned）', () => {
  it('设备级目标 == 当前版本且非跟随全站 → 已固定', () => {
    expect(
      isDevicePinned({ agentVersion: '0.2.4', targetVersion: '0.2.4', targetFromGlobal: false })
    ).toBe(true)
  })

  it('跟随全站不是固定（即使目标恰等于当前版本）', () => {
    expect(
      isDevicePinned({ agentVersion: '0.2.4', targetVersion: '0.2.4', targetFromGlobal: true })
    ).toBe(false)
  })

  it('设备级目标指向其它版本（在途升级）不是固定', () => {
    expect(
      isDevicePinned({ agentVersion: '0.2.4', targetVersion: '0.3.0', targetFromGlobal: false })
    ).toBe(false)
  })

  it('无目标 / 目标为空 → 不是固定', () => {
    expect(isDevicePinned({ agentVersion: '0.2.4' })).toBe(false)
    expect(isDevicePinned({ agentVersion: '0.2.4', targetVersion: '' })).toBe(false)
    expect(isDevicePinned({})).toBe(false)
  })
})

describe('任务进度条视图（taskProgress）', () => {
  it('分母取全部桶之和，成功/失败段按占比', () => {
    const p = taskProgress({ succeeded: 6, running: 1, pending: 2, failed: 1 })
    expect(p.total).toBe(10)
    expect(p.succeeded).toBe(6)
    expect(p.failed).toBe(1)
    expect(p.succeededPercent).toBe(60)
    expect(p.failedPercent).toBe(10)
  })

  it('失败数为 0 时失败段占比为 0（页面不染 danger 段）', () => {
    const p = taskProgress({ succeeded: 3 })
    expect(p.failed).toBe(0)
    expect(p.failedPercent).toBe(0)
  })

  it('全零 / 空 → total 0 且占比为 0（不出现除零 NaN）', () => {
    const p = taskProgress({})
    expect(p.total).toBe(0)
    expect(p.succeededPercent).toBe(0)
    expect(p.failedPercent).toBe(0)
    expect(Number.isNaN(p.succeededPercent)).toBe(false)
    expect(taskProgress(null).total).toBe(0)
  })
})
