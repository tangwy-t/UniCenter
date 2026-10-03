// @vitest-environment jsdom
/**
 * 项目列表页（7b 薄索引 → 9b 跨主机化）的行为面：聚合行数据（每行带归属）、
 * 主机列 + 主机筛选（?host 作筛选初始值）、服务端筛选参数透传、失败口径、
 * 「打开工作台」深链（host 取**行主机**）与权限门。
 *
 * 挂载口径沿用 workloads.test.ts（mock ../api 与 useAuth，Art* 用轻量替身）；
 * 行断言不点 DOM —— jsdom 里 ElTable 不渲染行单元格，从**传给 ArtTable 的 data**
 * 与**列配置的 formatter**（直接调用拿 vnode，workloads.test 的 operationVNodes
 * 同款口径）取事实。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'

const api = vi.hoisted(() => ({
  fetchDockerHosts: vi.fn(),
  fetchDockerProjects: vi.fn()
}))
vi.mock('../api', () => ({ ...api, default: undefined }))

// hasAuth 可变桩：权限门用例要单独关掉 docker:inspect。
const auth = vi.hoisted(() => ({ allow: new Set<string>() }))
vi.mock('@/hooks/core/useAuth', () => ({
  useAuth: () => ({
    hasAuth: (perm: string) => auth.allow.has(perm),
    hasAnyAuth: () => true
  })
}))

import Projects from '../views/projects.vue'
import type { DockerProjectListItem } from '../api'

const HOSTS = {
  list: [
    { id: 'h1', hostname: 'bogon', primaryIp: '192.168.12.105', online: true, dockerOk: true },
    { id: 'h2', hostname: 'nas', primaryIp: '192.168.12.106', online: true, dockerOk: true }
  ]
}

/** 跨主机聚合条目：单主机字段 + 归属两列（hostId/hostname）。 */
const PROJECTS: DockerProjectListItem[] = [
  {
    name: 'uni-center',
    configFiles: ['/data/UniCenter/docker-compose.yml'],
    state: 'running',
    services: 2,
    containersCount: 2,
    protected: true,
    hostId: 'h1',
    hostname: 'bogon'
  },
  {
    name: 'media-stack',
    configFiles: ['/srv/media/docker-compose.yml'],
    state: 'partial',
    services: 3,
    containersCount: 2,
    protected: false,
    hostId: 'h2',
    hostname: 'nas'
  }
]

/** Art* 全局组件替身：保留 slot（workloads.test 同款）。 */
const passthrough = (name: string) =>
  defineComponent({
    name,
    setup:
      (_, { slots }) =>
      () =>
        h('div', { 'data-stub': name }, [slots.default?.(), slots.left?.(), slots.table?.()])
  })

/** ArtSearchBar 替身要**声明 items prop**：筛选面板的形态断言（key 集合/选项）
 *  从它读，透传替身会把 props 留成 undefined。 */
const artSearchBarStub = defineComponent({
  name: 'ArtSearchBar',
  props: {
    modelValue: { type: Object, default: () => ({}) },
    items: { type: Array, default: () => [] }
  },
  emits: ['update:modelValue', 'search', 'reset'],
  setup: () => () => h('div', { 'data-stub': 'ArtSearchBar' })
})

const STUBS = {
  ArtTable: passthrough('ArtTable'),
  ArtTableHeader: passthrough('ArtTableHeader'),
  ArtSearchBar: artSearchBarStub,
  ArtButtonTable: passthrough('ArtButtonTable'),
  ArtSvgIcon: passthrough('ArtSvgIcon')
}

const mounted: VueWrapper[] = []
let currentRouter: Router | null = null

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
  auth.allow = new Set(['docker:list', 'docker:inspect'])
  api.fetchDockerHosts.mockResolvedValue(HOSTS)
  api.fetchDockerProjects.mockResolvedValue({ items: PROJECTS, total: PROJECTS.length })
})

async function mountPage(query: Record<string, string> = {}): Promise<VueWrapper> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/docker/projects', name: 'DockerProjects', component: Projects },
      {
        path: '/docker/projects/:name',
        name: 'DockerProjectWorkspace',
        component: { template: '<div />' }
      }
    ]
  })
  currentRouter = router
  await router.push({ path: '/docker/projects', query })
  await router.isReady()
  const w = mount(Projects, { global: { plugins: [router], stubs: STUBS } })
  mounted.push(w)
  await new Promise((r) => setTimeout(r, 0))
  await nextTick()
  return w
}

/** 页面传给 ArtTable 的行（替身把 data 落在 $attrs）。 */
function tableRows(w: VueWrapper): DockerProjectListItem[] {
  const table = w.findComponent({ name: 'ArtTable' })
  return ((table.vm as unknown as { $attrs: Record<string, unknown> }).$attrs.data ??
    []) as DockerProjectListItem[]
}

/** 页面传给 ArtTable 的列配置。 */
function tableColumns(w: VueWrapper): {
  prop?: string
  label?: string
  hideBelow?: string
  formatter?: (row: never) => unknown
}[] {
  const table = w.findComponent({ name: 'ArtTable' })
  return ((table.vm as unknown as { $attrs: Record<string, unknown> }).$attrs.columns ?? []) as {
    prop?: string
    label?: string
    hideBelow?: string
    formatter?: (row: never) => unknown
  }[]
}

