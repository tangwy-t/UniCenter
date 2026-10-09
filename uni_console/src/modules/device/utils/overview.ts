/**
 * device · 总览页的**纯函数层**（图表拆分、衍生序列、状态判定、格式化）。
 *
 * ── 为什么这些逻辑必须留在前端且必须纯 ──────────────────────────────
 *
 * 后端只做「取数 + 转置 + 统计量」（见 response.DeviceOverviewResp），
 * 它**刻意不知道**「页面要画几张图、每张图画什么」。原因是这个决策会频繁变化
 * （产品想让磁盘容量与使用率合并/拆分、想加一个新指标块），而每次变化都改后端
 * 意味着重新发版、重新联调；留在前端则是一处纯函数改动。
 *
 * 对应的代价是「同一列可能被两张图引用」的风险，故本模块用
 * `buildOverviewCharts` 作为**唯一的图表清单**：它按
 * `CHART_BLOCKS` 的顺序逐个消费 `available_metrics`，
 * 同一列只会被**第一个**声明它的图块取走（见 `assignColumns`），
 * 从结构上杜绝「同一列被重复画进两张图」与「有列没被任何图画」两侧的遗漏。
 *
 * 本文件不 import 任何 Vue / ECharts / HTTP：全部是纯函数，可在 vitest 里
 * 直接喂响应对象断言。渲染层（views/overview.vue）只负责把这里的输出交给图表。
 */

import {
  METRIC_META,
  groupColumnsByUnit,
  formatMetricText,
  metricLabel,
  metricUnit
} from './column-meta'

/** 后端列名常量。**只在此处出现一次**，避免字面量散落。 */
export const COL = {
  cpu: 'cpu_used_percent',
  cpuIowait: 'cpu_iowait',
  load1: 'load1',
  load5: 'load5',
  load15: 'load15',
  memPercent: 'mem_used_percent',
  memUsedMB: 'mem_used_mb',
  memAvailMB: 'mem_available_mb',
  swapPercent: 'swap_used_percent',
  swapUsedMB: 'swap_used_mb',
  diskTotalGB: 'disk_total_gb',
  diskUsedGB: 'disk_used_gb',
  diskUsedPercent: 'disk_used_percent',
  diskReadBytesSec: 'disk_io_read_bytes_sec',
  diskWriteBytesSec: 'disk_io_write_bytes_sec',
  nicRxBytesSec: 'nic_rx_bytes_sec',
  nicTxBytesSec: 'nic_tx_bytes_sec',
  tcpTotal: 'tcp_total',
  tcpEstablished: 'tcp_established',
  tcpListen: 'tcp_listen',
  procCount: 'proc_count',
  uptimeSec: 'uptime_sec',
  maxTemperatureC: 'max_temperature_c'
} as const

// ── 图表类型 ──────────────────────────────────────────────

/**
 * 图表类型枚举。**每一种都有明确的适用理由**（对应设计文档第 4 步的选型要求），
 * 这里把理由写在类型上，避免渲染层随意换类型：
 *
 * - `line`    趋势：连续时间轴上的多条折线（每台设备一条），用于看**走势与对比**；
 * - `area`    趋势 + 面积：同 `line`，但强调「堆叠总量」的观感（用于流量类）；
 * - `bar`     存量对比：**每台设备一个柱**，用于「此刻各设备的容量/占用」横向对比；
 * - `stacked` 堆叠柱：一台设备的多个**分量**构成一个柱（如磁盘已用/可用、连接状态）；
 * - `ring`    占比：**单台设备的构成占比**（饼/环），不用于多设备对比
 *             （多设备的多个环无法横向比较大小，那正是柱状图的用途）。
 */
export type OverviewChartType = 'line' | 'area' | 'bar' | 'stacked' | 'ring'

/** 图块位置（决定渲染在哪一栏；配合响应式栅格）。 */
export type ChartSpan = 'full' | 'half'

/** 一次图表拆分的声明。 */
export interface ChartBlockSpec {
  /** 稳定 key（用作 v-for 的 key 与折叠状态记忆，不用下标）。 */
  key: string
  /** 图块标题（中文）。 */
  title: string
  /**
   * 该图块**可能**消费的列（按优先级排序）。
   * 实际用到的列 = 此列表 ∩ `available_metrics` ∩ 尚未被前面的图块取走的列。
   * 故这里的列会「尽力而为」：后端不产某列时该图块自动用剩下的列，全无则整块隐藏。
   */
  columns: string[]
  type: OverviewChartType
  span: ChartSpan
  /** 图块用途说明（渲染为副标题/悬浮提示，让读者知道这张图回答什么问题）。 */
  purpose: string
  /**
   * 选型理由：**仅供维护者，不得渲染到页面**。
   *
   * 曾经渲染在图块右上角的「?」里，已撤下：把设计论证（「刻意不用堆叠柱：
   * 状态之间不构成部分-整体关系」）摆在界面上，等于把设计文档混进产品页面 ——
   * 读者是来看数据的人，评审选型不是他的任务。论证留在此处，是为了让
   * 「为什么这么画」有处可查（并有守卫测试要求每个图块都能说清），
   * 而不是为了展示。回归由 `__tests__/copy-no-internals.test.ts` 拦住。
   */
  rationale: string
  /** 单位覆盖：本图块所有列共用该单位时的显示单位（不设则由列的元数据决定）。 */
  unit?: string
}

