// @vitest-environment jsdom
/**
 * Wave 3a「列表/表格视觉语汇统一」的守卫（本波坐实的缺陷当波落守卫）：
 *   ① 容器状态列 formatter 对 running/exited(非0)/exited(0)/created/paused/
 *      restarting/dead 各产出正确的点色与中文标签，且**不是纯文本**；
 *   ② `?state=` 深链挂载后写入 searchForm 并触发重拉（总览异常清单的出口）；
 *   ③ 数字列带 is-num + align，且样式表有 tabular-nums 规则；
 *   ④ midEllipsis 保住头尾（tag 与 /tcp）—— 见 display.test.ts；
 *   ⑤ 主机列渲染为 chip 节点（胶囊）；
 *   ⑥ 等宽 token --art-font-family-mono 已定义（tailwind.css）。
 *
 * 挂载口径照 workloads.test.ts（mock ../api 与 useAuth，Art* 用轻量替身保留 slot；
 * 列与 formatter 从页面传给 ArtTable 的 $attrs 断言 —— jsdom 里 ElTable 不渲染行单元格）。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { defineComponent, h, nextTick } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'

const api = vi.hoisted(() => ({
  fetchDockerHosts: vi.fn(),
  fetchDockerContainers: vi.fn(),
  sendDockerCmd: vi.fn(),
  fetchDockerCmdResult: vi.fn(),
  openDockerLogStream: vi.fn()
}))
vi.mock('../api', () => ({ ...api, default: undefined }))

const auth = vi.hoisted(() => ({ allow: new Set<string>() }))
vi.mock('@/hooks/core/useAuth', () => ({
  useAuth: () => ({
    hasAuth: (perm: string) => auth.allow.has(perm),
    hasAnyAuth: () => true
  })
}))

import Containers from '../views/containers.vue'
import type { DockerWorkloadItem } from '../api'

const HOSTS = {
  list: [{ id: 'h1', hostname: 'bogon', primaryIp: '192.168.12.105', online: true, dockerOk: true }]
}

const row = (over: Partial<DockerWorkloadItem> = {}): DockerWorkloadItem => ({
  id: 'c1',
  name: 'uni-center-core',
  image: 'quay.io/minio/minio:RELEASE.2024-01-16T22-43-05Z',
  state: 'running',
  statusText: 'Up 16 hours',
  cpuPercent: 0.6,
  memUsageMb: 91,
  memLimitMb: 1024,
  netRxBytesSec: 0,
  netTxBytesSec: 0,
  protected: false,
  hostId: 'h1',
  hostname: 'bogon',
  ...over
})

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
  ArtSvgIcon: passthrough('ArtSvgIcon')
}

const mounted: VueWrapper[] = []
afterEach(() => {
  for (const w of mounted.splice(0)) w.unmount()
  vi.clearAllMocks()
  vi.unstubAllGlobals()
})

beforeEach(() => {
  vi.stubGlobal(
    'ResizeObserver',
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
  )
  auth.allow = new Set(['docker:list', 'docker:inspect', 'docker:manage', 'docker:delete'])
  api.fetchDockerHosts.mockResolvedValue(HOSTS)
  api.fetchDockerContainers.mockResolvedValue({ items: [row()], total: 1 })
  api.sendDockerCmd.mockResolvedValue({ ref: 'r1' })
  api.fetchDockerCmdResult.mockResolvedValue({ status: 'succeeded' })
})

async function mountPage(query: Record<string, string> = {}): Promise<VueWrapper> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/docker/containers', component: Containers },
      { path: '/docker/containers/create', component: { template: '<div />' } },
      { path: '/docker/containers/:id', component: { template: '<div />' } },
      { path: '/docker/tasks', component: { template: '<div />' } }
    ]
  })
  await router.push({ path: '/docker/containers', query })
  await router.isReady()
  const w = mount(Containers, { global: { plugins: [router], stubs: STUBS } })
  mounted.push(w)
  await new Promise((r) => setTimeout(r, 0))
  await nextTick()
  return w
}

/** 页面传给 ArtTable 的列配置。 */
type Col = {
  prop?: string
  label?: string
  align?: string
  className?: string
  formatter?: (row: never) => unknown
}

function columns(w: VueWrapper): Col[] {
  const table = w.findComponent({ name: 'ArtTable' })
  return ((table.vm as unknown as { $attrs: Record<string, unknown> }).$attrs.columns ??
    []) as Col[]
}

/** 取某 prop 列的 formatter（不存在即测试失败）。 */
function formatterOf(w: VueWrapper, prop: string): (row: never) => unknown {
  const col = columns(w).find((c) => c.prop === prop)
  expect(col?.formatter, `列 ${prop} 应有 formatter`).toBeTruthy()
  return col!.formatter!
}

