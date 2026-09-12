/**
 * 服务端监控视图的纯格式化 / 阈值配色工具。
 *
 * 从 views/server.vue(1550+ 行)抽出:这些函数不依赖响应式状态,
 * 抽出后可脱离组件单测(此前配色档位只能靠人工构造负载观察)。
 *
 * 与 use-sql-format 的同名函数是刻意分开的:两者档位阈值不同
 * (例如 clamp01 的入参宽容度、配色分界),合并会掩盖各自的语义,
 * 待确认真实需求一致后再收敛。
 */

/** 未知 / 不适用时的中性灰。 */
const NEUTRAL = '#94a3b8'

/** 保留 1 位小数;非有限数返回占位符。 */
export function fmt1(v: number | null | undefined): string {
  return typeof v === 'number' && Number.isFinite(v) ? v.toFixed(1) : '-'
}

/** 保留 2 位小数;非有限数返回占位符。 */
export function fmt2(v: number | null | undefined): string {
  return typeof v === 'number' && Number.isFinite(v) ? v.toFixed(2) : '-'
}

/**
 * 以 MB 为输入自动选择单位:>=1024MB 显示 GB、<1MB 显示 KB、其余 MB。
 * 注意 1024MB 的边界归入 GB。
 */
export function fmtMB(mb: number): string {
  if (!Number.isFinite(mb)) return '-'
  if (mb >= 1024) return `${(mb / 1024).toFixed(2)} GB`
  if (mb < 1) return `${Math.round(mb * 1024)} KB`
  return `${mb.toFixed(1)} MB`
}

/**
 * 以 GB 为输入自动选择单位:>=1024GB 显示 TB。
 * 其余 GB:>=100 取整(避免无意义的小数),否则保留 1 位。
 */
export function fmtGB(gb: number): string {
  if (!Number.isFinite(gb)) return '-'
  if (gb >= 1024) return `${(gb / 1024).toFixed(2)} TB`
  return `${gb >= 100 ? gb.toFixed(0) : gb.toFixed(1)} GB`
}

/**
 * 运行时长文案:按天/时/分逐级降级,只显示到"第一个非零单位"的下一级。
 * 负数或非有限数返回占位符。
 */
export function fmtDurationText(seconds: number): string {
  const s = Math.floor(seconds)
  if (!Number.isFinite(s) || s < 0) return '-'
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  if (d > 0) return `${d} 天 ${h} 时 ${m} 分`
  if (h > 0) return `${h} 时 ${m} 分`
  if (m > 0) return `${m} 分 ${s % 60} 秒`
  return `${s % 60} 秒`
}

/**
 * 资源占用百分比(0..100)的档位色:
 * <50 绿 / <80 蓝 / <90 琥珀 / >=90 红。
 */
export function usageTone(p: number | null | undefined): string {
  if (typeof p !== 'number' || !Number.isFinite(p)) return NEUTRAL
  if (p < 50) return '#10b981'
  if (p < 80) return '#3b82f6'
  if (p < 90) return '#f59e0b'
  return '#dc2626'
}

/**
 * 负载(load / 核数)的档位色:
 * <0.7 绿 / <1.3 蓝 / <2.5 琥珀 / >=2.5 红。
 * 核数非法(<=0)时无法判断,返回中性色。
 */
export function loadTone(load: number | null | undefined, cores: number): string {
  if (typeof load !== 'number' || !Number.isFinite(load) || cores <= 0) return NEUTRAL
  const ratio = load / cores
  if (ratio < 0.7) return '#10b981'
  if (ratio < 1.3) return '#3b82f6'
  if (ratio < 2.5) return '#f59e0b'
  return '#dc2626'
}

/** 夹到 [0,100];非法输入归 0(供进度条宽度使用)。 */
export function clamp01(v: number | null | undefined): number {
  return typeof v === 'number' && Number.isFinite(v) ? Math.min(100, Math.max(0, v)) : 0
}