// ── 图表清单（**唯一事实源**）───────────────────────────────

/**
 * 总览页的图表清单。顺序 = 页面从上到下的展示顺序。
 *
 * 拆分依据是**指标类别**（一个问题一张图），不是「每列一张图」：
 * 把 24 列画成 24 张图没人看得完，而把全部列塞进一张图则无法比较不同量纲。
 * 故按「读者想问的问题」聚合：
 *
 *   1. 状态与水位总览   → 「谁在线、谁快满了？」（表 + 进度，不是趋势）
 *   2. CPU 使用率       → 「谁在忙？」（趋势，同行对比）
 *   3. 系统负载         → 「谁在排队？」（趋势，load1/5/15）
 *   4. 内存使用率       → 「谁在吃内存？」（趋势）
 *   5. 交换分区         → 「谁在换页？」（趋势，饱和前兆）
 *   6. 磁盘容量对比     → 「谁快满了？」（当前值柱状）
 *   7. 磁盘使用率趋势   → 「什么时候开始涨的？」（趋势）
 *   8. 磁盘 IO          → 「谁在压盘？」（趋势 + 面积）
 *   9. 网络吞吐         → 「谁在占带宽？」（趋势 + 面积）
 *  10. TCP 连接状态     → 「连接堆积在哪？」（趋势，分组）
 *  11. 进程与运行时长   → 「谁重启过？」（趋势）
 *  12. 温度             → 「谁在过热？」（趋势）
 *
 * 「一张图一个问题」也决定了同列不会被两块取走的自然边界：每块声明自己
 * 语义上需要的列，交集由 assignColumns 解决。
 */
