import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope, nextTick, type EffectScope } from 'vue'

// 高度轴断点判定的行为测试：用可控的 matchMedia 桩同时驱动宽度与高度两个轴。
//
// 覆盖的约定：
// 1. heightAtMost 走独立的 max-height 查询（不借用宽度轴的 useBreakpoints）；
// 2. 档位随视口高度变化即时翻转（旋转/拉窗口）——依赖它的 JS 闸门（阀让位、
//    侧栏图标栏态）才会响应；
// 3. 判定结果按名字缓存（同一 ref），避免每处调用新建一组 matchMedia 订阅。
//
// node 环境没有 matchMedia：与 useResponsiveMenu.test.ts 同一手法，用内存替身，
// 且必须在 import 断点模块前就绪（VueUse 在模块加载时判定运行环境）。

const mediaController = vi.hoisted(() => {
  interface Entry {
    query: string
    matches: boolean
    listeners: Set<(event: { matches: boolean; media: string }) => void>
  }

  const queries = new Map<string, Entry>()
  const viewport = { width: 1440, height: 900 }

  const evaluate = (query: string, size: { width: number; height: number }): boolean => {
    const conditions = [...query.matchAll(/\((min|max)-(width|height):\s*(-?[\d.]+)px\s*\)/g)]
    if (conditions.length === 0) return false

    return conditions.every(([, kind, axis, value]) => {
      const px = Number(value)
      const current = axis === 'width' ? size.width : size.height
      return kind === 'min' ? current >= px : current <= px
    })
  }

  type Listener = (event: { matches: boolean; media: string }) => void

  const matchMedia = (query: string) => {
    let entry = queries.get(query)
    if (!entry) {
      entry = { query, matches: evaluate(query, viewport), listeners: new Set() }
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

  /** 改视口尺寸并触发已注册媒体查询的 change 事件 */
  const setViewport = (next: { width?: number; height?: number }) => {
    if (next.width !== undefined) viewport.width = next.width
    if (next.height !== undefined) viewport.height = next.height

    for (const entry of queries.values()) {
      const matches = evaluate(entry.query, viewport)
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
    innerWidth: viewport.width,
    innerHeight: viewport.height,
    addEventListener: noop,
    removeEventListener: noop,
    setTimeout,
    clearTimeout
  }

  return { setViewport }
})

type BreakpointsModule = typeof import('./useAppBreakpoints')

describe('useAppBreakpoints（高度轴）', () => {
  let mod: BreakpointsModule
  const scopes: EffectScope[] = []

  beforeAll(async () => {
    mod = await import('./useAppBreakpoints')
  })

  beforeEach(() => {
    mediaController.setViewport({ width: 1440, height: 900 })
  })

  /** 在独立 effect scope 中取用判定，便于逐个用例回收 */
  function mount() {
    const scope = effectScope()
    const api = scope.run(() => mod.useAppBreakpoints())!
    scopes.push(scope)
    return api
  }

  it('常规桌面（1440×900）：两个高度档都不中', () => {
    const { heightAtMost } = mount()
    expect(heightAtMost('short').value).toBe(false)
    expect(heightAtMost('phoneShort').value).toBe(false)
  })

  it('矮桌面窗（1280×620）：short 中、phoneShort 不中', async () => {
    mediaController.setViewport({ width: 1280, height: 620 })
    const { heightAtMost } = mount()
    await nextTick()

    expect(heightAtMost('short').value).toBe(true)
    expect(heightAtMost('phoneShort').value).toBe(false)
  })

  it('手机横屏（844×390）：两个高度档都中', async () => {
    mediaController.setViewport({ width: 844, height: 390 })
    const { heightAtMost } = mount()
    await nextTick()

    expect(heightAtMost('short').value).toBe(true)
    expect(heightAtMost('phoneShort').value).toBe(true)
  })

  it('档位随视口高度翻转（旋转/拉窗的响应式闸门）', async () => {
    const { heightAtMost } = mount()
    const short = heightAtMost('short')
    expect(short.value).toBe(false)

    mediaController.setViewport({ height: 390, width: 844 })
    await nextTick()
    expect(short.value).toBe(true)

    mediaController.setViewport({ height: 900, width: 844 })
    await nextTick()
    expect(short.value).toBe(false)
  })

  it('宽度轴不受高度轴影响，反之亦然（两轴独立）', async () => {
    const { smaller, heightAtMost } = mount()
    const isPhone = smaller('tablet')
    const short = heightAtMost('short')
    expect(isPhone.value).toBe(false)

    // 只压高度：宽度轴不动、高度轴翻档
    mediaController.setViewport({ height: 390 })
    await nextTick()
    expect(isPhone.value).toBe(false)
    expect(short.value).toBe(true)

    // 只压宽度：宽度轴翻档、高度轴不动
    mediaController.setViewport({ width: 375, height: 900 })
    await nextTick()
    expect(isPhone.value).toBe(true)
    expect(short.value).toBe(false)
  })

  it('同名判定返回同一份缓存（不重复建订阅）', () => {
    const a = mount()
    const b = mount()
    expect(a.heightAtMost('short')).toBe(b.heightAtMost('short'))
    expect(a.smaller('tablet')).toBe(b.smaller('tablet'))
  })
})
