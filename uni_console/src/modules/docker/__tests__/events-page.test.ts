// @vitest-environment jsdom
/**
 * 事件流详版页（views/events.vue）的渲染与数据编排钉子：
 *   - 加载序（先历史、后实时）与按去重键的跨来源合并（回放重发不重行）；
 *   - 列的事实（绝对 + 相对时刻 / 类型徽标 / 中文 + 原始动作码 / 对象 / 主机 / 退出码染色）；
 *   - 服务端过滤（主机/类型/关键字透传）＋ 实时行的客户端过滤（流是全量的）；
 *   - 实时跟随 / 暂停（冻结列表 + 计数 + 恢复合并）；
 *   - 游标翻页与账目句（已加载 N 条 · 窗口共 M 条；「窗口共」以历史首拉落定为闸）；
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
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

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

/** Art* 全局组件替身：保留 slot（default + left —— 工具条内容在 ArtTableHeader 的
 *  #left 里，替身不渲染它的话连接态/暂停/重试控件整块看不见）。 */
const passthrough = (name: string) =>
  defineComponent({
    name,
    setup:
      (_, { slots }) =>
      () =>
        h('div', { 'data-stub': name }, [slots.default?.(), slots.left?.()])
  })

/** 三个 Art* 替身的统一桩面（三处挂载共用一份）。 */
const STUBS = {
  ArtTableHeader: passthrough('ArtTableHeader'),
  ArtButtonTable: passthrough('ArtButtonTable'),
  ArtSvgIcon: passthrough('ArtSvgIcon')
}

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

/** 已就绪的路由（页面只借它做 router.back 与 route.query）。 */
async function readyRouter() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/', component: { template: '<div />' } }]
  })
  await router.push('/')
  await router.isReady()
  return router
}

async function mountEvents() {
  const router = await readyRouter()
  const w = mount(EventsPage, {
    global: {
      plugins: [router],
      stubs: STUBS
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
      stubs: STUBS
    }
  })
  mounted.push(wrapper)
  return { wrapper, toggle }
}

/** 触发工具条内置刷新（ArtTableHeader 的 @refresh —— 原 hero 刷新钮语义的继承者；
 *  ArtTableHeader 是透传替身，直接 $emit 走页面的 manualRefresh 链）。 */
async function clickRefresh(w: VueWrapper) {
  const header = w.findComponent({ name: 'ArtTableHeader' })
  expect(header.exists(), '页内应有 ArtTableHeader（刷新的唯一入口）').toBe(true)
  header.vm.$emit('refresh')
  await flush()
}

/** 在过滤面板（ArtSearchBar）里改一项筛选：直接驱动面板的 v-model。
 *  面板 → 表单的控件接线是 ArtSearchBar 自己的事（各列表页共用同一份），本页的
 *  回归面是「过滤变化 → 服务端参数 + 时间轴重建」，故从面板的模型口进入。
 *  关键词另有一条**走真控件**的用例（打字即生效，见「过滤」组）。 */
async function setFilter(w: VueWrapper, patch: Record<string, unknown>) {
  const bar = w.findComponent({ name: 'ArtSearchBar' })
  expect(bar.exists(), '页内应有 ArtSearchBar（过滤面板）').toBe(true)
  bar.vm.$emit('update:modelValue', { ...(bar.props('modelValue') as object), ...patch })
  await flush()
}

/** 关键字输入（走真 ElInput：打字 → 面板 → 页面过滤，输入即生效）。 */
async function typeKeyword(w: VueWrapper, text: string) {
  const input = w.find('.art-search-bar input')
  expect(input.exists(), '过滤面板应有关键词输入框').toBe(true)
  await input.setValue(text)
  await flush()
}

