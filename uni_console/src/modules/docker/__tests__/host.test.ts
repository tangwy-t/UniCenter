import { describe, expect, it } from 'vitest'
import { hostLabel, staleClass, syncText } from '../utils/host'

// 同步状态是列表页头部唯一一行状态 —— 它的措辞决定了「这页数据能不能信」。
describe('同步状态文案', () => {
  it('从未上报与陈旧是三句不同的话', () => {
    expect(syncText(false, 0, true)).toBe('尚未收到该主机的数据')
    expect(syncText(true, 120, false)).toBe('数据陈旧 2 分钟')
    expect(syncText(false, 12, false)).toBe('同步于 12 秒前')
  })

  it('陈旧时按分钟向上取整，避免出现「0 分钟」', () => {
    expect(syncText(true, 91, false)).toBe('数据陈旧 2 分钟')
    expect(syncText(true, 600, false)).toBe('数据陈旧 10 分钟')
  })

  it('秒数不足一分钟时按秒显示', () => {
    expect(syncText(false, 0, false)).toBe('刚刚同步')
    expect(syncText(false, 59, false)).toBe('同步于 59 秒前')
  })

  it('陈旧用琥珀色、正常用常规色', () => {
    expect(staleClass(true)).toContain('warn')
    expect(staleClass(false)).toBe('')
  })

  it('主机标签优先显示主机名，其次地址', () => {
    const base = {
      id: '7',
      hostname: 'bogon',
      primaryIp: '192.168.12.105',
      online: true,
      dockerOk: true
    }
    expect(hostLabel(base as never)).toBe('bogon')
    expect(hostLabel({ ...base, hostname: '' } as never)).toBe('192.168.12.105')
    expect(hostLabel({ ...base, hostname: '', primaryIp: '' } as never)).toBe('主机 7')
  })
})
