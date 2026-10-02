<template>
  <figure class="sc-chart">
    <!-- 两序列及以上必须有图例（单序列图标题已点名，不设图例盒）。 -->
    <figcaption v-if="series.length > 1" class="sc-legend">
      <span v-for="s in series" :key="s.name" class="sc-legend__item">
        <!-- 线键（短色条）：折线图的图例/tooltip 行都镜像「线」这个记号，不用色块。 -->
        <span class="sc-key" :style="{ background: s.color }" />
        <span class="sc-legend__name">{{ s.name }}</span>
      </span>
    </figcaption>

    <div class="sc-body">
      <!-- y 刻度沟：文字穿 text token，绝不穿序列色（浅色分类色当文字会读不清）。 -->
      <div class="sc-yaxis" :style="{ height: `${plotCssHeight}px` }">
        <span
          v-for="tk in yTicks"
          :key="tk.label"
          class="sc-yaxis__tick"
          :style="{ top: `${tk.topPercent}%` }"
          >{{ tk.label }}</span
        >
      </div>

      <div
        class="sc-plot"
        :style="{ height: `${plotCssHeight}px` }"
        tabindex="0"
        role="img"
        :aria-label="ariaLabel"
        @pointermove="onPointerMove"
        @pointerleave="hoverIdx = -1"
        @keydown.left.prevent="moveHover(-1)"
        @keydown.right.prevent="moveHover(1)"
      >
        <!-- viewBox 横向铺满（preserveAspectRatio="none"），线条用 vector-effect 锁
             2px 视觉宽；网格是 hairline 实线（虚线网格是反模式）。文字全部留在
             HTML 层 —— 被 non-uniform 拉伸的 SVG 里放文字会变形。 -->
        <svg
          class="sc-plot__svg"
          viewBox="0 0 600 100"
          preserveAspectRatio="none"
          aria-hidden="true"
        >
          <line v-for="g in gridYs" :key="g" x1="0" :y1="g" x2="600" :y2="g" class="sc-gridline" />
          <!-- 十字线：竖 hairline，吸附到最近样本的 x（读者瞄的是时刻，不是 2px 的线）。 -->
          <line
            v-if="hoverIdx >= 0"
            class="sc-crosshair"
            :x1="hoverViewX"
            y1="0"
            :x2="hoverViewX"
            y2="100"
          />
          <!-- 面积水洗（仅单序列）：10% 同色填充，两序列会互相叠色故不铺。 -->
          <path v-if="areaPath" class="sc-area" :style="{ fill: series[0]?.color }" :d="areaPath" />
          <path
            v-for="s in series"
            :key="s.name"
            class="sc-line"
            :style="{ stroke: s.color }"
            :d="linePathOf(s)"
          />
        </svg>

        <!-- 端点点标记（8px、2px 表面色环）与悬停点都用 HTML 圆点：viewBox 里的圆
             会被横向拉伸成椭圆。 -->
        <span
          v-for="dot in endDots"
          :key="dot.name"
          class="sc-dot"
          :style="{ left: `${dot.leftPercent}%`, top: `${dot.topPercent}%`, background: dot.color }"
        />
        <span
          v-for="dot in hoverDots"
          :key="`h-${dot.name}`"
          class="sc-dot sc-dot--hover"
          :style="{ left: `${dot.leftPercent}%`, top: `${dot.topPercent}%`, background: dot.color }"
        />

        <!-- tooltip：值是主角（primary 加粗），序列名次要；插值即 textContent。 -->
        <div v-if="hoverIdx >= 0 && tooltip" class="sc-tip" :style="tooltip.style">
          <div class="sc-tip__time">{{ tooltip.time }}</div>
          <div v-for="row in tooltip.rows" :key="row.name" class="sc-tip__row">
            <span class="sc-key" :style="{ background: row.color }" />
            <span class="sc-tip__name">{{ row.name }}</span>
            <span class="sc-tip__value">{{ row.value }}</span>
          </div>
        </div>
      </div>
    </div>

    <div class="sc-xaxis">
      <span
        v-for="tk in xTicks"
        :key="tk.label"
        class="sc-xaxis__tick"
        :class="`is-${tk.align}`"
        :style="{ left: `${tk.leftPercent}%` }"
        >{{ tk.label }}</span
      >
    </div>
  </figure>
</template>

<script lang="ts">
  /** 一条序列：名称 + 颜色（CSS 变量字符串）+ 逐样本值。（供调用方构造 props。） */
  export interface StatsSeries {
    name: string
    color: string
    values: number[]
  }