export const CHART_BLOCKS: ChartBlockSpec[] = [
  {
    key: 'cpu',
    title: 'CPU 使用率',
    columns: [COL.cpu, COL.cpuIowait],
    type: 'line',
    span: 'half',
    purpose: '各设备 CPU 使用率随时间的变化，用于定位持续高负载的机器',
    rationale: '时间序列的连续变化 → 折线；多设备同行对比 → 每台一条线并固定配色'
  },
  {
    key: 'load',
    title: '系统负载',
    columns: [COL.load1, COL.load5, COL.load15],
    type: 'line',
    span: 'half',
    purpose: '1/5/15 分钟平均负载，反映排队长度而不仅是瞬时占用',
    rationale: '同为时间序列 → 折线；三者同量纲可共用 Y 轴，且 5/15 分钟的平滑特性天然适合同图对比'
  },
  {
    key: 'mem',
    title: '内存使用率',
    columns: [COL.memPercent, COL.memAvailMB],
    type: 'line',
    span: 'half',
    purpose: '内存使用率趋势；可用内存按字节量级单独成块（量纲不同）',
    rationale:
      '时间序列 → 折线；百分比与 MB 量纲不同，共轴时百分比线被压成贴 0 的直线 —— 故按量纲分轴（主量纲左轴，MB 挂隐藏右轴各自缩放），而不是砍掉某一列'
  },
  {
    key: 'swap',
    title: '交换分区使用',
    columns: [COL.swapPercent, COL.swapUsedMB],
    type: 'line',
    span: 'half',
    purpose: '换页活动是内存饱和的前兆，单独成块以便在内存告警前发现',
    rationale:
      '时间序列 → 折线；量纲混杂（% 与 MB）时按量纲分轴：百分比占左轴，MB 挂隐藏轴各自缩放 —— 两列都保留且互不压扁'
  },
  {
    key: 'disk-capacity',
    title: '磁盘容量对比',
    columns: [COL.diskUsedGB, COL.diskTotalGB],
    type: 'stacked',
    span: 'half',
    purpose: '当前各设备的磁盘容量与已用量（非趋势），用于一眼看出谁的容量吃紧',
    rationale:
      '「容量 vs 已用」是**存量构成**而非时间序列 → 堆叠柱：每台设备一个柱，已用与可用（total − used）两段，柱高即总容量，可直接比较绝对规模与剩余空间。数据取每列**最后一个非空值**（存量快照），不是时间序列'
  },
  {
    key: 'disk-usage',
    title: '磁盘使用率趋势',
    columns: [COL.diskUsedPercent],
    type: 'line',
    span: 'half',
    purpose: '磁盘使用率随时间的变化，用于判断增长速率与何时触及阈值',
    rationale:
      '时间序列 → 折线；使用率是**百分比**（0–100 有天然边界），与容量（GB）量纲不同故必须与容量图分开'
  },
  {
    key: 'disk-io',
    title: '磁盘 IO 吞吐',
    columns: [COL.diskReadBytesSec, COL.diskWriteBytesSec],
    type: 'area',
    span: 'half',
    purpose: '读/写速率随时间的变化，用于定位 IO 压力来源',
    rationale:
      '时间序列 → 折线；速率是「持续流量」，面积填充能直观表现吞吐的累积感，读/写两种方向用不同色相区分'
  },
  {
    key: 'nic',
    title: '网络吞吐',
    columns: [COL.nicRxBytesSec, COL.nicTxBytesSec],
    type: 'area',
    span: 'half',
    purpose: '网卡接收/发送速率随时间的变化，用于发现带宽占用与流量异常',
    rationale: '同磁盘 IO：持续流量的时间序列 → 面积；收/发两向分色，单位按量级自适应格式化'
  },
  {
    key: 'tcp',
    title: 'TCP 连接状态',
    columns: [COL.tcpTotal, COL.tcpEstablished, COL.tcpListen],
    type: 'line',
    span: 'half',
    purpose: '连接总数/已建立/监听数的变化，用于发现连接堆积或泄漏',
    rationale:
      '状态计数随时间变化 → 折线（**分组对比**）；刻意不用堆叠柱：状态之间不构成「和」的部分-整体关系（总数是独立观测量，不是分量之和）'
  },
  {
    key: 'proc',
    title: '进程数与运行时长',
    columns: [COL.procCount, COL.uptimeSec],
    type: 'line',
    span: 'half',
    purpose: '进程数变化与运行时长（运行时长回退即代表设备重启过）',
    rationale:
      '同为时间序列 → 折线；两者量纲不同（个 vs 秒）且数值差两个数量级（数百 vs 数万），按量纲分轴：进程数占左轴，秒挂隐藏轴各自缩放 —— 同轴会把进程数压成贴 0 的直线'
  },
  {
    key: 'temp',
    title: '最高温度',
    columns: [COL.maxTemperatureC],
    type: 'line',
    span: 'half',
    purpose: '温度传感器最高读数趋势，用于发现散热异常',
    rationale: '时间序列 → 折线；单列也用折线以保持「趋势块」的一致性（柱状会误导为存量对比）'
  }
]

// ── 图表构建 ──────────────────────────────────────────────

/** 一段（一台设备 × 一列）可绘制的序列，已转成 [时间毫秒, 值|null][] 的 ECharts 形状。 */
export interface OverviewSeries {
  /**
   * 图例文本。
   *
   * 命名策略**随图块的列数变化**（不是随意，而是图例拥挤度的直接对策）：
   *   - 图块只有 1 列（如「磁盘使用率趋势」「最高温度」）→ **只写主机名**。
   *     此时整张图都是同一个指标，再给每台设备加「· 磁盘使用率」是纯噪声 ——
   *     而总览页最多会同时显示十余台设备，这层噪声会把图例挤到换行/分页。
   *   - 图块有多列（如「系统负载」的 load1/5/15）→ `主机名 · 指标名`。
   *     此时必须带上指标名，否则同一条图例无法区分是 1 分钟还是 15 分钟负载。
   */
  name: string
  /** 设备 ID（用于与选中状态联动、以及点击跳转详情）。 */
  deviceId: string
  /** 该系列对应的原始列名（snake_case，用于元数据与提示）。 */
  metric: string
  /** 设备主机名。 */
  hostname: string
  /** `[时间毫秒, 值]`；值为 `null` 表示该桶**无数据**（图上断线，不是 0）。 */
  points: [number, number | null][]
  /** 本系列非空的桶数（用于判断「有数据」）。 */
  present: number
  /** 本系列总桶数。 */
  total: number
}

/**
 * 堆叠柱（存量图）的一行：一台设备的**分类快照**。
 *
 * 「磁盘容量对比」不是时间序列 —— 它要的是每台设备**此刻**的已用/可用两段。
 * 取值口径：每列取**最后一个非空值**（后端 pts 按时间升序，见 device_overview.go
 * 的 buildOverviewSeries —— lastIdx 只增不减）。缺列（该设备全窗无数据）整行不进。
 */
export interface OverviewSnapshotRow {
  /** 设备 ID。 */
  deviceId: string
  /** 主机名（缺省回退 ID）。 */
  hostname: string
  /** 列名 → 最新非空值。缺列不进 map（与「值为 0」形状上可区分）。 */
  values: Record<string, number>
}

