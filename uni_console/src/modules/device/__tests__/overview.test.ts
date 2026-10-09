import { readFileSync } from 'node:fs'

import { describe, expect, it } from 'vitest'

import {
  abnormalDeviceCount,
  assignColumns,
  buildOverviewCharts,
  buildOverviewStats,
  capacitySegments,
  CHART_BLOCKS,
  COL,
  countPresent,
  deviceIssueText,
  drawableChartCount,
  FALLBACK_CHART_KEY,
  formatMetric,
  hasDrawableSeries,
  isEmptyResult,
  lastValue,
  overviewState,
  seriesPoints,
  TOP_N_ALL,
  usageHotspot,
  type DeviceOverviewLike
} from '../utils/overview'

/**
 * 本文件守的是总览页最容易出错、且出错后**看起来很正常**的几条契约。
 * 每条用例的注释都写明「错了会看到什么」，因为那正是需要防住的东西。
 */

// ── 测试数据 ───────────────────────────────────────────────

/** 构造一台设备（series 由 列名→值数组 的映射生成，present 自动算出）。 */
function dev(
  id: string,
  hostname: string,
  seriesData: Record<string, (number | null)[]>,
  extra: Partial<DeviceOverviewLike> = {}
): DeviceOverviewLike {
  return {
    id,
    hostname,
    online: true,
    stale: false,
    series: Object.entries(seriesData).map(([metric, values]) => ({
      metric,
      values,
      present: countPresent(values),
      missing: values.filter((v) => v === null).length
    })),
    ...extra
  }
}

const AXIS = [1700000000, 1700000300, 1700000600]

// ── 列分配：不重复、不遗漏 ─────────────────────────────────

describe('assignColumns', () => {
  it('同一列只会被第一个声明它的图块取走', () => {
    // 若实现改成「每个图块各自 filter available」，同一列会同时出现在
    // CPU 图与兜底图里 —— 页面上会看到两处画同一个指标，而 total 计数却对不上。
    const { assigned, leftover } = assignColumns([COL.cpu, COL.cpuIowait, COL.load1])

    expect(assigned.get('cpu')).toEqual([COL.cpu, COL.cpuIowait])
    expect(assigned.get('load')).toEqual([COL.load1])
    expect(leftover).toEqual([])
    // 全量核对：所有被分配的列互不重叠
    const all = [...assigned.values()].flat()
    expect(new Set(all).size).toBe(all.length)
  })

  it('后端不产的列不会出现在任何图块里', () => {
    // 设备不产 swap / 温度时，对应图块拿不到列 → 上层会整块隐藏，
    // 而不是画一张 Y 轴为空的图（那看起来像「页面坏了」）。
    const { assigned } = assignColumns([COL.cpu])
    expect(assigned.get('cpu')).toEqual([COL.cpu])
    expect(assigned.get('swap')).toEqual([])
    expect(assigned.get('temp')).toEqual([])
  })

  it('未被任何图块声明的列进入 leftover（不静默丢数据）', () => {
    const { leftover } = assignColumns([COL.cpu, 'brand_new_column_from_backend'])
    expect(leftover).toEqual(['brand_new_column_from_backend'])
  })

  it('列顺序跟随图块声明顺序（而非 available 顺序）', () => {
    // 顺序会影响图例与配色的稳定性：跟随 available 会让「后端列顺序变了」
    // 导致前端配色跳变，用户会以为换了指标。
    const { assigned } = assignColumns([COL.cpuIowait, COL.cpu])
    expect(assigned.get('cpu')).toEqual([COL.cpu, COL.cpuIowait])
  })
})

// ── 图表构建 ───────────────────────────────────────────────

