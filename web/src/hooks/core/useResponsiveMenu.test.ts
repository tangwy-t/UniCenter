import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope, nextTick, type EffectScope } from 'vue'

// 断点行为测试：通过可控的 matchMedia 桩驱动 VueUse useBreakpoints。
//
// 覆盖的核心约定（改造前是三个 bug 的来源）：
// 1. 进入手机断点不会改写持久化偏好（旧实现在断点切换时直接写 localStorage）
// 2. 手机端抽屉开合只改瞬态状态，不触碰持久化偏好
// 3. 桌面端完全遵循持久化偏好，只有用户 toggle 才写 store
// 4. 离开手机断点后抽屉复位，回到桌面看到的是用户偏好
//
// node 环境没有 matchMedia：与仓库既有测试（如 remember-login.test.ts）一致用内存替身。
// VueUse 在模块加载时判定运行环境，因此替身必须在 import 前就绪，用 vi.hoisted 保证时序。
// store 用 mock 替身（本测试只关心 composable 是否调用 setMenuOpen，避免拉起整个应用依赖图）。

const mediaController = vi.hoisted(() => {
  interface Entry {
    query: string
    matches: boolean
    listeners: Set<(event: { matches: boolean; media: string }) => void>
  }

  const queries = new Map<string, Entry>()
  let width = 1440

  const evaluate = (query: string, currentWidth: number): boolean => {
    // 支持复合查询，例如 VueUse 的 between() 生成
    // "(min-width: 768px) and (max-width: 1023.9px)"
    const conditions = [...query.matchAll(/\((min|max)-width:\s*(-?[\d.]+)px\s*\)/g)]
    if (conditions.length === 0) {
      return false
    }

    return conditions.every(([, kind, value]) => {
      const px = Number(value)
      return kind === 'min' ? currentWidth >= px : currentWidth <= px
    })
  }

  type Listener = (event: { matches: boolean; media: string }) => void

  const matchMedia = (query: string) => {
    let entry = queries.get(query)
    if (!entry) {
      entry = { query, matches: evaluate(query, width), listeners: new Set() }
      queries.set(query, entry)
    }
    const current = entry

    return {
      media: query,
      get matches() {
        return current.matches
      },
      addEventListener: (_type: string, listener: Listener) => current.listeners.add(listener),
      removeEventListener: (_type: string, listener: Listener) =>
        current.listeners.delete(listener),
      addListener: (listener: Listener) => current.listeners.add(listener),
      removeListener: (listener: Listener) => current.listeners.delete(listener),
      onchange: null,
      dispatchEvent: () => false
    }
  }

  /** 改变视口宽度并触发已注册媒体查询的 change 事件 */
  const setViewport = (nextWidth: number) => {
    width = nextWidth
    for (const entry of queries.values()) {
      const matches = evaluate(entry.query, nextWidth)
      if (matches !== entry.matches) {
        entry.matches = matches
        entry.listeners.forEach((listener) => listener({ matches, media: entry.query }))
      }
    }
  }

  const noop = () => {}
  const elementStub = () => ({
    style: { setProperty: noop, removeProperty: noop },
    classList: { add: noop, remove: noop, toggle: noop, contains: () => false },
    setAttribute: noop,
    removeAttribute: noop,
    appendChild: noop,
    removeChild: noop,
    insertBefore: noop,
    addEventListener: noop,
    removeEventListener: noop,
    getBoundingClientRect: () => ({ top: 0, left: 0, width: 0, height: 0, bottom: 0, right: 0 }),
    innerHTML: '',
    textContent: ''
  })
  // VueUse 的 isClient 同时检查 window 与 document（typeof document !== 'undefined'），
  // 缺一则 defaultWindow 为 undefined、useMediaQuery 直接跳过注册；Vue runtime-dom
  // 在模块加载时还会调用 document.createElement。
  ;(globalThis as Record<string, unknown>).document = {
    documentElement: elementStub(),
    body: elementStub(),
    head: elementStub(),
    createElement: elementStub,
    createTextNode: elementStub,
    createComment: elementStub,
    createDocumentFragment: elementStub,
    querySelector: () => null,
    querySelectorAll: () => [],
    getElementById: () => null,
    addEventListener: noop,
    removeEventListener: noop
  }
  ;(globalThis as Record<string, unknown>).window = {
    matchMedia,
    innerWidth: 1440,
    addEventListener: noop,
    removeEventListener: noop,
    setTimeout,
    clearTimeout
  }

  return { setViewport }
})

