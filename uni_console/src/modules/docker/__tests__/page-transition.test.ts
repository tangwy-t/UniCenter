// @vitest-environment jsdom
/**
 * 回归：Docker 四个菜单页之间切换白屏（2026-10-01 浏览器实测）。
 *
 * ── 故障形态 ────────────────────────────────────────────────────────
 *
 * 侧边栏在 总览(/docker)、容器(/docker/containers)、镜像与存储(/docker/resources)、
 * 项目(/docker/projects) 之间切换时，内容区整块空白：URL 已变、侧边栏与面包屑还在、
 * 控制台**零报错**、F5 刷新立即恢复。无头浏览器实证定位到布局的页面过渡
 *（components/core/layouts/art-page-content）：
 *
 *   `<Transition mode="out-in">` + `<component :key="route.path">` 的“先离场、
 *   离场完成后再入场”序列中，离场钩子（before-leave / leave / after-leave）全部
 *   走完之后 **入场钩子（before-enter）永远不触发** —— 过渡停留在空占位（DOM 里只剩
 *   一个 `<!---->` 注释），此后所有页面切换持续空白，直到刷新。
 *
 * 触发条件是页面在 ~400ms 的离场窗口内有重渲染落进来（Docker 各页挂载后都会异步
 * 落数据 / 写 route query，正是这类重渲染的密集区）；once 命中，过渡内部待 flush
 * 的补渲染被吞掉，就给不出入场子节点。修复是去掉 mode="out-in"（离/入场动画并行，
 * 同样有交叉过渡，但不存在可卡死的中间态 —— 实测十种导航组合全部恢复）。
 *
 * ── 本文件的两种守法 ───────────────────────────────────────────────
 *
 * 1. **源码守卫**（红→绿）：布局的两个页面 `<Transition>` 上不得再出现
 *    `mode="out-in"` —— 防修复被回退（与 single-root.test 同款口径：源码扫描）。
 * 2. **行为冒烟**：把真实的 ArtPageContent 挂进带嵌套 RouterView 的路由树，
 *    连续 A→B→A 切页，断言每一跳后新页面内容都在、旧页面内容已清 —— 防「过渡
 *    链静默断掉」这类只在渲染期出现的故障（与 page-render.test 同款动机）。
 */
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, nextTick, ref } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { mount, type VueWrapper } from '@vue/test-utils'
import {
  createMemoryHistory,
  createRouter,
  RouterView as RouterViewCmp,
  type Router
} from 'vue-router'

// art-page-content 依赖的头高度计算（@vueuse 的 ResizeObserver 在 jsdom 里不存在）；
// 页面过渡与此无关，桩成固定值。
vi.mock('@/hooks/core/useLayoutHeight', () => ({
  useAutoLayoutHeight: () => ({ containerMinHeight: ref('480px') })
}))

// 前置条件：把两个桩页面挂成「布局 → 子路由」的树 —— 真实应用里 App.vue 的
// RouterView 消费 matched[0]（布局），art-page-content 内的 RouterView 消费
// matched[1]（页面）。直接挂 ArtPageContent 会让它的 RouterView 变成 depth 0，
// 拿到没有组件的父记录渲染出空 —— 与真实深度不一致，所以必须包一层。
const PageA = defineComponent({
  name: 'PageA',
  render: () => h('div', { class: 'page-a-root' }, 'PAGE-A')
})
const PageB = defineComponent({
  name: 'PageB',
  render: () => h('div', { class: 'page-b-root' }, 'PAGE-B')
})
const LayoutShell = defineComponent({
  name: 'LayoutShell',
  render: () => h('div', { class: 'shell' }, [h(ArtPageContent)])
})
// LayoutShell 里 ArtPageContent 是核心布局件，走真实组件（不桩）：
import ArtPageContent from '@/components/core/layouts/art-page-content/index.vue'

/** 最外层：真实应用 App.vue 的 RouterView（depth 0，消费布局记录）。 */
const App = {
  render: () => h(RouterViewCmp)
}

const routes = [
  {
    path: '/',
    component: LayoutShell,
    children: [
      { path: '/a', component: PageA },
      { path: '/b', component: PageB }
    ]
  }
]

/** 等待路由就绪 + 过渡（jsdom 无 CSS，离场按 nextFrame 兜底完成）与下一轮渲染。 */
async function settle(ms = 80): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, ms))
  await nextTick()
}

describe('art-page-content 页面过渡回归（Docker 四页切换白屏）', () => {
  let router: Router
  let wrapper: VueWrapper<unknown>

  beforeEach(async () => {
    setActivePinia(createPinia())
    router = createRouter({ history: createMemoryHistory(), routes })
    await router.push('/a')
    await router.isReady()
    wrapper = mount(App, { global: { plugins: [router] } })
    await settle()
  })

  afterEach(() => {
    wrapper?.unmount()
  })

  it('守卫：布局页面过渡不得使用 mode="out-in"', () => {
    // vitest 的模块 URL 带 /@fs/ 前缀，直接相对 import.meta.url 会被解析到根目录；
    // 走 cwd（vitest 的 cwd 即 uni_console）。
    const file = resolve(process.cwd(), 'src/components/core/layouts/art-page-content/index.vue')
    const src = readFileSync(file, 'utf8')
    // 自检：文件里确实有两个页面过渡（防守卫空转 —— 结构变了没扫到任何 Transition
    // 也应暴露，而不是把「没扫到」当成「合规」）。
    const transitionTags = src.match(/<Transition\b[\s\S]*?>/g) ?? []
    expect(transitionTags.length).toBeGreaterThanOrEqual(2)
    for (const tag of transitionTags) {
      expect(tag).not.toMatch(/mode="out-in"/)
    }
  })

  it('连续切页后新页面渲染、旧页面清除（A→B→A）', async () => {
    expect(wrapper.find('.page-a-root').exists()).toBe(true)
    expect(wrapper.text()).toContain('PAGE-A')

    await router.push('/b')
    await settle()
    expect(wrapper.find('.page-b-root').exists()).toBe(true)
    expect(wrapper.text()).toContain('PAGE-B')
    expect(wrapper.text()).not.toContain('PAGE-A')

    await router.push('/a')
    await settle()
    expect(wrapper.find('.page-a-root').exists()).toBe(true)
    expect(wrapper.text()).toContain('PAGE-A')
    expect(wrapper.text()).not.toContain('PAGE-B')
  })

  it('query 变化不重建页面（?host= 写回路径连续导航不白屏）', async () => {
    // Docker 页挂载后会把主机写回 query（host-context.reload），真实用户路径上
    // 该导航紧跟着任何菜单点击。保证 query 变更 + 紧接着的切页都渲染出来。
    await router.replace({ path: '/a', query: { host: 'h1' } })
    await settle(60)
    expect(wrapper.find('.page-a-root').exists()).toBe(true)

    await router.push({ path: '/b', query: { host: 'h1' } })
    await settle()
    expect(wrapper.find('.page-b-root').exists()).toBe(true)
    expect(wrapper.text()).toContain('PAGE-B')
  })
})
