/**
 * P3·安全面（image:scan）报告载荷的**本地类型镜像**与解析（纯函数）。
 *
 * ── 为什么是本地 interface 而不是生成类型 ─────────────────────────────
 * 报告走 result.payload 通道，而生成类型对它只声明 `unknown`（cmd result 的
 * any 通道 —— 与 image:inspect 的先例同一条路，见 utils/cmd.ts 的
 * RawImageInspect）。形状的事实源是 `uni_protocol/docker.go` 的 `DockerScanReport`
 * （含 json tag：image_id / scanned_at / counts / vulns / truncated /
 * fixed_version / title）：core 对结果载荷**原样透传**，协议 json tag 是
 * snake_case，故这里的线上形状用 snake_case 镜像、视图用 camelCase（转换只做
 * 一次，照 cmd.ts 的既有模式）。它同时是 core 侧扫描缓存（docker:scan:<镜像ID>，
 * 24h）的存储形态 —— 缓存命中回放的字节与 agent 直发的字节只有一个字段之差
 * （scanned_at 由 core 重盖），前端一份解析两条路径通吃。
 *
 * ── 口径从协议注释里搬过来的三条 ────────────────────────────────────
 *   - **计数是全量**：severity 计数对 trivy 全部去重条目如实求和，不受 Vulns
 *     500 条截断影响 ——「共 3200 条、其中 critical M 条」是行动依据，截断后的
 *     数字会把大镜像谎报成干净镜像；
 *   - **条目上限 500**（协议 MaxDockerScanVulnEntries，导出常量两端同一个数）：
 *     result.payload 有 256KB 硬上限，不截断会「扫描成功却报不出来」；超限
 *     truncated=true，条目按 severity 降序、同档按 id；
 *   - **severity 白名单外的值折到 unknown**（协议归一化的同一条纪律：空串/
 *     拼写漂移/新档都折「未知」—— 丢弃会让计数与条目对不上）。
 */

/**
 * 扫描报告里 CVE 条目的上限（**镜像协议常量** MaxDockerScanVulnEntries = 500：
 * 协议定义尺、agent 执法 —— 前端只在截断提示里说这个数，条数守卫在后端）。
 */
export const MAX_SCAN_VULN_ENTRIES = 500

/** severity 五档（trivy 的 CRITICAL/HIGH/MEDIUM/LOW/UNKNOWN 折成小写）。 */
export type ScanSeverity = 'critical' | 'high' | 'medium' | 'low' | 'unknown'

/** 展示顺序（severity 降序）与中文档名 —— 表头计数行与表格徽标共用一份事实。 */
export const SCAN_SEVERITIES: readonly { key: ScanSeverity; label: string }[] = [
  { key: 'critical', label: '严重' },
  { key: 'high', label: '高危' },
  { key: 'medium', label: '中危' },
  { key: 'low', label: '低危' },
  { key: 'unknown', label: '未知' }
]

/** severity 的展示档名（白名单外折「未知」，与协议归一化同向）。 */
export function scanSeverityLabel(s: string): string {
  return SCAN_SEVERITIES.find((x) => x.key === s)?.label ?? '未知'
}

/** severity 的着色类名（五档色点/chip 共用；颜色本体在组件样式里取 EP 语义色）。 */
export function scanSeverityClass(s: string): string {
  const ok = SCAN_SEVERITIES.some((x) => x.key === s)
  return ok ? `is-${s}` : 'is-unknown'
}

/** 一条 CVE（视图形态；线上 snake_case 的转换见 parseDockerScanReport）。 */
export interface DockerScanVulnView {
  id: string
  pkg: string
  severity: ScanSeverity
  /** 修复版本（**有修复才有**；空 = 修复未发布/不适用 —— 缺席本身是信息）。 */
  fixedVersion?: string
  /** 一句话摘要（协议截断到 256B）。 */
  title?: string
}

/** severity 全量计数（不受 500 条截断影响，见文件头口径）。 */
export interface DockerScanCountsView {
  critical: number
  high: number
  medium: number
  low: number
  unknown: number
}

export interface DockerScanReportView {
  /** 被扫镜像的完整 ID（内容寻址键 —— tag 换了内容不变仍命中同一份缓存）。 */
  imageId: string
  /** 扫描完成时刻（unix 秒；缓存回放路径上由 core 重盖）。 */
  scannedAt: number
  counts: DockerScanCountsView
  vulns: DockerScanVulnView[]
  /** 条目因 500 上限被截断（计数仍为全量）。 */
  truncated: boolean
}

/** 漏洞总数（severity 全量计数求和 —— 不是 vulns.length，截断时两者不同）。 */
export function scanTotalCount(c: DockerScanCountsView): number {
  return c.critical + c.high + c.medium + c.low + c.unknown
}

// ── 线上原始形状（snake_case，键名照抄协议 json tag；理由见文件头） ──

interface RawDockerScanVuln {
  id?: string
  pkg?: string
  severity?: string
  fixed_version?: string
  title?: string
}

interface RawDockerScanCounts {
  critical?: number
  high?: number
  medium?: number
  low?: number
  unknown?: number
}

interface RawDockerScanReport {
  image_id?: string
  scanned_at?: number
  counts?: RawDockerScanCounts
  vulns?: RawDockerScanVuln[]
  truncated?: boolean
}

/** severity 归一化：白名单五档之外（含空串/类型错）一律折 unknown。 */
function normalizeSeverity(s: unknown): ScanSeverity {
  const v = typeof s === 'string' ? s : ''
  return SCAN_SEVERITIES.some((x) => x.key === v) ? (v as ScanSeverity) : 'unknown'
}

/** 计数归一化：非有限数当 0（计数是行动依据，坏一档不给 NaN）。 */
function countOf(v: unknown): number {
  return typeof v === 'number' && Number.isFinite(v) && v >= 0 ? Math.floor(v) : 0
}

/**
 * 解析扫描报告载荷。
 *
 * 形状不符时返回 null 而不是抛错（与 parseLogsPayload 同一条纪律：载荷来自
 * agent，页面不该因为一次形状意外白屏 —— 调用方给「扫描完成但报告未能读取」
 * 的可恢复结论）。**关键字段（counts/scanned_at）缺失即视为不认识**：缓存回放
 * 与 agent 直发两条路径都恒有它们，缺了说明不是同一份契约，宁可不说也不瞎猜。
 */
export function parseDockerScanReport(payload: unknown): DockerScanReportView | null {
  const p = (payload ?? null) as RawDockerScanReport | null
  if (!p || typeof p !== 'object') return null
  const raw = p.counts
  if (!raw || typeof raw !== 'object') return null
  const scannedAt =
    typeof p.scanned_at === 'number' && Number.isFinite(p.scanned_at) ? p.scanned_at : 0
  if (scannedAt <= 0) return null
  return {
    imageId: typeof p.image_id === 'string' ? p.image_id : '',
    scannedAt,
    counts: {
      critical: countOf(raw.critical),
      high: countOf(raw.high),
      medium: countOf(raw.medium),
      low: countOf(raw.low),
      unknown: countOf(raw.unknown)
    },
    vulns: Array.isArray(p.vulns)
      ? p.vulns.map((v) => ({
          id: typeof v?.id === 'string' ? v.id : '',
          pkg: typeof v?.pkg === 'string' ? v.pkg : '',
          severity: normalizeSeverity(v?.severity),
          fixedVersion: typeof v?.fixed_version === 'string' ? v.fixed_version : '',
          title: typeof v?.title === 'string' ? v.title : ''
        }))
      : [],
    truncated: p.truncated === true
  }
}
