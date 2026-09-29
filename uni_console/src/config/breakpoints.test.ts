import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { BREAKPOINTS } from './breakpoints'

// TS 断点表（src/config/breakpoints.ts）与 SCSS 断点表
// （src/assets/styles/core/_breakpoints.scss）是同一份约定的两个载体。
//
// 本套断言的价值在于：任何一侧单独修改（例如只改了 TS 而忘记同步 SCSS，
// 或反之）都会在 CI 立刻失败，避免 JS 判断与 CSS 媒体查询再次错位——
// 这正是改造前的问题（500/800/1000/1024 等阈值散落在 JS 与 SCSS 中）。

const scssPath = fileURLToPath(new URL('../assets/styles/core/_breakpoints.scss', import.meta.url))
const tailwindPath = fileURLToPath(new URL('../assets/styles/core/tailwind.css', import.meta.url))

/** 解析 SCSS 中的 $breakpoints map（只取 '名称': 数值 形式的行） */
function parseScssBreakpoints(source: string): Record<string, number> {
  const mapStart = source.indexOf('$breakpoints:')
  expect(mapStart, '未找到 $breakpoints 定义').toBeGreaterThanOrEqual(0)

  const mapEnd = source.indexOf(');', mapStart)
  expect(mapEnd, '未找到 $breakpoints 结束括号').toBeGreaterThan(mapStart)

  const block = source.slice(mapStart, mapEnd)
  const result: Record<string, number> = {}

  for (const match of block.matchAll(/'([A-Za-z0-9_-]+)'\s*:\s*(\d+)/g)) {
    result[match[1]] = Number(match[2])
  }

  return result
}

/** 解析 tailwind.css @theme 中显式声明的 --breakpoint-*（kebab-case → px 数值） */
function parseTailwindBreakpoints(source: string): Record<string, number> {
  const result: Record<string, number> = {}

  for (const match of source.matchAll(/--breakpoint-([a-z0-9-]+)\s*:\s*(\d+)px/g)) {
    result[match[1]] = Number(match[2])
  }

  return result
}

describe('断点单一事实源（TS ↔ SCSS）', () => {
  const scssBreakpoints = parseScssBreakpoints(readFileSync(scssPath, 'utf8'))
  const tsBreakpoints: Record<string, number> = { ...BREAKPOINTS }

  it('TS 与 SCSS 的断点名称完全一致', () => {
    expect(Object.keys(scssBreakpoints).sort()).toEqual(Object.keys(tsBreakpoints).sort())
  })

  it('TS 与 SCSS 的断点数值逐一相等', () => {
    for (const [name, value] of Object.entries(tsBreakpoints)) {
      expect(scssBreakpoints[name], `断点 ${name} 在 SCSS 中缺失或数值不同`).toBe(value)
    }
  })

  it('断点按升序排列（VueUse useBreakpoints 要求从小到大的顺序）', () => {
    const values = Object.values(tsBreakpoints)
    expect(values).toEqual([...values].sort((a, b) => a - b))
  })

  it('关键断点数值锁定（防止无声漂移）', () => {
    // 这些值对齐 Tailwind 默认断点与项目既有阈值，被 shell / 侧栏 / 表单共同消费，
    // 改动会同时影响 CSS 媒体查询与 JS 判断，必须在评审中显式确认。
    expect(tsBreakpoints.phone).toBe(640)
    expect(tsBreakpoints.tablet).toBe(768)
    expect(tsBreakpoints.desktop).toBe(1024)
  })
})

// Tailwind @theme 的 --breakpoint-* 是断点表的第三个载体：模板里的
// `max-compact:` 等命名前缀由它生成，若与 TS/SCSS 漂移，会出现
// "JS 判定与 CSS 前缀不同步"。默认前缀（sm/md/lg/xl）与项目断点的
// 对应关系见下方映射，显式覆盖时也会被本套断言捕获。
describe('断点单一事实源（Tailwind @theme ↔ TS）', () => {
  const tailwindBreakpoints = parseTailwindBreakpoints(readFileSync(tailwindPath, 'utf8'))
  const tsBreakpoints: Record<string, number> = { ...BREAKPOINTS }

  // 项目特有断点：必须在 @theme 显式注册，供 max-* 命名前缀使用
  const projectSpecific: Record<string, keyof typeof BREAKPOINTS> = {
    'phone-narrow': 'phoneNarrow',
    compact: 'compact',
    wide: 'wide',
    xwide: 'xwide'
  }

  // 与 Tailwind 默认断点对齐的项目断点（默认值：sm=40rem / md=48rem / lg=64rem / xl=80rem）
  const defaultAligned: Record<string, keyof typeof BREAKPOINTS> = {
    sm: 'phone',
    md: 'tablet',
    lg: 'desktop',
    xl: 'xl'
  }

  it('项目特有断点已在 @theme 注册且数值与 TS 一致', () => {
    for (const [tailwindName, tsName] of Object.entries(projectSpecific)) {
      expect(
        tailwindBreakpoints[tailwindName],
        `@theme 缺少 --breakpoint-${tailwindName}（或未使用 px 数值）`
      ).toBe(tsBreakpoints[tsName])
    }
  })

  it('默认对齐断点若被 @theme 覆盖，数值必须与 TS 一致', () => {
    for (const [tailwindName, tsName] of Object.entries(defaultAligned)) {
      const declared = tailwindBreakpoints[tailwindName]

      // 未声明时使用 Tailwind 默认（640/768/1024/1280），与项目断点相符；
      // 一旦声明覆盖就必须与 TS 表一致
      if (declared !== undefined) {
        expect(
          declared,
          `@theme 覆盖的 --breakpoint-${tailwindName} 与 TS 断点 ${tsName} 不同`
        ).toBe(tsBreakpoints[tsName])
      } else {
        expect(
          tsBreakpoints[tsName],
          `Tailwind 默认 --breakpoint-${tailwindName} 与项目断点 ${tsName} 不再对齐，需在 @theme 显式注册`
        ).toBe({ sm: 640, md: 768, lg: 1024, xl: 1280 }[tailwindName])
      }
    }
  })

  it('@theme 不注册与项目断点表无关的自定义断点（避免两套语义并存）', () => {
    const known = new Set([...Object.keys(projectSpecific), ...Object.keys(defaultAligned)])
    const unknown = Object.keys(tailwindBreakpoints).filter((name) => !known.has(name))
    expect(unknown, `发现未在断点表登记的 @theme 断点：${unknown.join(', ')}`).toEqual([])
  })
})
