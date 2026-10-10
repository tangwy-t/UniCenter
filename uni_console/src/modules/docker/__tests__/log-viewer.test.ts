import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import {
  detectLogLevel,
  followStateText,
  formatLogTs,
  logHint,
  MAX_LOG_LINES,
  parseLogLine,
  splitLogLines,
  visibleLines
} from '../utils/log'

// 日志查看器的逻辑全部在这几个纯函数里（组件不挂载：测试环境是 node，没有 jsdom）。
describe('日志拆行', () => {
  it('空文本没有任何行', () => {
    expect(splitLogLines('')).toEqual([])
  })

  it('尾部的换行不算一个空行', () => {
    expect(splitLogLines('a\nb\n')).toEqual(['a', 'b'])
    expect(splitLogLines('a\nb')).toEqual(['a', 'b'])
  })

  it('CRLF 按行尾拆干净，行内不留不可见字符', () => {
    const lines = splitLogLines('a\r\nb\r\n')
    expect(lines).toEqual(['a', 'b'])
    // 留下 \r 会让「行尾的词」永远搜不到，复制出去还会多出空行。
    for (const line of lines) expect(line.includes('\r')).toBe(false)
  })

  it('裸 CR 也算行尾（旧程序与部分 Windows 产物会这么写）', () => {
    expect(splitLogLines('a\rb\rc')).toEqual(['a', 'b', 'c'])
  })

  it('中间的空行是真实内容，保留', () => {
    expect(splitLogLines('a\n\nb')).toEqual(['a', '', 'b'])
  })
})

describe('缓冲上限与关键字过滤', () => {
  it('空输入给空结果（页面据此显示「尚未取到日志」）', () => {
    expect(visibleLines('')).toEqual({
      text: '',
      lines: [],
      totalCount: 0,
      shownCount: 0,
      truncated: false
    })
  })

  it('超上限只保留最近一段，并报告被截断', () => {
    const v = visibleLines('l1\nl2\nl3\nl4\nl5', '', 3)
    expect(v.text).toBe('l3\nl4\nl5')
    expect(v.shownCount).toBe(3)
    expect(v.totalCount).toBe(5)
    expect(v.truncated).toBe(true)
  })

  it('恰好等于上限时不算截断（「仅显示最近 N 行」是给真被截断的内容看的）', () => {
    const v = visibleLines('l1\nl2\nl3', '', 3)
    expect(v.truncated).toBe(false)
    expect(v.text).toBe('l1\nl2\nl3')
  })

  it('默认上限是 5000（提示文案里的数字与缓冲同源）', () => {
    expect(MAX_LOG_LINES).toBe(5000)
    const text = Array.from({ length: MAX_LOG_LINES + 1 }, (_, i) => `l${i + 1}`).join('\n')
    const v = visibleLines(text)
    expect(v.truncated).toBe(true)
    expect(v.shownCount).toBe(MAX_LOG_LINES)
    expect(v.text.startsWith('l2\n')).toBe(true)
    expect(v.text.endsWith('l5001')).toBe(true)
  })

  it('关键字只保留命中行，总行数仍按全文计', () => {
    const v = visibleLines('a\nERROR x\nERROR y\nb', 'ERROR')
    expect(v.text).toBe('ERROR x\nERROR y')
    expect(v.shownCount).toBe(2)
    expect(v.totalCount).toBe(4)
    expect(v.truncated).toBe(false)
  })

  it('关键字大小写敏感（ERROR 与 error 在日志里常是两回事）', () => {
    expect(visibleLines('ERROR x', 'error').text).toBe('')
  })

  it('全空白关键字视为不过滤', () => {
    expect(visibleLines('a\nb', '   ').text).toBe('a\nb')
  })

  it('过滤在截断之后：只在被丢掉那一段里的关键字不会命中', () => {
    const v = visibleLines('needle\nl2\nl3\nl4', 'needle', 2)
    expect(v.text).toBe('')
    expect(v.shownCount).toBe(0)
    expect(v.totalCount).toBe(4)
    expect(v.truncated).toBe(true)
  })

  it('CRLF 的日志能按行尾的词命中，且渲染文本不带不可见字符', () => {
    const v = visibleLines('booting\r\nstarted\r\n', 'started')
    expect(v.text).toBe('started')
  })
})

describe('提示文案', () => {
  it('没有日志时说「尚未取到日志」，不说「共 0 行」', () => {
    expect(logHint('', false)).toBe('尚未取到日志')
  })

  it('未截断时只说行数', () => {
    expect(logHint('a\nb\n', false)).toBe('共 2 行')
  })

  it('被截断时说清楚保留了哪一段', () => {
    expect(logHint('a\nb\n', true)).toBe('共 2 行（仅显示最近 5000 行）')
  })

  it('跟随流给累计行数时，「共 N 行」报真实累计而不是缓冲里剩下的', () => {
    const text = Array.from({ length: 3 }, (_, i) => `l${i + 1}`).join('\n')
    expect(logHint(text, true, 12000)).toBe('共 12000 行（仅显示最近 5000 行）')
    // 累计数不大于文本行数时以文本为准（避免把「尚未取到」说成有行）
    expect(logHint(text, false, 2)).toBe('共 3 行')
    expect(logHint('', false, 0)).toBe('尚未取到日志')
  })
})