</script>

<script setup lang="ts">
  /**
   * 实时 stats 曲线（内联 SVG 折线，时序形态）。
   *
   * 为什么不用现成的 ArtLineChart / echarts 封装：那套封装是 category 轴 + 平滑
   * 曲线 + 渐变面积的「仪表盘装饰」风格，表达不了本图要的形态 —— 采样折线必须
   * 直线连接（平滑会伪造数据形状）、网格要 hairline 实线、线宽锁 2px、悬停要
   * 十字线吸附最近样本。设备模块的 metrics-panel 当初也是因此绕开封装直接写
   * option；这里更进一步：本图是监控面上的一张小图（原先寄生在抽屉、8a 起是详情页），
   * SVG 直接吃 CSS 变量取色，
   * 明暗两模式不需要图表库的主题机制（也无 canvas 初始化成本）。
   */
  import { computed, ref } from 'vue'
  import { formatAxisTickByUnit } from '@/modules/device/utils/display'

  defineOptions({ name: 'DockerStatsChart' })

  const props = withDefaults(
    defineProps<{
      /** 序列（1 条 = 单序列小图，无图例；≥2 条才配图例）。 */
      series: StatsSeries[]
      /** x 轴时刻（unix 毫秒），与每条序列的 values 等长同序。 */
      times: number[]
      /** y 刻度量纲（% / MB / B/s），刻度文案与表格读数同一套 formatByUnit 口径。 */
      unit: string
      /** 绘图区高度（px；轴带另计，容器高度含轴带 —— 不给图表留出滚动缝）。 */
      plotHeight?: number
      /** 单序列是否铺 10% 面积水洗。 */
      area?: boolean
    }>(),
    { plotHeight: 96, area: false }
  )

  /** 逻辑坐标系：横向 600 单位铺满宽度，纵向 100 单位映射整幅量程。 */
  const VB_W = 600
  const VB_H = 100

  const plotCssHeight = computed(() => Math.max(48, Math.floor(props.plotHeight)))

  /** 有效样本数（times 与各序列 values 的公共长度 —— 防御性取 min）。 */
  const count = computed(() =>
    Math.min(props.times.length, ...props.series.map((s) => s.values.length))
  )

  const times = computed(() => props.times.slice(0, count.value))

  /** 时间跨度（毫秒）；单样本为 0（点落左端 —— 窗口自左向右生长）。 */
  const span = computed(() => {
    const t = times.value
    return t.length > 1 ? Math.max(0, t[t.length - 1]! - t[0]!) : 0
  })

  /** 样本 i 的横向比例位置（0..1）。 */
  function xFrac(i: number): number {
    if (i < 0 || i >= times.value.length) return 0
    return span.value > 0 ? (times.value[i]! - times.value[0]!) / span.value : 0
  }

  /** 量程顶：向 1/2/2.5/5×10^k 上取整（刻度才会是干净数字）；全零数据给 1 防除零。 */
  const yMax = computed(() => {
    let max = 0
    for (const s of props.series) {
      for (const v of s.values.slice(0, count.value)) {
        if (Number.isFinite(v) && v > max) max = v
      }
    }
    if (max <= 0) return 1
    const pow = 10 ** Math.floor(Math.log10(max))
    for (const step of [1, 2, 2.5, 5, 10]) {
      if (step * pow >= max) return step * pow
    }
    return 10 * pow
  })

  function yFrac(v: number): number {
    if (!Number.isFinite(v)) return 1
    return Math.min(1, Math.max(0, v / yMax.value))
  }

  /** y 刻度：顶 / 半程 / 基线 三枚（安静；数字纵向对齐用 tabular-nums）。 */
  const yTicks = computed(() => {
    const mk = (v: number) => ({
      label: formatAxisTickByUnit(props.unit, v),
      topPercent: (1 - yFrac(v)) * 100
    })
    return [mk(yMax.value), mk(yMax.value / 2), mk(0)]
  })

  /** 网格线（viewBox y）：顶 / 中 / 基线三根 hairline。 */
  const gridYs = computed(() => [0, VB_H / 2, VB_H])

  function linePathOf(s: StatsSeries): string {
    const n = count.value
    if (n <= 0) return ''
    const parts: string[] = []
    for (let i = 0; i < n; i++) {
      const x = xFrac(i) * VB_W
      const y = (1 - yFrac(s.values[i] ?? 0)) * VB_H
      parts.push(`${i === 0 ? 'M' : 'L'}${x.toFixed(2)} ${y.toFixed(2)}`)
    }
    return parts.join(' ')
  }

  const areaPath = computed(() => {
    if (!props.area || props.series.length !== 1 || count.value <= 0) return ''
    const line = linePathOf(props.series[0]!)
    if (!line) return ''
    const x0 = xFrac(0) * VB_W
    const x1 = xFrac(count.value - 1) * VB_W
    return `${line} L${x1.toFixed(2)} ${VB_H} L${x0.toFixed(2)} ${VB_H} Z`
  })

  interface Dot {
    name: string
    color: string
    leftPercent: number
    topPercent: number
  }

  /** 端点点标记：每条序列最后一个样本（「线 → 值标在末端」）。 */
  const endDots = computed<Dot[]>(() => {
    const n = count.value
    if (n <= 0) return []
    const i = n - 1
    return props.series.map((s) => ({
      name: s.name,
      color: s.color,
      leftPercent: xFrac(i) * 100,
      topPercent: (1 - yFrac(s.values[i] ?? 0)) * 100
    }))
  })

  // ── 悬停层：十字线吸附最近样本，tooltip 列出该时刻全部序列 ──────────
  const hoverIdx = ref(-1)

  function nearestIndex(frac: number): number {
    const n = count.value
    if (n <= 0) return -1
    if (span.value <= 0) return 0
    const target = times.value[0]! + frac * span.value
    let best = 0
    let bestDist = Infinity
    for (let i = 0; i < n; i++) {
      const d = Math.abs(times.value[i]! - target)
      if (d < bestDist) {
        bestDist = d
        best = i
      }
    }
    return best
  }

  function onPointerMove(e: PointerEvent) {
    if (count.value <= 0) return
    const rect = (e.currentTarget as HTMLElement).getBoundingClientRect()
    const frac = Math.min(1, Math.max(0, (e.clientX - rect.left) / rect.width))
    hoverIdx.value = nearestIndex(frac)
  }

  /** 键盘可达：左右键逐样本移动（焦点读数与悬停同一份 tooltip —— 不许只给鼠标）。 */
  function moveHover(delta: number) {
    const n = count.value
    if (n <= 0) return
    const cur = hoverIdx.value < 0 ? n - 1 : hoverIdx.value
    hoverIdx.value = Math.min(n - 1, Math.max(0, cur + delta))
  }

  const hoverViewX = computed(() => (hoverIdx.value >= 0 ? xFrac(hoverIdx.value) * VB_W : 0))

  const hoverDots = computed<Dot[]>(() => {
    const i = hoverIdx.value
    if (i < 0) return []
    return props.series.map((s) => ({
      name: s.name,
      color: s.color,
      leftPercent: xFrac(i) * 100,
      topPercent: (1 - yFrac(s.values[i] ?? 0)) * 100
    }))
  })

  function hhmmss(t: number): string {
    const d = new Date(t)
    const p = (v: number) => String(v).padStart(2, '0')
    return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
  }

  const tooltip = computed(() => {
    const i = hoverIdx.value
    if (i < 0 || i >= times.value.length) return null
    const frac = xFrac(i)
    // 靠右翻转，避免 tooltip 顶出容器右缘。
    const flip = frac > 0.72
    return {
      style: {
        left: flip ? '100%' : `${frac * 100}%`,
        transform: flip ? 'translateX(-100%)' : 'none'
      },
      time: hhmmss(times.value[i]!),
      rows: props.series.map((s) => ({
        name: s.name,
        color: s.color,
        value: formatAxisTickByUnit(props.unit, s.values[i] ?? 0)
      }))
    }
  })

  /** x 刻度：首 / 中 / 尾三枚时刻（边缘对齐防溢出）；单样本时一枚。 */
  const xTicks = computed(() => {
    const n = count.value
    if (n <= 0) return []
    if (n === 1) return [{ label: hhmmss(times.value[0]!), leftPercent: 0, align: 'left' }]
    return [
      { label: hhmmss(times.value[0]!), leftPercent: 0, align: 'left' },
      { label: hhmmss(times.value[Math.floor((n - 1) / 2)]!), leftPercent: 50, align: 'center' },
      { label: hhmmss(times.value[n - 1]!), leftPercent: 100, align: 'right' }
    ]
  })

  /** 无悬停时的读屏描述：最新值在 aria-label 里说一遍（tooltip 只是增强，不是门槛）。 */
  const ariaLabel = computed(() => {
    const n = count.value
    const title =
      props.series.length === 1 ? props.series[0]!.name : props.series.map((s) => s.name).join('与')
    if (n <= 0) return `${title}：暂无样本`
    const latest = props.series
      .map((s) => `${s.name} ${formatAxisTickByUnit(props.unit, s.values[n - 1] ?? 0)}`)
      .join('，')
    return `${title}实时曲线，最新：${latest}`
  })
