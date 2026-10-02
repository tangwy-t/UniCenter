// @vitest-environment jsdom
/**
 * 容器详情页（8a 页面化）：四个 Tab 的懒挂载与断流纪律、主机跟随、写操作后的
 * 状态重读，以及「权限门控」这类整页化后才存在的形态。
 *
 * 为什么这些用例要存在：被删的 workload-drawer 把详情页的三块能力（inspect /
 * 日志 / 终端）搬进列表页，靠复制粘贴最容易走样的正是生命周期纪律（首切才拉、
 * 切走断流、离开页面断流）—— 8a 又把它整页化了一次，那些纪律必须原样站住。
 * 页面与抽屉的两处结构性差别也在本文里钉住：
 *   ① 容器 id 在**路由参数**里（不再有「行桩 + inspect 兜底」这一层）；
 *   ② 写指令成功后重读本页的 inspect（QA 发现抽屉版只重拉列表，抽屉自己停在旧
 *      状态上 —— 页面化后本页就是唯一表面，必须重读自己）。
 *
 * 页面不再 teleport（没有 ElDrawer）：页面内容从 wrapper 断言；确认弹窗要 wrapper
 * 与 document.body 两处都查 —— ElDialog 默认就地渲染（EP 的 teleport 只在
 * append-to-body 时才启用），而 VTU 挂载的容器不进 document。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, nextTick, onUnmounted, type Component } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'

const api = vi.hoisted(() => ({
  fetchDockerHosts: vi.fn(),
  fetchDockerState: vi.fn(),
  sendDockerCmd: vi.fn(),
  fetchDockerCmdResult: vi.fn(),
  fetchDockerStatsHistory: vi.fn(),
  openDockerLogStream: vi.fn(),
  openDockerStatsStream: vi.fn()
}))
vi.mock('../api', () => ({ ...api, default: undefined }))

// 终端懒挂载桩：真实 pty-terminal 会拉 xterm（重），这里只要「挂没挂、销毁没销毁」
// 的事实（真组件在 onUnmounted 里向服务端发 cancel —— 销毁计数就是「会话下线」）。
// （__isTeleport 等 vue 内部标记一并给出：defineAsyncComponent 的 loader 会按
// 具名导出探测模块形态，mock 工厂缺了它们会抛「No export is defined」。）
const pty = vi.hoisted(() => ({ mounted: 0, unmounted: 0 }))
vi.mock('../components/pty-terminal.vue', () => ({
  default: defineComponent({
    name: 'PtyTerminalStub',
    setup() {
      pty.mounted += 1
      onUnmounted(() => {
        pty.unmounted += 1
      })
      return () => h('div', { 'data-stub': 'PtyTerminal' })
    }
  }),
  __esModule: true,
  name: 'PtyTerminalStub',
  __isTeleport: false,
  __isKeepAlive: false,
  __isSuspense: false
}))

// 权限可开关：终端 Tab 的渲染门（docker:exec）与统计/日志门（docker:inspect）要看它。
const auth = vi.hoisted(() => ({ allow: new Set<string>() }))
vi.mock('@/hooks/core/useAuth', () => ({
  useAuth: () => ({
    hasAuth: (perm: string) => auth.allow.has(perm),
    hasAnyAuth: () => true
  })
}))

import ContainerDetail from '../views/container-detail/index.vue'

const CID = 'abcdef1234567890'
const OTHER_ID = 'fedcba0987654321'

const HOSTS = {
  list: [
    { id: 'h1', hostname: 'bogon', primaryIp: '192.168.12.105', online: true, dockerOk: true },
    { id: 'h2', hostname: 'nas', primaryIp: '192.168.12.106', online: true, dockerOk: true }
  ]
}

const INSPECT_PAYLOAD = {
  name: 'mysql',
  image: 'mysql:8',
  state: 'running',
  created: 1790000000,
  started_at: 1790000100,
  restart_policy: 'always',
  networks: ['bridge'],
  mounts: [{ type: 'volume', source: 'dbdata', destination: '/var/lib/mysql', rw: true }],
  env: ['MYSQL_ROOT_PASSWORD=secret', 'PATH=/usr/sbin:/usr/bin:/sbin'],
  labels: { 'com.docker.compose.project': 'uni-center' },
  entrypoint: ['docker-entrypoint.sh'],
  cmd: ['mysqld'],
  ports: [{ privatePort: 3306, publicPort: 3306, type: 'tcp' }]
}

/** 主机快照：同一容器的那一行（保护标记与原生状态句只在这里）。 */
const STATE_H2 = {
  lastSync: 1790600000,
  stale: false,
  ageSeconds: 3,
  neverReported: false,
  dockerOk: true,
  containers: [
    {
      id: CID,
      name: 'mysql',
      image: 'mysql:8',
      state: 'running',
      statusText: 'Up 16 hours',
      cpuPercent: 1,
      memUsageMb: 200,
      memLimitMb: 1024,
      netRxBytesSec: 0,
      netTxBytesSec: 0,
      ports: [{ privatePort: 3306, publicPort: 3306, type: 'tcp' }],
      protected: false
    }
  ],
  images: [],
  volumes: [],
  networks: [],
  projects: []
}

