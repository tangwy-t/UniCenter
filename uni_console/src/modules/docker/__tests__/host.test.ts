import { describe, expect, it } from 'vitest'
import { hostLabel, staleClass, syncText, syncTextWithError } from '../utils/host'

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

/* 拉取失败必须压过新鲜度结论（D-1）：修复前 state 被清成 null，派生值回落到
 * stale=false/ageSeconds=0，头部把网络失败渲染成「刚刚同步」。这里钉住失败分支
 * 的三句话，以及无失败时对三句结论的原样透传。 */
describe('拉取失败时的同步文案（error 优先于新鲜度）', () => {
  it('失败且没有快照 → 失败结论句，绝不显示「刚刚同步」', () => {
    expect(syncTextWithError(true, false, false, 0, false)).toBe('数据获取失败')
  })

  it('失败但还握有上一次的快照 → 保留同步结论并标注本次刷新失败', () => {
    expect(syncTextWithError(true, true, false, 12, false)).toBe('同步于 12 秒前，本次刷新失败')
    expect(syncTextWithError(true, true, true, 120, false)).toBe('数据陈旧 2 分钟，本次刷新失败')
    expect(syncTextWithError(true, true, false, 130, false)).toBe('同步于 2 分钟前，本次刷新失败')
    expect(syncTextWithError(true, true, false, 0, true)).toBe('尚未收到该主机的数据，本次刷新失败')
  })

  it('无失败 → 三句结论原样（现状语义不变，不因新增参数合并）', () => {
    expect(syncTextWithError(false, false, false, 0, false)).toBe(syncText(false, 0, false))
    expect(syncTextWithError(false, true, false, 12, false)).toBe('同步于 12 秒前')
    expect(syncTextWithError(false, true, false, 0, true)).toBe('尚未收到该主机的数据')
    expect(syncTextWithError(false, true, true, 120, false)).toBe('数据陈旧 2 分钟')
  })
})