/** 一张已构建好的图表。 */
export interface OverviewChart {
  key: string
  title: string
  type: OverviewChartType
  span: ChartSpan
  purpose: string
  rationale: string
  /** 本图块实际用到的列（顺序与 CHART_BLOCKS 声明一致）。 */
  columns: string[]
  /** 实际绘制的系列（每台设备 × 每列）。存量图（stacked）此为空，改看 snapshot。 */
  series: OverviewSeries[]
  /**
   * 存量图（stacked）的分类快照：每台设备一行、按**该块第一列**的最新值降序。
   * 时间序列图为空数组。
   */
  snapshot: OverviewSnapshotRow[]
  /**
   * 按量纲分出的纵轴组（多量纲图块用）：主量纲（列最多）在第一位占左轴，
   * 其余挂隐藏轴。渲染层据此给每个系列配 yAxisIndex，杜绝「百分比线贴 0」。
   */
  unitAxes: { unit: string; columns: string[] }[]
  /** 图中**有数据**的设备台数（用于「N 台中有 M 台有数据」的说明）。 */
  devicesWithData: number
  /** 图块涉及的设备总数（与 devicesWithData 对比得出覆盖率）。 */
  devicesTotal: number
}

/**
 * 按 CHART_BLOCKS 把所有可用列**恰好分配一次**。
 *
 * 这是「不遗漏、不重复」的结构保证：
 *   - 遍历 CHART_BLOCKS 时，已被前面图块取走的列会从池子里删掉，故同一列
 *     不可能进两张图（**不重复**）；
 *   - 结束后池子里剩下的列会被收进一个「其他指标」兜底图块（见 buildOverviewCharts），
 *     故后端新增列时不会**遗漏**（它可能被兜底图收走，也可能被后续显式声明收走）。
 *
 * @returns `assigned` 每块实际拿到的列；`leftover` 未被任何块声明的列。
 */
export function assignColumns(
  available: string[],
  blocks: ChartBlockSpec[] = CHART_BLOCKS
): { assigned: Map<string, string[]>; leftover: string[] } {
  const pool = new Set(available)
  const assigned = new Map<string, string[]>()

  for (const block of blocks) {
    const cols: string[] = []
    for (const col of block.columns) {
      if (pool.has(col)) {
        cols.push(col)
        pool.delete(col)
      }
    }
    assigned.set(block.key, cols)
  }

  return { assigned, leftover: [...pool] }
}

/** 兜底图块的 key（用于 v-for 与折叠记忆）。 */
export const FALLBACK_CHART_KEY = 'other'

/**
 * 取一个系列的 `[时间毫秒, 值|null][]`。
 *
 * 后端 `axis` 是**共享时间轴**（unix 秒），json 反序列化后是 number。
 * 必须 `× 1000` 转毫秒：ECharts 的 `time` 轴单位是毫秒。
 * 值原样透传（含 `null`）—— **绝不** `?? 0`：
 * 0 是合法观测值，用它填「没数据」会让图上出现一条虚假的零线。
 */
export function seriesPoints(
  axis: number[],
  values: (number | null)[] | undefined
): [number, number | null][] {
  if (!Array.isArray(values) || values.length === 0) {
    return axis.map((t) => [t * 1000, null])
  }
  // 以 axis 为基准遍历（而**不是**遍历 values）：axis 是契约里的共享轴，
  // values 短于 axis 时按缺失处理，长于 axis 时忽略多余部分（两端都不错位）。
  return axis.map((t, i) => [t * 1000, values[i] ?? null])
}

/** 统计一段值里非空的数量（用于「有数据」判定）。 */
export function countPresent(values: (number | null)[] | undefined): number {
  if (!Array.isArray(values)) return 0
  return values.reduce<number>((n, v) => (v === null || v === undefined ? n : n + 1), 0)
}

/** top-N 的「全部」哨兵：不裁剪，画全部设备（快照表本就全量）。 */
export const TOP_N_ALL = Number.POSITIVE_INFINITY

/**
 * 取一个设备在一列上的**最新非空值**。
 *
 * 排名口径（top-N 与存量快照共用）：后端 values 按共享时间轴对齐、时间升序，
 * 故「最后一个非 null 的值」即最新观测。**null 不当 0** —— 全窗无数据返回
 * `null`，排序时排在最后。
 */
export function lastValue(values: (number | null)[] | undefined): number | null {
  if (!Array.isArray(values)) return null
  for (let i = values.length - 1; i >= 0; i--) {
    const v = values[i]
    if (typeof v === 'number' && Number.isFinite(v)) return v
  }
  return null
}

