/**
 * device 趋势面板 / 资源下钻的**纯逻辑**（可脱离 DOM、脱离组件单测）。
 *
 * 这些函数原本以具名导出放在 `components/metrics-panel.vue` 的普通 `<script>`
 * 块里（Task 4 的有意为之）；Task 5 把它们**原样**搬到这里，组件改为 import，
 * 组件行为一字未改，但纯逻辑不再依赖 SFC 编译即可被 vitest 直接单测。
 *
 * 三条契约（写错最难在浏览器里发现，故由单测钉死）：
 * 1. **稀疏桶**：`buckets` 是稀疏的、`t` 有洞，X 轴**必须**用 `t`；
 *    按下标 × resolution 推算时间会把「3 小时空洞」画成「等距相邻两桶」。
 * 2. **`available_metrics` 两态**：「列在集合内但该桶无样本 → 未采集」与
 *    「列不在集合内 → 该档位无此指标」**必须可区分**（不同返回字段）。
 * 3. **档位约束**：下钻排除 >30d（2592000 恰是 30d 且仍是 5m 档，含）。
 */

/** 后端 `request.DeviceRangeMin`：小于它 service 返回 400。 */
export const RANGE_MIN_SECONDS = 3600
/** 后端 `request.DeviceRangeMax`（180d）。 */
export const RANGE_MAX_SECONDS = 15552000
/**
 * 下钻的 400 边界。后端 `ResourceMetrics` 在 `sel.Table != device_metric_5m`
 * 时返回 400；`SelectTier` 的 `rangeSec <= 30*day` 是**闭区间**，故 2592000
 * 恰好是 30d 且**仍是** 5m 档（后端 wireup e2e 已实测确认）。前端必须与之一致：
 * 下钻允许到 30d（含），只禁用 >30d。
 */
export const DRILL_MAX_RANGE_SECONDS = 2592000

/**
 * 桶时间列名。它是响应契约里的**桶键**（`t`），不是指标列 ——
 * 后端的 `available_metrics` 会把它一并列出（恒补 bucket_ts），
 * 故必须从列选择里剔除，否则 UI 会把时间当指标提供给用户。
 */
export const BUCKET_TS_COLUMN = 'bucket_ts'

/** 「取全量列」哨兵值（后端 `request.MetricsAll`）。 */
export const METRICS_ALL = '*'

/** 默认窗口（秒）：24h（后端 `range` 缺省值也是 24h）。 */
export const DEFAULT_RANGE_SECONDS = 86400

export interface RangeOption {
  key: string
  label: string
  seconds: number
}

const RANGE_DEFS: RangeOption[] = [
  { key: '1h', label: '1 小时', seconds: 3600 },
  { key: '24h', label: '24 小时', seconds: 86400 },
  { key: '7d', label: '7 天', seconds: 604800 },
  { key: '30d', label: '30 天', seconds: 2592000 },
  { key: '90d', label: '90 天', seconds: 7776000 },
  { key: '180d', label: '180 天', seconds: 15552000 }
]

export interface RangeChoice extends RangeOption {
  disabled: boolean
  disabledReason: string
}

/**
 * 可用档位。下钻（`isDrill`）排除 >30d 的档位，并给出**禁用原因**，
 * 使用户不可能「点了再吃 400」。
 */
export function rangeOptions(isDrill: boolean): RangeChoice[] {
  return RANGE_DEFS.map((r) => {
    const disabled = isDrill && r.seconds > DRILL_MAX_RANGE_SECONDS
    return {
      ...r,
      disabled,
      disabledReason: disabled
        ? `资源明细只保留 30 天（子表只有 5min 档），range>${DRILL_MAX_RANGE_SECONDS} 秒会被后端拒绝（400）`
        : ''
    }
  })
}

/**
 * 秒 → 人类可读文案。
 * 10→「10 秒」、300→「5 分钟」、900→「15 分钟」、3600→「1 小时」、7200→「2 小时」。
 */
