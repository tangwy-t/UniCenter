// @vitest-environment jsdom
/**
 * Wave 2 分页/排序契约守卫（五个跨主机列表页：容器 / 镜像 / 数据卷 / 网络 / 项目）。
 *
 * 本文件钉住「服务端分页 + 服务端排序 + 不可用主机清单」在页面层的接线不变量：
 *   ① 分页器随数据出现 / 随空数据不出现（复刻 ArtTable 的 `pagination && !isEmpty`
 *      公式，见 components/core/tables/art-table/index.vue:373）——「不传 :pagination
 *      就永不出现」是本波修复的核心，五页都必须在有行时挂上分页态；
 *   ② 筛选变更（搜索/重置）把 current 打回第 1 页（否则翻到第 3 页再筛会落空页）；
 *   ③ sort-change 只认白名单列（列 prop → 后端 sortBy 键），非白名单列不发请求；
 *   ④ unavailableHosts 非空时如实列出（主机名 + reason），空时不渲染；
 *   ⑤ truncated（旧截断结论）已彻底移除 —— 源码里不再有它的引用。
 *
 * 挂载口径沿用 resources.test.ts（mock ../api 与 useAuth，Art* 用轻量替身）；
 * 分页态 / 请求参数 / 排序参数都从替身的 props、$attrs 与 mock 调用记录断言。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { defineComponent, h, nextTick, type Component } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'

const api = vi.hoisted(() => ({
  fetchDockerHosts: vi.fn(),
  fetchDockerContainers: vi.fn(),
  fetchDockerImages: vi.fn(),
  fetchDockerVolumes: vi.fn(),
  fetchDockerNetworks: vi.fn(),
  fetchDockerProjects: vi.fn(),
  sendDockerCmd: vi.fn(),
  fetchDockerCmdResult: vi.fn(),
  // 镜像页挂载的进度/凭据对话框中会用到，import 面必须齐全（用例里不触发）。
  openDockerPullStream: vi.fn(),
  openDockerBuildStream: vi.fn(),
  openDockerPushStream: vi.fn(),
  uploadDockerBuildContext: vi.fn(),
  fetchDockerRegistries: vi.fn()
}))
vi.mock('../api', () => ({ ...api, default: undefined }))
vi.mock('@/hooks/core/useAuth', () => ({
  useAuth: () => ({ hasAuth: () => true, hasAnyAuth: () => true })
}))

import Containers from '../views/containers.vue'
import Images from '../views/images.vue'
import Volumes from '../views/volumes.vue'
import Networks from '../views/networks.vue'
import Projects from '../views/projects.vue'

const HOSTS = {
  list: [
    { id: 'h1', hostname: 'bogon', primaryIp: '192.168.12.105', online: true, dockerOk: true },
    { id: 'h2', hostname: 'nas', primaryIp: '192.168.12.106', online: true, dockerOk: true }
  ]
}

const CONTAINER = {
  id: 'c1',
  name: 'uni-center-core',
  image: 'uni-center-core:latest',
  state: 'running',
  cpuPercent: 0.6,
  memUsageMb: 91,
  memLimitMb: 1024,
  netRxBytesSec: 0,
  netTxBytesSec: 0,
  protected: false,
  hostId: 'h1',
  hostname: 'bogon'
}
const IMAGE = {
  id: 'i1',
  repoTags: ['uni-center-core:latest'],
  sizeMb: 91.8,
  inUse: true,
  dangling: false,
  hostId: 'h1',
  hostname: 'bogon'
}
const VOLUME = {
  name: 'uni-center_uploads',
  driver: 'local',
  sizeMb: 12,
  inUse: true,
  protected: false,
  hostId: 'h1',
  hostname: 'bogon'
}
const NETWORK = {
  name: 'uni-center_default',
  driver: 'bridge',
  scope: 'local',
  internal: false,
  containersCount: 2,
  hostId: 'h1',
  hostname: 'bogon'
}
const PROJECT = {
  name: 'uni-center',
  state: 'running',
  services: 2,
  containersCount: 2,
  protected: true,
  hostId: 'h1',
  hostname: 'bogon'
}

/**
 * 一页的接线事实：端点 mock 名、路径、行夹具、可排/不可排的列 prop → 后端 sortBy 键。
 */
interface PageCase {
  name: string
  component: Component
  path: string
  fetchKey: keyof typeof api
  row: Record<string, unknown>
  /** 一个白名单列：列 prop 与它在后端发出的 sortBy 键。 */
  sortable: { prop: string; key: string }
  /** 一个**非**白名单列（点了不该产生请求）。 */
  nonSortableProp: string
}

