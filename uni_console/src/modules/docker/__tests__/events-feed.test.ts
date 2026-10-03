// @vitest-environment jsdom
/**
 * 活动流面板（overview-events-feed.vue）的组件级行为钉子：流消费（回放+实时+坏行）、
 * 滚动暂停与恢复、断流自动重连（含重放去重不重行）、致命失败（401）停机不重连、
 * keep-alive 失活断流/激活重连，以及总览页的集成（连接生命周期 = 页面激活期）。
 *
 * 沿用 page-render / host-state 的 vi.mock 模式（mock 整个 '../api'）—— 流端点用
 * 可控的假 NDJSON 响应（手写 reader 队列：push 喂行、end 收尾、abort 即收尾），
 * 不依赖真 ReadableStream。纯逻辑（行解析/排序/去重/文案映射）在 events.test.ts。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, nextTick, ref, KeepAlive, type Component } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'

const api = vi.hoisted(() => ({
  openDockerEventsStream: vi.fn(),
  fetchDockerOverview: vi.fn(),
  // 6b 任务中心抽屉（hero 入口）挂载但不开 —— import 面必须齐全。
  fetchDockerTasks: vi.fn(),
  openDockerPullStream: vi.fn()
}))
vi.mock('../api', () => ({ ...api }))

// overview.vue 的 hero 入口（6b）按 docker:list 门控 —— 挂载真 useAuth 需要 pinia，
// 与 page-render 同款替身绕开（门控行为的用例在 task-center.test.ts）。
vi.mock('@/hooks/core/useAuth', () => ({
  useAuth: () => ({ hasAuth: () => true, hasAnyAuth: () => true })
}))

import OverviewEventsFeed from '../components/overview-events-feed.vue'
import Overview from '../views/overview.vue'
import { eventClockTime } from '../utils/events'

/* ── 可控的假 NDJSON 流 ───────────────────────────────────────────
 * reader 队列：push 入队（有等待者直接唤醒）、end 收尾（done）、组件 abort 时
 * 同步收尾 —— 对齐真 fetch 的「abort 让 read 以非正常路径结束」行为，让失活
 * 断流的时序与生产一致。
 */
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

beforeEach(() => {
  vi.resetAllMocks()
  signals.length = 0
  streams.length = 0
  api.openDockerEventsStream.mockImplementation((signal: AbortSignal) => {
    signals.push(signal)
    const s = makeFakeStream()
    // abort 即收尾：对齐真 fetch（AbortController 断开后 read 结束，不再有数据）
    signal.addEventListener('abort', () => s.end())
    streams.push(s)
    return Promise.resolve(s.response)
  })
})

afterEach(() => {
  for (const w of mounted.splice(0)) w.unmount()
})

const mounted: VueWrapper[] = []

/** 事件行（形状对齐 core 的 eventNDJSONLine；t 用 unix 毫秒 —— agent UnixMilli 透传）。 */
function evLine(overrides: Record<string, unknown> = {}): string {
  return JSON.stringify({
    host_id: 1,
    hostname: 'bogon',
    t: Date.now() - 60_000,
    type: 'container',
    action: 'start',
    actor_name: 'uni-center-core',
    actor_id: 'sha256:abcdef123456',
    ...overrides
  })
}

/** 微任务 + 渲染冲刷（假定时器下也成立：微任务与 nextTick 不被 vi.useFakeTimers 接管）。 */
async function flush(rounds = 10) {
  for (let i = 0; i < rounds; i++) {
    await Promise.resolve()
    await nextTick()
  }
}

/** keep-alive 宿主：v-if 切换触发子组件的 deactivated/activated（不是卸载）。 */
function mountInKeepAlive(child: Component, opts: Record<string, unknown> = {}) {
  let toggle = () => {}
  const host = defineComponent({
    setup() {
      const show = ref(true)
      toggle = () => (show.value = !show.value)
      return () => h(KeepAlive, null, () => (show.value ? h(child) : null))
    }
  })
  const wrapper = mount(host, opts)
  mounted.push(wrapper)
  return { wrapper, toggle }
}