/** 点一档类型筛选（面板的类型下拉）。 */
async function clickType(w: VueWrapper, label: string) {
  const TYPES: Record<string, string> = {
    容器: 'container',
    镜像: 'image',
    卷: 'volume',
    网络: 'network'
  }
  expect(TYPES[label], `类型筛选「${label}」应是四档之一`).toBeTruthy()
  await setFilter(w, { type: TYPES[label] })
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

  it('列集合与行结构：六列固定，每行六格一一对应（表头与单元格同序）', async () => {
    api.fetchDockerEventHistory.mockResolvedValue(
      historyResp([historyItem({ t: BASE_SEC * 1000 - 120_000, action: 'die', exitCode: 137 })])
    )
    const w = await mountEvents()

    const heads = w
      .findAll('.el-table__header th')
      .map((th) => th.text().trim())
      .filter((t) => t !== '')
    expect(heads).toEqual(['时刻', '类型', '动作', '对象', '主机', '退出码'])

    const cells = w.find('.el-table__body tbody tr').findAll('td')
    expect(cells, '每行六格（列集合与行结构一致）').toHaveLength(6)
    // 时刻：绝对 + 相对两行同格（同一 t 的两种读法）。
    expect(cells[0]!.find('.ev-time__abs').exists()).toBe(true)
    expect(cells[0]!.find('.ev-time__rel').exists()).toBe(true)
    // 类型：图标 + 中文名（色不单独表意）。
    expect(cells[1]!.find('.ev-type').exists()).toBe(true)
    // 动作：中文结论（.ev-action）与原始码（.ev-raw）是**两个**元素（主辅分家）。
    expect(cells[2]!.find('.ev-action').text()).toBe('退出')
    expect(cells[2]!.find('.ev-raw').text()).toBe('die')
    expect(cells[2]!.find('.ev-act').attributes('title')).toBe('退出 · die')
    // 对象：短显 + 悬停看全（title 是完整引用）。
    expect(cells[3]!.find('.ev-actor').attributes('title')).toContain('uni-center-core')
    // 主机：模块统一的主机胶囊（docker-host）。
    expect(cells[4]!.find('.docker-host').exists()).toBe(true)
    expect(cells[4]!.find('.ev-host').text()).toBe('bogon')
    // 退出码：原始事实直显（exit 137），≠0 红染。
    expect(cells[5]!.find('.ev-exit').text()).toBe('exit 137')
    expect(cells[5]!.find('.ev-exit').classes()).toContain('is-danger')
  })

  it('工具行：连接态给点 + 文字（颜色不单独表意）；暂停事实在工具行且可点即恢复', async () => {
    api.fetchDockerEventHistory.mockResolvedValue(historyResp([historyItem()]))
    const w = await mountEvents()

    // 实时：点 + 「实时」文字同列（点只是形，结论在文字里）。
    const status = w.find('.ev-status')
    expect(status.text()).toBe('实时')
    expect(status.find('.ev-status__dot').exists()).toBe(true)
    expect(status.classes()).toContain('is-live')

    // 暂停：事实一条在工具行（不是跨列横幅），文字是结论；点它即恢复。
    await w
      .findAll('button')
      .find((b) => b.text() === '暂停')!
      .trigger('click')
    await flush()
    const chip = w.find('.ev-paused')
    expect(chip.exists()).toBe(true)
    expect(chip.text()).toBe('已暂停')
    await chip.trigger('click')
    await flush()
    expect(w.find('.ev-paused').exists()).toBe(false)
  })

  it('工具条搜索钮与过滤面板接线：has-active-filter 随筛选变化', async () => {
    api.fetchDockerEventHistory.mockResolvedValue(historyResp([]))
    const w = await mountEvents()
    const header = () => w.findComponent({ name: 'ArtTableHeader' }).attributes('has-active-filter')
    expect(header()).toBe('false')
    await clickType(w, '镜像')
    expect(header()).toBe('true')
  })

  it('对象列缩短而非截断：actor_name 本身就是完整 sha256（71 字符）时取 12 位短 id', async () => {
    const fullSha = 'sha256:' + 'a'.repeat(64) // 7 + 64 = 71 字符（评审 D 的越界渲染）
    api.fetchDockerEventHistory.mockResolvedValue(
      historyResp([historyItem({ t: BASE_SEC * 1000 - 120_000, actorName: fullSha })])
    )
    const w = await mountEvents()
    const actor = w.find('.ev-actor').text()
    expect(actor).toBe('aaaaaaaaaaaa') // 12 位短 id（模块「长 id 短显」惯例）
    expect(actor.length).toBe(12)
    expect(actor).not.toContain('sha256:')
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

  it('空态两态可辨：流已连接但窗口无事件 vs 筛选没命中（两种事实，结论句不同）', async () => {
    api.fetchDockerEventHistory.mockResolvedValue(historyResp([]))
    const w = await mountEvents()
    // 实时流已连上（openDockerEventsStream 已解析）：报「已连接、等待事件」这一
    // 件事实；此时没有筛选可清，不给「清除筛选」。
    expect(w.find('.ev-empty__fact').text()).toBe('实时流已连接 · 等待事件')
    expect(w.findAll('button').find((b) => b.text() === '清除筛选')).toBeUndefined()

    // 筛了没命中：结论句换成筛选的事实，并给「清除筛选」的出口。
    await clickType(w, '容器')
    expect(w.find('.ev-empty__fact').text()).toBe('没有匹配的事件')
    const clear = w.findAll('button').find((b) => b.text() === '清除筛选')
    expect(clear, '筛选没命中应给「清除筛选」').toBeTruthy()

    // 清除筛选 = 清空三项 + 重拉（回到「窗口内没有事件」那一态）。
    api.fetchDockerEventHistory.mockClear()
    await clear!.trigger('click')
    await flush()
    expect(api.fetchDockerEventHistory).toHaveBeenLastCalledWith({})
    expect(w.find('.ev-empty__fact').text()).toBe('实时流已连接 · 等待事件')
  })

  it('空态：流不在线（停机）时不冒充「已连接」—— 只报窗口这一件事实', async () => {
    api.fetchDockerEventHistory.mockResolvedValue(historyResp([]))
    api.openDockerEventsStream.mockImplementationOnce(() =>
      Promise.resolve(makeFakeStream({ ok: false, status: 401 }).response)
    )
    const w = await mountEvents()
    expect(w.find('.ev-empty__fact').text()).toBe('窗口内没有事件')
  })

  it('过滤面板：三项维度与选项不变（关键词 / 类型四档 / 主机），全部服务端过滤', async () => {
    const w = await mountEvents()
    const bar = w.findComponent({ name: 'ArtSearchBar' })
    const items = bar.props('items') as Array<Record<string, any>>
    expect(items.map((i) => i.key)).toEqual(['keyword', 'type', 'host'])
    expect(items[0].placeholder).toBe('对象 / 动作')
    expect(items[1].options.map((o: any) => o.value)).toEqual([
      'container',
      'image',
      'volume',
      'network'
    ])
    expect(items[1].placeholder).toBe('全部类型')
    expect(items[2].placeholder).toBe('全部主机')
    // 主机多了要能敲名字找（与容器/镜像/卷/网络/项目页同款）。
    expect(items[2].filterable).toBe(true)
  })

  it('关键词：打字即生效（不点「查询」也重拉），且只在关键词真变了时才打端点', async () => {
    api.fetchDockerEventHistory.mockResolvedValue(historyResp([]))
    const w = await mountEvents()
    api.fetchDockerEventHistory.mockClear()

    await typeKeyword(w, 'web')
    expect(api.fetchDockerEventHistory).toHaveBeenLastCalledWith({ keyword: 'web' })

    // 只多敲一个空白：去空白后的过滤没变（仍是 web）→ 不重复打端点。
    api.fetchDockerEventHistory.mockClear()
    await typeKeyword(w, 'web ')
    expect(api.fetchDockerEventHistory).not.toHaveBeenCalled()
  })

  it('主机筛选：透传 hostId（全部主机 = 空 → 不发该参数）', async () => {
    api.fetchDockerEventHistory.mockResolvedValue(historyResp([]))
    const w = await mountEvents()
    api.fetchDockerEventHistory.mockClear()

    await setFilter(w, { host: 'h2' })
    expect(api.fetchDockerEventHistory).toHaveBeenLastCalledWith({ hostId: 'h2' })

    await setFilter(w, { host: '' })
    expect(api.fetchDockerEventHistory).toHaveBeenLastCalledWith({})
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

  it('账目句以历史首拉落定为闸：未落定不报「窗口共」；落定后两数并列自洽', async () => {
    // 首拉挂在半空（可控 deferred）：「历史未落定」的窗口由用例精确持有。
    let release!: (v: unknown) => void
    api.fetchDockerEventHistory.mockImplementation(
      () =>
        new Promise((res) => {
          release = res
        })
    )
    const router = await readyRouter()
    // 同步挂载后（骨架重渲染尚未冲刷）就是「落定前」的真实一帧：账目句在场，
    // 但不报「窗口共 N」（total 在历史落定前是缺位事实，初值 0 报出来就是
    // 「已加载 N · 窗口共 0」的当场矛盾）。
    const w = mount(EventsPage, { global: { plugins: [router], stubs: STUBS } })
    mounted.push(w as VueWrapper)
    const before = w.find('.ev-more__hint')
    expect(before.exists()).toBe(true)
    expect(before.text()).toContain('已加载 0 条')
    expect(before.text()).not.toContain('窗口共')

    // 首拉在飞（骨架态）：账目句整句不渲染（历史仍在路上时的主形态）。
    await flush()
    expect(w.find('.ev-more__hint').exists()).toBe(false)

    // 落定：两数并列，且与响应自洽（已加载 = 页内行数、窗口共 = total）。
    release(
      historyResp(
        [
          historyItem({ t: BASE_SEC * 1000 - 1_000, action: 'die' }),
          historyItem({ t: BASE_SEC * 1000 - 2_000, action: 'stop' })
        ],
        3,
        '1790600000000:2'
      )
    )
    await flush()
    expect(w.find('.ev-more__hint').text()).toContain('已加载 2 条 · 窗口共 3 条')
  })
})

describe('事件流详版页：四态与致命失败', () => {
  it('首拉失败：紧凑失败态（ElEmpty）+ 重试；重试成功后回到列表', async () => {
    api.fetchDockerEventHistory.mockRejectedValueOnce(new Error('boom'))
    const w = await mountEvents()
    // 列表页纯化：失败态是紧凑 ElEmpty（wkl 卡外观已删），结论句 + 重试钮。
    expect(w.find('.docker-empty').exists()).toBe(true)
    expect(w.text()).toContain('事件历史获取失败')

    api.fetchDockerEventHistory.mockResolvedValue(historyResp([historyItem({ action: 'start' })]))
    const retry = w.findAll('button').find((b) => b.text() === '重试')
    expect(retry).toBeTruthy()
    await retry!.trigger('click')
    await flush()
    expect(w.findAll('.ev-raw').map((n) => n.text())).toEqual(['start'])
  })

  it('历史拉取失败但实时流正常：失败态照旧（不报账目句、不假装有列表）；重试落定后账目自洽', async () => {
    api.fetchDockerEventHistory.mockRejectedValueOnce(new Error('boom'))
    const w = await mountEvents()
    // 失败态是结论句 + 重试；实时流仍在后台收行，但不在失败态里混进账目句。
    expect(w.find('.docker-empty').exists()).toBe(true)
    expect(w.find('.ev-more__hint').exists()).toBe(false)
    streams[0].push(evLine({ action: 'die', t: BASE_SEC * 1000 - 5_000 }) + '\n')
    await flush()
    expect(w.find('.ev-more__hint').exists()).toBe(false)

    // 重试成功：历史落定，账目句重启且与事实自洽 —— 失败期间实时接受的
    // 那条（die）也进了时间轴，窗口共 = 新抓取时刻的窗口全量（2）。
    api.fetchDockerEventHistory.mockResolvedValue(
      historyResp([historyItem({ t: BASE_SEC * 1000 - 60_000, action: 'start' })], 2)
    )
    const retry = w.findAll('button').find((b) => b.text() === '重试')
    expect(retry, '失败态应给重试入口').toBeTruthy()
    await retry!.trigger('click')
    await flush()
    expect(w.findAll('.ev-raw').map((n) => n.text())).toEqual(['die', 'start'])
    expect(w.find('.ev-more__hint').text()).toContain('已加载 2 条 · 窗口共 2 条')
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

// ───────────────────────────────────────────────────────────────────────────
// 表体高度口径：**不得来自写死常数**（旧病：`calc(var(--art-full-height) - 256px)`
// 猜上方 chrome，猜错时表体比内容矮、卡片下方却还空着一块 —— 空白与滚动条并存，
// 低视口下还切行）；装得下时**不得产生内滚容器**，只是「由真实剩余高度约束」。
//
// 为什么一半是源码断言：jsdom 没有布局引擎（getBoundingClientRect 恒 0、
// scrollHeight 恒 0），「表体可视高 = 内容高」这类不变量在单测里测不到（会绿得
// 毫无意义）。真值验证走 playwright 探针（.tmp-ux-20261010/fix-events-height/：
// 1920×1080 / 820 / 760 三档改前改后量测 + 真滚轮）。这里钉住的是**让探针变绿的
// 结构**，以及一个 jsdom 里确实可断言的 DOM 事实（EP 只在拿到 max-height 属主时
// 才往表根写内联 max-height —— 没有内联值 = 高度不来自常数）。
// ───────────────────────────────────────────────────────────────────────────

/** 事件流页 SFC 源码（含 `<style scoped>`）。取法随 w1-list-ux.test.ts（process.cwd()
 *  即 uni_console：测试按 `pnpm --dir uni_console test` 跑）。 */
const EVENTS_SRC = readFileSync(
  resolve(process.cwd(), 'src/modules/docker/views/events.vue'),
  'utf8'
)

/** 去掉注释（模板 HTML 注释 / 块注释 / 行注释）：论证里引用旧写法（如 `-256px`
 *  的推导、被删的 max-height 口径）不该被当成真实代码。**块注释必须在行过滤之前
 *  整块去掉** —— 先按行丢 `/*` 开头那行的话，多行块注释的正则就少了起点，注释体
 *  会留在「代码」里（旧写法被误判成真实声明）。 */
function stripComments(source: string): string {
  return source
    .replace(/<!--[\s\S]*?-->/g, '')
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .split('\n')
    .filter((line) => {
      const t = line.trim()
      return !t.startsWith('//') && !t.startsWith('*')
    })
    .join('\n')
}

/** 取一段 SCSS 块（从 `from` 起、花括号配平到结束）。 */
function block(source: string, from: string): string {
  const start = source.indexOf(from)
  expect(start, `未找到块：${from}`).toBeGreaterThanOrEqual(0)
  let depth = 0
  for (let i = start; i < source.length; i++) {
    if (source[i] === '{') depth++
    else if (source[i] === '}') {
      depth--
      if (depth === 0) return source.slice(start, i + 1)
    }
  }
  throw new Error(`块未闭合：${from}`)
}

const EVENTS_CODE = stripComments(EVENTS_SRC)

describe('事件流详版页：表体高度口径（不猜 chrome 高度）', () => {
  it('表体高度不来自写死常数：没有 max-height 声明、没有 max-height 属性、没有减去常数的 calc', () => {
    expect(
      EVENTS_CODE,
      '本页不再用「max-height 上限」定表体高度（任何 max-height 声明都请改走 .ev-table 的 flex 链）'
    ).not.toMatch(/max-height/)
    expect(EVENTS_CODE, 'ElTable 不得再收 max-height 属性').not.toMatch(/:max-height=/)
    expect(
      EVENTS_CODE,
      '不得再用 `var(--art-full-height) - <常数>` 猜上方 chrome（猜错就是空白 + 滚动条并存）'
    ).not.toMatch(/var\(--art-full-height\)\s*-/)
    expect(EVENTS_CODE, '旧的 max(240px, …) 下限常数不得复活').not.toMatch(/max\(\s*240px/)
  })

  it('表根接回 flex 链：flex 1 1 auto + min-height 0（由真实剩余高度约束，能缩才能让内滚有出路）', () => {
    const rule = block(EVENTS_CODE, '.ev-table {')
    expect(rule, '表根必须是 flex 项').toMatch(/flex:\s*1 1 auto/)
    expect(
      rule,
      'basis 取 auto（不是 0）：卡随内容收缩时卡体没有确定高度，basis:0 会把表塌成 0'
    ).not.toMatch(/flex:\s*1 1 0/)
    expect(rule, 'min-height: 0 才允许被页根截住时收缩').toMatch(/min-height:\s*0/)
  })

  it('工具行 / 静默失败标注 / 游标条 flex: none：让位的只能是表体一节（结论行常驻卡内）', () => {
    expect(block(EVENTS_CODE, ':deep(#art-table-header) {')).toMatch(/flex:\s*none/)
    expect(block(EVENTS_CODE, '.ev-refresh-error {')).toMatch(/flex:\s*none/)
    const more = block(EVENTS_CODE, '.ev-more {')
    expect(more).toMatch(/flex:\s*none/)
  })

  it('DOM：表是卡体的直接子项（flex 链成立的结构前提），且 EP 没拿到内联 max-height', async () => {
    api.fetchDockerEventHistory.mockResolvedValue(historyResp([historyItem()]))
    const w = await mountEvents()

    const table = w.find('.el-card__body > .el-table')
    expect(table.exists(), '表必须是卡体的直接子项（中间夹一层，剩余高度就传不下来）').toBe(true)
    expect(table.classes(), '表根带 ev-table：flex 规则落点').toContain('ev-table')
    // EP 只在 maxHeight 属性在场时把 max-height 写进表根的内联样式 —— 空值即「高度不来自常数」。
    const el = table.element as HTMLElement
    expect(el.style.maxHeight, '表根不得有内联 max-height').toBe('')
    expect(el.style.height, '表根不得有内联 height（高度由 flex 链给）').toBe('')

    // 卡体三节同序：工具行 → 表 → 游标条（账目句/加载更早在表之后，常驻卡底）。
    const body = w.find('.el-card__body')
    const kids = Array.from(body.element.children)
    const tableIdx = kids.findIndex((c) => c.classList.contains('ev-table'))
    const moreIdx = kids.findIndex((c) => c.classList.contains('ev-more'))
    expect(tableIdx, '表在卡体内').toBeGreaterThanOrEqual(0)
    expect(moreIdx, '游标条在卡体内、且排在表之后').toBeGreaterThan(tableIdx)
  })
})
