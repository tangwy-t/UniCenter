// @vitest-environment jsdom
/**
 * 页面渲染冒烟：把每个页面**真正挂起来跑一遍 setup + 首次渲染**。
 *
 * 为什么必须有它（2026-09-29 的教训）：本模块此前所有测试都是纯函数 / 源码扫描，
 * 从不真正渲染页面 —— 于是「页面 setup 抛错 → 内容区整块空白」这类**只有在渲染时
 * 才会发生**的故障一路溜到生产：五个列表页在页内 `provideDockerHost()` 之后又
 * `inject` 自己（Vue 的 inject 读的是 parent.provides），setup 抛错、整页白屏，
 * 而 692 条单测全绿。
 *
 * 这类故障的判据很硬：**mount 抛不抛错**。断言因此很简单 —— 挂载成功 + 页面根
 * 节点在 + 关键文案在。渲染一个页面比断言十条实现细节更能防住「白屏」。
 *
 * 只桩掉两类外部依赖：`../api`（网络）与 `@/hooks/core/useAuth`（权限）。
 * **不桩 host-context** —— 它正是出事的那个东西。Art* 全局组件用轻量替身
 * （真实组件靠 unplugin 自动注册，测试环境里没有；替身保留 slot 以便页面内容
 * 仍然被渲染出来）。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, nextTick, type Component } from 'vue'
import { ElTable } from 'element-plus'
import { mount, type VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'

const api = vi.hoisted(() => ({
  fetchDockerHosts: vi.fn(),
  fetchDockerState: vi.fn(),
  sendDockerCmd: vi.fn(),
  fetchDockerCmdResult: vi.fn()
}))
vi.mock('../api', () => ({
  ...api,
  // 类型别名在运行期不存在，这里只需函数面
  default: undefined
}))
vi.mock('@/hooks/core/useAuth', () => ({
  useAuth: () => ({ hasAuth: () => true, hasAnyAuth: () => true })
}))

import Containers from '../views/containers.vue'
import Images from '../views/images.vue'
import Volumes from '../views/volumes.vue'
import Networks from '../views/networks.vue'
import Projects from '../views/projects.vue'
import ContainerDetail from '../views/container-detail.vue'
import ImageDetail from '../views/image-detail.vue'
import { BREAKPOINTS } from '@/config/breakpoints'
import { filterColumnsForViewport } from '@/components/core/tables/responsive-columns'
import type { ColumnOption } from '@/types/component'

const HOSTS = {
  list: [
    {
      id: 'h1',
      hostname: 'bogon',
      primaryIp: '192.168.12.105',
      online: true,
      dockerOk: true,
      agentVersion: '0.5.4'
    }
  ]
}

const STATE = {
  lastSync: 1790600000,
  stale: false,
  ageSeconds: 3,
  neverReported: false,
  dockerOk: true,
  compose: { flavor: 'plugin', version: 'v2.27.0' },
  containers: [
    {
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
      protected: true
    }
  ],
  images: [
    {
      id: 'i1',
      repoTags: ['uni-center-core:latest'],
      sizeMb: 91.8,
      inUse: true,
      dangling: false,
      inUseBy: ['uni-center-core']
    }
  ],
  volumes: [
    { name: 'uni-center_uploads', driver: 'local', sizeMb: 12, inUse: true, protected: true }
  ],
  networks: [{ name: 'uni-center_default', driver: 'bridge', scope: 'local', containersCount: 2 }],
  projects: [
    {
      name: 'uni-center',
      configFiles: ['/data/UniCenter/docker-compose.yml'],
      state: 'running',
      services: 2,
      containersCount: 2,
      protected: true
    }
  ]
}

/** Art* 全局组件替身：保留 slot，页面内容仍会被渲染（否则断言看不到页面结构）。 */
const passthrough = (name: string) =>
  defineComponent({
    name,
    setup:
      (_, { slots }) =>
      () =>
        h('div', { 'data-stub': name }, [slots.default?.(), slots.left?.(), slots.table?.()])
  })

/**
 * ArtTable 的替身必须**真的提供一个表格上下文**：项目页把 `ElTableColumn` 直接写在
 * 它的默认插槽里（ArtTable 支持 el-table 的插槽），而 `ElTableColumn` 依赖父表格的
 * provide —— 用纯透传替身会让它 inject 失败（`Cannot use 'in' operator … in null`），
 * 那是测试假象而不是页面缺陷。故这里用真 `ElTable` 包一层，并把页面传的 data 转下去。
 */
const artTableStub = defineComponent({
  name: 'ArtTable',
  inheritAttrs: false,
  setup:
    (_, { slots, attrs }) =>
    () =>
      h(
        ElTable,
        { ...attrs, data: (attrs.data as unknown[]) ?? [] },
        { default: () => slots.default?.() }
      )
})

const STUBS = {
  ArtTable: artTableStub,
  ArtTableHeader: passthrough('ArtTableHeader'),
  ArtSearchBar: passthrough('ArtSearchBar'),
  ArtButtonTable: passthrough('ArtButtonTable'),
  ArtButtonMore: passthrough('ArtButtonMore'),
  ArtIconButton: passthrough('ArtIconButton'),
  ArtPageContent: passthrough('ArtPageContent')
}

