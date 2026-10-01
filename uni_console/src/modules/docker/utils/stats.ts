/**
 * 五期（监控面）stats 实时流的纯逻辑：NDJSON 样本行解析 + 滑动窗口。
 *
 * 与日志流（utils/stream.ts 的 createLogFeed）同一条流水线的另一条通道：拆包器
 * （createNdjsonParser）复用，行形状换成「打平的样本字段」。为什么单独一份而不
 * 塞进 stream.ts：日志的缓冲是**文本行**（上限按行数、渲染要拼接），stats 的缓冲
 * 是**数值样本**（上限按点数、按字段喂给曲线）—— 两者的消费形态不同，混在一个
 * feed 里会让两个都不是纯函数。
 *
 * P2 起这份纯逻辑多了另一半：stats-history 的 30 分钟回看与「历史段 ↔ 实时段」
 * 的衔接（文件后半的 mergeStatsHistory）—— 抽屉打开先见历史再接实时，两段在
 * 同一个滑窗里拼成一条 t 单调的序列。
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
  /**
   * 网络速率：**只有实时流帧携带**（历史端点的契约只回 t/cpu/mem）。缺省 = 无
   * 数据而不是 0 —— 网络曲线据此只画真实流样本，不拿假 0 充历史。
   */
  netRxBytesSec?: number
  netTxBytesSec?: number
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

// ── P2 历史半边：30 分钟回看与两段衔接 ───────────────────────────────
//
// 上面是「实时流」半边；这半边是它的前置数据源：GET stats-history（过去约
// 30 分钟，30s 一采样共 ≤60 点）在开流**之前**拉回来预填曲线 —— 抽屉一打开
// 曲线就有形状，「昨晚为什么慢」有了 30 分钟的回看窗口，而不是从一条空白
// 曲线干等第一帧。

/**
 * 历史端点的样本形状（引用 apigen 生成类型 —— 生成物是唯一事实源）。
 *
 * 与实时流帧**同义不同名**：历史走生成 DTO 的 camelCase（t/cpuPercent/
 * memUsageMb/memLimitMb），流帧是 agent 的 snake_case 裸行（t/cpu_percent/…，
 * 已在 parseStatsStreamLine 折成同一套内部名）。两套命名的对齐只收在下面的
 * 衔接函数里 —— 折名点唯一，生成物改名时只会在这里红灯。
 */
export type StatsHistorySample = Api.Docker.DockerStatsHistorySample

/**
 * 历史样本 → 窗口样本（折名 + 校验）。字段不齐或非有限数给 null —— 与流解析
 * 同一纪律：丢一个点只让曲线缺一格；为一份坏历史抛异常，等于把整个回看窗口
 * 一起丢掉。
 */
function historyToSample(h: StatsHistorySample): StatsSample | null {
  if (!num(h.t) || h.t <= 0 || !num(h.cpuPercent) || !num(h.memUsageMb) || !num(h.memLimitMb)) {
    return null
  }
  // 网络字段不折（历史没有它）：undefined = 无数据，见 StatsSample 的字段注。
  return {
    t: Math.floor(h.t),
    cpuPercent: h.cpuPercent,
    memUsageMb: h.memUsageMb,
    memLimitMb: h.memLimitMb
  }
}

/**
 * 历史段与实时段的衔接：两段在同一个滑窗里拼成一条 **t 严格递增** 的序列。
 *
 * 两条衔接边界的口径（测试同款钉死）：
 *   - **同 t 去重：丢历史、留流帧**。历史末样本与首帧撞在同一时刻时，流帧是
 *     刚从 agent 采下的当下值（且带网络字段），历史是回看拷贝 —— 留新弃旧；
 *   - **缺口连线**。历史末样本与首帧之间隔一段（受理 + 轮询建流的耗时，通常
 *     几秒；30s 的历史采样节拍本身就可能留 ≤30s 的缝）时，序列如实保留两个
 *     端点、不造中间假点 —— 曲线在时间比例轴上把这道缝画成相邻两点的直连
 *     （折线图相邻样本之间本就直线相连，悬停也只吸附真实样本），不为这道缝
 *     去动图表组件。
 *
 * 两侧都按升序喂入（历史端点升序返回、流样本按到达序入窗）；函数再兜一道
 * 底：t 不严格大于已入窗尾样本的一律不入 —— 乱序回吐与同 t 重复帧都不会让
 * 序列走回头路。上限沿用滑窗口径（默认 120 点）：历史逐点让位给实时，约
 * 2 分钟实时后窗口回到纯实时 —— 回看是「打开时」的窗口，不是常驻监控面。
 */
export function mergeStatsHistory(
  history: readonly StatsHistorySample[],
  stream: readonly StatsSample[],
  maxSamples = MAX_STATS_SAMPLES
): StatsSample[] {
  const cap =
    Number.isFinite(maxSamples) && maxSamples > 0 ? Math.floor(maxSamples) : MAX_STATS_SAMPLES
  const merged: StatsSample[] = []
  /** 入窗守卫：t 严格递增（同 t 丢旧、乱序丢迟到 —— 口径见函数注释）。 */
  const push = (s: StatsSample): void => {
    const tail = merged[merged.length - 1]
    if (tail !== undefined && s.t <= tail.t) return
    merged.push(s)
  }
  let i = 0
  let j = 0
  while (i < history.length || j < stream.length) {
    const h = i < history.length ? history[i] : undefined
    const s = j < stream.length ? stream[j] : undefined
    if (s !== undefined && (h === undefined || s.t <= h.t)) {
      // 流样本不晚于历史样本（或历史已尽）：流侧先入窗 —— 同 t 即在此去重
      // （留流弃历史）；流样本严格更早则历史样本留到下一轮再比。
      push(s)
      j++
      if (h !== undefined && s.t === h.t) i++
    } else if (h !== undefined) {
      // 历史样本更早（或流已尽）：折名入窗；坏样本丢点不丢段。
      const sample = historyToSample(h)
      i++
      if (sample !== null) push(sample)
    }
  }
  if (merged.length > cap) merged.splice(0, merged.length - cap)
  return merged
}
