// @vitest-environment jsdom
/**
 * 工作负载抽屉（切片 2）：四个 Tab 的懒挂载与断流纪律，以及「host 取行主机」。
 *
 * 为什么这些用例要存在：抽屉把详情页的三块能力（inspect / 日志 / 终端）搬进列表页，
 * 生命周期纪律（首切才拉、切走断流、关抽屉断流）靠复制粘贴最容易走样 ——
 * 这里把「什么时候发指令 / 什么时候断流」钉住。行主机是统一表的新事实：
 * 抽屉没有 provide 主机上下文，所有指令都必须带行自己的 hostId。
 *
 * 7b 两个新面：环境 Tab（被删详情页「环境变量与配置」的平移，复用同一次 inspect）
 * 与行桩（?id 深链的外部打开：名称/状态/镜像由 inspect 兜底填充）。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'

const api = vi.hoisted(() => ({
  sendDockerCmd: vi.fn(),
  fetchDockerCmdResult: vi.fn(),
  fetchDockerStatsHistory: vi.fn(),
  openDockerLogStream: vi.fn(),
  openDockerStatsStream: vi.fn()
}))
vi.mock('../api', () => ({ ...api, default: undefined }))

// 终端懒挂载桩：真实 pty-terminal 会拉 xterm（重），这里只要「挂没挂」的事实。
// （__isTeleport 等 vue 内部标记一并给出：defineAsyncComponent 的 loader 会按
// 具名导出探测模块形态，mock 工厂缺了它们会抛「No export is defined」。）
const pty = vi.hoisted(() => ({ mounted: 0 }))
vi.mock('../components/pty-terminal.vue', () => ({
  default: defineComponent({
    name: 'PtyTerminalStub',
    setup() {
      pty.mounted += 1
      return () => h('div', { 'data-stub': 'PtyTerminal' })
    }
  }),
  // vue/vitest 对 SFC mock 会逐个探测模块形态标记与具名导出（缺一个抛一个）；
  // __esModule 让 defineAsyncComponent 的 loader 走 mod.default 分支。
  __esModule: true,
  name: 'PtyTerminalStub',
  __isTeleport: false,
  __isKeepAlive: false,
  __isSuspense: false
}))

vi.mock('@/hooks/core/useAuth', () => ({
  useAuth: () => ({ hasAuth: () => true, hasAnyAuth: () => true })
}))

import WorkloadDrawer from '../components/workload-drawer.vue'
import type { DockerWorkloadItem } from '../api'

const ROW: DockerWorkloadItem = {
  id: 'abcdef1234567890',
  name: 'mysql',
  image: 'mysql:8',
  state: 'running',
  statusText: 'Up 16 hours',
  cpuPercent: 1,
  memUsageMb: 200,
  memLimitMb: 1024,
  netRxBytesSec: 0,
  netTxBytesSec: 0,
  protected: false,
  hostId: 'h2',
  hostname: 'nas'
}

const INSPECT_PAYLOAD = {
  name: 'mysql',
  image: 'mysql:8',
  created: 1790000000,
  started_at: 1790000100,
  restart_policy: 'always',
  networks: ['bridge'],
  mounts: [{ type: 'volume', source: 'dbdata', destination: '/var/lib/mysql', rw: true }],
  env: ['MYSQL_ROOT_PASSWORD=secret', 'PATH=/usr/sbin:/usr/bin:/sbin'],
  labels: { 'com.docker.compose.project': 'uni-center' },
  entrypoint: ['docker-entrypoint.sh'],
  cmd: ['mysqld']
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

const mounted: VueWrapper[] = []
afterEach(() => {
  for (const w of mounted.splice(0)) w.unmount()
  vi.clearAllMocks()
  vi.unstubAllGlobals()
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
  api.sendDockerCmd.mockResolvedValue({ ref: 'r1' })
  api.fetchDockerCmdResult.mockResolvedValue({ status: 'succeeded', payload: INSPECT_PAYLOAD })
  // stats 历史默认空（= 纯实时，现状行为）；要历史的用例用 mockResolvedValueOnce。
  api.fetchDockerStatsHistory.mockResolvedValue({ samples: [] })
  api.openDockerLogStream.mockResolvedValue(fakeStreamResponse())
  api.openDockerStatsStream.mockResolvedValue(fakeStreamResponse())
})

async function mountDrawer(
  initialTab: 'overview' | 'logs' | 'pty' | 'env' = 'overview',
  row: DockerWorkloadItem = ROW
) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/', component: { template: '<div />' } }]
  })
  // 先落定初始路由再 isReady：没有初始导航时 isReady() 永不落定（页面渲染用例同款）。
  await router.push('/')
  await router.isReady()
  const w = mount(WorkloadDrawer, {
    props: { modelValue: true, row, initialTab },
    global: {
      plugins: [router],
      // ArtTable（环境 Tab 的 env/labels 两张表）依赖 Pinia 的表格 store —— 单组件
      // 挂载没有应用实例给 store，用透传替身（data/columns 落 $attrs，用例从那里断言）。
      stubs: {
        ArtTable: defineComponent({
          name: 'ArtTable',
          inheritAttrs: false,
          setup:
            (_, { slots }) =>
            () =>
              h('div', { 'data-stub': 'ArtTable' }, [slots.default?.()])
        })
      }
    }
  })
  mounted.push(w)
  await new Promise((r) => setTimeout(r, 0))
  await nextTick()
  return w
}

/** 抽屉 append-to-body：内容 teleport 到 body，w.html() 只剩锚点 —— 断言查 body。 */
const bodyHTML = () => document.body.innerHTML
const bodyButtons = () =>
  Array.from(document.body.querySelectorAll('button')).map((b) => ({
    el: b,
    text: (b.textContent ?? '').trim()
  }))

