import { describe, expect, it } from 'vitest'
import {
  EMPTY_TEXT,
  clampPercent,
  deviceIcon,
  formatAxisTickByUnit,
  formatByUnit,
  formatBytesPerSec,
  formatCapacityMb,
  formatMb,
  formatPercent,
  formatRelative,
  formatUnixSeconds,
  formatUptime,
  isCriticalUsage,
  isWatermarkStale,
  usageTone,
  watermarkState
} from '../utils/display'

// 固定「现在」= 2026-09-17T12:00:00Z，让所有相对时间断言可复现。
const NOW = Date.UTC(2026, 8, 17, 12, 0, 0)
const nowSec = Math.floor(NOW / 1000)

describe('formatRelative', () => {
  it('缺值返回占位符而不是 0 或空串', () => {
    expect(formatRelative(undefined, NOW)).toBe(EMPTY_TEXT)
    expect(formatRelative(null, NOW)).toBe(EMPTY_TEXT)
    expect(formatRelative(Number.NaN, NOW)).toBe(EMPTY_TEXT)
  })

  it('按量级选择单位', () => {
    expect(formatRelative(nowSec - 3, NOW)).toBe('刚刚')
    expect(formatRelative(nowSec - 30, NOW)).toBe('30 秒前')
    expect(formatRelative(nowSec - 120, NOW)).toBe('2 分钟前')
    expect(formatRelative(nowSec - 7200, NOW)).toBe('2 小时前')
    expect(formatRelative(nowSec - 86400 * 3, NOW)).toBe('3 天前')
    expect(formatRelative(nowSec - 86400 * 60, NOW)).toBe('2 个月前')
    expect(formatRelative(nowSec - 86400 * 400, NOW)).toBe('1 年前')
  })

  // 服务端与浏览器时钟不可能完全一致：未来时刻显示成「-3 分钟前」
  // 只会让人怀疑数据本身。
  it('未来时刻归为「刚刚」而不是负数', () => {
    expect(formatRelative(nowSec + 120, NOW)).toBe('刚刚')
  })
})

describe('formatUnixSeconds', () => {
  it('缺值返回占位符', () => {
    expect(formatUnixSeconds(undefined)).toBe(EMPTY_TEXT)
    expect(formatUnixSeconds(null)).toBe(EMPTY_TEXT)
  })

  it('格式化并补零', () => {
    // 2026-09-17T12:00:00Z 在 UTC+8 为 20:00:00
    expect(formatUnixSeconds(nowSec)).toMatch(/^2026-09-1[78] \d{2}:\d{2}:\d{2}$/)
  })
})

describe('formatMb / formatBytesPerSec', () => {
  it('MB 超过 1024 转 GB', () => {
    expect(formatMb(512)).toBe('512.0 MB')
    expect(formatMb(16384)).toBe('16.00 GB')
  })

  it('MB 的 0 / 负数视为缺值', () => {
    // 0 MB 内存总量在物理上不可能，显示「0.0 MB」会误导
    expect(formatMb(0)).toBe(EMPTY_TEXT)
    expect(formatMb(-1)).toBe(EMPTY_TEXT)
  })

  it('速率换算单位', () => {
    expect(formatBytesPerSec(512)).toBe('512 B/s')
    expect(formatBytesPerSec(2048)).toBe('2.0 KB/s')
    expect(formatBytesPerSec(5 * 1024 * 1024)).toBe('5.0 MB/s')
  })

  it('速率缺值返回占位符', () => {
    expect(formatBytesPerSec(undefined)).toBe(EMPTY_TEXT)
  })
})

describe('formatUptime', () => {
  it('缺值 / 无效值返回占位符', () => {
    expect(formatUptime(undefined, NOW)).toBe(EMPTY_TEXT)
    expect(formatUptime(0, NOW)).toBe(EMPTY_TEXT)
  })

  it('按时长选择精度', () => {
    expect(formatUptime(nowSec - 300, NOW)).toBe('5 分')
    expect(formatUptime(nowSec - 3600 * 3 - 600, NOW)).toBe('3 时 10 分')
    expect(formatUptime(nowSec - 86400 * 2 - 3600 * 5, NOW)).toBe('2 天 5 时')
  })

  // 时钟偏差会让 bootTime 落在未来，此时不该显示负数时长。
  it('开机时间在未来时返回占位符', () => {
    expect(formatUptime(nowSec + 600, NOW)).toBe(EMPTY_TEXT)
  })
})