function mountFeed() {
  const wrapper = mount(OverviewEventsFeed)
  mounted.push(wrapper)
  return wrapper
}

/* ── 流消费与渲染 ─────────────────────────────────────────────── */
describe('活动流：回放 + 实时 + 坏行', () => {
  it('挂载即接入（一次调用），回放行渲染并按时间排序（跨主机乱序到达也排好）', async () => {
    const w = mountFeed()
    await flush()
    expect(api.openDockerEventsStream).toHaveBeenCalledTimes(1)
    expect(w.find('.dov-feed__status').text()).toContain('实时')

    const now = Date.now()
    streams[0].push(
      [
        // h1 先到（t 较新），h2 后到（t 较旧）—— 展示序必须按 t（旧在上）
        evLine({ host_id: 1, hostname: 'bogon', t: now - 30_000, action: 'die' }),
        evLine({ host_id: 2, hostname: 'nas', t: now - 50_000, action: 'pull', type: 'image' }),
        '{broken json',
        evLine({ host_id: 1, hostname: 'bogon', t: now - 10_000, action: 'destroy' })
      ].join('\n') + '\n'
    )
    await flush()

    const rows = w.findAll('.dov-feed__row')
    expect(rows).toHaveLength(3) // 坏行跳过，不打断流
    // 排序：最旧（pull@nas）在最上；行内含中文化 action、actor 与主机标签
    expect(rows[0].text()).toContain('拉取')
    expect(rows[0].text()).toContain('nas')
    expect(rows[1].text()).toContain('退出')
    expect(rows[2].text()).toContain('销毁')
    expect(rows[2].text()).toContain('uni-center-core')
  })

  it('实时事件继续追加，表外动作原样透传（协议扩词的兜底）', async () => {
    const w = mountFeed()
    await flush()
    streams[0].push(evLine({ t: Date.now() - 5_000 }) + '\n')
    await flush()
    streams[0].push(evLine({ t: Date.now() - 1_000, action: 'frobnicate' }) + '\n')
    await flush()

    const rows = w.findAll('.dov-feed__row')
    expect(rows).toHaveLength(2)
    expect(rows[0].text()).toContain('启动')
    expect(rows[1].text()).toContain('frobnicate') // 表外词元原样显示
  })

  it('health_status 后缀上屏为中文结论，daemon 原始短语进 title（可核对的事实）', async () => {
    const w = mountFeed()
    await flush()
    streams[0].push(
      evLine({ t: Date.now() - 3_000, action: 'health_status: unhealthy' }) + '\n'
    )
    await flush()

    const action = w.find('.dov-feed__action')
    expect(action.text()).toBe('健康检查异常')
    // 原始短语（含检查结果）不丢：悬停可取，排障引用靠它。
    expect(action.attributes('title')).toBe('health_status: unhealthy')
  })

  it('回放后也无事件：空态句与断流态区分（「舰队很安静」）', async () => {
    const w = mountFeed()
    await flush()
    expect(w.find('.dov-feed__note').text()).toBe('舰队很安静 —— 回放与实时均无事件')
  })

  it('actor 缺名字时用短 id 兜底（sha256 前缀剥掉）', async () => {
    const w = mountFeed()
    await flush()
    streams[0].push(evLine({ actor_name: '', actor_id: 'sha256:0123456789abcdef' }) + '\n')
    await flush()
    expect(w.find('.dov-feed__actor').text()).toBe('0123456789ab')
  })
})

