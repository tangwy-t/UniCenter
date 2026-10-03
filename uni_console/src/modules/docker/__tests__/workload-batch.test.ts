// @vitest-environment jsdom
/**
 * 批量波次（6d）的钉子：分组纯函数（groupBatchWaves/waveConclusion）、组件级的
 * 波次推进与波级反馈（跨主机选择拆波、逐波推进、行内结论句）、失败收集不回归
 * （跨全批累计、批末统一汇总、单条失败不中断 —— 语义照旧，只加了分组与反馈）。
 *
 * 指令通道（useDockerCmds）经 mock 整个 '../api' 接管：受理回 ref、结果即时终态，
 * 整批在微任务里跑完（现实里每行要轮询到终态，这里只钉编排不钉节奏）。ElMessage
 * 桩掉（批末汇总是被断言的行为本身，弹窗渲染不是）—— 其余 EP 组件用真件
 * （registry-dialog 同款手法）。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'

const api = vi.hoisted(() => ({
  sendDockerCmd: vi.fn(),
  fetchDockerCmdResult: vi.fn()
}))
vi.mock('../api', () => ({ ...api, default: undefined }))

// 批末汇总走 toast —— 桩掉 ElMessage（可断言调用与载荷），其余 EP 组件真件。
const elMessage = vi.hoisted(() => {
  const fn = vi.fn()
  return Object.assign(fn, { success: vi.fn(), warning: vi.fn(), error: vi.fn() })
})
vi.mock('element-plus', async (importOriginal) => {
  const actual = await importOriginal<typeof import('element-plus')>()
  return { ...actual, ElMessage: elMessage }
})

vi.mock('@/hooks/core/useAuth', () => ({
  useAuth: () => ({ hasAuth: () => true, hasAnyAuth: () => true })
}))

import WorkloadBatchBar from '../components/workload-batch-bar.vue'
import { groupBatchWaves, waveConclusion } from '../utils/batch'
import type { DockerWorkloadItem } from '../api'

/** 一行（跨主机表的行形态：hostId/hostname/name/protected 是批量要用到的字段）。 */
function row(overrides: Partial<DockerWorkloadItem> = {}): DockerWorkloadItem {
  return {
    id: `c-${overrides.name ?? 'x'}`,
    name: 'x',
    image: 'nginx:latest',
    state: 'running',
    cpuPercent: 0,
    memUsageMb: 10,
    memLimitMb: 0,
    netRxBytesSec: 0,
    netTxBytesSec: 0,
    protected: false,
    hostId: 'h1',
    hostname: 'bogon',
    ...overrides
  }
}

/** 混合主机选择：bogon 两行、nas 两行（勾选顺序交错 —— 分组不依赖相邻）。 */
function mixedSelection(): DockerWorkloadItem[] {
  return [
    row({ name: 'web', hostId: 'h1', hostname: 'bogon' }),
    row({ name: 'db', hostId: 'h2', hostname: 'nas' }),
    row({ name: 'cache', hostId: 'h1', hostname: 'bogon' }),
    row({ name: 'queue', hostId: 'h2', hostname: 'nas' })
  ]
}

const mounted: VueWrapper[] = []
const refresh = vi.fn()

beforeEach(() => {
  vi.stubGlobal(
    'ResizeObserver',
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
  )
  api.sendDockerCmd.mockReset()
  api.fetchDockerCmdResult.mockReset()
  elMessage.mockReset()
  elMessage.success.mockReset()
  elMessage.warning.mockReset()
  elMessage.error.mockReset()
  // 默认全成功：受理回按目标命名的 ref，结果即时 succeeded。
  api.sendDockerCmd.mockImplementation(async (_hostId: string, body: { target?: string }) => ({
    ref: body.target ?? 'r'
  }))
  api.fetchDockerCmdResult.mockResolvedValue({ status: 'succeeded' })
})

afterEach(() => {
  for (const w of mounted.splice(0)) w.unmount()
  vi.clearAllMocks()
  vi.unstubAllGlobals()
})

async function flush(rounds = 14) {
  for (let i = 0; i < rounds; i++) {
    await Promise.resolve()
    await nextTick()
  }
}

async function mountBar(selected: DockerWorkloadItem[]) {
  const w = mount(WorkloadBatchBar, {
    props: { selected, busy: false, refresh },
    global: {}
  })
  mounted.push(w)
  await flush()
  return w
}

