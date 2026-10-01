// @vitest-environment jsdom
/**
 * 镜像与存储页（7a 收敛页）的**收敛不变量**——这一页存在的理由就是这些行为：
 *
 *   - tab 切换写 URL query（query 记忆：深链/刷新/分享还原当前 tab）；
 *   - 页面级一个主机上下文：切 tab 不换主机、**不重拉**（三 tab 共享一份快照，
 *     fetch 各恰好一次 —— 每多一次 fetch 就是一次「收敛失败、退化成三页各拉」）；
 *   - 切 tab 保留各 tab 的筛选（ElTabPane lazy 首挂后常驻、v-show 隐藏 —— 组件
 *     实例不销毁，状态不丢）；
 *   - 主机切换触发**合并的重置纪律**（原三页各自 onHostSwitch 收拢为页面级一份：
 *     清各 tab 筛选、镜像 tab 关掉开着的拉取进度对话框、按新主机重拉）。
 *
 * 同页面的另一半守卫在 page-render.test.ts（渲染冒烟/列集合/D-1 同步文案）。
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
  // 拉取/构建进度对话框（镜像 tab 挂载）不会真开流，但 import 面必须齐全。
  openDockerPullStream: vi.fn(),
  openDockerBuildStream: vi.fn(),
  // 凭据面（pull 对话框的下拉与凭据管理对话框）。
  fetchDockerRegistries: vi.fn(),
  // 任务中心抽屉（docker-page 主机条入口）挂载但不开。
  fetchDockerTasks: vi.fn()
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

const STATE = {
  lastSync: 1790600000,
  stale: false,
  ageSeconds: 3,
  neverReported: false,
  dockerOk: true,
  images: [
    { id: 'i1', repoTags: ['uni-center-core:latest'], sizeMb: 91.8, inUse: true, dangling: false }
  ],
  volumes: [
    { name: 'uni-center_uploads', driver: 'local', sizeMb: 12, inUse: true, protected: true }
  ],
  networks: [{ name: 'uni-center_default', driver: 'bridge', scope: 'local', containersCount: 2 }]
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
 *  tab 的筛选表单（收敛不变量「切 tab 保留筛选」「切主机清空筛选」都看它）。 */
const artSearchBarStub = defineComponent({
  name: 'ArtSearchBar',
  props: { modelValue: { type: Object, default: () => ({}) } },
  emits: ['update:modelValue'],
  setup(props, { emit }) {
    return () =>
      h('input', {
        class: 'test-search-input',
        value: (props.modelValue as { keyword?: string })?.keyword ?? '',
        onInput: (e: Event) =>
          emit('update:modelValue', { keyword: (e.target as HTMLInputElement).value })
      })
  }
})

/** ArtTable 的替身用真 ElTable 包一层（ElTableColumn 依赖父表格 provide；页面不写
 *  ElTableColumn，这里主要为与 page-render.test.ts 同构）。 */
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
      { path: '/docker/resources', component: Resources }
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

/** 镜像 tab 的筛选输入（ArtSearchBar 替身渲染的真输入；按 tab 组件收窄查找范围）。
 *  返回值直接给 DOMWrapper：断言取值用 keywordOf（element 是 Element 类型，value
 *  只在 HTMLInputElement 上 —— 收窄一次，三处用例不各写一遍 as）。 */
const imagesInput = (w: VueWrapper) => {
  const tab = w.findComponent({ name: 'DockerImagesTab' })
  expect(tab.exists(), '镜像 tab 未挂载').toBe(true)
  return tab.find('input.test-search-input')
}

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
  api.fetchDockerHosts.mockClear()
  api.fetchDockerState.mockClear()
  api.fetchDockerHosts.mockResolvedValue(HOSTS)
  api.fetchDockerState.mockResolvedValue(STATE)
})