const PAGES: PageCase[] = [
  {
    name: '容器',
    component: Containers,
    path: '/docker/containers',
    fetchKey: 'fetchDockerContainers',
    row: CONTAINER,
    sortable: { prop: 'statusText', key: 'state' },
    nonSortableProp: 'cpuPercent'
  },
  {
    name: '镜像',
    component: Images,
    path: '/docker/images',
    fetchKey: 'fetchDockerImages',
    row: IMAGE,
    sortable: { prop: 'sizeMb', key: 'size' },
    nonSortableProp: 'inUse'
  },
  {
    name: '数据卷',
    component: Volumes,
    path: '/docker/volumes',
    fetchKey: 'fetchDockerVolumes',
    row: VOLUME,
    sortable: { prop: 'sizeMb', key: 'size' },
    nonSortableProp: 'driver'
  },
  {
    name: '网络',
    component: Networks,
    path: '/docker/networks',
    fetchKey: 'fetchDockerNetworks',
    row: NETWORK,
    sortable: { prop: 'containersCount', key: 'containers' },
    nonSortableProp: 'driver'
  },
  {
    name: '项目',
    component: Projects,
    path: '/docker/projects',
    fetchKey: 'fetchDockerProjects',
    row: PROJECT,
    sortable: { prop: 'state', key: 'state' },
    nonSortableProp: 'containersCount'
  }
]

const passthrough = (name: string) =>
  defineComponent({
    name,
    setup:
      (_, { slots }) =>
      () =>
        h('div', { 'data-stub': name }, [slots.default?.(), slots.left?.()])
  })

const artSearchBarStub = defineComponent({
  name: 'ArtSearchBar',
  props: {
    modelValue: { type: Object, default: () => ({}) },
    items: { type: Array, default: () => [] }
  },
  emits: ['update:modelValue', 'search', 'reset'],
  setup: () => () => h('div', { 'data-stub': 'ArtSearchBar' })
})

/**
 * ArtTable 替身：复刻真组件的 `showPagination = pagination && !isEmpty`
 * （index.vue:373）—— 出现/不出现由它决定，页面只负责挂 `:pagination`。
 * pagination 是声明 prop（用例从它读 total）；data 也声明，判空用。
 */
const artTableStub = defineComponent({
  name: 'ArtTable',
  inheritAttrs: false,
  props: {
    pagination: { type: Object, default: undefined },
    data: { type: Array, default: () => [] }
  },
  setup(props, { slots }) {
    return () =>
      h('div', { 'data-stub': 'ArtTable' }, [
        props.pagination && (props.data?.length ?? 0) > 0
          ? h('div', { class: 'pagination-element' })
          : null,
        slots.default?.()
      ])
  }
})

const STUBS = {
  ArtTable: artTableStub,
  ArtSearchBar: artSearchBarStub,
  ArtTableHeader: passthrough('ArtTableHeader'),
  ArtButtonTable: passthrough('ArtButtonTable'),
  ArtButtonMore: passthrough('ArtButtonMore'),
  ArtIconButton: passthrough('ArtIconButton'),
  ArtPageContent: passthrough('ArtPageContent'),
  ArtSvgIcon: passthrough('ArtSvgIcon')
}

const mounted: VueWrapper[] = []
afterEach(() => {
  for (const w of mounted.splice(0)) w.unmount()
  vi.clearAllMocks()
})

const flush = async () => {
  await new Promise((r) => setTimeout(r, 0))
  await nextTick()
}

/** 默认：两台主机的清单各给一行（total 与行数一致，未触发分页额外信息）。 */
function mockFetchers(items: Record<string, unknown>, total: number, unavailableHosts?: unknown[]) {
  api.fetchDockerHosts.mockResolvedValue(HOSTS)
  for (const key of [
    'fetchDockerContainers',
    'fetchDockerImages',
    'fetchDockerVolumes',
    'fetchDockerNetworks',
    'fetchDockerProjects'
  ] as const) {
    api[key].mockResolvedValue({
      items: [items],
      total,
      ...(unavailableHosts ? { unavailableHosts } : {})
    })
  }
}

async function mountPage(c: PageCase, query: Record<string, string> = {}): Promise<VueWrapper> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: c.path, component: c.component },
      // 各页的行内动作会 push 到这些目标路径（本文件不触发，仅避免导航告警）。
      { path: `${c.path}/:id`, component: { template: '<div />' } },
      { path: `${c.path}/:name`, component: { template: '<div />' } }
    ]
  })
  await router.push({ path: c.path, query })
  await router.isReady()
  const w = mount(c.component, { global: { plugins: [router], stubs: STUBS } })
  mounted.push(w as VueWrapper)
  await flush()
  return w as VueWrapper
}

function tableOf(w: VueWrapper) {
  const table = w.findComponent({ name: 'ArtTable' })
  expect(table.exists(), '页面里没有表格').toBe(true)
  return table
}

beforeEach(() => {
  vi.stubGlobal(
    'ResizeObserver',
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
  )
  for (const fn of Object.values(api)) fn.mockReset()
})

