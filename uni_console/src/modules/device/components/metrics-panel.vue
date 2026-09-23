<template>
  <div class="mp-panel dd-surface">
    <!-- ============ 头部：标题 + 窗口/粒度（均来自响应字段） ============ -->
    <div class="mp-head">
      <div class="mp-head__title">
        <ArtSvgIcon :icon="drillMode ? 'ri:pie-chart-2-line' : 'ri:line-chart-line'" />
        <span>{{ drillMode ? `${kindLabel} · ${props.name}` : '整机趋势' }}</span>
      </div>
      <!-- 主行只说人话：粒度 / 数据源 / 采样点数。
           range_seconds、resolution_seconds、source 这三个技术参数收进 tooltip —— 
           它们对排障有用，但对「这图能信吗」没有帮助，不该占据主视线。 -->
      <div v-if="meta" class="mp-head__meta">
        <span class="mp-head__summary">{{ metaSummary }}</span>
        <!-- 采样点数远低于该窗口应有桶数 → 显式标注「数据稀疏」，
             避免用户把断线当成系统故障（或反过来，把稀疏曲线当成真实负载）。 -->
        <ElTag v-if="isSparse" size="small" type="warning" effect="light">数据稀疏</ElTag>
        <ElTooltip placement="top">
          <template #content>
            <div class="mp-tip">
              <div
                >窗口 {{ formatDurationText(meta.rangeSeconds) }}（range_seconds={{
                  meta.rangeSeconds
                }}）</div
              >
              <div
                >粒度 {{ formatResolution(meta.resolutionSeconds) }}（resolution_seconds={{
                  meta.resolutionSeconds
                }}）</div
              >
              <div>数据源 {{ meta.source }}（redis=热层 / db=历史表）</div>
              <div>桶数上限 {{ MAX_BUCKETS }}，超出时后端自动升档（粒度变粗）</div>
            </div>
          </template>
          <ArtSvgIcon class="mp-head__info" icon="ri:information-line" />
        </ElTooltip>
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

    <!-- ============ 列选择（F-8）============ -->
    <!-- 已选列以 chips 常驻显示（点击即取消）；「+ 添加指标」打开分组下拉。
         chips 与下拉共用同一份 selectedColumns —— 单一事实源，不会出现
         「下拉里勾了但 chips 没变」的双重真相。 -->
    <div v-if="chipColumns.length" class="mp-columns">
      <span class="mp-columns__label">指标</span>
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
        :title="chipTitle(column)"
        @click="toggleColumn(column)"
      >
        {{ metricLabel(column) }}
        <span v-if="!availableColumns.includes(column)" class="mp-chip__flag">本档位无</span>
      </button>

      <ElDropdown trigger="click" :hide-on-click="false">
        <button type="button" class="mp-chip mp-chip--action">+ 添加指标</button>
        <template #dropdown>
          <ElDropdownMenu class="mp-colmenu">
            <div v-for="bucket in groupedColumns" :key="bucket.group" class="mp-colmenu__group">
              <p class="mp-colmenu__group-name">{{ bucket.group }}</p>
              <ElDropdownItem
                v-for="column in bucket.columns"
                :key="column"
                :disabled="!availableColumns.includes(column)"
                @click="toggleColumn(column)"
              >
                <span class="mp-colmenu__item">
                  <ArtSvgIcon
                    :icon="
                      selectedColumns.includes(column)
                        ? 'ri:checkbox-line'
                        : 'ri:checkbox-blank-line'
                    "
                  />
                  <span>{{ metricLabel(column) }}</span>
                  <span v-if="metricUnit(column)" class="mp-colmenu__unit">
                    ({{ metricUnit(column) }})
                  </span>
                  <span v-if="!availableColumns.includes(column)" class="mp-colmenu__na">
                    本档位无
                  </span>
                </span>
              </ElDropdownItem>
            </div>
          </ElDropdownMenu>
        </template>
      </ElDropdown>

      <button type="button" class="mp-chip mp-chip--action" @click="selectAllColumns()">
        全量（{{ availableColumns.length }}）
      </button>
      <button type="button" class="mp-chip mp-chip--action" @click="selectDefaultColumns()">
        常用
      </button>
      <!-- F-18 导出：把当前已选列 + 桶时间导出成 CSV（纯前端） -->
      <button v-if="rows.length" type="button" class="mp-chip mp-chip--action" @click="exportCsv()">
        导出 CSV
      </button>
    </div>

    <!-- ============ 「该档位无此指标」 vs 「未采集」：两种状态必须可区分 ============ -->
    <div v-if="built.unavailableColumns.length" class="mp-note mp-note--tier">
      <ArtSvgIcon icon="ri:information-line" />
      <span>
        该档位无此指标：<b>{{ unavailableLabels }}</b
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
  </div>