/**
 * 构建**全部**可显示的图表。
 *
 * 过滤规则（两条，缺一不可）：
 *   1. 图块拿不到任何列 → 该档位/该批次没有这些指标 → 整块隐藏。
 *      画一张没有系列的图只有标题与空坐标轴，读者无法区分「没这个指标」
 *      与「页面坏了」；
 *   2. 图块拿到了列、但**所有设备这些列都无数据** → 同样隐藏。
 *      这与规则 1 的区别在于原因（前者是「该档不产此列」，后者是「选了设备
 *      但设备没上报」），但对读者的结论相同：这张图现在是空的，不该占用屏幅。
 *      整体「全都画不出」的情况由 `isEmptyResult` + `drawableChartCount`
 *      渲染成一个**显式空态**，而不是一屏空白图。
 *
 * top-N：每个图块按**该块第一个列**的最新非空值降序，只保留前 N 台的曲线，
 * 其余设备不进图（快照表仍显示全部）。字段缺失/全 null 的设备排在最后，
 * **绝不**被当成 0 参与降序。默认 8 —— 100 台规模下全画只会糊成一片。
 *
 * @param axis      共享时间轴（unix 秒）
 * @param available 后端 `available_metrics`（决定哪些图块存在）
 * @param devices   后端 `devices`（每台设备的 series + 主机名）
 * @param topN      每块最多画几台设备（默认 8；`TOP_N_ALL` = 不裁剪）
 */
export function buildOverviewCharts(
  axis: number[],
  available: string[],
  devices: DeviceOverviewLike[],
  topN: number = 8
): OverviewChart[] {
  const { assigned, leftover } = assignColumns(available)

  // 把图块声明与实际分配拼起来；只保留拿得到列的图块。
  const specs: { spec: ChartBlockSpec; columns: string[] }[] = []
  for (const block of CHART_BLOCKS) {
    const cols = assigned.get(block.key) ?? []
    if (cols.length > 0) specs.push({ spec: block, columns: cols })
  }

  // 兜底：有列没被任何块声明（后端新增了列）→ 收进「其他指标」折线图。
  // 这条分支让「后端加列 → 前端静默丢数据」不可能发生。
  if (leftover.length > 0) {
    specs.push({
      columns: leftover,
      spec: {
        key: FALLBACK_CHART_KEY,
        title: '其他指标',
        columns: leftover,
        type: 'line',
        span: 'half',
        purpose: '未被专门图块覆盖的指标列（后端新增列时会落到这里，保证不丢数据）',
        rationale: '时间序列默认按趋势处理 → 折线；本块的存在是为了「不遗漏」而非特定分析目的'
      }
    })
  }

  return specs
    .map(({ spec, columns }) => buildChart(spec, columns, axis, devices, topN))
    .filter(hasDrawableSeries)
}

/**
 * 构建单张图表。
 *
 * 存量图（stacked）：每台设备一行分类快照（列 → 最后一个非空值），按第一列
 * 降序；不进 series（时间点序列对存量图没有意义，见 disk-capacity 的 rationale）。
 * 时间序列图（line/area）：按第一列的最新非空值降序取 top-N，再逐列产出系列。
 */
