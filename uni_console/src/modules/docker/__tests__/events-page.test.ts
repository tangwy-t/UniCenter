// @vitest-environment jsdom
/**
 * 事件流详版页（views/events.vue）的渲染与数据编排钉子：
 *   - 加载序（先历史、后实时）与按去重键的跨来源合并（回放重发不重行）；
 *   - 列的事实（绝对 + 相对时刻 / 类型徽标 / 中文 + 原始动作码 / 对象 / 主机 / 退出码染色）；
 *   - 服务端过滤（主机/类型/关键字透传）＋ 实时行的客户端过滤（流是全量的）；
 *   - 实时跟随 / 暂停（冻结列表 + 计数 + 恢复合并）；
 *   - 游标翻页与账目句（已加载 N 条 · 窗口共 M 条）；
 *   - 四态（首拉失败/空态/静默刷新失败）与致命失败（401）停机；
 *   - keep-alive：失活断流、激活重连并补拉历史。
 *
 * 桩掉 '../api'（网络）与 Art* 全局组件（unplugin 自动注册在测试环境不存在）；
 * El* 用真件（radio 切换与空态文案都要真渲染）。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, nextTick, ref, KeepAlive, type Component } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'

const api = vi.hoisted(() => ({
  fetchDockerEventHistory: vi.fn(),
  fetchDockerHosts: vi.fn(),
  openDockerEventsStream: vi.fn()
}))
vi.mock('../api', () => ({ ...api, default: undefined }))

import EventsPage from '../views/events.vue'
import type { DockerEventHistoryItem } from '../api'
import { eventClockTime } from '../utils/events'

/** 固定「现在」（秒）：与假定时器同基准，相对时间文案可精确断言。 */
const BASE_SEC = 1_790_600_000

/* ── 可控的假 NDJSON 流（与 events-feed.test.ts 同款：reader 队列 + abort 收尾） ── */
interface FakeStream {
  response: { ok: boolean; status: number; body: { getReader: () => unknown } }
  push: (text: string) => void
  end: () => void
}

const encoder = new TextEncoder()
const signals: AbortSignal[] = []
const streams: FakeStream[] = []

function makeFakeStream(overrides: { ok?: boolean; status?: number } = {}): FakeStream {
  const queue: Uint8Array[] = []
  const waiters: Array<(r: { done: boolean; value?: Uint8Array }) => void> = []
  let closed = false
  const stream: FakeStream = {
    response: {
      ok: overrides.ok ?? true,
      status: overrides.status ?? 200,
      body: {
        getReader: () => ({
          read: () => {
            if (queue.length > 0) return Promise.resolve({ done: false, value: queue.shift() })
            if (closed) return Promise.resolve({ done: true })
            return new Promise((resolve) => waiters.push(resolve))
          }
        })
      }
    },
    push: (text: string) => {
      queue.push(encoder.encode(text))
      const w = waiters.shift()
      if (w) w({ done: false, value: queue.shift() })
    },
    end: () => {
      closed = true
      for (const w of waiters.splice(0)) w({ done: true })
    }
  }
  return stream
}

/** 一条历史 DTO 行（形状对齐 core 的 DockerEventHistoryItem）。 */
function historyItem(overrides: Partial<DockerEventHistoryItem> = {}): DockerEventHistoryItem {
  return {
    hostId: '1',
    hostname: 'bogon',
    t: BASE_SEC * 1000 - 60_000,
    type: 'container',
    action: 'start',
    actorName: 'uni-center-core',
    actorId: 'sha256:abcdef123456',
    ...overrides
  }
}

/** 历史响应（信封：items/total/nextCursor）。 */
function historyResp(items: DockerEventHistoryItem[], total = items.length, nextCursor = '') {
  return { items, total, ...(nextCursor ? { nextCursor } : {}) }
}

/** 实时流行（形状对齐 core 的 eventNDJSONLine）。 */
function evLine(overrides: Record<string, unknown> = {}): string {
  return JSON.stringify({
    host_id: 1,
    hostname: 'bogon',
    t: BASE_SEC * 1000 - 30_000,
    type: 'container',
    action: 'die',
    actor_name: 'web',
    actor_id: 'sha256:abcdef123456',
    ...overrides
  })
}

