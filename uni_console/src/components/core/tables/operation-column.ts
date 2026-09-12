/**
 * 表格「操作」列构建工具
 *
 * 统一处理操作列的两个诉求:
 * 1. 列宽随按钮数量自动计算，避免硬编码宽度导致的空白或挤压;
 * 2. 没有任何可用的操作按钮时直接不渲染操作列。
 *
 * 用法:
 * ```ts
 * operationColumn({
 *   count: (hasAuth('system:user:edit') ? 1 : 0) + (hasAuth('system:user:delete') ? 1 : 0),
 *   formatter: (row) => h('div', { class: 'flex items-center' }, [/* ... *\/])
 * })
 * ```
 *
 * @module components/core/tables/operation-column
 */
import type { ColumnOption } from '@/types/component'

/** 单个操作按钮占用的固定宽度(图标按钮 + 右侧间距) */
export const OPERATION_BUTTON_WIDTH = 44

/** 操作列内容两侧内边距合计 */
export const OPERATION_COLUMN_PADDING = 16

/** 操作列最小宽度(仅剩一个按钮或纯标签时的兜底宽度) */
export const OPERATION_COLUMN_MIN_WIDTH = 80

export interface OperationColumnOptions<T = any> {
  /** 行操作渲染函数 */
  formatter: (row: T) => any
  /** 可显示的操作按钮数量(0 表示隐藏操作列) */
  count: number
  /** 列标题，默认「操作」 */
  label?: string
  /** 是否固定在右侧，默认 right */
  fixed?: boolean | 'right' | 'left'
  /** 最小宽度，默认 OPERATION_COLUMN_MIN_WIDTH */
  minWidth?: number
}

/**
 * 构建「操作」列:
 * - 按按钮数量自动计算列宽;
 * - 按钮数量为 0 时返回 null(调用方过滤后即不渲染操作列)。
 */
export function operationColumn<T = any>(
  options: OperationColumnOptions<T>
): ColumnOption<T> | null {
  const {
    formatter,
    count,
    label = '操作',
    fixed = 'right',
    minWidth = OPERATION_COLUMN_MIN_WIDTH
  } = options

  if (count <= 0) return null

  return {
    prop: 'operation',
    label,
    fixed,
    width: Math.max(minWidth, count * OPERATION_BUTTON_WIDTH + OPERATION_COLUMN_PADDING),
    formatter
  }
}
