// @vitest-environment jsdom
/**
 * 全局跨资源检索页（Wave 4d 收尾件，隐藏路由 /docker/search）的守卫。
 *
 * 两层：
 *   ① 纯行为（挂载 + 交互）：空 q 不发请求、输入后按组渲染 + 组计数、truncated 如实
 *      报「仅显示前 N 条」、unavailableHosts 非空渲染/空不渲染、可跳条目点击跳转正确
 *      （不可跳的不绑跳转）；
 *   ② 源码级锚：总览 hero 的入口钮指向 /docker/search（行为用例在 overview.test.ts 的
 *      挂载冒烟里）。隐藏路由断言并入 routes.test.ts。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { defineComponent, h, nextTick } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'

const api = vi.hoisted(() => ({ fetchDockerSearch: vi.fn() }))
vi.mock('../api', () => ({ ...api }))

import Search from '../views/search.vue'
import type { DockerSearchItem } from '../api'

/** Art* 全局组件替身：保留 slot（页面内容仍被渲染出来）。 */
const passthrough = (name: string) =>
  defineComponent({
    name,
    setup:
      (_, { slots }) =>
      () =>
        h('div', { 'data-stub': name }, [slots.left?.(), slots.default?.()])
  })

const STUBS = {
  ArtTableHeader: passthrough('ArtTableHeader'),
  // ArtButtonTable 替身渲染真 button（attrs 含入口的 onClick/onClick 语义），
  // 「返回上一页」与 hero 入口都可点。
  ArtButtonTable: defineComponent({
    name: 'ArtButtonTable',
    inheritAttrs: false,
    setup:
      (_, { attrs }) =>
      () =>
        h('button', { type: 'button', ...attrs })
  }),
  ArtSvgIcon: passthrough('ArtSvgIcon')
}

const item = (over: Partial<DockerSearchItem> = {}): DockerSearchItem => ({
  name: 'mysql',
  hostId: '7',
  hostname: 'uni-105',
  ...over
})

const mounted: VueWrapper[] = []
afterEach(() => {
  for (const w of mounted.splice(0)) w.unmount()
  vi.clearAllMocks()
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
})

async function mountSearch(): Promise<{ w: VueWrapper; router: Router }> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/docker/search', component: Search },
      { path: '/docker/containers', component: { template: '<div />' } },
      { path: '/docker/images', component: { template: '<div />' } },
      { path: '/docker/volumes', component: { template: '<div />' } },
      { path: '/docker/networks', component: { template: '<div />' } },
      { path: '/docker/projects/:name', component: { template: '<div />' } }
    ]
  })
  await router.push('/docker/search')
  await router.isReady()
  const w = mount(Search, { global: { plugins: [router], stubs: STUBS } })
  mounted.push(w)
  await nextTick()
  return { w, router }
}

/** 敲入关键字（真 ElInput 的 input 事件 → v-model）。 */
async function setKeyword(w: VueWrapper, term: string) {
  await w.find('input').setValue(term)
  await nextTick()
}

/** 点「搜索」钮（回车由同一 runSearch 处理，这里走按钮这条等价路径）。 */
async function clickSearch(w: VueWrapper) {
  const btn = w.findAll('button').find((b) => b.text() === '搜索')!
  expect(btn, '找不到「搜索」按钮').toBeTruthy()
  await btn.trigger('click')
  await new Promise((r) => setTimeout(r, 0))
  await nextTick()
}

async function waitFullPath(router: Router, fullPath: string) {
  await vi.waitUntil(() => router.currentRoute.value.fullPath === fullPath, { timeout: 5000 })
}

/** 某组的 section（按组标题定位）。 */
function group(w: VueWrapper, label: string) {
  return w.findAll('.dsp-group').find((g) => g.find('.dsp-group__label').text() === label)!
}