/* ── 相对时间的 1 秒节流 ─────────────────────────────────────── */
describe('活动流：相对时间 1 秒节流刷新', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('文案随 tick 前进（3 秒前 → 4 秒前），1 秒一跳；悬停给绝对时刻（title）', async () => {
    const base = 1_790_600_000
    vi.setSystemTime(base * 1000)
    const w = mountFeed()
    await flush()
    // 真实线格式：t 是毫秒戳（agent UnixMilli）——组件须除千后再算相对时间。
    streams[0].push(evLine({ t: base * 1000 - 3_000 }) + '\n')
    await flush()
    expect(w.find('.dov-feed__time').text()).toBe('3 秒前')
    // 绝对时刻在 title（YYYY-MM-DD HH:mm:ss 本地时区），与相对时间同源同刻。
    expect(w.find('.dov-feed__time').attributes('title')).toBe(eventClockTime(base * 1000 - 3_000))

    await vi.advanceTimersByTimeAsync(1_000)
    expect(w.find('.dov-feed__time').text()).toBe('4 秒前')
    await vi.advanceTimersByTimeAsync(2_000)
    expect(w.find('.dov-feed__time').text()).toBe('6 秒前')
  })

  it('毫秒戳换算（QA 路 1 P2 的回归钉）：两分钟前的事件显示分钟档，而不是恒「刚刚」', async () => {
    const base = 1_790_600_000
    vi.setSystemTime(base * 1000)
    const w = mountFeed()
    await flush()
    streams[0].push(evLine({ t: base * 1000 - 120_000 }) + '\n')
    await flush()
    expect(w.find('.dov-feed__time').text()).toBe('2 分钟前')
  })
})

/* ── 滚动暂停与恢复 ─────────────────────────────────────────── */
describe('活动流：滚动暂停与恢复（log-viewer 同款语义）', () => {
  /** jsdom 不做布局：scrollHeight/clientHeight 用实例属性覆写（sticky/scrollTop 可设）。 */
  function armScrollRegion(w: VueWrapper, scrollHeight: number, clientHeight: number) {
    const el = w.find('.dov-feed__body').element
    Object.defineProperty(el, 'scrollHeight', { value: scrollHeight, configurable: true })
    Object.defineProperty(el, 'clientHeight', { value: clientHeight, configurable: true })
    el.scrollTop = 0
    return el
  }

  it('新事件到达自动贴底；用户上滚即暂停（阅读位置不被顶走）', async () => {
    const w = mountFeed()
    await flush()
    const el = armScrollRegion(w, 1000, 400)
    streams[0].push(evLine() + '\n')
    await flush()
    expect(el.scrollTop).toBe(1000) // 未暂停：自动贴底

    // 用户上滚看历史 → 暂停自动滚动
    el.scrollTop = 100
    el.dispatchEvent(new Event('scroll'))
    await nextTick()
    expect(w.find('.dov-feed__paused').exists()).toBe(true)
    expect(w.find('.dov-feed__paused-text').text()).toContain('已暂停')

    // 暂停期间新事件照常入窗（计数），但不顶走阅读位置
    streams[0].push(evLine({ action: 'die' }) + '\n')
    await flush()
    expect(el.scrollTop).toBe(100)
    // 等「新事件已入窗并计数」这个**事实**：从 push 到进窗是一串异步（读循环 →
    // 折叠 → 渲染），判据用事实（计数文案）而不是固定轮数的 flush。
    await vi.waitUntil(
      () => w.find('.dov-feed__paused-text').text().includes('新事件 1 条'),
      { timeout: 5000 }
    )
    let diag = ''
    if (!w.find('.dov-feed__paused-text').text().includes('新事件')) {
      diag =
        `phase=${w.find('.dov-feed__status').text()} streams=${streams.length} ` +
        `connects=${api.openDockerEventsStream.mock.calls.length} ` +
        `aborted=${String(signals[0]?.aborted)} rows=${w.findAll('.dov-feed__row').length}`
    }
    expect(w.find('.dov-feed__paused-text').text(), diag).toContain('新事件 1 条')
    expect(w.findAll('.dov-feed__row')).toHaveLength(2)
  })

  it('点「回到底部」恢复：暂停条消失、贴底、后续新事件继续跟随', async () => {
    const w = mountFeed()
    await flush()
    const el = armScrollRegion(w, 1000, 400)
    el.scrollTop = 100
    el.dispatchEvent(new Event('scroll'))
    await nextTick()
    expect(w.find('.dov-feed__paused').exists()).toBe(true)

    await w.find('.dov-feed__paused button').trigger('click')
    await flush()
    expect(w.find('.dov-feed__paused').exists()).toBe(false)
    expect(el.scrollTop).toBe(1000)

    streams[0].push(evLine({ action: 'die' }) + '\n')
    await flush()
    expect(el.scrollTop).toBe(1000) // 恢复后继续跟随
  })

  it('自己滚回底部也恢复（不必找按钮）', async () => {
    const w = mountFeed()
    await flush()
    const el = armScrollRegion(w, 1000, 400)
    el.scrollTop = 100
    el.dispatchEvent(new Event('scroll'))
    await nextTick()
    expect(w.find('.dov-feed__paused').exists()).toBe(true)

    el.scrollTop = 1000
    el.dispatchEvent(new Event('scroll'))
    await nextTick()
    expect(w.find('.dov-feed__paused').exists()).toBe(false)
  })
})