describe('①分页器随数据出现 / 空数据不出现', () => {
  it.each(PAGES)('$name：有行时渲染分页器，pagination.total 同步响应', async (c) => {
    mockFetchers(c.row, 640)
    const w = await mountPage(c)
    // 分页器出现（复刻 ArtTable 的 showPagination 公式）。
    expect(w.find('.pagination-element').exists()).toBe(true)
    const pag = tableOf(w).props('pagination') as { current: number; size: number; total: number }
    expect(pag).toMatchObject({ current: 1, size: 50, total: 640 })
    // 请求显式带分页参数（pageSize 不靠后端默认）。
    expect(api[c.fetchKey]).toHaveBeenCalledWith(expect.objectContaining({ page: 1, pageSize: 50 }))
  })

  it.each(PAGES)('$name：空数据时不渲染分页器', async (c) => {
    mockFetchers(c.row, 0)
    // 空清单：items 空、total 0 → 表格换空态 → 分页器不出现。
    for (const key of [
      'fetchDockerContainers',
      'fetchDockerImages',
      'fetchDockerVolumes',
      'fetchDockerNetworks',
      'fetchDockerProjects'
    ] as const) {
      api[key].mockResolvedValue({ items: [], total: 0 })
    }
    const w = await mountPage(c)
    expect(w.find('.pagination-element').exists()).toBe(false)
  })
})

describe('②筛选变更把 current 打回第 1 页', () => {
  it.each(PAGES)('$name：翻到第 2 页后搜索 / 重置都回 page=1', async (c) => {
    mockFetchers(c.row, 640)
    const w = await mountPage(c)
    const table = tableOf(w)

    // 翻到第 2 页：current-change 触发重拉，请求带 page=2。
    table.vm.$emit('pagination:current-change', 2)
    await flush()
    expect(api[c.fetchKey]).toHaveBeenLastCalledWith(expect.objectContaining({ page: 2 }))

    // 搜索：current 打回 1。
    w.findComponent({ name: 'ArtSearchBar' }).vm.$emit('search')
    await flush()
    expect(api[c.fetchKey]).toHaveBeenLastCalledWith(expect.objectContaining({ page: 1 }))

    // 再翻到第 2 页，重置也回 1。
    table.vm.$emit('pagination:current-change', 2)
    await flush()
    w.findComponent({ name: 'ArtSearchBar' }).vm.$emit('reset')
    await flush()
    expect(api[c.fetchKey]).toHaveBeenLastCalledWith(expect.objectContaining({ page: 1 }))
  })
})

describe('③sort-change 只对白名单列发出请求参数', () => {
  it.each(PAGES)('$name：白名单列 → 发 sortBy/sortOrder；非白名单列 → 不请求', async (c) => {
    mockFetchers(c.row, 640)
    const w = await mountPage(c)
    const table = tableOf(w)
    ;(api[c.fetchKey] as ReturnType<typeof vi.fn>).mockClear()

    // 白名单列：列 prop 映射成后端 sortBy 键，ascending → asc。
    table.vm.$emit('sort-change', { prop: c.sortable.prop, order: 'ascending' })
    await flush()
    expect(api[c.fetchKey]).toHaveBeenCalledWith(
      expect.objectContaining({ sortBy: c.sortable.key, sortOrder: 'asc', page: 1 })
    )

    const calls = (api[c.fetchKey] as ReturnType<typeof vi.fn>).mock.calls.length
    // 非白名单列：一律忽略（不发请求）。
    table.vm.$emit('sort-change', { prop: c.nonSortableProp, order: 'descending' })
    await flush()
    expect((api[c.fetchKey] as ReturnType<typeof vi.fn>).mock.calls.length).toBe(calls)

    // 第三态（取消排序）：sortBy 清空，回到默认次序。
    table.vm.$emit('sort-change', { prop: c.sortable.prop, order: null })
    await flush()
    expect(api[c.fetchKey]).toHaveBeenLastCalledWith(expect.objectContaining({ sortBy: undefined }))
  })
})

describe('④不可用主机清单：非空如实列出、空不渲染', () => {
  it.each(PAGES)('$name：unavailableHosts 非空 → 列主机名 + reason 原文', async (c) => {
    mockFetchers(c.row, 1, [{ hostId: 'h9', hostname: 'nas', reason: '连接超时' }])
    const w = await mountPage(c)
    const text = w.text()
    expect(text).toContain('台主机不可用')
    expect(text).toContain('nas')
    expect(text).toContain('连接超时')
  })

  it.each(PAGES)('$name：unavailableHosts 空 → 不出现该句', async (c) => {
    mockFetchers(c.row, 1)
    const w = await mountPage(c)
    expect(w.text()).not.toContain('台主机不可用')
  })
})

describe('⑤truncated（旧截断结论）已彻底移除', () => {
  const files = [
    'composables/useWorkloadList.ts',
    'composables/useResourceList.ts',
    'views/containers.vue',
    'views/images.vue',
    'views/volumes.vue',
    'views/networks.vue',
    'views/projects.vue'
  ]
  it.each(files)('%s：源码不再引用 truncated', (rel) => {
    const src = readFileSync(resolve(process.cwd(), 'src/modules/docker', rel), 'utf8')
    expect(src).not.toMatch(/\btruncated\b/)
  })
})