async function makeRouter(query: Record<string, string> = {}): Promise<Router> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/docker/containers', component: Containers },
      { path: '/docker/containers/:id', component: ContainerDetail },
      { path: '/docker/images', component: Images },
      { path: '/docker/images/:id', component: ImageDetail },
      { path: '/docker/volumes', component: Volumes },
      { path: '/docker/networks', component: Networks },
      { path: '/docker/projects', component: Projects }
    ]
  })
  // 先落定路由再挂载：主机 id 来自 route.query（host-context 的唯一事实源），
  // 没 ready 时 query 还是空的，断言会读到 ''（主机上下文尚未落定）。
  await router.push({ path: '/', query })
  await router.isReady()
  return router
}

/** jsdom 里 Element Plus 的部分组件需要它。 */
beforeEach(() => {
  vi.stubGlobal(
    'ResizeObserver',
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
  )
  api.fetchDockerHosts.mockResolvedValue(HOSTS)
  api.fetchDockerState.mockResolvedValue(STATE)
  api.sendDockerCmd.mockResolvedValue({ ref: 'r1' })
  api.fetchDockerCmdResult.mockResolvedValue({ status: 'succeeded' })
})

/** 已挂载的页面：用例结束后逐个卸载，别把 Element Plus 的监听/定时器留给下一个用例
 *（悬着不放会让 vitest 进程在收尾时超时——实测过）。 */
const mounted: VueWrapper[] = []

afterEach(() => {
  for (const w of mounted.splice(0)) w.unmount()
})

/** 列表页的共同挂法：路由 query 带主机 id（host-context 的唯一事实源）。 */
async function mountPage(component: Component, query: Record<string, string> = { host: 'h1' }) {
  const router = await makeRouter(query)
  const wrapper = mount(component, {
    global: { plugins: [router], stubs: STUBS }
  })
  mounted.push(wrapper as VueWrapper)
  // 等 setup 里的首轮拉取（主机清单 + 快照）落定，再断言渲染结果
  await new Promise((r) => setTimeout(r, 0))
  await nextTick()
  return wrapper
}

describe('页面渲染冒烟（挂载即验证，白屏类故障的守卫）', () => {
  it('容器页：挂载成功并渲染出页面与表头', async () => {
    const w = await mountPage(Containers)
    expect(w.find('.docker-containers-page').exists()).toBe(true)
    expect(w.html()).toContain('个容器') // 计数文案在，说明模板渲染到了表格上方
  })

  it('镜像页：挂载成功并渲染出页面', async () => {
    const w = await mountPage(Images)
    expect(w.find('.docker-images-page').exists()).toBe(true)
  })

  it('数据卷页：挂载成功并渲染出页面', async () => {
    const w = await mountPage(Volumes)
    expect(w.find('.docker-volumes-page').exists()).toBe(true)
  })

  it('网络页：挂载成功并渲染出页面', async () => {
    const w = await mountPage(Networks)
    expect(w.find('.docker-networks-page').exists()).toBe(true)
  })

  it('项目页：挂载成功并渲染出页面', async () => {
    const w = await mountPage(Projects)
    expect(w.find('.docker-projects-page').exists()).toBe(true)
  })

  it('容器详情页：挂载成功（详情页同样 provide 后自用上下文）', async () => {
    const w = await mountPage(ContainerDetail, { host: 'h1', id: 'c1' })
    expect(w.html().length).toBeGreaterThan(0)
    expect(w.find('.container-detail').exists()).toBe(true)
  })

  it('镜像详情页：挂载成功', async () => {
    const w = await mountPage(ImageDetail, { host: 'h1', id: 'i1' })
    expect(w.html().length).toBeGreaterThan(0)
    expect(w.find('.imd').exists()).toBe(true)
  })
})

/* ── 各视口的列集合（手机横屏 / 平板 / 桌面） ─────────────────────
 * hideBelow 是**声明式**的：断点名写错、该留的列被藏、或某次重构把声明弄丢，
 * 现有测试一条都不会红（页面照样挂载、单测照样绿），只有真机上某个视口看不到
 * 关键列时才暴露。这里把「哪档视口能看到哪些列」钉成两条不变量：
 *   ① 关键列（行标识 / 状态 / 操作）在任何视口都必须可见；
 *   ② 列集合随视口变宽**单调不减**（不会出现「大屏反而少一列」的倒挂）。
 * 断言取的是页面真正传给 ArtTable 的列配置，过滤规则复用 responsive-columns 的同一函数
 * （纯函数本身由 components/core/tables/responsive-columns.test.ts 覆盖）。
 */
