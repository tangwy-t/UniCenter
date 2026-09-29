/**
 * 三期流通道的纯逻辑：NDJSON 行拆包、帧载荷解码（base64 → UTF-8 文本）、
 * 日志行缓冲（上限 5000 行、超出丢最旧）。
 *
 * 为什么下沉到纯函数：本仓库测试环境是 node（没有 jsdom），组件挂载不了 ——
 * 而这里恰好是**会静默出错**的地方：跨 chunk 的半行丢掉一段日志、UTF-8 字符被
 * 劈成两帧后变成替换符、缓冲上限差一行……页面都照常显示，只有肉眼能看出少了东西。
 *
 * 两条通道（日志 NDJSON / 终端 WS）的解码链是一样的：
 *   base64 载荷 → 字节 →（流式 UTF-8）→ 文本 → 行缓冲/终端渲染。
 */
import { MAX_LOG_LINES } from './log'

/** 数据帧的 base64 字母表（自己实现而不用 atob：node 与浏览器行为一致，且非法输入可预期）。 */
const B64_ALPHABET = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/'
const B64_REVERSE = (() => {
  const table = new Int16Array(256).fill(-1)
  for (let i = 0; i < B64_ALPHABET.length; i++) table[B64_ALPHABET.charCodeAt(i)] = i
  return table
})()

/**
 * base64 → 字节。
 *
 * 非法字符让**整帧作废**（返回空字节）而不是抛错或跳字符：一帧坏了只该丢这一段，
 * 不该把这条流打断，更不能把错位的字节拼出另一种内容。空白字符容忍（与
 * WHATWG forgiving-base64 同口径），`=` 之后不再取数据。
 */
export function decodeBase64(b64: string): Uint8Array {
  const out: number[] = []
  let acc = 0
  let bits = 0
  for (let i = 0; i < b64.length; i++) {
    const c = b64.charCodeAt(i)
    if (c === 0x3d) break // '='
    if (c === 0x20 || c === 0x0a || c === 0x0d || c === 0x09) continue
    const v = c < 256 ? B64_REVERSE[c] : -1
    if (v < 0) return new Uint8Array(0)
    acc = (acc << 6) | v
    bits += 6
    if (bits >= 8) {
      bits -= 8
      out.push((acc >> bits) & 0xff)
    }
  }
  return Uint8Array.from(out)
}

export interface FrameTextDecoder {
  /** 解一帧 base64；跨帧被劈开的 UTF-8 序列在这里续接。 */
  push(b64: string): string
  /** 流结束：把未完成的字节序冲出来（不完整序列给替换符），解码器回到干净状态。 */
  flush(): string
}

/**
 * 帧文本解码器（有状态）。
 *
 * 必须让 TextDecoder 工作在**流式**模式：agent 的合帧按时间/字节切，一个多字节
 * 汉字完全可能被切在两帧里；逐帧独立解码会把两半都变成替换符 —— 用户看到的是
 * 日志/终端里凭空多出的「」。PTY 控制序列是 ASCII 字节，原样通过。
 */
export function createFrameTextDecoder(): FrameTextDecoder {
  const decoder = new TextDecoder('utf-8')
  return {
    push(b64: string): string {
      return decoder.decode(decodeBase64(b64), { stream: true })
    },
    flush(): string {
      return decoder.decode()
    }
  }
}

/** 单帧便捷解码（无跨帧状态；只用于确定不会跨帧切分的场景）。 */
export function decodeFrameText(b64: string): string {
  return createFrameTextDecoder().push(b64)
}

export interface NdjsonParser {
  /** 喂一个原始 chunk，返回其中**完整**的行（空行不算）。半行缓存到下次。 */
  push(chunk: string): string[]
  /** 流结束时取走没有换行结尾的残留行。 */
  flush(): string
}

/**
 * NDJSON 行拆包器。
 *
 * 行尾的三种形态都按行尾处理（`\n`、`\r\n`、裸 `\r`）。`\r` 恰好落在 chunk 末尾时
 * **先扣住**：不知道下一 chunk 是不是以 `\n` 开头 —— 提前把 `\r` 当行尾，CRLF 跨
 * 边界就会多出一个幽灵空行（日志里会看起来凭空多了一行）。
 */
export function createNdjsonParser(): NdjsonParser {
  let pending = ''
  return {
    push(chunk: string): string[] {
      if (!chunk) return []
      let text = pending + chunk
      pending = ''
      if (text.endsWith('\r')) {
        pending = '\r'
        text = text.slice(0, -1)
      }
      text = text.replace(/\r\n/g, '\n').replace(/\r/g, '\n')
      const parts = text.split('\n')
      pending = (parts.pop() ?? '') + pending
      return parts.map((line) => line.trim()).filter((line) => line !== '')
    },
    flush(): string {
      const rest = (pending + '').replace(/\r+$/, '').trim()
      pending = ''
      return rest
    }
  }
}

/** 日志流的一行（core 的 NDJSON 形状：`{"seq":n,"data":"<base64>","eof":false}`）。 */
export interface LogStreamLine {
  seq: number
  /** 未带 data 的 eof 行给空串。 */
  data: string
  eof: boolean
}