/** 流端点的假 Response：reader 永不落定（流保持在飞）—— 断流断言的前提。 */
function fakeStreamResponse() {
  return {
    ok: true,
    body: {
      getReader: () => ({
        read: () => new Promise<ReadableStreamReadResult<Uint8Array>>(() => {})
      })
    }
  }
}

/**
 * stats 流的假 Response：逐行喂 NDJSON 样本后正常 done。
 * 样本行是 ASCII（纯数字字段），手工按 charCode 编码即够 —— 不赌测试环境有没有 TextEncoder。
 */
function statsStreamResponse(lines: string[]) {
  const chunks = lines.map((l) => Uint8Array.from(`${l}\n`, (c) => c.charCodeAt(0)))
  let i = 0
  return {
    ok: true,
    body: {
      getReader: () => ({
        read: () =>
          i < chunks.length
            ? Promise.resolve({ done: false, value: chunks[i++] })
            : Promise.resolve({ done: true, value: undefined })
      })
    }
  }
}

/** 一行 stats 样本（core statsNDJSONLine 的线上形状）。 */
function statsLine(t: number, over: Record<string, number | boolean> = {}): string {
  return JSON.stringify({
    seq: 1,
    t,
    cpu_percent: 3.5,
    mem_usage_mb: 200,
    mem_limit_mb: 1024,
    net_rx_bytes_sec: 10240,
    net_tx_bytes_sec: 2048,
    eof: false,
    ...over
  })
}

/** 等流闭环走完（受理 → 轮询 → 接流 → 读帧全是微任务，一轮宏任务即收敛）。 */
async function flushStream(rounds = 4) {
  for (let i = 0; i < rounds; i++) {
    await new Promise((r) => setTimeout(r, 0))
    await nextTick()
  }
}

/** Art* 全局组件替身：保留 slot（真组件靠 unplugin 注册，测试环境里没有）。 */
const passthrough = (name: string) =>
  defineComponent({
    name,
    setup:
      (_, { slots }) =>
      () =>
        h('div', { 'data-stub': name }, [slots.default?.()])
  })

const STUBS = {
  // ArtTable（环境 Tab 的 env/labels 两张表）依赖 Pinia 的表格 store —— 单组件挂载
  // 没有应用实例给 store，用透传替身（data/columns 落 $attrs，用例从那里断言）。
  ArtTable: defineComponent({
    name: 'ArtTable',
    inheritAttrs: false,
    setup:
      (_, { slots }) =>
      () =>
        h('div', { 'data-stub': 'ArtTable' }, [slots.default?.()])
  }),
  ArtSvgIcon: passthrough('ArtSvgIcon')
}

const mounted: VueWrapper[] = []
afterEach(() => {
  for (const w of mounted.splice(0)) w.unmount()
  vi.clearAllMocks()
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

beforeEach(() => {
  vi.stubGlobal(
    'ResizeObserver',
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
  )
  pty.mounted = 0
  pty.unmounted = 0
  auth.allow = new Set(['docker:inspect', 'docker:manage', 'docker:delete', 'docker:exec'])
  api.fetchDockerHosts.mockResolvedValue(HOSTS)
  api.fetchDockerState.mockResolvedValue(STATE_H2)
  api.sendDockerCmd.mockResolvedValue({ ref: 'r1' })
  api.fetchDockerCmdResult.mockResolvedValue({ status: 'succeeded', payload: INSPECT_PAYLOAD })
  // stats 历史默认空（= 纯实时，现状行为）；要历史的用例用 mockResolvedValueOnce。
  api.fetchDockerStatsHistory.mockResolvedValue({ samples: [] })
  api.openDockerLogStream.mockResolvedValue(fakeStreamResponse())
  api.openDockerStatsStream.mockResolvedValue(fakeStreamResponse())
})

let currentRouter: Router | null = null

/** 挂载页面：路由参数给容器 id，query 给主机（与站内深链同一形态）。 */
async function mountPage(
  query: Record<string, string> = { host: 'h2' },
  id: string = CID,
  component: Component = ContainerDetail
) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/docker/containers', name: 'DockerContainers', component: { template: '<div />' } },
      { path: '/docker/containers/:id', name: 'DockerWorkloadDetail', component }
    ]
  })
  currentRouter = router
  await router.push({ path: `/docker/containers/${id}`, query })
  await router.isReady()
  const w = mount(component, { global: { plugins: [router], stubs: STUBS } })
  mounted.push(w)
  await flushStream(2)
  return w
}