/** 切 Tab：ElTabs 是真组件，v-model 走 update:modelValue。 */
async function switchTab(w: VueWrapper, tab: string) {
  const tabs = w.findComponent({ name: 'ElTabs' })
  expect(tabs.exists(), '抽屉里应渲染 ElTabs').toBe(true)
  tabs.vm.$emit('update:modelValue', tab)
  await nextTick()
  await new Promise((r) => setTimeout(r, 0))
  await nextTick()
}

describe('概览 Tab（container:inspect，host 取行主机）', () => {
  it('打开抽屉即按行主机读 inspect，展示载荷字段（含 snake_case 折叠）', async () => {
    await mountDrawer()
    expect(api.sendDockerCmd).toHaveBeenCalledWith('h2', {
      action: 'container:inspect',
      target: 'abcdef1234567890',
      options: {}
    })
    expect(bodyHTML()).toContain('mysql:8')
    // snake_case → camelCase 的边界折叠：没折叠这些字段会静默空白。
    expect(bodyHTML()).toContain('always')
    expect(bodyHTML()).toContain('dbdata → /var/lib/mysql')
  })

  it('头部给出容器名、短 id 与行归属主机', async () => {
    await mountDrawer()
    expect(bodyHTML()).toContain('mysql')
    expect(bodyHTML()).toContain('abcdef123456')
    expect(bodyHTML()).toContain('@ nas')
  })
})