/** 解析一行日志流帧；形状不符给 null（调用方跳过这一行，不中断整条流）。 */
export function parseLogStreamLine(line: string): LogStreamLine | null {
  let raw: unknown
  try {
    raw = JSON.parse(line)
  } catch {
    return null
  }
  if (typeof raw !== 'object' || raw === null || Array.isArray(raw)) return null
  const o = raw as Record<string, unknown>
  if (typeof o.seq !== 'number' || !Number.isFinite(o.seq)) return null
  if (o.data !== undefined && typeof o.data !== 'string') return null
  if (o.eof !== undefined && typeof o.eof !== 'boolean') return null
  return { seq: o.seq, data: typeof o.data === 'string' ? o.data : '', eof: o.eof === true }
}

/**
 * 日志行缓冲：按行累积、上限丢最旧、跨 chunk 的半个行缓存住。
 *
 * - `totalLines` 只增不减（暂停期间的「N 行」靠它算差值，即使最旧的行已被上限丢掉）；
 * - `text` 含未完成的行（跟着流长出来），不补尾换行；
 * - 行尾口径与一期 `splitLogLines` 一致：`\r\n`/裸 `\r` 都算行尾。
 */
export class LogLineBuffer {
  readonly maxLines: number
  private lines: string[] = []
  /** 未完成的行（可能是半行，也可能是扣住的 `\r`）。 */
  private pending = ''
  /** 累计的**完整**行数（含被上限丢掉的）。 */
  private seen = 0
  private dropped = 0

  constructor(maxLines = MAX_LOG_LINES) {
    this.maxLines = Number.isFinite(maxLines) && maxLines > 0 ? Math.floor(maxLines) : MAX_LOG_LINES
  }

  /** 未完成行的可渲染形态（扣住的尾部 `\r` 不显示）。 */
  private tail(): string {
    return this.pending.replace(/\r+$/, '')
  }

  push(chunk: string): void {
    if (!chunk) return
    let text = this.pending + chunk
    this.pending = ''
    // 末尾的 \r 先扣住：下一 chunk 若以 \n 开头，两者合起来才是一次换行。
    if (text.endsWith('\r')) {
      this.pending = '\r'
      text = text.slice(0, -1)
    }
    text = text.replace(/\r\n/g, '\n').replace(/\r/g, '\n')
    const parts = text.split('\n')
    this.pending = (parts.pop() ?? '') + this.pending
    for (const line of parts) {
      this.lines.push(line)
      this.seen++
    }
    this.trim()
  }

  /** 完成行 + 未完成行的总行数（即当前能看到的行数）。 */
  get lineCount(): number {
    return this.lines.length + (this.tail() !== '' ? 1 : 0)
  }

  /** 至今出现过的完整行数（不受上限影响）。 */
  get totalLines(): number {
    return this.seen
  }

  /** 是否因上限丢过最旧的行。 */
  get truncated(): boolean {
    return this.dropped > 0
  }

  /** 渲染文本（行间 `\n`，不补尾换行；未完成行也在）。 */
  get text(): string {
    const tail = this.tail()
    if (!this.lines.length) return tail
    const head = this.lines.join('\n')
    return tail === '' ? head : `${head}\n${tail}`
  }

  private trim(): void {
    // 未完成行也占一个名额：上限是「最多能看到多少行」，不是「最多能存多少完整行」。
    const budget = Math.max(1, this.maxLines - (this.tail() !== '' ? 1 : 0))
    if (this.lines.length > budget) {
      const cut = this.lines.length - budget
      this.lines.splice(0, cut)
      this.dropped += cut
    }
  }
}

export interface LogFeed {
  /** 喂入原始 NDJSON chunk（可跨帧、跨行，也可是半行）。 */
  pushRaw(chunk: string): void
  /** 当前缓冲的可渲染文本。 */
  readonly text: string
  /** 缓冲里的行数（含未完成行）。 */
  readonly lineCount: number
  /** 至今累计的完整行数（含被上限丢掉的）。 */
  readonly totalLines: number
  /** 是否因上限丢过最旧的行。 */
  readonly truncated: boolean
  /** 是否已收到 eof 帧。 */
  readonly eof: boolean
}

/**
 * 日志流消费端：把 NDJSON 原始 chunk 变成可渲染文本的一条流水线
 * （拆包 → 逐行解析 → base64/UTF-8 解码 → 行缓冲）。
 *
 * 解码器**跨帧有状态**：UTF-8 多字节被 agent 合帧切开时不会出现替换符。
 */
export function createLogFeed(maxLines = MAX_LOG_LINES): LogFeed {
  const parser = createNdjsonParser()
  const decoder = createFrameTextDecoder()
  const buffer = new LogLineBuffer(maxLines)
  let ended = false

  function handleLine(line: string): void {
    const frame = parseLogStreamLine(line)
    if (!frame) return // 形状意外的一行：跳过，不打断流
    if (frame.data !== '') buffer.push(decoder.push(frame.data))
    if (frame.eof) {
      buffer.push(decoder.flush())
      ended = true
    }
  }

  return {
    pushRaw(chunk: string): void {
      for (const line of parser.push(chunk)) handleLine(line)
    },
    get text(): string {
      return buffer.text
    },
    get lineCount(): number {
      return buffer.lineCount
    },
    get totalLines(): number {
      return buffer.totalLines
    },
    get truncated(): boolean {
      return buffer.truncated
    },
    get eof(): boolean {
      return ended
    }
  }
}