/** 切 Tab：ElTabs 是真组件，一次真实点击会同时发 update:modelValue（v-model）与
 *  tab-change（页面据此把 Tab 写进 URL）—— 两个都要发，只发前者会让「Tab 进 URL」
 *  这条纪律在用例里静默失守。 */
async function switchTab(w: VueWrapper, tab: string) {
  const tabs = w.findComponent({ name: 'ElTabs' })
  expect(tabs.exists(), '详情页里应渲染 ElTabs').toBe(true)
  tabs.vm.$emit('update:modelValue', tab)
  tabs.vm.$emit('tab-change', tab)
  await flushStream(2)
}

/** 页面上的按钮（确认弹窗 teleport 到 body，页面按钮在 wrapper 里）。 */
function pageButtons(w: VueWrapper) {
  return w
    .findAll('button')
    .map((b) => ({ el: b.element as HTMLButtonElement, text: b.text().trim() }))
}

async function clickPageButton(w: VueWrapper, text: string) {
  const btn = pageButtons(w).find((b) => b.text === text)
  expect(btn, `按钮「${text}」应已渲染`).toBeTruthy()
  btn!.el.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  await flushStream(2)
}

/** 确认弹窗里的主按钮。ElDialog 默认**就地渲染**（EP 的 teleport 只在 append-to-body
 *  时才启用），而页面在测试里挂在游离容器上 —— 弹窗按钮既可能在 wrapper 里，也可能
 *  被别的 teleport 带到 body，两处都查。 */
async function clickDialogButton(w: VueWrapper, text: string) {
  const candidates = [
    ...w.findAll('button').map((b) => b.element as HTMLButtonElement),
    ...Array.from(document.body.querySelectorAll('button'))
  ]
  const btn = candidates.find((b) => (b.textContent ?? '').trim() === text && !b.disabled)
  expect(btn, `弹窗按钮「${text}」应已渲染且可用`).toBeTruthy()
  btn!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  await flushStream(2)
}

const inspectCalls = () =>
  api.sendDockerCmd.mock.calls.filter(
    (c) => (c[1] as { action?: string }).action === 'container:inspect'
  )

describe('概览（container:inspect，host 取 query）', () => {
  it('进页面即按 query 主机读 inspect，展示载荷字段（含 snake_case 折叠）', async () => {
    const w = await mountPage()
    expect(api.sendDockerCmd).toHaveBeenCalledWith('h2', {
      action: 'container:inspect',
      target: CID,
      options: {}
    })
    const text = w.text()
    expect(text).toContain('mysql:8')
    // snake_case → camelCase 的边界折叠：没折叠这些字段会静默空白。
    expect(text).toContain('always')
    expect(text).toContain('dbdata → /var/lib/mysql')
    // 端口来自载荷（快照与载荷同源，页面不写两种话）。
    expect(text).toContain('3306 → 3306/tcp')
  })

  it('实体头给出容器名、短 id 与行归属主机；状态句用快照的原生句子', async () => {
    const w = await mountPage()
    expect(w.find('.cd-hero__title').text()).toBe('mysql')
    expect(w.find('.cd-hero__id').text()).toBe('abcdef123456')
    expect(w.find('.cd-hero__host').text()).toContain('nas')
    expect(w.text()).toContain('Up 16 hours')
  })

  it('快照未到时保护显示「—」（不猜一个「未受保护」）', async () => {
    api.fetchDockerState.mockRejectedValue(new Error('snapshot down'))
    const w = await mountPage()
    const facts = w.findAll('.cd-fact').map((f) => f.text())
    expect(facts.find((t) => t.includes('保护'))).toContain('—')
  })
})