export function formatDurationText(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 0) return '—'
  if (seconds % 86400 === 0) return `${seconds / 86400} 天`
  if (seconds % 3600 === 0) return `${seconds / 3600} 小时`
  if (seconds % 60 === 0) return `${seconds / 60} 分钟`
  return `${seconds} 秒`
}

/**
 * 栅格粒度文案。**只由响应的 `resolution_seconds` 推导**，
 * 绝不按「1h/24h/7d」按钮名硬编码 —— 实际栅格可能被后端升档（如 30d → 15 分钟）。
 */
export function formatResolution(seconds: number): string {
  const text = formatDurationText(seconds)
  return text === '—' ? text : `每 ${text}`
}

/** 图表一个系列：x 是桶的**真实 unix 时刻（ms）**，不是下标。 */
export interface SeriesFrame {
  name: string
  points: Array<[number, number | null]>
}

export interface SeriesBuild {
  /** 只含 `availableMetrics` 内的列（渲染进图表）。 */
  series: SeriesFrame[]
  /** 不在 `availableMetrics` 内的列 → UI 显示「该档位无此指标」，**不是**「未采集」。 */
  unavailableColumns: string[]
}

/**
 * 读一个桶的值列。
 *
 * **存在性用 `in` 判断，不用 `!= null`**：后端保证「值为 nil 的列不出现」
 * （`DeviceResourcePoint.Values` 的 omitempty；宽表同理），故「键不存在」
 * 与「值为 nil」在语义上都是「该桶无该列样本」，但用 `in` 判断才不会把
 * 「缺」与「nil」混同（Task 1 报告点名的坑）。
 */
function cellOf(row: Record<string, unknown>, column: string): number | null {
  if (!(column in row)) return null
  const value = row[column]
  return typeof value === 'number' && Number.isFinite(value) ? value : null
}

/**
 * 桶的真实时刻（ms）。**只用 `t`**（unix 秒）：
 * `buckets` 是稀疏的、`t` 有洞，按下标推算时间会把「3 小时的空洞」
 * 画成「等距的相邻两桶」，是误导性直线。
 */
function bucketTimeMs(row: Record<string, unknown>): number {
  const t = row.t
  return typeof t === 'number' && Number.isFinite(t) ? t * 1000 : Number.NaN
}

/**
 * 由桶数组构造图表 series。
 *
 * @param rows             开放形状的桶（含 `t`，值列按 `列名 → 值`）
 * @param columns          调用方选择的列（用户选中的「pin」）
 * @param availableMetrics 响应里的 `available_metrics`（该档位真实的列集）
 */
export function buildSeries(
  rows: readonly Record<string, unknown>[],
  columns: readonly string[],
  availableMetrics: readonly string[]
): SeriesBuild {
  const available = new Set(availableMetrics)
  const series: SeriesFrame[] = []
  const unavailableColumns: string[] = []
  for (const column of columns) {
    if (column === BUCKET_TS_COLUMN) continue // 桶键：时间走 x 轴，不是系列
    if (!available.has(column)) {
      // 该档位不产此列 —— 与「未采集」**必须可区分**，故单列一档。
      unavailableColumns.push(column)
      continue
    }
    series.push({
      name: column,
      points: rows.map((row) => [bucketTimeMs(row), cellOf(row, column)] as [number, number | null])
    })
  }
  return { series, unavailableColumns }
}

/** 整机宽表桶 → 开放形状（`t` 是 unix 秒，值列按后端 snake_case 列名）。 */
export function trendRows(
  buckets: readonly Api.Device.DeviceMetricPoint[]
): Record<string, unknown>[] {
  return buckets.map((bucket) => ({ ...bucket }))
}

/**
 * 下钻子表桶 → 开放形状：把 `values` 摊平。
 *
 * `values?: Record<string, number|null>` 是**可选**的（Task 1 报告点名的坑），
 * 且后端不写 nil 列，故 `?? {}` 后摊平即可；`t` 最后覆盖，避免值列里
 * 出现同名键时把桶时间冲掉。
 */
