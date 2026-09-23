/**
 * device 指标列 / 资源种类的**展示元数据**（中文名、单位、分组、方向）。
 *
 * ── 为什么是前端映射表，而不是接口字段（对应设计文档 I-4/F-8）────────
 *
 * 后端的唯一事实源是 `available_metrics`（列名集合，snake_case）。它保证
 * **哪些列可选**，但不给任何展示信息。同时后端刻意**不能**直接改名：
 * 列名是子表/宽表的真实 DB 列名，改动会波及投影白名单、反射守卫测试与
 * 已落库的数据。
 *
 * 于是分工是：
 *   - 「**有没有**这一列」→ 后端 `available_metrics`（前端**绝不**硬编码）；
 *   - 「这一列**叫什么、什么单位**」→ 本表（纯展示，缺失即回退原名）。
 *
 * 这条分工是刻意的：本表**多**一列不会有副作用（唯一后果是列名能显示中文），
 * 本表**少**一列也不会隐藏数据（回退显示 `cpu_used_percent` 这种原名）。
 * 反过来，若把「有没有」也放进本表，后端新增一列就会被前端静默吞掉。
 *
 * 映射表未命中时的行为统一为：`label = 原名`、`unit = ''`、`group = '其他'`。
 */

/** 指标列的展示分组（下拉分组顺序 = 本数组顺序）。 */
export const METRIC_GROUP_ORDER = ['CPU', '内存', '磁盘', '网络', '进程', '其他'] as const

export type MetricGroup = (typeof METRIC_GROUP_ORDER)[number]

export interface MetricMeta {
  /** 中文展示名。 */
  label: string
  /** 单位（显示在主值后；无量纲用空串）。 */
  unit: string
  /** 分组（下拉里归类用）。 */
  group: MetricGroup
  /**
   * 是否为「越大越需要关注」的量。
   *
   * 注意它**不**用于着色：本项目的着色口径是「水位百分比」阈值
   * （见 usageTone），而像 `disk_io_read_bytes_sec` 这类吞吐量并**没有**
   * 通用阈值，硬套会把正常流量染成告警色。故该字段仅用于图例排序与提示，
   * 不参与配色。
   */
  higherIsBusier: boolean
}

/**
 * 指标列元数据表。键 = 后端 `available_metrics` 里的**原始列名**。
 *
 * 覆盖两套列名（它们**不同名**，这是既有事实而非笔误）：
 *   - 整机宽表（spec §5.5）：`cpu_used_percent` / `mem_used_percent` / …
 *   - 下钻子表：`used_percent` / `read_bytes_per_sec` / …
 * 两套都在表内，靠键名区分即可，调用方不需要知道自己查的是哪一套。
 */
export const METRIC_META: Record<string, MetricMeta> = {
  // ── 整机宽表 · CPU ────────────────────────────────
  cpu_used_percent: { label: 'CPU 使用率', unit: '%', group: 'CPU', higherIsBusier: true },
  cpu_iowait: { label: 'CPU IO 等待', unit: '%', group: 'CPU', higherIsBusier: true },
  load1: { label: '负载 1 分钟', unit: '', group: 'CPU', higherIsBusier: true },
  load5: { label: '负载 5 分钟', unit: '', group: 'CPU', higherIsBusier: true },
  load15: { label: '负载 15 分钟', unit: '', group: 'CPU', higherIsBusier: true },

  // ── 整机宽表 · 内存 ──────────────────────────────
  mem_used_percent: { label: '内存使用率', unit: '%', group: '内存', higherIsBusier: true },
  mem_used_mb: { label: '内存已用', unit: 'MB', group: '内存', higherIsBusier: true },
  mem_available_mb: { label: '内存可用', unit: 'MB', group: '内存', higherIsBusier: false },
  swap_used_percent: { label: '交换分区使用率', unit: '%', group: '内存', higherIsBusier: true },
  swap_used_mb: { label: '交换分区已用', unit: 'MB', group: '内存', higherIsBusier: true },

  // ── 整机宽表 · 磁盘 ──────────────────────────────
  disk_total_gb: { label: '磁盘总容量', unit: 'GB', group: '磁盘', higherIsBusier: false },
  disk_used_gb: { label: '磁盘已用', unit: 'GB', group: '磁盘', higherIsBusier: true },
  disk_used_percent: { label: '磁盘使用率', unit: '%', group: '磁盘', higherIsBusier: true },
  disk_io_read_bytes_sec: {
    label: '磁盘读速率',
    unit: 'B/s',
    group: '磁盘',
    higherIsBusier: true
  },
  disk_io_write_bytes_sec: {
    label: '磁盘写速率',
    unit: 'B/s',
    group: '磁盘',
    higherIsBusier: true
  },

  // ── 整机宽表 · 网络 ──────────────────────────────
  nic_rx_bytes_sec: { label: '网卡接收速率', unit: 'B/s', group: '网络', higherIsBusier: true },
  nic_tx_bytes_sec: { label: '网卡发送速率', unit: 'B/s', group: '网络', higherIsBusier: true },

  // ── 整机宽表 · 进程 / 连接 / 其他 ─────────────────
  proc_count: { label: '进程数', unit: '', group: '进程', higherIsBusier: false },
  uptime_sec: { label: '开机时长', unit: 's', group: '进程', higherIsBusier: false },
  tcp_total: { label: 'TCP 连接总数', unit: '', group: '网络', higherIsBusier: false },
  tcp_established: { label: 'TCP 已建立', unit: '', group: '网络', higherIsBusier: false },
  tcp_listen: { label: 'TCP 监听中', unit: '', group: '网络', higherIsBusier: false },
  max_temperature_c: { label: '最高温度', unit: '°C', group: '其他', higherIsBusier: true },
  samples: { label: '样本数', unit: '', group: '其他', higherIsBusier: false },

  // ── 下钻子表 · 磁盘分区（disk）────────────────────
  used_percent: { label: '使用率', unit: '%', group: '磁盘', higherIsBusier: true },
  used_gb: { label: '已用容量', unit: 'GB', group: '磁盘', higherIsBusier: true },
  total_gb: { label: '总容量', unit: 'GB', group: '磁盘', higherIsBusier: false },
  inodes_used_percent: { label: 'inode 使用率', unit: '%', group: '磁盘', higherIsBusier: true },
  free_gb: { label: '可用容量', unit: 'GB', group: '磁盘', higherIsBusier: false },

  // ── 下钻子表 · 磁盘 IO（disk_io）──────────────────
  read_bytes_per_sec: { label: '读速率', unit: 'B/s', group: '磁盘', higherIsBusier: true },
  write_bytes_per_sec: { label: '写速率', unit: 'B/s', group: '磁盘', higherIsBusier: true },
  read_ops_per_sec: { label: '读 IOPS', unit: '次/s', group: '磁盘', higherIsBusier: true },
  write_ops_per_sec: { label: '写 IOPS', unit: '次/s', group: '磁盘', higherIsBusier: true },
  io_time_percent: { label: 'IO 繁忙度', unit: '%', group: '磁盘', higherIsBusier: true },

  // ── 下钻子表 · 网卡（nic）────────────────────────
  rx_bytes_per_sec: { label: '接收速率', unit: 'B/s', group: '网络', higherIsBusier: true },
  tx_bytes_per_sec: { label: '发送速率', unit: 'B/s', group: '网络', higherIsBusier: true },
  rx_packets_per_sec: { label: '接收包速率', unit: '包/s', group: '网络', higherIsBusier: true },
  tx_packets_per_sec: { label: '发送包速率', unit: '包/s', group: '网络', higherIsBusier: true },
  rx_errors_per_sec: { label: '接收错误', unit: '次/s', group: '网络', higherIsBusier: true },
  tx_errors_per_sec: { label: '发送错误', unit: '次/s', group: '网络', higherIsBusier: true },
  rx_dropped_per_sec: { label: '接收丢包', unit: '次/s', group: '网络', higherIsBusier: true },
  tx_dropped_per_sec: { label: '发送丢包', unit: '次/s', group: '网络', higherIsBusier: true },

  // ── 下钻子表 · 传感器（sensor）────────────────────
  temperature_c: { label: '温度', unit: '°C', group: '其他', higherIsBusier: true }
}