describe('日志 Tab（懒挂载 + Follow 断流）', () => {
  it('概览 Tab 停留时不发 container:logs；首次切到才拉', async () => {
    const w = await mountDrawer()
    expect(api.sendDockerCmd).not.toHaveBeenCalledWith(
      'h2',
      expect.objectContaining({ action: 'container:logs' })
    )
    await switchTab(w, 'logs')
    expect(api.sendDockerCmd).toHaveBeenCalledWith('h2', {
      action: 'container:logs',
      target: 'abcdef1234567890',
      options: { tail: 100 }
    })
  })

  it('Follow 建立流；切走 Tab 即断流（AbortController abort）', async () => {
    const w = await mountDrawer('logs')
    const viewer = w.findComponent({ name: 'LogViewer' })
    expect(viewer.exists()).toBe(true)
    // 打开跟随开关：LogViewer 的 v-model:following 翻转 → 抽屉建会话 + 接流。
    viewer.vm.$emit('update:following', true)
    await new Promise((r) => setTimeout(r, 0))
    await nextTick()

    expect(api.sendDockerCmd).toHaveBeenCalledWith('h2', {
      action: 'container:logs',
      target: 'abcdef1234567890',
      options: { tail: 100, follow: true }
    })
    expect(api.openDockerLogStream).toHaveBeenCalledTimes(1)
    const signal = api.openDockerLogStream.mock.calls[0]![2] as AbortSignal

    await switchTab(w, 'overview')
    expect(signal.aborted, '切走日志 Tab 应 abort 跟随流').toBe(true)
  })

  it('关抽屉断流：会话生命周期 = 抽屉打开期', async () => {
    const w = await mountDrawer('logs')
    const viewer = w.findComponent({ name: 'LogViewer' })
    viewer.vm.$emit('update:following', true)
    await new Promise((r) => setTimeout(r, 0))
    await nextTick()
    const signal = api.openDockerLogStream.mock.calls[0]![2] as AbortSignal

    await w.setProps({ modelValue: false })
    await nextTick()
    expect(signal.aborted, '关抽屉应 abort 跟随流').toBe(true)
  })
})

describe('概览 Tab · 实时统计（container:stats 流，切片 3a）', () => {
  it('概览 Tab 激活即按行主机发 container:stats 并接入流；首帧样本落到读数与曲线', async () => {
    api.openDockerStatsStream.mockResolvedValueOnce(
      statsStreamResponse([statsLine(1790000000000), statsLine(1790000001000)])
    )
    await mountDrawer()
    expect(api.sendDockerCmd).toHaveBeenCalledWith('h2', {
      action: 'container:stats',
      target: 'abcdef1234567890'
    })
    expect(api.openDockerStatsStream).toHaveBeenCalledWith('h2', 'r1', expect.any(AbortSignal))
    await flushStream()
    expect(bodyHTML()).toContain('实时统计')
    // 读数 = 曲线的直标通道（数值带单位、内存附上限与占比）。
    expect(bodyHTML()).toContain('3.5%')
    expect(bodyHTML()).toContain('200 MB')
    expect(bodyHTML()).toContain('上限 1 GB · 20%')
    // 曲线本体：折线 path 与端点点标记都吃到了样本。
    expect(document.querySelector('.sc-line')).toBeTruthy()
    expect(document.querySelector('.sc-dot')).toBeTruthy()
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
    await mountDrawer()
    await flushStream()
    expect(bodyHTML()).toContain('3.5%')
    // CPU 与内存两张小图至少各一条折线（网络组若也被折叠面板渲染则更多）。
    expect(document.querySelectorAll('.sc-line').length).toBeGreaterThanOrEqual(2)
  })

  it('eof 行收尾：给状态句而不是错误', async () => {
    api.openDockerStatsStream.mockResolvedValueOnce(
      statsStreamResponse([statsLine(1790000000000), statsLine(1790000001000, { eof: true })])
    )
    await mountDrawer()
    await flushStream()
    // eof 挂在末样本行上：样本入窗且流标记已结束。
    expect(bodyHTML()).toContain('3.5%')
    expect(bodyHTML()).toContain('统计流已结束')
    expect(bodyHTML()).not.toContain('统计流已断开')
  })

  it('切走 Tab 即断流（AbortController abort）', async () => {
    const w = await mountDrawer()
    await flushStream(2)
    expect(api.openDockerStatsStream).toHaveBeenCalledTimes(1)
    const signal = api.openDockerStatsStream.mock.calls[0]![2] as AbortSignal

    await switchTab(w, 'logs')
    expect(signal.aborted, '切走概览 Tab 应 abort 统计流').toBe(true)
  })

  it('关抽屉断流：会话生命周期 = 抽屉打开期', async () => {
    const w = await mountDrawer()
    await flushStream(2)
    const signal = api.openDockerStatsStream.mock.calls[0]![2] as AbortSignal

    await w.setProps({ modelValue: false })
    await nextTick()
    expect(signal.aborted, '关抽屉应 abort 统计流').toBe(true)
  })

  it('换行即换流：旧流 abort，新指令带新容器 id', async () => {
    const w = await mountDrawer()
    await flushStream(2)
    const signal = api.openDockerStatsStream.mock.calls[0]![2] as AbortSignal

    await w.setProps({ row: { ...ROW, id: 'fedcba0987654321', name: 'redis' } })
    await flushStream(2)
    expect(signal.aborted, '换行应 abort 旧统计流').toBe(true)
    expect(api.sendDockerCmd).toHaveBeenCalledWith('h2', {
      action: 'container:stats',
      target: 'fedcba0987654321'
    })
    expect(api.openDockerStatsStream).toHaveBeenCalledTimes(2)
  })

  it('未运行的行不发起流（空转指令只会换回一个错误结论），并给出解释句', async () => {
    await mountDrawer('overview', { ...ROW, state: 'exited', statusText: 'Exited (0)' })
    await flushStream(2)
    expect(api.sendDockerCmd).not.toHaveBeenCalledWith(
      'h2',
      expect.objectContaining({ action: 'container:stats' })
    )
    // 历史 GET 同一道门：不运行的容器没有可回看的曲线，空请求只会换回错误。
    expect(api.fetchDockerStatsHistory).not.toHaveBeenCalled()
    expect(api.openDockerStatsStream).not.toHaveBeenCalled()
    expect(bodyHTML()).toContain('容器未运行，暂无实时统计')
  })
})

