// @vitest-environment jsdom
/**
 * 镜像版本一致性（漂移）视图（views/image-drift.vue，Wave 4b）的行为钉子：
 *   - 行渲染：repoTag（等宽 + 中段省略保 tag）、主机数/摘要数（数字列右对齐 + is-num）、
 *     多数派（摘要短显 + 主机数）、少数派行内摘要；
 *   - 一句话口径由结构化字段自拼（契约 summary 恒空），**零解释文案**；
 *   - 服务端过滤：keyword / group 透传到 fetchImageDrift；
 *   - 服务端分页与排序：白名单三列发 sortBy，非白名单列不发；
 *   - unavailableHosts 提示条（本页最关键的诚实性设计）：非空渲染、空不渲染；
 *   - 少数派展开区（每种摘要的主机 chip）；
 *   - 入口：镜像页工具条「版本一致性」钮 push /docker/image-drift；
 *   - 页根 class 与纯清单形态（art-table-card / ArtTableHeader / ArtSearchBar / ArtButtonTable）。
 *
 * 挂载方式沿用 workloads.test.ts：mock '../api'，Art* 用轻量替身保留 slot；
 * jsdom 里 ElTable 不渲染 formatter 单元格，故行内事实从**页面传给 ArtTable 的
 * columns 的 formatter vnode** 断言（render→文本提取），行数据从替身 $attrs.data 断言。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'

const api = vi.hoisted(() => ({
  fetchDockerHosts: vi.fn(),
  fetchImageDrift: vi.fn(),
  // 镜像页（入口测试）挂载所需 import 面。
  fetchDockerImages: vi.fn(),
  sendDockerCmd: vi.fn(),
  fetchDockerCmdResult: vi.fn(),
  openDockerPullStream: vi.fn(),
  openDockerBuildStream: vi.fn(),
  uploadDockerBuildContext: vi.fn(),
  fetchDockerRegistries: vi.fn()
}))
vi.mock('../api', () => ({ ...api, default: undefined }))
vi.mock('@/hooks/core/useAuth', () => ({
  useAuth: () => ({ hasAuth: () => true, hasAnyAuth: () => true })
}))

import ImageDrift from '../views/image-drift.vue'
import ImagesPage from '../views/images.vue'
import type { DockerHostItem, DockerImageDriftItem } from '../api'

/** 可管主机（含分组）：分组筛选选项与空态判定的前置。 */
const HOSTS: DockerHostItem[] = [
  {
    id: '9',
    hostname: 'uni-109',
    primaryIp: '192.168.12.109',
    group: 'prod',
    online: true,
    dockerOk: true,
    containers: 0,
    images: 0,
    stale: false
  },
  {
    id: '8',
    hostname: 'uni-106',
    primaryIp: '192.168.12.106',
    group: 'stage',
    online: true,
    dockerOk: true,
    containers: 0,
    images: 0,
    stale: false
  }
]

/** 一条漂移行：4 台 · 2 种摘要 · 多数派 3 台 · 少数派 1 种 1 台。 */
const driftRow = (over: Partial<DockerImageDriftItem> = {}): DockerImageDriftItem => ({
  repoTag: 'mysql:8.0.22',
  hostCount: 4,
  distinctDigests: 2,
  dominantDigest: 'sha256:aaa0000000000000000000000000000000000000000000000000000000000aaa',
  dominantHostCount: 3,
  outliers: [
    {
      digest: 'sha256:bbb1111111111111111111111111111111111111111111111111111111111bbb',
      hostCount: 1,
      hosts: [{ hostId: '9', hostname: 'uni-109', group: 'prod' }]
    }
  ],
  ...over
})

/** 漂移端点响应（信封：items/total/page/pageSize/unavailableHosts）。 */
function driftResp(
  items: DockerImageDriftItem[],
  over: Partial<{ total: number; unavailableHosts: unknown[] }> = {}
) {
  return {
    items,
    total: over.total ?? items.length,
    page: 1,
    pageSize: 50,
    unavailableHosts: over.unavailableHosts ?? []
  }
}

/** Art* 全局组件替身：保留 slot（同 workloads/page-render 口径；attrs 透传到根元素，
 *  工具条钮的 title 落款到根 div —— 入口断言按 title 找）。 */
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
  ArtSvgIcon: passthrough('ArtSvgIcon')
}

const mounted: VueWrapper[] = []

beforeEach(() => {
  vi.clearAllMocks()
  api.fetchDockerHosts.mockResolvedValue({ list: HOSTS, snapshotInterval: 30 })
  api.fetchImageDrift.mockResolvedValue(driftResp([driftRow()]))
  api.fetchDockerImages.mockResolvedValue({ items: [], total: 0 })
})

