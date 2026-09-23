/**
 * 设备详情页的**展示层纯函数**（无 DOM、可单测）。
 *
 * 与 `./metrics` 的分工：
 *   - `./metrics` 是**趋势图**的纯逻辑（稀疏桶、available_metrics 两态、档位约束），
 *     那里是被 21 条单测钉住的核心契约，本文件**不复用也不改动**它；
 *   - 本文件是**详情页外壳**的展示逻辑（时间文案、数据新鲜度、设备信息分组、
 *     异常分类），纯粹为「把后端字段翻译成人话」。
 *
 * 三条贯穿全文件的纪律：
 *  1. **缺值一律「—」**，绝不显示 0/空串：0% 与「没采到」在容量判断上完全相反。
 *  2. **相对时间由前端算**，绝对时刻由后端给（unix 秒）。相对时间随页面存活
 *     而推进，故只用于「刚刚/N 分钟前」这类粗粒度表达。
 *  3. **不臆造阈值**：判「数据过期」用的阈值必须由调用方传入（详情页传后端的
 *     `offlineThresholdSec`），本文件不内置 30/60 之类的魔法数。
 */

/** 缺值占位符（全站统一）。 */
export const EMPTY_TEXT = '—'

/** 保留 n 位小数（去掉尾随 0，让整数不显示成 12.00）。 */
export function roundTo(v: number, digits: number): number {
  const f = 10 ** digits
  return Math.round(v * f) / f
}

/**
 * 秒 → 相对时间文案（「刚刚」「12 分钟前」…）。
 *
 * @param atSec    目标时刻（unix 秒）
 * @param nowMs    当前时刻（毫秒，便于单测注入；默认 Date.now()）
 *
 * 未来时刻（时钟偏差）返回「刚刚」而不是「-3 分钟前」：服务端与浏览器时钟
 * 不可能完全一致，出现负数时把它显示成负数只会让人怀疑数据。
 */
export function formatRelative(atSec?: number | null, nowMs: number = Date.now()): string {
  if (typeof atSec !== 'number' || !Number.isFinite(atSec)) return EMPTY_TEXT
  const diffSec = Math.floor(nowMs / 1000) - atSec
  if (diffSec < 0) return '刚刚'
  if (diffSec < 10) return '刚刚'
  if (diffSec < 60) return `${diffSec} 秒前`
  const min = Math.floor(diffSec / 60)
  if (min < 60) return `${min} 分钟前`
  const hour = Math.floor(min / 60)
  if (hour < 24) return `${hour} 小时前`
  const day = Math.floor(hour / 24)
  if (day < 30) return `${day} 天前`
  const month = Math.floor(day / 30)
  if (month < 12) return `${month} 个月前`
  return `${Math.floor(day / 365)} 年前`
}