async function clickStart(w: VueWrapper) {
  const btn = Array.from(w.findAll('button')).find((b) => (b.text() ?? '').trim() === '启动')
  expect(btn, '「启动」按钮应已渲染（canManage 门控在替身里全开）').toBeTruthy()
  btn!.element.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  await flush()
}

/* ── 纯函数：分波与波级结论 ───────────────────────────────── */

describe('groupBatchWaves · 按主机分波', () => {
  it('混合主机选择拆成多波：主机间按首次出现序、波内保持勾选行序', () => {
    const waves = groupBatchWaves(mixedSelection())
    expect(waves.map((wv) => wv.hostId)).toEqual(['h1', 'h2'])
    expect(waves[0]!.rows.map((r) => r.name)).toEqual(['web', 'cache'])
    expect(waves[1]!.rows.map((r) => r.name)).toEqual(['db', 'queue'])
    expect(waves[0]!.hostname).toBe('bogon')
    expect(waves[1]!.hostname).toBe('nas')
  })

  it('单主机选择就是一波；空选择没有波', () => {
    const one = groupBatchWaves([row({ name: 'a' }), row({ name: 'b' })])
    expect(one).toHaveLength(1)
    expect(one[0]!.rows).toHaveLength(2)
    expect(groupBatchWaves([])).toEqual([])
  })
})

describe('waveConclusion · 波级结论句（点名到目标）', () => {
  it('全成：计数 + 成功项波级列名', () => {
    const wave = groupBatchWaves(mixedSelection())[0]!
    expect(waveConclusion(wave, ['web', 'cache'])).toBe('bogon：2/2 成功（web、cache）')
  })

  it('有失败：失败项逐一可读（名 + 原因），成功项照常列名', () => {
    const wave = groupBatchWaves(mixedSelection())[0]!
    expect(waveConclusion(wave, ['web'], [{ name: 'cache', message: '容器处于运行状态' }])).toBe(
      'bogon：1/2 成功（web）；失败：cache（容器处于运行状态）'
    )
  })

  it('全失败：不给空括号，失败名单照常逐一可读', () => {
    const wave = groupBatchWaves([row({ name: 'web' }), row({ name: 'cache' })])[0]!
    expect(
      waveConclusion(
        wave,
        [],
        [
          { name: 'web', message: '受保护，未发送' },
          { name: 'cache', message: '执行超时' }
        ]
      )
    ).toBe('bogon：0/2 成功；失败：web（受保护，未发送）、cache（执行超时）')
  })

  it('主机名为空（设备已删）回退 hostId 展示', () => {
    const wave = groupBatchWaves([row({ name: 'a', hostname: '' })])[0]!
    expect(waveConclusion(wave, ['a'])).toBe('h1：1/1 成功（a）')
  })
})

/* ── 组件：波次推进 + 波级反馈 + 失败收集 ─────────────────── */

