// @vitest-environment jsdom
/**
 * 跨主机统一工作负载表（切片 2）的页面行为：多主机渲染、服务端筛选参数、
 * 行操作按行主机派发、?host= 深链兼容、D-3 权限门控，以及 8a/8b 页面化之后的
 * 详情/创建入口（一律整页路由）与 `?id=` 深链语义的删除（零兼容）。
 *
 * 挂载方式沿用 page-render.test.ts 的口径（mock ../api 与 useAuth，Art* 用
 * 轻量替身保留 slot）；行操作不走 DOM 点击 —— jsdom 里 ElTable 不渲染
 * formatter 单元格，故对 WorkloadTable 组件实例 emit menu-select / open-detail
 * （页面 handler 链路照常走到路由与指令通道），列与菜单条目层面的事实从 columns
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

/** 最近一次 mountPage 用的路由（详情/创建入口 push 的目标从它断言）。 */
let currentRouter: ReturnType<typeof createRouter> | null = null

async function mountPage(query: Record<string, string> = {}): Promise<VueWrapper> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/docker/containers', component: Containers },
      // 8a/8b 的目标页（本页只负责把 path + query 交出去，目标页自身的行为在
      // container-detail-page.test.ts / page-transition.test.ts 里钉）。
      { path: '/docker/containers/create', component: { template: '<div />' } },
      { path: '/docker/containers/:id', component: { template: '<div />' } },
      // 8c 任务中心（容器页 hero 入口移栽后的目标页）。
      { path: '/docker/tasks', component: { template: '<div />' } }
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

/** navigation 的落定：router.push 是异步的（内存历史也要过导航管线），
 *  nextTick 不够 —— 多给一轮宏任务。 */
async function flushNav() {
  await nextTick()
  await new Promise((r) => setTimeout(r, 0))
}

/** hero 簇里的图标钮（ArtButtonTable 替身，title 经 attrs fallthrough 到根 div）。 */
function findHeroButton(w: VueWrapper, title: string) {
  return w
    .findAllComponents({ name: 'ArtButtonTable' })
    .find((b) => b.attributes('title') === title)
}

/** 页面 hero 的刷新按钮。 */
function findRefreshButton(w: VueWrapper) {
  const btn = findHeroButton(w, '刷新')
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

  it('主机筛选项可搜（filterable：主机多了敲名字找）', async () => {
    const w = await mountPage()
    // ArtSearchBar 是替身，items 落在 $attrs（与 tableRows 同一读法）。
    const bar = w.findComponent({ name: 'ArtSearchBar' })
    const items = ((bar.vm as unknown as { $attrs: Record<string, unknown> }).$attrs.items ??
      []) as { key: string; filterable?: boolean }[]
    expect(items.find((i) => i.key === 'host')?.filterable).toBe(true)
  })
})