/** unix 秒 → `yyyy-MM-dd HH:mm:ss`；缺值「—」。 */
export function formatUnixSeconds(v?: number | null): string {
  if (v === undefined || v === null || !Number.isFinite(v)) return EMPTY_TEXT
  const d = new Date(v * 1000)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(
    d.getMinutes()
  )}:${p(d.getSeconds())}`
}

/** 字节/秒 → 可读速率（B/s、KB/s、MB/s、GB/s）。 */
export function formatBytesPerSec(v?: number | null): string {
  if (typeof v !== 'number' || !Number.isFinite(v)) return EMPTY_TEXT
  const abs = Math.abs(v)
  if (abs < 1024) return `${v.toFixed(0)} B/s`
  if (abs < 1024 * 1024) return `${(v / 1024).toFixed(1)} KB/s`
  if (abs < 1024 * 1024 * 1024) return `${(v / 1024 / 1024).toFixed(1)} MB/s`
  return `${(v / 1024 / 1024 / 1024).toFixed(2)} GB/s`
}

/**
 * MB → 可读容量（>1024MB 转 GB）。
 *
 * 与列表页/详情页既有口径一致（详情页原来的 formatMb 已搬到这里统一）。
 */
export function formatMb(mb?: number | null): string {
  if (typeof mb !== 'number' || !Number.isFinite(mb) || mb <= 0) return EMPTY_TEXT
  return mb >= 1024 ? `${(mb / 1024).toFixed(2)} GB` : `${mb.toFixed(1)} MB`
}

/**
 * 实测容量（MB）→ 可读容量（≥1024MB 转 GB、≥1024GB 转 TB）。
 *
 * 与 `formatMb` 的区别只有一条，但很关键：**0 是合法值**。
 * `formatMb` 服务的是「设备规格里的内存总量」（0 表示未知，显示「0.0 MB」会误导）；
 * 本函数的入参是实测读数（`mem_used_mb` 可以是 0），把 0 显示成「—」会与「没采到」
 * 混淆 —— 这正是本项目反复强调的一条纪律。
 */
export function formatCapacityMb(mb?: number | null): string {
  if (typeof mb !== 'number' || !Number.isFinite(mb)) return EMPTY_TEXT
  const abs = Math.abs(mb)
  if (abs >= 1024 * 1024) return `${roundTo(mb / 1024 / 1024, 2)} TB`
  if (abs >= 1024) return `${roundTo(mb / 1024, 2)} GB`
  return `${roundTo(mb, 1)} MB`
}

/** 实测容量（GB）→ 可读容量（≥1024GB 转 TB）。进位规则与 formatCapacityMb 一致。 */
export function formatGb(gb?: number | null): string {
  if (typeof gb !== 'number' || !Number.isFinite(gb)) return EMPTY_TEXT
  return Math.abs(gb) >= 1024 ? `${roundTo(gb / 1024, 2)} TB` : `${roundTo(gb, 2)} GB`
}

/**
 * 时长（秒）→「N 天 N 时」/「N 时 N 分」/「N 分 N 秒」/「N 秒」。
 *
 * 与 `formatUptime` 的分工：那个吃的是**开机时刻**（自己算与当前的差值），
 * 这个吃的是**时长本身**（`uptime_sec` 列等）。文案结构两者一致
 * （formatUptime 已改为调用本函数，避免两处各写一套进位规则）。
 */
export function formatDurationSec(sec?: number | null): string {
  if (typeof sec !== 'number' || !Number.isFinite(sec) || sec < 0) return EMPTY_TEXT
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  if (d > 0) return `${d} 天 ${h} 时`
  if (h > 0) return `${h} 时 ${m} 分`
  if (m > 0) return `${m} 分`
  return `${Math.floor(sec)} 秒`
}

/**
 * 计数类速率（次/s、包/s）→ 大数进位（≥1e4 → K、≥1e6 → M）。
 *
 * 这些量纲的跨度很大（网卡包速率可以从几十到几十万），原样打印会出现
 * 「1234567 包/s」这种读不出量级的长数字；而它们没有字节那样公认的单位阶梯，
 * 故只在量级确实变大时才进位。
 */
export function formatScaledCount(v?: number | null, suffix = ''): string {
  if (typeof v !== 'number' || !Number.isFinite(v)) return EMPTY_TEXT
  const abs = Math.abs(v)
  const tail = suffix ? ` ${suffix}` : ''
  if (abs >= 1e6) return `${roundTo(v / 1e6, 2)}M${tail}`
  if (abs >= 1e4) return `${roundTo(v / 1e3, 1)}K${tail}`
  return `${roundTo(v, 2)}${tail}`
}

/**
 * 量纲 → 展示文案：**全站唯一的一处单位换算**。
 *
 * 为什么必须收敛成一份：此前有三套实现（本文件的具名格式化函数、
 * `overview.formatMetric`、详情面板里的 `toFixed`），同一条指标在不同界面上的
 * 精度与单位都不一样 —— 实测「内存已用（MB）: 7523.4」与「7.34 GB」并存，
 * 网卡速率在详情 tooltip 上是 `761286.09`、在总览页却是 `743.4 KB/s`。
 *
 * 规则：**只在数字大到难读时才进位**（MB→GB→TB、B/s→KB/s→MB/s→GB/s…），
 * 且**不向下换算** —— 不把 0.4 GB 显示成 409.6 MB，容量类读数需要保住量级感。
 * 缺值一律 `—`；`0` 是合法值，走正常格式化（0 与缺值必须可区分）。
 */
export function formatByUnit(unit: string, value: number | null | undefined): string {
  if (typeof value !== 'number' || !Number.isFinite(value)) return EMPTY_TEXT
  switch (unit) {
    case '%':
      return formatPercent(value)
    case 'B/s':
      return formatBytesPerSec(value)
    case 'MB':
      return formatCapacityMb(value)
    case 'GB':
      return formatGb(value)
    case 's':
      return formatDurationSec(value)
    case '°C':
      return `${roundTo(value, 1)} °C`
    case '次/s':
    case '包/s':
      return formatScaledCount(value, unit)
    default:
      return String(roundTo(value, 2))
  }
}

/**
 * 轴刻度文案：与 `formatByUnit` 同口径，但去掉无意义的尾随零（`20.0%` → `20%`）。
 *
 * 刻度是**密排**的（一根轴上 5~8 个），每一位小数都会重复出现，故百分比这类
 * 本来就短的量纲在刻度上取整；其余量纲沿用值文案（它们自带单位，不用再写轴名）。
 */
export function formatAxisTickByUnit(unit: string, v: number): string {
  if (!Number.isFinite(v)) return EMPTY_TEXT
  if (unit === '%') return `${roundTo(v, 1)}%`
  return formatByUnit(unit, v)
}

/** 开机时长文案（unix 秒 → 天/时/分）。进位规则复用 formatDurationSec。 */
export function formatUptime(bootTime?: number | null, nowMs: number = Date.now()): string {
  if (typeof bootTime !== 'number' || !Number.isFinite(bootTime) || bootTime <= 0) return EMPTY_TEXT
  // 未来时刻（时钟偏差）不显示负数时长。
  const seconds = Math.floor(nowMs / 1000) - bootTime
  if (seconds < 0) return EMPTY_TEXT
  return formatDurationSec(seconds)
}

/**
 * 水位数据是否「可能已过期」。
 *
 * 判据：`now - watermarkAt > staleAfterSec`。
 *
 * **阈值必须由调用方传入**（详情页传后端下发的 `offlineThresholdSec`）：
 * 本文件不内置 30 秒这类魔法数 —— 离线阈值是服务端可热更配置，前端写死会在
 * 运维改配置后与后端判定**静默矛盾**（UI 说数据已过期，后端仍判在线）。
 *
 * watermarkAt 缺值（从未上报过水位）返回 false：那是「无数据」而不是「过期」，
 * 两者文案不同（见 watermarkState）。
 */
export function isWatermarkStale(
  watermarkAt: number | null | undefined,
  staleAfterSec: number,
  nowMs: number = Date.now()
): boolean {
  if (typeof watermarkAt !== 'number' || !Number.isFinite(watermarkAt)) return false
  if (!Number.isFinite(staleAfterSec) || staleAfterSec <= 0) return false
  return Math.floor(nowMs / 1000) - watermarkAt > staleAfterSec
}

/** 水位三态的判定结果（**「无数据」与「过期」必须可区分**）。 */
export type WatermarkState = 'ok' | 'stale' | 'empty'

/**
 * 水位整体状态。
 *
 * 三态而不是布尔：
 *   - `empty`：三个百分比全缺 → 「该设备尚未上报水位」。这不是故障，
 *     新接入还没上报的设备就是这种状态，文案不能写成「数据过期」。
 *   - `stale`：有值但采样时刻过旧 → 「数据可能已过期」+ 建议排查离线。
 *   - `ok`：有值且新鲜。
 * 把 empty 与 stale 合成一种，会让「刚接入的设备」被误报成「掉线了」——
 * 这是最容易让人白跑一趟的误报。
 */
export function watermarkState(
  percents: ReadonlyArray<number | null | undefined>,
  watermarkAt: number | null | undefined,
  staleAfterSec: number,
  nowMs: number = Date.now()
): WatermarkState {
  const hasAny = percents.some((p) => typeof p === 'number' && Number.isFinite(p))
  if (!hasAny) return 'empty'
  return isWatermarkStale(watermarkAt, staleAfterSec, nowMs) ? 'stale' : 'ok'
}

/** 百分比展示：缺值「—」（**不是** 0%）。 */
export function formatPercent(v: number | null | undefined): string {
  if (typeof v !== 'number' || !Number.isFinite(v)) return EMPTY_TEXT
  return `${v.toFixed(1)}%`
}

/** ElProgress 需要数字：缺值退化成 0（进度条只表达「有多满」，文案另有「—」）。 */
export function clampPercent(v: number | null | undefined): number {
  if (typeof v !== 'number' || !Number.isFinite(v)) return 0
  return Math.min(Math.max(v, 0), 100)
}

/** 水位阈值配色：<50 绿 / <80 蓝 / <90 琥珀 / ≥90 红（与 monitor 页同口径）。 */
export function usageTone(v: number | null | undefined): string {
  if (typeof v !== 'number' || !Number.isFinite(v)) return 'var(--el-text-color-placeholder)'
  if (v >= 90) return 'var(--el-color-danger)'
  if (v >= 80) return 'var(--el-color-warning)'
  if (v >= 50) return 'var(--el-color-primary)'
  return 'var(--el-color-success)'
}

/** 是否处于「需要显眼提示」的高水位（进度条右上角脉冲点用）。 */
export function isCriticalUsage(v: number | null | undefined): boolean {
  return typeof v === 'number' && Number.isFinite(v) && v >= 90
}

/**
 * 按 os / platform 推断设备图标（纯展示，前端映射）。
 *
 * 未识别时回退通用主机图标 —— 不返回空串，否则页头会出现一个空位。
 */
export function deviceIcon(os?: string | null, platform?: string | null): string {
  const key = `${os ?? ''} ${platform ?? ''}`.toLowerCase()
  if (key.includes('ubuntu')) return 'ri:ubuntu-fill'
  if (key.includes('debian')) return 'ri:ubuntu-fill'
  if (key.includes('centos') || key.includes('redhat') || key.includes('rhel'))
    return 'ri:centos-fill'
  if (key.includes('darwin') || key.includes('mac')) return 'ri:mac-fill'
  if (key.includes('windows') || key.includes('win')) return 'ri:windows-fill'
  if (key.includes('linux')) return 'ri:ubuntu-fill'
  return 'ri:computer-line'
}
