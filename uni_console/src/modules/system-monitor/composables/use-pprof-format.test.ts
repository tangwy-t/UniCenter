import { describe, expect, it } from 'vitest'
import {
  fmtClock,
  fmtDur,
  fmtTime,
  fmtValue,
  profileIcon,
  profileMetaOf,
  stamp
} from './use-pprof-format'

// 从 views/pprof.vue(1920+ 行)抽出。抽取前这些格式化只能靠人工看
// 火焰图 tooltip 验证。

describe('fmtValue', () => {
  it('按 1024 进制逐级降档', () => {
    expect(fmtValue(1024)).toBe('1.0K')
    expect(fmtValue(1024 * 1024)).toBe('1.00M')
    expect(fmtValue(1024 * 1024 * 1024)).toBe('1.00G')
  })

  it('小于 1K 原样输出(计数场景)', () => {
    expect(fmtValue(0)).toBe('0')
    expect(fmtValue(999)).toBe('999')
  })

  it('1023 不升档,1024 升档(边界)', () => {
    expect(fmtValue(1023)).toBe('1023')
    expect(fmtValue(1024)).toBe('1.0K')
  })

  it('非有限数返回占位符', () => {
    expect(fmtValue(NaN)).toBe('-')
    expect(fmtValue(Infinity)).toBe('-')
  })
})

describe('fmtClock', () => {
  it('不足 1 小时用 mm:ss', () => {
    expect(fmtClock(65)).toBe('01:05')
    expect(fmtClock(0)).toBe('00:00')
  })

  it('满 1 小时补小时位', () => {
    expect(fmtClock(3661)).toBe('1:01:01')
  })

  it('恰好 3600 秒进入小时格式', () => {
    expect(fmtClock(3600)).toBe('1:00:00')
    expect(fmtClock(3599)).toBe('59:59')
  })

  it('负数按 0 处理', () => {
    expect(fmtClock(-5)).toBe('00:00')
  })
})

describe('fmtDur', () => {
  it('秒 / 分钟 / 小时分级', () => {
    expect(fmtDur(30)).toBe('30 秒')
    expect(fmtDur(90)).toBe('2 分钟')
    expect(fmtDur(7200)).toBe('2.0 小时')
  })

  it('60 秒边界进入分钟', () => {
    expect(fmtDur(59)).toBe('59 秒')
    expect(fmtDur(60)).toBe('1 分钟')
  })

  it('<=0 或非有限数返回破折号(表示无采样)', () => {
    expect(fmtDur(0)).toBe('—')
    expect(fmtDur(-1)).toBe('—')
    expect(fmtDur(NaN)).toBe('—')
  })
})

describe('fmtTime', () => {
  it('输出 24 小时制本地时间', () => {
    const d = new Date(2026, 8, 10, 13, 45, 7)
    expect(fmtTime(d)).toBe('13:45:07')
  })
})

describe('stamp', () => {
  it('生成 YYYYMMDD_HHmmss', () => {
    expect(stamp(new Date(2026, 8, 10, 13, 45, 7))).toBe('20260910_134507')
  })

  it('月/日/时/分/秒补零', () => {
    expect(stamp(new Date(2026, 0, 2, 3, 4, 5))).toBe('20260102_030405')
  })

  it('省略参数时使用当前时间(可解析)', () => {
    expect(stamp()).toMatch(/^\d{8}_\d{6}$/)
  })
})

describe('profileMetaOf / profileIcon', () => {
  it('已知采样类型返回对应图标与颜色', () => {
    expect(profileMetaOf('goroutine')).toEqual({
      icon: 'ri:git-branch-line',
      color: '#10b981'
    })
    expect(profileIcon('heap')).toBe('ri:database-2-line')
  })

  it('未知类型回退到中性值', () => {
    expect(profileMetaOf('nope')).toEqual({ icon: 'ri:radar-line', color: '#64748b' })
  })

  it('profileIcon 等于 profileMetaOf().icon', () => {
    for (const name of ['goroutine', 'heap', 'allocs', 'block', 'mutex', 'threadcreate']) {
      expect(profileIcon(name)).toBe(profileMetaOf(name).icon)
    }
  })
})