describe('全局检索页（/docker/search）', () => {
  it('① 空 q：不发请求、不报错，五组空结果', async () => {
    const { w } = await mountSearch()
    // 点「搜索」时 keyword 仍为空 → runSearch 走空 q 分支：不发请求。
    await clickSearch(w)
    expect(api.fetchDockerSearch).not.toHaveBeenCalled()

    const groups = w.findAll('.dsp-group')
    expect(groups).toHaveLength(5)
    expect(groups.map((g) => g.find('.dsp-group__label').text())).toEqual([
      '容器',
      '镜像',
      '数据卷',
      '网络',
      'Compose'
    ])
    for (const g of groups) {
      expect(g.find('.dsp-group__count').text()).toBe('共 0 条')
      expect(g.find('.dsp-group__empty').text()).toBe('—')
    }
    // 空 q 不报错（无错误提示）。
    expect(w.find('.dsp-error').exists()).toBe(false)
  })

  it('② 输入关键字后：发请求 + 按组渲染 + 组计数（缺的组补空）', async () => {
    api.fetchDockerSearch.mockResolvedValue({
      groups: [
        {
          kind: 'containers',
          items: [item({ name: 'mysql', hint: 'mysql:8.0.22' })],
          total: 3,
          truncated: false
        },
        { kind: 'images', items: [], total: 0, truncated: false }
        // projects/networks/volumes 缺省 → 前端补空组
      ],
      unavailableHosts: []
    })
    const { w } = await mountSearch()
    await setKeyword(w, 'mysql')
    await clickSearch(w)

    expect(api.fetchDockerSearch).toHaveBeenCalledWith({ q: 'mysql' })
    const groups = w.findAll('.dsp-group')
    expect(groups).toHaveLength(5) // 固定五组、缺的补空

    const containers = group(w, '容器')
    expect(containers.find('.dsp-group__count').text()).toBe('共 3 条')
    expect(containers.find('.dsp-name').text()).toBe('mysql')
    expect(containers.find('.docker-host').text()).toBe('uni-105')
    expect(containers.find('.dsp-hint').text()).toBe('mysql:8.0.22')

    // 缺省的组是空组（非 null）。
    expect(group(w, 'Compose').find('.dsp-group__empty').text()).toBe('—')
  })

  it('②b 镜像 hint 的摘要短显：sha256: 前缀剥离、取前 12 位（全文在 title）', async () => {
    api.fetchDockerSearch.mockResolvedValue({
      groups: [
        {
          kind: 'images',
          items: [
            item({
              name: 'mysql:8.0',
              hint: 'sha256:7dcddc01f13bab2f15cde676d44d01f61fc9f99fe7785e86196dfc07d358ae2b'
            })
          ],
          total: 1,
          truncated: false
        }
      ],
      unavailableHosts: []
    })
    const { w } = await mountSearch()
    await setKeyword(w, 'mysql')
    await clickSearch(w)

    const hint = group(w, '镜像').find('.dsp-hint')
    expect(hint.text()).toBe('7dcddc01f13b')
    expect(hint.attributes('title')).toContain('sha256:')
  })

  it('③ truncated 为真：如实「仅显示前 N 条」；为假不渲染该句', async () => {
    api.fetchDockerSearch.mockResolvedValue({
      groups: [
        {
          kind: 'containers',
          items: [item({ name: 'a' }), item({ name: 'b' })],
          total: 9,
          truncated: true
        },
        { kind: 'images', items: [item({ name: 'x' })], total: 1, truncated: false }
      ],
      unavailableHosts: []
    })
    const { w } = await mountSearch()
    await setKeyword(w, 'a')
    await clickSearch(w)

    const containers = group(w, '容器')
    expect(containers.find('.dsp-group__count').text()).toBe('共 9 条')
    expect(containers.find('.dsp-group__trunc').text()).toBe('仅显示前 2 条')
    // 未截断的组没有这句。
    expect(group(w, '镜像').find('.dsp-group__trunc').exists()).toBe(false)
  })

  it('④ unavailableHosts：非空渲染提示条（含主机名 + reason），空不渲染', async () => {
    api.fetchDockerSearch.mockResolvedValue({
      groups: [{ kind: 'containers', items: [item()], total: 1, truncated: false }],
      unavailableHosts: [
        { hostId: '8', hostname: 'uni-106', reason: '从未上报资源快照' }
      ]
    })
    const { w } = await mountSearch()
    await setKeyword(w, 'mysql')
    await clickSearch(w)

    expect(w.find('.dsp-note').exists()).toBe(true)
    expect(w.find('.dsp-note').text()).toContain('uni-106')
    expect(w.find('.dsp-note').text()).toContain('从未上报资源快照')

    // 零值（空清单）：提示条不渲染。
    api.fetchDockerSearch.mockResolvedValue({
      groups: [{ kind: 'containers', items: [item()], total: 1, truncated: false }],
      unavailableHosts: []
    })
    await clickSearch(w)
    expect(w.find('.dsp-note').exists()).toBe(false)
  })

  it('⑤ 可跳条目点击 → 正确路由；未知类型条目不可点（不绑跳转）', async () => {
    api.fetchDockerSearch.mockResolvedValue({
      groups: [
        { kind: 'containers', items: [item({ name: 'mysql' })], total: 1, truncated: false },
        { kind: 'projects', items: [item({ name: 'shop' })], total: 1, truncated: false },
        // 未知资源类型：前端补展示（不可跳）。
        { kind: 'widgets', items: [item({ name: 'w1' })], total: 1, truncated: false }
      ],
      unavailableHosts: []
    })
    const { w, router } = await mountSearch()
    await setKeyword(w, 'm')
    await clickSearch(w)

    // 容器 → 列表页带 ?host=（容器列表只认 host 深链，无 keyword 深链）。
    const containerBtn = w.findAll('button.dsp-name').find((b) => b.text() === 'mysql')!
    await containerBtn.trigger('click')
    await waitFullPath(router, '/docker/containers?host=7')
    expect(router.currentRoute.value.fullPath).toBe('/docker/containers?host=7')

    // Compose → 工作台 /docker/projects/:name?host=（名字在条目里，精确落位）。
    const projectBtn = w.findAll('button.dsp-name').find((b) => b.text() === 'shop')!
    await projectBtn.trigger('click')
    await waitFullPath(router, '/docker/projects/shop?host=7')
    expect(router.currentRoute.value.fullPath).toBe('/docker/projects/shop?host=7')

    // 未知类型（widgets）：名称是不可点的 span，不是 button。
    const widgetRow = w.findAll('.dsp-item').find((li) => li.text().includes('w1'))!
    expect(widgetRow.find('button.dsp-name').exists()).toBe(false)
    expect(widgetRow.find('span.dsp-name').exists()).toBe(true)
  })

  it('⑥ 总览 hero 的「全局检索」入口指向 /docker/search（源码级；行为用例在 overview.test.ts）', () => {
    const src = readFileSync(
      join(process.cwd(), 'src/modules/docker/views/overview.vue'),
      'utf8'
    )
    expect(src).toContain('title="全局检索"')
    expect(src).toContain("'/docker/search'")
  })
})