</script>

<style lang="scss" scoped>
  .sc-chart {
    position: relative;
    min-width: 0; // 网格里允许收缩（窄屏两列变一列时不撑破容器）
    margin: 0;
  }

  .sc-legend {
    display: flex;
    flex-wrap: wrap;
    gap: 4px 12px;
    margin-bottom: 4px;

    &__item {
      display: inline-flex;
      gap: 5px;
      align-items: center;
    }

    &__name {
      color: var(--el-text-color-secondary);
      font-size: 12px;
    }
  }

  .sc-body {
    display: flex;
    gap: 6px;
  }

  // y 刻度沟：右对齐、纵向按量程百分比落位。
  .sc-yaxis {
    position: relative;
    flex: none;
    width: 44px;

    &__tick {
      position: absolute;
      right: 0;
      transform: translateY(-50%);
      color: var(--el-text-color-secondary);
      font-size: 11px;
      font-variant-numeric: tabular-nums;
      white-space: nowrap;
    }
  }

  .sc-plot {
    position: relative;
    flex: 1;
    min-width: 0;
    cursor: crosshair;
    outline: none;

    &:focus-visible {
      outline: 2px solid var(--el-color-primary);
      outline-offset: 2px;
    }

    &__svg {
      display: block;
      width: 100%;
      height: 100%;
    }
  }

  .sc-gridline {
    stroke: var(--el-border-color-lighter);
    stroke-width: 1;
  }

  .sc-crosshair {
    stroke: var(--el-border-color);
    stroke-width: 1;
  }

  .sc-line {
    fill: none;
    stroke-width: 2;
    stroke-linecap: round;
    stroke-linejoin: round;
    // 非等比拉伸下锁定 2px 视觉线宽（时序线不因容器宽度变形）。
    vector-effect: non-scaling-stroke;
  }

  .sc-area {
    stroke: none;
    fill-opacity: 0.1;
  }

  // 点标记：8px 圆 + 2px 表面色环（点压线 / 叠点仍可读）。
  .sc-dot {
    position: absolute;
    width: 8px;
    height: 8px;
    border: 2px solid var(--el-bg-color);
    border-radius: 50%;
    transform: translate(-50%, -50%);
    pointer-events: none;

    &--hover {
      width: 7px;
      height: 7px;
    }
  }

  .sc-key {
    display: inline-block;
    flex: none;
    width: 12px;
    height: 2px;
    border-radius: 1px;
  }

  .sc-tip {
    position: absolute;
    bottom: calc(100% - 2px);
    z-index: 1;
    min-width: 120px;
    padding: 6px 8px;
    background: var(--el-bg-color-overlay);
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 6px;
    box-shadow: var(--el-box-shadow-light);
    pointer-events: none;

    &__time {
      margin-bottom: 2px;
      color: var(--el-text-color-secondary);
      font-size: 11px;
      font-variant-numeric: tabular-nums;
    }

    &__row {
      display: flex;
      gap: 6px;
      align-items: baseline;
      justify-content: space-between;
    }

    &__name {
      color: var(--el-text-color-secondary);
      font-size: 12px;
    }

    // 值是主角：主文字色 + 加粗（图例的层级关系在 tooltip 里反过来）。
    &__value {
      color: var(--el-text-color-primary);
      font-size: 12px;
      font-weight: 600;
      font-variant-numeric: tabular-nums;
    }
  }

  // x 轴带：与 y 刻度沟（44px）+ 间距（6px）左对齐，高度固定 —— 容器高度含轴带。
  .sc-xaxis {
    position: relative;
    height: 18px;
    margin-left: 50px;

    &__tick {
      position: absolute;
      color: var(--el-text-color-secondary);
      font-size: 11px;
      font-variant-numeric: tabular-nums;
      white-space: nowrap;

      &.is-left {
        left: 0;
      }

      &.is-center {
        transform: translateX(-50%);
      }

      &.is-right {
        right: 0;
        left: auto;
      }
    }
  }
</style>