/* ── 断流重连 ───────────────────────────────────────────────── */
describe('活动流：断流自动重连', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('对端收尾 → 「已断开 · 重连中」→ 退避到点重连，重放去重不重行', async () => {
    const now = Date.now()
    const w = mountFeed()
    await flush()
    const replay =
      [evLine({ t: now - 30_000 }), evLine({ t: now - 20_000, action: 'die' })].join('\n') + '\n'
    streams[0].push(replay)
    await flush()
    expect(w.findAll('.dov-feed__row')).toHaveLength(2)

    streams[0].end() // core 重启 / 网关断开：流被对端收尾
    await flush()
    expect(w.find('.dov-feed__status').text()).toContain('已断开 · 重连中')
    expect(w.findAll('.dov-feed__row')).toHaveLength(2) // 断流不清窗口（历史还在）

    await vi.advanceTimersByTimeAsync(1_100) // 首档退避 1s
    expect(api.openDockerEventsStream).toHaveBeenCalledTimes(2)
    expect(w.find('.dov-feed__status').text()).toContain('实时')

    // 重连后回放重发同一批（重放语义）→ 去重守卫吃掉，不出现重复行
    streams[1].push(replay)
    await flush()
    expect(w.findAll('.dov-feed__row')).toHaveLength(2)
    // 之后的新事件照常入窗
    streams[1].push(evLine({ t: now, action: 'pull', type: 'image' }) + '\n')
    await flush()
    expect(w.findAll('.dov-feed__row')).toHaveLength(3)
  })

  it('打开失败走同一条重连路（非 401/403 的失败不放弃）', async () => {
    api.openDockerEventsStream.mockImplementationOnce(() =>
      Promise.resolve(makeFakeStream({ ok: false, status: 502 }).response)
    )
    const w = mountFeed()
    await flush()
    expect(w.find('.dov-feed__status').text()).toContain('已断开 · 重连中')

    await vi.advanceTimersByTimeAsync(1_100)
    expect(api.openDockerEventsStream).toHaveBeenCalledTimes(2)
    expect(w.find('.dov-feed__status').text()).toContain('实时')
  })
})

/* ── 致命失败：停机不重连 ───────────────────────────────────── */
describe('活动流：致命失败（401/403）停机 + 手动重试', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('401 → 已停止 + 结论句 + 重试按钮；不自动重连，手动重试可恢复', async () => {
    api.openDockerEventsStream.mockImplementationOnce(() =>
      Promise.resolve(makeFakeStream({ ok: false, status: 401 }).response)
    )
    const w = mountFeed()
    await flush()
    expect(w.find('.dov-feed__status').text()).toContain('已停止')
    expect(w.find('.dov-feed__status').text()).toContain('登录状态已失效')

    await vi.advanceTimersByTimeAsync(60_000) // 停机不重连：一分钟后也没有第二次接入
    expect(api.openDockerEventsStream).toHaveBeenCalledTimes(1)

    await w.find('.dov-feed__retry').trigger('click')
    await flush()
    expect(api.openDockerEventsStream).toHaveBeenCalledTimes(2)
    expect(w.find('.dov-feed__status').text()).toContain('实时')
  })
})

