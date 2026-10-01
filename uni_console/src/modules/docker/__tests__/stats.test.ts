/**
 * 五期 stats 流纯逻辑的测试：样本行解析（含 eof 的两种形态与坏行）与滑动窗口。
 *
 * 与 stream.test.ts 同一理由：这些是**会静默出错**的最后一跳 —— 空收尾行被当成
 * 全零样本画进曲线（CPU 直接砸到 0）、窗口上限差一个点（内存随挂机时长线性涨）、
 * 坏行没跳过（整条流被一个畸形样本杀死）……页面都照常显示，只有肉眼事后能看出
 * 曲线不对。这里把行形状与窗口边界钉死。
 */
import { describe, expect, it } from 'vitest'
import {
  createStatsFeed,
  MAX_STATS_SAMPLES,
  parseStatsStreamLine,
  type StatsSample
} from '../utils/stats'

/** 一个正常样本行（字段名与值都是 core statsNDJSONLine 的线上形状）。 */
function sampleLine(t: number, over: Partial<StatsSample> = {}): string {
  const s: StatsSample = {
    t,
    cpuPercent: 3.5,
    memUsageMb: 200,
    memLimitMb: 1024,
    netRxBytesSec: 10240,
    netTxBytesSec: 2048,
    ...over
  }
  return JSON.stringify({
    seq: 7,
    t: s.t,
    cpu_percent: s.cpuPercent,
    mem_usage_mb: s.memUsageMb,
    mem_limit_mb: s.memLimitMb,
    net_rx_bytes_sec: s.netRxBytesSec,
    net_tx_bytes_sec: s.netTxBytesSec,
    eof: false
  })
}

describe('样本行解析', () => {
  it('完整样本行 → 样本（字段打平直取，不折名）', () => {
    const parsed = parseStatsStreamLine(sampleLine(1790000000123))
    expect(parsed).not.toBeNull()
    expect(parsed!.sample).toEqual({
      t: 1790000000123,
      cpuPercent: 3.5,
      memUsageMb: 200,
      memLimitMb: 1024,
      netRxBytesSec: 10240,
      netTxBytesSec: 2048
    })
    expect(parsed!.eof).toBe(false)
  })

  it('eof 挂在最后一个样本行上：样本入窗且带 eof 信号', () => {
    const line = sampleLine(1790000000123)
    const withEof = line.replace('"eof":false', '"eof":true')
    const parsed = parseStatsStreamLine(withEof)
    expect(parsed).not.toBeNull()
    expect(parsed!.sample).not.toBeNull()
    expect(parsed!.sample!.t).toBe(1790000000123)
    expect(parsed!.eof).toBe(true)
  })

  it('不带样本的空收尾行（全零 + t=0 + eof）→ 只有 eof，不产出全零假样本', () => {
    const parsed = parseStatsStreamLine(
      JSON.stringify({
        seq: 9,
        t: 0,
        cpu_percent: 0,
        mem_usage_mb: 0,
        mem_limit_mb: 0,
        net_rx_bytes_sec: 0,
        net_tx_bytes_sec: 0,
        eof: true
      })
    )
    expect(parsed).toEqual({ sample: null, eof: true })
  })

  it('坏行给 null：非 JSON / 非对象 / 字段缺一 / 字段类型错 / t 非正数', () => {
    expect(parseStatsStreamLine('not json')).toBeNull()
    expect(parseStatsStreamLine('[]')).toBeNull()
    expect(parseStatsStreamLine('null')).toBeNull()
    // 缺一个数值字段：整行作废，而不是给个半样本（半样本会让曲线缺一条腿）。
    expect(
      parseStatsStreamLine(sampleLine(1790000000123).replace('"mem_limit_mb":1024,', ''))
    ).toBeNull()
    expect(
      parseStatsStreamLine(
        sampleLine(1790000000123).replace('"cpu_percent":3.5', '"cpu_percent":"3.5"')
      )
    ).toBeNull()
    expect(parseStatsStreamLine(sampleLine(1790000000123).replace('1790000000123', '0'))).toBeNull()
    // eof 字段类型错同样拒收（布尔被写成数字会让收尾判定失真）。
    expect(
      parseStatsStreamLine(sampleLine(1790000000123).replace('"eof":false', '"eof":1'))
    ).toBeNull()
  })
})

describe('stats 消费端（滑动窗口）', () => {
  it('逐行入窗，旧 → 新有序；跨 chunk 的半行被缓存拼上', () => {
    const feed = createStatsFeed()
    feed.pushRaw(`${sampleLine(1000)}\n${sampleLine(2000)}\n`)
    expect(feed.samples.map((s) => s.t)).toEqual([1000, 2000])
    feed.pushRaw(sampleLine(3000).slice(0, 20))
    expect(feed.samples).toHaveLength(2)
    feed.pushRaw(`${sampleLine(3000).slice(20)}\n`)
    expect(feed.samples.map((s) => s.t)).toEqual([1000, 2000, 3000])
  })

  it('窗口上限 120：超限丢最旧，totalSamples 照常累计（不无限累积内存与重绘）', () => {
    const feed = createStatsFeed()
    for (let i = 0; i < MAX_STATS_SAMPLES + 10; i++) {
      feed.pushRaw(`${sampleLine(1000 + i)}\n`)
    }
    expect(feed.samples).toHaveLength(MAX_STATS_SAMPLES)
    expect(feed.samples[0]!.t).toBe(1010) // 最旧的 10 个（t=1000..1009）被丢掉
    expect(feed.samples[feed.samples.length - 1]!.t).toBe(1000 + MAX_STATS_SAMPLES + 9)
    expect(feed.totalSamples).toBe(MAX_STATS_SAMPLES + 10)
  })

  it('坏行跳过不打断流；eof 行入窗收尾', () => {
    const feed = createStatsFeed()
    feed.pushRaw('garbage\n')
    feed.pushRaw(`${sampleLine(1000)}\n`)
    feed.pushRaw('{broken json}\n')
    const last = sampleLine(2000).replace('"eof":false', '"eof":true')
    feed.pushRaw(`${last}\n`)
    expect(feed.samples.map((s) => s.t)).toEqual([1000, 2000])
    expect(feed.eof).toBe(true)
    // eof 后残余字节不再入窗。
    feed.pushRaw(`${sampleLine(3000)}\n`)
    expect(feed.samples.map((s) => s.t)).toEqual([1000, 2000])
  })

  it('空收尾行只置 eof，不给窗口添全零样本', () => {
    const feed = createStatsFeed()
    feed.pushRaw(`${sampleLine(1000)}\n`)
    feed.pushRaw(
      '{"seq":9,"t":0,"cpu_percent":0,"mem_usage_mb":0,"mem_limit_mb":0,"net_rx_bytes_sec":0,"net_tx_bytes_sec":0,"eof":true}\n'
    )
    expect(feed.samples.map((s) => s.t)).toEqual([1000])
    expect(feed.eof).toBe(true)
  })
})