describe('深链形态：?tab= 落位与权限回退', () => {
  it('?tab=logs 直接落在日志 Tab 并首拉日志', async () => {
    await mountPage({ host: 'h2', tab: 'logs' })
    expect(api.sendDockerCmd).toHaveBeenCalledWith('h2', {
      action: 'container:logs',
      target: CID,
      options: { tail: 100 }
    })
  })

  it('?tab=pty 有执行权限时落终端 Tab；无权限回退概览（且终端 Tab 不渲染）', async () => {
    auth.allow = new Set(['docker:inspect', 'docker:manage', 'docker:delete'])
    const w = await mountPage({ host: 'h2', tab: 'pty' })
    const tabs = w.findComponent({ name: 'ElTabs' })
    expect(tabs.props('modelValue')).toBe('overview')
    expect(w.text()).not.toContain('终端')
    expect(pty.mounted).toBe(0)
  })

  it('?tab 之外：概览是默认屏', async () => {
    const w = await mountPage({ host: 'h2' })
    expect(w.findComponent({ name: 'ElTabs' }).props('modelValue')).toBe('overview')
  })

  it('Tab 切换写进 URL（刷新/分享回到同一屏）', async () => {
    const w = await mountPage()
    await switchTab(w, 'env')
    expect(currentRouter!.currentRoute.value.query.tab).toBe('env')
    // 回到默认屏时不留 tab（URL 只说与默认不同的那部分）。
    await switchTab(w, 'overview')
    expect(currentRouter!.currentRoute.value.query.tab).toBeUndefined()
  })
})

describe('日志 Tab（懒挂载 + Follow 断流）', () => {
  it('概览停留时不发 container:logs；首次切到才拉', async () => {
    const w = await mountPage()
    expect(api.sendDockerCmd).not.toHaveBeenCalledWith(
      'h2',
      expect.objectContaining({ action: 'container:logs' })
    )
    await switchTab(w, 'logs')
    expect(api.sendDockerCmd).toHaveBeenCalledWith('h2', {
      action: 'container:logs',
      target: CID,
      options: { tail: 100 }
    })
  })

  it('Follow 建立流；切走 Tab 即断流（AbortController abort）', async () => {
    const w = await mountPage({ host: 'h2', tab: 'logs' })
    const viewer = w.findComponent({ name: 'LogViewer' })
    expect(viewer.exists()).toBe(true)
    viewer.vm.$emit('update:following', true)
    await flushStream(2)

    expect(api.sendDockerCmd).toHaveBeenCalledWith('h2', {
      action: 'container:logs',
      target: CID,
      options: { tail: 100, follow: true }
    })
    expect(api.openDockerLogStream).toHaveBeenCalledTimes(1)
    const signal = api.openDockerLogStream.mock.calls[0]![2] as AbortSignal

    await switchTab(w, 'overview')
    expect(signal.aborted, '切走日志 Tab 应 abort 跟随流').toBe(true)
  })

  it('离开页面断流：会话生命周期 = 页面存活期', async () => {
    const w = await mountPage({ host: 'h2', tab: 'logs' })
    const viewer = w.findComponent({ name: 'LogViewer' })
    viewer.vm.$emit('update:following', true)
    await flushStream(2)
    const signal = api.openDockerLogStream.mock.calls[0]![2] as AbortSignal

    w.unmount()
    mounted.splice(mounted.indexOf(w), 1)
    expect(signal.aborted, '离开页面应 abort 跟随流').toBe(true)
  })
})

