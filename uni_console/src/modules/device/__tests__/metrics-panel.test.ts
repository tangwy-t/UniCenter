import { describe, expect, it } from 'vitest'

// Task 5：纯逻辑已搬到 `../utils/metrics`（一个**零副作用**的普通 TS 模块），
// 故不再需要 Task 4 为「经组件 setup 块触达 store/localStorage」而加的
// `vi.mock` 挡板 —— 那些 mock 现在覆盖不到任何真实依赖，留着反而是死代码。

import {
  BUCKET_TS_COLUMN,
  DRILL_MAX_RANGE_SECONDS,
  buildSeries,
  defaultColumns,
  formatAxisTick,
  formatMetricValue,
  formatResolution,
  rangeOptions,
  resourceRows,
  trendRows
} from '../utils/metrics'

const BASE = 1758000000 // 一个固定的 unix 秒基准

describe('buildSeries · 稀疏桶必须用 t 定位（纪律 2 断言 1）', () => {
  it('X 轴是三个真实时间点，而非「三个等距点」', () => {
    // t 有洞：base、base+900、base+3600（中间缺了 3 个 900s 桶）。
    const rows = [
      { t: BASE, cpu_used_percent: 10 },
      { t: BASE + 900, cpu_used_percent: 20 },
      { t: BASE + 3600, cpu_used_percent: 30 }
    ]
    const built = buildSeries(rows, ['cpu_used_percent'], ['cpu_used_percent'])
    expect(built.series).toHaveLength(1)

    const xs = built.series[0].points.map(([x]) => x)
    // 真实时刻（ms）：相邻间隔 900s 与 2700s，**不相等**。
    expect(xs).toEqual([BASE * 1000, (BASE + 900) * 1000, (BASE + 3600) * 1000])
    const gaps = xs.slice(1).map((x, i) => x - xs[i])
    expect(gaps).toEqual([900 * 1000, 2700 * 1000])
    // 若按下标推算（index × resolution），三点会等距 —— 这条必须不成立。
    expect(gaps[0]).not.toBe(gaps[1])
  })

  it('空桶（后端不产行）保持 null，不被连成直线', () => {
    const rows = [
      { t: BASE, cpu_used_percent: 10 },
      { t: BASE + 900 } // 该桶无 cpu 样本：键不存在（后端不写 nil 列）
    ]
    const built = buildSeries(rows, ['cpu_used_percent'], ['cpu_used_percent'])
    expect(built.series[0].points.map(([, y]) => y)).toEqual([10, null])
  })
})

describe('buildSeries · available_metrics 驱动（纪律 2 断言 2）', () => {
  it('「不在 available_metrics」与「未采集」是两种可区分的状态', () => {
    const rows = [{ t: BASE, cpu_used_percent: 10, load1: null }]
    // 请求两列：cpu 在可用集内（但本桶有值）、load1 在可用集内但该桶为 null；
    // tcp_time_wait 不在可用集内（该档位不产此列）。
    const built = buildSeries(
      rows,
      ['cpu_used_percent', 'load1', 'tcp_time_wait'],
      ['cpu_used_percent', 'load1']
    )

    // 「未采集」：列在 available_metrics 内 → 有 series，值为 null。
    const load1 = built.series.find((s) => s.name === 'load1')
    expect(load1).toBeDefined()
    expect(load1?.points.map(([, y]) => y)).toEqual([null])

    // 「该档位无此指标」：列**不在** available_metrics 内 → **不产生 series**，
    // 而是进入 unavailableColumns（UI 显示「该档位无此指标」）。
    // 反向验证点：若按「不在 available_metrics 的也当未采集」实现，
    // 这里会出现一条恒为 null 的 series，且 unavailableColumns 为空 → 变红。
    expect(built.unavailableColumns).toEqual(['tcp_time_wait'])
    expect(built.series.map((s) => s.name)).not.toContain('tcp_time_wait')
  })

  it('bucket_ts 是桶键，绝不作为指标列进入系列', () => {
    const rows = [{ t: BASE, bucket_ts: BASE, cpu_used_percent: 1 }]
    const built = buildSeries(
      rows,
      [BUCKET_TS_COLUMN, 'cpu_used_percent'],
      [BUCKET_TS_COLUMN, 'cpu_used_percent']
    )
    expect(built.series.map((s) => s.name)).toEqual(['cpu_used_percent'])
    expect(built.unavailableColumns).toEqual([])
  })

  it('bucket_ts 即使被列进 available_metrics（后端 withBucketTS 恒补）也必须剔除', () => {
    // 后端 `withBucketTS` 会**恒补** bucket_ts 到 available_metrics 里，
    // 故它一定出现在响应的列集内。它必须走「既不是指标、也不算无此指标」
    // 这条路：不渲染、不进 unavailableColumns、更不占用 x 轴。
    const rows = [{ t: BASE, bucket_ts: BASE - 3600, cpu_used_percent: 7 }]
    const built = buildSeries(
      rows,
      ['bucket_ts', 'cpu_used_percent'],
      ['bucket_ts', 'cpu_used_percent']
    )
    expect(built.series.map((s) => s.name)).toEqual(['cpu_used_percent'])
    // 剔除的桶键**不得**混进「该档位无此指标」（它不是指标，谈不上「无此指标」）。
    expect(built.unavailableColumns).toEqual([])
    // x 轴取 `t`，不是 bucket_ts：否则时间轴会被桶键（另一列）带偏。
    expect(built.series[0].points.map(([x]) => x)).toEqual([BASE * 1000])
  })
})

