<template>
  <ElCard class="mp-panel" shadow="never">
    <!-- ============ 头部：标题 + 窗口/粒度（均来自响应字段） ============ -->
    <div class="mp-head">
      <div class="mp-head__title">
        <ArtSvgIcon :icon="drillMode ? 'ri:pie-chart-2-line' : 'ri:line-chart-line'" />
        <span>{{ drillMode ? `${kindLabel} · ${props.name}` : '整机趋势' }}</span>
      </div>
      <div v-if="meta" class="mp-head__meta">
        <!-- 把 resolution_seconds 与所选 range **一起**显示：
             「30 天 实际是 15 分钟一桶」这件事必须一眼可见。 -->
        <ElTag size="small" effect="plain">
          窗口 {{ formatDurationText(meta.rangeSeconds) }}（range_seconds={{ meta.rangeSeconds }}）
        </ElTag>
        <ElTag size="small" effect="plain" type="info">
          粒度 {{ formatResolution(meta.resolutionSeconds) }}（resolution_seconds={{
            meta.resolutionSeconds
          }}）
        </ElTag>
        <!-- source（redis/db）仅供调试与观测，业务不依赖；用极轻的 tag 展示。 -->
        <ElTag size="small" effect="plain" type="info">数据源 {{ meta.source }}</ElTag>
      </div>
    </div>

    <!-- ============ 档位：range 用**秒**传给后端 ============ -->
    <div class="mp-ranges">
      <ElRadioGroup
        :model-value="rangeSeconds"
        size="small"
        @update:model-value="selectRange($event as number)"
      >
        <ElTooltip
          v-for="r in ranges"
          :key="r.key"
          :disabled="!r.disabled"
          :content="r.disabledReason"
          placement="top"
        >
          <!-- disabled 的档位给出**原因**，而不是让用户点了再吃 400。 -->
          <span class="mp-ranges__slot">
            <ElRadioButton :value="r.seconds" :disabled="r.disabled">{{ r.label }}</ElRadioButton>
          </span>
        </ElTooltip>
      </ElRadioGroup>
      <div class="mp-ranges__hint">
        <template v-if="drillMode">
          资源下钻只保留 30 天（子表只有 5min 档）：range&gt;{{ DRILL_MAX_RANGE_SECONDS }}
          秒会被后端 400 拒绝，故 90 天 / 180 天档位在下钻场景已禁用。
        </template>
        <template v-else>
          档位按「整机趋势」口径可用：1 小时 ~ 180 天（后端 range 允许区间
          {{ RANGE_MIN_SECONDS }} ~ {{ RANGE_MAX_SECONDS }} 秒）。
        </template>
      </div>
    </div>

    <!-- ============ 列选择：列名**全部来自响应** available_metrics ============ -->
    <div v-if="chipColumns.length" class="mp-columns">
      <span class="mp-columns__label">指标列</span>
      <button
        v-for="column in chipColumns"
        :key="column"
        type="button"
        class="mp-chip"
        :class="{
          'is-active': selectedColumns.includes(column),
          'is-missing': !availableColumns.includes(column)
        }"
        :aria-pressed="selectedColumns.includes(column)"
        :title="
          availableColumns.includes(column)
            ? '切换此列'
            : '该档位无此指标（列不在本响应的 available_metrics 内）'
        "
        @click="toggleColumn(column)"
      >
        {{ column }}
      </button>
      <button type="button" class="mp-chip mp-chip--action" @click="selectAllColumns()">
        全量（{{ availableColumns.length }} 列）
      </button>
      <button type="button" class="mp-chip mp-chip--action" @click="selectDefaultColumns()">
        常用列
      </button>
    </div>

    <!-- ============ 「该档位无此指标」 vs 「未采集」：两种状态必须可区分 ============ -->
    <div v-if="built.unavailableColumns.length" class="mp-note mp-note--tier">
      <ArtSvgIcon icon="ri:information-line" />
      <span>
        该档位无此指标：<b>{{ built.unavailableColumns.join('、') }}</b
        >——这些列不在本响应 available_metrics 内（该档位根本不产出该列），与「未采集」不是一回事。
      </span>
    </div>
    <div class="mp-note mp-note--legend">
      断线 / 显示「—」= <b>未采集</b>（列在 available_metrics 内，但该桶没有样本；后端空桶不产行，故
      <code>t</code> 有洞）。上方「该档位无此指标」的列不会进入图表。
    </div>

    <!-- ============ 图表 + 三态 ============ -->
    <div class="mp-chart-wrap">
      <div ref="chartRef" class="mp-chart" />
      <div v-if="loading" class="mp-state">
        <ArtSvgIcon icon="ri:loader-4-line" class="mp-state__spin" /> 正在加载趋势…
      </div>
      <div v-else-if="errorText" class="mp-state mp-state--error">
        <ArtSvgIcon icon="ri:error-warning-line" />
        <span>{{ errorText }}</span>
        <ElButton size="small" type="primary" plain @click="load()">重试</ElButton>
      </div>
      <div v-else-if="!rows.length" class="mp-state">
        <ArtSvgIcon icon="ri:line-chart-line" />
        <span>该窗口暂无数据（后端在此时段没有产桶，不是加载失败）</span>
      </div>
      <div v-else-if="!selectedColumns.length" class="mp-state">
        <ArtSvgIcon icon="ri:checkbox-multiple-blank-line" />
        <span>请至少选择一个指标列</span>
      </div>
    </div>
  </ElCard>