describe('概览 · 实时统计（container:stats 流）', () => {
  it('概览 Tab 激活即按 query 主机发 container:stats 并接入流；首帧样本落到读数与曲线', async () => {
    api.openDockerStatsStream.mockResolvedValueOnce(
      statsStreamResponse([statsLine(1790000000000), statsLine(1790000001000)])
    )
    const w = await mountPage()
    expect(api.sendDockerCmd).toHaveBeenCalledWith('h2', {
      action: 'container:stats',
      target: CID
    })
    expect(api.openDockerStatsStream).toHaveBeenCalledWith('h2', 'r1', expect.any(AbortSignal))
    await flushStream()
    const text = w.text()
    expect(text).toContain('实时统计')
    // 读数 = 曲线的直标通道（数值带单位、内存附上限与占比）。
    expect(text).toContain('3.5%')
    expect(text).toContain('200 MB')
    expect(text).toContain('上限 1 GB · 20%')
    // 曲线本体：折线 path 与端点点标记都吃到了样本。
    expect(w.find('.sc-line').exists()).toBe(true)
    expect(w.find('.sc-dot').exists()).toBe(true)
  })

  it('坏行跳过且不打断流（好样本照常入窗画线）', async () => {
    api.openDockerStatsStream.mockResolvedValueOnce(
      statsStreamResponse([
        'garbage',
        statsLine(1790000000000),
        '{broken json',
        statsLine(1790000001000)
      ])
    )
    const w = await mountPage()
    await flushStream()
    expect(w.text()).toContain('3.5%')
    // CPU 与内存两张小图至少各一条折线（网络组若也被折叠面板渲染则更多）。
    expect(w.findAll('.sc-line').length).toBeGreaterThanOrEqual(2)
  })

  it('eof 行收尾：给状态句而不是错误', async () => {
    api.openDockerStatsStream.mockResolvedValueOnce(
      statsStreamResponse([statsLine(1790000000000), statsLine(1790000001000, { eof: true })])
    )
    const w = await mountPage()
    await flushStream()
    expect(w.text()).toContain('统计流已结束')
    expect(w.text()).not.toContain('统计流已断开')
  })

  it('切走 Tab 即断流（AbortController abort）', async () => {
    const w = await mountPage()
    await flushStream(2)
    expect(api.openDockerStatsStream).toHaveBeenCalledTimes(1)
    const signal = api.openDockerStatsStream.mock.calls[0]![2] as AbortSignal

    await switchTab(w, 'logs')
    expect(signal.aborted, '切走概览 Tab 应 abort 统计流').toBe(true)
  })

  it('离开页面断流', async () => {
    const w = await mountPage()
    await flushStream(2)
    const signal = api.openDockerStatsStream.mock.calls[0]![2] as AbortSignal

    w.unmount()
    mounted.splice(mounted.indexOf(w), 1)
    expect(signal.aborted, '离开页面应 abort 统计流').toBe(true)
  })

  it('换容器（路由参数变）即换流：旧流 abort，新指令带新容器 id', async () => {
    await mountPage()
    await flushStream(2)
    const signal = api.openDockerStatsStream.mock.calls[0]![2] as AbortSignal

    await currentRouter!.push({ path: `/docker/containers/${OTHER_ID}`, query: { host: 'h2' } })
    await flushStream(2)
    expect(signal.aborted, '换容器应 abort 旧统计流').toBe(true)
    expect(api.sendDockerCmd).toHaveBeenCalledWith('h2', {
      action: 'container:stats',
      target: OTHER_ID
    })
    expect(api.openDockerStatsStream).toHaveBeenCalledTimes(2)
  })

  it('未运行的容器不发起流（空转指令只会换回一个错误结论），并给出解释句', async () => {
    // 两份事实**必须一致**：页面给统计组件的 running 是「inspect 优先、快照兜底」，
    // 而 inspect 要走受理 + 轮询两段、可能晚于快照落地 —— 只把 inspect 改成 exited
    // 的话，快照那几拍会把一个已停止的容器报成运行中并起流。那是夹具自相矛盾
    //（同一容器又 running 又 exited），不是页面的行为。
    api.fetchDockerCmdResult.mockResolvedValue({
      status: 'succeeded',
      payload: { ...INSPECT_PAYLOAD, state: 'exited', exit_code: 0 }
    })
    api.fetchDockerState.mockResolvedValue({
      ...STATE_H2,
      containers: STATE_H2.containers.map((c) => ({
        ...c,
        state: 'exited',
        statusText: 'Exited (0) 2 days ago'
      }))
    })
    const w = await mountPage()
    await flushStream(2)
    expect(api.sendDockerCmd).not.toHaveBeenCalledWith(
      'h2',
      expect.objectContaining({ action: 'container:stats' })
    )
    // 历史 GET 同一道门：不运行的容器没有可回看的曲线，空请求只会换回错误。
    expect(api.fetchDockerStatsHistory).not.toHaveBeenCalled()
    expect(api.openDockerStatsStream).not.toHaveBeenCalled()
    expect(w.text()).toContain('容器未运行，暂无实时统计')
  })

  it('无 docker:inspect 权限时不挂统计组件（不渲染 ≠ 禁用）', async () => {
    auth.allow = new Set(['docker:manage', 'docker:delete', 'docker:exec'])
    const w = await mountPage()
    await flushStream(2)
    expect(api.openDockerStatsStream).not.toHaveBeenCalled()
    expect(w.text()).not.toContain('实时统计')
  })
})