afterEach(() => {
  for (const w of mounted.splice(0)) w.unmount()
})

/** 微任务 + 渲染冲刷。 */
async function flush(rounds = 4) {
  for (let i = 0; i < rounds; i++) {
    await new Promise((r) => setTimeout(r, 0))
    await nextTick()
  }
}

/** 挂漂移页（隐藏路由，页面借 router 做 back）。 */
async function mountDrift(path = '/docker/image-drift'): Promise<VueWrapper> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/docker/image-drift', component: ImageDrift },
      { path: '/docker/images', component: { template: '<div />' } }
    ]
  })
  await router.push(path)
  await router.isReady()
  const w = mount(ImageDrift, { global: { plugins: [router], stubs: STUBS } })
  mounted.push(w)
  await flush()
  return w
}

/** 页面传给 ArtTable 的列（替身把 columns 落在 $attrs）。 */
type Col = {
  prop?: string
  type?: string
  label?: string
  align?: string
  className?: string
  sortable?: unknown
  formatter?: (row: DockerImageDriftItem) => unknown
}

function columnsOf(w: VueWrapper): Col[] {
  const table = w.findComponent({ name: 'ArtTable' })
  return ((table.vm as unknown as { $attrs: Record<string, unknown> }).$attrs.columns ??
    []) as Col[]
}

function columnByProp(w: VueWrapper, prop: string): Col {
  const col = columnsOf(w).find((c) => c.prop === prop)
  expect(col, `页面没有把「${prop}」列传给 ArtTable`).toBeTruthy()
  return col!
}

function tableRows(w: VueWrapper): Record<string, unknown>[] {
  const table = w.findComponent({ name: 'ArtTable' })
  return ((table.vm as unknown as { $attrs: Record<string, unknown> }).$attrs.data ?? []) as Record<
    string,
    unknown
  >[]
}

/** 递归提取 vnode 文本（formatter 返回值可能是 span/div 或纯字符串）。 */
function vnodeText(node: unknown): string {
  if (node == null || typeof node === 'boolean') return ''
  if (typeof node === 'string' || typeof node === 'number') return String(node)
  if (Array.isArray(node)) return node.map(vnodeText).join('')
  const v = node as { children?: unknown }
  return vnodeText(v.children)
}

/** 递归收集带某 class 的 vnode（断言主机 chip 形制用）。 */
function vnodesByClass(node: unknown, cls: string, out: unknown[] = []): unknown[] {
  if (node == null || typeof node !== 'object') return out
  if (Array.isArray(node)) {
    for (const n of node) vnodesByClass(n, cls, out)
    return out
  }
  const v = node as { props?: { class?: string }; children?: unknown }
  if (v.props && typeof v.props.class === 'string' && v.props.class.split(/\s+/).includes(cls)) {
    out.push(node)
  }
  vnodesByClass(v.children, cls, out)
  return out
}

/** 触发搜索栏的搜索（ArtSearchBar 替身：v-model 经 update:modelValue，search 经 @search）。 */
async function search(w: VueWrapper, form: { keyword?: string; group?: string }) {
  const bar = w.findComponent({ name: 'ArtSearchBar' })
  bar.vm.$emit('update:modelValue', form)
  bar.vm.$emit('search')
  await flush()
}