</template>

<script lang="ts">
  /**
   * device 趋势面板的**纯逻辑**（可脱离 DOM、脱离组件单测）。
   *
   * 放在组件的普通 `<script>` 块里是有意的：这些函数不依赖任何响应式状态，
   * 具名导出后可被 vitest 直接 `import`（已实测 SFC 普通块的具名导出可用），
   * 从而本 Task 就能对「按下标推算时间」「把无此指标当成未采集」两类错误
   * 做真·反向验证。Task 5 会把它们原样抽到 `utils/metrics.ts`。
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
        points: rows.map(
          (row) => [bucketTimeMs(row), cellOf(row, column)] as [number, number | null]
        )
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
</script>

<script setup lang="ts">
  import { computed, nextTick, onMounted, ref, watch } from 'vue'
  import type { EChartsOption } from '@/plugins/echarts'
  import type { SetOptionOpts } from 'echarts/core'
  import { useChart } from '@/hooks/core/useChart'
  import { fetchDeviceMetrics, fetchDeviceResourceMetrics, type DeviceMetricsQuery } from '../api'

  defineOptions({ name: 'DeviceMetricsPanel' })

  const props = defineProps<{
    /** 设备 id（后端 `id,string`）。 */
    deviceId: string
    /** 资源种类；与 `name` 成对出现时是资源下钻，否则是整机趋势。 */
    kind?: string
    /** 资源名（挂载点/设备名/网卡名/传感器名）。 */
    name?: string
  }>()

  /** 有 kind + name = 资源下钻（同一个端点两种语义）。 */
  const drillMode = computed(() => Boolean(props.kind && props.name))
  const kindLabel = computed(() => kindLabelOf(props.kind ?? ''))
  const ranges = computed(() => rangeOptions(drillMode.value))

  const rangeSeconds = ref(DEFAULT_RANGE_SECONDS)
  const loading = ref(false)
  const errorText = ref('')

  /** 响应元信息：窗口 / **实际生效的**栅格 / 数据源 / 该档位可用列集。 */
  const meta = ref<{
    rangeSeconds: number
    resolutionSeconds: number
    source: string
    availableMetrics: string[]
  } | null>(null)

  /** 桶的开放形状（整机或下钻，见 trendRows / resourceRows）。 */
  const rows = ref<Record<string, unknown>[]>([])
  /** 用户选中的列（pin）：切换档位时**保留**，以便暴露「该档位无此指标」。 */
  const selectedColumns = ref<string[]>([])

  /** 可用列：来自响应，剔除桶键 bucket_ts。 */
  const availableColumns = computed(() =>
    (meta.value?.availableMetrics ?? []).filter((c) => c !== BUCKET_TS_COLUMN)
  )

  /** 给用户点选的列：可用列 ∪ 已 pin 的列（pin 但已不可用的列也能被取消）。 */
  const chipColumns = computed(() => {
    const out = [...availableColumns.value]
    for (const c of selectedColumns.value) {
      if (c !== BUCKET_TS_COLUMN && !out.includes(c)) out.push(c)
    }
    return out
  })

  /** 核心：由响应驱动构造 series，并区分「该档位无此指标」。 */
  const built = computed(() =>
    buildSeries(rows.value, selectedColumns.value, meta.value?.availableMetrics ?? [])
  )

  const { chartRef, initChart } = useChart()

  /** 系列名集合变化时才 replaceMerge：列增删时旧折线必须真正消失。 */
  let lastSeriesKey = ''
  function resolveSetOptionOpts(frames: SeriesFrame[]): SetOptionOpts | undefined {
    const key = frames.map((f) => f.name).join('\u0001')
    if (key === lastSeriesKey) return undefined
    lastSeriesKey = key
    return { replaceMerge: ['series'] }
  }

  interface AxisTipParam {
    seriesName?: string
    value?: [number, number | null] | number | null
  }

  function buildChartOption(frames: SeriesFrame[]): EChartsOption {
    const range = meta.value?.rangeSeconds ?? DEFAULT_RANGE_SECONDS
    return {
      animation: false,
      grid: { top: 16, right: 16, bottom: 8, left: 8, containLabel: true },
      tooltip: {
        trigger: 'axis',
        formatter: (params: unknown) => {
          const list = (Array.isArray(params) ? params : [params]) as AxisTipParam[]
          if (!list.length) return ''
          const firstValue = list[0]?.value
          const at = Array.isArray(firstValue) ? firstValue[0] : null
          const lines = list.map((p) => {
            const v = Array.isArray(p.value) ? p.value[1] : null
            return `${p.seriesName ?? ''}: ${formatMetricValue(v)}`
          })
          const head = typeof at === 'number' ? formatBucketTime(at) : ''
          return [head, ...lines].filter((s) => s !== '').join('<br/>')
        }
      },
      xAxis: {
        // time 轴：x 是真实时间戳，稀疏桶之间的空洞按**真实跨度**呈现。
        type: 'time',
        axisLabel: {
          hideOverlap: true,
          formatter: (value: number) => formatAxisTick(value, range)
        }
      },
      yAxis: { type: 'value', scale: true },
      series: frames.map((frame) => ({
        name: frame.name,
        type: 'line' as const,
        showSymbol: false,
        // 既有封装 ArtLineChart **不支持** connectNulls（无该 prop、且是 category
        // 轴 + number[] 数据，无法表达 [t, value] 对），故这里直接用同一套
        // useChart/echarts 基础设施，并**显式**关掉连线：
        // 空桶（后端不产行）不得被连成误导性直线。
        connectNulls: false,
        data: frame.points
      }))
    }
  }

  function renderChart() {
    if (loading.value || errorText.value) return
    const frames = built.value.series
    if (!rows.value.length) return
    if (!frames.length) {
      // 用户取消了全部列（或所选列全部「该档位无此指标」）：必须把画布上的旧折线
      // 真正清掉 —— 否则 overlay 下面仍留着上一种状态的曲线，一取消列就露出来。
      lastSeriesKey = ''
      initChart(buildChartOption([]), false, { replaceMerge: ['series'] })
      return
    }
    initChart(buildChartOption(frames), false, resolveSetOptionOpts(frames))
  }

  function applyResponse(res: {
    range_seconds: number
    resolution_seconds: number
    source: string
    available_metrics: string[]
    buckets: unknown[]
  }) {
    meta.value = {
      rangeSeconds: res.range_seconds,
      resolutionSeconds: res.resolution_seconds,
      source: res.source,
      availableMetrics: res.available_metrics
    }
    rows.value = drillMode.value
      ? resourceRows(res.buckets as Api.Device.DeviceResourcePoint[])
      : trendRows(res.buckets as Api.Device.DeviceMetricPoint[])
    const available = availableColumns.value
    // 首次加载给「常用列」；之后切换档位**保留**用户的 pin —— 这样某档位
    // 不再产某列时，它会出现在「该档位无此指标」里，而不是静默消失。
    if (!selectedColumns.value.length) selectedColumns.value = defaultColumns(available)
  }

  async function load() {
    if (!props.deviceId) return
    loading.value = true
    errorText.value = ''
    try {
      // metrics=* ：后端 MetricsAll 哨兵，令 available_metrics 成为**该档位真实的
      // 列集**（若改传显式 CSV，后端会把请求列原样回显进 available_metrics，
      // 该字段就退化成请求的复读，无法反映「该档位到底产哪些列」）。
      const params: DeviceMetricsQuery = { range: rangeSeconds.value, metrics: METRICS_ALL }
      const res = drillMode.value
        ? await fetchDeviceResourceMetrics(props.deviceId, {
            ...params,
            kind: props.kind,
            name: props.name
          })
        : await fetchDeviceMetrics(props.deviceId, params)
      applyResponse(res)
      await nextTick()
      renderChart()
    } catch (e) {
      meta.value = null
      rows.value = []
      errorText.value = e instanceof Error && e.message ? e.message : '加载趋势失败'
    } finally {
      loading.value = false
    }
  }

  function selectRange(seconds: number) {
    if (rangeSeconds.value === seconds) return
    rangeSeconds.value = seconds
  }

  function toggleColumn(column: string) {
    if (selectedColumns.value.includes(column)) {
      selectedColumns.value = selectedColumns.value.filter((c) => c !== column)
    } else {
      selectedColumns.value = [...selectedColumns.value, column]
    }
  }

  function selectAllColumns() {
    selectedColumns.value = availableColumns.value.slice()
  }

  function selectDefaultColumns() {
    selectedColumns.value = defaultColumns(availableColumns.value)
  }

  watch(rangeSeconds, () => load())
  // 下钻目标变化（切资源 / 切 kind）→ 重新加载并给该资源自己的默认列。
  watch(
    () => `${props.deviceId}|${props.kind ?? ''}|${props.name ?? ''}`,
    () => {
      selectedColumns.value = []
      load()
    }
  )
  watch([() => built.value, () => loading.value, () => errorText.value], () => {
    nextTick(renderChart)
  })

  onMounted(() => {
    load()
  })
