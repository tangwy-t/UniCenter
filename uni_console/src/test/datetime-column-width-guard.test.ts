/**
 * 守卫：列表页的**日期时间列**宽度不得低于可容纳单行的下限（否则折行）。
 *
 * ## 为什么是红线（2026-10-16 用户连续报了四次）
 * `2026-10-16 11:05` 这类值在列内会**在空格处折成两行**，行高从 23px 变 ~40px：
 * 表格节奏被打乱、同一张表里高低不一，看上去像渲染错误。当天实测的最小可容纳宽度
 * 是 **165px**（`system-job/views/logs.vue:388` 的 `startTime` 就是 165，是仓内既有
 * 最紧的一档）；130 / 140 / 150 三种实测都会折行（system-user 最后登录/创建时间、
 * system-role 创建时间、system-file 上传时间、system-log 登录/操作时间）。
 *
 * ## 判据为什么用 prop 而不是 label
 * `prop` 尾缀 `Time` / `At` / `Date` 是**结构化**判据，不会误伤同名字段的无关列：
 *   - `system-monitor/views/online.vue` 的「登录地址」prop 是 `loginLocation`
 *     （IP 串，140px 足够）—— 按 label 里的「登录」二字去判断就会误报；
 *   - 反过来 `system-user` 的「最后登录」label 里没有「时间/日期」二字，
 *     按 label 判断会漏报（而这正是用户报的那一列）。
 *
 * ## 排除表（须给"不可区分性证明"，不是随手放行）
 * `costTime`（耗时）：值是**毫秒数**（如 `12ms`），没有日期部分、永远不会折行，
 * 与"时刻"不可混同 —— 故显式排除，而不是把阈值放宽到它也能过。
 *
 * ## 破线姿势（本守卫必须真能红）
 * 把任一时间列的 `minWidth` 改到 165 以下即红；同时下方钉了两条**自检断言**
 * （匹配数量下限 + 已知列在列），防止扫描器匹配不到任何东西时静默变绿。
 */
import { readdirSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

/** 可容纳 `YYYY-MM-DD HH:mm` 单行的下限（锚定 logs.vue 的 startTime=165）。 */
const MIN_DATETIME_MIN_WIDTH = 165

/** prop 尾缀即"时刻/日期"语义；`costTime` 是耗时（毫秒数）—— 见文件头"排除表"。 */
const EXCLUDED_PROPS = new Set(['costTime'])

const MODULES_DIR = fileURLToPath(new URL('../modules', import.meta.url))

/** 手写递归（不依赖 Dirent 的 parentPath：该字段在部分 node 组合下缺失）。 */
function listViewFiles(): string[] {
  const out: string[] = []
  const walk = (dir: string): void => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const full = `${dir}/${entry.name}`
      if (entry.isDirectory()) {
        walk(full)
        continue
      }
      if (!entry.isFile() || !entry.name.endsWith('.vue')) continue
      // 只收 views 目录下的页面（列表页都在 views 下；组件目录里没有列定义）
      if (!dir.includes('/views')) continue
      out.push(full)
    }
  }
  walk(MODULES_DIR)
  return out.sort()
}

interface ColumnDef {
  file: string
  line: number
  prop: string
  label: string
  minWidth: number
}

/**
 * 取"同一列定义块内"的 prop/label/minWidth：限定在 400 字符窗口内，避免跨列误配。
 * 这是源码级扫描的通用取舍 —— 表格列都是对象字面量、字段顺序固定为
 * prop → label → minWidth（见各模块 views 下的列定义区）。
 */
function collectDatetimeColumns(files: string[]): ColumnDef[] {
  const found: ColumnDef[] = []
  const blockRe = /\{\s*prop:\s*'([A-Za-z0-9_]+)'([\s\S]{0,400}?)minWidth:\s*(\d+)/g
  for (const file of files) {
    const src = readFileSync(file, 'utf-8')
    for (const m of src.matchAll(blockRe)) {
      const [, prop, body, width] = m
      if (!/(Time|At|Date)$/.test(prop)) continue
      if (EXCLUDED_PROPS.has(prop)) continue
      const label = /label:\s*'([^']*)'/.exec(body)?.[1] ?? ''
      found.push({
        file: file.slice(file.indexOf('/uni_console/') + 1),
        line: src.slice(0, m.index).split('\n').length,
        prop,
        label,
        minWidth: Number(width)
      })
    }
  }
  return found
}

describe('列表页日期时间列宽度守卫', () => {
  const columns = collectDatetimeColumns(listViewFiles())

  it('扫描器自检：能扫到仓内已知的日期时间列（防止静默匹配不到）', () => {
    // 数量下限：2026-10 实测为 16 个（含两个被排除的 costTime 即 14 个）
    expect(columns.length).toBeGreaterThanOrEqual(12)
    // 已知列在列（用户报过的那几列必须在扫描结果里）
    const key = columns.map((c) => `${c.file.split('/').slice(-3).join('/')}#${c.prop}`)
    for (const needle of [
      'system-user/views/index.vue#lastLoginTime',
      'system-user/views/index.vue#createdAt',
      'system-role/views/index.vue#createdAt',
      'system-file/views/index.vue#createdAt',
      'system-log/views/login.vue#loginTime',
      'system-log/views/operation.vue#operTime'
    ]) {
      expect(key.some((k) => k.endsWith(needle)), `扫描器未覆盖已知日期列 ${needle}`).toBe(true)
    }
  })

  it(`每个日期时间列的 minWidth 都 ≥ ${MIN_DATETIME_MIN_WIDTH}px（否则会折成两行）`, () => {
    const offenders = columns.filter((c) => c.minWidth < MIN_DATETIME_MIN_WIDTH)
    const detail = offenders
      .map((c) => `  ${c.file}:${c.line} ${c.label}(${c.prop}) minWidth=${c.minWidth}`)
      .join('\n')
    expect(
      offenders.length,
      `以下日期时间列 minWidth 低于 ${MIN_DATETIME_MIN_WIDTH}，会折行：\n${detail}\n` +
        `修法：提到 ${MIN_DATETIME_MIN_WIDTH} 的档位（锚定 system-job/views/logs.vue:388 的 startTime）；` +
        `确属"非时刻"语义（如耗时）请加入本守卫的 EXCLUDED_PROPS 并写明理由。`
    ).toBe(0)
  })
})