describe('概览 · 实时统计 · 历史预填（P2 · stats-history 半边）', () => {
  /** 按 y 轴量纲取一张曲线（CPU=% / 内存=MB / 网络=B/s；网络组折叠时也在 DOM）。 */
  function chartByUnit(w: VueWrapper, unit: string) {
    const chart = w
      .findAllComponents({ name: 'DockerStatsChart' })
      .find((c) => c.props('unit') === unit)
    expect(chart, `应渲染 unit=${unit} 的 stats 曲线`).toBeTruthy()
    return chart!
  }

  it('打开即先拉历史（query 主机 + 容器 id）再受理 container:stats —— 顺序不许倒', async () => {
    await mountPage()
    // 先有形状（历史预填），再接当下（实时流受理）：倒过来用户就要对着空白
    // 曲线干等建流。invocationCallOrder 钉的是先后，不是各自调没调。
    expect(api.fetchDockerStatsHistory).toHaveBeenCalledWith('h2', CID)
    const statsIdx = api.sendDockerCmd.mock.calls.findIndex(
      (c) => (c[1] as { action: string }).action === 'container:stats'
    )
    expect(statsIdx).toBeGreaterThanOrEqual(0)
    expect(api.fetchDockerStatsHistory.mock.invocationCallOrder[0]!).toBeLessThan(
      api.sendDockerCmd.mock.invocationCallOrder[statsIdx]!
    )
  })

  it('历史预填曲线；首帧同 t 去重衔接，网络曲线只画流样本', async () => {
    api.fetchDockerStatsHistory.mockResolvedValueOnce({
      samples: [
        { t: 1789999970000, cpuPercent: 10, memUsageMb: 300, memLimitMb: 1024 },
        { t: 1789999980000, cpuPercent: 12, memUsageMb: 320, memLimitMb: 1024 }
      ]
    })
    // 流首帧与历史末样本同 t（1789999980000）：去重丢历史留流帧；再一帧推进。
    api.openDockerStatsStream.mockResolvedValueOnce(
      statsStreamResponse([statsLine(1789999980000), statsLine(1789999990000)])
    )
    const w = await mountPage()
    await flushStream()

    // CPU 曲线的时刻序列 = 两枚历史 + 去重后的两枚流帧，t 严格递增 —— 用户
    // 看到的是一条连续序列，感知不到拼接缝。
    expect(chartByUnit(w, '%').props('times')).toEqual([
      1789999970000, 1789999980000, 1789999990000
    ])
    // 同 t 处留的是流帧（网络字段在场）—— 网络曲线不吃历史、也不丢这一帧。
    expect(chartByUnit(w, 'B/s').props('times')).toEqual([1789999980000, 1789999990000])
    // 摘要句如实交代两段口径；折叠组里的说明句交代网络无历史。
    expect(w.text()).toContain('历史约 30 分钟')
    expect(w.text()).toContain('网络速率仅实时段')
  })

  it('历史拉取失败静默降级为纯实时：流照常建立，不给错误态', async () => {
    api.fetchDockerStatsHistory.mockRejectedValueOnce(new Error('history down'))
    api.openDockerStatsStream.mockResolvedValueOnce(statsStreamResponse([statsLine(1790000000000)]))
    const w = await mountPage()
    await flushStream()
    // 实时流没被历史拖下水：照常受理、照常接流、读数照常落地。
    expect(api.openDockerStatsStream).toHaveBeenCalledTimes(1)
    expect(w.text()).toContain('3.5%')
    expect(chartByUnit(w, '%').props('times')).toEqual([1790000000000])
    // 错误态属于流的生命周期；回看失败只降级 —— 曲线回到纯实时口径。
    expect(w.text()).not.toContain('实时统计连接失败')
    expect(w.text()).toContain('最近约 2 分钟')
  })

  it('流端点打不开时历史仍在屏上（30 分钟回看不跟着流陪葬）', async () => {
    api.fetchDockerStatsHistory.mockResolvedValueOnce({
      samples: [{ t: 1789999970000, cpuPercent: 10, memUsageMb: 300, memLimitMb: 1024 }]
    })
    api.openDockerStatsStream.mockResolvedValueOnce({ ok: false, status: 404 })
    const w = await mountPage()
    await flushStream()
    // 404 = 会话过期的结论句；但曲线已经不是空白 —— 历史段先落了屏。
    expect(w.text()).toContain('该统计会话已过期')
    expect(chartByUnit(w, '%').props('times')).toEqual([1789999970000])
  })
})