describe('buildOverviewCharts', () => {
  it('每个有数据的列都恰好出现一次（不遗漏、不重复）', () => {
    // 这是总览页**最核心**的验收口径。把 available 全集丢进去，
    // 断言「所有图块的 columns 之并 = available」且「两两不相交」。
    const available = Object.values(COL).slice()
    const d = dev('1', 'host-a', Object.fromEntries(available.map((c) => [c, [1, 2, 3]])))
    const charts = buildOverviewCharts(AXIS, available, [d])

    const seen = charts.flatMap((c) => c.columns)
    expect(new Set(seen).size).toBe(seen.length) // 无重复
    // 无遗漏：每个可用列都被某张图消费（兜底图也会收走剩余的）
    for (const col of available) {
      expect(seen, `列 ${col} 未被任何图表消费`).toContain(col)
    }
  })

  it('后端新增未声明列时落入「其他指标」兜底图', () => {
    const d = dev('1', 'host-a', { cpu_used_percent: [1, 2, 3], newly_added_metric: [7, 8, 9] })
    const charts = buildOverviewCharts(AXIS, ['cpu_used_percent', 'newly_added_metric'], [d])

    const fallback = charts.find((c) => c.key === FALLBACK_CHART_KEY)
    expect(fallback, '未声明列必须进兜底图，否则后端加列会被前端静默丢弃').toBeTruthy()
    expect(fallback!.columns).toEqual(['newly_added_metric'])
  })

  it('整列无数据的设备不进图例', () => {
    // 若不跳过，图例会被「host · 交换分区」这类空系列淹没，
    // 用户无法从图例判断哪台设备真有数据。
    const withData = dev('1', 'a', { cpu_used_percent: [10, 20, 30] })
    const noData = dev('2', 'b', { cpu_used_percent: [null, null, null] })
    const charts = buildOverviewCharts(AXIS, ['cpu_used_percent'], [withData, noData])

    const cpu = charts.find((c) => c.key === 'cpu')!
    expect(cpu.series).toHaveLength(1)
    expect(cpu.series[0].deviceId).toBe('1')
    // 但「设备总数」仍如实反映 2 台，故页面能说「2 台中有 1 台有数据」
    expect(cpu.devicesTotal).toBe(2)
    expect(cpu.devicesWithData).toBe(1)
  })

  it('多列图块的系列名 = 主机名 · 指标名（否则分不清 1m/5m/15m 负载）', () => {
    // CPU 图块声明了 cpu_used_percent + cpu_iowait 两列 → 必须带指标名。
    const charts = buildOverviewCharts(
      AXIS,
      [COL.cpu, COL.cpuIowait, COL.load1],
      [
        dev('1', 'alpha', { cpu_used_percent: [1, 2, 3], load1: [0.4, 0.5, 0.6] }),
        dev('2', 'beta', { cpu_used_percent: [4, 5, 6] })
      ]
    )
    const cpuNames = charts.find((c) => c.key === 'cpu')!.series.map((s) => s.name)
    expect(cpuNames).toContain('alpha · CPU 使用率')
    expect(cpuNames).toContain('beta · CPU 使用率')
  })

  it('单列图块的系列名只写主机名（图例拥挤的直接对策）', () => {
    // 「磁盘使用率趋势」只有 1 列 → 整张图都是同一指标，再给每台设备加
    // 「· 磁盘使用率」是纯噪声，会把图例挤到换行/分页（总览常同时画十余台）。
    // 注意顺序：系列按**该列最新值降序**（top-N 排名序），beta 最新值 22 > alpha 12。
    const charts = buildOverviewCharts(
      AXIS,
      [COL.diskUsedPercent],
      [
        dev('1', 'alpha', { disk_used_percent: [10, 11, 12] }),
        dev('2', 'beta', { disk_used_percent: [20, 21, 22] })
      ]
    )
    const names = charts.find((c) => c.key === 'disk-usage')!.series.map((s) => s.name)
    expect(names).toEqual(['beta', 'alpha'])
  })

  it('无数据可画时返回空图表列表（供页面渲染显式空态）', () => {
    expect(buildOverviewCharts([], [], [])).toEqual([])
    // 有设备但该设备这些列全无数据 → 图块被过滤掉，而不是渲染一屏空坐标轴
    expect(buildOverviewCharts(AXIS, ['cpu_used_percent'], [dev('1', 'a', {})])).toEqual([])
    expect(
      buildOverviewCharts(
        AXIS,
        ['cpu_used_percent'],
        [dev('1', 'a', { cpu_used_percent: [null, null, null] })]
      )
    ).toEqual([])
  })

  it('drawableChartCount 区分「该档有列」与「真有数据可画」', () => {
    // 这是空态判定的依据：图块数恒 > 0（只要有可用列），拿它判空会让空态永不触发。
    const available = Object.values(COL).slice()
    const empty = dev('1', 'a', Object.fromEntries(available.map((c) => [c, [null, null, null]])))
    const chartSpecs = buildOverviewCharts(AXIS, available, [empty])
    expect(chartSpecs).toEqual([])
    expect(drawableChartCount(chartSpecs)).toBe(0)
    expect(isEmptyResult(1, drawableChartCount(chartSpecs))).toBe(true)

    const ok = dev('1', 'a', { cpu_used_percent: [1, 2, 3] })
    const charts = buildOverviewCharts(AXIS, available, [ok])
    expect(drawableChartCount(charts)).toBeGreaterThan(0)
    expect(isEmptyResult(1, drawableChartCount(charts))).toBe(false)
  })

  it('图表类型按图块声明保持不变（选型不被渲染层改写）', () => {
    const available = Object.values(COL).slice()
    const d = dev('1', 'a', Object.fromEntries(available.map((c) => [c, [1, 2, 3]])))
    const charts = buildOverviewCharts(AXIS, available, [d])
    const byKey = Object.fromEntries(charts.map((c) => [c.key, c.type]))

    expect(byKey['cpu']).toBe('line')
    expect(byKey['load']).toBe('line')
    expect(byKey['disk-capacity']).toBe('stacked')
    expect(byKey['disk-io']).toBe('area')
    expect(byKey['nic']).toBe('area')
    expect(byKey['tcp']).toBe('line')
  })

  it('每个图块都带 purpose 与 rationale（选型理由必须可展示）', () => {
    const available = Object.values(COL).slice()
    const d = dev('1', 'a', Object.fromEntries(available.map((c) => [c, [1, 2, 3]])))
    for (const c of buildOverviewCharts(AXIS, available, [d])) {
      expect(c.purpose.length, `${c.key} 缺 purpose`).toBeGreaterThan(0)
      expect(c.rationale.length, `${c.key} 缺 rationale`).toBeGreaterThan(0)
    }
  })
})

// ── top-N：100 台规模下的曲线裁剪 ───────────────────────────