describe('逐行结构：时间戳 + 级别（Wave 1）', () => {
  it('无前导时间戳的行：ts 缺席、正文原样（容器日志通常没有时间戳，绝不伪造）', () => {
    const line = parseLogLine('hello world')
    expect(line.ts).toBeUndefined()
    expect(line.text).toBe('hello world')
    expect(line.level).toBe('info')
  })

  it('有前导时间戳的行：ts 摘出、正文去掉时间戳与后随空格', () => {
    const line = parseLogLine('2026-09-30T08:00:00.123456789Z web-1  | GET /health 200')
    expect(line.ts).toBe('2026-09-30T08:00:00.123456789Z')
    expect(line.text).toBe('web-1  | GET /health 200')
  })

  it('parseLogLine 的行首时间戳只在开头命中（行中出现的时间戳不动）', () => {
    const line = parseLogLine('前缀 2026-09-30T08:00:00Z 不是行首')
    expect(line.ts).toBeUndefined()
    expect(line.text).toBe('前缀 2026-09-30T08:00:00Z 不是行首')
  })

  it('compose 聚合日志真机形态：服务前缀在前、时间戳在后（前缀保留进正文）', () => {
    // 2026-10-02 本机真机实测样本（docker compose v5.1.4）。
    const line = parseLogLine('svc2-1        | 2026-10-02T02:35:59.962206484Z svc2 line one')
    expect(line.ts).toBe('2026-10-02T02:35:59.962206484Z')
    expect(line.text).toBe('svc2-1        | svc2 line one')
  })

  it('级别识别：error / warn / info（保守词表，大小写不敏感）', () => {
    expect(detectLogLevel('ERROR failed to connect')).toBe('error')
    expect(detectLogLevel('panic: runtime error')).toBe('error')
    expect(detectLogLevel('WARN disk almost full')).toBe('warn')
    expect(detectLogLevel('warning: deprecated')).toBe('warn')
    expect(detectLogLevel('GET /health 200')).toBe('info')
    // 普通正文里的裸词不误伤（按词边界）。
    expect(detectLogLevel('terrarium built')).toBe('info')
  })

  it('时间戳展示形态：去 T 与亚秒、保留时区后缀；异常形状原样返回', () => {
    expect(formatLogTs('2026-09-30T08:00:00.123456789Z')).toBe('2026-09-30 08:00:00Z')
    expect(formatLogTs('2026-09-30T08:00:00+08:00')).toBe('2026-09-30 08:00:00+08:00')
    expect(formatLogTs('not-a-timestamp')).toBe('not-a-timestamp')
  })

  it('visibleLines 的 text 是原文（含时间戳），lines 是逐行结构（同源）', () => {
    const v = visibleLines('2026-09-30T08:00:00Z ERROR boom\nplain line')
    // 复制/下载路径拿到的是原文（保真）。
    expect(v.text).toBe('2026-09-30T08:00:00Z ERROR boom\nplain line')
    expect(v.lines).toHaveLength(2)
    expect(v.lines[0]!.ts).toBe('2026-09-30T08:00:00Z')
    expect(v.lines[0]!.text).toBe('ERROR boom')
    expect(v.lines[0]!.level).toBe('error')
    expect(v.lines[1]!.ts).toBeUndefined()
    expect(v.lines[1]!.text).toBe('plain line')
  })

  it('跟随态三态词', () => {
    expect(followStateText('live')).toBe('正在跟随')
    expect(followStateText('paused')).toBe('已暂停')
    expect(followStateText('ended')).toBe('已结束')
  })
})

describe('注入面守卫：日志渲染路径不含 v-html（Wave 1）', () => {
  it('log-viewer.vue 只用文本插值渲染日志正文，源码里不出现 v-html', () => {
    const src = readFileSync(
      new URL('../components/log-viewer.vue', import.meta.url).pathname,
      'utf8'
    )
    // 日志是不可信文本 —— 渲染路径必须零 v-html（断言的是属性用法 `v-html=`，
    // 文件里那句「不要改成 v-html」的注释不构成用法）。
    expect(src).not.toContain('v-html=')
    // 且正文走文本插值（v-for + {{ }}），与本条的意图互证。
    expect(src).toContain('v-for="(line, i) in bodyLines"')
    expect(src).toContain('{{ line.text }}')
  })
})
