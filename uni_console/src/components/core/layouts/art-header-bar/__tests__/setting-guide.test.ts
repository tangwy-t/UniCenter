// @vitest-environment jsdom
/**
 * 守卫：顶栏「设置引导」浮层不再常驻遮挡（P1-1 回归钉子）。
 *
 * ── 缺陷 ────────────────────────────────────────────────────────────
 * 引导浮层锚在顶栏设置钮上（`position:absolute`、z-index 3005、190×85），**不随内容
 * 滚动**。`SETTING_DEFAULT_CONFIG.showSettingGuide` 原为 `true`，于是它对新用户常驻：
 * 真滚轮实测 `elementFromPoint` 命中浮层，volumes「更多操作」49/50 步、images「详情」
 * 49/50 步、image-drift「声明目标」1366 档 59/60 步被盖；844×390 滚 ~80px 后表格
 * 工具条（搜索/刷新/表格密度/全屏）整块被盖。
 *
 * ── 本文件钉住的不变量（三条，缺一即回归）────────────────────────────
 *  1. **默认关**：`SETTING_DEFAULT_CONFIG.showSettingGuide === false` —— 新用户
 *     首次进入就不会有这个常驻浮层（改回 true 本用例必红）。
 *  2. **显隐由持久化偏好控制**：store 初始值读自 localStorage 的 `…-setting`
 *     （写成 true → 浮层开；无该项 → 默认关）；`hideSettingGuide` 写回偏好。
 *     也就是说它不是"写死的常驻 true"，而是可持久化的用户偏好。
 *  3. **挂载真组件**：ArtHeaderBar 渲染的 ElPopover 的 `visible` 绑定随偏好变化
 *     —— 默认态下不可见，偏好打开后才可见。浮层与偏好之间没有别的开关短路。
 *
 * 另有一条源码级钉子：`onAnyScroll` 必须在捕获阶段监听 scroll 并在浮层可见时
 * 收起（浮层不随内容滚动，靠滚动自动收起保证它不可能长期盖住滚动经过的钮）。
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { ref, defineComponent, h, createApp } from 'vue'
import { createPinia, setActivePinia, type Pinia } from 'pinia'
import { createPersistedState } from 'pinia-plugin-persistedstate'
import { mount } from '@vue/test-utils'

// 档位判定（useAppBreakpoints）在**模块加载时**就建 matchMedia 订阅 —— 必须在
// 导入 ArtHeaderBar 之前装桩（同 w1-list-ux.test.ts 的口径）。
vi.hoisted(() => {
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    configurable: true,
    value: (query: string) => ({
      media: query,
      matches: false,
      addEventListener() {},
      removeEventListener() {},
      addListener() {},
      removeListener() {},
      onchange: null,
      dispatchEvent: () => false
    })
  })
})

// 顶栏正文里显式 import 的子件会**加载整个模块**（哪怕模板被 stub 掉）：
// ArtUserMenu → user store → api/router，socket store → WS 客户端。这里按名替身，
// 免去与守卫无关的依赖（本文件断言的是引导浮层的显隐接线，不是这些子件）。
const menuList = ref([])
vi.mock('@/store/modules/menu', () => ({
  useMenuStore: () => ({ menuList })
}))
const unreadCount = ref(0)
const lastNotice = ref(null)
vi.mock('@/store/modules/socket', () => ({
  useSocketStore: () => ({ unreadCount, lastNotice })
}))
vi.mock('../widget/ArtUserMenu.vue', () => ({
  default: defineComponent({ name: 'ArtUserMenu', setup: () => () => h('div') })
}))

import { SETTING_DEFAULT_CONFIG } from '@/config/setting'
import { useSettingStore } from '@/store/modules/setting'
import ArtHeaderBar from '../index.vue'

/** localStorage 里 setting 偏好的键（pinia-plugin-persistedstate 的版本化键）。 */
const SETTING_KEY = `sys-v${__APP_VERSION__}-setting`

/**
 * 新建一份带持久化插件的 pinia 并**安装到 Vue app**。
 *
 * 必须 `createApp({}).use(pinia)`：Pinia 3 的 `use()` 在 pinia 未安装到 app 时只把
 * 插件压进 `toBeInstalled`，要等 `install(app)` 才并入 `_p` —— 测试里不安装 app 的话
 * 插件根本不跑（本文件第一版就踩了这个坑，持久化断言全红）。
 */
function freshPinia(): Pinia {
  const pinia = createPinia()
  pinia.use(createPersistedState({ key: (id: string) => `sys-v${__APP_VERSION__}-${id}` }))
  createApp({}).use(pinia)
  setActivePinia(pinia)
  return pinia
}

