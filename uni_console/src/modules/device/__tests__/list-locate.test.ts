// @vitest-environment jsdom
/**
 * 「列表定位表达力」守卫：IP 列 / IP+版本筛选 / 服务端排序 三条接线。
 *
 * ── 为什么行为级 + 源码级两条腿 ───────────────────────────────────
 *
 * 行为级（挂载真列表组件，断言 fetchDevices 收到的 query 与列配置）钉住
 * 「参数真的发出去、列真的声明了可排序」；源码级（扫描 index.vue）钉住
 * 「@sort-change 落到 handleSortChange 且三态清空正确」—— 表头点击的
 * 真实交互链（ElTable sortable:'custom' → sort-change → 写回 sortBy/
 * sortOrder → 回第一页重查）端到端太长，jsdom 里点 EP 表头不稳，
 * 取链上两端各钉一头。
 *
 * 排序默认口径的守卫（后端回落 lastSeenAt desc）在 uni_core 侧
 * TestDeviceSortWhitelistAndFallback；这里只守前端发的是合法键。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { h, defineComponent, nextTick } from 'vue'
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

/** Art* 全局组件替身：保留 slot（与 list-ux.test.ts 同款）。 */
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
  api.fetchDevices.mockReset().mockResolvedValue({ list: [], total: 0 })
})

afterEach(() => {
  wrapper?.unmount()
  wrapper = null
  vi.unstubAllGlobals()
})

async function mountList() {
  router = createRouter({
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
  return wrapper!
}

/** 最近一次列表请求的 query（fetchDevices 首参）。 */
const lastQuery = (): Record<string, unknown> =>
  api.fetchDevices.mock.calls[api.fetchDevices.mock.calls.length - 1]![0] as Record<string, unknown>

describe('IP / Agent 版本筛选：参数必须真的发出去', () => {
  it('searchForm 写入 primaryIp/agentVersion 后随 loadList 发给后端', async () => {
    await mountList()
    const vm = wrapper!.vm as unknown as {
      searchForm: Record<string, unknown>
      handleSearch: () => void
    }
    vm.searchForm.primaryIp = '10.1.1.'
    vm.searchForm.agentVersion = '0.6.0'
    vm.handleSearch()
    await new Promise((r) => setTimeout(r, 0))
    const q = lastQuery()
    expect(q.primaryIp, 'primaryIp 未随请求发出（筛选静默失效）').toBe('10.1.1.')
    expect(q.agentVersion, 'agentVersion 未随请求发出（筛选静默失效）').toBe('0.6.0')
  })

  it('未填筛选时不带 primaryIp/agentVersion 键（空串不是合法筛选值）', async () => {
    await mountList()
    const q = lastQuery()
    expect('primaryIp' in q).toBe(false)
    expect('agentVersion' in q).toBe(false)
  })
})

describe('IP 列：声明在列集合里（缺值「—」、桌面档显示）', () => {
  it('列集合含 primaryIp（minWidth/hideBelow 与契约一致）', async () => {
    await mountList()
    const vm = wrapper!.vm as unknown as {
      columns: Array<Record<string, unknown>>
    }
    const col = vm.columns.find((c) => c.prop === 'primaryIp')
    expect(col, '列表必须声明 primaryIp 列').toBeTruthy()
    expect(col!.hideBelow, 'IP 列桌面起显示（窄屏让位给身份列）').toBe('desktop')
    expect(Number(col!.minWidth), 'IP 列 minWidth ≥ 130（IPv6 也放得下）').toBeGreaterThanOrEqual(130)
  })
})

describe('服务端排序：sortable 列集合与 sort-change 写回', () => {
  it('可排序列恰好是 lastSeenAt/hostname/agentVersion，且都是 custom（本地不排）', async () => {
    await mountList()
    const vm = wrapper!.vm as unknown as {
      columns: Array<{ prop?: string; sortable?: unknown }>
    }
    const sortableProps = vm.columns
      .filter((c) => c.sortable === 'custom')
      .map((c) => c.prop)
      .sort()
    expect(sortableProps).toEqual(['agentVersion', 'hostname', 'lastSeenAt'])
    // 本地排序（sortable: true）绝不允许：整页数据在服务端，本地排只重排当前页。
    const localSorted = vm.columns.filter((c) => c.sortable === true || c.sortable === 'local')
    expect(localSorted, '水位/其它列是 Redis 拼装或非白名单列，不得本地排序').toHaveLength(0)
  })

  it('handleSortChange 三态：升/降写回 sortBy+sortOrder，取消清空回默认', async () => {
    await mountList()
    const vm = wrapper!.vm as unknown as {
      handleSortChange: (e: { prop?: string; order?: string | null }) => void
      sortBy: string
      sortOrder: string
    }
    api.fetchDevices.mockClear()
    vm.handleSortChange({ prop: 'hostname', order: 'ascending' })
    expect(vm.sortBy).toBe('hostname')
    expect(vm.sortOrder).toBe('asc')
    // loadList 是 async：先 flush 微任务再看调用记录与请求参数
    await new Promise((r) => setTimeout(r, 0))
    expect(api.fetchDevices, '排序变化必须重查（pagination.current=1 → loadList）').toHaveBeenCalledTimes(1)
    expect(lastQuery().sortBy).toBe('hostname')
    expect(lastQuery().sortOrder).toBe('asc')

    vm.handleSortChange({ prop: 'hostname', order: 'descending' })
    expect(vm.sortBy).toBe('hostname')
    expect(vm.sortOrder).toBe('desc')

    // EP 三态循环第三态（order=null）：清回默认（后端回落 lastSeenAt desc）
    vm.handleSortChange({ prop: 'hostname', order: null })
    expect(vm.sortBy, '取消排序必须清空（后端默认 lastSeenAt desc）').toBe('')
    expect(vm.sortOrder).toBe('')
    await new Promise((r) => setTimeout(r, 0))
    expect(lastQuery().sortBy, '清空后不得再发 sortBy（回到不传=默认）').toBeUndefined()

    // 未知列不改变排序（列集合外的表头点击不该扰动）
    vm.handleSortChange({ prop: 'cpuUsedPercent', order: 'ascending' })
    expect(vm.sortBy, '非白名单列不得触发排序').toBe('')
  })
})

describe('排序接线（源码级）：@sort-change 必须落在页面模板上', () => {
  it('ArtTable 上声明 @sort-change=\"handleSortChange\"（attrs 透传链是既有能力）', () => {
    const src = readFileSync(
      join(process.cwd(), 'src/modules/device/views/index.vue'),
      'utf8'
    )
    expect(src).toContain('@sort-change="handleSortChange"')
  })
})
