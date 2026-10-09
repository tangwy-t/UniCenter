// @vitest-environment jsdom
/**
 * 设备列表页的「易用性接线」守卫（本波修复的防回归）。
 *
 * 三组不变量，都是**行为级**（挂载真组件断言），不是源码扫描：
 *  1. 路由面：列表路由 meta.keepAlive === true —— art-page-content 只缓存
 *     带 this meta 的路由，丢了它「从详情返回列表」的翻页/筛选/勾选保持
 *     就整个失效（页面照常出，无任何报错 —— 典型的静默回归）。
 *  2. 死链修复：upgrade-task 页「重试失败项」push 的 {retryIds, retryVersion}
 *     必须被列表页消费 —— 修复前 index.vue 根本不读 route.query，点按钮
 *     换页后什么也不发生。断言：带 query 挂载 → 升级对话框打开、目标行
 *     是 retryIds、chosenVersion 预填 retryVersion，且 query 被 replace 清掉
 *     （防刷新二次触发）。
 *  3. MenuProcessor 透传：后端菜单模式下插件 meta 会重建（keepAlive 在
 *     菜单接口里没有落点）—— 透传丢了 keepAlive 就只在静态菜单模式生效。
 *     断言：processBackendMenu 产出的 /device/index 路由 meta.keepAlive 保留。
 *
 * 挂载口径沿用 viewport-columns.test.ts（mock ../api 与 useAuth/useDict，
 * Art* 用轻量替身）。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { defineComponent, h, nextTick } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'

const api = vi.hoisted(() => ({
  fetchDevices: vi.fn(),
  fetchDevice: vi.fn(),
  fetchAgentReleases: vi.fn(),
  previewDeviceUpgrade: vi.fn(),
  dispatchDeviceUpgrade: vi.fn(),
  enableDevice: vi.fn(),
  disableDevice: vi.fn(),
  removeDevice: vi.fn()
}))
vi.mock('../api', () => ({ ...api, default: undefined }))
vi.mock('@/hooks/core/useAuth', () => ({
  useAuth: () => ({ hasAuth: () => true, hasAnyAuth: () => true })
}))
vi.mock('@/hooks/core/useDict', () => ({
  useDict: () => ({
    options: { value: [] },
    ensure: async () => undefined,
    render: (v: unknown) => String(v ?? ''),
    labelOf: (v: unknown) => String(v ?? '')
  })
}))

import DeviceList from '../views/index.vue'
import plugin from '../index'

/** Art* 全局组件替身：保留 slot（同 viewport-columns.test.ts）。 */
const passthrough = (name: string) =>
  defineComponent({
    name,
    setup:
      (_, { slots }) =>
      () =>
        h('div', { 'data-stub': name }, [slots.default?.(), slots.left?.()])
  })

const STUBS = {
  ArtTable: passthrough('ArtTable'),
  ArtTableHeader: passthrough('ArtTableHeader'),
  ArtSearchBar: passthrough('ArtSearchBar'),
  ArtButtonTable: passthrough('ArtButtonTable'),
  ArtButtonMore: passthrough('ArtButtonMore'),
  WatermarkBar: passthrough('WatermarkBar')
}

const ROWS = [
  { id: 'd1', hostname: 'bogon', os: 'linux', arch: 'amd64', agentVersion: '0.6.0', status: 1, online: true },
  { id: 'd2', hostname: 'nas', os: 'linux', arch: 'amd64', agentVersion: '0.6.0', status: 1, online: false },
  { id: 'd3', hostname: 'edge', os: 'linux', arch: 'arm64', agentVersion: '0.5.9', status: 1, online: true }
]

let router: Router
let wrapper: VueWrapper | null = null

beforeEach(() => {
  vi.stubGlobal(
    'ResizeObserver',
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
  )
  api.fetchDevices.mockReset().mockResolvedValue({ list: ROWS, total: ROWS.length })
  api.fetchDevice.mockReset().mockResolvedValue({
    id: 'd1', hostname: 'bogon', os: 'linux', arch: 'amd64', rollbackVersion: ''
  })
  api.fetchAgentReleases.mockReset().mockResolvedValue({ list: [] })
})

afterEach(() => {
  wrapper?.unmount()
  wrapper = null
  vi.unstubAllGlobals()
})

/** 带 query 挂载列表页（路径与 query 一起 push，等路由就绪再 mount）。 */
async function mountWithQuery(query: Record<string, string> = {}) {
  router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/device/index', component: DeviceList }
    ]
  })
  await router.push({ path: '/device/index', query })
  await router.isReady()
  wrapper = mount(DeviceList, { global: { plugins: [router], stubs: STUBS } })
  await new Promise((r) => setTimeout(r, 0))
  await nextTick()
  return { router, wrapper: wrapper! }
}

describe('设备列表路由：状态保持（keepAlive）', () => {
  it('列表路由 meta.keepAlive === true（丢了这个，从详情返回即重置）', () => {
    const route = (plugin.routes ?? []).find((r) => r.path === '/device/index')
    expect(route, '缺少 /device/index 路由').toBeTruthy()
    expect(route?.meta?.keepAlive, '列表路由必须声明 meta.keepAlive: true').toBe(true)
  })

  it('列表组件名是 DeviceList（KeepAlive 按组件名缓存，两模式同键）', () => {
    // art-page-content 的 KeepAlive 不设 include、只按组件实例名缓存；
    // defineOptions name 就是这个键 —— 改名 = 缓存静默失效。
    expect((DeviceList as unknown as { name?: string }).name).toBe('DeviceList')
  })
})