describe('终端 Tab（懒挂载 + 权限门）', () => {
  it('切到终端 Tab 才挂载 PtyTerminal，离开/卸载即卸载', async () => {
    const w = await mountPage()
    expect(pty.mounted, '概览停留时不应挂终端').toBe(0)
    expect(pty.unmounted).toBe(0)

    await switchTab(w, 'pty')
    expect(pty.mounted, '切到终端 Tab 才建立会话').toBe(1)
    expect(w.find('[data-stub="PtyTerminal"]').exists()).toBe(true)

    await switchTab(w, 'overview')
    expect(pty.unmounted, '切走即销毁（真组件在卸载时发 cancel，会话随之下线）').toBe(1)
    expect(w.find('[data-stub="PtyTerminal"]').exists(), '切走即卸载（组件卸载时发 cancel）').toBe(
      false
    )

    await switchTab(w, 'pty')
    w.unmount()
    mounted.splice(mounted.indexOf(w), 1)
    // 页面卸载同样要走销毁。这里断的是**实例销毁**而不是 DOM：页面在测试里挂在
    // 游离 div 上（VTU 不带 attachTo 就不进 document），app.unmount 后 DOM 仍在
    // 那个 div 里，查 DOM 会给出假阴性。
    expect(pty.unmounted).toBe(2)
  })
})

describe('头部写操作（与行菜单同一套注册表与确认档）', () => {
  it('停止按钮直接派发（无确认档），按 query 主机发', async () => {
    const w = await mountPage()
    api.sendDockerCmd.mockClear()

    await clickPageButton(w, '停止')
    expect(api.sendDockerCmd).toHaveBeenCalledWith('h2', {
      action: 'container:stop',
      target: 'mysql',
      options: { target: 'mysql' }
    })
  })

  it('删除按钮走确认弹窗（标准档），确认后按 query 主机派发', async () => {
    const w = await mountPage()
    api.sendDockerCmd.mockClear()

    await clickPageButton(w, '删除…')
    expect(api.sendDockerCmd).not.toHaveBeenCalled() // 标准档：先弹窗后派发
    await clickDialogButton(w, '删除')
    expect(api.sendDockerCmd).toHaveBeenCalledWith(
      'h2',
      expect.objectContaining({ action: 'container:remove', target: 'mysql' })
    )
  })
})

/* ── QA 修正点：写指令成功后重读本页 inspect ─────────────────────────
 * 抽屉版只有「行数据 + 列表重拉」，watch 又只看 id/host —— 点完停止，抽屉自己的
 * 概览/头部停在旧状态上。整页化后本页是唯一表面，refresh 必须重读 inspect
 * （useDockerCmds 的 refresh 立即 + 落定各一次，与旧详情页的 reloadAll 同源）。
 */
describe('写指令成功后的状态重读（QA 修正：页面不能带着旧状态往下走）', () => {
  it('停止成功后重读 inspect，页面状态跟着 agent 的事实走', async () => {
    const w = await mountPage()
    expect(inspectCalls()).toHaveLength(1)

    // 停止受理 → 成功；此后 inspect 回来的是 stopped（agent 侧状态已变）。
    api.sendDockerCmd.mockImplementation(async (_hostId, body: { action: string }) => ({
      ref: body.action === 'container:stop' ? 'stop-ref' : 'r1'
    }))
    api.fetchDockerCmdResult.mockImplementation(async (_hostId, ref: string) =>
      ref === 'stop-ref'
        ? { status: 'succeeded', detail: '已停止容器' }
        : { status: 'succeeded', payload: { ...INSPECT_PAYLOAD, state: 'exited', exit_code: 0 } }
    )

    await clickPageButton(w, '停止')
    // 立即重读那一次（落定 1.5s 那一次在 composables.test 已钉）。
    expect(inspectCalls(), '写操作成功后应重读本页 inspect').toHaveLength(2)
    expect(w.text()).toContain('已退出（退出码 0）')
    expect(w.find('.cd-hero__title').text()).toBe('mysql')
  })

  it('删除成功后回容器列表（页面已不存在，留在原地只会给一片读不到的事实）', async () => {
    const w = await mountPage()
    api.sendDockerCmd.mockImplementation(async () => ({ ref: 'rm-ref' }))
    api.fetchDockerCmdResult.mockResolvedValue({ status: 'succeeded', detail: '已删除容器' })

    await clickPageButton(w, '删除…')
    await clickDialogButton(w, '删除')
    await flushStream(2)
    expect(currentRouter!.currentRoute.value.path).toBe('/docker/containers')
    expect(currentRouter!.currentRoute.value.query.host).toBe('h2')
  })
})

