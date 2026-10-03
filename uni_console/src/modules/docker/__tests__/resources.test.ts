// @vitest-environment jsdom
/**
 * 镜像与存储页（9b：跨主机化）的行为面 —— 这一页存在的理由就是这些行为：
 *
 *   - tab 切换写 URL query（query 记忆：深链/刷新/分享还原当前 tab）；
 *   - **聚合数据源**：三个 tab 各拉自己的跨主机端点（GET /docker/images|
 *     volumes|networks），切 tab 不重拉（ElTabPane lazy 首挂后常驻、v-show 隐藏）；
 *   - **页面级一份主机清单**：三 tab 共用（fetchDockerHosts 恰好一次），
 *     主机筛选下拉与「尚无可管主机」判定都从它来；
 *   - `?host=` 深链作**主机筛选初始值**（单向：用户改筛选不回写 query）；
 *   - 筛选全部作为 query 发给端点（服务端过滤），重置清空；
 *   - D-1 的 tab 对应面：首拉失败 → 错误块 + 重试；刷新失败 → 保留最后已知行 + 标注；
 *   - 行内下钻语义保持：镜像「详情」带**行主机**跳单主机窗口的镜像详情页。
 *
 * 同页面的另一半守卫在 page-render.test.ts（渲染冒烟/列集合）。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, nextTick, type Component } from 'vue'
import { ElTable } from 'element-plus'
import { mount, type VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'

const api = vi.hoisted(() => ({
  fetchDockerHosts: vi.fn(),
  fetchDockerImages: vi.fn(),
  fetchDockerVolumes: vi.fn(),
  fetchDockerNetworks: vi.fn(),
  sendDockerCmd: vi.fn(),
  fetchDockerCmdResult: vi.fn(),
  // 拉取/构建进度对话框与凭据对话框（镜像 tab 挂载）不会真开流，但 import 面必须齐全。
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

import Resources from '../views/resources.vue'

const HOSTS = {
  list: [
    {
      id: 'h1',
      hostname: 'bogon',
      primaryIp: '192.168.12.105',
      online: true,
      dockerOk: true,
      agentVersion: '0.5.4'
    },
    {
      id: 'h2',
      hostname: 'nas',
      primaryIp: '192.168.12.106',
      online: true,
      dockerOk: true,
      agentVersion: '0.5.4'
    }
  ]
}

/** 跨主机聚合端点的响应形态：条目内嵌单主机字段 + 归属两列（hostId/hostname）。 */
const IMAGES = {
  items: [
    {
      id: 'i1',
      repoTags: ['uni-center-core:latest'],
      sizeMb: 91.8,
      inUse: true,
      dangling: false,
      hostId: 'h1',
      hostname: 'bogon'
    },
    {
      id: 'i2',
      repoTags: ['mysql:8.0'],
      sizeMb: 596.2,
      inUse: false,
      dangling: false,
      hostId: 'h2',
      hostname: 'nas'
    }
  ],
  total: 2
}

const VOLUMES = {
  items: [
    {
      name: 'uni-center_uploads',
      driver: 'local',
      sizeMb: 12,
      inUse: true,
      protected: true,
      hostId: 'h1',
      hostname: 'bogon'
    }
  ],
  total: 1
}

const NETWORKS = {
  items: [
    {
      name: 'uni-center_default',
      driver: 'bridge',
      scope: 'local',
      internal: false,
      containersCount: 2,
      hostId: 'h2',
      hostname: 'nas'
    }
  ],
  total: 1
}

/** Art* 全局组件替身（真组件靠 unplugin 注册，测试环境里没有；保住 slot）。 */
const passthrough = (name: string) =>
  defineComponent({
    name,
    setup:
      (_, { slots }) =>
      () =>
        h('div', { 'data-stub': name }, [slots.default?.(), slots.left?.()])
  })