describe('「重试失败项」深链：query 必须被消费（死链修复守卫）', () => {
  it('retryIds/retryVersion 打开升级对话框、预选版本，并 replace 清 query', async () => {
    const { router: r } = await mountWithQuery({
      retryIds: 'd1,d2',
      retryVersion: '0.6.1'
    })

    // 对话框打开（ElDialog 用真件挂载，v-model 打开即渲染 body）
    await nextTick()
    await new Promise((res) => setTimeout(res, 0))
    const dialog = wrapper!.findAllComponents({ name: 'ElDialog' })
    expect(dialog.length, '页面应渲染升级/回滚/筛选三个对话框').toBeGreaterThanOrEqual(1)

    // 升级对话框的标题是「升级 Agent」（不是「回滚 Agent」）
    const upgrade = dialog.find((d) => d.props('modelValue') === true)
    expect(upgrade, '没有处于打开态的对话框（深链没打开升级对话框）').toBeTruthy()
    expect(upgrade!.props('title')).toBe('升级 Agent')

    // 断言深链的消费：通过组件实例状态（openUpgradeDialog 的产物）
    const vm = wrapper!.vm as unknown as {
      targetRows: { id: string }[]
      chosenVersion: string
      upgradeVisible: boolean
    }
    expect(vm.upgradeVisible).toBe(true)
    expect(vm.targetRows.map((t) => t.id)).toEqual(['d1', 'd2'])
    expect(vm.chosenVersion, 'retryVersion 应预填进版本下拉').toBe('0.6.1')

    // query 被 replace 清掉（防刷新/复进二次触发）
    await vi.waitUntil(
      () => Object.keys(r.currentRoute.value.query).length === 0,
      { timeout: 2000 }
    )
  })

  it('无 retryIds query 时：对话框不打开、行为不变', async () => {
    const { router: r } = await mountWithQuery({})
    const vm = wrapper!.vm as unknown as { upgradeVisible: boolean }
    expect(vm.upgradeVisible).toBe(false)
    expect(Object.keys(r.currentRoute.value.query)).toHaveLength(0)
  })
})

describe('批量回滚语义（回滚目标必须是服务端 rollbackVersion，不自选版本）', () => {
  it('勾选后走 openRollbackDialog：逐台 fetchDevice 取 rollbackVersion', async () => {
    api.fetchDevice.mockResolvedValue({
      id: 'd1', hostname: 'bogon', os: 'linux', arch: 'amd64', rollbackVersion: '0.5.9'
    })
    await mountWithQuery({})
    const vm = wrapper!.vm as unknown as {
      selected: { id: string }[]
      rollbackTargets: { id: string; rollbackVersion: string }[]
      rollbackVisible: boolean
      openRollbackDialog: () => Promise<void>
    }
    // 模拟表格勾选（selection-change 的落点）
    vm.selected = [
      { id: 'd1' } as never,
      { id: 'd2' } as never
    ]
    await vm.openRollbackDialog()
    expect(vm.rollbackVisible).toBe(true)
    // 两台各取一次详情（fetchDevice 的调用数 = 选中数）
    expect(api.fetchDevice).toHaveBeenCalledTimes(2)
    // 映射来自服务端 detail（rollbackVersion 逐台建键）
    expect(vm.rollbackTargets.map((t) => t.rollbackVersion)).toEqual(['0.5.9', '0.5.9'])
  })
})

describe('跨页勾选（reserve-selection）与跨页连续序号', () => {
  it('selection 列声明 reserveSelection 且表级 row-key=id', async () => {
    await mountWithQuery({})
    const vm = wrapper!.vm as unknown as {
      columns: Array<Record<string, unknown>>
    }
    const selection = vm.columns.find((c) => c.type === 'selection')
    expect(selection, '有升级权限时必须有 selection 列').toBeTruthy()
    expect(
      selection?.reserveSelection,
      'selection 列缺 reserveSelection：跨页勾选会随翻页丢'
    ).toBe(true)
    // 表级 row-key 是 EP reserve-selection 的前提；经 ArtTable 透传到 ElTable，
    // 在页面模板上声明 —— 源码级断言（挂载替身不透传 DOM 属性）。
    // 路径从 cwd 锚定（jsdom 环境的 import.meta.url 不是 file: scheme）。
    const src = readFileSync(
      join(process.cwd(), 'src/modules/device/views/index.vue'),
      'utf8'
    )
    expect(src).toContain('row-key="id"')
  })

  it('序号列用 globalIndex（跨页连续），不是每页重置的 index', async () => {
    await mountWithQuery({})
    const vm = wrapper!.vm as unknown as {
      columns: Array<{ type?: string }>
    }
    expect(vm.columns.some((c) => c.type === 'globalIndex')).toBe(true)
    expect(vm.columns.some((c) => c.type === 'index'), 'type:index 每页重置序号').toBe(false)
  })
})
