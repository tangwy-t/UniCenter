// @vitest-environment jsdom
/**
 * 全局跨资源检索页（Wave 4d 收尾件，隐藏路由 /docker/search）的守卫。
 *
 * 两层：
 *   ① 纯行为（挂载 + 交互）：未输入态提示 / 已搜全 0 无匹配（两态可辨）、输入后按组
 *      渲染 + 组计数、truncated 如实报「仅显示前 N 条」、unavailableHosts 非空渲染/空
 *      不渲染、可跳条目点击**落到条目本身**（按类型落详情页 / 列表带 keyword；不可跳的
 *      不绑跳转）；
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
  // id 是契约里新增的**必填**字段（容器=容器 ID / 镜像=镜像 ID 摘要 / 卷·网络=名称 /
  // Compose=项目名）；下钻落到条目本身靠它，工厂缺了它 vue-tsc 直接报错。
  id: 'id-1',
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
      // 下钻落条目：容器/镜像落各自详情页（param 段单独登记）。
      { path: '/docker/containers/:id', component: { template: '<div />' } },
      { path: '/docker/images', component: { template: '<div />' } },
      { path: '/docker/image-detail/:id', component: { template: '<div />' } },
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

/** 某组的 section（按组标题定位）。 */
function group(w: VueWrapper, label: string) {
  return w.findAll('.dsp-group').find((g) => g.find('.dsp-group__label').text() === label)!
}

