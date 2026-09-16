import { describe, expect, it } from 'vitest'

// Task 5：纯逻辑已搬到**零副作用**的 `../utils/metrics`，Task 4 为「经组件
// setup 块触达 store/localStorage」而加的 `vi.mock` 挡板已无对象，故移除。

import { RESOURCE_KINDS, formatSeenAt, isStaleResource } from '../utils/metrics'

describe('isStaleResource · stale 标注', () => {
  it('stale=true 判为「已消失」，false 不判', () => {
    expect(isStaleResource({ stale: true })).toBe(true)
    expect(isStaleResource({ stale: false })).toBe(false)
  })
})

describe('formatSeenAt · unix 秒 → 本地时间', () => {
  it('缺值显示「—」', () => {
    expect(formatSeenAt(undefined)).toBe('—')
    expect(formatSeenAt(null)).toBe('—')
  })

  it('有值按本地时区格式化到分钟（不依赖测试机时区）', () => {
    const seconds = 1758000000
    const d = new Date(seconds * 1000)
    const p = (n: number) => String(n).padStart(2, '0')
    const expected = `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(
      d.getHours()
    )}:${p(d.getMinutes())}`
    expect(formatSeenAt(seconds)).toBe(expected)
    // 必须到分钟且含日期，而不是裸时间戳或 ISO 串。
    expect(formatSeenAt(seconds)).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$/)
  })
})

describe('RESOURCE_KINDS · 与后端 device_resource.kind 一致', () => {
  it('恰好是 disk/disk_io/nic/sensor 四种', () => {
    expect(RESOURCE_KINDS.map((k) => k.value)).toEqual(['disk', 'disk_io', 'nic', 'sensor'])
  })
})