function buildChart(
  spec: ChartBlockSpec,
  columns: string[],
  axis: number[],
  devices: DeviceOverviewLike[],
  topN: number
): OverviewChart {
  // 单列图块只写主机名，多列图块才带指标名（理由见 OverviewSeries.name 的注释）。
  const singleColumn = columns.length === 1
  // 按量纲分轴：主量纲（列最多，平票取先出现）在第一位。单量纲时只有一组 ——
  // 渲染层拿到的是退化的单轴，行为与「一根 Y 轴」完全一致。
  const unitAxes = groupColumnsByUnit(columns)

  // ── 排名：按该块第一列的最新非空值降序，缺值排最后（不是 0）──────────
  const rankColumn = columns[0]
  const ranked = devices
    .map((dev) => ({
      dev,
      rank: lastValue(dev.series?.find((x) => x.metric === rankColumn)?.values)
    }))
    // null 排最后：先把有值的按值降序、无值的按设备出现顺序排在尾部 ——
    // 两者分别 sort 再拼，避免 comparator 里对 null 与 number 混合比较。
    .sort((a, b) => {
      if (a.rank === null && b.rank === null) return 0
      if (a.rank === null) return 1
      if (b.rank === null) return -1
      return b.rank - a.rank
    })

  // 「有数据」按**全部**设备算（top-N 裁的只是画出来的台数，不是有数据的台数）：
  // 角标「已显示 N / 总 M 台」与「M 台有数据」各自回答不同的问题，混用会让
  // 「3 台没数据」与「3 台被 top-N 裁掉」不可区分。
  const devicesWithData = new Set<string>()
  for (const { dev } of ranked) {
    for (const col of columns) {
      if ((dev.series?.find((x) => x.metric === col)?.present ?? 0) > 0) {
        devicesWithData.add(dev.id)
        break
      }
    }
  }

  const series: OverviewSeries[] = []
  const snapshot: OverviewSnapshotRow[] = []

  if (spec.type === 'stacked') {
    // 存量快照：每台设备取各列的最后一个非空值；任一列有值即入图。
    // top-N 同样适用（100 台的柱状图同样糊）；全部列缺值的设备不进。
    for (const { dev } of ranked.slice(0, topN === TOP_N_ALL ? undefined : topN)) {
      const values: Record<string, number> = {}
      for (const col of columns) {
        const v = lastValue(dev.series?.find((x) => x.metric === col)?.values)
        if (v !== null) values[col] = v
      }
      if (Object.keys(values).length === 0) continue
      snapshot.push({
        deviceId: dev.id,
        hostname: dev.hostname || dev.id,
        values
      })
    }
  } else {
    // 时间序列：top-N 内的设备才产出曲线；其余不进图（看全量走快照表）。
    const kept = ranked.slice(0, topN === TOP_N_ALL ? undefined : topN)
    for (const { dev } of kept) {
      for (const col of columns) {
        const s = (dev.series ?? []).find((x) => x.metric === col)
        const present = s?.present ?? 0
        if (present === 0) continue // 整列无数据的设备不进图例（避免图例被空系列淹没）
        const host = dev.hostname || dev.id
        series.push({
          name: singleColumn ? host : `${host} · ${metricLabel(col)}`,
          deviceId: dev.id,
          metric: col,
          hostname: dev.hostname || dev.id,
          points: seriesPoints(axis, s?.values),
          present,
          total: axis.length
        })
      }
    }
  }

  return {
    key: spec.key,
    title: spec.title,
    type: spec.type,
    span: spec.span,
    purpose: spec.purpose,
    rationale: spec.rationale,
    columns,
    series,
    snapshot,
    unitAxes,
    devicesWithData: devicesWithData.size,
    devicesTotal: devices.length
  }
}

// ── 输入类型（结构化，避免与生成类型的可选性差异耦合）────────

/**
 * 图表构建所需的最小设备形状。
 *
 * 刻意**不**直接依赖 `Api.Device.DeviceOverviewItem`：本模块只需要这几个字段，
 * 声明成结构化类型后，单测可以直接喂普通对象（无需构造完整响应），
 * 且后端将来增删无关字段不会波及本模块。
 */
export interface DeviceOverviewLike {
  id: string
  hostname: string
  series?: { metric: string; values: (number | null)[]; present: number; missing: number }[]
  watermark?: Record<string, number>
  watermarkAt?: number | null
  online: boolean
  stale: boolean
  error?: string
}

// ── 页面级状态判定 ────────────────────────────────────────

/** 总览页的整体状态（决定渲染哪一个骨架）。 */
export type OverviewState = 'loading' | 'error' | 'empty' | 'ready'

/**
 * 判定页面状态。
 *
 * 顺序即优先级，三条都是**硬要求**：
 *   1. `loading` 优先于一切（首次加载时不该闪一下「无数据」）；
 *   2. `error` 优先于 empty（请求失败必须说清是失败，而不是伪装成「没有设备」——
 *      后者会让用户以为集群空了）；
 *   3. `empty` 判定见 `isEmptyResult`（0 台设备 / 全部无数据都要显式空态）。
 *
 * 注意 `ready` 也可能「部分可用」：有设备但个别设备带 error，此时仍渲染页面，
 * 由图块与表格就地提示（见 deviceIssueText）。
 */
export function overviewState(
  hasError: boolean,
  loading: boolean,
  deviceCount: number
): OverviewState {
  if (loading) return 'loading'
  if (hasError) return 'error'
  if (deviceCount === 0) return 'empty'
  return 'ready'
}

/**
 * 判定「本页整体无数据可看」——即**没有任何一张图**能画出至少一条曲线。
 *
 * 与 `deviceCount === 0` 的区别很重要：用户可能筛出了 3 台设备，但它们在
 * 所选时间窗内一条指标都没有（刚 enroll 完还没上报）。此时页面若照常渲染
 * 一屏空白坐标轴，看起来就是「页面坏了」；故返回 `true` 让视图渲染
 * 「已选设备在所选时间窗内无指标数据」的显式空态。
 *
 * 传入的是**可绘制的图块数**（用 `drawableChartCount` 算），而不是图块总数：
 * 图块总数恒 > 0（只要该档位有可用列），拿它判断会让空态永不触发。
 */