/** ElPopover 替身：把 `visible` 落到 DOM 上，便于断言"浮层此刻可见否"。 */
const PopoverProbe = defineComponent({
  name: 'ElPopover',
  props: { visible: { type: Boolean, default: false } },
  setup(props, { slots }) {
    return () =>
      h('div', { class: 'guide-popover', 'data-visible': String(props.visible) }, [
        props.visible ? slots.default?.() : null,
        slots.reference?.()
      ])
  }
})

function mountHeader() {
  return mount(ArtHeaderBar, {
    global: {
      stubs: {
        ElPopover: PopoverProbe,
        ElBadge: true,
        ArtLogo: true,
        ArtIconButton: true,
        ArtBreadcrumb: true,
        ArtHorizontalMenu: true,
        ArtMixedMenu: true,
        ArtWorkTab: true,
        ArtNotification: true,
        ArtUserMenu: true
      }
    }
  })
}

beforeEach(() => {
  localStorage.clear()
})

describe('P1-1 设置引导浮层：默认关 + 显隐由持久化偏好控制', () => {
  it('默认值不再是常驻 true（改回 true 本用例必红）', () => {
    expect(SETTING_DEFAULT_CONFIG.showSettingGuide).toBe(false)
  })

  it('无持久化偏好时 store 默认关；写成 true 后初始值即为 true', () => {
    freshPinia()
    expect(useSettingStore().showSettingGuide, '默认态必须是关').toBe(false)

    localStorage.setItem(SETTING_KEY, JSON.stringify({ showSettingGuide: true }))
    freshPinia()
    expect(useSettingStore().showSettingGuide, '持久化偏好应被尊重').toBe(true)
  })

  it('hideSettingGuide 写回持久化偏好（收起后不再复现）', async () => {
    const pinia = freshPinia()
    const store = useSettingStore()
    store.openSettingGuide()
    expect(store.showSettingGuide).toBe(true)

    store.hideSettingGuide()
    expect(store.showSettingGuide).toBe(false)
    // 持久化插件在 state 变更后落盘（$subscribe 默认非 sync：等一拍）
    await new Promise((r) => setTimeout(r, 0))
    expect(pinia.state.value.settingStore.showSettingGuide).toBe(false)
    expect(JSON.parse(localStorage.getItem(SETTING_KEY) || '{}').showSettingGuide).toBe(false)
  })
})

describe('P1-1 设置引导浮层：ArtHeaderBar 的可见性接线', () => {
  it('默认态：浮层不可见（不常驻遮挡）', () => {
    freshPinia()
    const w = mountHeader()
    expect(w.find('.guide-popover').attributes('data-visible')).toBe('false')
    w.unmount()
  })

  it('偏好打开后浮层可见；收起后回到不可见', async () => {
    freshPinia()
    const w = mountHeader()
    const store = useSettingStore()

    store.openSettingGuide()
    await w.vm.$nextTick()
    expect(w.find('.guide-popover').attributes('data-visible')).toBe('true')

    store.hideSettingGuide()
    await w.vm.$nextTick()
    expect(w.find('.guide-popover').attributes('data-visible')).toBe('false')
    w.unmount()
  })

  it('持久化偏好为 true 时，挂载即可见（显隐真由偏好控制，不是写死 false）', () => {
    localStorage.setItem(SETTING_KEY, JSON.stringify({ showSettingGuide: true }))
    freshPinia()
    const w = mountHeader()
    expect(w.find('.guide-popover').attributes('data-visible')).toBe('true')
    w.unmount()
  })
})

describe('P1-1 设置引导浮层：滚动即自动收起（源码级钉子）', () => {
  const src = readFileSync(
    resolve(process.cwd(), 'src/components/core/layouts/art-header-bar/index.vue'),
    'utf8'
  )

  it('onAnyScroll 在浮层可见时收起引导', () => {
    const fn = src.match(/const onAnyScroll[\s\S]*?\n {2}\}/)
    expect(fn, '没有 onAnyScroll —— 滚动自动收起的实现被删了').toBeTruthy()
    expect(fn![0]).toContain('hideSettingGuide')
  })

  it('scroll 监听挂在捕获阶段（内层滚动容器也要能触发）', () => {
    expect(src).toMatch(/addEventListener\('scroll',\s*onAnyScroll,\s*true\)/)
    expect(src).toMatch(/removeEventListener\('scroll',\s*onAnyScroll,\s*true\)/)
  })
})