describe('跨主机索引渲染（9b：聚合数据源）', () => {
  it('两台主机的项目都进同一张表，行带 hostId/hostname，计数说出口', async () => {
    const w = await mountPage()
    expect(w.find('.docker-projects-page').exists()).toBe(true)
    expect(w.html()).toContain('共 2 个项目')
    const rows = tableRows(w)
    expect(rows.map((p) => p.name)).toEqual(['uni-center', 'media-stack'])
    expect(rows[0]).toMatchObject({ hostId: 'h1', hostname: 'bogon' })
    expect(rows[1]).toMatchObject({ hostId: 'h2', hostname: 'nas' })
  })

  it('主机列在表里（平板竖屏起可见），行归属是排查第一线索', async () => {
    const w = await mountPage()
    const hostCol = tableColumns(w).find((c) => c.label === '主机')
    expect(hostCol, '项目表缺少「主机」列').toBeTruthy()
    expect(hostCol?.prop).toBe('hostname')
    expect(hostCol?.hideBelow).toBe('tablet')
  })

  it('服务端截断时计数说出口（total 全量 + 前 N 条）', async () => {
    api.fetchDockerProjects.mockResolvedValue({ items: PROJECTS, total: 640 })
    const w = await mountPage()
    expect(w.html()).toContain('共 640 个项目')
    expect(w.html()).toContain('列表显示前 2 条')
  })

  it('状态列给点 + 文字结论（与工作台 hero 同一批口径）', async () => {
    const w = await mountPage()
    const state = tableColumns(w).find((c) => c.prop === 'state')
    expect(state?.formatter, '状态列应有 formatter').toBeTruthy()

    const runVnode = state!.formatter!(PROJECTS[0] as never) as {
      children: unknown[]
      props?: Record<string, unknown>
    }
    // h('span', {class}, [dot vnode, '运行中'])：文字结论与点都在 children 里。
    expect(runVnode.children).toContain('运行中')
    const partialVnode = state!.formatter!(PROJECTS[1] as never) as { children: unknown[] }
    expect(partialVnode.children).toContain('部分运行')
  })

  it('保护列给锁图标 + 「受保护」的可见标记（保护档结论句属于工作台 hero，索引不重复）', async () => {
    const w = await mountPage()
    const prot = tableColumns(w).find((c) => c.prop === 'protected')
    const vnode = prot!.formatter!(PROJECTS[0] as never) as {
      props?: Record<string, unknown>
      children?: unknown[]
    }
    expect(vnode.props?.class).toBe('docker-proj-lock')
    const kids = (Array.isArray(vnode.children) ? vnode.children : []) as unknown[]
    expect(kids.some((k) => typeof k === 'string' && k.includes('受保护'))).toBe(true)
    // 图标是组件 vnode（ArtSvgIcon + ri:lock-2-line），不再是 🔒 文本
    const icon = kids[0] as { type?: { name?: string }; props?: Record<string, unknown> }
    expect(icon?.type?.name).toBe('ArtSvgIcon')
    expect(icon?.props?.icon).toBe('ri:lock-2-line')

    expect(prot?.formatter!(PROJECTS[1] as never)).toBe('—')
  })
})

describe('筛选（ArtSearchBar 统一形态；服务端过滤）', () => {
  it('筛选面板是统一的三件套（项目名/状态/主机），主机选项来自主机清单', async () => {
    const w = await mountPage()
    const bar = w.findComponent({ name: 'ArtSearchBar' })
    const items = bar.props('items') as {
      key: string
      filterable?: boolean
      options?: { label: string; value: string }[]
    }[]
    expect(items.map((i) => i.key)).toEqual(['keyword', 'state', 'host'])
    expect(items.find((i) => i.key === 'host')?.options).toEqual([
      { label: 'bogon', value: 'h1' },
      { label: 'nas', value: 'h2' }
    ])
    // 主机下拉可搜（filterable）：主机多了要能敲名字找（QA 实测不可搜）
    expect(items.find((i) => i.key === 'host')?.filterable).toBe(true)
    expect(items.find((i) => i.key === 'state')?.options).toEqual([
      { label: '运行中', value: 'running' },
      { label: '已停止', value: 'stopped' }
    ])
  })

  it('搜索时 keyword/state/hostId 三项透传（服务端过滤）', async () => {
    const w = await mountPage()
    api.fetchDockerProjects.mockClear()

    const bar = w.findComponent({ name: 'ArtSearchBar' })
    bar.vm.$emit('update:modelValue', { keyword: 'media', state: 'stopped', host: 'h2' })
    bar.vm.$emit('search')
    await new Promise((r) => setTimeout(r, 0))

    expect(api.fetchDockerProjects).toHaveBeenCalledWith({
      hostId: 'h2',
      keyword: 'media',
      state: 'stopped'
    })
  })

  it('重置清空三项参数（undefined 不发）', async () => {
    const w = await mountPage({ host: 'h2' }) // 先带一个 host 深链
    const bar = w.findComponent({ name: 'ArtSearchBar' })
    bar.vm.$emit('update:modelValue', {})
    bar.vm.$emit('reset')
    await new Promise((r) => setTimeout(r, 0))

    expect(api.fetchDockerProjects).toHaveBeenLastCalledWith({
      hostId: undefined,
      keyword: undefined,
      state: undefined
    })
  })

  it('?host 深链作主机筛选初始值：首拉就带 hostId', async () => {
    await mountPage({ host: 'h2' })
    expect(api.fetchDockerProjects).toHaveBeenCalledWith(expect.objectContaining({ hostId: 'h2' }))
  })

  it('不带 host 的进入是全部主机首拉', async () => {
    await mountPage()
    expect(api.fetchDockerProjects).toHaveBeenCalledWith(
      expect.objectContaining({ hostId: undefined })
    )
  })
})