/** ArtSearchBar 的替身换成一个**真输入**：v-model 双向接线，让用例能设置/读取
 *  筛选表单（「切 tab 保留筛选」看它；主机/开关类筛选直接 $emit 表单对象）。 */
const artSearchBarStub = defineComponent({
  name: 'ArtSearchBar',
  props: {
    modelValue: { type: Object, default: () => ({}) },
    items: { type: Array, default: () => [] }
  },
  emits: ['update:modelValue', 'search', 'reset'],
  setup(props, { emit }) {
    return () =>
      h('input', {
        class: 'test-search-input',
        value: (props.modelValue as { keyword?: string })?.keyword ?? '',
        onInput: (e: Event) =>
          emit('update:modelValue', {
            ...props.modelValue,
            keyword: (e.target as HTMLInputElement).value
          })
      })
  }
})

/** ArtTable 的替身用真 ElTable 包一层（ElTableColumn 依赖父表格 provide；同时把
 *  data/columns 落在 attrs 里供断言 —— workloads.test 同款口径）。 */
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
  ArtSearchBar: artSearchBarStub,
  ArtTableHeader: passthrough('ArtTableHeader'),
  ArtButtonTable: passthrough('ArtButtonTable'),
  ArtButtonMore: passthrough('ArtButtonMore'),
  ArtIconButton: passthrough('ArtIconButton'),
  ArtPageContent: passthrough('ArtPageContent'),
  ArtSvgIcon: passthrough('ArtSvgIcon')
}

async function makeRouter(query: Record<string, string> = {}): Promise<Router> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/docker/resources', component: Resources },
      {
        path: '/docker/image-detail/:id',
        name: 'DockerImageDetail',
        component: { template: '<div />' }
      }
    ]
  })
  await router.push({ path: '/', query })
  await router.isReady()
  return router
}

/** 已挂载的页面：用例结束后逐个卸载（Element Plus 的监听/定时器不留给下个用例）。 */
const mounted: VueWrapper[] = []

afterEach(() => {
  for (const w of mounted.splice(0)) w.unmount()
})

/** 导航链跨几个微任务（tab 点击 → watch → router.replace），用宏任务兜平。 */
const flush = async () => {
  await new Promise((r) => setTimeout(r, 0))
  await nextTick()
}

/**
 * 等 query.tab 落到期望值：tab→query 的写回是一次**异步导航**（watch → replace），
 * 判据用「路由到没到」而不是「过了几轮宏任务」—— 管线里若有别的导航在飞，步数会漂。
 */
async function waitTab(router: Router, tab: string) {
  await vi.waitUntil(() => String(router.currentRoute.value.query.tab ?? '') === tab, {
    timeout: 5000
  })
}

async function mountResources(query: Record<string, string> = { host: 'h1' }) {
  const router = await makeRouter(query)
  const wrapper = mount(Resources as unknown as Component, {
    global: { plugins: [router], stubs: STUBS }
  })
  mounted.push(wrapper as VueWrapper)
  await flush()
  return { wrapper: wrapper as VueWrapper, router }
}

/** 点 tab 导航条上的第 n 项（Element Plus 的 tab item 对 click 语义友好）。 */
async function clickTab(w: VueWrapper, label: string) {
  const item = w.findAll('.el-tabs__item').find((n) => n.text().includes(label))
  expect(item, `找不到 tab 导航项「${label}」`).toBeTruthy()
  await item!.trigger('click')
  await flush()
}

/** 某 tab 组件内的 ArtSearchBar 替身（按 tab 收窄查找范围）。 */
function searchBarOf(w: VueWrapper, tabName: string) {
  const tab = w.findComponent({ name: tabName })
  expect(tab.exists(), `${tabName} 未挂载`).toBe(true)
  const bar = tab.findComponent({ name: 'ArtSearchBar' })
  expect(bar.exists(), `${tabName} 里没有搜索栏`).toBe(true)
  return bar
}

