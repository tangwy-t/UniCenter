// @vitest-environment jsdom
/**
 * 「布局与滚动链」修复的回归守卫（本波 P0 三件 + 附带项）。
 *
 * ## 为什么一半是源码断言
 *
 * 本波修的缺陷全是**布局链**问题（`--art-full-height` 的占高口径、卡体只裁不滚、
 * 表根与分页条不在同一个盒子里），jsdom 没有布局引擎：`getBoundingClientRect` 恒
 * 返回 0、`scrollWidth/scrollHeight` 恒为 0，真渲染断言**测不到**这些不变量（会
 * 绿得毫无意义）。真值验证走 playwright 探针（见 .tmp-ux-20261010/fix-layout/），
 * 这里钉住的是「让探针变绿的**结构**不改回去」——正是这些结构在上一轮无声回归过。
 *
 * 例外是 `useLayoutHeight`（占高口径的唯一实现）：它可以被单测精确驱动（把上方
 * 各块的高度桩进去，断言算出来的 CSS 变量），所以那部分是真行为断言。
 *
 * 覆盖的缺陷：
 *   P0-1 任务中心分页器在矮视口不可达 → .tk-list 自持滚动 + .tk-page 常驻卡底；
 *   P0-2 容器详情 844×390 整卡塌陷   → 页根 art-full-height--flow + 自然流豁免；
 *   P0-3 六个列表页 844×390 分页不可达 → 表根 flex 列 + 分页条 flex:none；
 *   附带 终端面板吃满剩余高度        → .pty-terminal flex:1。
 */