describe('watermarkState', () => {
  it('三个百分比全缺 → empty（而不是 stale）', () => {
    // 新接入、尚未上报的设备就是这种状态，报成「数据过期」是误报
    expect(watermarkState([null, undefined, null], nowSec, 30, NOW)).toBe('empty')
  })

  it('有值且新鲜 → ok', () => {
    expect(watermarkState([10, 20, 30], nowSec - 5, 30, NOW)).toBe('ok')
  })

  it('有值但采样过旧 → stale', () => {
    expect(watermarkState([10, 20, 30], nowSec - 600, 30, NOW)).toBe('stale')
  })

  it('只要有一个百分比有值就不算 empty', () => {
    expect(watermarkState([null, null, 42], nowSec - 5, 30, NOW)).toBe('ok')
  })

  it('阈值未下发给 0 时不做过期判定（避免误报）', () => {
    // 阈值缺失时宁可判为新鲜：误报「数据过期」会让人去查一个不存在的故障
    expect(watermarkState([10, 20, 30], nowSec - 99999, 0, NOW)).toBe('ok')
  })
})

describe('isWatermarkStale', () => {
  it('缺采样时刻返回 false（那是「无数据」而非「过期」）', () => {
    expect(isWatermarkStale(null, 30, NOW)).toBe(false)
    expect(isWatermarkStale(undefined, 30, NOW)).toBe(false)
  })

  it('阈值非法时不判定为过期', () => {
    expect(isWatermarkStale(nowSec - 99999, 0, NOW)).toBe(false)
    expect(isWatermarkStale(nowSec - 99999, -1, NOW)).toBe(false)
  })

  it('严格超过阈值才算过期', () => {
    expect(isWatermarkStale(nowSec - 30, 30, NOW)).toBe(false)
    expect(isWatermarkStale(nowSec - 31, 30, NOW)).toBe(true)
  })
})

describe('formatPercent / clampPercent', () => {
  it('百分比缺值显示占位符而不是 0%', () => {
    // 这是全页最重要的约定：0% 与「没采到」在容量判断上完全相反
    expect(formatPercent(null)).toBe(EMPTY_TEXT)
    expect(formatPercent(undefined)).toBe(EMPTY_TEXT)
    expect(formatPercent(0)).toBe('0.0%')
  })

  it('clampPercent 只用于进度条长度，缺值退化成 0', () => {
    expect(clampPercent(null)).toBe(0)
    expect(clampPercent(-5)).toBe(0)
    expect(clampPercent(150)).toBe(100)
    expect(clampPercent(42.5)).toBe(42.5)
  })
})

describe('usageTone / isCriticalUsage', () => {
  it('按阈值分档', () => {
    expect(usageTone(10)).toContain('success')
    expect(usageTone(60)).toContain('primary')
    expect(usageTone(85)).toContain('warning')
    expect(usageTone(95)).toContain('danger')
  })

  it('缺值用 placeholder 色（不能借用 success 绿）', () => {
    expect(usageTone(null)).toContain('placeholder')
  })

  it('≥90 才算需要显眼提示', () => {
    expect(isCriticalUsage(89.9)).toBe(false)
    expect(isCriticalUsage(90)).toBe(true)
    expect(isCriticalUsage(null)).toBe(false)
  })
})

describe('deviceIcon', () => {
  it('按 os / platform 推断', () => {
    expect(deviceIcon('linux', 'ubuntu')).toContain('ubuntu')
    expect(deviceIcon('linux', 'centos')).toContain('centos')
    expect(deviceIcon('darwin', null)).toContain('mac')
    expect(deviceIcon('windows', null)).toContain('windows')
  })

  it('未识别回退通用图标而不是空串', () => {
    // 返回空串会让页头出现一个空位
    expect(deviceIcon(null, null)).toBe('ri:computer-line')
    expect(deviceIcon('freebsd', '')).toBe('ri:computer-line')
  })
})

// ─────────────────────────────────────────────────────────────
// 列表页水位单元格（watermark-bar.vue）依赖的三件事
// ─────────────────────────────────────────────────────────────
//
// 组件本身不重复实现取数/配色，全部转调 display.ts —— 所以这里钉住的是
// 「组件与详情页/监控页共用同一套阈值」这条约束，而不是组件的 DOM。

describe('水位单元格：缺值 / 配色 / 条长', () => {
  it('缺值一律「—」，且不得被当成 0 参与配色', () => {
    // 回归用例：列表页此前的三列直接用 toFixed(1)，缺值会走到 undefined 报错或
    // 显示成「NaN%」。现在统一走 formatPercent。
    expect(formatPercent(undefined)).toBe(EMPTY_TEXT)
    expect(formatPercent(null)).toBe(EMPTY_TEXT)
    expect(formatPercent(NaN)).toBe(EMPTY_TEXT)
    // 缺值的配色必须是中性占位色，不能是绿色的「0% 很健康」
    expect(usageTone(undefined)).toBe('var(--el-text-color-placeholder)')
    expect(usageTone(null)).not.toBe(usageTone(0))
  })

  it('配色与详情页同阈值：<50 绿 / <80 蓝 / <90 琥珀 / ≥90 红', () => {
    expect(usageTone(0)).toBe('var(--el-color-success)')
    expect(usageTone(49.9)).toBe('var(--el-color-success)')
    expect(usageTone(50)).toBe('var(--el-color-primary)')
    expect(usageTone(79.9)).toBe('var(--el-color-primary)')
    expect(usageTone(80)).toBe('var(--el-color-warning)')
    expect(usageTone(89.9)).toBe('var(--el-color-warning)')
    expect(usageTone(90)).toBe('var(--el-color-danger)')
    expect(usageTone(150)).toBe('var(--el-color-danger)')
  })

  it('条长被夹到 0~100：>100 不溢出，<0 不反向', () => {
    expect(clampPercent(0)).toBe(0)
    expect(clampPercent(100)).toBe(100)
    expect(clampPercent(137)).toBe(100)
    expect(clampPercent(-8)).toBe(0)
  })

  it('数值保留一位小数（含整数水位，如后端给的 50）', () => {
    expect(formatPercent(50)).toBe('50.0%')
    expect(formatPercent(35.83)).toBe('35.8%')
    expect(formatPercent(100)).toBe('100.0%')
  })
})