</template>

<script lang="ts">
  // 纯逻辑已原样抽到 `../utils/metrics`（Task 5）。这里**必须**用普通 import（而非
  // `export … from` 中转）：经 SFC 实测，普通块的 import 会注册为模板可见绑定，
  // 而纯再导出不会（`@vue/compiler-sfc` 的 bindings 里根本没有它们）→ 会编译报错。
  import {
    BUCKET_TS_COLUMN,
    DEFAULT_RANGE_SECONDS,
    DRILL_MAX_RANGE_SECONDS,
    MAX_BUCKETS,
    METRICS_ALL,
    PREFERRED_COLUMNS,
    RANGE_MAX_SECONDS,
    RANGE_MIN_SECONDS,
    RESOURCE_KINDS,
    buildSeries,
    cellOf,
    defaultColumns,
    expectedBucketCount,
    formatAxisTick,
    formatBucketTime,
    formatDurationText,
    formatMetricValue,
    formatResolution,
    isSparseData,
    kindLabelOf,
    rangeOptions,
    resourceRows,
    trendRows
  } from '../utils/metrics'
  import type { RangeChoice, RangeOption, SeriesBuild, SeriesFrame } from '../utils/metrics'

  // 列名展示元数据（F-8）：中文名 / 单位 / 分组。
  // 必须在这里以普通 import 引入（不能 `export … from` 中转）—— 理由见本块顶部注释。
  import { groupMetricColumns, metricLabel, metricTipLine, metricUnit } from '../utils/column-meta'

  // 既有 import 方（resource-drill.vue 与单测）照旧走组件路径，故一并再导出。
  export {
    BUCKET_TS_COLUMN,
    DEFAULT_RANGE_SECONDS,
    DRILL_MAX_RANGE_SECONDS,
    METRICS_ALL,
    PREFERRED_COLUMNS,
    RANGE_MAX_SECONDS,
    RANGE_MIN_SECONDS,
    RESOURCE_KINDS,
    buildSeries,
    defaultColumns,
    expectedBucketCount,
    formatAxisTick,
    formatBucketTime,
    formatDurationText,
    formatMetricValue,
    formatResolution,
    groupMetricColumns,
    isSparseData,
    kindLabelOf,
    metricLabel,
    metricUnit,
    rangeOptions,
    resourceRows,
    trendRows
  }
  export type { RangeChoice, RangeOption, SeriesBuild, SeriesFrame }
</script>

