<template>
  <div class="oc-card do-card">
    <!-- ============ 头部：标题 + 用途 + 选型理由 ============ -->
    <div class="oc-card__head">
      <div class="oc-card__title">
        <span class="oc-card__name">{{ chart.title }}</span>
        <!-- 覆盖率：让读者知道这张图代表了几台设备。
             「3 / 5 台」比「画了 3 条线」更有信息量 —— 后者无法判断
             是只有 3 台设备，还是另外 2 台没数据。 -->
        <ElTag
          v-if="chart.devicesWithData < chart.devicesTotal"
          size="small"
          type="warning"
          effect="light"
        >
          {{ chart.devicesWithData }} / {{ chart.devicesTotal }} 台有数据
        </ElTag>
        <ElTag v-else size="small" type="info" effect="plain">{{ chart.devicesTotal }} 台</ElTag>
      </div>

      <div class="oc-card__tools">
        <!-- 选型理由：不把设计决策藏在代码里，放出来让使用者能质疑它。 -->
        <ElTooltip placement="top" :show-after="200">
          <template #content>
            <div class="oc-tip">
              <div class="oc-tip__line">用途：{{ chart.purpose }}</div>
              <div class="oc-tip__line">选型：{{ chart.rationale }}</div>
              <div class="oc-tip__line oc-tip__line--dim">
                指标：{{ columnLabels }}（共 {{ chart.columns.length }} 列）
              </div>
            </div>
          </template>
          <ArtSvgIcon class="oc-card__info" icon="ri:question-line" />
        </ElTooltip>
      </div>
    </div>

    <!-- ============ 图体 ============ -->
    <!-- useChartPlus 的 isEmpty 会自己渲染空状态浮层；但本页的空状态需要
         **说清楚是哪一种空**（该设备没数据 / 该时间窗没数据），故这里显式
         用 ElEmpty 覆盖，并把 hasData 作为它的判据。 -->
    <div class="oc-card__chart" :style="{ height: chartHeight }">
      <div ref="chartRef" class="oc-card__canvas"></div>
      <div v-if="!hasData" class="oc-card__empty">
        <ElEmpty :description="emptyText" :image-size="60" />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
  import { computed, onBeforeUnmount, onMounted, watch } from 'vue'

  import type { EChartsOption } from '@/plugins/echarts'
  import { useChart, useChartOps } from '@/hooks/core/useChart'

  import { metricLabel, metricUnit } from '../utils/column-meta'
  import { EMPTY_TEXT, formatBytesPerSec, formatPercent } from '../utils/display'
  import type { OverviewChart } from '../utils/overview'

  defineOptions({ name: 'DeviceOverviewChartCard' })

  const props = defineProps<{
    chart: OverviewChart
    /** 共享时间轴（unix 秒）——由页面下发，保证所有图块横轴完全一致。 */
    axis: number[]
    /** 时间窗（秒），用于 Tooltip 里显示桶宽与窗口说明。 */
    rangeSeconds: number
    /** 桶宽（秒）。 */
    resolutionSeconds: number
    /**
     * 高亮/淡出依据：为空数组表示「不筛选」（全部同等显示）；
     * 非空时不在集合内的设备会被淡出，用于「点选对比」时突出子集。
     */
    highlightedDeviceIds?: string[]
  }>()

  const emit = defineEmits<{
    /** 点击某条曲线 → 由页面决定是筛选该设备还是跳转详情。 */
    (e: 'pick-device', deviceId: string): void
  }>()

  // 主题色板取自项目既有的 useChartOps（与其它图表同源，避免本页自成一套配色）。
  const ops = useChartOps()

  // 图表高度按展示类型区分：柱状图需要给 X 轴标签留更多空间
  // （设备名可能较长），折线/面积可以更紧凑。
  const chartHeight = computed(() => (isBarLike.value ? '18rem' : '16rem'))
  const isBarLike = computed(() => props.chart.type === 'bar' || props.chart.type === 'stacked')

  const hasData = computed(() => props.chart.series.length > 0)

  const emptyText = computed(() =>
    props.chart.devicesTotal === 0 ? '暂无设备' : '所选设备在该时间窗内无此指标数据'
  )

  /** 指标中文名清单（用于头部提示）。 */
  const columnLabels = computed(() => props.chart.columns.map((c) => metricLabel(c)).join('、'))

  /**
   * 固定配色：按**系列名**（主机名 · 指标）稳定取色，而不是按系列下标。
   *
   * 为什么不能按下标：多台设备的系列顺序会随「哪台先返回数据」变化，
   * 按下标取色会让同一台机器的颜色在两次刷新之间跳变 —— 用户会以为
   * 换了设备在看。按名字取色则颜色与设备身份绑定，刷新后颜色不变。
   *
   * 色板取自 settingStore 的主题色以与全局主题一致，末尾几个点缀色用于
   * 设备数超出主题色数量时的补充（循环使用）。
   */
  const palette = computed<string[]>(() => {
    // 基础色来自全局主题（与其它图表一致）；其后补足可辨识的扩展色 ——
    // 总览常同时画 5 台以上设备，只有主题的 5~7 色不够区分。
    const base = ops.colors ?? []
    return [
      ...base,
      '#7C4DFF',
      '#FF6D00',
      '#00B8D4',
      '#C2185B',
      '#43A047',
      '#8D6E63',
      '#546E7A',
      '#F4511E'
    ]
  })

  const colorOf = (name: string): string => {
    // 简单稳定散列（djb2 变体）：同一名字恒得同色，不同名字分散良好。
    let h = 5381
    for (let i = 0; i < name.length; i++) {
      h = ((h << 5) + h + name.charCodeAt(i)) | 0
    }
    const colors = palette.value
    return colors[Math.abs(h) % colors.length]
  }

  /**
   * 该图块的数值格式化器。
   *
   * 一个图块可能含多列（如「磁盘 IO」的读与写），故格式化按**列**决定而不是
   * 按图块决定：读速率与写速率同为 B/s 没问题，但若将来把 % 与 B/s 放进同块，
   * 按图块格式化会把百分比也写成「1.5 KB/s」。系列名里带得出列名（见
   * nameOfSeries），故我们从 figure 里反查列。
   */
  const seriesColumn = new Map<string, string>()
  const fmt = (seriesName: string, v: unknown): string => {
    if (v === null || v === undefined) return EMPTY_TEXT
    const num = typeof v === 'number' ? v : Number(v)
    if (!Number.isFinite(num)) return EMPTY_TEXT
    const col = seriesColumn.get(seriesName)
    const unit = col ? metricUnit(col) : ''
    if (unit === '%') return formatPercent(num)
    if (unit === 'B/s') return formatBytesPerSec(num)
    if (unit === 'GB') return `${num} GB`
    if (unit === 'MB') return `${num} MB`
    if (unit === '°C') return `${num} °C`
    return String(num)
  }

  /**
   * 构建 ECharts option。
   *
   * 关键决策与理由：
   *   - `connectNulls: false`：桶缺数据时**断线**。若连起来，读者会以为
   *     中间那段是平滑过渡的真实值；
   *   - `showSymbol: false` + `sampling: 'lttb'`：长窗口（180 天）下点数可达
   *     数千，逐点画圆点会拖慢渲染且遮住曲线；lttb 抽样保留极值形状；
   *   - X 轴 `type: 'time'`：轴是真实时间（毫秒），不是等距分类 ——
   *     用 category 轴会在「桶边界不规则」时说谎；
   *   - `animation: false`：总览页有十余张图，同时播放动画会让整页卡顿，
   *     且动画期间无法读数。
   */
  const buildOption = (): EChartsOption => {
    seriesColumn.clear()

    // @/plugins/echarts 没有导出通用的 SeriesOption（只有 EChartsOption 与
    // BarSeriesOption），故这里用 EChartsOption['series'] 的数组元素类型。
    // 直接写 SeriesItem[] 而不加类型断言，让「构造出的系列形状不合法」
    // 在编译期就暴露，而不是等到运行期 ECharts 静默忽略。
    const series = props.chart.series.map((s) => {
      seriesColumn.set(s.name, s.metric)
      const dimmed =
        props.highlightedDeviceIds !== undefined &&
        props.highlightedDeviceIds.length > 0 &&
        !props.highlightedDeviceIds.includes(s.deviceId)

      const common = {
        name: s.name,
        // connectNulls:false —— 缺桶断线而不是连成直线
        connectNulls: false,
        showSymbol: false,
        sampling: 'lttb' as const,
        // 淡出：降透明度 + 变细，而不是隐藏 —— 用户仍能看到全貌，
        // 只是被选中的那几台更突出（隐藏会让「对比」失去参照）。
        lineStyle: dimmed
          ? { width: 1, opacity: 0.18 }
          : { width: 1.6, color: colorOf(s.name) },
        itemStyle: { color: colorOf(s.name), opacity: dimmed ? 0.18 : 1 },
        emphasis: { focus: 'series' as const }
      }

      if (props.chart.type === 'bar' || props.chart.type === 'stacked') {
        return { ...common, type: 'bar' as const, stack: 'total', barMaxWidth: 32, data: s.points }
      }
      if (props.chart.type === 'area') {
        return {
          ...common,
          type: 'line' as const,
          areaStyle: { opacity: dimmed ? 0.05 : 0.18, color: colorOf(s.name) },
          data: s.points
        }
      }
      // line 与 ring：环形在总览里按折线处理是不对的，故 ring 走 pie 分支
      // （下方单独 return，不从这条路径出去）。
      return { ...common, type: 'line' as const, data: s.points }
    })

    return {
      animation: false,
      color: palette.value,
      // bottom 留出图例高度（legend 在 bottom:0，高 28）+ X 轴标签空间。
      grid: { top: 24, right: 16, bottom: 44, left: 8, containLabel: true },
      tooltip: {
        trigger: 'axis',
        confine: true,
        // 用 formatter（而不是 valueFormatter）才能拿到 seriesName —— 单位
        // 必须按**列**决定，同一图块可能含量纲不同的列。
        formatter: (params) => renderTooltip(params),
        axisPointer: { type: 'line', snap: true }
      },
      legend: {
        type: 'scroll',
        bottom: 0,
        // 设备多时图例会很长：小字号 + 固定行高，避免挤压绘图区。
        // 实测 2 台设备时单行已接近图宽（主机名 + 指标名较长），
        // 故这里把 legend 的高度也钉住，防止多行图例把 grid 顶上去。
        textStyle: { fontSize: 11 },
        itemWidth: 12,
        itemHeight: 8,
        itemGap: 10,
        // 图例超宽时换行（而不是悄悄裁掉）——被裁掉的图例意味着
        // 「有设备在图上但你不知道是哪台」。
        orient: 'horizontal',
        left: 'center',
        padding: 0,
        // 显式给图例区域高度：多行时 ECharts 会自己撑开，配合 grid.bottom 留白。
        height: 28
      },
      xAxis: {
        type: 'time',
        axisLabel: { hideOverlap: true, fontSize: 11 },
        splitLine: { show: false }
      },
      yAxis: {
        type: 'value',
        // scale:false —— 从 0 起。总览是「横向对比」场景，截断 Y 轴基准
        // 会放大微小差异（20% 与 22% 看起来天差地别），误导读者。
        scale: false,
        axisLabel: { fontSize: 11 }
      },
      series
    } satisfies EChartsOption
  }

  /**
   * 自绘 tooltip 内容。
   *
   * 不用 `valueFormatter` 的原因是它的签名只有 value、拿不到 seriesName，
   * 而本页的单位必须按**列**决定。自绘还能顺带控制信息密度：一行一台设备，
   * 名字在前、数值在后（右对齐），比默认的多行堆叠更易横向扫读。
   */
  const renderTooltip = (params: unknown): string => {
    const arr = Array.isArray(params) ? params : [params]
    if (arr.length === 0) return ''
    const rows = arr
      .filter((p) => p && typeof p === 'object')
      .map((p) => {
        const item = p as { seriesName?: string; value?: unknown; color?: string }
        const name = item.seriesName ?? ''
        // value 在 time 轴上是 [时间戳, 值]
        const raw = Array.isArray(item.value) ? item.value[1] : item.value
        const dot = item.color
          ? `<span style="display:inline-block;width:8px;height:8px;border-radius:50%;background:${item.color};margin-right:6px"></span>`
          : ''
        return `<div style="display:flex;align-items:center;gap:12px;justify-content:space-between">
          <span style="display:flex;align-items:center;min-width:0">${dot}${escapeHtml(name)}</span>
          <span style="font-weight:600">${escapeHtml(fmt(name, raw))}</span>
        </div>`
      })
    // 时间头：用第一个系列的时间（同轴同点，取首个即代表本次 hover 的时刻）。
    const first = arr[0] as { value?: unknown } | undefined
    const ts = Array.isArray(first?.value) ? (first?.value as number[])[0] : undefined
    const head = ts
      ? `<div style="margin-bottom:4px;font-size:12px;color:#999">${formatTooltipTime(ts)}</div>`
      : ''
    return head + rows.join('')
  }

  const option = computed(() => buildOption())

  /** 转义 HTML：系列名含主机名（用户可控），直接拼进 tooltip 会有注入风险。 */
  const escapeHtml = (s: string): string =>
    s.replace(/[&<>"']/g, (c) => {
      const map: Record<string, string> = {
        '&': '&amp;',
        '<': '&lt;',
        '>': '&gt;',
        '"': '&quot;',
        "'": '&#39;'
      }
      return map[c] ?? c
    })

  /** tooltip 头部时间：按当前档位的粒度决定是否显示秒。 */
  const formatTooltipTime = (ms: number): string => {
    const d = new Date(ms)
    const pad = (n: number) => String(n).padStart(2, '0')
    const date = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
    const time = `${pad(d.getHours())}:${pad(d.getMinutes())}`
    // 桶宽 ≥ 60s 时秒位没有信息量（同一桶内的秒都是桶起点），反而占宽度。
    return props.resolutionSeconds >= 60 ? `${date} ${time}` : `${date} ${time}:${pad(d.getSeconds())}`
  }

  const { chartRef, initChart, updateChart } = useChart({
    initOptions: { renderer: 'canvas' }
  })

  /**
   * 图块重新进入视口时把当前 option 真正应用到 canvas 上。
   *
   * 为什么必须有这个监听（曾经漏掉，构成一个静默缺陷）：
   * useChart 对「首屏之外」的容器走**懒初始化** —— 它只 echarts.init()，
   * 把 option 存进 pendingOptions，然后派发 chartVisible，再把
   * pendingOptions 置空。**应用 option 是消费方的责任**（useChartComponent
   * 就是靠注册这个事件来完成的，见 useChart.ts 的 setupLifecycle）。
   *
   * 漏听的后果不是报错，而是一张「有标题、有 echarts 实例、但没有 canvas」
   * 的空白卡片：总览页有 11 个图块，首屏只能容下 6 个，于是下半屏 5 个
   * 全部空白且没有任何提示 —— 用户看到的就是「下面统计图没有数据」。
   * 它也不会自愈，因为 option 再也不会被应用（只有 setOption 的显式调用
   * 才能补上，点击「查询」正是通过 updateChart 走到 setOption 才好起来的）。
   */
  const onChartVisible = () => updateChart(option.value, { replaceMerge: ['series'] })

  onMounted(() => {
    initChart(option.value, !hasData.value)
    chartRef.value?.addEventListener('chartVisible', onChartVisible)
  })

  onBeforeUnmount(() => {
    chartRef.value?.removeEventListener('chartVisible', onChartVisible)
  })

  watch(
    () => [props.chart, props.axis, props.highlightedDeviceIds],
    () => {
      // replaceMerge: ['series'] 是必须的：图块系列数量会随筛选变化
      // （筛掉设备后系列变少），默认的 merge 会**残留**上一次的多余系列，
      // 表现为「筛掉后那条线还在」。
      updateChart(option.value, { replaceMerge: ['series'] })
    },
    { deep: false }
  )

  // 点击曲线 → 上报设备 ID（页面决定语义：筛选或跳转）。
  const onPick = (seriesName: string) => {
    const s = props.chart.series.find((x) => x.name === seriesName)
    if (s) emit('pick-device', s.deviceId)
  }
  defineExpose({ onPick })
