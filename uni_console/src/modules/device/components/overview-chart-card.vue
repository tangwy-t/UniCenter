<template>
  <div class="oc-card do-card">
    <!-- ============ 头部：标题 + 台数口径 + 指标说明 ============ -->
    <div class="oc-card__head">
      <div class="oc-card__title">
        <span class="oc-card__name">{{ chart.title }}</span>
        <!-- 台数口径：后端截断 → 「已显示 N / 总 M 台」；top-N 裁剪 → 单列
             「已显示」角标；有数据不足 → 「N / M 台有数据」。三种「数字比
             显示台数少」各有各的角标，不共用一个数 —— 共用会让「没数据」
             与「被裁掉」不可区分。 -->
        <ElTag v-if="grandTotal > chart.devicesTotal" size="small" type="warning" effect="light">
          已显示 {{ chart.devicesTotal }} / 总 {{ grandTotal }} 台
        </ElTag>
        <ElTag
          v-else-if="chart.devicesWithData < chart.devicesTotal"
          size="small"
          type="warning"
          effect="light"
        >
          {{ chart.devicesWithData }} / {{ chart.devicesTotal }} 台有数据
        </ElTag>
        <ElTag v-else size="small" type="info" effect="plain">{{ chart.devicesTotal }} 台</ElTag>
        <ElTag
          v-if="displayedDevices < chart.devicesWithData"
          size="small"
          type="warning"
          effect="plain"
          class="oc-card__topn"
        >
          已显示 {{ displayedDevices }} / {{ chart.devicesWithData }} 台
        </ElTag>
      </div>

      <div class="oc-card__tools">
        <!-- 只留「用途」与「指标」。图表选型的论证（为什么用折线而不是堆叠柱）
             留在 CHART_BLOCKS 的 rationale 里 —— 那是给维护者的论证，不是页面
             文案：读者是来看数据的人，评审设计不是他的任务，把两者混在一处
             只会让页面变成设计文档。 -->
        <ElTooltip placement="top" :show-after="200">
          <template #content>
            <div class="oc-tip">
              <div class="oc-tip__line">用途：{{ chart.purpose }}</div>
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
  import { storeToRefs } from 'pinia'
  import { useSettingStore } from '@/store/modules/setting'

  import type { EChartsOption } from '@/plugins/echarts'
  import { useChart, useChartOps } from '@/hooks/core/useChart'

  import { formatMetricText, metricLabel, metricUnit } from '../utils/column-meta'
  import { EMPTY_TEXT, formatByUnit, formatAxisTickByUnit } from '../utils/display'
  import { formatAxisTick } from '../utils/metrics'
  import { capacitySegments, COL, type OverviewChart, type OverviewSeries } from '../utils/overview'

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
    /**
     * 后端口径的设备总数（device_total，未截断）。后端截断（>max_devices）时
     * 与 devicesTotal（截断后数量）不等 → 头部角标如实报「已显示 N / 总 M 台」。
     * 缺省（undefined）时与 devicesTotal 同口径（无截断）。
     */
    devicesGrandTotal?: number
  }>()

  const emit = defineEmits<{
    /** 点击某条曲线/某根柱 → 页面决定去向（本页约定为跳转设备详情）。 */
    (e: 'pick-device', deviceId: string): void
  }>()

  // 主题：isDark 驱动色板重取（见 palette 的依赖声明注释）。
  const { isDark } = storeToRefs(useSettingStore())

  /** 存量图（stacked：每台一柱的磁盘容量对比）。 */
  const isSnapshotChart = computed(() => props.chart.type === 'stacked')

  /** 实际画出来的台数（角标「已显示 N 台」的 N）。 */
  const displayedDevices = computed(() =>
    isSnapshotChart.value
      ? props.chart.snapshot.length
      : new Set(props.chart.series.map((s) => s.deviceId)).size
  )

  /** 后端口径总数（缺省回落到本地图块总数 —— 无截断时两者恒等）。 */
  const grandTotal = computed(() => props.devicesGrandTotal ?? props.chart.devicesTotal)

  // 图表高度按展示类型区分：柱状图需要给 X 轴标签留更多空间
  // （设备名可能较长），折线/面积可以更紧凑。
  const chartHeight = computed(() =>
    isSnapshotChart.value ? '18rem' : isBarLike.value ? '18rem' : '16rem'
  )
  const isBarLike = computed(() => props.chart.type === 'bar' || props.chart.type === 'stacked')

  const hasData = computed(() =>
    isSnapshotChart.value ? props.chart.snapshot.length > 0 : props.chart.series.length > 0
  )

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
   * 色板来自 useChartOps（亮/暗两套）。注意 ops 是 setup 时的**一次性求值**
   * （普通对象，不是响应式），故这里显式依赖 isDark 重新调 useChartOps ——
   * 只读 ops.colors 的话 computed 不追踪主题，切深浅色后仍旧是旧色板
   * （这正是被修掉的那个「一次性快照」缺陷的回潮路径）。
   */
  const palette = computed<string[]>(() => {
    void isDark.value // 依赖声明：主题切换时本 computed 失效 → 重取色板
    return useChartOps().colors ?? []
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
   * 按图块决定：读速率与写速率同为 B/s 没问题，但若把 % 与 B/s 放进同块，
   * 按图块格式化会把百分比也写成「1.5 KB/s」。系列名里带得出列名（见
   * nameOfSeries），故我们从 figure 里反查列。
   */
  const seriesColumn = new Map<string, string>()
  const fmt = (seriesName: string, v: unknown): string => {
    if (v === null || v === undefined) return EMPTY_TEXT
    const num = typeof v === 'number' ? v : Number(v)
    if (!Number.isFinite(num)) return EMPTY_TEXT
    const col = seriesColumn.get(seriesName)
    // 单位换算走全站唯一映射（formatMetricText → display.formatByUnit）：
    // 此前这里自己 switch 了一遍，GB 写死成 `{num} GB`（1795 GB 而不是 1.75 TB），
    // 与详情页的进位规则不一致。
    return col ? formatMetricText(col, num) : formatByUnit('', num)
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
   *     用 category 轴会在「桶边界不规则」时说谎（存量图走 category 分支，
   *     那是「每台一柱」的语义要求，不是时间轴）；
   *   - 按量纲分 Y 轴（unitAxes）：混量纲块（% + MB、个 + 秒）各自缩放，
   *     不再共轴把小量纲压成贴 0 的直线；主量纲占左轴（带刻度与单位），
   *     其余轴 show:false —— 刻度互挤没有任何信息量，绝对值由 tooltip 承载；
   *   - `animation: false`：总览页有十余张图，同时播放动画会让整页卡顿，
   *     且动画期间无法读数。
   */
  const buildOption = (): EChartsOption => {
    seriesColumn.clear()
    if (isSnapshotChart.value) return buildSnapshotOption()

    // @/plugins/echarts 没有导出通用的 SeriesOption（只有 EChartsOption 与
    // BarSeriesOption），故这里用 EChartsOption['series'] 的数组元素类型。
    // 直接写 SeriesItem[] 而不加类型断言，让「构造出的系列形状不合法」
    // 在编译期就暴露，而不是等到运行期 ECharts 静默忽略。
    const axisIndexByUnit = new Map(props.chart.unitAxes.map((g, i) => [g.unit, i]))
    const series = props.chart.series.map((s) => {
      seriesColumn.set(s.name, s.metric)
      const dimmed =
        props.highlightedDeviceIds !== undefined &&
        props.highlightedDeviceIds.length > 0 &&
        !props.highlightedDeviceIds.includes(s.deviceId)

      // 按量纲挂轴（查不到时回到 0 / 主量纲 —— 绝不画出没有轴的线）。
      const yAxisIndex = axisIndexByUnit.get(metricUnit(s.metric)) ?? 0

      const common = {
        name: s.name,
        // connectNulls:false —— 缺桶断线而不是连成直线
        connectNulls: false,
        showSymbol: false,
        sampling: 'lttb' as const,
        yAxisIndex,
        // 淡出：降透明度 + 变细，而不是隐藏 —— 用户仍能看到全貌，
        // 只是被选中的那几台更突出（隐藏会让「对比」失去参照）。
        lineStyle: dimmed ? { width: 1, opacity: 0.18 } : { width: 1.6, color: colorOf(s.name) },
        itemStyle: { color: colorOf(s.name), opacity: dimmed ? 0.18 : 1 },
        emphasis: { focus: 'series' as const }
      }

      if (props.chart.type === 'bar') {
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

    // 主量纲（unitAxes[0]）占左轴：刻度带单位（`20%`、`743.4 KB/s`）。
    // 其余量纲 show:false —— 只给该量纲的线提供自适应量程（实测落点与
    // 可见轴逐字相同），不画刻度：多套刻度互相叠、且横向网格线只能有
    // 一根轴的语义，多套并存本身就是误导。绝对值由 tooltip 逐条带单位承载。
    const primaryUnit = props.chart.unitAxes[0]?.unit ?? ''
    const yAxis =
      props.chart.unitAxes.length > 1
        ? props.chart.unitAxes.map((group, i) =>
            i === 0
              ? {
                  type: 'value' as const,
                  // scale:false —— 从 0 起。总览是「横向对比」场景，截断 Y 轴基准
                  // 会放大微小差异（20% 与 22% 看起来天差地别），误导读者。
                  scale: false,
                  axisLabel: {
                    fontSize: 11,
                    formatter: (v: number) => formatAxisTickByUnit(group.unit, v)
                  }
                }
              : { type: 'value' as const, scale: false, show: false }
          )
        : {
            type: 'value' as const,
            // scale:false —— 从 0 起。总览是「横向对比」场景，截断 Y 轴基准
            // 会放大微小差异（20% 与 22% 看起来天差地别），误导读者。
            scale: false,
            axisLabel: {
              fontSize: 11,
              formatter: (v: number) => formatAxisTickByUnit(primaryUnit, v)
            }
          }

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
        // 单行 + 翻页是本页有意为之：图块只有 16rem 高，图例换行会直接压扁绘图区。
        // 注意 scroll 图例**不会**换行 —— 它把内容裁成一个窗口，放不下的条目
        // 挂在「1/2」翻页控件后面（见下方 itemHeight 的说明：这个窗口有代价）。
        type: 'scroll',
        bottom: 0,
        // 设备多时图例会很长：小字号 + 固定行高，避免挤压绘图区。
        textStyle: { fontSize: 11 },
        itemWidth: 12,
        // itemHeight 必须高于文字行高（11px），否则**汉字天头会被裁掉**：
        //
        // 滚动图例在内容超宽时会给内容套一个 clipPath，而这个裁剪窗口的上沿
        // 与条目包围盒上沿严格齐平（实测：窗口上沿 243.0 = 条目框上沿 243.0）。
        // 折线系列的图例标记尺寸是 itemHeight × 0.8（LineSeries.getLegendIcon），
        // 故 itemHeight 取 8 时条目框 == 文字框，裁剪线正好压在文字框上沿 ——
        // 汉字墨迹高出行框的那 1~2px 就被切掉，顶上一道没有反锯齿的硬边。
        // 16 让条目框变成 14.8px，文字框上沿因此比裁剪线低 1.9px（实测），天头完整。
        itemHeight: 16,
        itemGap: 10,
        orient: 'horizontal',
        left: 'center',
        padding: 0,
        // 显式给图例区域高度：翻页窗口的高度取它（而不是按文字行高算），
        // 与 grid.bottom 的 44px 一起分成「图例带 + X 轴标签」两段留白。
        height: 28
      },
      xAxis: {
        type: 'time',
        axisLabel: {
          hideOverlap: true,
          fontSize: 11,
          // 刻度文案与详情页同口径（formatAxisTick：按 range 决定到分/到日），
          // 不再显示原始毫秒大数。
          formatter: (value: number) => formatAxisTick(value, props.rangeSeconds)
        },
        splitLine: { show: false }
      },
      yAxis,
      series
    } satisfies EChartsOption
  }

  /**
   * 存量图（stacked）的 option：X 轴 category（按设备名），每台一柱、
   * 已用 / 可用两段堆叠（可用 = total − used）。
   *
   * 修复前：时间序列点被塞进 stack:'total' 的柱系列、X 轴还是 time ——
   * 渲染出的是一根随时间漂移的无意义「总量柱」，与声明里「每台一柱、
   * 两段」的语义完全不符。
   */
  const buildSnapshotOption = (): EChartsOption => {
    // 两段固定语义色：已用 = 主色（紧张度由快照表进度条承载，这里只表构成），
    // 可用 = 中性填充（残余量不是「数据」，是柱子的另一半）。两段的区分靠
    // 图例文字与堆叠缝隙，不靠色相对比 —— 色弱读者不依赖颜色也能读。
    const usedColor = palette.value[0] ?? '#3B82F6'
    const freeColor = isDark.value ? '#3a3a42' : '#dcdfe6'
    const surfaceColor = isDark.value ? '#161618' : '#ffffff'
    seriesColumn.set('已用', COL.diskUsedGB)
    seriesColumn.set('可用', COL.diskTotalGB)

    // 缺一段（total 或 used 缺失 / total < used 翻转）的设备不画柱 ——
    // 残缺两段会让柱高失去「总容量」语义，且与「缺值不画条」的全站纪律一致。
    const rows = props.chart.snapshot
      .map((row) => ({ row, seg: capacitySegments(row) }))
      .filter((x) => x.seg !== null)

    const series = [
      {
        name: '已用',
        type: 'bar' as const,
        stack: 'capacity',
        barMaxWidth: 24,
        // 2px 表面色描边 = 堆叠段之间的缝隙（描边在段边缘、与背景同色）：
        // 分隔靠缝隙不靠深色描边 —— 深描边是「数据之外的墨」。
        itemStyle: {
          color: usedColor,
          borderColor: surfaceColor,
          borderWidth: 1
        },
        data: rows.map(({ row, seg }) => ({
          value: seg!.used,
          deviceId: row.deviceId
        }))
      },
      {
        name: '可用',
        type: 'bar' as const,
        stack: 'capacity',
        barMaxWidth: 24,
        itemStyle: {
          color: freeColor,
          borderColor: surfaceColor,
          borderWidth: 1
        },
        data: rows.map(({ row, seg }) => ({
          value: seg!.free,
          deviceId: row.deviceId
        }))
      }
    ]

    return {
      animation: false,
      grid: { top: 24, right: 16, bottom: 44, left: 8, containLabel: true },
      tooltip: {
        trigger: 'axis',
        confine: true,
        formatter: (params) => renderTooltip(params)
      },
      legend: {
        type: 'scroll',
        bottom: 0,
        textStyle: { fontSize: 11 },
        itemWidth: 12,
        // 同折线图例的天头余量守卫（见上方注释）。
        itemHeight: 16,
        itemGap: 10,
        orient: 'horizontal',
        left: 'center',
        padding: 0,
        height: 28
      },
      xAxis: {
        type: 'category',
        data: rows.map(({ row }) => row.hostname),
        axisLabel: { hideOverlap: true, fontSize: 11, interval: 0 },
        splitLine: { show: false }
      },
      yAxis: {
        type: 'value',
        scale: false,
        axisLabel: {
          fontSize: 11,
          // 容量刻度带单位（`500 GB` / `1.75 TB`），与 tooltip 同口径。
          formatter: (v: number) => formatAxisTickByUnit('GB', v)
        }
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
   *
   * 规模控制：只渲染**当前值降序的前 10 行** + 「其余 N 台」折叠一行。
   * 100 台 × 2 列 = 200 行的 tooltip 没有任何可读性，只有渲染开销。
   */
  const TOOLTIP_MAX_ROWS = 10
  const renderTooltip = (params: unknown): string => {
    const arr = Array.isArray(params) ? params : [params]
    if (arr.length === 0) return ''
    const items = arr
      .filter((p) => p && typeof p === 'object')
      .map((p) => {
        const item = p as { seriesName?: string; value?: unknown; color?: string }
        const name = item.seriesName ?? ''
        // value 在 time 轴上是 [时间戳, 值]；category 轴上可能是 [idx, 值] 或纯值
        const raw = Array.isArray(item.value) ? item.value[1] : item.value
        return { name, raw, color: item.color ?? '' }
      })
      // 按当前值降序取前 N：hover 的任务是「谁此刻最高」，降序正是读的顺序。
      .sort((a, b) => {
        const av = typeof a.raw === 'number' ? a.raw : Number.NEGATIVE_INFINITY
        const bv = typeof b.raw === 'number' ? b.raw : Number.NEGATIVE_INFINITY
        return bv - av
      })

    const shown = items.slice(0, TOOLTIP_MAX_ROWS)
    const rest = items.length - shown.length
    const rows = shown
      .map(({ name, raw, color }) => {
        const dot = color
          ? `<span style="display:inline-block;width:8px;height:8px;border-radius:50%;background:${color};margin-right:6px"></span>`
          : ''
        return `<div style="display:flex;align-items:center;gap:12px;justify-content:space-between">
          <span style="display:flex;align-items:center;min-width:0">${dot}${escapeHtml(name)}</span>
          <span style="font-weight:600;white-space:nowrap;flex:none">${escapeHtml(fmt(name, raw))}</span>
        </div>`
      })
      .join('')
    const fold =
      rest > 0 ? `<div style="margin-top:2px;font-size:11px;color:#999">其余 ${rest} 台…</div>` : ''

    // 时间头：用第一个系列的时间（同轴同点，取首个即代表本次 hover 的时刻）。
    // 存量图（category 轴）没有时间头 —— 头一行换成设备名。
    if (isSnapshotChart.value) {
      const first = arr[0] as { name?: string } | undefined
      const head = first?.name
        ? `<div style="margin-bottom:4px;font-weight:600">${escapeHtml(first.name)}</div>`
        : ''
      return head + rows + fold
    }
    const first = arr[0] as { value?: unknown } | undefined
    const ts = Array.isArray(first?.value) ? (first?.value as number[])[0] : undefined
    const head = ts
      ? `<div style="margin-bottom:4px;font-size:12px;color:#999">${formatTooltipTime(ts)}</div>`
      : ''
    return head + rows + fold
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
    return props.resolutionSeconds >= 60
      ? `${date} ${time}`
      : `${date} ${time}:${pad(d.getSeconds())}`
  }

  const { chartRef, initChart, updateChart, getChartInstance } = useChart({
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

  // 点击曲线/柱 → 上报设备 ID（页面语义：跳转该设备详情）。
  // 折线图按系列名反查 deviceId；存量柱的 data 项直接带 deviceId。
  const onChartClick = (params: unknown) => {
    const p = params as { seriesName?: string; data?: unknown }
    if (typeof p?.data === 'object' && p.data !== null && 'deviceId' in (p.data as object)) {
      emit('pick-device', (p.data as { deviceId: string }).deviceId)
      return
    }
    const name = p?.seriesName
    if (!name) return
    const s: OverviewSeries | undefined = props.chart.series.find((x) => x.name === name)
    if (s) emit('pick-device', s.deviceId)
  }

  const bindClick = () => {
    const chart = getChartInstance()
    chart?.off('click', onChartClick)
    chart?.on('click', onChartClick)
  }

  onMounted(() => {
    initChart(option.value, !hasData.value)
    bindClick()
    chartRef.value?.addEventListener('chartVisible', onChartVisible)
  })

  onBeforeUnmount(() => {
    getChartInstance()?.off('click', onChartClick)
    chartRef.value?.removeEventListener('chartVisible', onChartVisible)
  })

  watch(
    () => [props.chart, props.axis, props.highlightedDeviceIds],
    () => {
      // replaceMerge: ['series'] 是必须的：图块系列数量会随筛选变化
      // （筛掉设备后系列变少），默认的 merge 会**残留**上一次的多余系列，
      // 表现为「筛掉后那条线还在」。
      updateChart(option.value, { replaceMerge: ['series'] })
      bindClick()
    },
    { deep: false }
  )

  // 主题切换：色板重取（ops.colors 已按亮/暗两套切换），重画并重挂点击。
  watch(isDark, () => {
    updateChart(option.value, { replaceMerge: ['series'] })
    bindClick()
  })
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

    /* top-N 裁剪角标：与「N 台有数据」并排但视觉更轻（plain + warning）——
       它回答的是另一个问题（被裁掉多少台），不应与覆盖率告警等重。 */
    &__topn {
      flex: none;
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