describe('漂移页：行渲染的事实', () => {
  it('挂载：纯清单形态（页根 / art-table-card / ArtTableHeader / ArtSearchBar）+ 计数句', async () => {
    const w = await mountDrift()
    expect(w.find('.docker-image-drift-page').exists()).toBe(true)
    expect(w.find('.art-table-card').exists()).toBe(true)
    expect(w.findComponent({ name: 'ArtTableHeader' }).exists()).toBe(true)
    expect(w.findComponent({ name: 'ArtSearchBar' }).exists()).toBe(true)
    expect(w.findComponent({ name: 'ArtButtonTable' }).exists()).toBe(true)
    // 计数句（pageState=ready）。
    expect(w.text()).toContain('共 1 个仓库标签')
    // 行进了表（替身 $attrs.data）。
    expect(tableRows(w)).toHaveLength(1)
  })

  it('repoTag 列：等宽 + 中段省略保 tag，全文由 title 承载', async () => {
    const w = await mountDrift()
    const col = columnByProp(w, 'repoTag')
    expect(col.sortable).toBe('custom')
    const vnode = col.formatter!(driftRow()) as { props?: Record<string, unknown> }
    // 等宽类与全文 title 都在 vnode 上。
    expect(vnode.props!.class).toContain('drift-mono')
    expect(vnode.props!.title).toBe('mysql:8.0.22')
    // 短标签：原样（无需省略）。
    expect(vnodeText(vnode)).toBe('mysql:8.0.22')

    // 超长引用：尾部 tag 必须保留（中段省略保头+尾）。
    const long = 'quay.io/minio/minio:RELEASE.2024-01-16T22-43-05Z'
    const out = vnodeText(col.formatter!(driftRow({ repoTag: long })))
    expect(out).toContain('…')
    expect(out.endsWith(long.slice(-8))).toBe(true)
  })

  it('主机数 / 摘要数：数字列右对齐 + is-num + 可排序（白名单）', async () => {
    const w = await mountDrift()
    for (const prop of ['hostCount', 'distinctDigests']) {
      const col = columnByProp(w, prop)
      expect(col.align, `${prop} 未右对齐`).toBe('right')
      expect(col.className, `${prop} 未挂 is-num`).toContain('is-num')
      expect(col.sortable, `${prop} 不可排序`).toBe('custom')
    }
    // 行数据如实进表。
    expect(tableRows(w)[0]).toMatchObject({ hostCount: 4, distinctDigests: 2 })
  })

  it('多数派：摘要短显（12 位，剥 sha256:）+ 主机数', async () => {
    const w = await mountDrift()
    const col = columnByProp(w, 'dominantDigest')
    const text = vnodeText(col.formatter!(driftRow()))
    expect(text).toBe('aaa000000000 · 3 台')
    expect(text).not.toContain('sha256:')
  })

  it('少数派：行内摘要「N 种 · 共 M 台」；无少数派「—」', async () => {
    const w = await mountDrift()
    const col = columnByProp(w, 'outliers')
    expect(col.formatter!(driftRow())).toBe('1 种 · 共 1 台')
    expect(col.formatter!(driftRow({ outliers: [] }))).toBe('—')
  })
})

describe('漂移页：一句话口径（结构化字段自拼，零解释文案）', () => {
  it('口径句 =「N 台 · M 种摘要 · 多数派 K 台」，且不含解释/指引句', async () => {
    const w = await mountDrift()
    const col = columnByProp(w, 'summary')
    const text = col.formatter!(driftRow()) as string
    expect(text).toBe('4 台 · 2 种摘要 · 多数派 3 台')
    // 零解释文案：不得出现「点击 / 可展开 / 用于」这类指引句。
    for (const banned of ['点击', '可展开', '用于', '口径', '表示']) {
      expect(text).not.toContain(banned)
    }
  })
})

describe('漂移页：服务端过滤（keyword / group 透传）', () => {
  it('keyword 透传；group 透传（分组精确）', async () => {
    const w = await mountDrift()
    // 首次加载不带筛选（pageSize 显式）。
    expect(api.fetchImageDrift).toHaveBeenLastCalledWith(
      expect.objectContaining({ page: 1, pageSize: 50 })
    )
    expect(api.fetchImageDrift.mock.calls[0]![0].keyword).toBeUndefined()

    await search(w, { keyword: 'mysql' })
    expect(api.fetchImageDrift).toHaveBeenLastCalledWith(
      expect.objectContaining({ keyword: 'mysql' })
    )

    await search(w, { group: 'prod' })
    expect(api.fetchImageDrift).toHaveBeenLastCalledWith(expect.objectContaining({ group: 'prod' }))
  })

  it('分组选项来自主机清单里出现过的分组（去重）', async () => {
    const w = await mountDrift()
    const bar = w.findComponent({ name: 'ArtSearchBar' })
    const items = (bar.vm as unknown as { $attrs: Record<string, unknown> }).$attrs.items as {
      key: string
      options?: { label: string; value: string }[]
    }[]
    const groupItem = items.find((i) => i.key === 'group')
    expect(groupItem?.options?.map((o) => o.value)).toEqual(['prod', 'stage'])
  })
})