describe('buildOverviewCharts · top-N 选取', () => {
  /** 造 n 台设备，cpu 最新值 = i（i 越大越靠前）。 */
  function fleet(n: number, col = COL.cpu): DeviceOverviewLike[] {
    return Array.from({ length: n }, (_, i) => dev(String(i), `host-${i}`, { [col]: [i, i, i] }))
  }

  it('默认只画前 8 台（按第一列最新非空值降序）', () => {
    // 100 台全画只会糊成一片；快照表仍显示全部（见视图层）。
    const charts = buildOverviewCharts(AXIS, [COL.cpu], fleet(20))
    const cpu = charts.find((c) => c.key === 'cpu')!
    expect(cpu.series).toHaveLength(8)
    // 降序：最新值 19 的 host-19 排第一
    expect(cpu.series[0].hostname).toBe('host-19')
    expect(cpu.series[7].hostname).toBe('host-12')
    // 设备总数不受 top-N 影响（「N / M 台有数据」的分母口径）
    expect(cpu.devicesTotal).toBe(20)
  })

  it('字段缺失 / 全 null 的设备排最后，不被当成 0', () => {
    // 排名若把 null 当 0，缺值设备会插到「真实为 0」的设备前面 ——
    // 「没上报」在容量语境里恰恰是最该被看到的，排序不能把它埋掉。
    const devices = [
      dev('a', 'no-data', { cpu_used_percent: [null, null, null] }),
      dev('b', 'zero', { cpu_used_percent: [0, 0, 0] }),
      dev('c', 'high', { cpu_used_percent: [90, 90, 90] })
    ]
    const charts = buildOverviewCharts(AXIS, [COL.cpu], devices, 2)
    const names = charts.find((c) => c.key === 'cpu')!.series.map((s) => s.hostname)
    expect(names).toEqual(['high', 'zero'])
  })

  it('TOP_N_ALL 不裁剪（快照表配套的全量视图）', () => {
    const charts = buildOverviewCharts(AXIS, [COL.cpu], fleet(12), TOP_N_ALL)
    expect(charts.find((c) => c.key === 'cpu')!.series).toHaveLength(12)
  })

  it('top-N 不影响设备总数与「有数据」计数', () => {
    const devices = fleet(10)
    const charts = buildOverviewCharts(AXIS, [COL.cpu], devices, 3)
    const cpu = charts.find((c) => c.key === 'cpu')!
    expect(cpu.devicesTotal).toBe(10)
    // 有数据的是全部 10 台（都上报了），画出来的是 3 台 —— 两个数必须分得清。
    expect(cpu.devicesWithData).toBe(10)
  })
})

// ── 磁盘容量：category 堆叠快照 ─────────────────────────────

describe('buildOverviewCharts · 磁盘容量（存量堆叠）', () => {
  it('stacked 图产出分类快照而非时间序列（每台一柱的语义要求）', () => {
    // 修复前：时间序列点被塞进堆叠柱 → 渲染成无意义的「总量线」。
    // 修复后：series 为空、snapshot 每台一行、值 = 该列最后一个非空值。
    const devices = [
      dev('1', 'a', { disk_used_gb: [10, 12, 11], disk_total_gb: [100, 100, 100] }),
      dev('2', 'b', { disk_used_gb: [50, 55, 60], disk_total_gb: [80, 80, 80] })
    ]
    const charts = buildOverviewCharts(AXIS, [COL.diskUsedGB, COL.diskTotalGB], devices)
    const cap = charts.find((c) => c.key === 'disk-capacity')!

    expect(cap.type).toBe('stacked')
    expect(cap.series).toEqual([]) // 不再是时间序列
    expect(cap.snapshot).toHaveLength(2)
    // 快照取**最后一个非空值**：a 的 used=11（不是 10），b 的 used=60
    const rowA = cap.snapshot.find((r) => r.deviceId === '1')!
    expect(rowA.values[COL.diskUsedGB]).toBe(11)
    expect(rowA.values[COL.diskTotalGB]).toBe(100)
  })

  it('快照按第一列（已用）降序 —— 最吃紧的排最前', () => {
    const devices = [
      dev('1', 'a', { disk_used_gb: [10, 10, 10], disk_total_gb: [100, 100, 100] }),
      dev('2', 'b', { disk_used_gb: [60, 60, 60], disk_total_gb: [80, 80, 80] })
    ]
    const cap = buildOverviewCharts(AXIS, [COL.diskUsedGB, COL.diskTotalGB], devices).find(
      (c) => c.key === 'disk-capacity'
    )!
    expect(cap.snapshot.map((r) => r.hostname)).toEqual(['b', 'a'])
  })

  it('top-N 同样约束存量图（柱多了与线一样糊）', () => {
    const fleet = Array.from({ length: 10 }, (_, i) =>
      dev(String(i), `h${i}`, {
        disk_used_gb: [i, i, i],
        disk_total_gb: [100, 100, 100]
      })
    )
    const cap = buildOverviewCharts(AXIS, [COL.diskUsedGB, COL.diskTotalGB], fleet, 5).find(
      (c) => c.key === 'disk-capacity'
    )!
    expect(cap.snapshot).toHaveLength(5)
    expect(cap.snapshot[0].hostname).toBe('h9') // 降序第一位
  })

  it('两列全缺的设备不进快照（缺值不是 0）', () => {
    const devices = [
      dev('1', 'a', { disk_used_gb: [1, 1, 1], disk_total_gb: [2, 2, 2] }),
      dev('2', 'b', { disk_used_gb: [null, null, null], disk_total_gb: [null, null, null] })
    ]
    const cap = buildOverviewCharts(AXIS, [COL.diskUsedGB, COL.diskTotalGB], devices).find(
      (c) => c.key === 'disk-capacity'
    )!
    expect(cap.snapshot).toHaveLength(1)
    expect(cap.snapshot[0].deviceId).toBe('1')
  })
})

