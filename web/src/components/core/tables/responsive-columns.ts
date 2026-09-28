/**
 * 表格列的多设备渲染策略
 *
 * ## 约定
 *
 * 列可声明 `hideBelow: <断点名>`，表示视口小于该断点时该列不参与渲染：
 *
 * ```ts
 * { prop: 'ip', label: 'IP', hideBelow: 'desktop' }   // < 1024px 隐藏
 * { prop: 'browser', label: '浏览器', hideBelow: 'tablet' } // < 768px 隐藏
 * ```
 *
 * 断点名取自 `src/config/breakpoints.ts`（单一事实源），因此列的隐藏阈值与
 * CSS 媒体查询、shell 断点始终一致。
 *
 * ## 为什么在渲染层过滤而不是列配置层
 *
 * 列的显隐（columnChecks）是**用户偏好**，需要跨会话持久；
 * 而断点隐藏是**当前视口的临时行为**，不应写入用户偏好——否则在手机上打开过的页面
 * 会把"隐藏某列"持久化下来，回到桌面也看不到该列。因此过滤只发生在渲染时。
 *
 * ## 列宽约定
 *
 * - **数据列一律用 `minWidth`**：表格有剩余宽度时按 minWidth 比例分摊（列宽均匀），
 *   空间不足时收缩到 minWidth 并出现横向滚动；
 * - **只有结构性列用固定 `width`**：`type: 'selection' | 'index' | 'expand'` 与操作列。
 *   它们的内容宽度恒定，参与分摊会出现"序号列被拉宽"这类视觉问题；
 * - 混用固定 width 与 minWidth 时只有 minWidth 列伸展、固定列原地不动，表格看起来
 *   疏密不均——这是历史遗留问题，已于本轮改造统一收敛为 minWidth。
 *
 * @module components/core/tables/responsive-columns
 */
import { BREAKPOINTS, type BreakpointName } from '@/config/breakpoints'
import type { ColumnOption } from '@/types/component'

/** 校验 hideBelow 是否为合法断点名（拼错时该列会静默不隐藏，故需要显式判断） */
export function isValidHideBelow(value: unknown): value is BreakpointName {
  // 用 hasOwn 而不是 `in`：`in` 会命中原型链（'toString' 之类会蒙混过关）
  return typeof value === 'string' && Object.hasOwn(BREAKPOINTS, value)
}

/**
 * 按视口过滤列：隐藏声明了 hideBelow 且当前视口低于该断点的列。
 *
 * @param columns 用户显隐过滤后的列
 * @param isBelow 断点判定函数（如 `(name) => smaller(name).value`）
 */
export function filterColumnsForViewport<T = any>(
  columns: ColumnOption<T>[],
  isBelow: (name: BreakpointName) => boolean
): ColumnOption<T>[] {
  return columns.filter((col) => !isValidHideBelow(col.hideBelow) || !isBelow(col.hideBelow))
}

/** 已告警过的列（按 列标识:hideBelow 去重，避免列变化时重复刷屏） */
const warnedHideBelow = new Set<string>()

/**
 * 开发期校验：hideBelow 拼错（如写成 'mobile'）会让该列永远显示，属于静默失效。
 * 这里显式告警并给出可用断点名。
 */
export function warnInvalidHideBelow<T = any>(columns: ColumnOption<T>[]): void {
  if (!import.meta.env.DEV) {
    return
  }

  for (const col of columns) {
    if (col.hideBelow === undefined || isValidHideBelow(col.hideBelow)) {
      continue
    }

    const columnName = col.prop ?? col.type ?? '(未命名)'
    const warnKey = `${String(columnName)}:${String(col.hideBelow)}`
    if (warnedHideBelow.has(warnKey)) {
      continue
    }
    warnedHideBelow.add(warnKey)

    console.warn(
      `[ArtTable] 列 ${String(columnName)} 的 hideBelow="${String(col.hideBelow)}" 不是合法断点名，` +
        `可用值：${Object.keys(BREAKPOINTS).join(' / ')}`
    )
  }
}

/** 是否声明为左固定 */
function isFixedLeft<T>(col: ColumnOption<T>): boolean {
  return col.fixed === 'left' || col.fixed === true
}

/**
 * 找出"左固定列不是渲染列表前缀"的违规列。
 *
 * ## 为什么必须校验
 *
 * Element Plus 在布局时会把所有 `fixed: 'left'` 列**整体提到最前**（见
 * element-plus table store `updateColumns`：`[...fixedColumns, ...notFixedColumns, ...rightFixed]`；
 * selection 列若在首位会自动跟随保留第一）。因此若固定列前面还有普通列，
 * 实际渲染顺序就会与声明顺序不一致——典型症状是"序号列本该第一，却排在第二"。
 *
 * 结论：左固定列必须是声明顺序里的**连续前缀**（允许首位的 selection 列自动并入）。
 * 需要固定的身份列若不在前缀里，应先把它（及其中间的列）排到前面再加 fixed。
 *
 * @returns 违反约束的列（空数组表示合法），供测试断言
 */
export function findFixedLeftViolations<T = any>(columns: ColumnOption<T>[]): ColumnOption<T>[] {
  const lastFixedIndex = columns.reduce((last, col, index) => (isFixedLeft(col) ? index : last), -1)
  if (lastFixedIndex <= 0) {
    return []
  }

  const violations: ColumnOption<T>[] = []
  for (let index = 0; index < lastFixedIndex; index++) {
    const col = columns[index]
    // selection 列位于首位时，EP 会自动把它并入固定组，保持第一
    if (isFixedLeft(col) || (index === 0 && col.type === 'selection')) {
      continue
    }
    violations.push(col)
  }

  return violations
}

/** 已告警过的违规列（按列标识去重，避免视图列变化时重复刷屏） */
const warnedFixedLeft = new Set<string>()

/**
 * 开发期校验：左固定列不是前缀时告警（否则表格列顺序会被 EP 悄悄重排）
 */
export function warnNonPrefixFixedLeft<T = any>(columns: ColumnOption<T>[]): void {
  if (!import.meta.env.DEV) {
    return
  }

  for (const col of findFixedLeftViolations(columns)) {
    const columnName = String(col.prop ?? col.type ?? '(未命名)')
    if (warnedFixedLeft.has(columnName)) {
      continue
    }
    warnedFixedLeft.add(columnName)

    console.warn(
      `[ArtTable] 左固定列不是渲染列表的前缀：列 ${columnName} 位于固定列之前，` +
        `Element Plus 会把固定列整体提前，导致表格列顺序与声明顺序不一致。` +
        `请把要固定的列（含其之前的列）排到最前再加 fixed: 'left'。`
    )
  }
}