</script>

<style lang="scss" scoped>
  /* 与设备模块其余页面共用同一套令牌（数值同 monitor-tokens） */
  @use '../views/device-tokens' as t;

  .oc-card {
    /* 卡片外观来自共享令牌。这里**必须自己 @include t.card**：
       scoped 样式的作用域是「本组件的模板」，父组件的 .do-card 规则
       虽然能命中外层元素（因为 class 挂在根节点上、父级 scoped 属性也在），
       但本组件根节点同时带 oc-card 与 do-card 两个类，若依赖父级规则，
       组件被别处复用时外观就会静默丢失 —— 自带完整外观才可独立复用。

       不设 animation-delay：11 个图块同时入场，若加错峰延迟会让页面
       "排队出现"，反而显得卡顿（与服务监控 KPI 只有 6 个磁贴的场景不同）。 */
    @include t.card;
    @include t.rise;

    // 图块统一高度节奏：头部固定、图体由 chartHeight 决定。
    &__head {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 8px;
      margin-bottom: 8px;
    }

    &__title {
      display: flex;
      align-items: center;
      gap: 8px;
      min-width: 0;
    }

    &__name {
      font-size: 14px;
      font-weight: 600;
      color: var(--el-text-color-primary);
      white-space: nowrap;
    }

    &__info {
      color: var(--el-text-color-secondary);
      cursor: help;
      font-size: 15px;
    }

    &__chart {
      position: relative;
    }

    &__canvas {
      width: 100%;
      height: 100%;
    }

    &__empty {
      position: absolute;
      inset: 0;
      display: flex;
      align-items: center;
      justify-content: center;
      // 与 useChart 的空状态浮层同款半透明背景：让下面的网格线若隐若现，
      // 读者仍能看出「这是一张图，只是没数据」，而不是「页面缺了一块」。
      background: var(--el-bg-color-overlay);
      opacity: 0.92;
    }
  }

  .dark .oc-card {
    @include t.card-dark;
  }

  @include t.reduced-motion('.oc-card');

  .oc-tip {
    max-width: 320px;
    line-height: 1.6;

    &__line {
      &--dim {
        color: var(--el-text-color-secondary);
        font-size: 12px;
      }
    }
  }
</style>
