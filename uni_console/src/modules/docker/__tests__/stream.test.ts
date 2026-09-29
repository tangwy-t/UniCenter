/**
 * 三期流通道纯逻辑的测试：NDJSON 拆包、帧解码（base64 → UTF-8）、日志行缓冲。
 *
 * 这些函数是 Follow 与终端两条通道的最后一跳，错了的形态是**静默**的：
 * 半行丢了日志少一段、UTF-8 被截成替换符、缓冲上限差一行……页面都照常显示。
 * 故这里把跨 chunk / 跨帧的边界钉死。
 */
import { describe, expect, it } from 'vitest'
import { MAX_LOG_LINES } from '../utils/log'
import {
  createFrameTextDecoder,
  createLogFeed,
  createNdjsonParser,
  decodeBase64,
  decodeFrameText,
  LogLineBuffer,
  parseLogStreamLine
} from '../utils/stream'

/** 测试里自己编码 base64（不依赖 atob/btoa 的可用性，且与 Go 的编码一致）。 */
function b64(text: string): string {
  return Buffer.from(text, 'utf8').toString('base64')
}

describe('NDJSON 行拆包', () => {
  it('一个 chunk 里的多行一次给出，行尾换行不产生空行', () => {
    const p = createNdjsonParser()
    expect(p.push('{"a":1}\n{"b":2}\n')).toEqual(['{"a":1}', '{"b":2}'])
  })

  it('跨 chunk 的半行被缓存，拼上后半行后才成行', () => {
    const p = createNdjsonParser()
    expect(p.push('{"a":1}\n{"b')).toEqual(['{"a":1}'])
    expect(p.push('":2}\n')).toEqual(['{"b":2}'])
  })

  it('CRLF 恰好断在 \\r 与 \\n 之间时不产生幽灵空行', () => {
    const p = createNdjsonParser()
    expect(p.push('{"a":1}\r')).toEqual([])
    expect(p.push('\n{"b":2}\r\n')).toEqual(['{"a":1}', '{"b":2}'])
  })

  it('空行与纯空白行不是帧', () => {
    const p = createNdjsonParser()
    expect(p.push('\n  \n{"a":1}\n\n')).toEqual(['{"a":1}'])
  })

  it('flush 给出没有换行结尾的残留行（流自然结束在最后一行中间时）', () => {
    const p = createNdjsonParser()
    p.push('{"a":1}\n{"b":2}')
    expect(p.flush()).toBe('{"b":2}')
    expect(p.flush()).toBe('')
  })

  it('裸 CR 也按行尾拆（部分 Windows 工具会改写行尾）', () => {
    const p = createNdjsonParser()
    // 末尾的 \r 可能只是 \r\n 的前半，行先扣住；下个 chunk 或 flush 时才落定。
    expect(p.push('{"a":1}\r{"b":2}\r')).toEqual(['{"a":1}'])
    expect(p.flush()).toBe('{"b":2}')
  })
})

describe('base64 → 字节与文本', () => {
  it('ASCII 原样还原', () => {
    expect(decodeFrameText(b64('hello'))).toBe('hello')
  })

  it('UTF-8 中文正确解码（不是逐字节的 Latin-1）', () => {
    expect(decodeFrameText(b64('日志：启动完成'))).toBe('日志：启动完成')
  })

  it('PTY 控制序列逐字节保留（颜色/光标序列不能被解码弄丢）', () => {
    const raw = '\x1b[31mred\x1b[0m\r\n'
    expect(decodeFrameText(b64(raw))).toBe(raw)
  })

  it('NUL 也是合法字节（二进制输出不吞字节）', () => {
    expect(decodeBase64(b64('a\x00b'))).toEqual(Uint8Array.from([0x61, 0x00, 0x62]))
  })

  it('非法 base64 给空字节而不是抛错（一帧坏了不打断整条流）', () => {
    expect(decodeBase64('!!not-base64!!')).toEqual(new Uint8Array(0))
    expect(decodeFrameText('!!!')).toBe('')
  })

  it('一个 UTF-8 字符被劈成两帧时能续接，不留替换符', () => {
    const bytes = Buffer.from('你好', 'utf8')
    const first = bytes.subarray(0, 2).toString('base64') // “你”只给了前 2 字节
    const second = bytes.subarray(2).toString('base64')
    const d = createFrameTextDecoder()
    expect(d.push(first)).toBe('')
    expect(d.push(second)).toBe('你好')
  })

  it('flush 清掉未完成的字节序（流结束时不留内部状态）', () => {
    const bytes = Buffer.from('好', 'utf8')
    const d = createFrameTextDecoder()
    d.push(bytes.subarray(0, 1).toString('base64'))
    // 剩余字节没到就结束：只能给替换符，但 flush 后解码器回到干净状态。
    expect(d.flush()).toContain('\uFFFD')
    expect(d.push(b64('ok'))).toBe('ok')
  })
})

