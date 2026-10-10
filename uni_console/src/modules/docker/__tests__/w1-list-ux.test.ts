// @vitest-environment jsdom
/**
 * W1「列表页筛选可见性 / 批量栏吸底」的两个钉子 —— 本波坐实的缺陷当波落守卫。
 *
 * ① **筛选激活态**：ArtTableHeader 的搜索钮在「有筛选生效」时进激活态并挂一枚小点。
 *    面板收起但筛选仍在（如 ?host= 深链进入后又收起面板）时，用户要能一眼看出
 *    「这张表被过滤了」。这里用**真组件**挂载断言 class / aria-label —— 源码扫描
 *    钉不住交互态（page-render.test.ts 里表头是替身，看不到真钮）。
 *
 * ② **批量栏吸底**：`.wkl-batch` 必须是 `position: sticky; bottom: 0`（选中后入口不再
 *    被挤出表卡可视区）。scoped 样式在 jsdom 里拿不到计算值，故按源码扫描钉规则本身，
 *    并顺带钉住「不引入任何高度上限」（表格高度政策：默认不限高，见 art-table-height.test.ts）。
 *
 * 说明：ArtTableHeader 经 useAppBreakpoints 在**模块加载时**就建 matchMedia 订阅，
 * jsdom 没有 matchMedia —— 必须在导入组件之前装桩（vi.hoisted 先于 import 执行）。
 */
import { describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { createPinia } from 'pinia'
import { mount } from '@vue/test-utils'

// 装 matchMedia 桩（先于组件导入）：档位判定用得到；回恒 false（常规桌面档）即可。
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

import ArtTableHeader from '@/components/core/tables/art-table-header/index.vue'

/** 真表头挂载：弹层/拖拽类子件不是本用例的关注点，桩掉避免 jsdom 下的渲染噪声。 */
function mountHeader(props: { showSearchBar: boolean; hasActiveFilter: boolean }) {
  return mount(ArtTableHeader, {
    props,
    global: {
      plugins: [createPinia()],
      stubs: {
        ElDropdown: true,
        ElDropdownMenu: true,
        ElDropdownItem: true,
        ElPopover: true,
        ElCheckbox: true,
        ElScrollbar: true,
        VueDraggable: true
      }
    }
  })
}

describe('W1 筛选激活态：ArtTableHeader 搜索钮', () => {
  it('面板收起但筛选生效：带激活类 + 小点，aria-label 说「有筛选生效」', () => {
    const w = mountHeader({ showSearchBar: false, hasActiveFilter: true })
    const btn = w.find('.search-button')
    expect(btn.exists(), '搜索钮应渲染（showSearchBar 非 null）').toBe(true)
    expect(btn.classes()).toContain('active')
    expect(btn.classes()).toContain('has-active-filter')
    expect(btn.attributes('aria-label')).toContain('有筛选生效')
    w.unmount()
  })

  it('面板展开、无筛选：进激活态但不挂小点（面板自己就是线索）', () => {
    const w = mountHeader({ showSearchBar: true, hasActiveFilter: false })
    const btn = w.find('.search-button')
    expect(btn.classes()).toContain('active')
    expect(btn.classes()).not.toContain('has-active-filter')
    expect(btn.attributes('aria-label')).toBe('搜索')
    w.unmount()
  })

  it('面板收起且无筛选：不进激活态（回归基线）', () => {
    const w = mountHeader({ showSearchBar: false, hasActiveFilter: false })
    const btn = w.find('.search-button')
    expect(btn.classes()).not.toContain('active')
    expect(btn.classes()).not.toContain('has-active-filter')
    w.unmount()
  })

  it('两种激活来源并存：状态一致（面板展开 + 筛选生效）', () => {
    const w = mountHeader({ showSearchBar: true, hasActiveFilter: true })
    const btn = w.find('.search-button')
    expect(btn.classes()).toContain('active')
    expect(btn.classes()).toContain('has-active-filter')
    w.unmount()
  })

  it('hasActiveFilter 默认 false：单独挂表头（不传该 prop）不进激活态', () => {
    const w = mount(ArtTableHeader, {
      props: { showSearchBar: false },
      global: { plugins: [createPinia()], stubs: { ElDropdown: true, ElPopover: true } }
    })
    expect(w.find('.search-button').classes()).not.toContain('has-active-filter')
    w.unmount()
  })
})

describe('W1 批量栏吸底：.wkl-batch 的 sticky 规则（源码扫描；scoped 样式 jsdom 拿不到）', () => {
  const src = readFileSync(
    resolve(process.cwd(), 'src/modules/docker/components/workload-batch-bar.vue'),
    'utf8'
  )
  /** `.wkl-batch { … }` 规则块（非 __ 后缀的其它类；含嵌套的 &__count）。 */
  const rule = src.match(/\.wkl-batch\s*\{([\s\S]*?)\n\s*\}/)

  it('扫到 .wkl-batch 规则块（形态变了守卫需同步的自检）', () => {
    expect(rule, '没扫到 .wkl-batch 规则块').toBeTruthy()
  })

  it('规则含 position: sticky 与 bottom: 0（入口钉在可视区底缘）', () => {
    expect(rule![1]).toMatch(/position:\s*sticky/)
    expect(rule![1]).toMatch(/bottom:\s*0/)
  })

  it('吸底块用卡片底色做背景（滚过内容不穿透）', () => {
    expect(rule![1]).toMatch(/background:\s*var\(--default-box-color\)/)
  })

  it('吸底块不引入任何高度上限（表格高度政策：默认不限高）', () => {
    expect(rule![1]).not.toMatch(/max-height/)
    expect(rule![1]).not.toMatch(/\bheight\s*:/)
  })
})
