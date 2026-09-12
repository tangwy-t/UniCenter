/** 迷你趋势固定窗口(秒):与旧 sparkData 的 10m 窗口语义一致 */
export const SPARK_WINDOW_SECONDS = 600

/**
 * 迷你趋势取点数:ceil(窗口/步长),封顶 [min,max]。
 * step 非法(≤0)时回退 min,视图层由 history step_seconds 提供,恒 ≥1。
 */
export function sparkWindowCount(
  stepSeconds: number,
  windowSeconds: number = SPARK_WINDOW_SECONDS,
  min = 3,
  max = 60
): number {
  if (!Number.isFinite(stepSeconds) || stepSeconds <= 0) return min
  const n = Math.ceil(windowSeconds / stepSeconds)
  return Math.min(Math.max(n, min), max)
}

/**
 * 从历史桶尾部派生迷你趋势序列:缺值向前填充,跳过前导 undefined
 * (与旧 server.vue seriesOf 语义一致)。
 */
export function buildSparkSeries<T extends { timestamp: string }>(
  buckets: T[],
  stepSeconds: number,
  key: Extract<keyof T, string>,
  windowSeconds: number = SPARK_WINDOW_SECONDS
): number[] {
  const count = sparkWindowCount(stepSeconds, windowSeconds)
  const tail = buckets.slice(-count)
  const out: number[] = []
  let prev: number | undefined
  for (const b of tail) {
    const v = b[key]
    if (typeof v === 'number' && Number.isFinite(v)) prev = v
    if (prev !== undefined) out.push(prev)
  }
  return out
}

/** 迷你趋势曲线的 SVG path 片段(0..100 视口坐标)。 */
export interface SparkResult {
  /** 折线 path 的 points */
  line: string
  /** 面积 path 的 points(空串表示不绘制面积) */
  area: string
}

/**
 * 把数值序列转换为 0..100 视口内的折线/面积 path。
 *
 * 从 views/sql.vue 与 views/server.vue 各一份的同名实现合并而来。
 * 两份的唯一差异是 `pad`:
 *   - server.vue 写成 `pad + (1-t) * (50 - pad*2)`,默认 pad=3
 *   - sql.vue 写死 `3 + (1-t) * 44`
 * 二者在 pad=3 时**恒等**(3 + 44 == 3 + (50-6)),即 sql.vue 那份
 * 只是本函数的 pad=3 特例。统一为参数化实现,避免两份再次漂移。
 *
 * @param series 数值序列
 * @param pad    上下留白(视口 0..50),默认 3
 */
export function sparkPath(series: number[], pad = 3): SparkResult {
  const n = series.length
  if (!n) return { line: '', area: '' }
  // 单点无斜率可言,画一条水平中线;不画面积(没有基线可围合)。
  if (n === 1) return { line: '0,25 100,25', area: '' }

  let min = Infinity
  let max = -Infinity
  for (const v of series) {
    if (v < min) min = v
    if (v > max) max = v
  }
  // 全等序列 span 为 0 → 取 1 避免除零,此时所有点落在同一高度。
  const span = max - min || 1
  const px = (i: number) => ((i / (n - 1)) * 100).toFixed(2)
  const py = (v: number) => (pad + (1 - (v - min) / span) * (50 - pad * 2)).toFixed(2)
  const pts = series.map((v, i) => `${px(i)},${py(v)}`).join(' ')
  return { line: pts, area: `0,50 ${pts} 100,50` }
}
