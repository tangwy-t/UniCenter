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
