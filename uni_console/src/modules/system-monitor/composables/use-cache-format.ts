/**
 * Redis 值展示的纯格式化工具。
 *
 * 从 views/cache.vue 抽出:原文件 1900+ 行,这些函数是其中唯一位于
 * "输入 → 输出"、不依赖任何响应式状态的逻辑,抽出来即可被直接单测
 * (此前完全依赖人工点页面验证)。视图侧只保留状态与编排。
 */

/** 语法高亮的一段 token。 */
export interface CodeToken {
  cls: string
  text: string
}

/**
 * 极简 JSON 分词器:把源码切成"键 / 字符串 / 数字 / 关键字 / 标点 / 其他"
 * 六类 token,交给模板按 cls 上色。
 *
 * 刻意不引入高亮库:这里只需要区分颜色,不需要语法校验,
 * 且输入可能是被截断的 JSON(不保证可解析)。
 *
 * @param src 待高亮的文本(通常是一段 JSON 字符串)
 */
export function highlightJson(src: string): CodeToken[] {
  const tokens: CodeToken[] = []
  let i = 0
  const n = src.length
  let buf = ''
  const flush = (cls: string) => {
    if (buf) {
      tokens.push({ cls, text: buf })
      buf = ''
    }
  }

  while (i < n) {
    const ch = src[i]

    // 字符串:注意处理转义(\\" 不应结束字符串)
    if (ch === '"') {
      flush('tok-plain')
      let j = i + 1
      let out = '"'
      while (j < n) {
        out += src[j]
        if (src[j] === '\\' && j + 1 < n) {
          out += src[j + 1]
          j += 2
          continue
        }
        if (src[j] === '"') {
          j++
          break
        }
        j++
      }
      // 向后跳过空白,若下一个有效字符是 ':' 则该字符串是对象的键
      let k = j
      while (k < n && /\s/.test(src[k])) k++
      const isKey = src[k] === ':'
      tokens.push({ cls: isKey ? 'tok-key' : 'tok-str', text: out })
      i = j
      continue
    }

    if (/[{}[\]:,]/.test(ch)) {
      flush('tok-plain')
      tokens.push({ cls: 'tok-punct', text: ch })
      i++
      continue
    }

    const word = src.slice(i).match(/^(true|false|null)\b/)
    if (word) {
      flush('tok-plain')
      tokens.push({ cls: 'tok-kw', text: word[0] })
      i += word[0].length
      continue
    }

    const num = src.slice(i).match(/^-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?/)
    if (num) {
      flush('tok-plain')
      tokens.push({ cls: 'tok-num', text: num[0] })
      i += num[0].length
      continue
    }

    buf += ch
    i++
  }

  flush('tok-plain')
  return tokens
}

/**
 * 格式化 zset 的 score 展示:整数原样,小数最多保留 3 位。
 * 避免浮点误差产生 0.30000000000000004 这类噪声。
 */
export function formatScore(score: number): string {
  return Number.isInteger(score) ? String(score) : String(Math.round(score * 1000) / 1000)
}

/**
 * 把用户输入转换为 Redis SCAN 的 glob 模式。
 *
 * - 空输入 → undefined(表示不限制)
 * - 已含 `*` 则认为用户显式给了模式,原样使用
 * - 否则视为前缀,补 `*`(最常见的用法:按命名空间前缀过滤)
 *
 * 行为与抽取前逐字一致(仅识别 `*`,不识别 `?`/`[]`)——
 * 抽取只做搬运,不顺手改语义,避免把无关行为变化混进来。
 */
export function toPattern(input: string): string | undefined {
  const t = input.trim()
  if (!t) return undefined
  return t.includes('*') ? t : `${t}*`
}

/** 把任意值格式化为可展示文本:字符串原样,其余走 JSON(失败则 String)。 */
export function formatValue(value: unknown): string {
  if (value === null || value === undefined) return ''
  if (typeof value === 'string') return value
  try {
    return JSON.stringify(value, null, 2)
  } catch {
    return String(value)
  }
}