/** 未命中时的兜底元数据（**必须**保留原名，否则用户完全认不出这一列）。 */
export function metricMeta(column: string): MetricMeta {
  return (
    METRIC_META[column] ?? {
      label: column,
      unit: '',
      group: '其他',
      higherIsBusier: false
    }
  )
}

/**
 * 指标列 → 中文展示名（未登记时回退列名本身）。
 *
 * 回退到原名而非「未知指标」是刻意的：原名至少能让人去后端列清单里搜到，
 * 「未知指标」则什么都提供不了。
 */
export function metricLabel(column: string): string {
  return metricMeta(column).label
}

/** 指标列 → 单位（未登记为空串）。 */
export function metricUnit(column: string): string {
  return metricMeta(column).unit
}

/** 指标列 → 分组。 */
export function metricGroup(column: string): MetricGroup {
  return metricMeta(column).group
}

export interface MetricGroupBucket {
  group: MetricGroup
  columns: string[]
}

/**
 * 把列名按分组归并，供下拉分组渲染。
 *
 * 分组顺序固定为 METRIC_GROUP_ORDER，空分组**不返回**（调用方无需过滤）。
 * 组内顺序 = 传入 columns 的顺序（调用方通常传后端 available_metrics 的顺序，
 * 它是稳定且与后端列定义一致的）。
 */
export function groupMetricColumns(columns: readonly string[]): MetricGroupBucket[] {
  const byGroup = new Map<string, string[]>()
  for (const column of columns) {
    const g = metricGroup(column)
    const list = byGroup.get(g)
    if (list) list.push(column)
    else byGroup.set(g, [column])
  }
  return METRIC_GROUP_ORDER.filter((g) => byGroup.has(g)).map((g) => ({
    group: g,
    columns: byGroup.get(g) as string[]
  }))
}

/**
 * 悬浮框 / 图例里的单行文本：**中文名（单位）** + 值。
 *
 * ── 为什么要「翻译」而不是直接用 series 名 ────────────────────────
 *
 * `series.name` 是**列名本身**（snake_case，如 `cpu_used_percent`），
 * 因为它是 ECharts 做系列匹配的**标识** —— 面板的 `resolveSetOptionOpts`
 * 用 `frames.map(f => f.name)` 判断要不要 `replaceMerge`，单测也按列名断言。
 * 换成中文会让「列增删时旧折线真正消失」这条保证失效。
 *
 * 于是分工固定为：**标识用列名（稳定、唯一），展示用本模块的元数据**。
 * 这正是「悬浮框里全是英文」的成因与修法 —— 打印 `seriesName` 等于把
 * 内部标识直接泄露给了用户。
 *
 * 单位必须跟着中文名一起给出（如「磁盘读速率（B/s）」）：只给数字时，
 * 读图的人无法判断 1024 是 B/s 还是 KB/s —— 单位缺失是误读的主要来源。
 *
 * @param column   系列标识（列名）
 * @param rawValue 已格式化的值文本（缺值应为「—」）
 */
export function metricTipLine(column: string, rawValue: string): string {
  const unit = metricUnit(column)
  const label = unit ? `${metricLabel(column)}（${unit}）` : metricLabel(column)
  return `${label}: ${rawValue}`
}
