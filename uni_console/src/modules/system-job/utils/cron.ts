/**
 * Cron 表达式前端工具：人类可读描述 + 未来执行预览 + 统一校验。
 *
 * - 后端（robfig/cron v3, WithSeconds + Descriptor）支持 5/6 段表达式、
 *   命名字段（MON/JAN）、`?` 通配与 @every/@daily 等描述符；
 * - 前端本地用 cron-parser（5/6 段数字表达式）+ cronstrue(zh_CN) 做即时反馈，
 *   `?` 在预览前规范化为 `*`，`@` 描述符与命名语法本地不解析、交由服务端权威校验。
 */
import { CronExpressionParser } from 'cron-parser'
import cronstrue from 'cronstrue/dist/cronstrue-i18n.js'

/** 常用快捷预设（统一 6 段秒级表达式，与调度器 WithSeconds 解析兼容） */
export const CRON_PRESETS: ReadonlyArray<{ label: string; value: string }> = [
  { label: '每 30 秒', value: '*/30 * * * * *' },
  { label: '每分钟', value: '0 * * * * *' },
  { label: '每 5 分钟', value: '0 */5 * * * *' },
  { label: '每 10 分钟', value: '0 */10 * * * *' },
  { label: '每小时', value: '0 0 * * * *' },
  { label: '每天 00:00', value: '0 0 0 * * *' },
  { label: '每周一 00:00', value: '0 0 0 * * 1' },
  { label: '每月 1 日 00:00', value: '0 0 0 1 * *' }
]

/** robfig 允许 dom/dow 使用 `?`，cron-parser 不支持：预览前替换为 `*` */
export function normalizeForPreview(expr: string): string {
  const fields = expr.trim().split(/\s+/)
  if (fields.length >= 5) {
    if (fields[3] === '?') fields[3] = '*'
    if (fields[4] === '?') fields[4] = '*'
  }
  return fields.join(' ')
}

export interface CronEvalResult {
  /** 本地校验是否通过（服务端仍为最终权威） */
  valid: boolean
  /** 校验失败原因 */
  message: string
  /** 人类可读描述（zh_CN），解析失败为空 */
  description: string
  /** 未来 N 次执行时间（本地时区估算），解析失败为空数组 */
  nextRuns: Date[]
}

const describeCache = new Map<string, string>()

/** 人类可读描述（带缓存；解析失败回退空串） */
export function describeCron(expr: string | undefined | null): string {
  if (!expr?.trim()) return ''
  const key = expr.trim()
  if (describeCache.has(key)) return describeCache.get(key) as string
  let desc = ''
  try {
    desc =
      cronstrue.toString(normalizeForPreview(key), {
        locale: 'zh_CN',
        throwExceptionOnParseError: true
      }) || ''
  } catch {
    desc = ''
  }
  describeCache.set(key, desc)
  return desc
}

/** 解析并评估 cron 表达式（本地能力内；`@` 描述符仅做浅校验） */
export function evaluateCron(expr: string | undefined | null, count = 5): CronEvalResult {
  const trimmed = expr?.trim() ?? ''
  if (!trimmed) {
    return { valid: false, message: '请填写执行周期', description: '', nextRuns: [] }
  }
  // `@every 5m` / `@daily` 等描述符：服务端支持的语法，本地不做段数校验
  if (trimmed.startsWith('@')) {
    return { valid: true, message: '', description: '', nextRuns: [] }
  }
  const fields = trimmed.split(/\s+/)
  if (fields.length !== 5 && fields.length !== 6) {
    return {
      valid: false,
      message: '表达式应为 5 段(分 时 日 月 周)或 6 段(秒 分 时 日 月 周)',
      description: '',
      nextRuns: []
    }
  }
  const normalized = normalizeForPreview(trimmed)
  const nextRuns: Date[] = []
  try {
    const iterator = CronExpressionParser.parse(normalized, { currentDate: new Date() })
    for (let i = 0; i < count; i++) {
      nextRuns.push(iterator.next().toDate())
    }
  } catch {
    // 命名语法（MON/JAN）等本地不解析：交由服务端校验，预览置空
  }
  return { valid: true, message: '', description: describeCron(normalized), nextRuns }
}

const pad = (n: number) => String(n).padStart(2, '0')

/** 2026-09-06 21:05:30 格式（本地时区） */
export function formatDateTime(d: Date | number): string {
  const t = d instanceof Date ? d : new Date(d)
  return `${t.getFullYear()}-${pad(t.getMonth() + 1)}-${pad(t.getDate())} ${pad(
    t.getHours()
  )}:${pad(t.getMinutes())}:${pad(t.getSeconds())}`
}

/** 09-06 21:05:30 简短格式（用于预览列表） */
export function formatShortTime(d: Date): string {
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(
    d.getMinutes()
  )}:${pad(d.getSeconds())}`
}

/** 相对时间提示："还有 2 小时 15 分" / "已过期" */
export function formatRelative(next: string | null | undefined, now = Date.now()): string {
  if (!next) return ''
  const t = new Date(next.replace(' ', 'T')).getTime()
  if (Number.isNaN(t)) return ''
  const diff = t - now
  if (diff <= 0) return '已过期，等待调度器重排'
  const mins = Math.floor(diff / 60000)
  if (mins < 1) return `还有不到 1 分钟`
  if (mins < 60) return `还有 ${mins} 分钟`
  const hours = Math.floor(mins / 60)
  const restMins = mins % 60
  if (hours < 24) return `还有 ${hours} 小时${restMins ? ` ${restMins} 分` : ''}`
  const days = Math.floor(hours / 24)
  return `还有 ${days} 天 ${hours % 24} 小时`
}
