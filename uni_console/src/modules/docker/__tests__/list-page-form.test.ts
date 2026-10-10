/**
 * 范式守卫：Docker 列表页的「纯清单形态」不得回退（源码扫描）。
 *
 * ── 字体来由（2026-10-04 用户裁定）────────────────────────────────
 * 列表页 = 纯清单，参考系统管理：页头（hero）/三态重卡/解释句整块删除；页面 =
 * 搜索栏（或工具条内筛选）+ art-table-card（工具条 ArtTableHeader：#left 承载
 * 动作钮与计数文本）+ 表 + 弹窗。这是「新形态的事实」—— 本波（列表页纯化）
 * 落地后，挂载冒烟（page-render）钉不住「hero 又回来了」这类回退：页面照样
 * 挂载、用例照样绿，只有真机上才看得出。这里把七页的形态红线钉成源码级断言。
 *
 * ── 断言清单（剥注释后扫描）──────────────────────────────────────
 *   - 不得出现 `wkl-hero` / `usePageIcon` / `wkl-card` / `wkl-state`
 *     （去英雄、去三态重卡：hero 与页头图标源一起退场）；
 *   - 必须出现 `ArtSearchBar`（豁免：tasks / events —— 其筛选是工具条/卡内
 *     就地形态，理由写进豁免表）、`art-table-card`、`ArtTableHeader`；
 *   - 页面动作钮必须在 `ArtButtonTable` 家族（豁免：projects / networks ——
 *     两页工具条确无动作钮，理由写进豁免表；豁免表有自检防腐烂）；
 *   - 页根（模板里第一个 div）不带 `overflow-y-auto`（滚动交给表体的单滚动链）。
 *
 * 手法与 copy-no-internals.test.ts 同款：读源码 → 剥注释 → 断言。豁免不是漏洞
 * 通道：每个豁免都写死理由，且有「豁免确实需要」（豁免页真的没有那个东西）的
 * 自检 —— 形态变了先改表，不许无声漂移。
 */
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const ROOT = resolve(process.cwd(), 'src/modules/docker/views')

/** 列表页（本守卫的作用面；详情/工作台/创建页不在其列）。image-drift 是 Wave 4b
 *  的隐藏列表页（从镜像页工具条进），与五个资源列表页同一纯清单形态。 */
const PAGES = [
  'containers.vue',
  'projects.vue',
  'tasks.vue',
  'events.vue',
  'images.vue',
  'volumes.vue',
  'networks.vue',
  'image-drift.vue'
] as const

/** 剥注释：模板 HTML 注释 + JS/SCSS 的块注释与行注释（注释里的旧形态字样不参与断言）。 */
function stripComments(src: string): string {
  return src
    .replace(/<!--[\s\S]*?-->/g, '')
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/\/\/[^\n]*/g, '')
}

function sourceOf(page: (typeof PAGES)[number]): string {
  return stripComments(readFileSync(resolve(ROOT, page), 'utf8'))
}

/**
 * ArtSearchBar 豁免：这两页的筛选不是统一搜索面板，而是工具条/卡内的就地形态。
 * 理由写死在此，不许无声增删。
 */
const SEARCH_BAR_EXEMPT: Record<string, string> = {
  'tasks.vue': '筛选是工具条内 ElRadioGroup（状态三档，服务端过滤），整页形态无搜索面板',
  'events.vue': '过滤条在卡内自建行（主机/类型/关键字，服务端过滤），非 ArtSearchBar 面板'
}

/**
 * ArtButtonTable 豁免：这两页工具条确无动作钮（强行要求 import 会是不摆位的死
 * import —— 与「死引用直接删」的纪律相左）。
 */
const ACTION_BTN_EXEMPT: Record<string, string> = {
  'projects.vue': '工具条只有计数文本；唯一动作「打开工作台」是行内文本按钮',
  'networks.vue': '本页没有整体动作（网络无批量动作），行内只有删除菜单'
}

describe('docker 列表页形态守卫（纯清单：无 hero / 无三态重卡 / 工具条归 ArtTableHeader）', () => {
  it('扫描不是空转：列表页都在且非空', () => {
    for (const page of PAGES) {
      const src = sourceOf(page)
      expect(src.length, `${page} 读不到内容`).toBeGreaterThan(0)
      expect(src, `${page} 不是 Vue 单文件（缺 <template>）`).toContain('<template>')
    }
  })

  it.each(PAGES)('%s：hero / 页头图标源 / 三态重卡不得回退', (page) => {
    const src = sourceOf(page)
    for (const dead of ['wkl-hero', 'usePageIcon', 'wkl-card', 'wkl-state']) {
      expect(src, `${page} 出现了已裁形态「${dead}」（列表页纯化红线）`).not.toContain(dead)
    }
  })

  it.each(PAGES)(
    '%s：无包裹层的表卡形态（ArtSearchBar / art-table-card / ArtTableHeader）',
    (page) => {
      const src = sourceOf(page)
      expect(src, `${page} 缺 art-table-card（表卡是纯清单的载体）`).toContain('art-table-card')
      expect(src, `${page} 缺 ArtTableHeader（工具条是动作与计数的唯一落点）`).toContain(
        'ArtTableHeader'
      )
      if (!(page in SEARCH_BAR_EXEMPT)) {
        expect(src, `${page} 缺 ArtSearchBar（豁免页见 SEARCH_BAR_EXEMPT）`).toContain(
          'ArtSearchBar'
        )
      }
    }
  )

  it.each(PAGES)('%s：页面动作钮形制是 ArtButtonTable 家族', (page) => {
    if (page in ACTION_BTN_EXEMPT) return
    expect(
      sourceOf(page),
      `${page} 缺 ArtButtonTable（工具条动作钮必须走它；豁免页见 ACTION_BTN_EXEMPT）`
    ).toContain('ArtButtonTable')
  })

  it.each(PAGES)('%s：页根不带 overflow-y-auto（滚动归表体的单滚动链）', (page) => {
    const src = sourceOf(page)
    // 页根 = <template> 后的第一个 div（注释已剥掉，不再隔挡）。
    const root = src.match(/<template>\s*<div[^>]*\bclass="([^"]*)"/)
    expect(root, `${page} 没扫到页根 div 的 class —— 页根形态变了，守卫需同步`).toBeTruthy()
    expect(root![1], `${page} 页根带了 overflow-y-auto`).not.toContain('overflow-y-auto')
  })

  it('豁免表自检：豁免确实需要（豁免页现在真的没有那个东西）+ 理由写全', () => {
    for (const [page, reason] of Object.entries(SEARCH_BAR_EXEMPT)) {
      expect(reason.length, `${page} 的豁免理由不能为空`).toBeGreaterThan(0)
      expect(
        sourceOf(page as (typeof PAGES)[number]),
        `${page} 已有 ArtSearchBar，豁免该删`
      ).not.toContain('ArtSearchBar')
    }
    for (const [page, reason] of Object.entries(ACTION_BTN_EXEMPT)) {
      expect(reason.length, `${page} 的豁免理由不能为空`).toBeGreaterThan(0)
      expect(
        sourceOf(page as (typeof PAGES)[number]),
        `${page} 已有 ArtButtonTable，豁免该删`
      ).not.toContain('ArtButtonTable')
    }
  })
})