<script setup lang="ts">
  import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
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
    /**
     * 后端下发的离线判定阈值（秒）。
     *
     * 面板本身**不用**它做判定（趋势的稀疏/空洞由 buckets 直接反映），
     * 仅在 tooltip 里解释「多久没上报算离线」时引用，保持与详情页同口径。
     */
    offlineThresholdSec?: number
  }>()

  const emit = defineEmits<{
    /**
     * 最新采样点（F-7）。
     *
     * 详情页用它把「9.9/16 GB」这类绝对量回填到水位卡 —— 这些值本来就在
     * 趋势桶里（mem_used_mb / mem_total_mb / disk_used_gb / disk_total_gb），
     * 复用同一个响应的**最后一个非空桶**即可，不需要任何额外接口。
     */
    latest: [
      payload: {
        memUsedMb?: number | null
        memTotalMb?: number | null
        diskUsedGb?: number | null
        diskTotalGb?: number | null
      }
    ]
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

  /**
   * 「该档位无此指标」里的中文列名列表。
   *
   * 保留列名在括号里（`内存使用率(mem_used_percent)`）是刻意的：这条提示的
   * 读者通常是要去查后端列清单或对日志的开发者，只给中文会让他找不到对应
   * 的字段名；只给英文又会让纯使用者看不懂。
   */
  const unavailableLabels = computed(() =>
    built.value.unavailableColumns
      .map((c) => (metricLabel(c) === c ? c : `${metricLabel(c)}(${c})`))
      .join('、')
  )

  /** 分组后的可选列（F-8）：仅对「可用列」分组，未登记列会在 chips 里单独出现。 */
  const groupedColumns = computed(() => groupMetricColumns(availableColumns.value))

  /** 头部摘要（人话，F-3）：粒度 · 数据源 · 采样点数。 */
  const metaSummary = computed(() => {
    const m = meta.value
    if (!m) return ''
    const src = m.source === 'redis' ? '热层' : '历史表'
    return `粒度 ${formatResolution(m.resolutionSeconds)} · 数据源 ${src} · ${rows.value.length} 个采样点`
  })

  /** 数据是否稀疏（F-3）：让「断线图」有解释，而不是让人以为系统坏了。 */
  const isSparse = computed(() => {
    const m = meta.value
    if (!m) return false
    return isSparseData(rows.value.length, m.rangeSeconds, m.resolutionSeconds)
  })

  /** chip 的 tooltip：区分「未采集」与「该档位无此列」。 */
  function chipTitle(column: string): string {
    const name = metricLabel(column)
    const unit = metricUnit(column)
    const head = unit ? `${name}（${unit}）` : name
    if (!availableColumns.value.includes(column)) {
      return `${head}：该档位不产出此指标（列不在本响应的 available_metrics 内）`
    }
    return `${head}：点击取消显示`
  }

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
            // seriesName 是列名（系列标识）；展示走中文名 + 单位。
            return metricTipLine(p.seriesName ?? '', formatMetricValue(v))
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

  /**
   * 导出当前已选列为 CSV（F-18）。
   *
   * 只用**已经拿到的** rows，不重新请求：导出的必须与屏幕上是同一份数据，
   * 否则用户会拿到一份与所见不一致的文件（而且没有任何提示）。
   *
   * 列头用中文名 + 原始列名（`CPU 使用率(cpu_used_percent)`）：中文便于人读，
   * 原始名便于再导入/对日志。时间列导出为 `yyyy-MM-dd HH:mm:ss`。
   */
  function exportCsv() {
    if (!rows.value.length) return
    const cols = selectedColumns.value.filter((c) => c !== BUCKET_TS_COLUMN)
    if (!cols.length) return
    const header = ['时间', ...cols.map((c) => `${metricLabel(c)}(${c})`)]
    const lines = [header.join(',')]
    for (const row of rows.value) {
      const t = row[BUCKET_TS_COLUMN]
      const cells = [
        csvCell(typeof t === 'number' ? formatBucketTime(t) : ''),
        ...cols.map((c) => {
          const v = cellOf(row, c)
          return csvCell(v === null || v === undefined ? '' : String(v))
        })
      ]
      lines.push(cells.join(','))
    }
    // BOM：Excel 打开 UTF-8 CSV 不乱码（缺了它中文列头会变乱码）。
    const blob = new Blob(['\ufeff' + lines.join('\n')], { type: 'text/csv;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    const stamp = new Date().toISOString().slice(0, 16).replace(/[-:T]/g, '')
    a.href = url
    a.download = `device-metrics-${props.kind || 'machine'}-${props.name || ''}-${stamp}.csv`
    a.click()
    URL.revokeObjectURL(url)
  }

  /** CSV 单元格转义：含逗号/引号/换行时用双引号包裹（RFC 4180）。 */
  function csvCell(v: string): string {
    return /[",\n]/.test(v) ? `"${v.replace(/"/g, '""')}"` : v
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
    emitLatestSample()
  }

  /**
   * 把最新（最后一个）桶里的绝对量回传给父组件（F-7）。
   *
   * 只取**最后一个桶**：桶是稀疏的，最后一个桶就是最新的采样点。
   * 各列**分别**向前回溯找最近一个有值的桶（`findLastFilled`）—— 因为
   * mem_used_mb 与 mem_total_mb 未必出现在同一个桶里（缺列不写 key），
   * 只看最后一个桶会让本可显示的值变成空。
   */
  function emitLatestSample() {
    // 内存总量**不能**取 mem_total_mb：该列不在整机宽表的产出清单里
    // （见 available_metrics 实测：有 mem_used_mb / mem_available_mb，
    //  但没有 mem_total_mb）。总量由 已用 + 可用 推出 —— 这两个都在清单内，
    // 且语义上 available 是「还能给新进程用的量」，两者相加即总容量。
    const memUsed = findLastFilled('mem_used_mb')
    const memAvail = findLastFilled('mem_available_mb')
    emit('latest', {
      memUsedMb: memUsed,
      memTotalMb: memUsed !== null && memAvail !== null ? memUsed + memAvail : null,
      diskUsedGb: findLastFilled('disk_used_gb'),
      diskTotalGb: findLastFilled('disk_total_gb')
    })
  }

  /** 从最新桶往前找该列最近一次有值的样本。 */
  function findLastFilled(column: string): number | null {
    for (let i = rows.value.length - 1; i >= 0; i -= 1) {
      const v = cellOf(rows.value[i], column)
      if (v !== null) return v
    }
    return null
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

  /**
   * 面板滚入视口时把当前 option 应用于 canvas。
   *
   * 与 overview-chart-card.vue 同一个原因：useChart 对首屏之外的容器懒初始化，
   * 只 echarts.init() 并派发 chartVisible，**应用 option 是消费方责任**
   * （useChartComponent 就是靠注册该事件完成的）。详情页的图表区在首屏之下，
   * 漏听会让它变成「有标题、无 canvas」的空白卡片。
   */
  const onChartVisible = () => renderChart()

  onMounted(() => {
    load()
    chartRef.value?.addEventListener('chartVisible', onChartVisible)
  })

  onBeforeUnmount(() => {
    chartRef.value?.removeEventListener('chartVisible', onChartVisible)
  })
</script>

<style lang="scss" scoped>
  /* 与设备模块其余页面共用同一套令牌 */
  @use '../views/device-tokens' as t;

  .mp-panel {
    --mp-gap: 12px;
    /* 自带卡片外观：本组件可被独立复用，不依赖父级 .dd-surface 规则
       （scoped 样式不会跨组件传递，父级类名只在父模板的 DOM 上生效）。 */
    @include t.card;
    @include t.rise;
  }

  .dark .mp-panel {
    @include t.card-dark;
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
      font-size: 14px;
      font-weight: 600;
      color: var(--el-text-color-primary);
    }

    &__meta {
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      gap: 6px;
      margin-left: auto;
    }

    /* 数据口径摘要 → 胶囊标签（对齐服务监控 .sv-summary-pill）。
       此前该 span 没有任何样式规则，直接以正文 16px 呈现，
       在 14px 的标题旁边显得比标题还重，视觉层级是反的。 */
    &__summary {
      @include t.pill;
      font-variant-numeric: tabular-nums;
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