/** 某 tab 传给 ArtTable 的列配置（替身把 columns 落在 $attrs）。 */
function columnsOf(w: VueWrapper, tabName: string) {
  const table = w.findComponent({ name: tabName }).findComponent({ name: 'ArtTable' })
  expect(table.exists(), `${tabName} 里没有表格`).toBe(true)
  return ((table.vm as unknown as { $attrs: Record<string, unknown> }).$attrs.columns ?? []) as {
    prop?: string
    label?: string
    hideBelow?: string
    formatter?: (row: never) => unknown
  }[]
}

/** 镜像 tab 的筛选输入（ArtSearchBar 替身渲染的真输入）。 */
const imagesInput = (w: VueWrapper) =>
  searchBarOf(w, 'DockerImagesTab').find('input.test-search-input')

const keywordOf = (w: VueWrapper): string => (imagesInput(w).element as HTMLInputElement).value

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
  api.fetchDockerHosts.mockResolvedValue(HOSTS)
  api.fetchDockerImages.mockResolvedValue(IMAGES)
  api.fetchDockerVolumes.mockResolvedValue(VOLUMES)
  api.fetchDockerNetworks.mockResolvedValue(NETWORKS)
})

describe('tab 切换与 query 记忆', () => {
  it('点 tab 写回 query.tab（replace 不进历史），内容随 tab 切换', async () => {
    const { wrapper: w, router } = await mountResources()
    expect(router.currentRoute.value.query.tab).toBeUndefined()

    await clickTab(w, '数据卷')
    await waitTab(router, 'volumes')
    expect(router.currentRoute.value.query.tab).toBe('volumes')
    expect(w.findComponent({ name: 'DockerVolumesTab' }).exists()).toBe(true)
    // lazy 首挂后常驻：镜像 tab 已挂载过，切走只是 v-show 隐藏（实例还在）。
    expect(w.findComponent({ name: 'DockerImagesTab' }).exists()).toBe(true)

    await clickTab(w, '网络')
    await waitTab(router, 'networks')
    expect(router.currentRoute.value.query.tab).toBe('networks')
    expect(w.findComponent({ name: 'DockerNetworksTab' }).exists()).toBe(true)

    await clickTab(w, '镜像')
    await waitTab(router, 'images')
    expect(router.currentRoute.value.query.tab).toBe('images')
  })

  it('深链进入：query.tab 落到对应 tab，非法值回落镜像 tab', async () => {
    const { wrapper: w } = await mountResources({ host: 'h1', tab: 'networks' })
    expect(w.findComponent({ name: 'DockerNetworksTab' }).exists()).toBe(true)

    const { wrapper: w2 } = await mountResources({ host: 'h1', tab: '不存在的tab' })
    expect(w2.findComponent({ name: 'DockerImagesTab' }).exists()).toBe(true)
  })
})