describe('环境 Tab（明文 env/labels/入口点/命令）', () => {
  it('复用概览的同一次 inspect：切过去不再发指令，env/labels/入口点/命令都在', async () => {
    const w = await mountPage()
    api.sendDockerCmd.mockClear()

    await switchTab(w, 'env')

    // 数据源纪律：环境 Tab 不重新拉 —— 概览打开时的那一次 inspect 就是全部事实。
    expect(api.sendDockerCmd).not.toHaveBeenCalledWith(
      'h2',
      expect.objectContaining({ action: 'container:inspect' })
    )
    const text = w.text()
    // 风险提示行（明文口径的告示牌）。
    expect(text).toContain('环境变量常含口令类变量')
    // 三个块标题 + 计数（jsdom 里 ElTable 不渲染行单元格：行数据从 ArtTable 的入参断言）。
    expect(text).toContain('环境变量（2 项）')
    expect(text).toContain('标签（1 项）')
    expect(text).toContain('入口点与命令')
    // 入口点/命令是等宽 code 块（不经过表格），正文直接可见。
    expect(text).toContain('docker-entrypoint.sh')
    expect(text).toContain('mysqld')
  })

  it('env/labels 行进了 ArtTable（值里的 = 只按第一个切：口令值完整保留）', async () => {
    const w = await mountPage()
    await switchTab(w, 'env')

    // 两张 ArtTable（env / labels）；jsdom 不渲染行单元格，取传给表格的数据。
    const tables = w.findAllComponents({ name: 'ArtTable' })
    const datas = tables.map((t) => {
      const vm = t.vm as unknown as {
        $attrs: Record<string, unknown>
        $props: Record<string, unknown>
      }
      return (vm.$props.data ?? vm.$attrs.data ?? []) as { name: string; value: string }[]
    })
    expect(datas[0]).toEqual([
      { name: 'MYSQL_ROOT_PASSWORD', value: 'secret' },
      { name: 'PATH', value: '/usr/sbin:/usr/bin:/sbin' }
    ])
    expect(datas[1]).toEqual([{ name: 'com.docker.compose.project', value: 'uni-center' }])
  })

  it('inspect 失败：环境 Tab 给结论句 + 重试，不弹空表', async () => {
    api.fetchDockerCmdResult.mockResolvedValue({ status: 'failed', error: '没有找到这个容器' })
    const w = await mountPage({ host: 'h2', tab: 'env' })
    expect(w.text()).toContain('没有找到这个容器')
    expect(w.text()).not.toContain('环境变量（')
  })
})

/* ── 主机跟随（照抄旧详情页先例：query host 是事实源）─────────────── */
describe('主机跟随：query host 落定/切换 → 重读 + 断流', () => {
  it('query 里没有 host 时落到第一台并写回 query（模块主机上下文约定）', async () => {
    await mountPage({})
    await flushStream(2)
    expect(currentRouter!.currentRoute.value.query.host).toBe('h1')
    expect(api.sendDockerCmd).toHaveBeenCalledWith(
      'h1',
      expect.objectContaining({ action: 'container:inspect' })
    )
  })

  it('切主机：断跟随流 + 按新主机重读 inspect', async () => {
    const w = await mountPage({ host: 'h2', tab: 'logs' })
    const viewer = w.findComponent({ name: 'LogViewer' })
    viewer.vm.$emit('update:following', true)
    await flushStream(2)
    const signal = api.openDockerLogStream.mock.calls[0]![2] as AbortSignal

    await currentRouter!.push({ path: `/docker/containers/${CID}`, query: { host: 'h1' } })
    await flushStream(2)

    expect(signal.aborted, '换主机应断开属于旧主机的跟随流').toBe(true)
    expect(api.sendDockerCmd).toHaveBeenCalledWith(
      'h1',
      expect.objectContaining({ action: 'container:inspect', target: CID })
    )
  })

  it('详情页 authMark 与 inspect 同档：页面自己按 docker:inspect 判日志跟随', async () => {
    auth.allow = new Set(['docker:manage', 'docker:delete'])
    const w = await mountPage()
    const viewer = w.findComponent({ name: 'LogViewer' })
    // followable 为假 = 无权限不给跟随开关（不渲染 ≠ 禁用）。
    expect(viewer.props('followable')).toBe(false)
  })
})
