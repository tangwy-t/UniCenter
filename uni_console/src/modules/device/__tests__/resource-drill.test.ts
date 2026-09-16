import { describe, expect, it, vi } from 'vitest'

vi.mock('@/utils/http', () => ({ default: { get: vi.fn(), post: vi.fn(), del: vi.fn() } }))
vi.mock('@/store/modules/setting', () => ({
  useSettingStore: () => ({ isDark: false, menuOpen: true, menuType: 'left' })
}))
vi.mock('@/store/modules/user', () => ({
  useUserStore: () => ({ info: { permissions: [] }, accessToken: '', refreshToken: '' })
}))

import { RESOURCE_KINDS, formatSeenAt, isStaleResource } from '../components/resource-drill.vue'

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
