import { describe, expect, it } from 'vitest'
import {
  clamp01,
  fmt1,
  fmt2,
  fmtDurationText,
  fmtGB,
  fmtMB,
  loadTone,
  usageTone
} from './use-server-format'

// 从 views/server.vue(1550+ 行)抽出。抽取前这些档位/单位换算只能靠
// 人工构造负载观察颜色,边界行为无人验证。

describe('fmt1 / fmt2', () => {
  it('按位数格式化', () => {
    expect(fmt1(1.25)).toBe('1.3')
    expect(fmt2(1.234)).toBe('1.23')
  })

  it('非法输入返回占位符', () => {
    expect(fmt1(null)).toBe('-')
    expect(fmt1(undefined)).toBe('-')
    expect(fmt2(NaN)).toBe('-')
    expect(fmt2(Infinity)).toBe('-')
  })

  it('0 是合法值', () => {
    expect(fmt1(0)).toBe('0.0')
    expect(fmt2(0)).toBe('0.00')
  })
})

describe('fmtMB', () => {
  it('1024MB 边界归入 GB', () => {
    expect(fmtMB(1024)).toBe('1.00 GB')
    expect(fmtMB(2048)).toBe('2.00 GB')
  })

  it('小于 1024MB 用 MB(保留 1 位)', () => {
    expect(fmtMB(512)).toBe('512.0 MB')
    expect(fmtMB(1)).toBe('1.0 MB')
  })

  it('小于 1MB 用 KB 并取整', () => {
    expect(fmtMB(0.5)).toBe('512 KB')
    expect(fmtMB(0.25)).toBe('256 KB')
  })

  it('非有限数返回占位符', () => {
    expect(fmtMB(NaN)).toBe('-')
    expect(fmtMB(Infinity)).toBe('-')
  })
})

describe('fmtGB', () => {
  it('1024GB 边界归入 TB', () => {
    expect(fmtGB(1024)).toBe('1.00 TB')
  })

  it('>=100GB 取整,否则保留 1 位', () => {
    // 999.9 >= 100,走整数分支 → 四舍五入到 1000
    expect(fmtGB(999.9)).toBe('1000 GB')
    expect(fmtGB(99.9)).toBe('99.9 GB')
    expect(fmtGB(100)).toBe('100 GB')
    expect(fmtGB(16.55)).toBe('16.6 GB') // toFixed 四舍五入
  })

  it('非有限数返回占位符', () => {
    expect(fmtGB(NaN)).toBe('-')
  })
})

describe('fmtDurationText', () => {
  it('按天/时/分逐级降级', () => {
    expect(fmtDurationText(90061)).toBe('1 天 1 时 1 分') // 1d1h1m1s
    expect(fmtDurationText(3661)).toBe('1 时 1 分')
    expect(fmtDurationText(61)).toBe('1 分 1 秒')
    expect(fmtDurationText(45)).toBe('45 秒')
  })

  it('恰好整天不显示多余单位', () => {
    expect(fmtDurationText(86400)).toBe('1 天 0 时 0 分')
  })

  it('0 秒合法', () => {
    expect(fmtDurationText(0)).toBe('0 秒')
  })

  it('负数与非有限数返回占位符', () => {
    expect(fmtDurationText(-1)).toBe('-')
    expect(fmtDurationText(NaN)).toBe('-')
    expect(fmtDurationText(Infinity)).toBe('-')
  })

  it('小数秒向下取整', () => {
    expect(fmtDurationText(59.9)).toBe('59 秒')
  })
})

describe('usageTone', () => {
  it('按 50/80/90 分档', () => {
    expect(usageTone(10)).toBe('#10b981')
    expect(usageTone(60)).toBe('#3b82f6')
    expect(usageTone(85)).toBe('#f59e0b')
    expect(usageTone(95)).toBe('#dc2626')
  })

  it('边界值归属:50 进蓝、80 进琥珀、90 进红', () => {
    expect(usageTone(50)).toBe('#3b82f6')
    expect(usageTone(80)).toBe('#f59e0b')
    expect(usageTone(90)).toBe('#dc2626')
  })

  it('非法输入返回中性灰', () => {
    expect(usageTone(null)).toBe('#94a3b8')
    expect(usageTone(undefined)).toBe('#94a3b8')
    expect(usageTone(NaN)).toBe('#94a3b8')
  })

  it('0 属于最低档(绿)', () => {
    expect(usageTone(0)).toBe('#10b981')
  })
})

describe('loadTone', () => {
  it('按 load/cores 的 0.7/1.3/2.5 分档', () => {
    expect(loadTone(2, 8)).toBe('#10b981') // 0.25
    expect(loadTone(8, 8)).toBe('#3b82f6') // 1.0
    expect(loadTone(16, 8)).toBe('#f59e0b') // 2.0
    expect(loadTone(24, 8)).toBe('#dc2626') // 3.0
  })

  it('核数非法时无法判断,返回中性灰', () => {
    expect(loadTone(1, 0)).toBe('#94a3b8')
    expect(loadTone(1, -4)).toBe('#94a3b8')
  })

  it('负载非法返回中性灰', () => {
    expect(loadTone(null, 8)).toBe('#94a3b8')
    expect(loadTone(NaN, 8)).toBe('#94a3b8')
  })

  it('边界值归属:0.7 进蓝、1.3 进琥珀、2.5 进红', () => {
    expect(loadTone(0.7 * 4, 4)).toBe('#3b82f6')
    expect(loadTone(1.3 * 4, 4)).toBe('#f59e0b')
    expect(loadTone(2.5 * 4, 4)).toBe('#dc2626')
  })
})

describe('clamp01', () => {
  it('夹到 [0,100]', () => {
    expect(clamp01(150)).toBe(100)
    expect(clamp01(-1)).toBe(0)
    expect(clamp01(33.3)).toBe(33.3)
  })

  it('null/undefined/非有限数归 0', () => {
    expect(clamp01(null)).toBe(0)
    expect(clamp01(undefined)).toBe(0)
    expect(clamp01(NaN)).toBe(0)
    expect(clamp01(Infinity)).toBe(0)
  })
})
