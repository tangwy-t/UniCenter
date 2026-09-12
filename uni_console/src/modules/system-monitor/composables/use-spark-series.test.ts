import { describe, expect, it } from 'vitest'
import {
  sparkWindowCount,
  buildSparkSeries,
  sparkPath,
  SPARK_WINDOW_SECONDS
} from './use-spark-series'

describe('sparkWindowCount', () => {
  it('step 3s 与 12s 时按 10 分钟窗口取点并封顶 60', () => {
    expect(sparkWindowCount(3)).toBe(60) // ceil(600/3)=200 → cap 60
    expect(sparkWindowCount(12)).toBe(50)
    expect(sparkWindowCount(60)).toBe(10)
    expect(sparkWindowCount(240)).toBe(3)
  })

  it('窗口参数与上下限生效', () => {
    expect(sparkWindowCount(300, 600)).toBe(3) // ceil(600/300)=2 → min 3
    expect(sparkWindowCount(3, 3600, 1, 1000)).toBe(1000)
  })

  it('非法 step 回退 min', () => {
    expect(sparkWindowCount(0)).toBe(3)
    expect(sparkWindowCount(-5)).toBe(3)
  })
})

interface Point {
  timestamp: string
  cpu: number | null
  memSys: number | null
}

const ts = (s: number) => `2026-09-09 10:00:${String(s % 60).padStart(2, '0')}`

describe('buildSparkSeries', () => {
  it('取桶尾并向前填充缺值、跳过前导 undefined', () => {
    const buckets: Point[] = [
      { timestamp: ts(1), cpu: 1, memSys: null },
      { timestamp: ts(2), cpu: null, memSys: null },
      { timestamp: ts(3), cpu: 3, memSys: 5 }
    ]
    expect(buildSparkSeries(buckets, 3, 'cpu', SPARK_WINDOW_SECONDS)).toEqual([1, 1, 3])
    expect(buildSparkSeries(buckets, 3, 'memSys', SPARK_WINDOW_SECONDS)).toEqual([5])
  })

  it('桶多于窗口点数时只取尾部', () => {
    const buckets = Array.from({ length: 80 }, (_, i) => ({
      timestamp: ts(i),
      cpu: i,
      memSys: null
    }))
    const got = buildSparkSeries(buckets, 10, 'cpu', SPARK_WINDOW_SECONDS)
    expect(got[0]).toBe(80 - 60)
    expect(got.length).toBe(60)
  })

  it('空桶返回空数组', () => {
    expect(buildSparkSeries([], 10, 'cpu', SPARK_WINDOW_SECONDS)).toEqual([])
  })
})

describe('sparkPath', () => {
  it('空序列返回空 path', () => {
    expect(sparkPath([])).toEqual({ line: '', area: '' })
  })

  it('单点画水平中线且不画面积', () => {
    // 一个点没有斜率,画面积也无基线可围合。
    expect(sparkPath([5])).toEqual({ line: '0,25 100,25', area: '' })
  })

  it('端点横向铺满 0..100', () => {
    const { line } = sparkPath([1, 2, 3])
    const xs = line.split(' ').map((p) => Number(p.split(',')[0]))
    expect(xs).toEqual([0, 50, 100])
  })

  it('pad=3 时极值落在 3 与 47(与抽取前两处实现一致)', () => {
    // 这是合并的依据:min 映射到 y=47(底部留白 3),max 映射到 y=3。
    const { line } = sparkPath([0, 100])
    const ys = line.split(' ').map((p) => Number(p.split(',')[1]))
    expect(ys).toEqual([47, 3])
  })

  it('pad=0 时铺满 0..50', () => {
    const { line } = sparkPath([0, 100], 0)
    const ys = line.split(' ').map((p) => Number(p.split(',')[1]))
    expect(ys).toEqual([50, 0])
  })

  it('全等序列(span=0)不除零,所有点同一高度', () => {
    const { line } = sparkPath([7, 7, 7])
    const ys = line.split(' ').map((p) => Number(p.split(',')[1]))
    expect(new Set(ys).size).toBe(1)
    expect(Number.isFinite(ys[0])).toBe(true)
  })

  it('面积为闭合到 0,50 / 100,50 基线的多边形', () => {
    const { line, area } = sparkPath([1, 2])
    expect(area).toBe(`0,50 ${line} 100,50`)
  })

  it('默认 pad 等价于 sql.vue 原写死的 pad=3 公式', () => {
    // 原 sql.vue 用 `3 + (1-t)*44`,server.vue 用 `pad + (1-t)*(50-pad*2)`。
    // 前者恒等于后者在 pad=3 时的取值,故合并是行为等价的。
    const series = [3, 9, 1, 7, 5]
    const min = Math.min(...series)
    const max = Math.max(...series)
    const span = max - min || 1
    const legacySql = series
      .map(
        (v, i) =>
          `${((i / (series.length - 1)) * 100).toFixed(2)},${(3 + (1 - (v - min) / span) * 44).toFixed(2)}`
      )
      .join(' ')
    expect(sparkPath(series).line).toBe(legacySql)
  })
})
