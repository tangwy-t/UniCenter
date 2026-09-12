/**
 * pprof 视图的纯格式化 / 采样类型元数据。
 *
 * 从 views/pprof.vue(1920+ 行)抽出:这些函数不依赖任何响应式状态,
 * 抽出后可脱离组件单测。视图侧只保留状态、缩放与编排逻辑。
 */

/** 采样类型 → 图标(与 PROFILE_COLORS 同键)。 */
const PROFILE_ICONS: Record<string, string> = {
  goroutine: 'ri:git-branch-line',
  heap: 'ri:database-2-line',
  allocs: 'ri:cpu-line',
  block: 'ri:time-line',
  mutex: 'ri:lock-line',
  threadcreate: 'ri:code-s-slash-line',
  profile: 'ri:scan-2-line',
  trace: 'ri:pulse-line'
}

/** 采样类型 → 主色。 */
const PROFILE_COLORS: Record<string, string> = {
  goroutine: '#10b981',
  heap: '#3b82f6',
  allocs: '#06b6d4',
  block: '#f59e0b',
  mutex: '#ec4899',
  threadcreate: '#7c3aed',
  profile: '#f97316',
  trace: '#14b8a6'
}

/**
 * 采样值(字节或计数)的紧凑展示:按 1G/1M/1K 逐级降档。
 * 注意这里用的是 1024 进制,且大写后缀不带 B(与火焰图 tooltip 一致)。
 */
export function fmtValue(v: number): string {
  if (!Number.isFinite(v)) return '-'
  if (v >= 1024 * 1024 * 1024) return `${(v / (1024 * 1024 * 1024)).toFixed(2)}G`
  if (v >= 1024 * 1024) return `${(v / (1024 * 1024)).toFixed(2)}M`
  if (v >= 1024) return `${(v / 1024).toFixed(1)}K`
  return String(v)
}

/**
 * 秒 → 时钟样式:mm:ss,超过 1 小时补小时位(h:mm:ss)。
 * 负数按 0 处理(采样时钟不应为负,但避免出现 -1:-1)。
 */
export function fmtClock(sec: number): string {
  const s = Math.max(0, Math.floor(sec))
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const ss = s % 60
  const mm = String(m).padStart(2, '0')
  const sss = String(ss).padStart(2, '0')
  return h > 0 ? `${h}:${mm}:${sss}` : `${mm}:${sss}`
}

/**
 * 秒 → 中文时长(秒 / 分钟 / 小时)。
 * 非有限数或 <=0 返回破折号(表示"无采样"而非 0 秒)。
 */
export function fmtDur(sec: number): string {
  if (!Number.isFinite(sec) || sec <= 0) return '—'
  if (sec < 60) return `${sec} 秒`
  if (sec < 3600) return `${Math.round(sec / 60)} 分钟`
  return `${(sec / 3600).toFixed(1)} 小时`
}

/** 本地时间(HH:mm:ss,24 小时制)。 */
export function fmtTime(d: Date): string {
  return d.toLocaleTimeString('zh-CN', { hour12: false })
}

/**
 * 生成下载文件名用的时间戳:YYYYMMDD_HHmmss。
 * @param now 注入当前时间(便于测试);省略时取系统时间。
 */
export function stamp(now: Date = new Date()): string {
  const d = now
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}_${p(d.getHours())}${p(d.getMinutes())}${p(d.getSeconds())}`
}

/** 采样类型的图标与主色;未知类型回退到中性值。 */
export function profileMetaOf(name: string): { icon: string; color: string } {
  return {
    icon: PROFILE_ICONS[name] ?? 'ri:radar-line',
    color: PROFILE_COLORS[name] ?? '#64748b'
  }
}

/** 采样类型图标(仅取图标部分)。 */
export function profileIcon(name: string): string {
  return profileMetaOf(name).icon
}
