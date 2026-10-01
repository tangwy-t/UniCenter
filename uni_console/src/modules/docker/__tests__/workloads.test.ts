// @vitest-environment jsdom
/**
 * 跨主机统一工作负载表（切片 2）的页面行为：多主机渲染、服务端筛选参数、
 * 行操作按行主机派发、?host= 深链兼容、D-3 权限门控、抽屉入口，以及 7b 的
 * ?id= 深链抽屉外部打开模式（行桩 + inspect 兜底）。
 *
 * 挂载方式沿用 page-render.test.ts 的口径（mock ../api 与 useAuth，Art* 用
 * 轻量替身保留 slot）；行操作不走 DOM 点击 —— jsdom 里 ElTable 不渲染
 * formatter 单元格，故对 WorkloadTable 组件实例 emit menu-select / open-detail
 * （页面 handler 链路照常走到指令通道），列与菜单条目层面的事实从 columns
 * 的 formatter vnode 断言。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'

const api = vi.hoisted(() => ({
  fetchDockerHosts: vi.fn(),
  fetchDockerContainers: vi.fn(),
  sendDockerCmd: vi.fn(),
  fetchDockerCmdResult: vi.fn(),
  openDockerLogStream: vi.fn()
}))
vi.mock('../api', () => ({ ...api, default: undefined }))

// hasAuth 的可变桩：D-3 用例要单独关掉 docker:inspect。
const auth = vi.hoisted(() => ({ allow: new Set<string>() }))
vi.mock('@/hooks/core/useAuth', () => ({
  useAuth: () => ({
    hasAuth: (perm: string) => auth.allow.has(perm),
    hasAnyAuth: () => true
  })
}))

import Containers from '../views/containers.vue'
import type { DockerWorkloadItem } from '../api'

const HOSTS = {
  list: [
    { id: 'h1', hostname: 'bogon', primaryIp: '192.168.12.105', online: true, dockerOk: true },
    { id: 'h2', hostname: 'nas', primaryIp: '192.168.12.106', online: true, dockerOk: true }
  ]
}

const row = (over: Partial<DockerWorkloadItem> = {}): DockerWorkloadItem => ({
  id: 'c1',
  name: 'uni-center-core',
  image: 'uni-center-core:latest',
  state: 'running',
  statusText: 'Up 16 hours',
  cpuPercent: 0.6,
  memUsageMb: 91,
  memLimitMb: 1024,
  netRxBytesSec: 0,
  netTxBytesSec: 0,
  protected: false,
  hostId: 'h1',
  hostname: 'bogon',
  ...over
})

const ALL = {
  items: [row(), row({ id: 'c2', name: 'mysql', hostId: 'h2', hostname: 'nas' })],
  total: 2
}

/** Art* 全局组件替身：保留 slot（同 page-render.test.ts）。 */
const passthrough = (name: string) =>
  defineComponent({
    name,
    setup:
      (_, { slots }) =>
      () =>
        h('div', { 'data-stub': name }, [slots.default?.(), slots.left?.(), slots.table?.()])
  })

const STUBS = {
  ArtTable: passthrough('ArtTable'),
  ArtTableHeader: passthrough('ArtTableHeader'),
  ArtSearchBar: passthrough('ArtSearchBar'),
  ArtButtonTable: passthrough('ArtButtonTable'),
  ArtButtonMore: passthrough('ArtButtonMore'),
  ArtSvgIcon: passthrough('ArtSvgIcon')
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
  auth.allow = new Set([
    'docker:list',
    'docker:inspect',
    'docker:manage',
    'docker:delete',
    'docker:exec'
  ])
  api.fetchDockerHosts.mockResolvedValue(HOSTS)
  api.fetchDockerContainers.mockResolvedValue(ALL)
  api.sendDockerCmd.mockResolvedValue({ ref: 'r1' })
  api.fetchDockerCmdResult.mockResolvedValue({ status: 'succeeded' })
})

/** 最近一次 mountPage 用的路由（?id 深链关抽屉清 query 的断言读它）。 */
let currentRouter: ReturnType<typeof createRouter> | null = null

async function mountPage(query: Record<string, string> = {}): Promise<VueWrapper> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/docker/containers', component: Containers }
    ]
  })
  currentRouter = router
  await router.push({ path: '/docker/containers', query })
  await router.isReady()
  const w = mount(Containers, { global: { plugins: [router], stubs: STUBS } })
  mounted.push(w)
  await new Promise((r) => setTimeout(r, 0))
  await nextTick()
  return w
}

