import { readdirSync, readFileSync } from 'node:fs'
import { basename, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { BREAKPOINTS, HEIGHT_BREAKPOINTS } from './breakpoints'

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

/**
 * 解析 SCSS 中的 $height-breakpoints map（高度轴，与宽度轴同款解析）。
 * 注意 '$breakpoints:' 不是 '$height-breakpoints:' 的子串，两张表互不误抓。
 */
function parseScssHeightBreakpoints(source: string): Record<string, number> {
  const mapStart = source.indexOf('$height-breakpoints:')
  expect(mapStart, '未找到 $height-breakpoints 定义').toBeGreaterThanOrEqual(0)

  const mapEnd = source.indexOf(');', mapStart)
  expect(mapEnd, '未找到 $height-breakpoints 结束括号').toBeGreaterThan(mapStart)

  const block = source.slice(mapStart, mapEnd)
  const result: Record<string, number> = {}

  for (const match of block.matchAll(/'([A-Za-z0-9_-]+)'\s*:\s*(\d+)/g)) {
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

// 高度轴（矮视口/手机横屏）与宽度轴分开成表：这里是它的 TS ↔ SCSS 一致性断言，
// 外加「阈值自校准」——校准口径写死在用例里（390/360 档必中、620 视需要、900 不中），
// 任何一侧无声漂移都会在这里失败（例如有人把 short 调到 960，900 高的常规桌面
// 会平白变形）。
describe('高度轴断点单一事实源（TS ↔ SCSS）', () => {
  const scssHeightBreakpoints = parseScssHeightBreakpoints(readFileSync(scssPath, 'utf8'))
  const tsHeightBreakpoints: Record<string, number> = { ...HEIGHT_BREAKPOINTS }

  it('TS 与 SCSS 的高度断点名称完全一致', () => {
    expect(Object.keys(scssHeightBreakpoints).sort()).toEqual(
      Object.keys(tsHeightBreakpoints).sort()
    )
  })

  it('TS 与 SCSS 的高度断点数值逐一相等', () => {
    for (const [name, value] of Object.entries(tsHeightBreakpoints)) {
      expect(scssHeightBreakpoints[name], `高度断点 ${name} 在 SCSS 中缺失或数值不同`).toBe(value)
    }
  })

  it('高度断点按升序排列（VueUse/useMediaQuery 的档位语义要求从小到大）', () => {
    const values = Object.values(tsHeightBreakpoints)
    expect(values).toEqual([...values].sort((a, b) => a - b))
  })

  it('阈值自校准：390/360 必中 phoneShort、620 中 short、900 两个都不中', () => {
    const atMost = (name: keyof typeof HEIGHT_BREAKPOINTS, height: number) =>
      height <= HEIGHT_BREAKPOINTS[name]

    // 手机横屏实测档（本批的验收尺寸）
    expect(atMost('phoneShort', 390), '844×390 必须落在 phoneShort 档').toBe(true)
    expect(atMost('phoneShort', 360), '740×360 必须落在 phoneShort 档').toBe(true)
    expect(atMost('short', 620), '1280×620 必须落在 short 档').toBe(true)
    // 常规桌面：两档都不中，桌面零回归
    expect(atMost('short', 900), '900 高的常规桌面不得落入 short 档').toBe(false)
    expect(atMost('phoneShort', 900), '900 高的常规桌面不得落入 phoneShort 档').toBe(false)
  })

  it('关键高度数值锁定（防无声漂移）', () => {
    expect(tsHeightBreakpoints.phoneShort).toBe(420)
    expect(tsHeightBreakpoints.short).toBe(640)
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

// 高度轴在 Tailwind 侧只走 @custom-variant（v4 的 --breakpoint-* 只表达宽度，
// 高度阈值塞进去会污染上面那张「@theme 无未知断点」的表）。因此这里单独守：
// tailwind.css 里每个高度 @custom-variant 的阈值都必须是 HEIGHT_BREAKPOINTS
// 里的值 —— 既堵住「模板类与 JS/CSS 判定漂移」，也堵住「随手写一个新数字」。
describe('高度轴断点第四载体（Tailwind @custom-variant ↔ TS）', () => {
  const VARIANT = /@custom-variant\s+([a-z0-9-]+)\s+\(@media\s*\(max-height:\s*(\d+)px\)\)/g

  it('高度 @custom-variant 的阈值必须取自 HEIGHT_BREAKPOINTS', () => {
    const source = readFileSync(tailwindPath, 'utf8')
    const declared = [...source.matchAll(VARIANT)]

    expect(declared.length, 'tailwind.css 未注册任何高度轴 @custom-variant').toBeGreaterThan(0)

    const known = new Set(Object.values(HEIGHT_BREAKPOINTS) as number[])
    for (const [, name, value] of declared) {
      expect(
        known.has(Number(value)),
        `@custom-variant ${name} 的阈值 ${value}px 不在高度断点表内`
      ).toBe(true)
    }
  })
})

// 断点的第四个载体：组件/工具里内联写死的阈值（matchMedia 字符串、组件内 @media）。
// 前三个载体的一致性断言覆盖不到它，任何一处断点调整都会与它无声错位
// （竖屏守卫组件曾把 tablet 写成 767.98px 字面量）。
// 这里扫描源码，强制**两轴**判定都只走 respond-*/respond-height-* mixin 或
// useAppBreakpoints（宽度与高度同一纪律：内联的字面量阈值会在调表时无声漂移）。
describe('断点单一事实源（源码无内联阈值）', () => {
  const srcRoot = fileURLToPath(new URL('..', import.meta.url))

  // 只匹配"媒体查询形态"的阈值：`(max-width: 768px)` / `(width <= 768px)` /
  // `(max-height: 640px)` / `(height <= 390px)`。
  // 普通 CSS 属性（`max-width: 220px;`、`max-height="380px"`）不以 `(` 开头，不会误伤；
  // mixin 生成的查询用 `#{...}px` 插值，也不会被 `[\d.]+px` 命中。
  const INLINE_BREAKPOINT =
    /\((?:max|min)-(?:width|height):\s*[\d.]+px|\((?:width|height)\s*[<>]=?\s*[\d.]+px/

  /** 滤掉注释行与块注释，避免文档示例（如 mixin 用法说明）被当成真实阈值 */
  function stripComments(source: string): string {
    return source
      .split('\n')
      .filter((line) => {
        const trimmed = line.trim()
        return !trimmed.startsWith('//') && !trimmed.startsWith('*') && !trimmed.startsWith('/*')
      })
      .join('\n')
      .replace(/\/\*[\s\S]*?\*\//g, '')
  }

  /**
   * 滤掉 Tailwind 的高度轴 @custom-variant 声明（合法的自定义变体载体，
   * 形如 `@custom-variant max-short (@media (max-height: 640px));`）。
   * 豁免不是放行：这些声明由上面「Tailwind @custom-variant ↔ TS」断言把关
   * （阈值必须取自 HEIGHT_BREAKPOINTS），这里只避免它们被当成内联阈值。
   */
  function stripHeightCustomVariants(source: string): string {
    return source.replace(/@custom-variant[^\n]*\(max-height[^\n]*\n?/g, '')
  }

  it('源码中不存在内联的宽/高媒体查询阈值', () => {
    const offenders: string[] = []

    for (const relative of readdirSync(srcRoot, { recursive: true }) as string[]) {
      if (!/\.(ts|vue|scss|css)$/.test(relative)) continue
      // 本测试自身与自动生成的类型文件不在扫描范围
      if (basename(relative).endsWith('.test.ts')) continue
      if (relative.includes('types/import')) continue

      const source = stripHeightCustomVariants(
        stripComments(readFileSync(join(srcRoot, relative), 'utf8'))
      )
      if (INLINE_BREAKPOINT.test(source)) offenders.push(relative)
    }

    expect(
      offenders,
      `以下文件内联了宽/高阈值，应改用 respond-* / respond-height-* mixin 或 useAppBreakpoints：\n${offenders.join('\n')}`
    ).toEqual([])
  })
})
