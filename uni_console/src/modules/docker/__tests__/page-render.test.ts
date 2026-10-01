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
  fetchDockerContainers: vi.fn(),
  sendDockerCmd: vi.fn(),
  fetchDockerCmdResult: vi.fn(),
  // 4b 拉取进度对话框（images 页挂载）与 P2 构建/推送对话框（镜像 tab / 镜像
  // 详情挂载）不会真开流，但 import 面必须齐全。
  openDockerPullStream: vi.fn(),
  openDockerBuildStream: vi.fn(),
  openDockerPushStream: vi.fn(),
  // P3 构建上下文上传（构建对话框的上传形态；冒烟里不触发，import 面补齐）。
  uploadDockerBuildContext: vi.fn(),
  // 4c 凭据面（pull 对话框的下拉与凭据管理对话框；页面冒烟里不会真调）。
  fetchDockerRegistries: vi.fn(),
  // 6b 任务中心抽屉（docker-page 主机条入口）挂载但不开 —— import 面必须齐全。
  fetchDockerTasks: vi.fn()
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
import Resources from '../views/resources.vue'
import Projects from '../views/projects.vue'
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
  ArtPageContent: passthrough('ArtPageContent'),
  // resources 页 tab 标签里的图标（真组件靠 unplugin 注册；替身只保留占位 DOM）。
  ArtSvgIcon: passthrough('ArtSvgIcon')
}

async function makeRouter(query: Record<string, string> = {}): Promise<Router> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/docker/containers', component: Containers },
      // 7a：镜像/数据卷/网络三页收敛为 /docker/resources 的三个 tab（query.tab 记当前 tab）。
      { path: '/docker/resources', component: Resources },
      { path: '/docker/image-detail/:id', component: ImageDetail },
      { path: '/docker/projects', component: Projects }
    ]
  })
  // 先落定路由再挂载：主机 id 来自 route.query（host-context 的唯一事实源），
  // 没 ready 时 query 还是空的，断言会读到 ''（主机上下文尚未落定）。
  await router.push({ path: '/', query })
  await router.isReady()
  return router
}

/** 统一工作负载表（切片 2 起容器页的数据源）：两台主机的条目。 */
const WORKLOADS = {
  items: [
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
      protected: true,
      hostId: 'h1',
      hostname: 'bogon'
    },
    {
      id: 'c2',
      name: 'mysql',
      image: 'mysql:8',
      state: 'exited',
      statusText: 'Exited (0) 8 months ago',
      cpuPercent: 0,
      memUsageMb: 0,
      memLimitMb: 0,
      netRxBytesSec: 0,
      netTxBytesSec: 0,
      protected: false,
      hostId: 'h2',
      hostname: 'nas'
    }
  ],
  total: 2
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
  api.fetchDockerContainers.mockResolvedValue(WORKLOADS)
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
  it('容器页：挂载成功并渲染出页面与表头（切片 2：统一表数据源）', async () => {
    const w = await mountPage(Containers)
    expect(w.find('.docker-containers-page').exists()).toBe(true)
    expect(w.html()).toContain('个容器') // 计数文案在，说明模板渲染到了表格上方
  })

  it('镜像与存储页（7a）：挂载成功并渲染出页面与镜像 tab（默认）', async () => {
    const w = await mountPage(Resources)
    expect(w.find('.docker-resources-page').exists()).toBe(true)
    // 三个 tab 的导航条在（图标 + 文案），默认激活镜像 tab。
    const labels = w.findAll('.docker-resources-tab-label').map((n) => n.text())
    expect(labels).toEqual(['镜像', '数据卷', '网络'])
    expect(w.findComponent({ name: 'DockerImagesTab' }).exists()).toBe(true)
  })

  it('镜像与存储页：query.tab=volumes/networks 落到对应 tab（深链/刷新还原）', async () => {
    const w = await mountPage(Resources, { host: 'h1', tab: 'volumes' })
    expect(w.findComponent({ name: 'DockerVolumesTab' }).exists()).toBe(true)
    // lazy：未激活的 tab 不渲染（镜像 tab 此刻只是导航条上的一个名字）。
    expect(w.findComponent({ name: 'DockerImagesTab' }).exists()).toBe(false)

    const w2 = await mountPage(Resources, { host: 'h1', tab: 'networks' })
    expect(w2.findComponent({ name: 'DockerNetworksTab' }).exists()).toBe(true)
  })

  it('项目页（7b 薄索引）：挂载成功并渲染出页面与行数据', async () => {
    const w = await mountPage(Projects)
    expect(w.find('.docker-projects-page').exists()).toBe(true)
    // 索引页的行数据：STATE 快照里的 uni-center 项目进了 ArtTable（jsdom 里 ElTable
    // 不渲染行单元格，从传给表格的 data 断言 —— workloads.test 同款口径）。
    const table = w.findComponent({ name: 'ArtTable' })
    const rows = ((table.vm as unknown as { $attrs: Record<string, unknown> }).$attrs.data ??
      []) as { name?: string }[]
    expect(rows.map((r) => r.name)).toEqual(['uni-center'])
  })

  it('镜像详情页：挂载成功', async () => {
    const w = await mountPage(ImageDetail, { host: 'h1', id: 'i1' })
    expect(w.html().length).toBeGreaterThan(0)
    expect(w.find('.imd').exists()).toBe(true)
  })
})