describe('表格列（保护标记走图标，不用 emoji）', () => {
  it('保护列：受保护给锁图标 vnode + 「受保护」，未受保护给「—」', async () => {
    const w = await mountPage()
    const table = w.findComponent({ name: 'ArtTable' })
    const columns = ((table.vm as unknown as { $attrs: Record<string, unknown> }).$attrs.columns ??
      []) as { prop?: string; formatter?: (row: never) => unknown }[]
    const col = columns.find((c) => c.prop === 'protected')
    expect(col?.formatter, '表格没有把保护列 formatter 传给 ArtTable').toBeTruthy()

    const vnode = col!.formatter!({ protected: true } as never) as {
      props?: Record<string, unknown>
      children?: unknown[]
    }
    expect(vnode.props?.class).toBe('wkl-lock')
    const kids = (Array.isArray(vnode.children) ? vnode.children : []) as unknown[]
    expect(kids.some((k) => typeof k === 'string' && k.includes('受保护'))).toBe(true)
    // 图标是组件 vnode（ArtSvgIcon + ri:lock-2-line），不再是 🔒 文本
    const icon = kids[0] as { type?: { name?: string }; props?: Record<string, unknown> }
    expect(icon?.type?.name).toBe('ArtSvgIcon')
    expect(icon?.props?.icon).toBe('ri:lock-2-line')

    expect(col!.formatter!({ protected: false } as never)).toBe('—')
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

  it('行菜单「日志」→ 详情页 path + ?host + ?tab=logs（8a：能力搬到详情页的日志 Tab）', async () => {
    const w = await mountPage()
    const table = w.findComponent({ name: 'DockerWorkloadTable' })
    table.vm.$emit('menu-select', { row: ALL.items[1], key: 'logs' })
    await flushNav()

    // navigation 语义：本页只把目标行交给路由（表格不认识路由，跳转在页面里做）。
    expect(currentRouter!.currentRoute.value.path).toBe('/docker/containers/c2')
    expect(currentRouter!.currentRoute.value.query).toEqual({ host: 'h2', tab: 'logs' })
  })

  it('行「详情」→ 详情页 path + ?host（概览是默认屏，不带 tab）', async () => {
    const w = await mountPage()
    const table = w.findComponent({ name: 'DockerWorkloadTable' })
    table.vm.$emit('open-detail', ALL.items[0])
    await flushNav()

    expect(currentRouter!.currentRoute.value.path).toBe('/docker/containers/c1')
    expect(currentRouter!.currentRoute.value.query).toEqual({ host: 'h1' })
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

/* ── 详情/创建入口：一律整页路由（8a/8b）；?id= 深链语义已删（零兼容）───────
 * 7b 的「本页读 ?id 打开详情抽屉」是 ?id= 唯一的落点，抽屉与语义一起删。文字层的
 * 锚在 routes.test.ts（本页源码不得再出现 query.id），这里钉行为层：带着 ?id= 进来
 * 不打开任何东西、也不清 query —— 一条过期的站外深链不该静默变成另一种界面。
 * 创建入口（8b）则相反，是要**跳走**的那一类：hero 的创建钮把当前筛选主机随行
 * 交给创建页（跨主机表没有「当前主机」的概念，筛选就是最近的意图）；任务中心
 * （8c）入口同属跳走的那一类（移栽自被删的 docker-page 主机条）。
 */
describe('详情/创建入口：一律整页路由（8a/8b）', () => {
  it('?id= 已无落点：带了也不开详情面、不清 query，页面照常是统一表', async () => {
    const w = await mountPage({ host: 'h2', id: 'c2' })
    // 多给一轮宏任务 + 渲染：防「稍后才开」这类迟到的落点。
    await new Promise((r) => setTimeout(r, 0))
    await nextTick()

    expect(currentRouter!.currentRoute.value.path).toBe('/docker/containers')
    expect(currentRouter!.currentRoute.value.query.id).toBe('c2') // 页面不消费也不清
    expect(tableRows(w)).toHaveLength(2)
    // 没有任何详情面被拉起来：本页从不发指令（指令通道的入口是行菜单与详情页）。
    expect(api.sendDockerCmd).not.toHaveBeenCalled()
  })

  it('hero 的创建钮 → 创建页 path，host 取当前筛选（没筛选就不带）', async () => {
    const withHost = await mountPage({ host: 'h2' })
    const btn = findHeroButton(withHost, '创建容器')
    expect(btn, 'hero 上应有「创建容器」入口').toBeTruthy()
    await btn!.trigger('click')
    await flushNav()
    expect(currentRouter!.currentRoute.value.path).toBe('/docker/containers/create')
    expect(currentRouter!.currentRoute.value.query).toEqual({ host: 'h2' })

    const noHost = await mountPage()
    const btn2 = findHeroButton(noHost, '创建容器')
    expect(btn2, 'hero 上应有「创建容器」入口').toBeTruthy()
    await btn2!.trigger('click')
    await flushNav()
    expect(currentRouter!.currentRoute.value.path).toBe('/docker/containers/create')
    expect(currentRouter!.currentRoute.value.query).toEqual({})
  })

  it('hero 的任务中心钮 → /docker/tasks（入口移栽自被删的 docker-page 主机条）', async () => {
    const w = await mountPage()
    const btn = findHeroButton(w, '任务中心')
    expect(btn, 'hero 上应有任务中心入口').toBeTruthy()
    await btn!.trigger('click')
    await flushNav()
    expect(currentRouter!.currentRoute.value.path).toBe('/docker/tasks')
  })
})