/** Art* 全局组件替身：保留 slot，页面内容仍会被渲染。 */
const passthrough = (name: string) =>
  defineComponent({
    name,
    setup:
      (_, { slots }) =>
      () =>
        h('div', { 'data-stub': name }, [slots.default?.()])
  })

const mounted: VueWrapper[] = []

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(BASE_SEC * 1000)
  vi.resetAllMocks()
  signals.length = 0
  streams.length = 0
  api.fetchDockerHosts.mockResolvedValue({ list: [], snapshotInterval: 30 })
  api.fetchDockerEventHistory.mockResolvedValue(historyResp([]))
  api.openDockerEventsStream.mockImplementation((signal: AbortSignal) => {
    signals.push(signal)
    const s = makeFakeStream()
    signal.addEventListener('abort', () => s.end())
    streams.push(s)
    return Promise.resolve(s.response)
  })
})

afterEach(() => {
  for (const w of mounted.splice(0)) w.unmount()
  vi.useRealTimers()
})

/** 微任务 + 渲染冲刷（假定时器下也成立：微任务与 nextTick 不被接管）。 */
async function flush(rounds = 14) {
  for (let i = 0; i < rounds; i++) {
    await Promise.resolve()
    await nextTick()
  }
}

async function mountEvents() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/', component: { template: '<div />' } }]
  })
  await router.push('/')
  await router.isReady()
  const w = mount(EventsPage, {
    global: {
      plugins: [router],
      stubs: {
        ArtButtonTable: passthrough('ArtButtonTable'),
        ArtSvgIcon: passthrough('ArtSvgIcon')
      }
    }
  })
  mounted.push(w as VueWrapper)
  await flush()
  return w as VueWrapper
}

/** keep-alive 宿主：v-if 切换触发 deactivated/activated（不是卸载）。 */
function mountInKeepAlive(child: Component) {
  let toggle = () => {}
  const host = defineComponent({
    setup() {
      const show = ref(true)
      toggle = () => (show.value = !show.value)
      return () => h(KeepAlive, null, () => (show.value ? h(child) : null))
    }
  })
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/', component: { template: '<div />' } }]
  })
  const wrapper = mount(host, {
    global: {
      plugins: [router],
      stubs: {
        ArtButtonTable: passthrough('ArtButtonTable'),
        ArtSvgIcon: passthrough('ArtSvgIcon')
      }
    }
  })
  mounted.push(wrapper)
  return { wrapper, toggle }
}

/** 点 hero 的刷新入口（ArtButtonTable 的桩：data-stub 标记 + title）。 */
async function clickRefresh(w: VueWrapper) {
  const btn = w
    .findAll('[data-stub="ArtButtonTable"]')
    .find((b) => b.attributes('title') === '刷新')
  expect(btn, '应存在刷新入口').toBeTruthy()
  await btn!.trigger('click')
  await flush()
}

/** 点一档类型筛选（ElRadioButton 真件：置 radio 并触发 change）。 */
async function clickType(w: VueWrapper, label: string) {
  const btn = w.findAll('.el-radio-button').find((b) => b.text() === label)
  expect(btn, `类型筛选「${label}」应存在`).toBeTruthy()
  await btn!.find('input').setValue()
  await flush()
}