describe('聚合数据源（9b：跨主机端点 + 服务端过滤）', () => {
  it('镜像 tab 首拉：?host 深链为主机筛选初始值，三个筛选参数全部发给端点', async () => {
    await mountResources({ host: 'h1' })
    expect(api.fetchDockerImages).toHaveBeenCalledWith({
      hostId: 'h1',
      keyword: undefined,
      dangling: undefined,
      unused: undefined
    })
  })

  it('不带 host 的进入是全部主机首拉（hostId 不发）', async () => {
    await mountResources({})
    expect(api.fetchDockerImages).toHaveBeenCalledWith(
      expect.objectContaining({ hostId: undefined })
    )
  })

  it('三个 tab 全逛一遍：每个聚合端点各拉恰好一次，主机清单页面级一份只拉一次', async () => {
    const { wrapper: w } = await mountResources()
    await clickTab(w, '数据卷')
    await clickTab(w, '网络')
    await clickTab(w, '镜像')

    // 切 tab 不重拉：各 tab 的实例常驻（lazy 首挂后 v-show 隐藏）。
    expect(api.fetchDockerImages).toHaveBeenCalledTimes(1)
    expect(api.fetchDockerVolumes).toHaveBeenCalledTimes(1)
    expect(api.fetchDockerNetworks).toHaveBeenCalledTimes(1)
    // 主机清单是页面级一份（收敛收益在跨主机形态下的延续）。
    expect(api.fetchDockerHosts).toHaveBeenCalledTimes(1)
  })

  it('搜索时 keyword/开关/主机三项透传（开关只发 true）', async () => {
    const { wrapper: w } = await mountResources()
    api.fetchDockerImages.mockClear()

    const bar = searchBarOf(w, 'DockerImagesTab')
    bar.vm.$emit('update:modelValue', { keyword: 'mysql', danglingOnly: true, host: 'h2' })
    bar.vm.$emit('search')
    await flush()

    expect(api.fetchDockerImages).toHaveBeenCalledWith({
      hostId: 'h2',
      keyword: 'mysql',
      dangling: true,
      unused: undefined
    })
  })

  it('重置清空三项参数（undefined 不发）', async () => {
    const { wrapper: w } = await mountResources({ host: 'h2' }) // 先带一个 host 深链
    api.fetchDockerImages.mockClear()

    const bar = searchBarOf(w, 'DockerImagesTab')
    bar.vm.$emit('update:modelValue', {})
    bar.vm.$emit('reset')
    await flush()

    expect(api.fetchDockerImages).toHaveBeenCalledWith({
      hostId: undefined,
      keyword: undefined,
      dangling: undefined,
      unused: undefined
    })
  })
})

describe('底栏合计（可见行与后端账目的两种口径）', () => {
  it('「可回收」读后端账目求和，不是 Σ 可见悬空行 SizeMB（对账修）', async () => {
    // 可见行里有 1 张 1200MB 的悬空镜像（旧口径会把这 1200MB 当「可回收」），
    // 账目（df 对账的独占层）故意另给一套小数字：底栏必须说账目这套
    //（promise 的是 prune 真会释放的量，共享层不算）。
    api.fetchDockerImages.mockResolvedValue({
      items: [
        ...IMAGES.items,
        {
          id: 'i3',
          repoTags: [],
          sizeMb: 1200,
          inUse: false,
          dangling: true,
          hostId: 'h1',
          hostname: 'bogon'
        }
      ],
      total: 3,
      disk: [
        { hostId: 'h1', danglingCount: 1, danglingMb: 0.002 },
        { hostId: 'h2', danglingCount: 2, danglingMb: 12.5 }
      ]
    })
    const { wrapper: w } = await mountResources()

    // 主行仍是可见清单的合计（Σ 条目 SizeMB：91.8 + 596.2 + 1200 = 1888）。
    expect(w.find('.docker-total').text()).toContain('合计 3 个 · 1.84 GB')
    // 副行是账目求和（0.002 + 12.5 = 12.502 → 12.5 MB），不是 1.2 GB。
    const sub = w.find('.docker-total__sub').text()
    expect(sub).toContain('3 个可回收')
    expect(sub).toContain('12.5 MB')
    expect(sub).not.toContain('1.2 GB')
  })

  it('账目跟着主机筛选范围走（服务端按 hostId 收窄 → 底栏只算那台）', async () => {
    api.fetchDockerImages.mockImplementation((p: { hostId?: string } = {}) =>
      Promise.resolve({
        ...IMAGES,
        disk:
          p.hostId === 'h2'
            ? [{ hostId: 'h2', danglingCount: 2, danglingMb: 12.5 }]
            : [
                { hostId: 'h1', danglingCount: 1, danglingMb: 0.002 },
                { hostId: 'h2', danglingCount: 2, danglingMb: 12.5 }
              ]
      })
    )
    const { wrapper: w } = await mountResources({ host: 'h2' })
    expect(api.fetchDockerImages).toHaveBeenCalledWith(expect.objectContaining({ hostId: 'h2' }))
    const sub = w.find('.docker-total__sub').text()
    expect(sub).toContain('2 个可回收')
    expect(sub).toContain('12.5 MB')
  })

  it('没有任何主机报账（disk 为空）→ 副行说「不可用」，不折算成 0', async () => {
    api.fetchDockerImages.mockResolvedValue({ ...IMAGES, disk: [] })
    const { wrapper: w } = await mountResources()
    const sub = w.find('.docker-total__sub').text()
    expect(sub).toContain('可回收账目不可用')
    expect(sub).not.toContain('0 个可回收')
  })
})

