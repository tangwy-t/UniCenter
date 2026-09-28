import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { BREAKPOINTS } from '@/config/breakpoints'
import {
  findFixedLeftViolations,
  filterColumnsForViewport,
  isValidHideBelow
} from './responsive-columns'
import type { ColumnOption } from '@/types/component'

// 列的多设备渲染策略测试。
//
// 三个层面：
// 1. 纯函数行为：hideBelow 列的过滤语义（含拼错断点名时不得隐藏）
// 2. 纯函数行为：左固定列必须是渲染列表前缀（EP 会把固定列整体提前）
// 3. 仓库级扫描：所有页面声明的 hideBelow 必须是合法断点名——
//    拼错（如写成 'mobile'）会让该列静默永远显示，是最容易漏掉的一类问题。

const SRC_ROOT = fileURLToPath(new URL('../../../', import.meta.url))

/** 递归收集 src 下的 .vue / .ts 源码文件 */
function collectSourceFiles(dir: string, acc: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    if (entry === 'node_modules' || entry.startsWith('.')) {
      continue
    }

    const fullPath = join(dir, entry)
    if (statSync(fullPath).isDirectory()) {
      collectSourceFiles(fullPath, acc)
    } else if (/\.(vue|ts)$/.test(entry) && !entry.endsWith('.test.ts')) {
      acc.push(fullPath)
    }
  }

  return acc
}

describe('filterColumnsForViewport', () => {
  const baseColumns: ColumnOption[] = [
    { prop: 'username', label: '账号' },
    { prop: 'ip', label: 'IP', hideBelow: 'desktop' },
    { prop: 'browser', label: '浏览器', hideBelow: 'tablet' }
  ]

  it('未声明 hideBelow 的列始终保留', () => {
    const kept = filterColumnsForViewport(baseColumns, (name) => name === 'xwide')
    expect(kept.map((col) => col.prop)).toContain('username')
  })

  it('手机视口：低于 tablet 的列全部移除', () => {
    // 手机（<768）同时低于 tablet 与 desktop
    const kept = filterColumnsForViewport(
      baseColumns,
      (name) => name === 'tablet' || name === 'desktop'
    )
    expect(kept.map((col) => col.prop)).toEqual(['username'])
  })

  it('平板视口：只移除 desktop 级列，保留 tablet 级列', () => {
    // 平板（768-1023）低于 desktop 但不低于 tablet
    const kept = filterColumnsForViewport(baseColumns, (name) => name === 'desktop')
    expect(kept.map((col) => col.prop)).toEqual(['username', 'browser'])
  })

  it('桌面视口：恢复全部列', () => {
    const kept = filterColumnsForViewport(baseColumns, () => false)
    expect(kept.map((col) => col.prop)).toEqual(['username', 'ip', 'browser'])
  })

  it('断点名拼错时按"保留"处理（不得让列凭空消失）', () => {
    const columns: ColumnOption[] = [
      // @ts-expect-error 故意传入非法断点名，验证容错行为
      { prop: 'nickname', label: '昵称', hideBelow: 'mobile' }
    ]
    const kept = filterColumnsForViewport(columns, () => true)
    expect(kept.map((col) => col.prop)).toEqual(['nickname'])
  })

  it('不修改传入的列配置（渲染过滤不得污染用户列偏好）', () => {
    const columns: ColumnOption[] = [{ prop: 'ip', label: 'IP', hideBelow: 'desktop' }]
    filterColumnsForViewport(columns, () => true)
    expect(columns[0].hideBelow).toBe('desktop')
    expect(columns).toHaveLength(1)
  })
})

