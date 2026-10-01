// @vitest-environment jsdom
/**
 * 项目列表页（7b 薄索引）的行为面：行数据、keyword 本地过滤（D-9 补课）、
 * 「打开工作台」深链（host 随行）与权限门。
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
  fetchDockerState: vi.fn()
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
import type { DockerProjectItem } from '../api'

const HOSTS = {
  list: [{ id: 'h1', hostname: 'bogon', primaryIp: '192.168.12.105', online: true, dockerOk: true }]
}

const PROJECTS: DockerProjectItem[] = [
  {
    name: 'uni-center',
    configFiles: ['/data/UniCenter/docker-compose.yml'],
    state: 'running',
    services: 2,
    containersCount: 2,
    protected: true
  },
  {
    name: 'media-stack',
    configFiles: ['/srv/media/docker-compose.yml'],
    state: 'partial',
    services: 3,
    containersCount: 2,
    protected: false
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

const STUBS = {
  ArtTable: passthrough('ArtTable'),
  ArtTableHeader: passthrough('ArtTableHeader'),
  ArtSearchBar: passthrough('ArtSearchBar'),
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
  api.fetchDockerState.mockResolvedValue({
    lastSync: 1790000000,
    stale: false,
    ageSeconds: 3,
    neverReported: false,
    dockerOk: true,
    containers: [],
    images: [],
    volumes: [],
    networks: [],
    projects: PROJECTS
  })
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
function tableRows(w: VueWrapper): DockerProjectItem[] {
  const table = w.findComponent({ name: 'ArtTable' })
  return ((table.vm as unknown as { $attrs: Record<string, unknown> }).$attrs.data ??
    []) as DockerProjectItem[]
}

/** 页面传给 ArtTable 的列配置。 */
function tableColumns(w: VueWrapper): {
  prop?: string
  formatter?: (row: never) => unknown
}[] {
  const table = w.findComponent({ name: 'ArtTable' })
  return ((table.vm as unknown as { $attrs: Record<string, unknown> }).$attrs.columns ?? []) as {
    prop?: string
    formatter?: (row: never) => unknown
  }[]
}

describe('薄索引渲染（7b：列表页只是工作台的索引）', () => {
  it('两个项目都进表，提示行说清能力去向', async () => {
    const w = await mountPage()
    expect(w.find('.docker-projects-page').exists()).toBe(true)
    expect(w.html()).toContain('共 2 个项目')
    expect(w.html()).toContain('都在「打开工作台」里')
    expect(tableRows(w).map((p) => p.name)).toEqual(['uni-center', 'media-stack'])
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

  it('保护列给可见标记（保护档结论句属于工作台 hero，索引不重复）', async () => {
    const w = await mountPage()
    const prot = tableColumns(w).find((c) => c.prop === 'protected')
    expect(prot?.formatter!(PROJECTS[0] as never)).toBe('🔒 受保护')
    expect(prot?.formatter!(PROJECTS[1] as never)).toBe('—')
  })
})

describe('keyword 筛选（D-9 补课：本地过滤）', () => {
  it('查询后只留命中行，计数把两个数都说出来', async () => {
    const w = await mountPage()
    const bar = w.findComponent({ name: 'ArtSearchBar' })
    bar.vm.$emit('update:modelValue', { keyword: 'uni' })
    bar.vm.$emit('search')
    await nextTick()

    expect(tableRows(w).map((p) => p.name)).toEqual(['uni-center'])
    expect(w.html()).toContain('共 2 个项目')
    expect(w.html()).toContain('命中 1')
  })

  it('大小写不敏感；重置回到全量', async () => {
    const w = await mountPage()
    const bar = w.findComponent({ name: 'ArtSearchBar' })
    bar.vm.$emit('update:modelValue', { keyword: 'MEDIA' })
    bar.vm.$emit('search')
    await nextTick()
    expect(tableRows(w).map((p) => p.name)).toEqual(['media-stack'])

    bar.vm.$emit('update:modelValue', {})
    bar.vm.$emit('reset')
    await nextTick()
    expect(tableRows(w)).toHaveLength(2)
  })
})

describe('「打开工作台」（行上唯一的动作）', () => {
  it('点击后带 host 进工作台（项目是主机作用域的）', async () => {
    const w = await mountPage({ host: 'h1' })
    const op = tableColumns(w).find((c) => c.prop === 'operation')
    expect(op?.formatter, '操作列应有 formatter').toBeTruthy()

    // 直接调 formatter 拿 ElButton vnode（jsdom 里 ElTable 不渲染行单元格）。
    const vnode = op!.formatter!(PROJECTS[0] as never) as {
      type: unknown
      props?: { onClick?: () => void }
    }
    expect(vnode.props?.onClick, '工作台按钮应带跳转').toBeTruthy()
    vnode.props!.onClick!()
    await new Promise((r) => setTimeout(r, 0))

    expect(currentRouter!.currentRoute.value.name).toBe('DockerProjectWorkspace')
    expect(currentRouter!.currentRoute.value.params.name).toBe('uni-center')
    expect(currentRouter!.currentRoute.value.query.host).toBe('h1')
  })

  it('无 docker:inspect 权限时入口不渲染（不渲染 ≠ 禁用）', async () => {
    auth.allow = new Set(['docker:list'])
    const w = await mountPage()
    const op = tableColumns(w).find((c) => c.prop === 'operation')
    expect(op?.formatter!(PROJECTS[0] as never)).toBeNull()
  })
})

describe('空态与主机上下文', () => {
  it('该主机上没有项目：如实说，不冒充加载失败', async () => {
    api.fetchDockerState.mockResolvedValue({
      lastSync: 1790000000,
      stale: false,
      ageSeconds: 3,
      neverReported: false,
      dockerOk: true,
      containers: [],
      images: [],
      volumes: [],
      networks: [],
      projects: []
    })
    const w = await mountPage({ host: 'h1' })
    expect(w.html()).toContain('该主机上还没有项目')
  })

  it('快照按 query host 拉取（host-context 的唯一事实源）', async () => {
    await mountPage({ host: 'h1' })
    expect(api.fetchDockerState).toHaveBeenCalledWith('h1')
  })
})
