// @vitest-environment jsdom
/**
 * 拆分后的三张资源列表页（镜像 /docker/images、数据卷 /docker/volumes、
 * 网络 /docker/networks）的行为面 —— 原「镜像与存储」收敛页的资源三 tab 整体
 * 平移为三个独立整页（拆分切片），本文件随之改写：**tab 切换 / query 记忆 /
 * 「切 tab 不重拉」「切 tab 保留筛选」三类用例随 tab 形态一起删除**（没有 tab
 * 了），其余覆盖全部保留：
 *
 *   - **聚合数据源**：各页拉自己的跨主机端点（GET /docker/images|volumes|
 *     networks，服务端过滤，筛选参数全部透传）；各页的清单首拉恰一次；
 *   - **可管主机清单**：各页自拉一份（原页面级一份的主机清单随 tab 页退役，
 *     主机筛选下拉与「尚无可管主机」判定都从它来）；
 *   - `?host=` 深链作**主机筛选初始值**（单向：用户改筛选不回写 query）；
 *   - 失败口径（新形态）：首拉失败 → ElEmpty 失败描述 + 重试（重试成功后进表）；
 *     刷新失败 → 保留最后已知行 + 工具条标注（走 ArtTableHeader 的 refresh）；
 *   - 合计口径：合计读可见行、可回收读后端账目（空账目说「不可用」），随主机
 *     筛选收窄；服务端截断（total 全量 + 前 N 条说出口）；
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
  // 拉取/构建进度对话框与凭据对话框（镜像页挂载）不会真开流，但 import 面必须齐全。
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

import Images from '../views/images.vue'
import Volumes from '../views/volumes.vue'
import Networks from '../views/networks.vue'

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
 *  筛选表单（主机/开关类筛选直接 $emit 表单对象）。 */
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

const flush = async () => {
  await new Promise((r) => setTimeout(r, 0))
  await nextTick()
}

async function mountPage(component: Component, query: Record<string, string> = { host: 'h1' }) {
  const router = await makeRouter(query)
  const wrapper = mount(component, {
    global: { plugins: [router], stubs: STUBS }
  })
  mounted.push(wrapper as VueWrapper)
  await flush()
  return { wrapper: wrapper as VueWrapper, router }
}

/** 页面里唯一的搜索栏替身（三页各只挂一个 ArtSearchBar）。 */
function searchBarOf(w: VueWrapper) {
  const bar = w.findComponent({ name: 'ArtSearchBar' })
  expect(bar.exists(), '页面里没有搜索栏').toBe(true)
  return bar
}

/** 页面传给 ArtTable 的列配置（替身把 columns 落在 $attrs）。 */
function columnsOf(w: VueWrapper) {
  const table = w.findComponent({ name: 'ArtTable' })
  expect(table.exists(), '页面里没有表格').toBe(true)
  return ((table.vm as unknown as { $attrs: Record<string, unknown> }).$attrs.columns ?? []) as {
    prop?: string
    label?: string
    hideBelow?: string
    formatter?: (row: never) => unknown
  }[]
}

/** 页面传给 ArtTable 的行数据。 */
function rowsOf(w: VueWrapper) {
  const table = w.findComponent({ name: 'ArtTable' })
  expect(table.exists(), '页面里没有表格').toBe(true)
  return ((table.vm as unknown as { $attrs: Record<string, unknown> }).$attrs.data ?? []) as {
    hostname?: string
    name?: string
  }[]
}

/** 工具条内置刷新入口：ArtTableHeader 替身只保留占位 DOM，直接发 refresh 事件
 *  （与真组件点击同一处理链 —— 页面把 @refresh 接在它上面）。 */
async function triggerHeaderRefresh(w: VueWrapper) {
  const header = w.findComponent({ name: 'ArtTableHeader' })
  expect(header.exists(), '页面里没有 ArtTableHeader 工具条').toBe(true)
  header.vm.$emit('refresh')
  await flush()
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
  api.fetchDockerHosts.mockResolvedValue(HOSTS)
  api.fetchDockerImages.mockResolvedValue(IMAGES)
  api.fetchDockerVolumes.mockResolvedValue(VOLUMES)
  api.fetchDockerNetworks.mockResolvedValue(NETWORKS)
})