/**
 * 单位换算的**单一事实源**（`formatByUnit`）。
 *
 * 这些用例钉住的核心是「只在数字大到难读时才进位」：
 * 此前三套实现并存，同一条指标在不同界面上精度与单位都不一样
 * （详情 tooltip `761286.09`、总览页 `743.4 KB/s`、内存 `7523.4 MB` vs `7.34 GB`）。
 */
describe('formatByUnit · 量纲 → 文案', () => {
  it('字节速率按量级进位（B/s → KB/s → MB/s → GB/s）', () => {
    expect(formatByUnit('B/s', 512)).toBe('512 B/s')
    expect(formatByUnit('B/s', 761286.09)).toBe('743.4 KB/s')
    expect(formatByUnit('B/s', 5 * 1024 * 1024)).toBe('5.0 MB/s')
    expect(formatByUnit('B/s', 3 * 1024 ** 3)).toBe('3.00 GB/s')
  })

  it('容量：MB ≥1024 转 GB、GB ≥1024 转 TB，且不向下换算', () => {
    expect(formatByUnit('MB', 7523.4)).toBe('7.35 GB')
    expect(formatByUnit('MB', 512)).toBe('512 MB')
    expect(formatByUnit('GB', 1795.05)).toBe('1.75 TB')
    expect(formatByUnit('GB', 10.5)).toBe('10.5 GB')
    // 不向下换算：0.4 GB 不显示成 409.6 MB（容量读数要保住量级感）
    expect(formatByUnit('GB', 0.4)).toBe('0.4 GB')
  })

  it('时长（秒）走天/时/分文案，而不是裸秒数', () => {
    expect(formatByUnit('s', 7975803)).toBe('92 天 7 时')
    expect(formatByUnit('s', 3700)).toBe('1 时 1 分')
    expect(formatByUnit('s', 300)).toBe('5 分')
    expect(formatByUnit('s', 45)).toBe('45 秒')
  })

  it('计数类速率大数进位（次/s、包/s）', () => {
    expect(formatByUnit('包/s', 1234)).toBe('1234 包/s')
    expect(formatByUnit('包/s', 1234567)).toBe('1.23M 包/s')
    expect(formatByUnit('次/s', 23456)).toBe('23.5K 次/s')
  })

  it('百分比 / 温度 / 无量纲', () => {
    expect(formatByUnit('%', 30.8)).toBe('30.8%')
    expect(formatByUnit('°C', 55.25)).toBe('55.3 °C')
    expect(formatByUnit('', 0.42)).toBe('0.42')
    expect(formatByUnit('', 128)).toBe('128')
  })

  it('缺值是「—」，而 0 是合法值（两者必须可区分）', () => {
    expect(formatByUnit('MB', null)).toBe(EMPTY_TEXT)
    expect(formatByUnit('B/s', undefined)).toBe(EMPTY_TEXT)
    expect(formatByUnit('GB', 0)).toBe('0 GB')
    expect(formatByUnit('MB', 0)).toBe('0 MB')
    expect(formatByUnit('B/s', 0)).toBe('0 B/s')
  })

  it('实测容量允许 0，设备规格用的 formatMb 仍把 0 当缺值（两者语义不同）', () => {
    // formatMb 服务「内存总量」（0 = 未知）；formatCapacityMb 服务实测读数（0 合法）
    expect(formatMb(0)).toBe(EMPTY_TEXT)
    expect(formatCapacityMb(0)).toBe('0 MB')
    expect(formatCapacityMb(16384)).toBe('16 GB')
  })

  it('轴刻度去掉无意义的尾随零（密排的小数会重复出现）', () => {
    expect(formatAxisTickByUnit('%', 20)).toBe('20%')
    expect(formatAxisTickByUnit('%', 47.56)).toBe('47.6%')
    expect(formatAxisTickByUnit('B/s', 761286.09)).toBe('743.4 KB/s')
  })
})