describe('概览 Tab · 实时统计 · 历史预填（P2 · stats-history 半边）', () => {
  /** 按 y 轴量纲取一张曲线（CPU=% / 内存=MB / 网络=B/s；网络组折叠时也在 DOM）。 */
  function chartByUnit(w: VueWrapper, unit: string) {
    const chart = w
      .findAllComponents({ name: 'DockerStatsChart' })
      .find((c) => c.props('unit') === unit)
    expect(chart, `应渲染 unit=${unit} 的 stats 曲线`).toBeTruthy()
    return chart!
  }

  it('打开即先拉历史（行主机 + 容器 id）再受理 container:stats —— 顺序不许倒', async () => {
    await mountDrawer()
    // 先有形状（历史预填），再接当下（实时流受理）：倒过来用户就要对着空白
    // 曲线干等建流。invocationCallOrder 钉的是先后，不是各自调没调。
    expect(api.fetchDockerStatsHistory).toHaveBeenCalledWith('h2', 'abcdef1234567890')
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
    const w = await mountDrawer()
    await flushStream()

    // CPU 曲线的时刻序列 = 两枚历史 + 去重后的两枚流帧，t 严格递增 —— 用户
    // 看到的是一条连续序列，感知不到拼接缝。
    expect(chartByUnit(w, '%').props('times')).toEqual([
      1789999970000, 1789999980000, 1789999990000
    ])
    // 同 t 处留的是流帧（网络字段在场）—— 网络曲线不吃历史、也不丢这一帧。
    expect(chartByUnit(w, 'B/s').props('times')).toEqual([1789999980000, 1789999990000])
    // 摘要句如实交代两段口径；折叠组里的说明句交代网络无历史。
    expect(bodyHTML()).toContain('历史约 30 分钟')
    expect(bodyHTML()).toContain('网络速率仅实时段')
  })

  it('历史拉取失败静默降级为纯实时：流照常建立，不给错误态', async () => {
    api.fetchDockerStatsHistory.mockRejectedValueOnce(new Error('history down'))
    api.openDockerStatsStream.mockResolvedValueOnce(statsStreamResponse([statsLine(1790000000000)]))
    const w = await mountDrawer()
    await flushStream()
    // 实时流没被历史拖下水：照常受理、照常接流、读数照常落地。
    expect(api.openDockerStatsStream).toHaveBeenCalledTimes(1)
    expect(bodyHTML()).toContain('3.5%')
    expect(chartByUnit(w, '%').props('times')).toEqual([1790000000000])
    // 错误态属于流的生命周期；回看失败只降级 —— 曲线回到纯实时口径。
    expect(bodyHTML()).not.toContain('实时统计连接失败')
    expect(bodyHTML()).toContain('最近约 2 分钟')
  })

  it('流端点打不开时历史仍在屏上（30 分钟回看不跟着流陪葬）', async () => {
    api.fetchDockerStatsHistory.mockResolvedValueOnce({
      samples: [{ t: 1789999970000, cpuPercent: 10, memUsageMb: 300, memLimitMb: 1024 }]
    })
    api.openDockerStatsStream.mockResolvedValueOnce({ ok: false, status: 404 })
    const w = await mountDrawer()
    await flushStream()
    // 404 = 会话过期的结论句；但曲线已经不是空白 —— 历史段先落了屏。
    expect(bodyHTML()).toContain('该统计会话已过期')
    expect(chartByUnit(w, '%').props('times')).toEqual([1789999970000])
  })
})