/** 设置 store 替身：menuOpen 为 ref，setMenuOpen 记录调用（用于断言"未改写偏好"） */
const storeState = vi.hoisted(() => ({
  menuOpen: null as unknown,
  setMenuOpenCalls: [] as boolean[]
}))

vi.mock('@/store/modules/setting', async () => {
  const { ref } = await import('vue')
  const menuOpen = ref(true)
  storeState.menuOpen = menuOpen

  return {
    useSettingStore: () => ({
      menuOpen,
      setMenuOpen: (open: boolean) => {
        storeState.setMenuOpenCalls.push(open)
        menuOpen.value = open
      }
    })
  }
})

type ResponsiveMenuModule = typeof import('./useResponsiveMenu')

describe('useResponsiveMenu（响应式菜单派生状态）', () => {
  let mod: ResponsiveMenuModule
  const scopes: EffectScope[] = []

  beforeAll(async () => {
    // 必须在 matchMedia 桩就绪后再加载（composable 的断点单例在模块加载时创建）
    mod = await import('./useResponsiveMenu')
  })

  beforeEach(() => {
    mediaController.setViewport(1440)
    storeState.setMenuOpenCalls.length = 0
    const menuOpen = storeState.menuOpen as { value: boolean }
    menuOpen.value = true
  })

  /** 在独立 effect scope 中挂载 composable，便于逐个测试回收 watch */
  function mount() {
    const scope = effectScope()
    const state = scope.run(() => mod.useResponsiveMenu())!
    scopes.push(scope)
    return state
  }

  it('桌面端：菜单可见性跟随持久化偏好，toggle 才写入 store', () => {
    const { isPhone, isMenuVisible, toggleMenu } = mount()
    expect(isPhone.value).toBe(false)
    expect(isMenuVisible.value).toBe(true)

    toggleMenu()
    expect(storeState.setMenuOpenCalls).toEqual([false])
    expect(isMenuVisible.value).toBe(false)
  })

  it('进入手机断点：抽屉默认关闭，且不改写持久化偏好', async () => {
    const { isPhone, isMenuVisible } = mount()
    mediaController.setViewport(375)
    await nextTick()

    expect(isPhone.value).toBe(true)
    // 渲染上抽屉是关闭的……
    expect(isMenuVisible.value).toBe(false)
    // ……但不得写入持久化偏好（回归保护：旧实现会写入 false）
    expect(storeState.setMenuOpenCalls).toEqual([])
    expect((storeState.menuOpen as { value: boolean }).value).toBe(true)
  })

  it('手机端抽屉开合只改瞬态状态，不触碰持久化偏好', async () => {
    const { isMenuVisible, toggleMenu } = mount()
    mediaController.setViewport(375)
    await nextTick()

    toggleMenu()
    expect(isMenuVisible.value).toBe(true)
    expect(storeState.setMenuOpenCalls).toEqual([])

    toggleMenu()
    expect(isMenuVisible.value).toBe(false)
    expect(storeState.setMenuOpenCalls).toEqual([])
    expect((storeState.menuOpen as { value: boolean }).value).toBe(true)
  })

  it('离开手机断点后抽屉复位，回到桌面看到用户偏好', async () => {
    const { isPhone, isMenuVisible, toggleMenu } = mount()
    mediaController.setViewport(375)
    await nextTick()
    toggleMenu()
    expect(isMenuVisible.value).toBe(true)

    mediaController.setViewport(1440)
    await nextTick()
    expect(isPhone.value).toBe(false)
    expect(isMenuVisible.value).toBe(true) // 偏好未被改写

    // 再次进入手机断点时抽屉处于关闭状态
    mediaController.setViewport(375)
    await nextTick()
    expect(isMenuVisible.value).toBe(false)
  })

  it('closeMobileDrawer 在桌面端为 no-op，不影响用户偏好', () => {
    const { closeMobileDrawer, isMenuVisible } = mount()
    closeMobileDrawer()
    expect(storeState.setMenuOpenCalls).toEqual([])
    expect(isMenuVisible.value).toBe(true)
  })
})