describe('findFixedLeftViolations（左固定列必须是渲染列表前缀）', () => {
  // 背景：Element Plus 布局时会把所有 fixed:'left' 列整体提到最前
  // （见 element-plus table store updateColumns），因此固定列之前若还有普通列，
  // 实际列顺序会与声明顺序不一致——"序号列被挤到第二位"就是典型症状。

  it('没有固定列时不报违规', () => {
    const columns: ColumnOption[] = [{ prop: 'a' }, { prop: 'b' }]
    expect(findFixedLeftViolations(columns)).toEqual([])
  })

  it('固定列构成前缀时不报违规', () => {
    const columns: ColumnOption[] = [
      { prop: 'a', fixed: 'left' },
      { prop: 'b', fixed: 'left' },
      { prop: 'c' }
    ]
    expect(findFixedLeftViolations(columns)).toEqual([])
  })

  it('首位 selection 列自动并入固定组，不算违规', () => {
    const columns: ColumnOption[] = [
      { type: 'selection' },
      { prop: 'name', fixed: 'left' },
      { prop: 'status' }
    ]
    expect(findFixedLeftViolations(columns)).toEqual([])
  })

  it('固定列之前存在普通列时返回这些列', () => {
    const columns: ColumnOption[] = [
      { prop: 'index' },
      { prop: 'username', fixed: 'left' },
      { prop: 'ip' }
    ]
    expect(findFixedLeftViolations(columns).map((col) => col.prop)).toEqual(['index'])
  })

  it('selection 之后、固定列之前的普通列同样算违规', () => {
    const columns: ColumnOption[] = [
      { type: 'selection' },
      { prop: 'avatar' },
      { prop: 'username', fixed: 'left' }
    ]
    expect(findFixedLeftViolations(columns).map((col) => col.prop)).toEqual(['avatar'])
  })

  it('fixed: true 等价于左固定', () => {
    const columns: ColumnOption[] = [{ prop: 'a' }, { prop: 'b', fixed: true }]
    expect(findFixedLeftViolations(columns).map((col) => col.prop)).toEqual(['a'])
  })

  it('右固定列不受该约束（它会被 EP 提到最后）', () => {
    const columns: ColumnOption[] = [{ prop: 'a' }, { prop: 'b', fixed: 'right' }]
    expect(findFixedLeftViolations(columns)).toEqual([])
  })
})

describe('isValidHideBelow', () => {
  it('合法断点名返回 true', () => {
    expect(isValidHideBelow('tablet')).toBe(true)
    expect(isValidHideBelow('phone')).toBe(true)
    expect(isValidHideBelow('desktop')).toBe(true)
  })

  it('非法值返回 false', () => {
    expect(isValidHideBelow('mobile')).toBe(false)
    expect(isValidHideBelow('')).toBe(false)
    expect(isValidHideBelow(768)).toBe(false)
    expect(isValidHideBelow(undefined)).toBe(false)
    expect(isValidHideBelow('toString')).toBe(false) // 原型链上的属性不算合法断点
  })
})

describe('仓库内 hideBelow 声明校验', () => {
  const files = collectSourceFiles(SRC_ROOT)
  const declarations: Array<{ file: string; name: string }> = []

  for (const file of files) {
    const source = readFileSync(file, 'utf8')
    for (const match of source.matchAll(/hideBelow:\s*['"]([^'"]+)['"]/g)) {
      declarations.push({ file: file.slice(SRC_ROOT.length), name: match[1] })
    }
  }

  it('扫描到 hideBelow 声明（防止正则失效导致本用例空转）', () => {
    expect(declarations.length).toBeGreaterThan(0)
  })

  it('所有 hideBelow 都是合法断点名', () => {
    const validNames = Object.keys(BREAKPOINTS)
    for (const { file, name } of declarations) {
      expect(validNames, `${file} 中的 hideBelow="${name}" 不是合法断点名`).toContain(name)
    }
  })

  it('已采用列优先级的示例页仍保留声明（策略未被移除）', () => {
    const adoptedPages = [
      'modules/system-user/views/index.vue',
      'modules/system-log/views/login.vue',
      'modules/system-log/views/operation.vue',
      'modules/system-role/views/index.vue'
    ]

    for (const page of adoptedPages) {
      const declared = declarations.filter((item) => item.file.endsWith(page))
      expect(declared.length, `${page} 未声明任何 hideBelow`).toBeGreaterThan(0)
    }
  })
})