describe('日志行缓冲（上限 5000 行，超出丢最旧）', () => {
  it('默认上限与提示文案同源（5000）', () => {
    const b = new LogLineBuffer()
    expect(b.maxLines).toBe(MAX_LOG_LINES)
  })

  it('超上限只保留最近一段，并记录累计行数与截断', () => {
    const b = new LogLineBuffer(3)
    b.push('l1\nl2\nl3\nl4\nl5\n')
    expect(b.text).toBe('l3\nl4\nl5')
    expect(b.lineCount).toBe(3)
    expect(b.totalLines).toBe(5)
    expect(b.truncated).toBe(true)
  })

  it('恰好等于上限时不算截断', () => {
    const b = new LogLineBuffer(3)
    b.push('l1\nl2\nl3\n')
    expect(b.text).toBe('l1\nl2\nl3')
    expect(b.truncated).toBe(false)
  })

  it('未完成的行也渲染（跟着流长出来），完成后不重复', () => {
    const b = new LogLineBuffer(10)
    b.push('boot')
    expect(b.text).toBe('boot')
    expect(b.lineCount).toBe(1)
    b.push('ing\n')
    expect(b.text).toBe('booting')
    expect(b.lineCount).toBe(1)
    expect(b.totalLines).toBe(1)
  })

  it('CRLF 跨 chunk 拆开时只算一次换行', () => {
    const b = new LogLineBuffer(10)
    b.push('a\r')
    b.push('\nb\r\nc')
    expect(b.text).toBe('a\nb\nc')
    expect(b.totalLines).toBe(2)
  })

  it('中间的空行是真实内容，保留', () => {
    const b = new LogLineBuffer(10)
    b.push('a\n\nb\n')
    expect(b.text).toBe('a\n\nb')
    expect(b.totalLines).toBe(3)
  })

  it('累计行数只增不减（暂停计数靠它算差值）', () => {
    const b = new LogLineBuffer(2)
    b.push('1\n2\n3\n4\n5\n')
    expect(b.totalLines).toBe(5)
    expect(b.lineCount).toBe(2)
  })
})

describe('日志流的一行帧', () => {
  it('解析数据帧', () => {
    expect(parseLogStreamLine('{"seq":3,"data":"aGk=","eof":false}')).toEqual({
      seq: 3,
      data: 'aGk=',
      eof: false
    })
  })

  it('eof 帧可以不带 data（末帧数据与 eof 同帧也允许）', () => {
    expect(parseLogStreamLine('{"seq":9,"eof":true}')).toEqual({ seq: 9, data: '', eof: true })
    expect(parseLogStreamLine('{"seq":9,"data":"eA==","eof":true}')).toEqual({
      seq: 9,
      data: 'eA==',
      eof: true
    })
  })

  it('形状不符的行给 null（调用方跳过，不中断整条流）', () => {
    expect(parseLogStreamLine('not json')).toBeNull()
    expect(parseLogStreamLine('[]')).toBeNull()
    expect(parseLogStreamLine('"str"')).toBeNull()
    expect(parseLogStreamLine('{"seq":"1"}')).toBeNull()
    expect(parseLogStreamLine('{"seq":1,"data":42}')).toBeNull()
    expect(parseLogStreamLine('{"seq":1,"eof":"no"}')).toBeNull()
  })
})

describe('日志流消费端（拆包 + 解码 + 缓冲的一条流水线）', () => {
  it('跨 chunk 的半行与 UTF-8 跨帧都能还原', () => {
    const feed = createLogFeed()
    const line1 = `{"seq":1,"data":"${b64('日志一\n')}","eof":false}\n`
    const line2 = `{"seq":2,"data":"${b64('日志二\n')}","eof":false}\n`
    // 从最别扭的地方切：一条行中间切开
    feed.pushRaw(line1.slice(0, 10))
    feed.pushRaw(line1.slice(10) + line2.slice(0, 20))
    expect(feed.text).toBe('日志一')
    feed.pushRaw(line2.slice(20))
    expect(feed.text).toBe('日志一\n日志二')
    expect(feed.totalLines).toBe(2)
    expect(feed.eof).toBe(false)
  })

  it('eof 帧置位（可与末帧数据同帧），其后不再有内容', () => {
    const feed = createLogFeed()
    feed.pushRaw(`{"seq":1,"data":"${b64('tail line')}","eof":true}\n`)
    expect(feed.text).toBe('tail line')
    expect(feed.eof).toBe(true)
  })

  it('形状意外的行被跳过，不影响后续行', () => {
    const feed = createLogFeed()
    feed.pushRaw('broken line\n')
    feed.pushRaw(`{"seq":2,"data":"${b64('ok')}","eof":false}\n`)
    expect(feed.text).toBe('ok')
  })

  it('上限仍然是总缓冲：累计行数继续涨，文本只留最近一段', () => {
    const feed = createLogFeed(2)
    for (let i = 1; i <= 4; i++) {
      feed.pushRaw(`{"seq":${i},"data":"${b64(`l${i}\n`)}","eof":false}\n`)
    }
    expect(feed.text).toBe('l3\nl4')
    expect(feed.totalLines).toBe(4)
    expect(feed.truncated).toBe(true)
  })
})