export function isEmptyResult(deviceCount: number, drawableCharts: number): boolean {
  if (deviceCount === 0) return true
  return drawableCharts === 0
}

/** 统计「至少有一条非空曲线」的图块数（供 isEmptyResult 判空）。 */
export function drawableChartCount(charts: OverviewChart[]): number {
  return charts.filter(hasDrawableSeries).length
}

/** 图表是否可画（有至少一条非空系列，或存量图有至少一行快照）。 */
export function hasDrawableSeries(chart: OverviewChart): boolean {
  if (chart.series.some((s) => s.present > 0)) return true
  return (chart.snapshot?.length ?? 0) > 0
}

// ── 设备级问题文案 ────────────────────────────────────────

/**
 * 单台设备的问题文案（无问题返回空串）。
 *
 * 三种可叠加的问题各自独立成句：离线、数据陈旧（心跳在但指标停更）、
 * 趋势取数失败。它们**不是**互斥的 —— 一台设备可以既在线又数据陈旧，
 * 也可以既离线又有历史取数错误，故按「最严重的先说」的顺序拼接。
 *
 * 水位阈值：CPU ≥80 标「高」、≥90 标「告警」（与 usageTone 的 80/90 分档
 * 同一阈值源，分档文案不走新造标准）。此前 CPU 97% 显示「正常」—— 提示列
 * 只看在线/陈旧，不看水位，恰是最需要人看的那台机器被漏报。
 */
export function deviceIssueText(dev: DeviceOverviewLike, thresholdSec?: number): string {
  const parts: string[] = []
  if (dev.error) parts.push(`趋势数据获取失败：${dev.error}`)
  if (!dev.online) {
    parts.push(thresholdSec ? `已离线（超过 ${thresholdSec} 秒未上报）` : '已离线')
  } else if (dev.stale) {
    // 在线但陈旧是最容易被忽略的一种：心跳正常所以「看似健康」，
    // 但指标已停更，图上是一条平的直线，极易被误读为「负载稳定」。
    parts.push('指标数据已陈旧（心跳正常但采样未更新）')
  }
  const hot = usageHotspot(dev.watermark)
  if (hot) parts.push(hot)
  if (!dev.watermarkAt && dev.online && !hot) parts.push('尚无指标快照')
  return parts.join('；')
}

/**
 * 水位热点文案（无热点返回空串）。
 *
 * 阈值与 usageTone 同源（80 高 / 90 告警），此处只取提示列关心的三个水位：
 * CPU / 内存 / 磁盘。只报**最高**的一档（一行装不下三个并列的「高」），
 * 缺值（null）不参与 —— 没采到不等于 0。
 */
export function usageHotspot(watermark: Record<string, number> | undefined): string {
  if (!watermark) return ''
  const watched: Array<[string, string]> = [
    [COL.cpu, 'CPU'],
    [COL.memPercent, '内存'],
    [COL.diskUsedPercent, '磁盘']
  ]
  let name = ''
  let value = -Infinity
  for (const [col, label] of watched) {
    const v = watermark[col]
    if (typeof v !== 'number' || !Number.isFinite(v)) continue
    if (v > value) {
      value = v
      name = label
    }
  }
  if (value >= 90) return `${name}使用率 ${Math.round(value)}%（告警）`
  if (value >= 80) return `${name}使用率 ${Math.round(value)}%（高）`
  return ''
}

// ── 展示格式化 ────────────────────────────────────────────

/**
 * 格式化单个数值（用于快照卡片与表格），缺值一律「—」。
 *
 * 单位换算**不在本文件**：委托给 `column-meta.formatMetricText`（→
 * `display.formatByUnit`），那是全站唯一映射。此处曾自己 switch 一遍单位，
 * 结果是总览页与详情页对同一条指标给出不同精度与单位（`1795 GB` vs `1.75 TB`、
 * `7523.4 MB` vs `7.34 GB`）。
 */
export function formatMetric(column: string, value: number | null | undefined): string {
  return formatMetricText(column, value)
}

// ── 存量图（磁盘容量）派生 ─────────────────────────────────

/**
 * 存量快照行 → 堆叠柱两段：已用 / 可用（= total − used）。
 *
 * 可用段**只在该行同时有 total 与 used 时**产出：只有 total 时画不出「已用
 * 占比」，只有 used 时剩余量不可知 —— 两段残缺会让柱高失去「总容量」语义，
 * 宁可不画（该设备不进图，缺值语义与快照表一致）。`total < used`（数据
 * 翻转，如换盘瞬间）返回 null 段，同样不画，不夹成 0。
 */
export function capacitySegments(row: OverviewSnapshotRow): {
  used: number | null
  free: number | null
} | null {
  const total = row.values[COL.diskTotalGB]
  const used = row.values[COL.diskUsedGB]
  if (total === undefined || used === undefined) return null
  if (!Number.isFinite(total) || !Number.isFinite(used) || total < used) return null
  return { used, free: total - used }
}

