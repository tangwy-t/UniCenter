/**
 * SQL 监控视图的纯格式化 / 阈值配色工具。
 *
 * 从 views/sql.vue(1790+ 行)抽出:这些函数不依赖任何响应式状态,
 * 是"输入 → 输出"的纯逻辑。抽出来后可脱离组件单测,视图只留编排。
 *
 * 配色函数返回的是**字面色值**而非 CSS 变量:视图侧需要把它们
 * 拼进 style(如 `${color}14` 做浅底),CSS 变量无法参与字符串拼接。
 * 例外是文字色的 AA 收口(走 token 层的 var(--aa-*-text)):durTextTone
 * (时长**文字**)与 opBadgeStyle 的 color 槽——它们不做字符串拼接、只喂
 * color,故返回 token 引用;durTone 与 OP_COLORS 的字面色值继续服务条形
 * 填充/环图/徽标浅底(非文本,不进文字族)。
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
 * 时长数值**文字**的档位色,与 durTone 同一分档,但返回 AA token 引用
 * (语义色文字族收口,数字与理由见 @styles/core/aa-text.scss 文件头):
 *   - 绿 #10b981 对白底原仅 2.54:1、琥珀 #f59e0b 2.15:1、红 #dc2626 在
 *     暗色下 3.74:1 —— 全部过不了 4.5:1 门槛;
 *   - 换 token 后(浅色白底/灰底、暗色暗卡均含在内):成功 5.07–5.41、
 *     警告 5.21–5.55、危险 5.15–5.81、暗色 7.75+。
 * 中性灰(阈值非法时的占位档)由 #94a3b8(白底 2.56:1)改走 regular 文字色
 * (白底 6.11:1,暗色随主题自适应)。durTone 的字面色值保留给条形填充与
 * 进度条(非文本族,维持既有裁定),二者共用同一分档逻辑(durTone 为唯一源)。
 */
const DUR_TEXT_TONE: Record<string, string> = {
  '#10b981': 'var(--aa-success-text)',
  '#f59e0b': 'var(--aa-warning-text)',
  '#dc2626': 'var(--aa-danger-text)',
  '#94a3b8': 'var(--el-text-color-regular)'
}

export function durTextTone(ms: number, thr: number): string {
  return DUR_TEXT_TONE[durTone(ms, thr)] ?? 'var(--el-text-color-regular)'
}

/**
 * 各类 SQL 操作的徽标主色。
 * 逐字取自原 sql.vue,同时被操作徽标与占比环图复用(单一来源)。
 * 注意:作为**文字**色的收官在 opBadgeStyle(见 OP_TEXT_TONE);本表的字面值
 * 继续服务徽标浅底/描边与占比环图(非文本族,维持既有裁定)。
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

/**
 * 徽标文字色的 AA 档:与 DUR_TEXT_TONE 同一处方(文字走 token、字面值留给
 * 非文本),数字与理由见 @styles/core/aa-text.scss 文件头。
 * 原值对白卡全部过不了线:#3b82f6 3.68、#10b981 2.54、#f59e0b 2.15、
 * #dc2626 4.83(对自身 8% 淡染底只有 4.2x)、中性灰 #94a3b8 2.56;
 * 换 token 后 5.15–6.04,中性档走 regular 文字色(白底 6.11、暗色随主题)。
 * 未知档(表外颜色)兜底 regular,与 NEUTRAL 的处置一致。
 */
const OP_TEXT_TONE: Record<string, string> = {
  '#3b82f6': 'var(--aa-primary-text)',
  '#10b981': 'var(--aa-success-text)',
  '#f59e0b': 'var(--aa-warning-text)',
  '#dc2626': 'var(--aa-danger-text)'
}

/** 操作徽标内联样式:文字色走 AA token / 浅底 / 描边由主色派生(非文本)。 */
export function opBadgeStyle(op: string): Record<string, string> {
  const color = OP_COLORS[op] ?? OP_COLORS.OTHER
  return {
    color: OP_TEXT_TONE[color] ?? 'var(--el-text-color-regular)',
    background: `${color}14`,
    borderColor: `${color}33`
  }
}

/** MySQL 系统库查询(GORM 元数据反射等),非业务表。 */
const SYS_TABLE_RE = /^(information_schema|performance_schema|mysql|sys)\./i

/** 判断表名是否属于系统库(带库名前缀才判定)。 */
export function isSysTable(table: string): boolean {
  return !!table && SYS_TABLE_RE.test(table)
}