import { afterEach, describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { defineComponent, h } from 'vue'
import { mount } from '@vue/test-utils'

const read = (rel: string) => readFileSync(fileURLToPath(new URL(rel, import.meta.url)), 'utf8')

const APP_SCSS = read('../../../assets/styles/core/app.scss')
const ART_TABLE = read('../../../components/core/tables/art-table/index.vue')
const TASKS = read('../views/tasks.vue')
const CONTAINER_DETAIL = read('../views/container-detail/index.vue')
const PTY = read('../components/pty-terminal.vue')

/** 去掉行注释与块注释：文档里引用旧写法（如 `flex: 1`）不该被当成真实声明。 */
function stripComments(source: string): string {
  return source
    .split('\n')
    .filter((line) => {
      const t = line.trim()
      return !t.startsWith('//') && !t.startsWith('*') && !t.startsWith('/*')
    })
    .join('\n')
    .replace(/\/\*[\s\S]*?\*\//g, '')
}

/** 取一段 SCSS 块（从 `from` 起、花括号配平到结束）。 */
function block(source: string, from: string): string {
  const start = source.indexOf(from)
  expect(start, `未找到块：${from}`).toBeGreaterThanOrEqual(0)
  let depth = 0
  for (let i = start; i < source.length; i++) {
    if (source[i] === '{') depth++
    else if (source[i] === '}') {
      depth--
      if (depth === 0) return source.slice(start, i + 1)
    }
  }
  throw new Error(`块未闭合：${from}`)
}

const APP_SCSS_CODE = stripComments(APP_SCSS)

describe('P0-3 · 表根与分页条在同一个盒子里（844×390 分页可达的结构前提）', () => {
  it('表根是 flex 列：表体拿剩余高度并自滚，分页条 flex:none 常驻表底', () => {
    const artTableRule = stripComments(block(ART_TABLE, '.art-table {'))
    expect(artTableRule, '表根必须是 flex 列（否则分页条会落到 .art-table 盒子之外）').toContain(
      'display: flex'
    )
    expect(artTableRule).toMatch(/flex-direction:\s*column/)
    // 表体：flex 1 1 auto + min-height:0（能缩、能自滚）
    expect(artTableRule).toMatch(/\.el-table\s*\{[^}]*flex:\s*1 1 auto/)
    expect(artTableRule).toMatch(/\.el-table\s*\{[^}]*min-height:\s*0/)
    // 分页条：不参与高度分配（常驻表底）
    expect(artTableRule).toMatch(/\.pagination\s*\{[^}]*flex:\s*none/)
  })

  it('卡体是 flex 列（分派容器），不再是「height:100% + 只裁不滚」', () => {
    const card = block(APP_SCSS_CODE, '\n.art-table-card {')
    const body = block(card, '.el-card__body {')
    expect(body).toContain('display: flex')
    expect(body).toMatch(/flex-direction:\s*column/)
    expect(body).toMatch(/overflow:\s*hidden/)
  })

  it('卡片高度语义 = 内容不足随内容收缩（长表由链接管）', () => {
    const card = block(APP_SCSS_CODE, '\n.art-table-card {')
    expect(card, '短表不再无条件撑满整屏').toMatch(/flex:\s*0 1 auto/)
    // 单滚动链仍在（短视口/全屏态把卡片拉满）——链里必须是 basis:0 的填充档。
    expect(APP_SCSS_CODE).toMatch(/\.art-table-card\s*\{\s*flex:\s*1 1 0;/)
  })

  it('@mixin art-single-scroll-chain 仍然定义且被两个入口消费', () => {
    expect(APP_SCSS_CODE).toContain('@mixin art-single-scroll-chain')
    expect(APP_SCSS_CODE).toMatch(/@include respond-height-at-most\('short'\)\s*\{/)
    expect(APP_SCSS_CODE).toMatch(/\.el-full-screen\s*\{\s*@include art-single-scroll-chain/)
  })
})

describe('P0-1 · 任务中心清单自持滚动（矮视口分页器可达）', () => {
  it('.tk-list 是滚动目标：flex + min-height:0 + overflow auto', () => {
    const list = block(stripComments(TASKS), '.tk-list {')
    expect(list).toMatch(/flex:\s*1 1 auto/)
    expect(list).toMatch(/min-height:\s*0/)
    expect(list).toMatch(/overflow:\s*auto/)
  })

  it('.tk-page 常驻卡底（flex:none）；卡体被收成 flex 列', () => {
    const code = stripComments(TASKS)
    expect(block(code, '.tk-page {')).toMatch(/flex:\s*none/)
    const deep = block(code, ':deep(.el-card__body) {')
    expect(deep).toContain('display: flex')
    expect(deep).toMatch(/flex-direction:\s*column/)
  })
})

describe('P0-2 · 容器详情在 844×390 回落自然流（不再整卡塌陷）', () => {
  it('页根挂 art-full-height--flow 标记类', () => {
    expect(CONTAINER_DETAIL).toContain('art-full-height art-full-height--flow')
  })

  it('app.scss 对 --flow 页在 phoneShort 档取消钉死高度，并撤掉整条 basis:0 链', () => {
    const rule = block(APP_SCSS_CODE, ".art-full-height.art-full-height--flow {")
    expect(rule, '自然流形态').toMatch(/height:\s*auto/)
    expect(rule, '撤掉链上会塌成 0 的 flex: 1 1 0').toContain('.art-table-card')
    expect(rule).toMatch(/flex:\s*0 1 auto/)
  })

  it('phoneShort 档卡片有「表头 + 一行 + 分页」的最小高度（chrome 比页根高时由页滚动兜底）', () => {
    expect(APP_SCSS_CODE).toMatch(/\.art-full-height \.art-table-card\s*\{\s*min-height:\s*\d+px/)
  })
})

describe('附带项 · 终端面板吃满剩余高度', () => {
  it('.pty-terminal 参与 flex 分配（不再是写死的 420px 面板）', () => {
    const rule = block(stripComments(PTY), '.pty-terminal {')
    expect(rule).toMatch(/flex:\s*1 1 auto/)
    expect(rule).toMatch(/flex-direction:\s*column/)
  })
})

// ───────────────────────────────────────────────────────────────────────────
// 占高口径：useLayoutHeight（本波把 `100dvh − 头部高度和` 换成「页根 offsetTop 实测」）
// ───────────────────────────────────────────────────────────────────────────

/** 建一份与真实布局同形的 DOM（#app-main → #app-content → 告警条 + .layout-content）。 */
function buildLayoutDom(rootTop: number, scrollTop = 0) {
  document.body.innerHTML = `
    <main id="app-main">
      <div id="app-header"></div>
      <div id="app-content">
        <div class="password-reminder"></div>
        <div class="layout-content">
          <div id="app-content-header"></div>
          <div class="art-page-view art-full-height"></div>
        </div>
      </div>
    </main>`

  const scroller = document.getElementById('app-main') as HTMLElement
  Object.defineProperty(scroller, 'scrollTop', { value: scrollTop, writable: true, configurable: true })
  scroller.getBoundingClientRect = () => ({ top: 0 }) as DOMRect

  const contentRoot = document.querySelector('.layout-content') as HTMLElement
  contentRoot.getBoundingClientRect = () => ({ top: rootTop }) as DOMRect
}

/** 以给定视口高度挂载一个只调用 useAutoLayoutHeight 的探针组件。 */
async function mountProbe() {
  const { useAutoLayoutHeight } = await import('@/hooks/core/useLayoutHeight')
  const Probe = defineComponent({
    name: 'LayoutHeightProbe',
    setup() {
      useAutoLayoutHeight()
      return () => h('div')
    }
  })
  return mount(Probe)
}

describe('--art-full-height 口径 · 实测值', () => {
  const originalRaf = globalThis.requestAnimationFrame
  const originalInnerHeight = window.innerHeight

  function setViewportHeight(px: number) {
    Object.defineProperty(window, 'innerHeight', { value: px, writable: true, configurable: true })
  }

  function syncRaf() {
    globalThis.requestAnimationFrame = ((cb: FrameRequestCallback) => {
      cb(0)
      return 0
    }) as typeof requestAnimationFrame
  }

  const cleanup: Array<() => void> = []

  afterEach(() => {
    for (const fn of cleanup.splice(0)) fn()
    globalThis.requestAnimationFrame = originalRaf
    setViewportHeight(originalInnerHeight)
    document.documentElement.style.removeProperty('--art-full-height')
    document.body.innerHTML = ''
  })

  it('常规视口：calc(100dvh − (196 + 15))（15px 是底部呼吸位）', async () => {
    syncRaf()
    setViewportHeight(900)
    buildLayoutDom(196)
    const w = await mountProbe()
    cleanup.push(() => w.unmount())

    expect(document.documentElement.style.getPropertyValue('--art-full-height')).toBe(
      'calc(100dvh - 211px)'
    )
  })

  it('phoneShort 档（≤420 高）：呼吸位归零，扣掉的就是实测占高', async () => {
    syncRaf()
    setViewportHeight(390)
    buildLayoutDom(140)
    const w = await mountProbe()
    cleanup.push(() => w.unmount())

    expect(document.documentElement.style.getPropertyValue('--art-full-height')).toBe(
      'calc(100dvh - 140px)'
    )
  })

  it('页根上方多一块告警条（+80）：扣掉的占高跟着长，不是写死的常数', async () => {
    syncRaf()
    setViewportHeight(900)
    buildLayoutDom(276) // 196 + 80：告警条换行到两行后的形态
    const w = await mountProbe()
    cleanup.push(() => w.unmount())

    expect(document.documentElement.style.getPropertyValue('--art-full-height')).toBe(
      'calc(100dvh - 291px)'
    )
  })

  it('滚动位移不参与：滚动容器滚了 120px，扣掉的仍是静态占高', async () => {
    syncRaf()
    setViewportHeight(900)
    // 页根 rect.top 因滚动变成 76，但 scrollTop 是 120 → 静态距离仍是 196。
    buildLayoutDom(76, 120)
    const w = await mountProbe()
    cleanup.push(() => w.unmount())

    expect(document.documentElement.style.getPropertyValue('--art-full-height')).toBe(
      'calc(100dvh - 211px)'
    )
  })

  it('不再写死 phoneShort 的「100dvh − 48px」（旧口径把告警条的 80px 漏在外面）', () => {
    expect(APP_SCSS_CODE).not.toContain('calc(100dvh - 48px)')
    expect(stripComments(read('../../../hooks/core/useLayoutHeight.ts'))).not.toMatch(
      /calc\(100dvh - \$\{?[a-zA-Z.]+ \+ 48/
    )
  })
})