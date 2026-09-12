<!-- 折线图，支持多组数据，支持阶梯式动画效果 -->
<template>
  <div
    ref="chartRef"
    class="relative w-[calc(100%+10px)]"
    :style="{ height: props.height }"
    v-loading="props.loading"
  >
  </div>
</template>

<script setup lang="ts">
  import { graphic, type EChartsOption } from '@/plugins/echarts'
  import type { SetOptionOpts } from 'echarts/core'
  import { getCssVar, hexToRgba } from '@/utils/ui'
  import { useChartOps, useChartComponent } from '@/hooks/core/useChart'
  import type { LineChartProps, LineDataItem } from '@/types/component/chart'

  defineOptions({ name: 'ArtLineChart' })

  const props = withDefaults(defineProps<LineChartProps>(), {
    // 基础配置
    height: useChartOps().chartHeight,
    loading: false,
    isEmpty: false,
    colors: () => useChartOps().colors,

    // 数据配置
    data: () => [0, 0, 0, 0, 0, 0, 0],
    xAxisData: () => [],
    lineWidth: 2.5,
    showAreaColor: false,
    smooth: true,
    symbol: 'none',
    symbolSize: 6,
    animationDelay: 200,
    live: false,

    // 轴线显示配置
    showAxisLabel: true,
    showAxisLine: true,
    showSplitLine: true,

    // 交互配置
    showTooltip: true,
    showLegend: false,
    legendPosition: 'bottom',

    // 缩放配置（inside 型 dataZoom）
    showDataZoom: false,
    dataZoomStart: 0,
    dataZoomEnd: 100
  })

  // 动画状态管理
  const isAnimating = ref(false)
  const animationTimers = ref<number[]>([])
  const animatedData = ref<number[] | LineDataItem[]>([])

  // 系列替换策略:仅在系列列表(名称集合)变化时才 replaceMerge——
  // 这样被移除的系列会被真正清掉(chips 增删指标时旧折线必须消失,
  // 默认合并模式不会删除旧系列);而日常同系列的定时刷新保持合并模式,
  // 走 update 过渡动画,保留"新数据自右端滑入、旧点左移"的滚动特效。
  const lastSeriesKey = ref('')
  const seriesKeyOf = (): string => {
    if (!isMultipleData.value) return 'single'
    return (props.data as LineDataItem[]).map((item) => item.name ?? '').join('\u0001')
  }
  const resolveSetOptionOpts = (): SetOptionOpts | undefined => {
    const key = seriesKeyOf()
    if (key === lastSeriesKey.value) return undefined
    lastSeriesKey.value = key
    return { replaceMerge: ['series'] }
  }
  /** live 增量模式：首次仍播开场动画，之后数据更新只做平滑过渡，不清零重播 */
  const hasRendered = ref(false)
  /** 当前数据窗口长度：live 模式下长度变化(如切换时间范围)视为新图表，重播开场动画 */
  const seriesLen = ref(0)

  // 清理所有定时器
  const clearAnimationTimers = () => {
    animationTimers.value.forEach((timer) => clearTimeout(timer))
    animationTimers.value = []
  }

  // 判断是否为多数据（使用 VueUse 的 computedEager 优化）
  const isMultipleData = computed(() => {
    return (
      Array.isArray(props.data) &&
      props.data.length > 0 &&
      typeof props.data[0] === 'object' &&
      'name' in props.data[0]
    )
  })

  // 缓存计算的最大值，避免重复计算
  const maxValue = computed(() => {
    if (isMultipleData.value) {
      const multiData = props.data as LineDataItem[]
      return multiData.reduce((max, item) => {
        if (item.data?.length) {
          const itemMax = Math.max(...item.data)
          return Math.max(max, itemMax)
        }
        return max
      }, 0)
    } else {
      const singleData = props.data as number[]
      return singleData?.length ? Math.max(...singleData) : 0
    }
  })

  // 初始化动画数据（优化：减少条件判断）
  const initAnimationData = (): number[] | LineDataItem[] => {
    if (isMultipleData.value) {
      const multiData = props.data as LineDataItem[]
      return multiData.map((item) => ({
        ...item,
        data: Array(item.data.length).fill(0)
      }))
    }
    const singleData = props.data as number[]
    return Array(singleData.length).fill(0)
  }

  // 复制真实数据（优化：使用结构化克隆）
  const copyRealData = (): number[] | LineDataItem[] => {
    if (isMultipleData.value) {
      return (props.data as LineDataItem[]).map((item) => ({ ...item, data: [...item.data] }))
    }
    return [...(props.data as number[])]
  }

  // 获取颜色配置（优化：缓存主题色）
  const primaryColor = computed(() => getCssVar('--el-color-primary'))

  const getColor = (customColor?: string, index?: number): string => {
    if (customColor) return customColor
    if (index !== undefined) return props.colors![index % props.colors!.length]
    return primaryColor.value
  }

  // 生成区域样式
  const generateAreaStyle = (item: LineDataItem, color: string) => {
    // 如果有 areaStyle 配置，或者显式开启了区域颜色，则显示区域样式
    if (!item.areaStyle && !item.showAreaColor && !props.showAreaColor) return undefined

    const areaConfig = item.areaStyle || {}
    if (areaConfig.custom) return areaConfig.custom

    return {
      color: new graphic.LinearGradient(0, 0, 0, 1, [
        {
          offset: 0,
          color: hexToRgba(color, areaConfig.startOpacity || 0.2).rgba
        },
        {
          offset: 1,
          color: hexToRgba(color, areaConfig.endOpacity || 0.02).rgba
        }
      ])
    }
  }

  // 生成单数据区域样式
  const generateSingleAreaStyle = () => {
    if (!props.showAreaColor) return undefined

    const color = getColor(props.colors[0])
    return {
      color: new graphic.LinearGradient(0, 0, 0, 1, [
        {
          offset: 0,
          color: hexToRgba(color, 0.2).rgba
        },
        {
          offset: 1,
          color: hexToRgba(color, 0.02).rgba
        }
      ])
    }
  }

  // 创建系列配置
  const createSeriesItem = (config: {
    name?: string
    data: number[]
    color?: string
    smooth?: boolean
    symbol?: string
    symbolSize?: number
    lineWidth?: number
    areaStyle?: any
  }) => {
    return {
      name: config.name,
      data: config.data,
      type: 'line' as const,
      color: config.color,
      smooth: config.smooth ?? props.smooth,
      symbol: config.symbol ?? props.symbol,
      symbolSize: config.symbolSize ?? props.symbolSize,
      lineStyle: {
        width: config.lineWidth ?? props.lineWidth,
        color: config.color
      },
      areaStyle: config.areaStyle,
      emphasis: {
        focus: 'series' as const,
        lineStyle: {
          width: (config.lineWidth ?? props.lineWidth) + 1
        }
      }
    }
  }

  // 生成图表配置
  // isInitial: 首次渲染零值占位（动画前置阶段）
  // liveUpdate: live 模式下的数据更新 —— 不清零、不重播入场动画，仅平滑过渡
  const generateChartOptions = (isInitial = false, liveUpdate = false): EChartsOption => {
    const options: EChartsOption = {
      animation: true,
      animationDuration: isInitial ? 0 : liveUpdate ? 300 : 1300,
      animationDurationUpdate: isInitial ? 0 : liveUpdate ? 450 : 1300,
      grid: getGridWithLegend(props.showLegend && isMultipleData.value, props.legendPosition, {
        top: 15,
        right: 15,
        left: 0
      }),
      tooltip: props.showTooltip ? getTooltipStyle() : undefined,
      xAxis: {
        type: 'category',
        boundaryGap: false,
        data: props.xAxisData,
        axisTick: getAxisTickStyle(),
        axisLine: getAxisLineStyle(props.showAxisLine),
        axisLabel: getAxisLabelStyle(props.showAxisLabel)
      },
      yAxis: {
        type: 'value',
        min: 0,
        max: maxValue.value,
        axisLabel: getAxisLabelStyle(props.showAxisLabel),
        axisLine: getAxisLineStyle(props.showAxisLine),
        splitLine: getSplitLineStyle(props.showSplitLine)
      }
    }

    // 添加图例配置:type: 'scroll' 让图例始终单行展示,系列过多时不折行、
    // 出现左右翻页箭头滑动浏览(装得下时与普通图例无差别)
    if (props.showLegend && isMultipleData.value) {
      options.legend = { ...getLegendStyle(props.legendPosition), type: 'scroll' }
    }

    // 数据缩放:inside 型 dataZoom,支持鼠标框选放大、滚轮缩放、放大后拖拽平移。
    // 只对 x 轴(时间轴)生效;实时轮询场景下不锁定默认窗口,让用户可自由回看。
    if (props.showDataZoom) {
      const sharedZoom = {
        type: 'inside' as const,
        xAxisIndex: 0,
        start: props.dataZoomStart,
        end: props.dataZoomEnd,
        minValueSpan: 0
      }
      options.dataZoom = [sharedZoom]
    }

    // 生成系列数据
    if (isMultipleData.value) {
      const multiData = animatedData.value as LineDataItem[]
      options.series = multiData.map((item, index) => {
        const itemColor = getColor(props.colors[index], index)
        const areaStyle = generateAreaStyle(item, itemColor)

        return createSeriesItem({
          name: item.name,
          data: item.data,
          color: itemColor,
          smooth: item.smooth,
          symbol: item.symbol,
          lineWidth: item.lineWidth,
          areaStyle
        })
      })
    } else {
      // 单数据情况
      const singleData = animatedData.value as number[]
      const computedColor = getColor(props.colors[0])
      const areaStyle = generateSingleAreaStyle()

      options.series = [
        createSeriesItem({
          data: singleData,
          color: computedColor,
          areaStyle
        })
      ]
    }

    return options
  }

  // 更新图表
  const updateChartOptions = (options: EChartsOption) => {
    initChart(options, false, resolveSetOptionOpts())
  }

  // 初始化动画函数：先以 0 值静帧占位(动画关闭),再一次合并真实数据——
  // 所有系列经 update 过渡自 0 值插值到真实值,折线整体从底部升起,
  // 单系列/多系列同一条路径(与 SQL 吞吐趋势的开场一致)。原先多系列
  // 的阶梯式 setTimeout 逐条重画,观感像从左往右重新渲染,已移除。
  const initChartWithAnimation = () => {
    clearAnimationTimers()
    isAnimating.value = true

    // 初始化为0值数据
    animatedData.value = initAnimationData()
    updateChartOptions(generateChartOptions(true))

    // 一次性收敛到真实数据(update 过渡升起)
    nextTick(() => {
      animatedData.value = copyRealData()
      updateChartOptions(generateChartOptions(false))
      isAnimating.value = false
    })
  }

  // 空数据检查函数
  const checkIsEmpty = () => {
    // 检查单数据情况
    if (Array.isArray(props.data) && typeof props.data[0] === 'number') {
      const singleData = props.data as number[]
      return !singleData.length || singleData.every((val) => val === 0)
    }

    // 检查多数据情况
    if (Array.isArray(props.data) && typeof props.data[0] === 'object') {
      const multiData = props.data as LineDataItem[]
      return (
        !multiData.length ||
        multiData.every((item) => !item.data?.length || item.data.every((val) => val === 0))
      )
    }

    return true
  }

  // 使用新的图表组件抽象
  const {
    chartRef,
    initChart,
    getAxisLineStyle,
    getAxisLabelStyle,
    getAxisTickStyle,
    getSplitLineStyle,
    getTooltipStyle,
    getLegendStyle,
    getGridWithLegend,
    isEmpty
  } = useChartComponent({
    props,
    checkEmpty: checkIsEmpty,
    watchSources: [() => props.data, () => props.xAxisData, () => props.colors],
    onVisible: () => {
      // 当图表变为可见时，检查是否为空数据
      if (!isEmpty.value) {
        if (props.live && hasRendered.value) {
          applyLiveUpdate()
        } else {
          initChartWithAnimation()
          hasRendered.value = true
        }
      }
    },
    // live 模式:内部数据监听已有图表时直接用短过渡收敛,不再重播入场动画
    generateOptions: () =>
      props.live && hasRendered.value
        ? generateChartOptions(false, true)
        : generateChartOptions(false),
    setOptionOpts: resolveSetOptionOpts
  })

  /** live 增量模式：已有图表时直接以短过渡动画收敛到新数据，避免清零重播 */
  const applyLiveUpdate = () => {
    clearAnimationTimers()
    isAnimating.value = false
    animatedData.value = copyRealData()
    updateChartOptions(generateChartOptions(false, true))
  }

  /** 当前系列数据窗口长度（多系列时取首个系列） */
  const currentDataLen = () => {
    if (isMultipleData.value) {
      const multi = props.data as LineDataItem[]
      return multi.length ? (multi[0].data?.length ?? 0) : 0
    }
    return (props.data as number[]).length
  }

  // 图表渲染函数（优化：防止动画期间重复触发；live 模式下已渲染后由
  // useChartComponent 内部监听在 nextTick 以 animatedData 收敛，这里只同步最新数据；
  // 窗口长度变化（如切换统计范围）时重播开场动画）
  const renderChart = () => {
    if (isEmpty.value) return
    const nextLen = currentDataLen()
    if (nextLen !== seriesLen.value) {
      seriesLen.value = nextLen
      hasRendered.value = false // 窗口长度变化：视为新图表，重播开场
    }
    if (props.live && hasRendered.value) {
      animatedData.value = copyRealData()
      return
    }
    if (!isAnimating.value) {
      initChartWithAnimation()
      hasRendered.value = true
    }
  }

  // 使用 VueUse 的 watchDebounced 优化数据监听（避免频繁更新）
  watch([() => props.data, () => props.xAxisData, () => props.colors], renderChart, { deep: true })

  // 生命周期
  onMounted(() => {
    renderChart()
  })

  onBeforeUnmount(() => {
    clearAnimationTimers()
  })
</script>