describe('列集合随视口分档（hideBelow 的不变量守卫）', () => {
  const WIDTHS = [500, 640, 768, 900, 1024, 1280, 1600]

  /** 某视口宽度下，页面实际会渲染的列（标签；无标签的结构列回退为 type） */
  const labelsAt = (wrapper: VueWrapper, width: number): string[] => {
    const table = wrapper.findComponent({ name: 'ArtTable' })
    expect(table.exists()).toBe(true)
    // 替身没有声明 props（用 inheritAttrs: false + attrs 渲染），故列配置落在 $attrs 里；
    // 真组件走 $props，两处都读一次，替身换成真组件也不会失效。
    const vm = table.vm as unknown as {
      $attrs: Record<string, unknown>
      $props: Record<string, unknown>
    }
    const columns = (vm.$props.columns ?? vm.$attrs.columns ?? []) as ColumnOption<unknown>[]
    expect(columns.length, '页面没有把列配置传给 ArtTable').toBeGreaterThan(0)
    return filterColumnsForViewport(columns, (name) => width < BREAKPOINTS[name]).map((col) =>
      String(col.label ?? col.type)
    )
  }

  /** ①关键列恒在 + ②随视口单调不减 */
  const assertInvariants = (wrapper: VueWrapper, essential: string[]) => {
    const sets = WIDTHS.map((width) => {
      const labels = labelsAt(wrapper, width)
      for (const key of essential) {
        expect(labels, `${width}px 下缺少关键列「${key}」`).toContain(key)
      }
      return new Set(labels)
    })
    for (let i = 1; i < WIDTHS.length; i++) {
      for (const kept of sets[i - 1]!) {
        expect(
          sets[i]!.has(kept),
          `${WIDTHS[i]}px 比更窄的 ${WIDTHS[i - 1]}px 少了一列「${kept}」（阈值写反了？）`
        ).toBe(true)
      }
    }
  }

  it('容器页：名称/状态/操作恒在，网络吞吐只在桌面档出现', async () => {
    const w = (await mountPage(Containers)) as VueWrapper
    assertInvariants(w, ['名称', '状态', '操作'])
    expect(labelsAt(w, 640)).not.toContain('网络')
    expect(labelsAt(w, 1024)).toContain('网络')
  })

  it('镜像页：仓库:标签/使用/操作恒在，大小与创建时间窄屏让位', async () => {
    const w = (await mountPage(Images)) as VueWrapper
    assertInvariants(w, ['仓库:标签', '使用', '操作'])
    expect(labelsAt(w, 640)).not.toContain('大小')
    expect(labelsAt(w, 640)).not.toContain('创建于')
    expect(labelsAt(w, 1024)).toContain('大小')
  })

  it('数据卷页：名称/使用/操作恒在，驱动与大小窄屏让位', async () => {
    const w = (await mountPage(Volumes)) as VueWrapper
    assertInvariants(w, ['名称', '使用', '操作'])
    expect(labelsAt(w, 640)).not.toContain('驱动')
    expect(labelsAt(w, 1024)).toContain('驱动')
  })

  it('网络页：名称/内部网络/操作恒在（内部网络决定容器能否出网）', async () => {
    const w = (await mountPage(Networks)) as VueWrapper
    assertInvariants(w, ['名称', '内部网络', '操作'])
    expect(labelsAt(w, 640)).not.toContain('容器数')
    expect(labelsAt(w, 640)).not.toContain('驱动')
    expect(labelsAt(w, 768)).toContain('驱动')
  })
})

describe('主机上下文：同组件 provide 之后自用（本次白屏的根因，逐条钉住）', () => {
  it('页面组件内 provideDockerHost() 后调 useDockerHost() 能拿到上下文', async () => {
    const { provideDockerHost, useDockerHost } = await import('../utils/host-context')
    let seen: string | null = null
    const Probe = defineComponent({
      setup() {
        const ctx = provideDockerHost()
        const again = useDockerHost()
        seen = again === ctx ? '同组件可自取' : '取到的是另一个对象'
        return () => h('div')
      }
    })
    const router = await makeRouter({ host: 'h1' })
    mount(Probe, { global: { plugins: [router] } })
    expect(seen).toBe('同组件可自取')
  })

  it('无参 useDockerCmds 在组件内定住主机：点击期（无实例）仍能发出指令', async () => {
    const { provideDockerHost } = await import('../utils/host-context')
    const { useDockerCmds } = await import('../composables/useDockerCmds')
    // 指令是在事件回调里发出的 —— 那一刻没有组件实例，任何「那时才 inject」的写法
    // 都会抛错。这条用例把 run() 拿到 setup 之外来调，正是模拟点击。
    let runOnce: (() => Promise<{ ok: boolean }>) | null = null
    const Probe = defineComponent({
      setup() {
        provideDockerHost()
        const { run } = useDockerCmds()
        runOnce = async () => run({ action: 'container:start', target: 'mysql', key: 'mysql' })
        return () => h('div')
      }
    })
    const router = await makeRouter({ host: 'h1' })
    mount(Probe, { global: { plugins: [router] } })
    await new Promise((r) => setTimeout(r, 0)) // 等 route.query 里的主机落定

    const res = await runOnce!()
    expect(api.sendDockerCmd).toHaveBeenCalledWith(
      'h1',
      expect.objectContaining({ action: 'container:start' })
    )
    expect(res.ok).toBe(true)
  })
})