/** 深链抽屉的落定：首拉 ready 后 watch 才开抽屉，多等一轮宏任务 + 渲染。 */
async function flushDeepLink() {
  await new Promise((r) => setTimeout(r, 0))
  await nextTick()
  await new Promise((r) => setTimeout(r, 0))
  await nextTick()
}

/** 页面 hero 的刷新按钮（ArtButtonTable 替身，title 经 attrs fallthrough 到根 div）。 */
function findRefreshButton(w: VueWrapper) {
  const btn = w
    .findAllComponents({ name: 'ArtButtonTable' })
    .find((b) => b.attributes('title') === '刷新')
  expect(btn, 'hero 上应有一个「刷新」按钮').toBeTruthy()
  return btn!
}

/** 页面传给 ArtTable 的行（替身把 data 落在 $attrs）。 */
function tableRows(w: VueWrapper): Record<string, unknown>[] {
  const table = w.findComponent({ name: 'ArtTable' })
  return ((table.vm as unknown as { $attrs: Record<string, unknown> }).$attrs.data ?? []) as Record<
    string,
    unknown
  >[]
}

/** 操作列的 formatter vnode：children = [详情按钮 vnode | null, ⋯ 菜单 vnode]。 */
function operationVNodes(w: VueWrapper, row: DockerWorkloadItem) {
  const table = w.findComponent({ name: 'ArtTable' })
  const columns = ((table.vm as unknown as { $attrs: Record<string, unknown> }).$attrs.columns ??
    []) as { prop?: string; formatter?: (row: never) => unknown }[]
  const op = columns.find((c) => c.prop === 'operation')
  expect(op?.formatter, '页面没有把操作列 formatter 传给 ArtTable').toBeTruthy()
  const vnode = op!.formatter!(row as never) as { children: unknown[] }
  const children = (Array.isArray(vnode.children) ? vnode.children : [vnode.children]) as {
    type?: { name?: string }
    props?: Record<string, unknown>
  }[]
  return children
}

describe('统一表渲染（多主机条目）', () => {
  it('两台主机的容器都在同一张表里，行带 hostId/hostname', async () => {
    const w = await mountPage()
    const rows = tableRows(w)
    expect(rows).toHaveLength(2)
    expect(rows[0]).toMatchObject({ hostId: 'h1', hostname: 'bogon' })
    expect(rows[1]).toMatchObject({ hostId: 'h2', hostname: 'nas' })
  })

  it('服务端截断时计数说出口（total 全量 + 前 N 条）', async () => {
    api.fetchDockerContainers.mockResolvedValue({ items: ALL.items, total: 640 })
    const w = await mountPage()
    expect(w.html()).toContain('共 640 个容器')
    expect(w.html()).toContain('显示前 2 条')
  })
})

describe('筛选参数全部发给端点（服务端过滤）', () => {
  it('搜索时 keyword/state/hostId 三项透传', async () => {
    const w = await mountPage()
    api.fetchDockerContainers.mockClear()

    const bar = w.findComponent({ name: 'ArtSearchBar' })
    bar.vm.$emit('update:modelValue', { keyword: 'mysql', state: 'stopped', host: 'h2' })
    bar.vm.$emit('search')
    await new Promise((r) => setTimeout(r, 0))

    expect(api.fetchDockerContainers).toHaveBeenCalledWith({
      hostId: 'h2',
      state: 'stopped',
      keyword: 'mysql'
    })
  })

  it('重置清空三项参数（undefined 不发）', async () => {
    const w = await mountPage({ host: 'h2' }) // 先带一个 host 深链
    const bar = w.findComponent({ name: 'ArtSearchBar' })
    bar.vm.$emit('update:modelValue', {})
    bar.vm.$emit('reset')
    await new Promise((r) => setTimeout(r, 0))

    expect(api.fetchDockerContainers).toHaveBeenLastCalledWith({
      hostId: undefined,
      state: undefined,
      keyword: undefined
    })
  })
})

