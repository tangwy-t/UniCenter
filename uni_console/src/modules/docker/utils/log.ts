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
 * compose 聚合行的前缀：`名字(对齐空白) | 正文`。名字形态随 CLI 而变 ——
 * 2026-10-02 本机真机实测（docker compose v5.1.4，argv 与 agent 侧逐字一致：
 * `docker compose -p qa -f … logs --timestamps --tail N`）拿到的是**项目名剥离后的
 * 容器短名**：`svc2-1` / `svc20-2` / `svc2-extra-1`（容器全名 `qa-svc2-1` 里的
 * 项目前缀被显示时剥掉）；QA 真栈另观察到**带项目前缀的容器全名**形态；v1
 * flavor 的历史形态是**服务名本体**（按最长名对齐补空格）。
 *
 * 对齐空白不可依赖：实测对齐到「最长前缀 + 2 空格」，但同一批行里出现过
 * pipe 列 10/15 混排（首行早于其余行的对齐窗口），故只按「竖线前去空白段」
 * 取前缀 —— 任何按列宽解析都会踩空。
 *
 * 名字字符集与协议的项目/服务名白名单同形：字母数字开头 + [A-Za-z0-9_.-]*。
 */
const COMPOSE_LOG_PREFIX_RE = /^[A-Za-z0-9][A-Za-z0-9_.-]*\s*\|/

/**
 * 一行聚合日志归属的服务名（在候选集合 services 内解码）；不属于任何候选
 * （CLI 自己的结论句、别的项目的行……）返回空串。project 给「容器全名」形态
 * 剥前缀用，可省略。
 *
 * 三种实测形态的命中规则（段边界纪律是核心）：
 *  1. 前缀与候选**等值**：v1 形态（前缀本就是服务名，`core  | …`）；
 *  2. 前缀 = `候选-序号`（**短名**，v5 实测形态）：序号段必须是纯数字 ——
 *     `svc2` 命中 `svc2-1`，但 `svc20-1` 不命中（`0` 黏在服务名后，`-` 不在
 *     段边界）、`svc2-extra-1` 也不命中（`extra` 不是数字段）；
 *  3. 前缀 = `项目-候选-序号`（**全名**，QA 真栈观察的形态）：剥一层
 *     `project-` 后按 1/2 解码；剥离要求前缀真带该前缀，且解码仍在候选集合内
 *     —— `qa` 项目里 `qa-svc20-1` 剥后是 `svc20-1`，不会归 `svc2`。
 *
 * 为什么必须在候选集合内解码、而不是「从前缀里解析出服务名再比对集合」：
 * `svc2-1` 由容器名而来时是「服务 svc2 的 1 号容器」，但它也可能字面就是某
 * 服务名（v1 里服务就叫 `svc2-1` 时前缀即 `svc2-1`）—— 离开候选集合二者
 * 不可分。把解码收敛到「选中的服务集」上，恰是过滤要问的问题：
 * 「这行是不是选中的网元说的」。优先序：等值 > 短名序号 > 全名（同层按候选
 * 顺序）；等值最优先，因为等值命中是「字面就叫这名」的最强证据。
 */
export function composeLogLineService(
  line: string,
  services: readonly string[],
  project = ''
): string {
  if (!COMPOSE_LOG_PREFIX_RE.test(line)) return ''
  // 前缀正则已保证「名字 + 空白 + |」，名字即竖线前的去空白段（对齐空白不定长）。
  const prefix = line.slice(0, line.indexOf('|')).trim()
  /** `p` 是否为服务 `s` 的「段边界 + 纯数字序号」容器形。 */
  const indexedAs = (p: string, s: string): boolean => {
    if (!p.startsWith(`${s}-`)) return false
    const ordinal = p.slice(s.length + 1)
    return ordinal !== '' && /^[0-9]+$/.test(ordinal)
  }
  const candidates = services.filter((s) => s !== '')
  for (const s of candidates) if (prefix === s) return s
  for (const s of candidates) if (indexedAs(prefix, s)) return s
  if (project !== '' && prefix.startsWith(`${project}-`)) {
    const stripped = prefix.slice(project.length + 1)
    for (const s of candidates) if (stripped === s) return s
    for (const s of candidates) if (indexedAs(stripped, s)) return s
  }
  return ''
}

/**
 * 按服务过滤聚合日志文本（纯前端过滤：会话不分服务开流，过滤不重开会话）。
 * 归属解码与三种形态的边界纪律都在 composeLogLineService（含项目名剥法）；
 * 本函数只做「逐行解码 → 命中保留」。
 *
 * 口径：选中集合为空 = 不过滤（原文返回）。选中后只保留**归属选中服务**的行；
 * 不带名字前缀的行（compose CLI 的报错/收尾句）**不保留** —— 过滤的语义是
 * 「只听选中的网元」，CLI 的行不是任何网元说的。行尾口径与拆行一致
 * （`\r\n`/裸 `\r` 都按行尾处理；流式期间最后一行可能是半行，照常参与匹配）。
 */
export function filterComposeLogLines(
  text: string,
  services: readonly string[],
  project = ''
): string {
  const picked = services.filter((s) => s !== '')
  if (picked.length === 0) return text
  const lines = splitLogLines(text)
  // 尾部换行产物的空行不算日志行（与 splitLogLines 同口径），保留中间空行的语义
  // 由「命中行的原样保留」延续 —— 未命中行整行丢弃，行间换行重排。
  return lines.filter((line) => composeLogLineService(line, picked, project) !== '').join('\n')
}