describe('漂移页：服务端分页与排序', () => {
  it('分页：切页/切大小透传（page/pageSize）', async () => {
    const w = await mountDrift()
    const table = w.findComponent({ name: 'ArtTable' })
    table.vm.$emit('pagination:current-change', 3)
    await flush()
    expect(api.fetchImageDrift).toHaveBeenLastCalledWith(
      expect.objectContaining({ page: 3, pageSize: 50 })
    )

    table.vm.$emit('pagination:size-change', 10)
    await flush()
    expect(api.fetchImageDrift).toHaveBeenLastCalledWith(
      expect.objectContaining({ page: 1, pageSize: 10 })
    )
  })

  it('排序：白名单三列发 sortBy/sortOrder；非白名单列不发', async () => {
    const w = await mountDrift()
    const table = w.findComponent({ name: 'ArtTable' })

    table.vm.$emit('sort-change', { prop: 'hostCount', order: 'descending' })
    await flush()
    expect(api.fetchImageDrift).toHaveBeenLastCalledWith(
      expect.objectContaining({ sortBy: 'hostCount', sortOrder: 'desc' })
    )

    table.vm.$emit('sort-change', { prop: 'repoTag', order: 'ascending' })
    await flush()
    expect(api.fetchImageDrift).toHaveBeenLastCalledWith(
      expect.objectContaining({ sortBy: 'repoTag', sortOrder: 'asc' })
    )

    // 非白名单列（少数派）：忽略，不发新请求。
    const before = api.fetchImageDrift.mock.calls.length
    table.vm.$emit('sort-change', { prop: 'outliers', order: 'ascending' })
    await flush()
    expect(api.fetchImageDrift.mock.calls.length).toBe(before)
  })
})

describe('漂移页：unavailableHosts 提示条（诚实性设计）', () => {
  it('非空即渲染（主机名 + reason 原文）；空则不渲染', async () => {
    api.fetchImageDrift.mockResolvedValue(
      driftResp([driftRow()], {
        unavailableHosts: [{ hostId: '8', hostname: 'uni-106', reason: '读取资源快照失败' }]
      })
    )
    const w = await mountDrift()
    const note = w.find('.docker-refresh-note')
    expect(note.exists()).toBe(true)
    expect(note.text()).toBe('1 台主机不可用：uni-106（读取资源快照失败）')
  })

  it('空即零渲染（不让「全一致」掩盖「有没有机器没算进来」）', async () => {
    api.fetchImageDrift.mockResolvedValue(driftResp([driftRow()]))
    const w = await mountDrift()
    expect(w.find('.docker-refresh-note').exists()).toBe(false)
  })
})

describe('漂移页：少数派展开区', () => {
  it('展开列 formatter 渲染每种摘要的主机 chip（.docker-host 胶囊）', async () => {
    const w = await mountDrift()
    const expand = columnsOf(w).find((c) => c.type === 'expand')
    expect(expand?.formatter, '页面没有把展开列 formatter 传给 ArtTable').toBeTruthy()
    const vnode = expand!.formatter!(driftRow())
    const text = vnodeText(vnode)
    // 摘要短显 + 主机数 + 主机名（chip）。
    expect(text).toContain('bbb111111111 · 1 台')
    expect(text).toContain('uni-109')
    // 主机名走 .docker-host 胶囊形制。
    expect(vnodesByClass(vnode, 'docker-host')).toHaveLength(1)
  })

  it('无少数派：展开区给「—」（不谎报有明细）', async () => {
    const w = await mountDrift()
    const expand = columnsOf(w).find((c) => c.type === 'expand')!
    expect(vnodeText(expand.formatter!(driftRow({ outliers: [] })))).toBe('—')
  })
})

describe('漂移页：四态', () => {
  it('首拉失败：紧凑失败态（结论句 + 重试）', async () => {
    api.fetchImageDrift.mockRejectedValueOnce(new Error('boom'))
    const w = await mountDrift()
    expect(w.find('.docker-empty').exists()).toBe(true)
    expect(w.text()).toContain('版本一致性获取失败')
    const retry = w.findAll('button').find((b) => b.text() === '重试')
    expect(retry).toBeTruthy()
  })

  it('无可管主机：空态说「尚无可管主机」', async () => {
    api.fetchDockerHosts.mockResolvedValue({ list: [], snapshotInterval: 30 })
    api.fetchImageDrift.mockResolvedValue(driftResp([]))
    const w = await mountDrift()
    expect(w.find('.docker-empty').exists()).toBe(true)
    expect(w.text()).toContain('尚无可管主机')
  })
})

describe('漂移页：入口（镜像页工具条「版本一致性」钮）', () => {
  it('点击 push /docker/image-drift', async () => {
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: '/docker/images', component: ImagesPage },
        { path: '/docker/image-drift', component: { template: '<div />' } }
      ]
    })
    await router.push('/docker/images')
    await router.isReady()
    const w = mount(ImagesPage, { global: { plugins: [router], stubs: STUBS } })
    mounted.push(w as VueWrapper)
    await flush()

    const btn = w
      .findAllComponents({ name: 'ArtButtonTable' })
      .find((b) => b.attributes('title') === '版本一致性')
    expect(btn, '镜像页工具条应有「版本一致性」入口钮').toBeTruthy()
    btn!.vm.$emit('click')
    await flush()
    expect(router.currentRoute.value.path).toBe('/docker/image-drift')
  })
})
