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
  // 纯逻辑已原样抽到 `../utils/metrics`（Task 5）。这里**必须**用普通 import（而非
  // `export … from` 中转）：经 SFC 实测，普通块的 import 会注册为模板可见绑定，
  // 而纯再导出不会（`@vue/compiler-sfc` 的 bindings 里根本没有它们）→ 会编译报错。
  import {
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
    formatAxisTick,
    formatBucketTime,
    formatDurationText,
    formatMetricValue,
    formatResolution,
    kindLabelOf,
    rangeOptions,
    resourceRows,
    trendRows
  } from '../utils/metrics'
  import type { RangeChoice, RangeOption, SeriesBuild, SeriesFrame } from '../utils/metrics'

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
    formatAxisTick,
    formatBucketTime,
    formatDurationText,
    formatMetricValue,
    formatResolution,
    kindLabelOf,
    rangeOptions,
    resourceRows,
    trendRows
  }
  export type { RangeChoice, RangeOption, SeriesBuild, SeriesFrame }
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
