// @vitest-environment jsdom
/**
 * ArtTable 默认限高策略的回归守卫（报裁 2026-10「表格必须有高度上限」）。
 *
 * 钉住的语义（都是组件级真渲染断言，不做源码扫描）：
 * 1. 有行且未指定高度：ElTable 只拿 max-height（默认安全阀），拿不到 height ——
 *    两者若同时下发，EP 的内滚会整个失效（scrollbarStyle 取 height 分支），
 *    这正是本策略最容易回归的点；
 * 2. 空/加载中（一行都没有）：走旧口径（emptyHeight / '100%'），默认安全阀不插手；
 * 3. 显式 height：页面接管高度权，默认安全阀让位（逃生门）；
 * 4. **短视口档（本批）**：视口高度 ≤ short（640）时默认阀整体让位 ——
 *    ElTable 拿 height（fill）、不拿 max-height，表体高度交给页面 flex 链
 *    （单滚动的档位开关，见 index.vue 的 maxHeight）。
 *
 * jsdom 没有 matchMedia（真实环境里 useMediaQuery 恒 false），短视口档必须能
 * 注入桩才测得到：下面把可控桩装在**所有 mount 之前**（模块级单例订阅只认
 * 第一份桩），再用 setViewport 的高度驱动档位翻转 —— 顺带把「旋转/拉窗时阀
 * 跟着换档」的响应式一起钉住。
 */
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createPinia } from 'pinia'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'

/** 可控 matchMedia 桩：装在所有用例之前（见文件头注释） */
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

  /** 改视口尺寸并触发已注册媒体查询的 change 事件（jsdom 其余部分保持原样） */
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

  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    configurable: true,
    value: matchMedia
  })

  const reset = () => setViewport({ width: 1440, height: 900 })

  return { setViewport, reset }
})

// 在桩装好之后再引入 ArtTable（它经 useAppBreakpoints 建模块级单例订阅）
const { default: ArtTable } = await import('./index.vue')

function mountArtTable(props: Record<string, unknown> = {}) {
  return mount(ArtTable, {
    // data 是 ElTable 的必填 prop（TableProps 里没有默认值），基线给空表；
    // 用例各自经 props 覆盖成有行形态。
    props: { data: [], columns: [], ...props },
    global: { plugins: [createPinia()] }
  })
}

/** 等 EP 的 setHeight/setMaxHeight watchEffect 与本地清理 watch 落完。 */
async function settle(wrapper: ReturnType<typeof mountArtTable>) {
  await nextTick()
  await nextTick()
  return wrapper.find('.el-table').element as HTMLElement
}

afterEach(() => {
  // 每个用例后回常规桌面档：免得用例间的档位互相串味
  mediaController.reset()
})

describe('ArtTable 默认限高策略', () => {
  it('有数据且未指定高度：只下发 max-height（默认安全阀），不下发 height', async () => {
    const wrapper = mountArtTable({ data: [{ a: 1 }, { a: 2 }] })
    const el = await settle(wrapper)
    expect(el.style.maxHeight).toContain('100vh')
    expect(el.style.height).toBe('')
    wrapper.unmount()
  })

  it('空数据（非加载）：不设默认安全阀，仍走 emptyHeight 口径', async () => {
    const wrapper = mountArtTable({ data: [] })
    const el = await settle(wrapper)
    expect(el.style.maxHeight).toBe('')
    expect(el.style.height).toBe('100%')
    wrapper.unmount()
  })

  it('加载中且无行：不设默认安全阀（旧口径 height 100%）', async () => {
    const wrapper = mountArtTable({ data: [], loading: true })
    const el = await settle(wrapper)
    expect(el.style.maxHeight).toBe('')
    expect(el.style.height).toBe('100%')
    wrapper.unmount()
  })

  it('显式 height：页面接管高度口径，默认安全阀让位', async () => {
    const wrapper = mountArtTable({ data: [{ a: 1 }], height: '320px' })
    const el = await settle(wrapper)
    expect(el.style.height).toBe('320px')
    expect(el.style.maxHeight).toBe('')
    wrapper.unmount()
  })

  it('显式 max-height：覆盖默认安全阀原样透传', async () => {
    const wrapper = mountArtTable({ data: [{ a: 1 }], maxHeight: '26rem' })
    const el = await settle(wrapper)
    expect(el.style.maxHeight).toBe('26rem')
    expect(el.style.height).toBe('')
    wrapper.unmount()
  })
})

describe('ArtTable 短视口档：默认阀让位（单滚动的档位开关）', () => {
  it('手机横屏（844×390）：默认阀让位 —— 只给 height（fill），不给 max-height', async () => {
    mediaController.setViewport({ width: 844, height: 390 })
    const wrapper = mountArtTable({ data: [{ a: 1 }, { a: 2 }] })
    const el = await settle(wrapper)

    // 让位口径 = fill：height 100%（表体高度交给页面 flex 链），默认阀不插手
    expect(el.style.maxHeight).toBe('')
    expect(el.style.height).toBe('100%')
    wrapper.unmount()
  })

  it('矮桌面窗（1280×620）：同样让位（4 行 → ~6 行的来源）', async () => {
    mediaController.setViewport({ width: 1280, height: 620 })
    const wrapper = mountArtTable({ data: [{ a: 1 }] })
    const el = await settle(wrapper)

    expect(el.style.maxHeight).toBe('')
    expect(el.style.height).toBe('100%')
    wrapper.unmount()
  })

  it('常规视口（1600×900）：默认阀在原位（桌面零回归）', async () => {
    mediaController.setViewport({ width: 1600, height: 900 })
    const wrapper = mountArtTable({ data: [{ a: 1 }] })
    const el = await settle(wrapper)

    expect(el.style.maxHeight).toContain('100vh')
    expect(el.style.height).toBe('')
    wrapper.unmount()
  })

  it('档位翻转是响应式的：900 → 390 让位，回到 900 阀复位', async () => {
    const wrapper = mountArtTable({ data: [{ a: 1 }] })
    const el = await settle(wrapper)
    expect(el.style.maxHeight).toContain('100vh')

    mediaController.setViewport({ height: 390 })
    await settle(wrapper)
    expect(el.style.maxHeight).toBe('')
    expect(el.style.height).toBe('100%')

    mediaController.setViewport({ height: 900 })
    await settle(wrapper)
    expect(el.style.maxHeight).toContain('100vh')
    expect(el.style.height).toBe('')
    wrapper.unmount()
  })

  it('显式 max-height 在短视口仍优先（逃生门 > 档位）', async () => {
    mediaController.setViewport({ width: 844, height: 390 })
    const wrapper = mountArtTable({ data: [{ a: 1 }], maxHeight: '26rem' })
    const el = await settle(wrapper)

    expect(el.style.maxHeight).toBe('26rem')
    expect(el.style.height).toBe('')
    wrapper.unmount()
  })

  it('显式 height 在短视口仍接管高度口径（逃生门 > 档位）', async () => {
    mediaController.setViewport({ width: 844, height: 390 })
    const wrapper = mountArtTable({ data: [{ a: 1 }], height: '320px' })
    const el = await settle(wrapper)

    expect(el.style.height).toBe('320px')
    expect(el.style.maxHeight).toBe('')
    wrapper.unmount()
  })

  it('空态让位分支与短视口档一致：仍走 emptyHeight 旧口径', async () => {
    mediaController.setViewport({ width: 844, height: 390 })
    const wrapper = mountArtTable({ data: [] })
    const el = await settle(wrapper)

    expect(el.style.maxHeight).toBe('')
    expect(el.style.height).toBe('100%')
    wrapper.unmount()
  })
})