describe('tab 切换与 query 记忆', () => {
  it('点 tab 写回 query.tab（replace 不进历史），内容随 tab 切换', async () => {
    const { wrapper: w, router } = await mountResources()
    expect(router.currentRoute.value.query.tab).toBeUndefined()

    await clickTab(w, '数据卷')
    expect(router.currentRoute.value.query.tab).toBe('volumes')
    expect(w.findComponent({ name: 'DockerVolumesTab' }).exists()).toBe(true)
    // lazy 首挂后常驻：镜像 tab 已挂载过，切走只是 v-show 隐藏（实例还在）。
    expect(w.findComponent({ name: 'DockerImagesTab' }).exists()).toBe(true)

    await clickTab(w, '网络')
    expect(router.currentRoute.value.query.tab).toBe('networks')
    expect(w.findComponent({ name: 'DockerNetworksTab' }).exists()).toBe(true)

    await clickTab(w, '镜像')
    expect(router.currentRoute.value.query.tab).toBe('images')
  })

  it('深链进入：query.tab 落到对应 tab，非法值回落镜像 tab', async () => {
    const { wrapper: w } = await mountResources({ host: 'h1', tab: 'networks' })
    expect(w.findComponent({ name: 'DockerNetworksTab' }).exists()).toBe(true)

    const { wrapper: w2 } = await mountResources({ host: 'h1', tab: '不存在的tab' })
    expect(w2.findComponent({ name: 'DockerImagesTab' }).exists()).toBe(true)
  })
})

describe('页面级共享主机上下文（收敛的核心收益）', () => {
  it('三个 tab 全逛一遍：主机清单与快照各拉恰好一次（切 tab 不重拉）', async () => {
    const { wrapper: w } = await mountResources()
    await clickTab(w, '数据卷')
    await clickTab(w, '网络')
    await clickTab(w, '镜像')

    expect(api.fetchDockerHosts).toHaveBeenCalledTimes(1)
    expect(api.fetchDockerState).toHaveBeenCalledTimes(1)
    expect(api.fetchDockerState).toHaveBeenCalledWith('h1')
  })

  it('切 tab 保留各 tab 的筛选（实例常驻，v-show 隐藏不销毁）', async () => {
    const { wrapper: w } = await mountResources()
    await imagesInput(w).setValue('mysql')
    expect(keywordOf(w)).toBe('mysql')

    await clickTab(w, '数据卷')
    await clickTab(w, '镜像')
    // 筛选还在：切 tab 不是切页，用户逛一圈回来不该丢输入。
    expect(keywordOf(w)).toBe('mysql')
  })

  it('主机切换：重拉新主机快照 + 执行合并的重置纪律（清筛选、关拉取对话框）', async () => {
    const { wrapper: w, router } = await mountResources()

    // 造出「需要被重置」的状态：镜像 tab 的筛选 + 开着的拉取进度对话框。
    await imagesInput(w).setValue('mysql')
    const pullBtn = w.findAll('button').find((b) => b.text().includes('拉取镜像…'))
    expect(pullBtn, '找不到「拉取镜像…」入口（权限替身应放行 docker:manage）').toBeTruthy()
    await pullBtn!.trigger('click')
    await nextTick()
    const pullDialog = w.findComponent({ name: 'DockerPullProgressDialog' })
    expect(pullDialog.exists()).toBe(true)
    expect(pullDialog.props('modelValue')).toBe(true)

    // 换主机（host-context 的唯一事实源是 route query，与 HostSwitcher 同路径）。
    await router.replace({ query: { ...router.currentRoute.value.query, host: 'h2' } })
    await flush()

    // 新主机重拉一次（旧主机那次不计入）。
    expect(api.fetchDockerState).toHaveBeenCalledTimes(2)
    expect(api.fetchDockerState).toHaveBeenLastCalledWith('h2')
    // 筛选被清空（原三页 onHostSwitch 的纪律合并到页面级一份后的落点）。
    expect(keywordOf(w)).toBe('')
    // 拉取对话框被关掉：一场拉取属于受理它的那台主机。
    expect(pullDialog.props('modelValue')).toBe(false)
  })
})