describe('聚合数据源（跨主机端点 + 服务端过滤）', () => {
  it('镜像页首拉：?host 深链为主机筛选初始值，三个筛选参数全部发给端点', async () => {
    await mountPage(Images, { host: 'h1' })
    expect(api.fetchDockerImages).toHaveBeenCalledWith({
      hostId: 'h1',
      keyword: undefined,
      dangling: undefined,
      unused: undefined
    })
  })

  it('不带 host 的进入是全部主机首拉（hostId 不发）', async () => {
    await mountPage(Images, {})
    expect(api.fetchDockerImages).toHaveBeenCalledWith(
      expect.objectContaining({ hostId: undefined })
    )
  })

  it('三页各自首拉自己的聚合端点；可管主机清单各页自拉一份（原页面级共用随 tab 页退役）', async () => {
    await mountPage(Images)
    expect(api.fetchDockerImages).toHaveBeenCalledTimes(1)
    expect(api.fetchDockerVolumes).not.toHaveBeenCalled()
    expect(api.fetchDockerNetworks).not.toHaveBeenCalled()
    expect(api.fetchDockerHosts).toHaveBeenCalledTimes(1)

    await mountPage(Volumes)
    expect(api.fetchDockerVolumes).toHaveBeenCalledTimes(1)
    expect(api.fetchDockerImages).toHaveBeenCalledTimes(1)
    expect(api.fetchDockerHosts).toHaveBeenCalledTimes(2)

    await mountPage(Networks)
    expect(api.fetchDockerNetworks).toHaveBeenCalledTimes(1)
    expect(api.fetchDockerHosts).toHaveBeenCalledTimes(3)
  })

  it('搜索时 keyword/开关/主机三项透传（开关只发 true）', async () => {
    const { wrapper: w } = await mountPage(Images)
    api.fetchDockerImages.mockClear()

    const bar = searchBarOf(w)
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
    const { wrapper: w } = await mountPage(Images, { host: 'h2' }) // 先带一个 host 深链
    api.fetchDockerImages.mockClear()

    const bar = searchBarOf(w)
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

describe('工具条合计（可见行与后端账目的两种口径）', () => {
  it('「可回收」读后端账目求和，不是 Σ 可见悬空行 SizeMB（对账修）', async () => {
    // 可见行里有 1 张 1200MB 的悬空镜像（旧口径会把这 1200MB 当「可回收」），
    // 账目（df 对账的独占层）故意另给一套小数字：工具条必须说账目这套
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
    const { wrapper: w } = await mountPage(Images)

    // 主行仍是可见清单的合计（Σ 条目 SizeMB：91.8 + 596.2 + 1200 = 1888）。
    expect(w.find('.docker-total').text()).toContain('合计 3 个 · 1.84 GB')
    // 副行是账目求和（0.002 + 12.5 = 12.502 → 12.5 MB），不是 1.2 GB。
    const sub = w.find('.docker-total__sub').text()
    expect(sub).toContain('3 个可回收')
    expect(sub).toContain('12.5 MB')
    expect(sub).not.toContain('1.2 GB')
  })

  it('账目跟着主机筛选范围走（服务端按 hostId 收窄 → 工具条只算那台）', async () => {
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
    const { wrapper: w } = await mountPage(Images, { host: 'h2' })
    expect(api.fetchDockerImages).toHaveBeenCalledWith(expect.objectContaining({ hostId: 'h2' }))
    const sub = w.find('.docker-total__sub').text()
    expect(sub).toContain('2 个可回收')
    expect(sub).toContain('12.5 MB')
  })

  it('没有任何主机报账（disk 为空）→ 副行说「不可用」，不折算成 0', async () => {
    api.fetchDockerImages.mockResolvedValue({ ...IMAGES, disk: [] })
    const { wrapper: w } = await mountPage(Images)
    const sub = w.find('.docker-total__sub').text()
    expect(sub).toContain('可回收账目不可用')
    expect(sub).not.toContain('0 个可回收')
  })

  it('数据卷页的合计与补充结论（未知/未使用）在工具条上（有才出现）', async () => {
    api.fetchDockerVolumes.mockResolvedValue({
      items: [
        ...VOLUMES.items,
        {
          name: 'orphan',
          driver: 'local',
          sizeMb: null,
          inUse: false,
          protected: false,
          hostId: 'h1',
          hostname: 'bogon'
        }
      ],
      total: 2
    })
    const { wrapper: w } = await mountPage(Volumes)
    expect(w.find('.docker-total').text()).toContain('合计 2 个')
    const sub = w.find('.docker-total__sub').text()
    expect(sub).toContain('1 个大小未知')
    expect(sub).toContain('1 个未使用')
  })
})

describe('主机列 + 主机筛选（跨主机形态的核心）', () => {
  it('镜像页：行带归属两列，表列里「主机」在平板竖屏起可见', async () => {
    const { wrapper: w } = await mountPage(Images)
    // 两台主机的镜像进同一张表，逐行带归属。
    expect(rowsOf(w).map((r) => r.hostname)).toEqual(['bogon', 'nas'])

    const hostCol = columnsOf(w).find((c) => c.label === '主机')
    expect(hostCol, '镜像表缺少「主机」列').toBeTruthy()
    expect(hostCol?.prop).toBe('hostname')
    expect(hostCol?.hideBelow).toBe('tablet')
  })

  it('主机筛选下拉来自本页主机清单（全部主机 + 各台，label 用主机名）', async () => {
    const { wrapper: w } = await mountPage(Images)
    const items = searchBarOf(w).props('items') as {
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

  it('深链 ?host=h2：数据卷页端点首拉就带 hostId（各页各自吃初值）', async () => {
    await mountPage(Volumes, { host: 'h2' })
    expect(api.fetchDockerVolumes).toHaveBeenCalledWith(expect.objectContaining({ hostId: 'h2' }))
  })

  it('网络页的搜索栏同样是三件套（名称/仅内部/主机）', async () => {
    const { wrapper: w } = await mountPage(Networks, { host: 'h1' })
    const items = searchBarOf(w).props('items') as {
      key: string
      filterable?: boolean
    }[]
    expect(items.map((i) => i.key)).toEqual(['keyword', 'internalOnly', 'host'])
    // 主机下拉可搜：与镜像/卷页同一处置（QA 实测不可搜）
    expect(items.find((i) => i.key === 'host')?.filterable).toBe(true)
  })
})

describe('失败口径（D-1 的页对应面）', () => {
  it('首拉失败：紧凑失败态（ElEmpty 失败描述）+ 重试；重试成功后进表', async () => {
    api.fetchDockerImages.mockRejectedValueOnce(new Error('network down'))
    const { wrapper: w } = await mountPage(Images)
    expect(w.text()).toContain('镜像清单获取失败')

    api.fetchDockerImages.mockResolvedValue(IMAGES)
    const retry = w.findAll('button').find((b) => b.text().includes('重试'))
    expect(retry, '失败态里应有重试按钮').toBeTruthy()
    await retry!.trigger('click')
    await flush()

    expect(w.text()).not.toContain('镜像清单获取失败')
    // 行数据进了表（jsdom 里 ElTable 不渲染行单元格，从传给 ArtTable 的 data 断言）。
    expect(rowsOf(w).map((r) => r.hostname)).toEqual(['bogon', 'nas'])
  })

  it('刷新失败：保留最后已知行并标注（不把看完的清单换成错误屏）', async () => {
    const { wrapper: w } = await mountPage(Images)
    expect(rowsOf(w)).toHaveLength(2)

    // 刷新 = 把眼前这页看新：主机清单（筛选源）与本页清单一起重拉（refreshAll）。
    const hostsBefore = api.fetchDockerHosts.mock.calls.length
    api.fetchDockerImages.mockRejectedValueOnce(new Error('network down'))
    // 刷新入口是 ArtTableHeader 的内置刷新钮（工具条上新形态）。
    await triggerHeaderRefresh(w)

    expect(api.fetchDockerHosts.mock.calls.length).toBe(hostsBefore + 1)
    expect(w.text()).not.toContain('镜像清单获取失败')
    expect(w.text()).toContain('刷新失败，正在显示上次结果')
    // 清单未被清空：喂给表格的仍是上一批数据（最后已知数据）。
    expect(rowsOf(w)).toHaveLength(2)
  })

  it('计数/合计文本的 ready 门控：失败/无可管主机时不报「共 0 个」假话', async () => {
    // 首拉失败（还没有任何数据）：错误态下不出现计数/合计文本（数据面不得伪装新鲜）。
    api.fetchDockerImages.mockRejectedValueOnce(new Error('network down'))
    const { wrapper: errorW } = await mountPage(Images)
    expect(errorW.find('.docker-count').exists()).toBe(false)
    expect(errorW.find('.docker-total').exists()).toBe(false)

    // 无可管主机（pageState=empty）：同样不报 —— 「0 个」不是这里的结论（「尚无可管主机」才是）。
    api.fetchDockerHosts.mockResolvedValue({ list: [] })
    api.fetchDockerVolumes.mockResolvedValue({ items: [], total: 0 })
    const { wrapper: emptyW } = await mountPage(Volumes)
    expect(emptyW.text()).toContain('尚无可管主机')
    expect(emptyW.find('.docker-count').exists()).toBe(false)
    expect(emptyW.find('.docker-total').exists()).toBe(false)
    expect(emptyW.find('.docker-total__sub').exists()).toBe(false)
  })

  it('服务端截断：total 全量 + 前 N 条说出口', async () => {
    api.fetchDockerVolumes.mockResolvedValue({ items: VOLUMES.items, total: 640 })
    const { wrapper: w } = await mountPage(Volumes, { host: 'h1' })
    expect(w.text()).toContain('共 640 个数据卷')
    expect(w.text()).toContain('列表显示前 1 条')
  })
})

describe('行内下钻语义保持（单主机窗口）', () => {
  it('镜像行「详情」带**行主机**跳详情页（不是页面级主机）', async () => {
    const { wrapper: w, router } = await mountPage(Images, { host: 'h1' })
    const op = columnsOf(w).find((c) => c.prop === 'operation')
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