describe('事件流详版页：加载序与列的事实', () => {
  it('挂载：先拉历史、再接实时流；历史行按列渲染（绝对+相对 / 类型 / 动作 / 对象 / 主机）', async () => {
    api.fetchDockerEventHistory.mockResolvedValue(
      historyResp([historyItem({ t: BASE_SEC * 1000 - 120_000, action: 'start' })])
    )
    const w = await mountEvents()

    // 加载序：历史先于流（同一次抓取完成后才 connect）。
    expect(api.fetchDockerEventHistory).toHaveBeenCalledTimes(1)
    expect(api.openDockerEventsStream).toHaveBeenCalledTimes(1)
    expect(api.fetchDockerEventHistory.mock.invocationCallOrder[0]).toBeLessThan(
      api.openDockerEventsStream.mock.invocationCallOrder[0]
    )

    // 绝对时刻与纯函数同源（本地时区 YYYY-MM-DD HH:mm:ss），相对时刻按毫秒戳除千。
    expect(w.find('.ev-time__abs').text()).toBe(eventClockTime(BASE_SEC * 1000 - 120_000))
    expect(w.find('.ev-time__rel').text()).toBe('2 分钟前')
    expect(w.find('.ev-type').text()).toContain('容器')
    expect(w.find('.ev-action').text()).toBe('启动')
    expect(w.find('.ev-raw').text()).toBe('start')
    expect(w.find('.ev-actor').text()).toBe('uni-center-core')
    expect(w.find('.ev-host').text()).toBe('bogon')
    // 非 die 事件（无退出码）：显示「—」而不是猜一个 0。
    expect(w.find('.ev-exit').text()).toBe('—')
  })

  it('退出码：≠0 红染、0 中性（die 系的事实，三值语义的显示半边）', async () => {
    api.fetchDockerEventHistory.mockResolvedValue(
      historyResp([
        historyItem({ t: BASE_SEC * 1000 - 3000, action: 'die', exitCode: 137 }),
        historyItem({ t: BASE_SEC * 1000 - 2000, action: 'die', exitCode: 0 }),
        historyItem({ t: BASE_SEC * 1000 - 1000, action: 'die' })
      ])
    )
    const w = await mountEvents()
    // t 降序：最新（无退出码）→ exit 0 → exit 137
    const exits = w.findAll('.ev-exit')
    expect(exits.map((n) => n.text())).toEqual(['—', 'exit 0', 'exit 137'])
    expect(exits[2]!.classes()).toContain('is-danger')
    expect(exits[1]!.classes()).toContain('is-neutral')
    expect(exits[0]!.classes()).toContain('is-none')
  })

  it('排序：t 降序（最新在前，与面板的升序窗口刻意分家）', async () => {
    api.fetchDockerEventHistory.mockResolvedValue(
      historyResp([
        historyItem({ t: BASE_SEC * 1000 - 3000, action: 'start' }),
        historyItem({ t: BASE_SEC * 1000 - 1000, action: 'die' }),
        historyItem({ t: BASE_SEC * 1000 - 2000, action: 'stop' })
      ])
    )
    const w = await mountEvents()
    expect(w.findAll('.ev-raw').map((n) => n.text())).toEqual(['die', 'stop', 'start'])
  })

  it('实时合并：流的新事件并入同一时间轴；回放重发已 seed 的行被内容键去重', async () => {
    api.fetchDockerEventHistory.mockResolvedValue(
      historyResp([historyItem({ t: BASE_SEC * 1000 - 60_000, action: 'start' })])
    )
    const w = await mountEvents()
    // 回放重发同一条（同主机/同毫秒/同类型/同动作/同主体）+ 一条真新事件。
    streams[0].push(
      [
        JSON.stringify({
          host_id: 1,
          hostname: 'bogon',
          t: BASE_SEC * 1000 - 60_000,
          type: 'container',
          action: 'start',
          actor_name: 'uni-center-core',
          actor_id: 'sha256:abcdef123456'
        }),
        evLine({ t: BASE_SEC * 1000 - 5_000, action: 'die' })
      ].join('\n') + '\n'
    )
    await flush()
    expect(w.findAll('.ev-raw').map((n) => n.text())).toEqual(['die', 'start'])
  })

  it('雪花 id（> 2^53，字符串形态）：历史与实时同一条事件只出现一次（真栈双份的回归钉）', async () => {
    const big = '2105604795992641536'
    api.fetchDockerEventHistory.mockResolvedValue(
      historyResp([historyItem({ hostId: big, t: BASE_SEC * 1000 - 60_000, action: 'die' })])
    )
    const w = await mountEvents()
    // 流回放重发同一条（core 的流行用字符串形态的 host_id）。
    streams[0].push(
      JSON.stringify({
        host_id: big,
        hostname: 'bogon',
        t: BASE_SEC * 1000 - 60_000,
        type: 'container',
        action: 'die',
        actor_name: 'uni-center-core',
        actor_id: 'sha256:abcdef123456'
      }) + '\n'
    )
    await flush()
    expect(w.findAll('.ev-raw')).toHaveLength(1)
    expect(w.find('.ev-more__hint').text()).toContain('已加载 1 条')
    // 归属兜底（hostname 缺失时）打出的 id 也必须是精确的雪花值。
    expect(w.find('.ev-host').text()).toBe('bogon')
  })

  it('首帧 sentinel 被跳过（不计数、不渲染）', async () => {
    api.fetchDockerEventHistory.mockResolvedValue(
      historyResp([historyItem({ t: BASE_SEC * 1000 - 60_000 })])
    )
    const w = await mountEvents()
    streams[0].push('{"kind":"opened"}\n')
    await flush()
    expect(w.findAll('.ev-raw')).toHaveLength(1)
    expect(w.find('.ev-more__hint').text()).toContain('已加载 1 条 · 窗口共 1 条')
  })
})

