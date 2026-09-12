import { describe, expect, it } from 'vitest'
import {
  OP_COLORS,
  OPS,
  clamp01,
  durTone,
  fmtCount,
  fmtPct,
  heatTone,
  isSysTable,
  opBadgeStyle,
  timeOf
} from './use-sql-format'

// 这些函数从 views/sql.vue(1770+ 行)抽出。抽取前只能靠人工点页面
// 验证配色与格式化;抽出后在此锁定边界行为。

describe('fmtCount', () => {
  it('有限数走千分位', () => {
    expect(fmtCount(1234567)).toBe('1,234,567')
  })

  it('0 与负数正常格式化', () => {
    expect(fmtCount(0)).toBe('0')
    expect(fmtCount(-5)).toBe('-5')
  })

  it('null/undefined/NaN/Infinity 返回占位符', () => {
    expect(fmtCount(null)).toBe('-')
    expect(fmtCount(undefined)).toBe('-')
    expect(fmtCount(NaN)).toBe('-')
    expect(fmtCount(Infinity)).toBe('-')
  })
})

describe('fmtPct', () => {
  it('正常百分比保留 2 位', () => {
    expect(fmtPct(1, 4)).toBe('25.00%')
    expect(fmtPct(1, 3)).toBe('33.33%')
  })

  it('分母为 0 或负数返回占位符(避免 Infinity%/NaN%)', () => {
    expect(fmtPct(1, 0)).toBe('-')
    expect(fmtPct(1, -3)).toBe('-')
  })

  it('分子为 0 时是合法的 0.00%', () => {
    expect(fmtPct(0, 10)).toBe('0.00%')
  })

  it('非有限输入返回占位符', () => {
    expect(fmtPct(NaN, 10)).toBe('-')
    expect(fmtPct(1, Infinity)).toBe('-')
  })
})

describe('clamp01', () => {
  it('夹到 [0,100]', () => {
    expect(clamp01(150)).toBe(100)
    expect(clamp01(-20)).toBe(0)
    expect(clamp01(42.5)).toBe(42.5)
  })

  it('非有限数归 0', () => {
    expect(clamp01(NaN)).toBe(0)
    expect(clamp01(Infinity)).toBe(0)
  })
})

describe('timeOf', () => {
  it('取 HH:mm:ss', () => {
    expect(timeOf('2026-09-10 13:45:07')).toBe('13:45:07')
  })

  it('短于 19 位原样返回', () => {
    expect(timeOf('13:45')).toBe('13:45')
  })
})

describe('heatTone', () => {
  it('按 p95/阈值 的档位取色', () => {
    expect(heatTone(10, 100)).toBe('#10b981') // 10% → 绿
    expect(heatTone(60, 100)).toBe('#3b82f6') // 60% → 蓝
    expect(heatTone(90, 100)).toBe('#f59e0b') // 90% → 琥珀
    expect(heatTone(120, 100)).toBe('#dc2626') // 超限 → 红
  })

  it('边界值归属:50 进蓝、80 进琥珀、100 进红', () => {
    expect(heatTone(50, 100)).toBe('#3b82f6')
    expect(heatTone(80, 100)).toBe('#f59e0b')
    expect(heatTone(100, 100)).toBe('#dc2626')
  })

  it('非法阈值或 p95<=0 返回中性灰', () => {
    expect(heatTone(10, 0)).toBe('#94a3b8')
    expect(heatTone(10, -1)).toBe('#94a3b8')
    expect(heatTone(0, 100)).toBe('#94a3b8')
    expect(heatTone(NaN, 100)).toBe('#94a3b8')
  })
})

describe('durTone', () => {
  it('按 阈值/3 与 阈值 分档', () => {
    expect(durTone(10, 300)).toBe('#10b981') // < 100 → 绿
    expect(durTone(200, 300)).toBe('#f59e0b') // [100,300) → 琥珀
    expect(durTone(500, 300)).toBe('#dc2626') // >= 300 → 红
  })

  it('非法阈值返回中性灰', () => {
    expect(durTone(10, 0)).toBe('#94a3b8')
    expect(durTone(NaN, 300)).toBe('#94a3b8')
  })
})

describe('opBadgeStyle', () => {
  it('已知操作取对应主色并派生浅底/描边', () => {
    expect(opBadgeStyle('SELECT')).toEqual({
      color: '#3b82f6',
      background: '#3b82f614',
      borderColor: '#3b82f633'
    })
  })

  it('未知操作回退到 OTHER 色', () => {
    expect(opBadgeStyle('TRUNCATE').color).toBe(OP_COLORS.OTHER)
  })

  it('OPS 中每个操作都有配色(与环图共用同一来源)', () => {
    for (const op of OPS) {
      expect(OP_COLORS[op]).toBeTruthy()
    }
  })
})

describe('isSysTable', () => {
  it('识别 MySQL 系统库(需带库名前缀)', () => {
    expect(isSysTable('information_schema.tables')).toBe(true)
    expect(isSysTable('mysql.user')).toBe(true)
    expect(isSysTable('performance_schema.events')).toBe(true)
    expect(isSysTable('sys.schema')).toBe(true)
  })

  it('大小写不敏感', () => {
    expect(isSysTable('MYSQL.USER')).toBe(true)
  })

  it('业务表与裸表名不误判', () => {
    expect(isSysTable('myapp.users')).toBe(false)
    expect(isSysTable('mysql_users')).toBe(false)
    expect(isSysTable('user')).toBe(false)
    expect(isSysTable('')).toBe(false)
  })

  it('仅库名前缀精确匹配,不做子串误伤', () => {
    // "mysqlx." 不应命中 mysql. 规则
    expect(isSysTable('mysqlx.foo')).toBe(false)
  })
})