describe('buildSeries · 列存在性用 `in` 判断（Task 1 报告点名的坑）', () => {
  it('「键存在且有值 / 键存在但值为 null / 键缺失」三态都不抛错且可判未采集', () => {
    // 契约：`DeviceResourcePoint.values?: Record<string, number|null>` 是**可选**的，
    // 而后端保证「值为 nil 的列不出现」（omitempty）。故前端可能收到三种形状，
    // 存在性判断**必须**是 `col in (values ?? {})`，而不是 `!= null`。
    const rows = [
      { t: BASE, a: 1 }, // 键存在、有数值 → 有样本
      { t: BASE + 900, a: null }, // 键存在、值为 null → 未采集
      { t: BASE + 1800 } // 键缺失（后端不写 nil 列）→ 未采集
    ]
    // 连 `t` 都缺的异常形状不得把函数打崩。
    expect(() => buildSeries([{}], ['a'], ['a'])).not.toThrow()

    const built = buildSeries(rows, ['a'], ['a'])
    const ys = built.series[0].points.map(([, y]) => y)
    expect(ys).toEqual([1, null, null])
    // 三条逐一钉住「未采集」谓词（y === null）。
    expect(ys[0] === null).toBe(false) // {a: 1}      → 有样本
    expect(ys[1] === null).toBe(true) // {a: null}   → 未采集
    expect(ys[2] === null).toBe(true) // {}（缺键）   → 未采集
  })

  it('值为 0 必须是样本，不得被当成「未采集」', () => {
    // `!= null` / truthiness 风格的实现会把 0 误判为缺值；`in` 判断不会。
    const built = buildSeries([{ t: BASE, a: 0 }], ['a'], ['a'])
    const ys = built.series[0].points.map(([, y]) => y)
    expect(ys).toEqual([0])
    expect(ys[0] === null).toBe(false)
  })

  it('下钻路径（resourceRows 摊平后）的 values 缺省同样不抛错', () => {
    const rows = resourceRows([
      { t: BASE, samples: 1, values: { used_percent: 42.5 } },
      { t: BASE + 300, samples: 0 } // values 缺省（omitempty）→ `?? {}` 后摊平
    ])
    expect(() => buildSeries(rows, ['used_percent'], ['used_percent'])).not.toThrow()
    const built = buildSeries(rows, ['used_percent'], ['used_percent'])
    expect(built.series[0].points.map(([, y]) => y)).toEqual([42.5, null])
    expect(built.unavailableColumns).toEqual([])
  })
})