describe('capacitySegments · 可用 = total − used', () => {
  it('两段齐全时 free = total − used', () => {
    const seg = capacitySegments({
      deviceId: '1',
      hostname: 'a',
      values: { disk_used_gb: 30, disk_total_gb: 100 }
    })
    expect(seg).toEqual({ used: 30, free: 70 })
  })

  it('只有一列（total 或 used）时不产出（柱高会失去「总容量」语义）', () => {
    expect(
      capacitySegments({ deviceId: '1', hostname: 'a', values: { disk_used_gb: 30 } })
    ).toBeNull()
    expect(
      capacitySegments({ deviceId: '1', hostname: 'a', values: { disk_total_gb: 100 } })
    ).toBeNull()
  })

  it('total < used（数据翻转）不产出、不夹成 0', () => {
    expect(
      capacitySegments({
        deviceId: '1',
        hostname: 'a',
        values: { disk_used_gb: 120, disk_total_gb: 100 }
      })
    ).toBeNull()
  })
})

// ── 按量纲分轴：混轴防「百分比贴 0」──────────────────────────

describe('buildOverviewCharts · 按量纲分轴（unitAxes）', () => {
  it('mem 块（% + MB）产出两个量纲组，主量纲（%）在前', () => {
    // 修复前：单 Y 轴下 MB（10^3 量级）把百分比（0~100）压成贴 0 的直线。
    // 修复后：每列挂到自己的量纲轴（渲染层见 overview-chart-card）。
    const devices = [
      dev('1', 'a', {
        mem_used_percent: [55, 56, 57],
        mem_available_mb: [4000, 3900, 3800]
      })
    ]
    const mem = buildOverviewCharts(AXIS, [COL.memPercent, COL.memAvailMB], devices).find(
      (c) => c.key === 'mem'
    )!
    expect(mem.unitAxes.map((g) => g.unit)).toEqual(['%', 'MB'])
  })

  it('proc 块（个 vs 秒）分轴：主量纲 = 先出现的列（proc_count）', () => {
    const devices = [
      dev('1', 'a', { proc_count: [200, 210, 220], uptime_sec: [80000, 81000, 82000] })
    ]
    const proc = buildOverviewCharts(AXIS, [COL.procCount, COL.uptimeSec], devices).find(
      (c) => c.key === 'proc'
    )!
    // 平票（各 1 列）时主量纲取先出现者 —— proc_count 占左轴。
    expect(proc.unitAxes.map((g) => g.unit)).toEqual(['', 's'])
  })

  it('单量纲块退化为单轴组（行为与一根 Y 轴一致）', () => {
    const devices = [dev('1', 'a', { disk_used_percent: [10, 20, 30] })]
    const du = buildOverviewCharts(AXIS, [COL.diskUsedPercent], devices).find(
      (c) => c.key === 'disk-usage'
    )!
    expect(du.unitAxes).toHaveLength(1)
    expect(du.unitAxes[0].unit).toBe('%')
  })
})

// ── lastValue：排名的取值口径 ───────────────────────────────

describe('lastValue', () => {
  it('取最后一个非空值（不是第一个、不是均值）', () => {
    expect(lastValue([10, null, 30])).toBe(30)
    expect(lastValue([10, 20, null])).toBe(20)
  })

  it('全 null / 缺数组 → null（绝不 0）', () => {
    expect(lastValue([null, null])).toBeNull()
    expect(lastValue(undefined)).toBeNull()
    expect(lastValue([])).toBeNull()
  })

  it('真实的 0 是合法最新值', () => {
    expect(lastValue([5, 0])).toBe(0)
  })
})

// ── 提示列接入水位阈值 ──────────────────────────────────────