describe('workload-batch-bar · 波次推进与波级反馈', () => {
  it('跨主机批量按波推进：先做完 bogon 波再动 nas 波，每波落一行结论', async () => {
    const w = await mountBar(mixedSelection())
    await clickStart(w)

    // 四条指令全部发出（每行一条 —— 分组不改逐行派发语义）。
    expect(api.sendDockerCmd).toHaveBeenCalledTimes(4)
    // 波次推进顺序：h1 的两行先发完，h2 的两行才开始（逐波、不交错）。
    const order = api.sendDockerCmd.mock.calls.map((c) => (c[1] as { target?: string }).target)
    expect(order).toEqual(['web', 'cache', 'db', 'queue'])
    // 每条指令都发给行主机（波的主机）。
    expect(api.sendDockerCmd.mock.calls.map((c) => c[0])).toEqual(['h1', 'h1', 'h2', 'h2'])

    // 波级反馈：两行结论（「主机：n/n 成功（点名）」形态），批末成功 toast 一次。
    const waveTexts = Array.from(w.findAll('.wkl-batch__wave')).map((e) => e.text())
    expect(waveTexts).toContain('bogon：2/2 成功（web、cache）')
    expect(waveTexts).toContain('nas：2/2 成功（db、queue）')
    expect(elMessage.success).toHaveBeenCalledTimes(1)
    expect(elMessage.success.mock.calls[0]![0]).toBe('已执行 4 项')

    // 在途语义照旧：busy-change 起止各一次（页面据此禁行菜单）。
    const busyEvents = w.emitted('busy-change')
    expect(busyEvents).toEqual([[true], [false]])
    // 批量结束：节奏行退场（waveCurrent 清空）。
    expect(w.find('.wkl-batch__wave.is-current').exists()).toBe(false)
  })

  it('失败收集不回归：跨波累计、批末统一汇总、单条失败不中断后续行', async () => {
    // cache 行（bogon 波的第二行）执行失败 —— 它身后的 queue（nas 波）仍要执行。
    api.fetchDockerCmdResult.mockImplementation(async (_hostId: string, ref: string) =>
      ref === 'cache' ? { status: 'failed', error: '容器处于运行状态' } : { status: 'succeeded' }
    )
    const w = await mountBar(mixedSelection())
    await clickStart(w)

    // 不中断：四行全部尝试（失败行之后的行照发）。
    expect(api.sendDockerCmd).toHaveBeenCalledTimes(4)
    // 汇总在全部波完成后统一展示一次（组件走函数形态带 type:warning）：总数 +
    // 失败数 + 首条失败明细（既有口径）。
    expect(elMessage).toHaveBeenCalledTimes(1)
    expect(elMessage.mock.calls[0]![0]).toMatchObject({
      type: 'warning',
      duration: 5000,
      message: expect.stringContaining('已执行 3 项，1 项失败：cache')
    })
    // 波级行如实分开且点名到目标：bogon 波带失败旗（失败项含原因逐一可读），
    // nas 波不受牵连。
    const failLine = w.findAll('.wkl-batch__wave.is-failed').map((e) => e.text())
    expect(failLine).toEqual(['bogon：1/2 成功（web）；失败：cache（容器处于运行状态）'])
    expect(w.text()).toContain('nas：2/2 成功（db、queue）')
  })

  it('受保护目标不发送（计入失败）；成功的行照旧触发列表重拉', async () => {
    const sel = [
      row({ name: 'web', hostId: 'h1', hostname: 'bogon', protected: true }),
      row({ name: 'db', hostId: 'h2', hostname: 'nas' })
    ]
    const w = await mountBar(sel)
    await clickStart(w)

    // 受保护行不发指令（批量没有「强制」输入面），结论句进失败收集（函数形态带
    // type:warning 的批末汇总）。
    expect(api.sendDockerCmd).toHaveBeenCalledTimes(1)
    expect(api.sendDockerCmd.mock.calls[0]![1]).toMatchObject({ target: 'db' })
    expect(elMessage).toHaveBeenCalledTimes(1)
    expect(elMessage.mock.calls[0]![0].message).toContain('受保护')
    // 波级行点名：受保护未发送的行逐一可读（名 + 结论句），成功行照常列名。
    expect(w.text()).toContain('bogon：0/1 成功；失败：web（')
    expect(w.text()).toContain('nas：1/1 成功（db）')
    // 成功行触发重拉（useDockerCmds 的成功重拉纪律；落定重拉在 1.5s 后，微任务
    // 冲刷内不会到 —— 这里只钉立即那次）。
    expect(refresh).toHaveBeenCalledTimes(1)
  })

  it('批量删除走同一套波次（DELETE 逐字确认后才派发）', async () => {
    const w = await mountBar(mixedSelection())
    // 打开删除确认：ElDialog 默认原地渲染（append-to-body 未开），断言走 wrapper。
    const del = Array.from(w.findAll('button')).find((b) => (b.text() ?? '').trim() === '删除…')
    del!.element.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await flush()

    // 等确认输入框**真的渲染出来**（开窗是一串异步；固定轮数的 flush 在负载下会漂）。
    await vi.waitUntil(() => w.find('.wkl-batch-del__input input').exists(), { timeout: 5000 })
    const input = w.find('.wkl-batch-del__input input')
    expect(input.exists()).toBe(true)
    await input.setValue('DELETE')

    const confirm = Array.from(w.findAll('button')).find((b) => (b.text() ?? '').trim() === '删除')
    expect(confirm).toBeTruthy()
    confirm!.element.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await flush()

    // 删除批量同样分波推进（动作码与按行主机派发照旧）。
    expect(api.sendDockerCmd).toHaveBeenCalledTimes(4)
    expect(
      api.sendDockerCmd.mock.calls.every(
        (c) => (c[1] as { action: string }).action === 'container:remove'
      )
    ).toBe(true)
    expect(w.text()).toContain('bogon：2/2 成功（web、cache）')
  })
})