describe('主机列 + 主机筛选（跨主机形态的核心新增）', () => {
  it('镜像 tab：行带归属两列，表列里「主机」在平板竖屏起可见', async () => {
    const { wrapper: w } = await mountResources()
    const table = w.findComponent({ name: 'DockerImagesTab' }).findComponent({ name: 'ArtTable' })
    const rows = ((table.vm as unknown as { $attrs: Record<string, unknown> }).$attrs.data ??
      []) as { hostname?: string }[]
    // 两台主机的镜像进同一张表，逐行带归属。
    expect(rows.map((r) => r.hostname)).toEqual(['bogon', 'nas'])

    const hostCol = columnsOf(w, 'DockerImagesTab').find((c) => c.label === '主机')
    expect(hostCol, '镜像表缺少「主机」列').toBeTruthy()
    expect(hostCol?.prop).toBe('hostname')
    expect(hostCol?.hideBelow).toBe('tablet')
  })

  it('主机筛选下拉来自页面级主机清单（全部主机 + 各台，label 用主机名）', async () => {
    const { wrapper: w } = await mountResources()
    const items = searchBarOf(w, 'DockerImagesTab').props('items') as {
      key: string
      filterable?: boolean
      options?: { label: string; value: string }[]
    }[]
    const host = items.find((i) => i.key === 'host')
    expect(host, '筛选面板缺少主机项').toBeTruthy()
    expect(host?.options).toEqual([
      { label: 'bogon', value: 'h1' },
      { label: 'nas', value: 'h2' }
    ])
    // 主机下拉可搜（filterable）：主机多了要能敲名字找（QA 实测不可搜）
    expect(host?.filterable).toBe(true)
  })

  it('深链 ?host=h2&tab=volumes：数据卷端点首拉就带 hostId（每个 tab 各自吃初值）', async () => {
    await mountResources({ host: 'h2', tab: 'volumes' })
    expect(api.fetchDockerVolumes).toHaveBeenCalledWith(expect.objectContaining({ hostId: 'h2' }))
  })

  it('网络 tab 的搜索栏同样是三件套（名称/仅内部/主机）', async () => {
    const { wrapper: w } = await mountResources({ host: 'h1', tab: 'networks' })
    const items = searchBarOf(w, 'DockerNetworksTab').props('items') as {
      key: string
      filterable?: boolean
    }[]
    expect(items.map((i) => i.key)).toEqual(['keyword', 'internalOnly', 'host'])
    // 主机下拉可搜：与镜像/卷 tab 同一处置（QA 实测不可搜）
    expect(items.find((i) => i.key === 'host')?.filterable).toBe(true)
  })
})

describe('切 tab 保留各 tab 的筛选（实例常驻，v-show 隐藏不销毁）', () => {
  it('输入翻页回来还在', async () => {
    const { wrapper: w } = await mountResources()
    await imagesInput(w).setValue('mysql')
    expect(keywordOf(w)).toBe('mysql')

    await clickTab(w, '数据卷')
    await clickTab(w, '镜像')
    // 筛选还在：切 tab 不是切页，用户逛一圈回来不该丢输入。
    expect(keywordOf(w)).toBe('mysql')
  })
})

