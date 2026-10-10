/** 展示文案（纯函数）。数值一律经 formatByUnit 换算 —— 不在这里另造一套单位换算。 */
import { formatByUnit } from '@/modules/device/utils/display'
import type { DockerContainerItem, DockerImageItem, DockerPortItem } from '../api'

/** 容器状态句：直接用 Docker 的原生状态（"Up 16 hours"），它比我们编的措辞更准。 */
export function containerStateText(c: DockerContainerItem): string {
  return c.statusText || c.state
}

/** Docker 原生状态码 → 中文（`state` 字段，见 agent adapter.go:97）。 */
const STATE_ZH: Record<string, string> = {
  running: '运行中',
  created: '已创建',
  paused: '已暂停',
  restarting: '重启中',
  removing: '移除中',
  dead: '已死亡',
  exited: '已退出',
  stopping: '停止中'
}

/** 时长单位（Docker 的原生英文 → 中文）。 */
const DURATION_UNIT_ZH: Record<string, string> = {
  second: '秒',
  minute: '分钟',
  hour: '小时',
  day: '天',
  week: '周',
  month: '月',
  year: '年'
}

/** Docker 的原生短语（非「数词 + 单位」的形态）→ 中文。 */
const DURATION_PHRASE_ZH: Record<string, string> = {
  'Less than a second': '不到 1 秒',
  'About a minute': '约 1 分钟',
  'About an hour': '约 1 小时',
  'About a day': '约 1 天',
  'About a month': '约 1 个月',
  'About a year': '约 1 年'
}

/**
 * 原生状态句里的英文时长 → 中文（"26 hours ago" → "26 小时前"，"32 hours" → "32 小时"）。
 *
 * 只处理 Docker 会产出的有限几档（秒/分/时/天/周/月/年 + About/Less 短语）；认不出的形态
 * 原样保留 —— 宁可在状态列里留一句英文，也不要编一个假的中文时长。
 */
function zhDuration(raw: string): string {
  const trimmed = raw.trim()
  const ago = /\bago$/.test(trimmed)
  const core = trimmed.replace(/\s+ago$/, '').trim()
  let out: string
  const phrase = DURATION_PHRASE_ZH[core]
  if (phrase) {
    out = phrase
  } else {
    const m = core.match(/^(\d+)\s+(second|minute|hour|day|week|month|year)s?$/)
    out = m ? `${m[1]} ${DURATION_UNIT_ZH[m[2]!]}` : core
  }
  return ago ? `${out}前` : out
}

/**
 * health 只在 `statusText` 的括号里（"Up 2 hours (healthy)"）—— 从串里解析。
 *
 * 优先匹配 `health: starting`：它的字面里含 "health"，排在 healthy 之前才不会误判。
 * 识别不到就返回空串（没配 healthcheck 的容器没有这一段）。
 */
function zhHealth(statusText: string): string {
  if (/health:\s*starting/i.test(statusText)) return '健康检查中'
  if (/\(unhealthy\)/i.test(statusText)) return '异常'
  if (/\(healthy\)/i.test(statusText)) return '健康'
  return ''
}

/**
 * 容器状态的中文句（列表「状态」列与镜像详情的关联容器表用它）。
 *
 * 与 `containerStateText` 的区别：这里**本地化** —— 原始状态码映成中文，原生状态句里的
 * 英文时长与 health 解析成中文补充。`running` → 「运行中 32 小时（健康）」；
 * `exited` → 「已退出（退出码 143）26 小时前」。认不出的状态码原样返回（不编措辞）。
 *
 * 本地化只动措辞，不改判据：退出码/health 都从 Docker 给的原生字段里取，没有就不写。
 */
