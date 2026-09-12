/**
 * 个人中心共享格式化工具:客户端图标映射与时间展示。
 * 后端 JSONTime 序列化为 "YYYY-MM-DD HH:mm:ss"(服务器本地时区,无时区后缀)。
 */

const BROWSER_ICONS: Record<string, string> = {
  chrome: 'ri:chrome-line',
  firefox: 'ri:firefox-line',
  edge: 'ri:edge-line',
  safari: 'ri:safari-line'
}

const OS_ICONS: Record<string, string> = {
  windows: 'ri:windows-line',
  mac: 'ri:apple-line',
  linux: 'ri:linux-line',
  android: 'ri:android-line',
  ios: 'ri:smartphone-line'
}

const FALLBACK_ICON = 'ri:question-line'

export function browserIcon(name?: string): string {
  return BROWSER_ICONS[(name ?? '').toLowerCase()] ?? FALLBACK_ICON
}

export function osIcon(name?: string): string {
  return OS_ICONS[(name ?? '').toLowerCase()] ?? FALLBACK_ICON
}

/** 解析服务器时间串(按浏览器本地时区解释,与全站时间展示口径一致) */
export function parseServerTime(value?: string | null): Date | null {
  if (!value) return null
  const d = new Date(value.replace(' ', 'T'))
  return Number.isNaN(d.getTime()) ? null : d
}

/** 行内短时间:MM-DD HH:mm */
export function shortTime(value?: string | null): string {
  const d = parseServerTime(value)
  if (!d) return value ?? '—'
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

/** 日期部分:YYYY-MM-DD */
export function datePart(value?: string | null): string {
  const d = parseServerTime(value)
  if (!d) return '—'
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}

export type RelTime =
  | { kind: 'now' }
  | { kind: 'minutes'; n: number }
  | { kind: 'hours'; n: number }
  | { kind: 'days'; n: number }
  | { kind: 'date'; text: string }
  | null

/** 相对时间:<1 分钟「刚刚」,<7 天相对表述,更早回退到 MM-DD HH:mm */
export function relTime(value?: string | null): RelTime {
  const d = parseServerTime(value)
  if (!d) return null
  const diff = Date.now() - d.getTime()
  if (diff < 0) return { kind: 'date', text: shortTime(value) }
  const min = Math.floor(diff / 60_000)
  if (min < 1) return { kind: 'now' }
  if (min < 60) return { kind: 'minutes', n: min }
  const hours = Math.floor(min / 60)
  if (hours < 24) return { kind: 'hours', n: hours }
  const days = Math.floor(hours / 24)
  if (days < 7) return { kind: 'days', n: days }
  return { kind: 'date', text: shortTime(value) }
}

/** "Chrome · macOS" 客户端描述;Unknown 用 unknownText 兜底 */
export function clientText(browser?: string, os?: string, unknownText = 'Unknown'): string {
  const norm = (v?: string) => (!v || v.toLowerCase() === 'unknown' ? unknownText : v)
  const b = norm(browser)
  const o = norm(os)
  if (b === o) return b
  return `${b} · ${o}`
}