describe('deviceIssueText · 水位阈值语义', () => {
  it('CPU ≥90 显示告警（修复前 97% 也显示「正常」）', () => {
    const txt = deviceIssueText(
      dev('1', 'a', {}, { watermarkAt: 123, watermark: { cpu_used_percent: 97 } })
    )
    expect(txt).toContain('CPU')
    expect(txt).toContain('97')
    expect(txt).toContain('告警')
  })

  it('≥80 且 <90 显示「高」', () => {
    const txt = deviceIssueText(
      dev('1', 'a', {}, { watermarkAt: 123, watermark: { mem_used_percent: 85 } })
    )
    expect(txt).toContain('内存')
    expect(txt).toContain('高')
    expect(txt).not.toContain('告警')
  })

  it('只报最高的一个水位（一行装不下三个并列的「高」）', () => {
    const txt = deviceIssueText(
      dev(
        '1',
        'a',
        {},
        {
          watermarkAt: 123,
          watermark: { cpu_used_percent: 95, mem_used_percent: 84, disk_used_percent: 91 }
        }
      )
    )
    // CPU 95 是三者最高 → 只提 CPU，不把三个水位都铺出来
    expect(txt).toContain('CPU')
    expect(txt).not.toContain('内存')
    expect(txt).not.toContain('磁盘')
  })

  it('水位全缺时不造「高」（null 不是 0）', () => {
    expect(usageHotspot(undefined)).toBe('')
    expect(usageHotspot({})).toBe('')
    expect(usageHotspot({ cpu_used_percent: 40, mem_used_percent: 60 })).toBe('')
  })

  it('有水位热点时不叠加「尚无指标快照」', () => {
    const txt = deviceIssueText(
      dev('1', 'a', {}, { watermarkAt: 123, watermark: { cpu_used_percent: 97 } })
    )
    expect(txt).not.toContain('尚无指标快照')
  })
})

// ── 缺值口径：最硬的一条 ───────────────────────────────────

describe('seriesPoints 缺值口径', () => {
  it('null 必须原样保留（绝不变成 0）', () => {
    // 若实现写成 `values[i] ?? 0`，图上「没数据」会画成「值为 0」——
    // 对 CPU/流量类指标这会被读成「机器很闲」，是最危险的误读。
    const pts = seriesPoints([100, 200, 300], [10, null, 30])
    expect(pts).toEqual([
      [100000, 10],
      [200000, null], // 必须仍是 null
      [300000, 30]
    ])
  })

  it('真实的 0 必须保留为 0（与缺失可区分）', () => {
    const pts = seriesPoints([100], [0])
    expect(pts[0][1]).toBe(0)
    expect(pts[0][1]).not.toBeNull()
  })

  it('values 短于 axis 时按缺失补齐，不错位', () => {
    // 错位会让时间轴说谎（把 A 时刻的值画到 B 时刻），比丢点危险得多。
    const pts = seriesPoints([100, 200, 300], [10])
    expect(pts).toEqual([
      [100000, 10],
      [200000, null],
      [300000, null]
    ])
  })

  it('values 长于 axis 时忽略多余部分', () => {
    const pts = seriesPoints([100, 200], [1, 2, 3, 4])
    expect(pts).toHaveLength(2)
  })

  it('values 缺失时整段为 null', () => {
    expect(seriesPoints([100, 200], undefined)).toEqual([
      [100000, null],
      [200000, null]
    ])
  })

  it('时间轴从秒转毫秒（ECharts time 轴要求）', () => {
    const pts = seriesPoints([1700000000], [1])
    expect(pts[0][0]).toBe(1700000000000)
  })
})

describe('countPresent', () => {
  it('只数非空值（0 也算有数据）', () => {
    expect(countPresent([0, null, 3, 5])).toBe(3)
    expect(countPresent([])).toBe(0)
    expect(countPresent(undefined)).toBe(0)
  })
})

// ── 状态判定 ───────────────────────────────────────────────

describe('overviewState', () => {
  it('loading 优先于一切（避免首屏闪「无数据」）', () => {
    expect(overviewState(false, true, 0)).toBe('loading')
    expect(overviewState(true, true, 5)).toBe('loading')
  })

  it('error 优先于 empty（失败不能伪装成「没有设备」）', () => {
    // 这条最容易被写反：若先判 empty，请求失败且此前无数据时会显示
    // 「暂无设备」，用户会以为集群空了，而不是「请求失败了」。
    expect(overviewState(true, false, 0)).toBe('error')
  })

  it('0 台设备是空态，不是错误', () => {
    expect(overviewState(false, false, 0)).toBe('empty')
  })

  it('有设备即 ready（个别设备失败由就地提示承载）', () => {
    expect(overviewState(false, false, 3)).toBe('ready')
  })
})

describe('isEmptyResult', () => {
  it('0 台设备为空', () => {
    expect(isEmptyResult(0, 0)).toBe(true)
  })

  it('有设备但一张图都画不出也算空（否则页面只有标题没有内容）', () => {
    expect(isEmptyResult(2, 0)).toBe(true)
  })

  it('有图即非空', () => {
    expect(isEmptyResult(2, 3)).toBe(false)
  })
})

describe('hasDrawableSeries', () => {
  it('所有系列都无数据时返回 false', () => {
    // 直接构造（buildOverviewCharts 会过滤掉无数据图块，故这里手工构造
    // 一张「有系列但全空」的图，验证判定函数本身）。
    const blank = {
      key: 'x',
      title: 't',
      type: 'line' as const,
      span: 'half' as const,
      purpose: 'p',
      rationale: 'r',
      columns: ['cpu_used_percent'],
      series: [
        {
          name: 'a · CPU',
          deviceId: '1',
          metric: 'cpu_used_percent',
          hostname: 'a',
          points: [[1, null]] as [number, number | null][],
          present: 0,
          total: 1
        }
      ],
      snapshot: [] as { deviceId: string; hostname: string; values: Record<string, number> }[],
      unitAxes: [{ unit: '%', columns: ['cpu_used_percent'] }],
      devicesWithData: 0,
      devicesTotal: 1
    }
    expect(hasDrawableSeries(blank)).toBe(false)
    expect(hasDrawableSeries({ ...blank, series: [{ ...blank.series[0], present: 1 }] })).toBe(true)
  })

  it('构建出的图块一定至少有一条非空曲线（过滤规则的不变量）', () => {
    const charts = buildOverviewCharts(
      AXIS,
      ['cpu_used_percent'],
      [dev('1', 'a', { cpu_used_percent: [null, 5, null] })]
    )
    expect(charts).toHaveLength(1)
    expect(hasDrawableSeries(charts[0])).toBe(true)
  })
})