export function resourceRows(
  buckets: readonly Api.Device.DeviceResourcePoint[]
): Record<string, unknown>[] {
  return buckets.map((bucket) => ({ ...(bucket.values ?? {}), t: bucket.t }))
}

/** 坐标轴刻度文案：由**响应的 range**决定粒度，不由按钮名决定。 */
export function formatAxisTick(ms: number, rangeSeconds: number): string {
  const d = new Date(ms)
  const p = (n: number) => String(n).padStart(2, '0')
  const hm = `${p(d.getHours())}:${p(d.getMinutes())}`
  if (rangeSeconds <= 86400) return hm
  const md = `${p(d.getMonth() + 1)}-${p(d.getDate())}`
  if (rangeSeconds <= 604800) return `${md} ${hm}`
  return `${d.getFullYear()}-${md}`
}

/** tooltip 里的完整时刻（含秒，便于对齐 resolution_seconds 栅格）。 */
export function formatBucketTime(ms: number): string {
  const d = new Date(ms)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(
    d.getMinutes()
  )}:${p(d.getSeconds())}`
}

/** 值展示：缺值一律「—」（不得当成 0）。 */
export function formatMetricValue(value: number | null | undefined): string {
  if (typeof value !== 'number' || !Number.isFinite(value)) return '—'
  return Number.isInteger(value) ? String(value) : value.toFixed(2)
}

/**
 * 默认（常用）列：**优先集 ∩ 响应可用列**，为空时退化为可用列前几个。
 *
 * 优先集只是一份「偏好顺序」，列名是否真的渲染**完全由 available_metrics 决定**
 * —— 不在集合里的列既不会被请求、也不会进图例（见 buildSeries）。
 */
export const PREFERRED_COLUMNS: string[] = [
  // 整机宽表（spec §5.5）默认系列
  'cpu_used_percent',
  'load1',
  'mem_used_percent',
  'disk_used_percent',
  'nic_rx_bytes_sec',
  'nic_tx_bytes_sec',
  'max_temperature_c',
  // 下钻子表列名（与子表自己的列一致，故与宽表列名不同名）
  'used_percent',
  'used_gb',
  'read_bytes_per_sec',
  'write_bytes_per_sec',
  'io_time_percent',
  'rx_bytes_per_sec',
  'tx_bytes_per_sec',
  'rx_errors_per_sec',
  'inodes_used_percent',
  'temperature_c'
]

export function defaultColumns(available: readonly string[]): string[] {
  const preferred = PREFERRED_COLUMNS.filter((c) => available.includes(c))
  return preferred.length ? preferred : available.slice(0, 4)
}

/** 资源种类 → 中文标签（下钻下拉的 kind 取值由后端 `device_resource.kind` 决定）。 */
export const RESOURCE_KINDS: Array<{ value: string; label: string }> = [
  { value: 'disk', label: '磁盘分区' },
  { value: 'disk_io', label: '磁盘 IO' },
  { value: 'nic', label: '网卡' },
  { value: 'sensor', label: '传感器' }
]

export function kindLabelOf(kind: string): string {
  return RESOURCE_KINDS.find((k) => k.value === kind)?.label ?? kind
}

/**
 * 下钻资源项 → 是否要在下拉里标「已消失」。
 *
 * 纯函数，便于单测/反向验证直接命中该语义
 * （stale 由后端给出：超过 resourceStaleMarkDays 未出现即 true）。
 */
export function isStaleResource(item: { stale: boolean }): boolean {
  return item.stale === true
}

/** unix 秒 → 本地时间；缺值「—」（与列表/详情同一约定）。 */
export function formatSeenAt(v?: number | null): string {
  if (v === undefined || v === null) return '—'
  const d = new Date(v * 1000)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(
    d.getMinutes()
  )}`
}