describe('终端 Tab（懒挂载）', () => {
  it('切到终端 Tab 才挂载 PtyTerminal，离开/关闭即卸载', async () => {
    const w = await mountDrawer()
    expect(pty.mounted, '概览停留时不应挂终端').toBe(0)

    await switchTab(w, 'pty')
    expect(pty.mounted, '切到终端 Tab 才建立会话').toBe(1)
    expect(document.querySelector('[data-stub="PtyTerminal"]')).toBeTruthy()

    await switchTab(w, 'overview')
    expect(
      document.querySelector('[data-stub="PtyTerminal"]'),
      '切走即卸载（组件卸载时发 cancel）'
    ).toBeNull()

    await switchTab(w, 'pty')
    await w.setProps({ modelValue: false })
    await nextTick()
    expect(document.querySelector('[data-stub="PtyTerminal"]'), '关抽屉即卸载终端会话').toBeNull()
  })
})

describe('头部写操作（与行菜单同一套注册表与确认档）', () => {
  it('停止按钮直接派发（无确认档），按行主机发', async () => {
    await mountDrawer()
    api.sendDockerCmd.mockClear()

    const stop = bodyButtons().find((b) => b.text === '停止')
    expect(stop).toBeTruthy()
    await stop!.el.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await new Promise((r) => setTimeout(r, 0))

    expect(api.sendDockerCmd).toHaveBeenCalledWith('h2', {
      action: 'container:stop',
      target: 'mysql',
      options: { target: 'mysql' }
    })
  })

  it('删除按钮走确认弹窗（标准档），确认后按行主机派发', async () => {
    await mountDrawer()
    api.sendDockerCmd.mockClear()

    const remove = bodyButtons().find((b) => b.text === '删除…')
    expect(remove).toBeTruthy()
    await remove!.el.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await nextTick()
    await new Promise((r) => setTimeout(r, 0))
    await nextTick()
    expect(api.sendDockerCmd).not.toHaveBeenCalled() // 标准档：先弹窗后派发

    // DockerActionConfirm 是真组件：标准档主按钮可点（无需逐字输入）。
    const confirm = bodyButtons().find(
      (b) => b.text === '删除' && !(b.el as HTMLButtonElement).disabled
    )
    expect(confirm, '确认弹窗主按钮应已渲染').toBeTruthy()
    await confirm!.el.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await new Promise((r) => setTimeout(r, 0))
    expect(api.sendDockerCmd).toHaveBeenCalledWith(
      'h2',
      expect.objectContaining({ action: 'container:remove', target: 'mysql' })
    )
  })
})

