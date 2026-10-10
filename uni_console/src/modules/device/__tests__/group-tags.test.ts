// @vitest-environment jsdom
/**
 * 设备列表「分组 / 标签」写读守卫（Wave 4a-2）。
 *
 * 三组不变量，都是**行为级**（挂载真组件断言）：
 *  ① 分组列：列表渲染分组列，缺值显示「—」（EMPTY_TEXT），有值原样；
 *  ② 分组筛选：搜索栏的分组值透传到 `fetchDevices` 的 `group` 参数；
 *  ③ 设置分组对话框：入口按 `device:index:group` 门控；提交发
 *     `PATCH /devices/:id`（updateDeviceGroup），且 body 走**部分更新**语义 ——
 *     只有与打开时不同的字段才进 body（未动的字段不出现，清空是显式空串）。
 *
 * 挂载口径沿用 list-ux.test.ts（mock ../api 与 useAuth/useDict，Art* 用轻量替身）。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
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
  removeDevice: vi.fn(),
  updateDeviceGroup: vi.fn()
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
import ArtButtonMore from '@/components/core/forms/art-button-more/index.vue'

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
  {
    id: 'd1',
    hostname: 'bogon',
    os: 'linux',
    arch: 'amd64',
    agentVersion: '0.6.0',
    status: 1,
    online: true,
    group: '机房A',
    tags: 'web,edge'
  },
  {
    id: 'd2',
    hostname: 'nas',
    os: 'linux',
    arch: 'amd64',
    agentVersion: '0.6.0',
    status: 1,
    online: false
  }
]

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
  api.fetchAgentReleases.mockReset().mockResolvedValue({ list: [] })
  api.updateDeviceGroup.mockReset().mockResolvedValue(undefined)
})

afterEach(() => {
  wrapper?.unmount()
  wrapper = null
  vi.unstubAllGlobals()
})

async function mountPage(): Promise<{
  router: Router
  wrapper: VueWrapper
  vm: Record<string, any>
}> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/device/index', component: DeviceList }
    ]
  })
  await router.push({ path: '/device/index' })
  await router.isReady()
  wrapper = mount(DeviceList, { global: { plugins: [router], stubs: STUBS } })
  await new Promise((r) => setTimeout(r, 0))
  await nextTick()
  return { router, wrapper: wrapper!, vm: wrapper!.vm as unknown as Record<string, any> }
}

describe('分组列（① 列表渲染分组且缺值显示「—」）', () => {
  it('有分组列，formatter 有值原样、缺值「—」', async () => {
    const { vm } = await mountPage()
    const col = (vm.columns as Array<Record<string, any>>).find((c) => c.prop === 'group')
    expect(col, '列表没有渲染分组列').toBeTruthy()
    expect(col!.label).toBe('分组')
    // 单行溢出省略（沿用列表页既有列形制）
    expect(col!.showOverflowTooltip).toBe(true)
    expect(col!.formatter(ROWS[0])).toBe('机房A')
    expect(col!.formatter(ROWS[1])).toBe('—')
  })
})

describe('分组筛选（② 透传到 fetchDevices.group）', () => {
  it('searchForm.group 透传为 fetchDevices 的 group 参数', async () => {
    const { vm } = await mountPage()
    api.fetchDevices.mockClear()
    vm.searchForm.group = '机房A'
    await vm.loadList()
    expect(api.fetchDevices).toHaveBeenCalledWith(expect.objectContaining({ group: '机房A' }))
  })

  it('未选分组时 group 不出现在请求参数里（空 = 不过滤）', async () => {
    const { vm } = await mountPage()
    api.fetchDevices.mockClear()
    await vm.loadList()
    const arg = api.fetchDevices.mock.calls[0]?.[0] as Record<string, unknown>
    expect('group' in arg).toBe(false)
  })

  it('分组选项来自加载过的设备分组去重（累积）', async () => {
    const { vm } = await mountPage()
    const items = vm.searchItems as Array<Record<string, any>>
    const groupItem = items.find((i) => i.key === 'group')
    expect(groupItem, '搜索栏缺少分组筛选项').toBeTruthy()
    expect(groupItem!.type).toBe('select')
    expect(groupItem!.options).toEqual([{ label: '机房A', value: '机房A' }])
  })
})

describe('设置分组对话框（③ 权限门控 + 部分更新语义）', () => {
  it('行操作「设置分组」入口带 device:index:group 权限门控', async () => {
    const { vm } = await mountPage()
    const opCol = (vm.columns as Array<Record<string, any>>).find((c) => c.prop === 'operation')
    expect(opCol, '没有操作列').toBeTruthy()
    const vnode = opCol!.formatter(ROWS[0]) as {
      children: Array<{ type: unknown; props?: Record<string, any> }>
    }
    const more = vnode.children.find((c) => c.type === ArtButtonMore)
    expect(more, '操作列没有「更多」下拉').toBeTruthy()
    const setGroup = (more!.props!.list as Array<Record<string, any>>).find(
      (i) => i.key === 'setGroup'
    )
    expect(setGroup, '「更多」里没有设置分组入口').toBeTruthy()
    expect(setGroup!.auth, '设置分组未按 device:index:group 门控').toBe('device:index:group')
  })

  it('提交：只发**变化**的字段（部分更新语义）', async () => {
    const { vm } = await mountPage()
    vm.openGroupDialog(ROWS[0])
    await nextTick()
    expect(vm.groupDialogVisible).toBe(true)
    // 只改组、标签不动 → body 里只有 group
    vm.groupForm.group = '机房B'
    await vm.submitGroupDialog()
    expect(api.updateDeviceGroup).toHaveBeenCalledWith('d1', { group: '机房B' })
  })

  it('提交：清空分组发显式空串（而不是省略字段）', async () => {
    const { vm } = await mountPage()
    vm.openGroupDialog(ROWS[0])
    await nextTick()
    vm.groupForm.group = ''
    await vm.submitGroupDialog()
    expect(api.updateDeviceGroup).toHaveBeenCalledWith('d1', { group: '' })
  })

  it('提交：两字段都没改 → 不发请求', async () => {
    const { vm } = await mountPage()
    vm.openGroupDialog(ROWS[0])
    await nextTick()
    await vm.submitGroupDialog()
    expect(api.updateDeviceGroup).not.toHaveBeenCalled()
    expect(vm.groupDialogVisible).toBe(false)
  })

  it('提交：标签单独变化时只发 tags', async () => {
    const { vm } = await mountPage()
    vm.openGroupDialog(ROWS[0])
    await nextTick()
    vm.groupForm.tags = 'web,db'
    await vm.submitGroupDialog()
    expect(api.updateDeviceGroup).toHaveBeenCalledWith('d1', { tags: 'web,db' })
  })

  it('提交成功后刷新列表', async () => {
    const { vm } = await mountPage()
    api.fetchDevices.mockClear()
    vm.openGroupDialog(ROWS[0])
    await nextTick()
    vm.groupForm.group = '机房B'
    await vm.submitGroupDialog()
    expect(api.fetchDevices).toHaveBeenCalled()
  })
})
