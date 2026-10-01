/**
 * 五期（监控面）stats 实时流的纯逻辑：NDJSON 样本行解析 + 滑动窗口。
 *
 * 与日志流（utils/stream.ts 的 createLogFeed）同一条流水线的另一条通道：拆包器
 * （createNdjsonParser）复用，行形状换成「打平的样本字段」。为什么单独一份而不
 * 塞进 stream.ts：日志的缓冲是**文本行**（上限按行数、渲染要拼接），stats 的缓冲
 * 是**数值样本**（上限按点数、按字段喂给曲线）—— 两者的消费形态不同，混在一个
 * feed 里会让两个都不是纯函数。
 *
 * 行形状（core 的 docker_stream.go 的 statsNDJSONLine，字段与快照 DockerContainer
 * 同名同义）：
 *   {"seq":n,"t":unix毫秒,"cpu_percent":f,"mem_usage_mb":f,"mem_limit_mb":f,
 *    "net_rx_bytes_sec":f,"net_tx_bytes_sec":f,"eof":b}
 * eof 挂在**最后一个样本行**上（与日志「末帧数据+eof 同帧」同理）；流里只剩收尾
 * 信号时会单独发一行全零 + eof:true 的空样本行 —— 消费端必须把它与「真实的全零
 * 样本」区分开（判据是 t：真实采样时刻是 unix 毫秒，永远为正；空行是 0）。
 */
import { createNdjsonParser } from './stream'

/**
 * 滑动窗口大小：1 秒采样下约等于最近 2 分钟。
 *
 * 曲线**不无限累积**：抽屉是「看一眼」的临时面，挂着不关也不该把内存和重绘成本
 * 随时间线性放大；2 分钟足够判断「现在稳不稳」，更久的历史属于监控系统的职责，
 * 不属于这条流。
 */
export const MAX_STATS_SAMPLES = 120

/** 一个样本：字段名与快照 DockerContainerItem 同名同义（曲线与表格读数同一套约定）。 */
export interface StatsSample {
  /** agent 采样时刻（unix 毫秒），曲线的 x 轴。 */
  t: number
  cpuPercent: number
  memUsageMb: number
  memLimitMb: number
  netRxBytesSec: number
  netTxBytesSec: number
}

/** 解析结果：样本（空收尾行给 null）与 eof 信号分开携带。 */
export interface StatsStreamLine {
  sample: StatsSample | null
  eof: boolean
}

/** 数值字段必须是有限数（t 还要求正整数 —— 见文件头对空收尾行的判据）。 */
function num(v: unknown): boolean {
  return typeof v === 'number' && Number.isFinite(v)
}

/**
 * 解析一行 stats 流帧；形状不符给 null（调用方跳过这一行，不打断整条流 ——
 * 与 parseLogStreamLine 同一纪律：丢一个点只让曲线缺一格，杀掉整条流则是杀了
 * 全部数据）。
 */
export function parseStatsStreamLine(line: string): StatsStreamLine | null {
  let raw: unknown
  try {
    raw = JSON.parse(line)
  } catch {
    return null
  }
  if (typeof raw !== 'object' || raw === null || Array.isArray(raw)) return null
  const o = raw as Record<string, unknown>
  if (o.eof !== undefined && typeof o.eof !== 'boolean') return null
  const eof = o.eof === true
  // 全字段齐才算一行样本；t 不是正数（缺字段 / 空收尾行）→ 只当 eof 信号。
  const hasSample =
    num(o.t) &&
    (o.t as number) > 0 &&
    num(o.cpu_percent) &&
    num(o.mem_usage_mb) &&
    num(o.mem_limit_mb) &&
    num(o.net_rx_bytes_sec) &&
    num(o.net_tx_bytes_sec)
  if (!hasSample) {
    return eof ? { sample: null, eof: true } : null
  }
  return {
    sample: {
      t: Math.floor(o.t as number),
      cpuPercent: o.cpu_percent as number,
      memUsageMb: o.mem_usage_mb as number,
      memLimitMb: o.mem_limit_mb as number,
      netRxBytesSec: o.net_rx_bytes_sec as number,
      netTxBytesSec: o.net_tx_bytes_sec as number
    },
    eof
  }
}

export interface StatsFeed {
  /** 喂入原始 NDJSON chunk（可跨行、跨 chunk 半行，复用日志流同一拆包器）。 */
  pushRaw(chunk: string): void
  /** 窗口内样本（旧 → 新；到达上限后丢最旧，不无限累积）。 */
  readonly samples: readonly StatsSample[]
  /** 是否已收到 eof（流自然收尾，例如容器停止）。 */
  readonly eof: boolean
  /** 至今累计收到的样本数（含被窗口丢掉的 —— 诊断「流活着吗」用）。 */
  readonly totalSamples: number
}

/**
 * stats 流消费端：NDJSON 原始 chunk → 样本窗口的一条流水线
 * （拆包 → 逐行解析 → 滚动窗口）。
 */
export function createStatsFeed(maxSamples = MAX_STATS_SAMPLES): StatsFeed {
  const parser = createNdjsonParser()
  const cap =
    Number.isFinite(maxSamples) && maxSamples > 0 ? Math.floor(maxSamples) : MAX_STATS_SAMPLES
  const window: StatsSample[] = []
  let seen = 0
  let ended = false

  return {
    pushRaw(chunk: string): void {
      if (ended) return // eof 后的残余字节不再入窗（收尾行之后的字节不属于样本）
      for (const line of parser.push(chunk)) {
        const parsed = parseStatsStreamLine(line)
        if (!parsed) continue // 坏行跳过，不打断流
        if (parsed.sample) {
          window.push(parsed.sample)
          seen++
          if (window.length > cap) window.splice(0, window.length - cap)
        }
        if (parsed.eof) ended = true
      }
    },
    get samples(): readonly StatsSample[] {
      return window
    },
    get eof(): boolean {
      return ended
    },
    get totalSamples(): number {
      return seen
    }
  }
}