// ── 设备问题文案 ───────────────────────────────────────────

describe('deviceIssueText', () => {
  it('健康设备返回空串', () => {
    expect(deviceIssueText(dev('1', 'a', {}, { watermarkAt: 123 }))).toBe('')
  })

  it('离线设备说明阈值', () => {
    const txt = deviceIssueText(dev('1', 'a', {}, { online: false }), 30)
    expect(txt).toContain('已离线')
    expect(txt).toContain('30')
  })

  it('在线但陈旧必须显式提示（最易被忽略的一种故障）', () => {
    // 心跳正常所以「看似健康」，图上却是平直的线 —— 极易被读成「负载稳定」。
    const txt = deviceIssueText(dev('1', 'a', {}, { online: true, stale: true }))
    expect(txt).toContain('陈旧')
  })

  it('取数失败与离线可叠加，且失败先说', () => {
    const txt = deviceIssueText(dev('1', 'a', {}, { online: false, error: 'boom' }), 30)
    expect(txt).toContain('趋势数据获取失败：boom')
    expect(txt).toContain('已离线')
    expect(txt.indexOf('失败')).toBeLessThan(txt.indexOf('离线'))
  })

  it('在线但尚无快照要提示', () => {
    const txt = deviceIssueText(dev('1', 'a', {}, { online: true, watermarkAt: null }))
    expect(txt).toContain('尚无指标快照')
  })
})

// ── 概览条 ─────────────────────────────────────────────────

describe('buildOverviewStats', () => {
  it('数值全部来自后端 summary（前端不自行统计）', () => {
    const stats = buildOverviewStats({
      total: 10,
      online: 7,
      offline: 3,
      disabled: 2,
      stale: 1,
      offline_threshold_sec: 30
    })
    const m = Object.fromEntries(stats.map((s) => [s.key, s.value]))
    expect(m).toEqual({ total: 10, online: 7, offline: 3, stale: 1, disabled: 2 })
  })

  it('离线与陈旧的告警语义按数量决定', () => {
    const stats = buildOverviewStats({
      total: 1,
      online: 1,
      offline: 0,
      disabled: 0,
      stale: 0,
      offline_threshold_sec: 60
    })
    const m = Object.fromEntries(stats.map((s) => [s.key, s.alert]))
    expect(m.offline).toBe(false)
    expect(m.stale).toBe(false)
    expect(m.total).toBe(false) // 总数永不告警
  })

  it('离线提示引用后端下发的阈值', () => {
    const stats = buildOverviewStats({
      total: 1,
      online: 0,
      offline: 1,
      disabled: 0,
      stale: 0,
      offline_threshold_sec: 45
    })
    expect(stats.find((s) => s.key === 'offline')!.hint).toContain('45')
  })
})

describe('abnormalDeviceCount', () => {
  it('离线 ∪ 陈旧 ∪ 取数失败，同一台只算一次', () => {
    // 相加会把「既离线又取数失败」的机器数两次，给出无法解释的数字。
    const devices = [
      dev('1', 'ok', {}, { online: true }),
      dev('2', 'offline', {}, { online: false }),
      dev('3', 'stale', {}, { online: true, stale: true }),
      // 同时离线 + 失败 + 陈旧 → 仍只算 1 台
      dev('4', 'bad', {}, { online: false, stale: true, error: 'x' })
    ]
    expect(abnormalDeviceCount(devices)).toBe(3)
  })

  it('全部健康时为 0', () => {
    expect(abnormalDeviceCount([dev('1', 'a', {}, { online: true })])).toBe(0)
  })
})

// ── 数值格式化 ─────────────────────────────────────────────

describe('formatMetric', () => {
  it('缺值显示「—」而不是 0', () => {
    expect(formatMetric('cpu_used_percent', null)).toBe('—')
    expect(formatMetric('cpu_used_percent', undefined)).toBe('—')
  })

  it('0 显示为 0（与缺值可区分）', () => {
    expect(formatMetric('cpu_used_percent', 0)).not.toBe('—')
  })

  it('按列的单位格式化', () => {
    expect(formatMetric('cpu_used_percent', 12.345)).toContain('%')
    expect(formatMetric('disk_used_gb', 10.5)).toBe('10.5 GB')
    expect(formatMetric('max_temperature_c', 55.25)).toBe('55.3 °C')
    expect(formatMetric('proc_count', 123)).toBe('123')
  })

  it('字节速率按量级自适应', () => {
    const s = formatMetric('nic_rx_bytes_sec', 1536)
    expect(s).not.toBe('—')
    expect(s).toMatch(/KB|kB|B/i)
  })

  it('未知列回退为纯数字（不抛错、不显示 undefined）', () => {
    const s = formatMetric('unknown_column', 3.14159)
    expect(s).not.toContain('undefined')
    expect(s).not.toBe('—')
  })
})