export function containerStateZh(c: DockerContainerItem): string {
  const state = c.state
  const base = STATE_ZH[state] ?? (state ? state : '—')
  const status = c.statusText ?? ''
  if (state === 'running') {
    const up = status.match(/^Up\s+(.+?)(?:\s*\(|$)/)
    const head = up ? `${base} ${zhDuration(up[1]!)}` : base
    const health = zhHealth(status)
    return health ? `${head}（${health}）` : head
  }
  if (state === 'exited' || state === 'dead') {
    // "Exited (143) 26 hours ago"：退出码与相对时长都在括号之后。
    const m = status.match(/\((\d+)\)\s*(.*?)\s*ago\b/)
    if (!m) return base
    const dur = m[2] ? zhDuration(`${m[2]} ago`) : ''
    return `${base}（退出码 ${m[1]}）${dur}`
  }
  return base
}

/** 内存用量分解：运行中才有值（未运行返回 null —— 缺值列渲染「—」，不画空条）。 */
export interface MemUsage {
  usedMb: number
  limitMb: number
  /** 已用 / 上限百分比（分母缺省时为 0；meter 的填充宽度）。 */
  pct: number
}

/**
 * 运行中容器的内存分解（meter 的输入）：未运行时返回 null。
 *
 * CPU/内存都是「运行中才有意义」的指标 —— 停止的容器没有实时用量，0 会被读成
 * 「真的很闲」。缺值一律走 null，由列渲染「—」。
 */
export function memUsage(c: DockerContainerItem): MemUsage | null {
  if (c.state !== 'running') return null
  const usedMb = c.memUsageMb
  const limitMb = c.memLimitMb
  const pct = limitMb > 0 ? Math.round((usedMb / limitMb) * 100) : 0
  return { usedMb, limitMb, pct }
}

/**
 * 内存文案：「已用 / 上限」（**单位 MB 进列头**，正文只留数值）；未运行「—」。
 * 缺分母（老 agent / limit 为 0）时只给已用，不编一个假分母。
 */
export function memText(c: DockerContainerItem): string {
  const u = memUsage(c)
  if (!u) return '—'
  return u.limitMb ? `${u.usedMb} / ${u.limitMb}` : `${u.usedMb}`
}

/** CPU 文案：未运行时「—」；单位（%）进列头，正文只留数值。 */
export function cpuText(c: DockerContainerItem): string {
  if (c.state !== 'running') return '—'
  return c.cpuPercent.toFixed(1)
}

/**
 * 网络文案：上/下行速率。`formatByUnit('B/s', …)` 的产物**自带速率单位**
 * （「10 B/s」「743.4 KB/s」），不再追加后缀 —— 旧实现补了第二个「/s」，
 * 列表上就是「10 B/s/s」这个错字面（投诉截图里可见）。
 */
export function netText(c: DockerContainerItem): string {
  if (c.state !== 'running') return '—'
  return `↑${formatByUnit('B/s', c.netTxBytesSec)} ↓${formatByUnit('B/s', c.netRxBytesSec)}`
}

/**
 * 端口映射（**单行折叠**）：「20080 → 8088/tcp」；多映射只留首条并标出余量
 * （「… +2」）—— 行不再竖向堆高（列表列宽有限，第一段映射已是连通性排查的
 * 第一线索）。无映射「—」。
 */
export function portsText(ports: DockerPortItem[] | undefined): string {
  if (!ports || ports.length === 0) return '—'
  const first = ports[0]!
  const text = first.publicPort
    ? `${first.publicPort} → ${first.privatePort}/${first.type ?? 'tcp'}`
    : `${first.privatePort}/${first.type ?? 'tcp'}`
  return ports.length > 1 ? `${text} +${ports.length - 1}` : text
}

/**
 * 中段省略（Wave 3a）：超长时保留**头 + 尾**、切掉中间 —— 保住信息量最大的两段。
 *
 * 为什么不用 CSS 的尾部省略（`text-overflow: ellipsis`，即 showOverflowTooltip）：
 * 被切掉的恰恰是唯一有信息的那段 —— 镜像 `quay.io/minio/minio:R…` 切掉 tag、
 * 端口 `20080 → 6379/tc…` 切进 `/tcp` 中间、内存 `1.63 GB / 15.51 GB…` 切掉分母。
 * 这里按预算在中间打省略号，头尾都留住。
 *
 * head/tail 是**字符数**预算（不是像素）：`text.length <= head + tail` 时原样返回。
 */
export function midEllipsis(text: string, head: number, tail: number): string {
  if (text.length <= head + tail) return text
  return `${text.slice(0, head)}…${text.slice(text.length - tail)}`
}

/**
 * 中段省略字符预算 —— **按真机列宽实测校准**（Wave 3c；3a 的 26/16、12/8 是估的，
 * 远超列宽，中段省略后的串仍溢出、被 CSS 省略号二次截断，尾部的 tag/`/tcp` 反被切掉）。
 *
 * ── 校准依据（1920 视口真机 measureText，表体 14px）──────────────────────
 * 表体实际字号 **14px**（任务书写的 13px 偏小，实测 14px 更保守，按 14px 定预算）。
 * EP .cell 左右各 12px padding ⇒ 可用文本宽 = 列 minWidth − 24：
 *   · 镜像列 minWidth 180 → 可用 156px；sha256/仓库引用按 ~8.5px/字符（数字位实测 8.4）
 *     ⇒ 约 18–19 字符放得下。取 HEAD 10 + TAIL 8（含省略号 19 字符）实测最坏串
 *     「sha256:cf7…8c573f0f」≈142px < 156（余 ~14px）。
 *   · 端口列 minWidth 160 → 可用 136px；等宽 _wkl-mono（数字 7px/字符、`→` 双宽）⇒
 *     取 HEAD 8 + TAIL 7（含省略号 16 字符）实测「5432 → 5…/tcp +1」≈126px < 136
 *     （余 ~10px）。TAIL 7 = `/tcp +N` 整段（多映射的余量计数不被切）。
 *
 * 这两个预算让中段省略后的串**自己就能放进列宽**：可见那截保住仓库前缀/:tag 与
 * `/tcp +N`，不必靠 CSS 省略号兜底（兜底只会切掉尾部那截有用信息）。若某天列宽或
 * 字号变化，守住 display.test.ts 里对预算上界的断言 + w3c-nowrap 的行高复核。
 */
export const IMAGE_MID_HEAD = 10
export const IMAGE_MID_TAIL = 8
/** 端口映射的中段省略预算：头留住宿主端口、尾留住 `/tcp +N`。 */
export const PORT_MID_HEAD = 8
export const PORT_MID_TAIL = 7

/** 镜像展示名：第一个仓库标签，无标签时用短 id（**页面不用「<none>」这种原始记号**）。 */
export function imageRefText(image: Pick<DockerImageItem, 'id' | 'repoTags'>): string {
  const tag = image.repoTags?.[0]
  if (tag) return tag
  return `无标签（${(image.id ?? '').replace('sha256:', '').slice(0, 12)}）`
}

/** 分层合计（MB）：**空元数据层不计入**（它们不占空间）。 */
export function layersTotalMB(
  history: { sizeBytes?: number; createdBy?: string; emptyLayer?: boolean }[] | undefined
): number {
  if (!history || history.length === 0) return 0
  return (
    history.reduce((sum, l) => sum + (l.emptyLayer ? 0 : (l.sizeBytes ?? 0)), 0) / (1024 * 1024)
  )
}

/**
 * 「使用」列：悬空 → 「可回收」，未被任何容器使用 → 「未使用」，在用则带上容器名。
 *
 * 悬空镜像**必然**无容器引用，故它先于 inUse 判断：把悬空说成「未使用」会丢掉
 * 唯一可安全回收的那条线索（回收的目标就是它）。
 */
export function inUseText(image: DockerImageItem): string {
  if (image.dangling) return '可回收（无标签）'
  if (!image.inUse) return '未使用'
  const names = image.inUseBy ?? []
  return names.length ? `在用（${names.join('、')}）` : '在用'
}

/**
 * 相对时间（「3 月前」）：只在列表里用，精确时刻在详情页给。
 *
 * 四档跨度刻意粗（分钟/小时/天/月）：镜像的创建时间是静态的，「3 月前」已够判断
 * 新旧，逐档细化只会占满一列。
 *
 * ── 异常时间的兜底（不再渲染成巨大月数）─────────────────────────────────
 * 上游某些层的时间字段是 **Go 零时刻的 Unix 哨兵 `-62135596800`**（0001-01-01），
 * 反算 diff 会得到约 24663 个月 —— 页面上就是「24663 月前」这个荒唐字面。判据：
 *   ① `!Number.isFinite` 或 `<= 0`：非正时间戳（含哨兵）不是「某时刻」，是坏值；
 *   ② `> now`（未来）：主机与浏览器的时钟偏差，或又一个坏值 —— 相对「多久以前」
 *      的语义在它上面无意义。
 * 两者都返回**「未知」**：它比「—」更准 —— 「—」在本模块表示「字段根本没有」，
 * 而这里字段是有值的，只是不可用；两者混为一谈会丢掉「上游给了脏数据」这条线索。
 */
export function formatRelativeTime(unixSec: number, now = Date.now() / 1000): string {
  if (!Number.isFinite(unixSec) || unixSec <= 0 || unixSec > now) return '未知'
  const diff = now - unixSec
  if (diff < 3600) return `${Math.max(1, Math.floor(diff / 60))} 分钟前`
  if (diff < 86400) return `${Math.floor(diff / 3600)} 小时前`
  if (diff < 86400 * 30) return `${Math.floor(diff / 86400)} 天前`
  return `${Math.floor(diff / (86400 * 30))} 月前`
}