describe('事件流详版页：过滤（服务端 + 实时行客户端半边）', () => {
  it('类型筛选：重拉历史（参数透传）并重建列表', async () => {
    api.fetchDockerEventHistory.mockResolvedValue(historyResp([historyItem({ action: 'start' })]))
    const w = await mountEvents()
    expect(api.fetchDockerEventHistory).toHaveBeenLastCalledWith({})

    api.fetchDockerEventHistory.mockResolvedValue(
      historyResp([historyItem({ type: 'image', action: 'pull' })])
    )
    await clickType(w, '镜像')
    expect(api.fetchDockerEventHistory).toHaveBeenLastCalledWith({ type: 'image' })
    expect(w.findAll('.ev-raw').map((n) => n.text())).toEqual(['pull'])
  })

  it('实时行按当前过滤在客户端拦下（流是全量的：筛了就得自己拦）', async () => {
    api.fetchDockerEventHistory.mockResolvedValue(historyResp([]))
    const w = await mountEvents()
    await clickType(w, '镜像')
    // 重拉历史（同一筛选）：种子里有一条镜像事件。
    api.fetchDockerEventHistory.mockResolvedValue(
      historyResp([historyItem({ type: 'image', action: 'pull', t: BASE_SEC * 1000 - 60_000 })])
    )
    await clickRefresh(w)
    // 另一条 container 的实时事件不该进列表（筛的是镜像）。
    streams[0].push(evLine({ type: 'container', action: 'die', t: BASE_SEC * 1000 - 5_000 }) + '\n')
    streams[0].push(evLine({ type: 'image', action: 'push', t: BASE_SEC * 1000 - 4_000 }) + '\n')
    await flush()
    expect(w.findAll('.ev-raw').map((n) => n.text())).toEqual(['push', 'pull'])
  })

  it('空态话术：无筛选说「窗口内没有事件」，有筛选说「没有匹配的事件」', async () => {
    api.fetchDockerEventHistory.mockResolvedValue(historyResp([]))
    const w = await mountEvents()
    expect(w.find('.el-empty__description').text()).toBe('窗口内没有事件')

    await clickType(w, '容器')
    expect(w.find('.el-empty__description').text()).toBe('没有匹配的事件')
  })
})

describe('事件流详版页：实时跟随 / 暂停', () => {
  it('暂停冻结列表（新事件只计数），继续跟随一次并回', async () => {
    api.fetchDockerEventHistory.mockResolvedValue(
      historyResp([historyItem({ t: BASE_SEC * 1000 - 60_000, action: 'start' })])
    )
    const w = await mountEvents()
    // 点「暂停」（hero 里的跟随开关）。
    const follow = w.findAll('button').find((b) => b.text() === '暂停')
    expect(follow, '应存在暂停按钮').toBeTruthy()
    await follow!.trigger('click')
    await flush()

    streams[0].push(evLine({ action: 'die', t: BASE_SEC * 1000 - 3_000 }) + '\n')
    await flush()
    // 列表冻结：die 不进列，但计数说出口（且窗口共 N 条随实时增长）。
    expect(w.findAll('.ev-raw').map((n) => n.text())).toEqual(['start'])
    expect(w.text()).toContain('已暂停 · 新事件 1 条')

    // 继续跟随：暂停期间的 1 条一次并回。
    const resume = w.findAll('button').find((b) => b.text().startsWith('继续跟随'))
    expect(resume, '应存在继续跟随按钮').toBeTruthy()
    await resume!.trigger('click')
    await flush()
    expect(w.findAll('.ev-raw').map((n) => n.text())).toEqual(['die', 'start'])
    expect(w.find('.ev-paused').exists()).toBe(false)
  })
})