describe('失败口径（D-1 的 tab 对应面）', () => {
  it('首拉失败：错误块 + 重试；重试成功后进表', async () => {
    api.fetchDockerImages.mockRejectedValueOnce(new Error('network down'))
    const { wrapper: w } = await mountResources()
    expect(w.find('.docker-tab-error').exists()).toBe(true)
    expect(w.text()).toContain('镜像清单获取失败')

    api.fetchDockerImages.mockResolvedValue(IMAGES)
    const retry = w.findAll('.docker-tab-error button').find((b) => b.text().includes('重试'))
    expect(retry, '错误块里应有重试按钮').toBeTruthy()
    await retry!.trigger('click')
    await flush()

    expect(w.find('.docker-tab-error').exists()).toBe(false)
    // 行数据进了表（jsdom 里 ElTable 不渲染行单元格，从传给 ArtTable 的 data 断言）。
    const table = w.findComponent({ name: 'DockerImagesTab' }).findComponent({ name: 'ArtTable' })
    const rows = ((table.vm as unknown as { $attrs: Record<string, unknown> }).$attrs.data ??
      []) as { hostname?: string }[]
    expect(rows.map((r) => r.hostname)).toEqual(['bogon', 'nas'])
  })

  it('刷新失败：保留最后已知行并标注（不把看完的清单换成错误屏）', async () => {
    const { wrapper: w } = await mountResources()
    const table = w.findComponent({ name: 'DockerImagesTab' }).findComponent({ name: 'ArtTable' })
    const rowsOf = () =>
      ((table.vm as unknown as { $attrs: Record<string, unknown> }).$attrs.data ?? []) as {
        hostname?: string
      }[]
    expect(rowsOf()).toHaveLength(2)

    api.fetchDockerImages.mockRejectedValueOnce(new Error('network down'))
    // 表头刷新按钮在 ArtTableHeader 的替身里没有实现（真组件才有点击件）——
    // 直接调 tab 暴露的 refresh（页面 hero 刷新走的是同一个入口）。
    const tab = w.findComponent({ name: 'DockerImagesTab' })
    await (tab.vm as unknown as { refresh: () => Promise<void> }).refresh()
    await flush()

    expect(w.find('.docker-tab-error').exists()).toBe(false)
    expect(w.text()).toContain('刷新失败，正在显示上次结果')
    // 清单未被清空：喂给表格的仍是上一批数据（最后已知数据）。
    expect(rowsOf()).toHaveLength(2)
  })

  it('服务端截断：total 全量 + 前 N 条说出口', async () => {
    api.fetchDockerVolumes.mockResolvedValue({ items: VOLUMES.items, total: 640 })
    const { wrapper: w } = await mountResources({ host: 'h1', tab: 'volumes' })
    expect(w.text()).toContain('共 640 个数据卷')
    expect(w.text()).toContain('列表显示前 1 条')
  })
})

describe('行内下钻语义保持（单主机窗口）', () => {
  it('镜像行「详情」带**行主机**跳详情页（不是页面级主机）', async () => {
    const { wrapper: w, router } = await mountResources({ host: 'h1' })
    const op = columnsOf(w, 'DockerImagesTab').find((c) => c.prop === 'operation')
    expect(op?.formatter, '操作列应有 formatter').toBeTruthy()

    // 行归属 h2：详情必须带 h2（镜像属于那台机器，单主机窗口语义不变）。
    const row = IMAGES.items[1]!
    const vnode = op!.formatter!(row as never) as {
      children?: { props?: { onClick?: () => void }; type?: { name?: string } }[]
    }
    const detailBtn = (Array.isArray(vnode.children) ? vnode.children : []).find(
      (c) => c?.props?.onClick
    )
    expect(detailBtn, '操作列应有可点的详情按钮').toBeTruthy()
    detailBtn!.props!.onClick!()
    await new Promise((r) => setTimeout(r, 0))

    expect(router.currentRoute.value.name).toBe('DockerImageDetail')
    expect(router.currentRoute.value.query.host).toBe('h2')
  })
})
