// @vitest-environment jsdom
/**
 * 设备列表的列集合守卫（手机横屏 / 平板 / 桌面）
 *
 * 与 docker 列表页同一动机：`hideBelow` 是**声明式**的——断点名写错、该留的列被藏、
 * 或某次重构把声明弄丢，现有测试一条都不会红（页面照样挂载、单测照样绿），只有真机上
 * 某档视口看不到关键列时才暴露。这里钉两条不变量：
 *   ① 关键列（主机名 / 在线状态 / 操作）在任何视口都必须可见；
 *   ② 列集合随视口变宽**单调不减**（不会出现「大屏反而少一列」的倒挂）。
 * 断言取的是页面真正传给 ArtTable 的列配置，过滤规则复用
 * components/core/tables/responsive-columns.ts 的同一函数（纯函数本身另有测试覆盖）。
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { ElTable } from 'element-plus'
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
// 字典只用来渲染状态文案，这里给最小面即可（真实现依赖 pinia 的字典 store）
vi.mock('@/hooks/core/useDict', () => ({
  useDict: () => ({
    options: { value: [] },
    ensure: async () => undefined,
    render: (v: unknown) => String(v ?? ''),
    labelOf: (v: unknown) => String(v ?? '')
  })
}))

import DeviceList from '../views/index.vue'
import { BREAKPOINTS } from '@/config/breakpoints'
import { filterColumnsForViewport } from '@/components/core/tables/responsive-columns'
import type { ColumnOption } from '@/types/component'

/** Art* 全局组件替身：保留 slot；ArtTable 用真 ElTable 包一层以提供表格上下文。 */
const passthrough = (name: string) =>
  defineComponent({
    name,
    setup:
      (_, { slots }) =>
      () =>
        h('div', { 'data-stub': name }, [slots.default?.(), slots.left?.()])
  })

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
  WatermarkBar: passthrough('WatermarkBar')
}

const WIDTHS = [500, 640, 768, 900, 1024, 1280, 1600]

let router: Router
let wrapper: VueWrapper | null = null

beforeEach(async () => {
  vi.stubGlobal(
    'ResizeObserver',
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
  )
  api.fetchDevices.mockResolvedValue({ list: [], total: 0 })
  api.fetchAgentReleases.mockResolvedValue({ list: [] })
  router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/', component: { template: '<div />' } }]
  })
  await router.push('/')
  await router.isReady()
  wrapper?.unmount()
  wrapper = mount(DeviceList, { global: { plugins: [router], stubs: STUBS } })
  await new Promise((r) => setTimeout(r, 0))
  await nextTick()
})

/** 某视口宽度下，页面实际会渲染的列（标签；无标签的结构列回退为 type） */
const labelsAt = (width: number): string[] => {
  const table = wrapper!.findComponent({ name: 'ArtTable' })
  expect(table.exists()).toBe(true)
  // 替身没声明 props（用 attrs 渲染），列配置落在 $attrs 里；真组件走 $props，两处都读
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

describe('设备列表：列集合随视口分档（hideBelow 的不变量守卫）', () => {
  it('关键列（主机名 / 在线状态 / 操作）在任何视口都可见', () => {
    for (const width of WIDTHS) {
      const labels = labelsAt(width)
      for (const key of ['主机名', '在线状态', '操作']) {
        expect(labels, `${width}px 下缺少关键列「${key}」`).toContain(key)
      }
    }
  })

  it('列集合随视口变宽单调不减（不会大屏反而丢列）', () => {
    const sets = WIDTHS.map((width) => new Set(labelsAt(width)))
    for (let i = 1; i < WIDTHS.length; i++) {
      for (const kept of sets[i - 1]!) {
        expect(
          sets[i]!.has(kept),
          `${WIDTHS[i]}px 比更窄的 ${WIDTHS[i - 1]}px 少了一列「${kept}」（阈值写反了？）`
        ).toBe(true)
      }
    }
  })

  it('分档点：序号/系统/Agent 版本平板起，水位与架构桌面起', () => {
    const phone = labelsAt(640)
    expect(phone).not.toContain('序号')
    expect(phone).not.toContain('操作系统')
    expect(phone).not.toContain('CPU 使用率')
    expect(phone).not.toContain('架构')

    const tablet = labelsAt(768)
    expect(tablet).toContain('序号')
    expect(tablet).toContain('操作系统')
    expect(tablet).toContain('Agent 版本')
    expect(tablet).toContain('最后上报')
    expect(tablet).not.toContain('CPU 使用率')

    const desktop = labelsAt(1024)
    expect(desktop).toContain('CPU 使用率')
    expect(desktop).toContain('内存使用率')
    expect(desktop).toContain('磁盘使用率')
    expect(desktop).toContain('架构')
  })
})