describe('?host= 深链兼容（总览主机卡片 / 详情页返回的既有链路）', () => {
  it('进入时作为主机筛选初始值：首拉就带 hostId', async () => {
    await mountPage({ host: 'h2' })
    expect(api.fetchDockerContainers).toHaveBeenCalledWith(
      expect.objectContaining({ hostId: 'h2' })
    )
  })

  it('不带 host 的进入是全主机首拉', async () => {
    await mountPage()
    expect(api.fetchDockerContainers).toHaveBeenCalledWith(
      expect.objectContaining({ hostId: undefined })
    )
  })
})

describe('行操作按行主机派发', () => {
  it('h2 行的菜单动作发到 h2 的指令通道', async () => {
    const w = await mountPage()
    api.sendDockerCmd.mockClear()

    const table = w.findComponent({ name: 'DockerWorkloadTable' })
    table.vm.$emit('menu-select', { row: ALL.items[1], key: 'container:stop' })
    await new Promise((r) => setTimeout(r, 0))

    expect(api.sendDockerCmd).toHaveBeenCalledWith(
      'h2',
      expect.objectContaining({ action: 'container:stop', target: 'mysql' })
    )
  })

  it('行菜单「日志」不再跳详情页：原地打开抽屉（落在日志 Tab）', async () => {
    const w = await mountPage()
    const table = w.findComponent({ name: 'DockerWorkloadTable' })
    table.vm.$emit('menu-select', { row: ALL.items[1], key: 'logs' })
    await nextTick()

    const drawer = w.findComponent({ name: 'DockerWorkloadDrawer' })
    expect(drawer.exists()).toBe(true)
    expect((drawer.props() as { modelValue: boolean }).modelValue).toBe(true)
    expect((drawer.props() as { initialTab: string }).initialTab).toBe('logs')
    expect((drawer.props() as { row: { hostId: string } }).row.hostId).toBe('h2')
  })

  it('详情按钮打开抽屉落概览 Tab', async () => {
    const w = await mountPage()
    const table = w.findComponent({ name: 'DockerWorkloadTable' })
    table.vm.$emit('open-detail', ALL.items[0])
    await nextTick()

    const drawer = w.findComponent({ name: 'DockerWorkloadDrawer' })
    expect((drawer.props() as { modelValue: boolean }).modelValue).toBe(true)
    expect((drawer.props() as { initialTab: string }).initialTab).toBe('overview')
  })
})

describe('D-3：无 docker:inspect 权限时不再看到死项', () => {
  it('操作列不渲染详情按钮，行菜单的日志项带 auth 门控', async () => {
    auth.allow = new Set(['docker:list', 'docker:manage', 'docker:delete', 'docker:exec'])
    const w = await mountPage()

    const children = operationVNodes(w, ALL.items[0])
    // 详情按钮（ArtButtonTable）不渲染：hasAuth(inspect)=false → null。
    expect(children.some((c) => c?.type?.name === 'ArtButtonTable')).toBe(false)

    // 「日志」条目带 auth=docker:inspect：ArtButtonMore 真组件按 auth 过滤条目
    // （无权限不出现），死项问题在条目源头被关掉。
    const more = children.find((c) => c?.type?.name === 'ArtButtonMore')
    const list = (more?.props?.list ?? []) as { key: string; auth?: string }[]
    const logs = list.find((i) => i.key === 'logs')
    expect(logs?.auth).toBe('docker:inspect')
    // 写动作的 auth 来自注册表（perm 码），门控口径不变。
    expect(list.find((i) => i.key === 'container:stop')?.auth).toBe('docker:manage')
  })

  it('有权限时详情按钮渲染、日志项照旧', async () => {
    const w = await mountPage()
    const children = operationVNodes(w, ALL.items[0])
    expect(children.some((c) => c?.type?.name === 'ArtButtonTable')).toBe(true)
  })
})

describe('首拉失败的口径（统一表自己的 D-1 对应面）', () => {
  it('首拉失败：整页错误态 + 重试按钮；刷新失败：保留最后已知数据并标注', async () => {
    api.fetchDockerContainers.mockRejectedValueOnce(new Error('network down'))
    const w = await mountPage()
    expect(w.html()).toContain('容器清单获取失败')

    // 重试成功 → 回到表格
    api.fetchDockerContainers.mockResolvedValue(ALL)
    await w
      .findAll('button')
      .find((b) => b.text().includes('重试'))!
      .trigger('click')
    await new Promise((r) => setTimeout(r, 0))
    await nextTick()
    expect(tableRows(w)).toHaveLength(2)

    // 此后的刷新失败（已握有数据）：表格不清空，页头标注「本次刷新失败」
    api.fetchDockerContainers.mockRejectedValueOnce(new Error('network down'))
    await findRefreshButton(w).trigger('click')
    await new Promise((r) => setTimeout(r, 0))
    await nextTick()
    expect(tableRows(w)).toHaveLength(2)
    expect(w.html()).toContain('上次数据仍在，本次刷新失败')
  })
})

