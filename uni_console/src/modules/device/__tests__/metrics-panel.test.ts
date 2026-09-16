import { describe, expect, it, vi } from 'vitest'

// 面板的 setup 块会经 `../api` → `@/utils/http` → store(模块级读 localStorage)
// 触达 node 环境不存在的东西。这里只测**普通块导出的纯函数**，故把副作用链
// 挡在门外（与仓库既有的 auth-permission 注释同一理由）。
vi.mock('@/utils/http', () => ({ default: { get: vi.fn(), post: vi.fn(), del: vi.fn() } }))
vi.mock('@/store/modules/setting', () => ({
  useSettingStore: () => ({ isDark: false, menuOpen: true, menuType: 'left' })
}))
vi.mock('@/store/modules/user', () => ({
  useUserStore: () => ({ info: { permissions: [] }, accessToken: '', refreshToken: '' })
}))

import {
  BUCKET_TS_COLUMN,
  DRILL_MAX_RANGE_SECONDS,
  buildSeries,
  defaultColumns,
  formatAxisTick,
  formatResolution,
  rangeOptions,
  resourceRows,
  trendRows
} from '../components/metrics-panel.vue'

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
})

describe('rangeOptions · 档位约束（>30d 在下钻禁用）', () => {
  it('整机含 90d/180d 且全部可用', () => {
    const opts = rangeOptions(false)
    expect(opts.map((o) => o.seconds)).toEqual([3600, 86400, 604800, 2592000, 7776000, 15552000])
    expect(opts.every((o) => !o.disabled)).toBe(true)
  })

  it('下钻禁用 >30d，但 30d（2592000）本身仍可用', () => {
    const opts = rangeOptions(true)
    // 2592000 恰好是 30d 且仍是 5m 档（后端 off-by-one 已实测）→ 必须**可用**。
    expect(opts.find((o) => o.seconds === DRILL_MAX_RANGE_SECONDS)?.disabled).toBe(false)
    const disabled = opts.filter((o) => o.disabled)
    expect(disabled.map((o) => o.seconds)).toEqual([7776000, 15552000])
    // 禁用项必须给出**原因**（不能让用户点了再吃 400）。
    expect(disabled.every((o) => o.disabledReason.length > 0)).toBe(true)
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
