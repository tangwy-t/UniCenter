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
 *
 * `totalOverride`：跟随流（三期）里缓冲上限之外的**累计**行数由流侧单独给 —— 文本只剩
 * 最近 5000 行，但「共 N 行」应该报真实累计数，否则数字与「仅显示最近 5000 行」自相矛盾。
 */
export function logHint(text: string, truncated: boolean, totalOverride?: number): string {
  const counted = splitLogLines(text).length
  const total = totalOverride != null && totalOverride > counted ? totalOverride : counted
  if (total === 0) return '尚未取到日志'
  const base = `共 ${total} 行`
  return truncated ? `${base}（仅显示最近 ${MAX_LOG_LINES} 行）` : base
}

// ── compose 聚合日志的服务过滤（5b 项目工作台）────────────────────────────

/**
 * compose 聚合行的服务名前缀：`服务名 | 正文`（CLI 按最长服务名对齐补空格，
 * 三种 flavor 一致；agent 侧固定带 --timestamps，正文以时间戳开头）。
 * 服务名字符集与协议的项目/服务名白名单同形：字母数字开头 + [A-Za-z0-9_.-]*。
 */
const COMPOSE_LOG_PREFIX_RE = /^[A-Za-z0-9][A-Za-z0-9_.-]*\s*\|/

/** 取一行聚合日志归属的服务名；行不带该前缀（CLI 自己的结论句等）返回空串。 */
export function composeLogLineService(line: string): string {
  if (!COMPOSE_LOG_PREFIX_RE.test(line)) return ''
  // 前缀正则已保证「服务名 + 空白 + |」，服务名即竖线前的去空白段。
  return line.slice(0, line.indexOf('|')).trim()
}

/**
 * 按服务过滤聚合日志文本（纯前端过滤：会话不分服务开流，过滤不重开会话）。
 *
 * 口径：选中集合为空 = 不过滤（原文返回）。选中后只保留**服务名命中**的行；
 * 不带服务前缀的行（compose CLI 的报错/收尾句）**不保留** —— 过滤的语义是
 * 「只听选中的网元」，CLI 的行不是任何网元说的。行尾口径与拆行一致
 * （`\r\n`/裸 `\r` 都按行尾处理；流式期间最后一行可能是半行，照常参与匹配）。
 */
export function filterComposeLogLines(text: string, services: readonly string[]): string {
  const picked = services.filter((s) => s !== '')
  if (picked.length === 0) return text
  const set = new Set(picked)
  const lines = splitLogLines(text)
  // 尾部换行产物的空行不算日志行（与 splitLogLines 同口径），保留中间空行的语义
  // 由「命中行的原样保留」延续 —— 未命中行整行丢弃，行间换行重排。
  return lines.filter((line) => set.has(composeLogLineService(line))).join('\n')
}