/**
 * 指标元数据查询（供渲染层取中文名/单位/分组）。
 *
 * 直接复用设备详情页的 `METRIC_META`：两页对同一列必须显示同一个中文名，
 * 否则用户会以为它们是两个不同的指标。
 */
export function overviewMetricLabel(column: string): string {
  return metricLabel(column)
}

export function overviewMetricUnit(column: string): string {
  return metricUnit(column)
}

export function overviewMetricGroup(column: string): string {
  return METRIC_META[column]?.group ?? '其他'
}

// ── 概览条（页面级 KPI）────────────────────────────────────

/**
 * 概览条的一项。
 *
 * `icon` / `tile` / `tile2` 用于渲染成**服务监控同款 KPI 磁贴**（图标方块 +
 * 数值 + 标签 + 说明）：图标方块用 `tile -> tile2` 的 135 度渐变，与
 * `system-monitor/views/server.vue` 的 kpi-tile__icon 同一套视觉语法。
 *
 * 配色语义（与服务监控保持一致，不另立一套）：
 *   蓝 #3b82f6 中性/总量, 绿 #10b981 正常/在线, 琥珀 #f59e0b 需留意,
 *   红 #ef4444 异常, 灰 #94a3b8 未启用(中性,不带情绪)。
 */
export interface OverviewStat {
  key: string
  label: string
  value: number
  /** 是否处于告警态（渲染层据此给数值着色）。 */
  alert: boolean
  /** 补充说明（如阈值）。 */
  hint?: string
  /** Remix Icon 名（服务监控磁贴同款写法）。 */
  icon: string
  /** 图标方块渐变起始色。 */
  tile: string
  /** 图标方块渐变结束色。 */
  tile2: string
}

/**
 * 概览磁贴的图标与配色。**集中一处**，避免 5 个磁贴各写一遍渐变色而漂移
 * （monitor-tokens.scss 顶部记录的正是「复制四份导致 22px/20px 漂移」的教训）。
 */
const STAT_TILE = {
  total: { icon: 'ri:server-line', tile: '#3b82f6', tile2: '#60a5fa' },
  online: { icon: 'ri:checkbox-circle-line', tile: '#10b981', tile2: '#34d399' },
  offline: { icon: 'ri:cloud-off-line', tile: '#ef4444', tile2: '#f87171' },
  stale: { icon: 'ri:history-line', tile: '#f59e0b', tile2: '#fbbf24' },
  disabled: { icon: 'ri:pause-circle-line', tile: '#94a3b8', tile2: '#cbd5e1' }
} as const

/**
 * 由后端 summary 构造页面级概览项。
 *
 * 数值**全部来自后端**（`summary`），前端不重新统计 —— 因为「在线」的判定
 * 依赖可热更的离线阈值，前端自行用「now - lastSeenAt < 30s」会与后端漂移，
 * 而漂移的表现是「概览条说 5 台在线，列表里 4 台亮着」这种自相矛盾的画面。
 *
 * 阈值随响应下发（`offline_threshold_sec`），故文案可以如实引用它。
 */
export function buildOverviewStats(summary: {
  total: number
  online: number
  offline: number
  disabled: number
  stale: number
  offline_threshold_sec: number
}): OverviewStat[] {
  return [
    { key: 'total', label: '设备总数', value: summary.total, alert: false, ...STAT_TILE.total },
    { key: 'online', label: '在线', value: summary.online, alert: false, ...STAT_TILE.online },
    {
      key: 'offline',
      label: '离线',
      value: summary.offline,
      alert: summary.offline > 0,
      hint: `超过 ${summary.offline_threshold_sec} 秒未上报`,
      ...STAT_TILE.offline
    },
    {
      key: 'stale',
      label: '数据陈旧',
      value: summary.stale,
      alert: summary.stale > 0,
      hint: '心跳正常但指标未更新',
      ...STAT_TILE.stale
    },
    {
      key: 'disabled',
      label: '已停用',
      value: summary.disabled,
      alert: false,
      ...STAT_TILE.disabled
    }
  ]
}

/**
 * 「异常设备」数：离线 ∪ 数据陈旧 ∪ 趋势取数失败。
 *
 * 用**并集**（Set）而不是相加：一台设备可以同时离线且取数失败，
 * 相加会把同一台机器数两次，给出一个无法解释的数字。
 */
export function abnormalDeviceCount(devices: DeviceOverviewLike[]): number {
  const ids = new Set<string>()
  for (const d of devices) {
    if (!d.online || d.stale || d.error) ids.add(d.id)
  }
  return ids.size
}
