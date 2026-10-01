/**
 * 五期 stats 流纯逻辑的测试：样本行解析（含 eof 的两种形态与坏行）与滑动窗口；
 * P2 起加上历史半边：30 分钟回看的衔接函数（同 t 去重 / 缺口 / 降级 / 滑窗让位）
 * 与 stats-history 的 api 封装（端点与 toast 口径）。
 *
 * 与 stream.test.ts 同一理由：这些是**会静默出错**的最后一跳 —— 空收尾行被当成
 * 全零样本画进曲线（CPU 直接砸到 0）、窗口上限差一个点（内存随挂机时长线性涨）、
 * 坏行没跳过（整条流被一个畸形样本杀死）……页面都照常显示，只有肉眼事后能看出
 * 曲线不对。这里把行形状与窗口边界钉死。历史衔接同理静默：同 t 去重丢了流帧
 * （回看与实时之间多一道台阶）、缺口被假点填掉（「昨晚为什么慢」的形状被伪造），
 * 曲线照样画、照样像那么回事。
 */
import { describe, expect, it, vi } from 'vitest'

// api 封装的测法照抄 registry.test.ts：只替 request 这一个面、桩掉 user store
// 的 import 链（node 环境没有 localStorage），api.ts 本体走真代码。
vi.mock('@/utils/http', () => ({
  default: { get: vi.fn() }
}))
vi.mock('@/store/modules/user', () => ({ useUserStore: vi.fn() }))

import request from '@/utils/http'
import { fetchDockerStatsHistory } from '../api'
import {
  createStatsFeed,
  MAX_STATS_SAMPLES,
  mergeStatsHistory,
  parseStatsStreamLine,
  type StatsHistorySample,
  type StatsSample
} from '../utils/stats'

/** 与 api.ts 同源的口径取前缀（测试命令给 /api/v1；断言不赌具体环境值）。 */
const PREFIX = import.meta.env.VITE_API_PREFIX

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

// ── P2 历史半边：mergeStatsHistory ────────────────────────────────────

/** 一枚历史样本（stats-history 端点的线上形状：apigen camelCase，无网络字段）。 */
function hist(t: number, over: Partial<StatsHistorySample> = {}): StatsHistorySample {
  return { t, cpuPercent: 2, memUsageMb: 100, memLimitMb: 512, ...over }
}

/** 一枚流样本（StatsSample 内部形状；网络字段流帧独有，这里必带）。 */
function flow(t: number, over: Partial<StatsSample> = {}): StatsSample {
  return {
    t,
    cpuPercent: 5,
    memUsageMb: 300,
    memLimitMb: 512,
    netRxBytesSec: 1,
    netTxBytesSec: 2,
    ...over
  }
}

describe('历史衔接（P2 · mergeStatsHistory）', () => {
  it('拼接 + 折名：历史样本折成窗口形状（camelCase 直取），网络字段缺省而非 0', () => {
    const merged = mergeStatsHistory([hist(1000), hist(2000)], [flow(3000)])
    expect(merged.map((s) => s.t)).toEqual([1000, 2000, 3000])
    expect(merged[0]).toEqual({ t: 1000, cpuPercent: 2, memUsageMb: 100, memLimitMb: 512 })
    // 无数据 ≠ 0：历史段缺网络字段，网络曲线据此只画真实流样本。
    expect(merged[0]!.netRxBytesSec).toBeUndefined()
    expect(merged[2]!.netRxBytesSec).toBe(1)
  })

  it('同 t 去重：丢历史留流帧（留新弃旧，且保住网络字段）', () => {
    const merged = mergeStatsHistory(
      [hist(1000), hist(2000, { cpuPercent: 12 })],
      [flow(2000, { cpuPercent: 9 })]
    )
    expect(merged.map((s) => s.t)).toEqual([1000, 2000])
    expect(merged[1]!.cpuPercent).toBe(9)
    expect(merged[1]!.netRxBytesSec).toBe(1)
  })

  it('缺口：两端点如实保留、不造中间假点（连线是图表口径，序列只认真实样本）', () => {
    const merged = mergeStatsHistory([hist(1000)], [flow(61000)])
    expect(merged.map((s) => s.t)).toEqual([1000, 61000])
    expect(merged).toHaveLength(2)
  })

  it('空历史 / 空流：两侧各自退化（空历史 = 纯实时，即现状行为）', () => {
    expect(mergeStatsHistory([], [flow(1000)])).toEqual([flow(1000)])
    expect(mergeStatsHistory([], [])).toEqual([])
    expect(mergeStatsHistory([hist(1000), hist(2000)], []).map((s) => s.t)).toEqual([1000, 2000])
  })

  it('流样本早于部分历史样本时按 t 穿插（归并看时刻，不看来源分组）', () => {
    const merged = mergeStatsHistory([hist(1000), hist(3000)], [flow(2000)])
    expect(merged.map((s) => s.t)).toEqual([1000, 2000, 3000])
  })

  it('滑窗上限 120：历史逐点让位给实时（回看是打开时的窗口，不是常驻监控面）', () => {
    const history = Array.from({ length: 60 }, (_, k) => hist(1000 + k))
    const stream = Array.from({ length: 61 }, (_, k) => flow(100000 + k))
    const merged = mergeStatsHistory(history, stream)
    expect(merged).toHaveLength(MAX_STATS_SAMPLES)
    // 61 枚流帧全在（实时是主角），最旧的一枚历史被挤出窗。
    expect(merged[0]!.t).toBe(1001)
    expect(merged.filter((s) => s.netRxBytesSec === undefined)).toHaveLength(59)
    // 上限可显式收窄（复用同一道守卫）。
    expect(mergeStatsHistory([hist(1000), hist(2000)], [flow(3000)], 2).map((s) => s.t)).toEqual([
      2000, 3000
    ])
  })

  it('乱序回吐与同 t 重复帧不入窗（输出永远 t 严格递增）', () => {
    const merged = mergeStatsHistory(
      [hist(1000), hist(2000)],
      [
        flow(3000),
        flow(2500), // 乱序回吐：比已入窗的 3000 旧
        flow(3000), // 同 t 重复帧
        flow(4000)
      ]
    )
    expect(merged.map((s) => s.t)).toEqual([1000, 2000, 3000, 4000])
  })

  it('坏历史样本丢点不丢段（t 非正 / 字段非有限 / 字段缺失）', () => {
    const merged = mergeStatsHistory(
      [
        hist(1000),
        { t: 0, cpuPercent: 1, memUsageMb: 1, memLimitMb: 1 },
        { t: 2000, cpuPercent: Number.NaN, memUsageMb: 1, memLimitMb: 1 },
        { t: 1500 } as unknown as StatsHistorySample, // 运行时缺字段（JSON 形态漂移）
        hist(3000)
      ],
      []
    )
    expect(merged.map((s) => s.t)).toEqual([1000, 3000])
  })
})

describe('stats 历史封装（api.ts）', () => {
  it('GET /docker/hosts/:id/containers/:cid/stats-history；错误不弹 toast（抽屉开着就地降级）', async () => {
    const http = request as unknown as { get: ReturnType<typeof vi.fn> }
    http.get.mockResolvedValueOnce({ samples: [] })
    await fetchDockerStatsHistory('h2', 'abcdef1234567890')
    expect(http.get).toHaveBeenCalledWith({
      url: `${PREFIX}/docker/hosts/h2/containers/abcdef1234567890/stats-history`,
      showErrorMessage: false
    })
  })
})