describe('全局检索页（/docker/search）', () => {
  it('① 未输入态：不发请求、不报错，给「输入关键字，回车或点搜索」提示（不是五组「—」）', async () => {
    const { w } = await mountSearch()
    // 点「搜索」时 keyword 仍为空 → runSearch 走空 q 分支：不发请求。
    await clickSearch(w)
    expect(api.fetchDockerSearch).not.toHaveBeenCalled()

    // 未输入态是一句硬提示，不是五组「共 0 条 —」（后者与「搜不到」同形，会骗人）。
    expect(w.find('.dsp-prompt').text()).toBe('输入关键字，回车或点搜索')
    expect(w.findAll('.dsp-group')).toHaveLength(0)
    expect(w.find('.dsp-empty').exists()).toBe(false)
    // 空 q 不报错。
    expect(w.find('.dsp-error').exists()).toBe(false)
  })

  it('①b 已检索但全 0：给「没有匹配的资源」（与未输入态的提示语逐字不同，一眼可辨）', async () => {
    api.fetchDockerSearch.mockResolvedValue({ groups: [], unavailableHosts: [] })
    const { w } = await mountSearch()
    await setKeyword(w, 'zzz-none')
    await clickSearch(w)

    expect(api.fetchDockerSearch).toHaveBeenCalledWith({ q: 'zzz-none' })
    expect(w.find('.dsp-empty').text()).toBe('没有匹配的资源')
    // 与未输入态可区分：提示语不在、结果组不渲染。
    expect(w.find('.dsp-prompt').exists()).toBe(false)
    expect(w.findAll('.dsp-group')).toHaveLength(0)
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

  it('⑤ 可跳条目点击 → **落到条目本身**：容器/镜像落详情页、卷/网络落列表按名筛、Compose 落工作台；未知类型不可点', async () => {
    api.fetchDockerSearch.mockResolvedValue({
      groups: [
        {
          kind: 'containers',
          items: [item({ name: 'mysql', id: 'cid1' })],
          total: 1,
          truncated: false
        },
        {
          kind: 'images',
          // 镜像 id 是 `sha256:<64>`（含冒号）—— 作路由参数必须编码。
          items: [item({ name: 'mysql:8.0', id: 'sha256:7dcddc01f13bab2f15cde676d44d01f61f' })],
          total: 1,
          truncated: false
        },
        {
          kind: 'volumes',
          items: [item({ name: 'mysql-data', id: 'mysql-data' })],
          total: 1,
          truncated: false
        },
        {
          kind: 'networks',
          items: [item({ name: 'mysql-net', id: 'mysql-net' })],
          total: 1,
          truncated: false
        },
        { kind: 'projects', items: [item({ name: 'shop', id: 'shop' })], total: 1, truncated: false },
        // 未知资源类型：前端补展示（不可跳）。
        { kind: 'widgets', items: [item({ name: 'w1' })], total: 1, truncated: false }
      ],
      unavailableHosts: []
    })
    const { w, router } = await mountSearch()
    await setKeyword(w, 'm')
    await clickSearch(w)

    const click = async (name: string) => {
      const btn = w.findAll('button.dsp-name').find((b) => b.text() === name)!
      expect(btn, `找不到可跳条目 ${name}`).toBeTruthy()
      await btn.trigger('click')
    }
    const waitPath = (path: string) =>
      vi.waitUntil(() => router.currentRoute.value.path === path, { timeout: 5000 })

    // 容器 → 容器详情页 /docker/containers/:id?host=（用条目 id 精确落位）
    await click('mysql')
    await waitPath('/docker/containers/cid1')
    expect(router.currentRoute.value.params.id).toBe('cid1')
    expect(router.currentRoute.value.query.host).toBe('7')

    // 镜像 → 镜像详情页 /docker/image-detail/:id?host=（冒号编码，详情页 decode 回同一 id）
    await click('mysql:8.0')
    await vi.waitUntil(
      () => router.currentRoute.value.path.startsWith('/docker/image-detail/'),
      { timeout: 5000 }
    )
    expect(decodeURIComponent(String(router.currentRoute.value.params.id))).toBe(
      'sha256:7dcddc01f13bab2f15cde676d44d01f61f'
    )
    expect(router.currentRoute.value.query.host).toBe('7')

    // 数据卷 → 列表页 ?host=&keyword=（没有详情页：至少按名把清单收窄到那条）
    await click('mysql-data')
    await waitPath('/docker/volumes')
    expect(router.currentRoute.value.query).toMatchObject({ host: '7', keyword: 'mysql-data' })

    // 网络 → 列表页 ?host=&keyword=
    await click('mysql-net')
    await waitPath('/docker/networks')
    expect(router.currentRoute.value.query).toMatchObject({ host: '7', keyword: 'mysql-net' })

    // Compose → 工作台 /docker/projects/:name?host=（名字在条目里，精确落位）
    await click('shop')
    await waitPath('/docker/projects/shop')
    expect(router.currentRoute.value.query.host).toBe('7')

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

  /**
   * ⑦ 结果区「自持滚动」守卫（本页无表格 → 卡体只裁不滚，必须自己给滚动出路）。
   *
   * 缺陷：本页复用了列表页的卡壳 `<ElCard class="art-table-card">`，但卡里**没有表**
   * ——列表页能滚是因为卡里有一个 `.art-table` 接住 flex 剩余高度成了滚动目标，而
   * 全局样式 `.art-table-card .el-card__body { overflow: hidden }` 只裁不滚。结果区
   * 当时只是 `display:flex; flex-direction:column`（无 overflow、无 min-height），
   * 于是结果一多就被**裁掉**且全页没有任何可滚的层（1920×1080 实测：卡体 clientH
   * 855 / scrollH 1174，末条 bottom 1353 在视口外；1920×760 被裁 639px）。
   *
   * jsdom 不做布局计算，故这里用**源码级锚**（与本文件 ⑥ 同款）钉住三件事：
   *   ① 结果区（.dsp-groups）自己就是滚动目标：overflow 可滚 + min-height:0 +
   *      `flex: 1 1 auto`（basis 不能是 0 —— 卡随内容收缩时卡体没有确定高度）；
   *   ② 卡内常驻块（工具行 / 未参与检索提示 / 四种结果态提示语）flex:none 不参与
   *      高度分配，且都排在结果区**之前**（在滚动区之外 —— 不滚）；
   *   ③ 本页样式里没有任何写死像素高度常数（`height: calc(…)` / `…px`）——
   *      高度一律由 flex 剩余空间决定（本模块刚因 `calc(… - 256px)` 栽过一次）。
   */
  it('⑦ 结果区是真正的滚动目标；常驻 chrome 不滚；无写死像素高度', () => {
    const src = readFileSync(join(process.cwd(), 'src/modules/docker/views/search.vue'), 'utf8')
    const styleStart = src.indexOf('<style')
    expect(styleStart, '找不到 <style> 块').toBeGreaterThan(-1)
    const style = src.slice(styleStart, src.lastIndexOf('</style>'))

    /** 规则扫描：先去掉 `//` 注释（本页注释里有反引号包的 `{ … }` 片段，会把括号配平
     *  带偏），再对每个 `{` 切出它所属的「选择器头」（到上一个 `{`/`}` 为止）与配平的
     *  「规则体」。嵌套规则各算一条。 */
    const rules: { header: string; body: string }[] = []
    {
      const stripped = style
        .split('\n')
        .map((l) => l.replace(/\/\/.*$/, ''))
        .join('\n')
      let boundary = 0
      for (let i = 0; i < stripped.length; i++) {
        const ch = stripped[i]
        if (ch !== '{' && ch !== '}') continue
        if (ch === '{') {
          let depth = 0
          let j = i
          for (; j < stripped.length; j++) {
            if (stripped[j] === '{') depth++
            else if (stripped[j] === '}' && --depth === 0) break
          }
          rules.push({ header: stripped.slice(boundary, i), body: stripped.slice(i + 1, j) })
        }
        boundary = i + 1
      }
    }
    /** 按选择器头定位规则（嵌套里的规则头以它的选择器收尾）。 */
    const rule = (sel: string) => {
      const hit = rules.find((r) => r.header.trimEnd().endsWith(sel))
      expect(hit, `样式里找不到规则 ${sel}`).toBeTruthy()
      return hit!
    }

    // ① 结果区是滚动目标：三条缺一不可（缺 overflow 就被裁；缺 min-height:0 收不下去；
    //    缺 flex:1 1 auto 就吃不到卡体的剩余高度）。
    const groups = rule('.dsp-groups').body
    expect(groups).toMatch(/overflow:\s*(auto|scroll)/)
    expect(groups).toMatch(/min-height:\s*0\b/)
    expect(groups).toMatch(/flex:\s*1\s+1\s+auto\b/)

    // ② 常驻 chrome 不参与高度分配：工具行 + 五种提示块同写一条 flex:none 规则 ——
    //    缺哪一个，那种结果态就会在卡被截住时被压扁/裁掉。
    expect(rule(':deep(#art-table-header)').body).toMatch(/flex:\s*none\b/)
    const pin = rules.find((r) => r.header.includes('.dsp-note') && /flex:\s*none/.test(r.body))
    expect(pin, '未参与检索提示 / 结果态提示语必须有一条 flex:none 规则').toBeTruthy()
    for (const cls of ['.dsp-note', '.dsp-loading', '.dsp-error', '.dsp-prompt', '.dsp-empty']) {
      expect(pin!.header, `${cls} 必须常驻（flex:none）`).toContain(cls)
    }

    // 常驻块都在滚动区**之外**：模板里它们必须排在 `<div class="dsp-groups">` 之前。
    const template = src.slice(0, styleStart)
    const groupsAt = template.indexOf('class="dsp-groups"')
    expect(groupsAt, '模板里找不到结果区 .dsp-groups').toBeGreaterThan(-1)
    for (const marker of ['<ArtTableHeader', 'class="dsp-note"', 'class="dsp-prompt"']) {
      const at = template.indexOf(marker)
      expect(at, `模板里找不到 ${marker}`).toBeGreaterThan(-1)
      expect(at, `${marker} 必须排在滚动区之前（否则会跟着结果一起滚）`).toBeLessThan(groupsAt)
    }

    // ③ 无写死像素高度常数：本页样式里所有 height 声明只能是零/百分比/变量。
    for (const m of style.matchAll(/(?:^|[;{\s])(min-|max-)?height\s*:\s*([^;}\n]+)/g)) {
      expect(m[2]!.trim(), `高度不得写死像素常数：${m[0]!.trim()}`).not.toMatch(/calc\(|\d+px/)
    }
  })
})