// ── 图表清单自身的完整性 ───────────────────────────────────

describe('CHART_BLOCKS 清单', () => {
  it('key 唯一（否则 v-for 会复用 DOM、折叠状态会串）', () => {
    const keys = CHART_BLOCKS.map((b) => b.key)
    expect(new Set(keys).size).toBe(keys.length)
  })

  it('声明内不重复列（同一图块里重复会让同列画两条线）', () => {
    for (const b of CHART_BLOCKS) {
      expect(new Set(b.columns).size, `${b.key} 内部列重复`).toBe(b.columns.length)
    }
  })

  it('每个图块都有标题、用途、选型理由', () => {
    for (const b of CHART_BLOCKS) {
      expect(b.title.length, `${b.key} 缺标题`).toBeGreaterThan(0)
      expect(b.purpose.length, `${b.key} 缺用途`).toBeGreaterThan(0)
      expect(b.rationale.length, `${b.key} 缺选型理由`).toBeGreaterThan(0)
    }
  })

  it('盘中各机型分布合理：容量用柱状、速率用面积、比例用折线', () => {
    const byKey = Object.fromEntries(CHART_BLOCKS.map((b) => [b.key, b]))
    // 容量对比是「存量构成」→ 堆叠柱（不是折线）
    expect(byKey['disk-capacity'].type).toBe('stacked')
    // IO / 网络是持续流量 → 面积
    expect(byKey['disk-io'].type).toBe('area')
    expect(byKey['nic'].type).toBe('area')
    // 使用率/负载/连接数随时间 → 折线
    expect(byKey['cpu'].type).toBe('line')
    expect(byKey['load'].type).toBe('line')
    expect(byKey['tcp'].type).toBe('line')
  })
})

/**
 * 源码级守卫：凡直接用底层 `useChart`（而不是 `useChartComponent`）的组件，
 * 必须自行监听 `chartVisible` 事件。
 *
 * ## 为什么需要这条守卫
 *
 * `useChart` 对「首屏之外」的容器走**懒初始化**：它只 `echarts.init()`，
 * 把 option 存进 `pendingOptions`，派发 `chartVisible`，随后把
 * `pendingOptions` 置空 —— **真正应用 option 是消费方的责任**
 * （`useChartComponent` 正是靠注册该事件完成，见 useChart.ts 的 setupLifecycle）。
 *
 * 漏听的失败方式极其隐蔽：不抛异常、不报错、控制台干净，只是留下一张
 * 「有标题、有 echarts 实例、却没有 canvas」的空白卡片。总览页 11 个图块
 * 在 1400px 视口下首屏只容得下 6 个，于是下半屏 5 张图全白且没有任何提示，
 * 用户以为「统计图没有数据」。它也不会自愈 —— 只有显式 setOption（例如点
 * 「查询」触发 updateChart）才能补上。
 *
 * 之所以用源码扫描而不是挂载测试：本仓库的测试环境是 node（无 jsdom /
 * @vue/test-utils），无法挂载组件；而这条不变量本质上是「两个文件之间必须
 * 成对出现的写法」，正是源码级守卫最擅长的形态（与 check-permissions.mjs 同思路）。
 */