/* ── keep-alive 失活断流 / 激活重连 ─────────────────────────── */
describe('活动流：连接生命周期 = 页面激活期', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('失活即断流（abort、重连计时器取消）；激活重连', async () => {
    const { wrapper, toggle } = mountInKeepAlive(OverviewEventsFeed)
    await flush()
    expect(api.openDockerEventsStream).toHaveBeenCalledTimes(1)
    expect(signals[0].aborted).toBe(false)

    toggle() // keep-alive 失活（worktab 切走）：不是卸载
    await flush()
    expect(signals[0].aborted).toBe(true)

    await vi.advanceTimersByTimeAsync(60_000) // 失活期间不重连（断流要彻底）
    expect(api.openDockerEventsStream).toHaveBeenCalledTimes(1)

    toggle() // 激活（切回）：重连
    await flush()
    expect(api.openDockerEventsStream).toHaveBeenCalledTimes(2)
    expect(signals[1].aborted).toBe(false)
    expect(wrapper.text()).toContain('实时')
  })
})

/* ── 总览页集成 ─────────────────────────────────────────────── */
describe('总览页集成：面板就位、生命周期随页面', () => {
  /** 最小可渲染的总览载荷（hosts 非空 → ready 态，活动流分区才会挂载）。 */
  const OVERVIEW = {
    fleet: {
      hosts: { total: 1, dockerOk: 1 },
      containers: { total: 2, running: 1, stopped: 1, protected: 0 },
      images: { total: 1, unused: 0 },
      volumes: { total: 1, unused: 0 },
      networks: { total: 1 },
      projects: { total: 1, running: 1 },
      // 6a：磁盘账目是 fleet 的必填块（0 = 该舰队没有 df 数据，页面如实显示）。
      disk: { hosts: 0, imagesTotalMb: 0, volumesTotalMb: 0, buildCacheMb: 0, imagesDanglingMb: 0 }
    },
    hosts: [
      {
        id: 'h1',
        hostname: 'bogon',
        online: true,
        dockerOk: true,
        containers: 2,
        images: 1,
        stale: false
      }
    ],
    anomalies: { total: 0, items: [] }
  }

  beforeEach(async () => {
    api.fetchDockerOverview.mockResolvedValue(OVERVIEW)
    // jsdom 里 ElTable（异常表）需要它
    vi.stubGlobal(
      'ResizeObserver',
      class {
        observe() {}
        unobserve() {}
        disconnect() {}
      }
    )
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  async function mountOverviewInKeepAlive() {
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: '/', component: { template: '<div />' } }]
    })
    // 先 push 再 isReady：初始导航由首次 push 触发（不 push 的 isReady 永远等不到）
    await router.push('/')
    await router.isReady()
    // ArtButtonTable 依赖 pinia（useUserStore），与 page-render 同款替身绕开
    return mountInKeepAlive(Overview, {
      global: {
        plugins: [router],
        stubs: {
          ArtButtonTable: defineComponent({
            name: 'ArtButtonTable',
            setup:
              (_, { slots }) =>
              () =>
                h('div', { 'data-stub': 'ArtButtonTable' }, slots.default?.())
          })
        }
      }
    })
  }

  it('ready 态渲染活动流分区并接入；页面失活断流、激活重连', async () => {
    const { wrapper, toggle } = await mountOverviewInKeepAlive()
    await flush(14)
    expect(wrapper.text()).toContain('活动流')
    expect(wrapper.find('.dov-feed').exists()).toBe(true)
    expect(api.openDockerEventsStream).toHaveBeenCalledTimes(1)

    streams[0].push(evLine() + '\n')
    await flush()
    expect(wrapper.findAll('.dov-feed__row')).toHaveLength(1)

    toggle() // 页面失活（worktab 缓存）：断流纪律与自动刷新同款
    await flush()
    expect(signals[0].aborted).toBe(true)

    toggle() // 回来：重连
    await flush(14)
    expect(api.openDockerEventsStream).toHaveBeenCalledTimes(2)
  })
})
