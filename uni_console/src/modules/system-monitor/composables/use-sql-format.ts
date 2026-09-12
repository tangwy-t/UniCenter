/**
 * SQL 监控视图的纯格式化 / 阈值配色工具。
 *
 * 从 views/sql.vue(1790+ 行)抽出:这些函数不依赖任何响应式状态,
 * 是"输入 → 输出"的纯逻辑。抽出来后可脱离组件单测,视图只留编排。
 *
 * 配色函数返回的是**字面色值**而非 CSS 变量:视图侧需要把它们
 * 拼进 style(如 `${color}14` 做浅底),CSS 变量无法参与字符串拼接。
 */

/** 未知 / 不适用时的中性灰(与视图中性色保持一致)。 */
const NEUTRAL = '#94a3b8'

/** 数值千分位;非有限数返回占位符。 */
export function fmtCount(v: number | null | undefined): string {
  return typeof v === 'number' && Number.isFinite(v) ? v.toLocaleString() : '-'
}

/**
 * 百分比文本(保留 2 位)。分母非法或 ≤0 时返回占位符,
 * 避免出现 Infinity% / NaN%。
 */
export function fmtPct(numerator: number, denominator: number): string {
  if (!Number.isFinite(numerator) || !Number.isFinite(denominator) || denominator <= 0) return '-'
  return `${((numerator / denominator) * 100).toFixed(2)}%`
}

/** 把数值夹到 [0,100];非有限数归 0(供进度条宽度使用)。 */
export function clamp01(v: number): number {
  return Number.isFinite(v) ? Math.min(100, Math.max(0, v)) : 0
}

/** 从 "YYYY-MM-DD HH:mm:ss" 取 HH:mm:ss;短于 19 位则原样返回。 */
export function timeOf(ts: string): string {
  return ts.length >= 19 ? ts.slice(11, 19) : ts
}

/**
 * 相对慢查询阈值的相位色:p95/threshold 的百分比所处档位。
 * 快→绿 / 关注→蓝 / 接近→琥珀 / 超限→红。
 */
export function heatTone(p95: number, thr: number): string {
  if (!Number.isFinite(p95) || !Number.isFinite(thr) || thr <= 0) return NEUTRAL
  if (p95 <= 0) return NEUTRAL
  const ratio = (p95 / thr) * 100
  if (ratio < 50) return '#10b981'
  if (ratio < 80) return '#3b82f6'
  if (ratio < 100) return '#f59e0b'
  return '#dc2626'
}

/** 单条查询耗时相对阈值的档位色。 */
export function durTone(ms: number, thr: number): string {
  if (!Number.isFinite(ms) || !Number.isFinite(thr) || thr <= 0) return NEUTRAL
  if (ms < thr / 3) return '#10b981'
  if (ms < thr) return '#f59e0b'
  return '#dc2626'
}

/**
 * 各类 SQL 操作的徽标主色。
 * 逐字取自原 sql.vue,同时被操作徽标与占比环图复用(单一来源)。
 */
export const OP_COLORS: Record<string, string> = {
  SELECT: '#3b82f6',
  INSERT: '#10b981',
  UPDATE: '#f59e0b',
  DELETE: '#dc2626',
  OTHER: '#94a3b8'
}

/** 操作维度列表(徽标/环图的展示顺序)。 */
export const OPS = ['SELECT', 'INSERT', 'UPDATE', 'DELETE', 'OTHER'] as const

/** 操作徽标内联样式:文字色 / 浅底 / 描边由主色派生。 */
export function opBadgeStyle(op: string): Record<string, string> {
  const color = OP_COLORS[op] ?? OP_COLORS.OTHER
  return { color, background: `${color}14`, borderColor: `${color}33` }
}

/** MySQL 系统库查询(GORM 元数据反射等),非业务表。 */
const SYS_TABLE_RE = /^(information_schema|performance_schema|mysql|sys)\./i

/** 判断表名是否属于系统库(带库名前缀才判定)。 */
export function isSysTable(table: string): boolean {
  return !!table && SYS_TABLE_RE.test(table)
}