/* ── 快照拉取失败的页头口径（D-1）─────────────────────────────────
 * 修复前：loadState 失败把 state 清成 null，页头回落到「刚刚同步」——网络失败
 * 被渲染成最新鲜状态。这里从**真正挂起来的页面**上断言两条：
 *   ① 首拉失败 → 页头是「数据获取失败」，且不再是「刚刚同步」；
 *   ② 刷新失败（已握有快照）→ 页头保留「同步于 N 前」并标注本次刷新失败，
 *      表格数据不清空（最后已知数据仍可见）。
 *
 * 切片 2 起容器页的数据源换成统一表（fetchDockerContainers）。7a 起旧镜像页收敛为
 * resources 页的镜像 tab —— 快照链路（fetchDockerState + DockerPage 页头）由
 * **页面级**的 useDockerHostState 承载（三 tab 共享一份），守卫落到 resources 页。
 * 统一表自己的失败口径（首拉整页错误态 / 刷新失败保留数据）在 workloads.test.ts。
 */
describe('快照拉取失败时的页头同步文案（D-1 守卫）', () => {
  it('① 首拉失败：页头给失败结论句，不显示「刚刚同步」', async () => {
    api.fetchDockerState.mockRejectedValue(new Error('network down'))
    const w = await mountPage(Resources)

    const text = w.find('.docker-page__sync').text()
    expect(text).toBe('数据获取失败')
    expect(text).not.toContain('刚刚同步')
  })

  it('② 刷新失败：页头标注本次刷新失败，表格保留最后已知数据', async () => {
    const w = await mountPage(Resources) // 首拉成功（beforeEach 的 STATE，ageSeconds=3）
    expect(w.find('.docker-page__sync').text()).toBe('同步于 3 秒前')

    api.fetchDockerState.mockRejectedValueOnce(new Error('network down'))
    const refreshBtn = w.findAll('button').find((b) => b.text().includes('刷新'))
    expect(refreshBtn).toBeTruthy()
    await refreshBtn!.trigger('click')
    await new Promise((r) => setTimeout(r, 0))
    await nextTick()

    expect(w.find('.docker-page__sync').text()).toBe('同步于 3 秒前，本次刷新失败')
    // state 未被清空：喂给表格的仍是上一批数据（最后已知数据）。jsdom 里 ElTable
    // 不渲染行单元格，故从页面传给 ArtTable 的 data 断言（labelsAt 同款口径）。
    const table = w.findComponent({ name: 'ArtTable' })
    const rows = ((table.vm as unknown as { $attrs: Record<string, unknown> }).$attrs.data ??
      []) as { id?: string }[]
    expect(rows.length).toBe(1)
    expect(rows[0]?.id).toBe('i1')
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

  it('容器页：名称/状态/操作恒在，主机列平板档起出现，网络吞吐只在桌面档', async () => {
    const w = (await mountPage(Containers)) as VueWrapper
    // 切片 2 的基线变更：统一表新增「主机」列（hideBelow tablet）——跨主机表的
    // 行归属是排查第一线索，与镜像/端口同档（平板竖屏起可见）。守卫本身不删，
    // 列集合断言逐字对齐新集合。
    assertInvariants(w, ['名称', '状态', '操作'])
    expect(labelsAt(w, 640)).not.toContain('网络')
    expect(labelsAt(w, 640)).not.toContain('主机')
    expect(labelsAt(w, 768)).toContain('主机')
    expect(labelsAt(w, 1024)).toContain('网络')
  })

  it('镜像 tab：仓库:标签/使用/操作恒在，大小与创建时间窄屏让位', async () => {
    const w = (await mountPage(Resources)) as VueWrapper
    assertInvariants(w, ['仓库:标签', '使用', '操作'])
    expect(labelsAt(w, 640)).not.toContain('大小')
    expect(labelsAt(w, 640)).not.toContain('创建于')
    expect(labelsAt(w, 1024)).toContain('大小')
  })

  it('数据卷 tab：名称/使用/操作恒在，驱动与大小窄屏让位', async () => {
    const w = (await mountPage(Resources, { host: 'h1', tab: 'volumes' })) as VueWrapper
    assertInvariants(w, ['名称', '使用', '操作'])
    expect(labelsAt(w, 640)).not.toContain('驱动')
    expect(labelsAt(w, 1024)).toContain('驱动')
  })

  it('网络 tab：名称/内部网络/操作恒在（内部网络决定容器能否出网）', async () => {
    const w = (await mountPage(Resources, { host: 'h1', tab: 'networks' })) as VueWrapper
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