/* ── ① 容器状态列：点色 + 中文标签（不是纯文本） ───────────────────────── */
describe('① 容器状态列（圆点 + 中文结论，色语汇收敛到 utils/status）', () => {
  const CASES: [Partial<DockerWorkloadItem>, string, string][] = [
    [{ state: 'running', statusText: 'Up 16 hours' }, 'var(--el-color-success)', '运行中 16 小时'],
    [
      { state: 'running', statusText: 'Up 2 hours (unhealthy)' },
      'var(--el-color-warning)',
      '运行中 2 小时（异常）'
    ],
    [
      { state: 'running', statusText: 'Up 5 seconds (health: starting)' },
      'var(--el-color-warning)',
      '运行中 5 秒（健康检查中）'
    ],
    [
      { state: 'exited', statusText: 'Exited (143) 26 hours ago' },
      'var(--el-color-danger)',
      '已退出（退出码 143）26 小时前'
    ],
    [
      { state: 'exited', statusText: 'Exited (0) 8 months ago' },
      'var(--el-text-color-secondary)',
      '已退出（退出码 0）8 月前'
    ],
    [{ state: 'created', statusText: 'Created' }, 'var(--el-color-primary)', '已创建'],
    [{ state: 'paused' }, 'var(--el-color-warning)', '已暂停'],
    [{ state: 'restarting' }, 'var(--el-color-warning)', '重启中'],
    [{ state: 'dead' }, 'var(--el-color-danger)', '已死亡'],
    [{ state: 'weird' }, 'var(--el-text-color-secondary)', 'weird']
  ]

  it.each(CASES)('状态 %o → 点 %s + 文字 %s', async (over, dotColor, label) => {
    const w = await mountPage()
    const fmt = formatterOf(w, 'statusText')
    const vnode = fmt(row(over) as never) as {
      props?: { class?: string; style?: Record<string, string> }
      children?: unknown[]
    }
    // 不是纯文本（改造前是 containerStateZh 返回的字符串）。
    expect(typeof vnode).not.toBe('string')
    expect(vnode.props?.class).toBe('wkl-state')
    const kids = (vnode.children ?? []) as { props?: Record<string, unknown> }[]
    // children[0] = 点（带背景色），children[1] = 中文标签字符串。
    expect(kids[0]?.props?.class).toBe('wkl-state-dot')
    expect((kids[0]?.props?.style as Record<string, string>)?.background).toBe(dotColor)
    expect(kids[1]).toBe(label)
    // 颜色永不单独传达：旁边永远有文字结论。
    expect(typeof kids[1]).toBe('string')
  })
})

/* ── ② ?state= 深链接线（总览异常清单的出口真正生效） ───────────────────── */
describe('② ?state= 深链：写入筛选并触发重拉', () => {
  it('带 ?state=stopped 进入：首拉即带 state=stopped', async () => {
    await mountPage({ state: 'stopped' })
    expect(api.fetchDockerContainers).toHaveBeenCalledWith(
      expect.objectContaining({ state: 'stopped' })
    )
  })

  it('不带 state 进入：不发空串（undefined = 不过滤）', async () => {
    await mountPage()
    expect(api.fetchDockerContainers).toHaveBeenCalledWith(
      expect.objectContaining({ state: undefined })
    )
  })

  it('?state= + ?host= 同时进入：两条都生效、且只首拉一次（合并 watch）', async () => {
    await mountPage({ state: 'stopped', host: 'h1' })
    expect(api.fetchDockerContainers).toHaveBeenCalledTimes(1)
    expect(api.fetchDockerContainers).toHaveBeenCalledWith(
      expect.objectContaining({ state: 'stopped', hostId: 'h1' })
    )
  })
})

/* ── ③ 数字列排版：is-num + align + 样式表 tabular-nums ─────────────────── */
describe('③ 数字列右对齐 + is-num（tabular-nums）', () => {
  it('CPU/内存/端口等数字列带 align:right 与 className:is-num', async () => {
    const w = await mountPage()
    for (const prop of ['cpuPercent', 'memUsageMb', 'ports']) {
      const col = columns(w).find((c) => c.prop === prop)
      expect(col?.className, `${prop} 应带 is-num`).toBe('is-num')
      expect(col?.align, `${prop} 应右对齐`).toBe('right')
    }
  })

  it('CPU 列单位进列头、内存列单位进列头', async () => {
    const w = await mountPage()
    expect(columns(w).find((c) => c.prop === 'cpuPercent')?.label).toBe('CPU (%)')
    expect(columns(w).find((c) => c.prop === 'memUsageMb')?.label).toBe('内存 (MB)')
  })

  it('样式表（art-table/style.scss）有 .is-num 的 tabular-nums 规则', () => {
    const scss = readFileSync(
      join(process.cwd(), 'src/components/core/tables/art-table/style.scss'),
      'utf8'
    )
    expect(scss).toContain('is-num')
    expect(scss).toContain('tabular-nums')
  })
})

/* ── ⑤ 主机列 chip ────────────────────────────────────────────────────── */
describe('⑤ 主机列渲染为 chip 节点（胶囊）', () => {
  it('主机列 formatter 返回 docker-host 的 span，文本取主机名', async () => {
    const w = await mountPage()
    const vnode = formatterOf(w, 'hostname')(row() as never) as {
      props?: { class?: string }
      children?: unknown
    }
    expect(typeof vnode).not.toBe('string')
    expect(vnode.props?.class).toBe('docker-host')
    expect(vnode.children).toBe('bogon')
  })
})

/* ── ⑥ 等宽 token ─────────────────────────────────────────────────────── */
describe('⑥ 等宽 token 已定义', () => {
  it('tailwind.css 定义 --art-font-family-mono', () => {
    const css = readFileSync(join(process.cwd(), 'src/assets/styles/core/tailwind.css'), 'utf8')
    expect(css).toContain('--art-font-family-mono')
  })
})
