import { describe, expect, it } from 'vitest'
import { formatScore, formatValue, highlightJson, toPattern } from './use-cache-format'

// 这些函数从 views/cache.vue(1900+ 行)抽出。抽取前它们只能靠人工点
// 页面验证;抽出后在此覆盖边界行为,防止后续改动静默破坏展示效果。

describe('highlightJson', () => {
  it('区分键与字符串值', () => {
    const tokens = highlightJson('{"a":"b"}')
    const key = tokens.find((t) => t.cls === 'tok-key')
    const str = tokens.find((t) => t.cls === 'tok-str')
    expect(key?.text).toBe('"a"')
    expect(str?.text).toBe('"b"')
  })

  it('识别标点、关键字与数字', () => {
    const tokens = highlightJson('{"n":1,"t":true,"z":null}')
    expect(tokens.filter((t) => t.cls === 'tok-punct').map((t) => t.text)).toEqual([
      '{',
      ':',
      ',',
      ':',
      ',',
      ':',
      '}'
    ])
    expect(tokens.filter((t) => t.cls === 'tok-kw').map((t) => t.text)).toEqual(['true', 'null'])
    expect(tokens.filter((t) => t.cls === 'tok-num').map((t) => t.text)).toEqual(['1'])
  })

  it('字符串内的转义引号不会提前结束字符串', () => {
    const tokens = highlightJson('"a\\"b"')
    const str = tokens.find((t) => t.cls === 'tok-str')
    expect(str?.text).toBe('"a\\"b"')
  })

  it('键判定跳过冒号前的空白', () => {
    const tokens = highlightJson('{"a"   : 1}')
    expect(tokens.find((t) => t.text === '"a"')?.cls).toBe('tok-key')
  })

  it('支持负数、小数与科学计数法', () => {
    const tokens = highlightJson('[-1,2.5,1e10,-3.2e-4]')
    expect(tokens.filter((t) => t.cls === 'tok-num').map((t) => t.text)).toEqual([
      '-1',
      '2.5',
      '1e10',
      '-3.2e-4'
    ])
  })

  it('不完整的 JSON(截断)不抛错', () => {
    expect(() => highlightJson('{"a": "unterminated')).not.toThrow()
  })

  it('空串返回空数组', () => {
    expect(highlightJson('')).toEqual([])
  })

  it('token 拼接后与原文一致(不丢字符)', () => {
    // 高亮只是分词上色,不应改变可见文本。
    const src = '{"k": [1, "v"], "n": null}'
    expect(
      highlightJson(src)
        .map((t) => t.text)
        .join('')
    ).toBe(src)
  })
})

describe('formatScore', () => {
  it('整数原样输出', () => {
    expect(formatScore(42)).toBe('42')
    expect(formatScore(-7)).toBe('-7')
  })

  it('小数最多保留 3 位(消除浮点噪声)', () => {
    expect(formatScore(0.30000000000000004)).toBe('0.3')
    expect(formatScore(1.23456)).toBe('1.235')
  })
})

describe('toPattern', () => {
  it('空输入返回 undefined(不限制)', () => {
    expect(toPattern('')).toBeUndefined()
    expect(toPattern('   ')).toBeUndefined()
  })

  it('无 * 时视为前缀并补 *', () => {
    expect(toPattern('user:')).toBe('user:*')
  })

  it('已含 * 时原样使用', () => {
    expect(toPattern('user:*:active')).toBe('user:*:active')
  })

  it('去除首尾空白', () => {
    expect(toPattern('  a  ')).toBe('a*')
  })
})

describe('formatValue', () => {
  it('null/undefined 返回空串', () => {
    expect(formatValue(null)).toBe('')
    expect(formatValue(undefined)).toBe('')
  })

  it('字符串原样返回(不加引号)', () => {
    expect(formatValue('hello')).toBe('hello')
  })

  it('对象格式化为缩进 JSON', () => {
    expect(formatValue({ a: 1 })).toBe('{\n  "a": 1\n}')
  })

  it('数字与布尔转字符串', () => {
    expect(formatValue(5)).toBe('5')
    expect(formatValue(false)).toBe('false')
  })

  it('循环引用不抛错(降级为 String)', () => {
    const cyclic: Record<string, unknown> = {}
    cyclic.self = cyclic
    expect(() => formatValue(cyclic)).not.toThrow()
  })
})