/* ── ?id= 深链 → 抽屉外部打开（7b：被删 container-detail 路由的替代形态）──────
 * overview 异常表 / 工作台容器行 / 镜像详情关联容器的深链统一改指本页 + query。
 * 两条路径都要钉住：目标行在视图里给全行（保护标记/状态句都在），不在就退到
 * 行桩（id/hostId/hostname 三个事实），其余由抽屉里的 inspect 兜底填充。
 */
describe('?id= 深链：统一表页打开详情抽屉（外部打开模式）', () => {
  it('目标行在视图里 → 抽屉拿全行（含主机筛选与保护标记）', async () => {
    await mountPage({ host: 'h2', id: 'c2' })
    await flushDeepLink()

    const drawer = w0().findComponent({ name: 'DockerWorkloadDrawer' })
    expect((drawer.props() as { modelValue: boolean }).modelValue).toBe(true)
    expect((drawer.props() as { initialTab: string }).initialTab).toBe('overview')
    const row = (drawer.props() as { row: DockerWorkloadItem }).row
    expect(row.id).toBe('c2')
    expect(row.hostId).toBe('h2')
    expect(row.name).toBe('mysql')
  })

  it('目标行不在视图里 → 行桩打开，inspect 兜底填充名称与状态', async () => {
    // cZ 不在首拉结果里（被筛掉/截断之外的形态）：行桩只有 id/hostId/hostname。
    api.fetchDockerCmdResult.mockResolvedValue({
      status: 'succeeded',
      payload: { name: 'redis', image: 'redis:7', state: 'running' }
    })
    await mountPage({ host: 'h2', id: 'cZ' })
    await flushDeepLink()

    const drawer = w0().findComponent({ name: 'DockerWorkloadDrawer' })
    expect((drawer.props() as { modelValue: boolean }).modelValue).toBe(true)
    const row = (drawer.props() as { row: DockerWorkloadItem }).row
    expect(row).toMatchObject({ id: 'cZ', hostId: 'h2', hostname: 'nas', name: '' })

    // 抽屉按行桩发 inspect（host = 行主机），并把返回的名称/状态展示出来。
    expect(api.sendDockerCmd).toHaveBeenCalledWith('h2', {
      action: 'container:inspect',
      target: 'cZ',
      options: {}
    })
    // 抽屉 append-to-body：内容 teleport 到 body（workload-drawer.test 同款口径）。
    expect(document.body.innerHTML).toContain('redis')
    expect(document.body.innerHTML).toContain('运行中')
  })

  it('没有 docker:inspect 权限时不开抽屉（与被删路由的 authMark 同档）', async () => {
    auth.allow = new Set(['docker:list', 'docker:manage', 'docker:delete'])
    const w = await mountPage({ host: 'h1', id: 'c1' })
    await flushDeepLink()

    const drawer = w.findComponent({ name: 'DockerWorkloadDrawer' })
    expect((drawer.props() as { modelValue: boolean }).modelValue).toBe(false)
  })

  it('关抽屉清掉深链 id（host 留作筛选初始值）：刷新不再重开抽屉', async () => {
    await mountPage({ host: 'h2', id: 'c2' })
    await flushDeepLink()

    const drawer = w0().findComponent({ name: 'DockerWorkloadDrawer' })
    drawer.vm.$emit('update:modelValue', false)
    await flushDeepLink()

    expect(currentRouter!.currentRoute.value.query.id).toBeUndefined()
    expect(currentRouter!.currentRoute.value.query.host).toBe('h2')
    // 关掉后模型值也回到关闭态（v-model 的另一半）。
    expect((drawer.props() as { modelValue: boolean }).modelValue).toBe(false)
  })
})

/** 深链用例里挂着抽屉的页面（mountPage 只返回 w，收口一个小取值器）。 */
function w0(): VueWrapper {
  const w = mounted[mounted.length - 1]
  expect(w, '深链用例应已挂载页面').toBeTruthy()
  return w!
}