describe('rangeOptions · 档位约束（>30d 在下钻禁用）', () => {
  it('整机含 90d/180d 且全部可用', () => {
    const opts = rangeOptions(false)
    expect(opts.map((o) => o.seconds)).toEqual([3600, 86400, 604800, 2592000, 7776000, 15552000])
    expect(opts.every((o) => !o.disabled)).toBe(true)
    // 整机口径必须真的包含 90d 与 180d 这两个 key。
    expect(opts.map((o) => o.key)).toEqual(['1h', '24h', '7d', '30d', '90d', '180d'])
  })

  it('下钻禁用 >30d，但 30d（2592000）本身仍可用', () => {
    const opts = rangeOptions(true)
    // 2592000 恰好是 30d 且仍是 5m 档（后端 off-by-one 已实测）→ 必须**可用**。
    expect(opts.find((o) => o.seconds === DRILL_MAX_RANGE_SECONDS)?.disabled).toBe(false)
    const disabled = opts.filter((o) => o.disabled)
    expect(disabled.map((o) => o.seconds)).toEqual([7776000, 15552000])
    // 禁用项必须给出**原因**（不能让用户点了再吃 400）。
    expect(disabled.every((o) => o.disabledReason.length > 0)).toBe(true)
    // 对称断言：**没有**任何 <=30d 的档位被禁用（含 30d 本身）。
    expect(opts.filter((o) => o.seconds <= DRILL_MAX_RANGE_SECONDS).map((o) => o.disabled)).toEqual(
      [false, false, false, false]
    )
  })
})

describe('formatResolution · 只信 resolution_seconds', () => {
  it('按秒数推导粒度文案', () => {
    expect(formatResolution(10)).toBe('每 10 秒')
    expect(formatResolution(300)).toBe('每 5 分钟')
    expect(formatResolution(900)).toBe('每 15 分钟')
    expect(formatResolution(3600)).toBe('每 1 小时')
    expect(formatResolution(7200)).toBe('每 2 小时')
    expect(formatResolution(0)).toBe('—')
  })
})

describe('formatAxisTick · 刻度粒度由 range 推导', () => {
  it('1d 内到分钟，1w 内到「日 时:分」，更长到日期', () => {
    const ms = new Date(2026, 8, 16, 13, 5, 0).getTime()
    expect(formatAxisTick(ms, 86400)).toBe('13:05')
    expect(formatAxisTick(ms, 604800)).toBe('09-16 13:05')
    expect(formatAxisTick(ms, 15552000)).toBe('2026-09-16')
  })
})

describe('formatMetricValue · 缺值「—」而不是 0', () => {
  it('null/undefined 显示「—」，0 显示 0', () => {
    expect(formatMetricValue(null)).toBe('—')
    expect(formatMetricValue(undefined)).toBe('—')
    expect(formatMetricValue(0)).toBe('0')
  })
})

describe('桶 → 开放形状', () => {
  it('trendRows 保留 t 与 snake_case 值列', () => {
    const rows = trendRows([{ t: BASE, cpu_used_percent: 1.5, samples: 3 }])
    expect(rows[0].t).toBe(BASE)
    expect(rows[0].cpu_used_percent).toBe(1.5)
  })

  it('resourceRows 摊平 values，且 t 不被值列覆盖', () => {
    const rows = resourceRows([
      { t: BASE, samples: 2, values: { used_percent: 42.5 } },
      // values 可选（omitempty）→ 缺省时不得崩。
      { t: BASE + 300, samples: 0 }
    ])
    expect(rows[0].used_percent).toBe(42.5)
    expect(rows[0].t).toBe(BASE)
    expect(rows[1].t).toBe(BASE + 300)
    expect(rows[1].used_percent).toBeUndefined()
  })
})

describe('defaultColumns · 偏好集只是顺序，可用性仍由响应决定', () => {
  it('取偏好集与可用列的交集', () => {
    expect(defaultColumns(['cpu_used_percent', 'load1', 'max_temperature_c'])).toEqual([
      'cpu_used_percent',
      'load1',
      'max_temperature_c'
    ])
  })

  it('无交集时退化为可用列前几个（不返回空，避免空图）', () => {
    // 这些列名都不在偏好集内 → 必须回退，而不是给用户一个空图。
    expect(defaultColumns(['disk_total_gb', 'mem_used_mb', 'tcp_total', 'proc_count'])).toEqual([
      'disk_total_gb',
      'mem_used_mb',
      'tcp_total',
      'proc_count'
    ])
  })
})