describe('事件流详版页：游标翻页与账目', () => {
  it('翻页按游标追加更早的一页；账目句并列「已加载 N 条 · 窗口共 M 条」', async () => {
    api.fetchDockerEventHistory.mockResolvedValue(
      historyResp(
        [
          historyItem({ t: BASE_SEC * 1000 - 1_000, action: 'die' }),
          historyItem({ t: BASE_SEC * 1000 - 2_000, action: 'stop' })
        ],
        3,
        '1790600000000:2'
      )
    )
    const w = await mountEvents()
    expect(w.find('.ev-more__hint').text()).toContain('已加载 2 条 · 窗口共 3 条')

    api.fetchDockerEventHistory.mockResolvedValue(
      historyResp([historyItem({ t: BASE_SEC * 1000 - 5_000, action: 'create' })], 3, '')
    )
    const more = w.findAll('button').find((b) => b.text() === '加载更早')
    expect(more, '有下一页游标时应给「加载更早」').toBeTruthy()
    await more!.trigger('click')
    await flush()

    expect(api.fetchDockerEventHistory).toHaveBeenLastCalledWith({ cursor: '1790600000000:2' })
    expect(w.findAll('.ev-raw').map((n) => n.text())).toEqual(['die', 'stop', 'create'])
    expect(w.find('.ev-more__hint').text()).toContain('已加载 3 条 · 窗口共 3 条')
    // 游标走完：翻页按钮消失（不给假动作）。
    expect(w.findAll('button').find((b) => b.text() === '加载更早')).toBeUndefined()
  })
})

describe('事件流详版页：四态与致命失败', () => {
  it('首拉失败：错误态 + 重试；重试成功后回到列表', async () => {
    api.fetchDockerEventHistory.mockRejectedValueOnce(new Error('boom'))
    const w = await mountEvents()
    expect(w.find('.ev-state').text()).toContain('事件历史获取失败')

    api.fetchDockerEventHistory.mockResolvedValue(historyResp([historyItem({ action: 'start' })]))
    const retry = w.findAll('button').find((b) => b.text() === '重试')
    expect(retry).toBeTruthy()
    await retry!.trigger('click')
    await flush()
    expect(w.findAll('.ev-raw').map((n) => n.text())).toEqual(['start'])
  })

  it('静默刷新失败：保留旧列表并标注（不整页切错误屏）', async () => {
    api.fetchDockerEventHistory.mockResolvedValue(historyResp([historyItem({ action: 'start' })]))
    const w = await mountEvents()
    api.fetchDockerEventHistory.mockRejectedValueOnce(new Error('boom'))
    await clickRefresh(w)
    expect(w.find('.ev-refresh-error').text()).toContain('刷新失败')
    expect(w.findAll('.ev-raw')).toHaveLength(1) // 旧列表仍在
  })

  it('401：停机 + 结论句 + 重试按钮（不自动重连）', async () => {
    api.openDockerEventsStream.mockImplementationOnce(() =>
      Promise.resolve(makeFakeStream({ ok: false, status: 401 }).response)
    )
    const w = await mountEvents()
    expect(w.text()).toContain('登录状态已失效')
    await vi.advanceTimersByTimeAsync(60_000)
    expect(api.openDockerEventsStream).toHaveBeenCalledTimes(1)
  })
})

describe('事件流详版页：keep-alive 生命周期', () => {
  it('失活即断流（abort）；激活重连并补拉历史', async () => {
    api.fetchDockerEventHistory.mockResolvedValue(historyResp([historyItem({ action: 'start' })]))
    const { wrapper, toggle } = mountInKeepAlive(EventsPage)
    await flush()
    expect(api.openDockerEventsStream).toHaveBeenCalledTimes(1)
    expect(signals[0]!.aborted).toBe(false)

    toggle() // 失活：断流
    await flush()
    expect(signals[0]!.aborted).toBe(true)

    toggle() // 激活：重连 + 补拉历史
    await flush()
    expect(api.openDockerEventsStream).toHaveBeenCalledTimes(2)
    expect(api.fetchDockerEventHistory.mock.calls.length).toBeGreaterThanOrEqual(2)
    expect(wrapper.text()).toContain('实时')
  })
})