describe('环境 Tab（7b：被删详情页「环境变量与配置」的平移面）', () => {
  it('复用概览的同一次 inspect：切过去不再发指令，env/labels/入口点/命令都在', async () => {
    const w = await mountDrawer()
    api.sendDockerCmd.mockClear()

    await switchTab(w, 'env')

    // 数据源纪律：环境 Tab 不重新拉 —— 概览打开时的那一次 inspect 就是全部事实。
    expect(api.sendDockerCmd).not.toHaveBeenCalledWith(
      'h2',
      expect.objectContaining({ action: 'container:inspect' })
    )
    const html = bodyHTML()
    // 风险提示行（明文口径的告示牌）。
    expect(html).toContain('环境变量常含口令类变量')
    // 三个块标题 + 计数（jsdom 里 ElTable 不渲染行单元格：行数据从 ArtTable 的入参断言）。
    expect(html).toContain('环境变量（2 项）')
    expect(html).toContain('标签（1 项）')
    expect(html).toContain('入口点与命令')
    // 入口点/命令是等宽 code 块（不经过表格），正文直接可见。
    expect(html).toContain('docker-entrypoint.sh')
    expect(html).toContain('mysqld')
  })

  it('env/labels 行进了 ArtTable（值里的 = 只按第一个切：口令值完整保留）', async () => {
    const w = await mountDrawer()
    await switchTab(w, 'env')

    // 抽屉里两张 ArtTable（env / labels）；jsdom 不渲染行单元格，取传给表格的数据。
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
    await mountDrawer('env')
    await new Promise((r) => setTimeout(r, 0))
    await nextTick()

    expect(bodyHTML()).toContain('没有找到这个容器')
    expect(bodyHTML()).not.toContain('环境变量（')
  })
})

describe('行桩（7b：?id 深链的外部打开形态）', () => {
  /** 行桩：只有 id/hostId/hostname 三个事实（目标行不在统一表当前过滤视图里）。 */
  const STUB: DockerWorkloadItem = {
    ...ROW,
    id: 'c9f8e7d6c5b4a3',
    name: '',
    image: '',
    state: '',
    statusText: undefined
  }

  it('名称/状态/镜像由 inspect 兜底；写操作 target 也落到 inspect 的名字', async () => {
    api.fetchDockerCmdResult.mockResolvedValue({
      status: 'succeeded',
      payload: { ...INSPECT_PAYLOAD, name: 'redis', image: 'redis:7', state: 'running' }
    })
    await mountDrawer('overview', STUB)
    await new Promise((r) => setTimeout(r, 0))
    await nextTick()

    // inspect 按行桩的 id/host 发出（名字此时还不知道，target 只能是 id）。
    expect(api.sendDockerCmd).toHaveBeenCalledWith('h2', {
      action: 'container:inspect',
      target: 'c9f8e7d6c5b4a3',
      options: {}
    })
    const html = bodyHTML()
    expect(html).toContain('redis') // 头部标题（view.name）
    expect(html).toContain('运行中') // 状态结论（view.state）
    expect(html).toContain('redis:7') // 概览镜像（view.image）

    // inspect 到位后写按钮可用，且 target 用的是 inspect 回来的名字。
    api.sendDockerCmd.mockClear()
    const stop = bodyButtons().find((b) => b.text === '停止')
    expect(stop, 'inspect 已给名字，停止按钮应可用').toBeTruthy()
    await stop!.el.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await new Promise((r) => setTimeout(r, 0))
    expect(api.sendDockerCmd).toHaveBeenCalledWith('h2', {
      action: 'container:stop',
      target: 'redis',
      options: { target: 'redis' }
    })
  })

  it('inspect 没回来前头部不显示空标题（退化到短 id），写按钮禁用', async () => {
    // 悬挂的结果：轮询永不返回终态 → view 永远是 null。
    api.fetchDockerCmdResult.mockImplementation(
      () => new Promise(() => {}) as Promise<{ status: string }>
    )
    await mountDrawer('overview', STUB)
    await new Promise((r) => setTimeout(r, 0))
    await nextTick()

    // 标题退化到短 id（不显示空串），行归属主机照旧显示。
    expect(bodyHTML()).toContain('c9f8e7d6c5b4')
    expect(bodyHTML()).toContain('@ nas')
    // 状态未知（行桩 state 为空）→ 启停二选一落在「启动」。
    const start = bodyButtons().find((b) => b.text === '启动')
    expect(start).toBeTruthy()
    expect((start!.el as HTMLButtonElement).disabled, '没有名字前发指令只会拿到空目标结论').toBe(
      true
    )
  })
})