</script>

<style lang="scss" scoped>
  .mp-panel {
    --mp-gap: 12px;
  }

  .mp-head {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;

    &__title {
      display: flex;
      align-items: center;
      gap: 6px;
      font-size: 15px;
      font-weight: 600;
      color: var(--el-text-color-primary);
    }

    &__meta {
      display: flex;
      flex-wrap: wrap;
      gap: 6px;
      margin-left: auto;
    }
  }

  .mp-ranges {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
    margin-top: var(--mp-gap);

    &__slot {
      display: inline-flex;
    }

    &__hint {
      font-size: 12px;
      line-height: 1.5;
      color: var(--el-text-color-secondary);
    }
  }

  .mp-columns {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px;
    margin-top: var(--mp-gap);

    &__label {
      font-size: 12px;
      color: var(--el-text-color-secondary);
    }
  }

  .mp-chip {
    padding: 2px 10px;
    font-size: 12px;
    color: var(--el-text-color-regular);
    cursor: pointer;
    background: var(--el-fill-color-light);
    border: 1px solid transparent;
    border-radius: 999px;

    &.is-active {
      color: var(--el-color-primary);
      background: var(--el-color-primary-light-9);
      border-color: var(--el-color-primary-light-5);
    }

    &.is-missing {
      color: var(--el-color-warning);
      text-decoration: line-through;
    }

    &--action {
      border-color: var(--el-border-color);
    }
  }

  .mp-note {
    display: flex;
    gap: 6px;
    align-items: flex-start;
    margin-top: 8px;
    font-size: 12px;
    line-height: 1.6;
    color: var(--el-text-color-secondary);

    &--tier {
      color: var(--el-color-warning-dark-2);
    }

    code {
      padding: 0 3px;
      font-family: var(--el-font-family-monospace, monospace);
      background: var(--el-fill-color-light);
      border-radius: 3px;
    }
  }

  .mp-chart-wrap {
    position: relative;
    min-height: 300px;
    margin-top: var(--mp-gap);
  }

  .mp-chart {
    width: 100%;
    height: 300px;
  }

  .mp-state {
    position: absolute;
    inset: 0;
    display: flex;
    flex-direction: column;
    gap: 8px;
    align-items: center;
    justify-content: center;
    font-size: 13px;
    color: var(--el-text-color-secondary);
    background: var(--el-bg-color);

    &--error {
      color: var(--el-color-danger);
    }

    &__spin {
      animation: mp-spin 1s linear infinite;
    }
  }

  @keyframes mp-spin {
    from {
      transform: rotate(0deg);
    }

    to {
      transform: rotate(360deg);
    }
  }
</style>
