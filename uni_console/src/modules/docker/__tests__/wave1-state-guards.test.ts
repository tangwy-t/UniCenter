/**
 * Wave 1 状态/失败态的源码守卫（本仓库测试环境是 node，组件不挂载 —— 只读源码断言）。
 *
 * 坐实的两处缺陷（当波落守卫）：
 *   ① container-stats：未运行时组件从不发起 stats 流，phase 停在初值 `connecting`，
 *      旧模板先判 phase ⇒ 屏上同时出现「正在连接…」与「容器未运行」，自相矛盾。
 *   ② pty-terminal：失败浮层只有一行结论 + 一个「重试」，无因（errorText）无出口
 *      （启动容器/查看日志）。
 */
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const ROOT = new URL('..', import.meta.url).pathname

function read(rel: string): string {
  return readFileSync(`${ROOT}${rel}`, 'utf8')
}

describe('container-stats 未运行时不出现「正在连接…」（Wave 1 守卫）', () => {
  const src = read('components/container-stats.vue')

  it('未运行分支排在 connecting 之前（v-if="!running"）', () => {
    expect(src).toContain('v-if="!running"')
    expect(src).toContain('容器未运行')
    // 按**属性串**取位（注释里也提到「正在连接…」，裸子串会误命中注释）。
    const notRunningAt = src.indexOf('v-if="!running"')
    const connectingAt = src.indexOf('v-else-if="phase === \'connecting\'"')
    expect(notRunningAt).toBeGreaterThanOrEqual(0)
    expect(connectingAt).toBeGreaterThan(notRunningAt)
  })

  it('「正在连接…」是 v-else-if（不再是与 phase 无关的裸 v-if）', () => {
    // 裸 v-if 会在未运行时也渲染 —— 这正是矛盾态的根因。
    expect(src).not.toContain('v-if="phase === \'connecting\'"')
    expect(src).toContain('v-else-if="phase === \'connecting\'"')
  })

  it('error 分支的出口齐备（样式之一的正面范式，未被本波改动）', () => {
    expect(src).toContain('v-else-if="phase === \'error\'"')
    expect(src).toContain('restart')
  })
})

describe('pty-terminal 失败态补因与出口（Wave 1 守卫）', () => {
  const src = read('components/pty-terminal.vue')

  it('失败浮层给出「原因」行（用已有的 errorText）', () => {
    expect(src).toContain('failureCause')
    expect(src).toContain('{{ failureCause }}')
  })

  it('出口复用宿主页动作（emit start / open-logs），重试降为次要档（text）', () => {
    expect(src).toContain("emit('start')")
    expect(src).toContain("emit('open-logs')")
    // 「重试」是 text 档（次要），不再是唯一的默认档按钮。
    expect(src).toMatch(/text[^>]*@click="retry"/)
  })

  it('失败态收窄黑幕（is-failed → screen 改 min-height）', () => {
    expect(src).toContain("'is-failed': failed")
    expect(src).toMatch(/&\.is-failed\s*\{[\s\S]*?min-height/)
  })

  it('宿主页把两个出口接上（@start / @open-logs）', () => {
    const detail = read('views/container-detail/index.vue')
    expect(detail).toContain('@start="onToggle"')
    expect(detail).toContain('@open-logs="onTabChange(\'logs\')"')
    expect(detail).toContain(':running="isRunning"')
  })
})