describe('useChart 消费方必须监听 chartVisible（防「空白图块」静默缺陷）', () => {
  const readSrc = (rel: string): string => readFileSync(new URL(rel, import.meta.url), 'utf8')

  it('overview-chart-card.vue 注册了 chartVisible 监听', () => {
    const src = readSrc('../components/overview-chart-card.vue')
    expect(src).toContain("@/hooks/core/useChart'")
    expect(src).toContain("addEventListener('chartVisible'")
    // 必须成对清理，否则组件销毁后监听会挂在已卸载的 DOM 上
    expect(src).toContain("removeEventListener('chartVisible'")
  })

  it('metrics-panel.vue 注册了 chartVisible 监听', () => {
    const src = readSrc('../components/metrics-panel.vue')
    expect(src).toContain("@/hooks/core/useChart'")
    expect(src).toContain("addEventListener('chartVisible'")
    expect(src).toContain("removeEventListener('chartVisible'")
  })

  it('overview-chart-card.vue 的 pick-device 是活的：注册 chart.on(click) 且页面有 @pick-device', () => {
    // 修复前是死代码：emit('pick-device') / defineExpose({ onPick }) 都在，
    // 但从未 chart.on('click')，页面也没绑 @pick-device —— 点击曲线什么都不
    // 发生。这条守卫钉住「接线两半必须成对」：
    //   ① 组件侧：on('click' → emit('pick-device'
    //   ② 页面侧：模板里 @pick-device=
    const src = readSrc('../components/overview-chart-card.vue')
    expect(src).toContain("emit('pick-device'")
    expect(src).toContain(".on('click'")
    expect(src).toContain(".off('click'")
    const view = readSrc('../views/overview.vue')
    expect(view).toContain('@pick-device')
  })

  it('overview.vue 的自动刷新带 document.hidden 守卫（与 detail.vue 同款）', () => {
    // 后台标签页里 setInterval 被浏览器限流，照常触发只会攒并发请求；
    // 可见恢复由 visibilitychange 补刷承接。漏掉守卫时切后台仍在轮询。
    const view = readSrc('../views/overview.vue')
    expect(view).toContain('document.hidden')
    expect(view).toContain('visibilitychange')
  })

  it('快照表缺值不渲染空轨道（hasWatermarkValue 守卫在模板里）', () => {
    // 缺值画 6px 空轨道会被读成「使用率 0%」—— 与 watermark-bar / 详情页
    // 的「缺值不画条」纪律矛盾。v-if 必须走 hasWatermarkValue，而不是
    // 恒真（clampPercent(null)→0 仍会渲染轨道）。
    const view = readSrc('../views/overview.vue')
    expect(view).toContain('v-if="c.bar && hasWatermarkValue(row, c.column)"')
  })

  it('overview-chart-card.vue 的 tooltip 有 Top-10 截断（100 台 × 2 列不渲染 200 行）', () => {
    const src = readSrc('../components/overview-chart-card.vue')
    expect(src).toContain('TOOLTIP_MAX_ROWS')
    expect(src).toContain('其余')
  })

  it('色板不残留近白/重复色（useChartOps 亮暗两套、主题响应）', () => {
    // #EDF2FF（近白，暗表面上不可见）与重复的 #FFAF20 是旧色板的病灶；
    // useChartOps 必须在 isDark 下重取（不做 setup 一次性快照）。
    const src = readSrc('../../../hooks/core/useChart.ts')
    expect(src).not.toContain('#EDF2FF')
    const ffaf = src.match(/#FFAF20/g)
    expect(ffaf ?? []).toHaveLength(0)
    expect(src).toContain('isDark')
  })
})

/**
 * 源码级守卫：滚动图例的条目高度必须高于文字行高。
 *
 * ## 为什么需要这条守卫
 *
 * 滚动图例（`type: 'scroll'`）在内容宽于容器时会给内容套一个 clipPath，
 * 而这个裁剪窗口的上沿与**条目包围盒上沿严格齐平**（零余量）—— 探针实测
 * 窗口上沿 243.0、条目框上沿 243.0、文字框上沿 243.0，三者同一个数。
 *
 * 折线系列的图例标记尺寸是 `itemHeight × 0.8`（`LineSeries.getLegendIcon`），
 * 文字行高是 `textStyle.fontSize`（zrender 的文字布局框高 = 字号）。于是当
 * `itemHeight × 0.8 ≤ fontSize` 时条目框就等于文字框，**裁剪线正好压在文字
 * 框上沿**：汉字墨迹高出行框的那 1~2px 被切掉。失败方式很隐蔽 —— 不报错、
 * 不缺字，只是图例汉字顶部一道没有反锯齿的硬边（截图放大才看得出），
 * 而「把 itemHeight 从 8 改成 16」看起来又像是纯粹的整理。
 *
 * 之所以用源码扫描而不是渲染断言：本仓库的测试环境是 node（无 jsdom，
 * 更无字模，测不出墨迹溢出），而这条不变量是「两个数值之间必须保持的关系」，
 * 正是源码级守卫最擅长的形态（与上面的 chartVisible 守卫同思路）。
 */
describe('滚动图例：条目高度必须高于文字行高（防「图例切字头」）', () => {
  /** 截取源码里 `key: {` 起的对象字面量（按花括号配对）。 */
  function objectLiteral(src: string, key: string): string {
    const start = src.indexOf(`${key}: {`)
    if (start < 0) return ''
    let depth = 0
    for (let i = src.indexOf('{', start); i < src.length; i++) {
      if (src[i] === '{') depth++
      else if (src[i] === '}' && --depth === 0) return src.slice(start, i + 1)
    }
    return ''
  }

  const readSrc = (rel: string): string => readFileSync(new URL(rel, import.meta.url), 'utf8')

  it('overview-chart-card.vue 的 legend.itemHeight 留出了文字天头的余量', () => {
    const legend = objectLiteral(readSrc('../components/overview-chart-card.vue'), 'legend')
    expect(legend, '没找到 legend 配置块（组件结构变了？）').not.toBe('')

    const itemHeight = Number(/itemHeight:\s*(\d+)/.exec(legend)?.[1])
    const fontSize = Number(/fontSize:\s*(\d+)/.exec(legend)?.[1])
    expect(Number.isFinite(itemHeight) && Number.isFinite(fontSize)).toBe(true)
    expect(fontSize).toBe(11)

    if (/type:\s*'scroll'/.test(legend)) {
      // 差值的下半部分就是文字框上沿到裁剪线的余量。留 4px 才有 2px 余量；
      // 实测 16 − 11 = 5 → 余量 1.9px，汉字天头完整（原先是 0，天头被切）。
      expect(itemHeight - fontSize).toBeGreaterThanOrEqual(4)
    } else {
      // 改成会换行的图例（无裁剪路径）后这条约束不再成立 —— 那时请连同本守卫
      // 与组件里的注释一起改，而不是悄悄把它调松。
      expect(legend, '图例已不是 scroll：请复核这条守卫的前提').toContain('orient')
    }
  })
})
