/**
 * 日志查看器的纯逻辑：拆行、缓冲上限、关键字过滤与提示文案。
 *
 * 为什么这些逻辑要下沉到纯函数：本仓库的测试环境是 node（没有 jsdom），组件挂载不了 ——
 * 能测的只剩纯函数。而这里恰好是**会静默出错**的地方：CRLF 少拆一次会多出一个不可见
 * 字符（复制出去在别的工具里变成空行，行内搜索也永远命中不了行尾的词）；缓冲截断差一行
 * 会让「最近 5000 行」这个数字与眼前的内容对不上。
 */

/** 一次最多渲染的行数（缓冲上限）。提示文案里的那个数字也用它，两者必须同源。 */
export const MAX_LOG_LINES = 5000

/**
 * 拆成行：`\r\n` 与裸 `\r` 都按行尾处理。
 *
 * 只按 `\n` 拆是不够的：Windows 容器的日志行尾是 `\r\n`，每行会连带一个不可见的 `\r`。
 *
 * 尾部换行产生的那个空串不是一段日志（文本以换行结尾是常态），丢掉；但**中间**的空行
 * 是真实内容（日志里常有空行分隔），必须保留。
 */
export function splitLogLines(text: string): string[] {
  if (!text) return []
  const lines = text.replace(/\r\n?/g, '\n').split('\n')
  if (lines[lines.length - 1] === '') lines.pop()
  return lines
}

export interface VisibleLog {
  /** 渲染文本：保留下来的行以 `\n` 连接；过滤后没有命中行时是空串。 */
  text: string
  /** 文本里的总行数（提示「共 N 行」用，不受缓冲与过滤影响）。 */
  totalCount: number
  /** 实际参与渲染的行数（截断并过滤之后的）。 */
  shownCount: number
  /** 是否因缓冲上限只保留了最近的一段。 */
  truncated: boolean
}

/**
 * 缓冲尾部 + 关键字过滤。
 *
 * 两点口径：
 * 1. **纯文本**：返回的只是文本，页面用文本插值渲染（`{{ }}`）—— 日志是不可信文本，
 *    任何 HTML 拼接都是注入面，故这里不做也不返回任何富文本。
 * 2. 顺序是「先截到缓冲上限、再按关键字过滤」：与「仅显示最近 5000 行」的文案一致
 *    （关键字是在留下的这一段里找）。反过来先把全部行过滤一遍再截，既费时，也会让
 *    「共 N 行」与眼前的内容对不上。
 *
 * 关键字命中是行内子串且**大小写敏感**（与 grep 同口径：日志里的人为约定—— ERROR 与
 * error 常常是两回事）。全空白的关键字视为「不过滤」。
 */
export function visibleLines(text: string, keyword = '', maxLines = MAX_LOG_LINES): VisibleLog {
  const all = splitLogLines(text)
  const cap = Number.isFinite(maxLines) && maxLines > 0 ? Math.floor(maxLines) : MAX_LOG_LINES
  const truncated = all.length > cap
  const kept = truncated ? all.slice(-cap) : all
  const kw = keyword.trim()
  const shown = kw ? kept.filter((line) => line.includes(kw)) : kept
  return {
    text: shown.join('\n'),
    totalCount: all.length,
    shownCount: shown.length,
    truncated
  }
}

/**
 * 提示文案。
 *
 * 「仅显示最近 5000 行」而不是「已截断」：前者说清了**保留了哪一段** —— 用户据此才知道
 * 该往回翻还是换个更大的行数重拉；只说「已截断」，他连丢的是头还是尾都不确定。
 */
export function logHint(text: string, truncated: boolean): string {
  const total = splitLogLines(text).length
  if (total === 0) return '尚未取到日志'
  const base = `共 ${total} 行`
  return truncated ? `${base}（仅显示最近 ${MAX_LOG_LINES} 行）` : base
}