describe('「打开工作台」（行上唯一的动作）', () => {
  it('点击后带**行主机**进工作台（项目是主机作用域的）', async () => {
    const w = await mountPage()
    const op = tableColumns(w).find((c) => c.prop === 'operation')
    expect(op?.formatter, '操作列应有 formatter').toBeTruthy()

    // 直接调 formatter 拿 ElButton vnode（jsdom 里 ElTable 不渲染行单元格）。
    // 取第二行（hostId=h2）：工作台的 host 必须是**行主机**，不是页面级主机。
    const vnode = op!.formatter!(PROJECTS[1] as never) as {
      type: unknown
      props?: { onClick?: () => void }
    }
    expect(vnode.props?.onClick, '工作台按钮应带跳转').toBeTruthy()
    vnode.props!.onClick!()
    // 等导航落定：判据用「路由到了」而不是「过了一轮宏任务」—— 导航管线多轮微任务，
    // 负载下固定轮数会漂。
    await vi.waitUntil(() => currentRouter!.currentRoute.value.name === 'DockerProjectWorkspace', {
      timeout: 5000
    })

    expect(currentRouter!.currentRoute.value.name).toBe('DockerProjectWorkspace')
    expect(currentRouter!.currentRoute.value.params.name).toBe('media-stack')
    expect(currentRouter!.currentRoute.value.query.host).toBe('h2')
  })

  it('无 docker:inspect 权限时入口不渲染（不渲染 ≠ 禁用）', async () => {
    auth.allow = new Set(['docker:list'])
    const w = await mountPage()
    const op = tableColumns(w).find((c) => c.prop === 'operation')
    expect(op?.formatter!(PROJECTS[0] as never)).toBeNull()
  })
})

describe('四种状态的空/错口径', () => {
  it('清单拉不到主机清单也为空：尚无可管主机（不是「没有项目」）', async () => {
    api.fetchDockerHosts.mockResolvedValue({ list: [] })
    api.fetchDockerProjects.mockResolvedValue({ items: [], total: 0 })
    const w = await mountPage()
    expect(w.text()).toContain('尚无可管主机')
  })

  it('有可管主机但项目为空：还没有项目', async () => {
    api.fetchDockerProjects.mockResolvedValue({ items: [], total: 0 })
    const w = await mountPage()
    expect(w.text()).toContain('还没有项目')
  })

  it('筛选没命中：分开说 + 「清除筛选」能回到全量', async () => {
    const w = await mountPage()
    // 服务端过滤：带筛条件时端点回空。
    api.fetchDockerProjects.mockResolvedValue({ items: [], total: 0 })
    const bar = w.findComponent({ name: 'ArtSearchBar' })
    bar.vm.$emit('update:modelValue', { keyword: '不存在' })
    bar.vm.$emit('search')
    await new Promise((r) => setTimeout(r, 0))
    expect(w.text()).toContain('没有符合筛选条件的项目')

    // 清除筛选：回到无筛选的一次拉取（替身此时回全量）。
    api.fetchDockerProjects.mockResolvedValue({ items: PROJECTS, total: PROJECTS.length })
    const clear = w.findAll('button').find((b) => b.text().includes('清除筛选'))
    expect(clear, '筛选空态应有清除筛选按钮').toBeTruthy()
    await clear!.trigger('click')
    await new Promise((r) => setTimeout(r, 0))
    expect(tableRows(w).map((p) => p.name)).toEqual(['uni-center', 'media-stack'])
  })

  it('首拉失败：整页错误态 + 重试（不显示旧数据）', async () => {
    api.fetchDockerProjects.mockRejectedValueOnce(new Error('network down'))
    const w = await mountPage()
    expect(w.text()).toContain('项目清单获取失败')
    expect(w.findComponent({ name: 'ArtTable' }).exists()).toBe(false)

    api.fetchDockerProjects.mockResolvedValue({ items: PROJECTS, total: PROJECTS.length })
    const retry = w.findAll('button').find((b) => b.text().includes('重试'))
    expect(retry, '错误态应有重试按钮').toBeTruthy()
    await retry!.trigger('click')
    await new Promise((r) => setTimeout(r, 0))
    expect(tableRows(w).map((p) => p.name)).toEqual(['uni-center', 'media-stack'])
  })
})
