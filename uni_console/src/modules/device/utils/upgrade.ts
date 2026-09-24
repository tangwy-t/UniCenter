/**
 * device · Agent 升级域的**展示层纯函数**
 *
 * 三条纪律（与后端契约一一对应，改这里之前先读后端 DTO）：
 *  1. **机器码 → 结论**：后端下发的是状态/原因码，页面只讲结论（原因码原文
 *     绝不出现）。翻译表收齐 12 个键：协议白名单 9 个 + 服务端推导 3 个。
 *  2. **不编造**：未知码返回空串而不是「未知错误」—— 空串会被页面渲染成「—」，
 *     而「未知错误」看起来像一条真的结论（排查时会把注意力引向错误的方向）。
 *  3. **进度三层**：任务级用台数比例、下载阶段用真实字节百分比、其余阶段用
 *     阶段轨 —— **不造连续假百分比**（校验/替换/重启没有天然刻度）。
 *
 * 这一层不做任何请求、不持有状态：全部可被单测直接断言（本模块的测试环境是
 * node，无法挂载组件，故展示逻辑一律下沉到这类纯函数）。
 */

/** 升级相位（后端 `DeviceListItem.upgradePhase` 的取值）。 */
export type UpgradePhase = 'pending' | 'running' | 'achieved' | ''

/** 升级终态（设备行 `upgradeResult`，对应 `entity.DeviceUpgrade*`）。 */
export const UPGRADE_RESULT = {
  none: 0,
  achieved: 1,
  failed: 2,
  rolledBack: 3
} as const

/** 阶段轨的四个节点（顺序 = 流水线顺序）。 */
export type StageKey = 'download' | 'verify' | 'install' | 'restart'

/** 阶段轨的中文名（也是「第 N/4 步」读屏文本的来源）。 */
const STAGE_LABEL: Record<StageKey, string> = {
  download: '下载',
  verify: '校验',
  install: '替换',
  restart: '重启'
}

export const STAGE_ORDER: readonly StageKey[] = ['download', 'verify', 'install', 'restart']

/** 阶段名（页面渲染阶段轨的节点文字用）。 */
export function stageLabel(key: StageKey): string {
  return STAGE_LABEL[key]
}

/**
 * 原因码 → 页面文案。
 *
 * 12 个键与后端 `spec §10` 的翻译表**逐字一致**：协议白名单 9 个（设备会发）
 * + 服务端推导 3 个（timeout / superseded / unexpected_version，设备永不发）。
 * 未知码返回空串（见文件头第 2 条）。
 */
const REASON_TEXT: Record<string, string> = {
  download_failed: '下载失败',
  checksum_mismatch: '文件校验不通过',
  smoke_test_failed: '新版本无法启动',
  no_write_permission: '设备上没有写权限',
  replace_failed: '替换程序文件失败',
  exec_failed: '重启 Agent 失败',
  not_connected_after_upgrade: '升级后未能连上服务端，已自动回滚',
  platform_unsupported: '该设备平台不支持自动升级',
  artifact_missing: '服务端没有该设备可用的程序文件',
  // 服务端推导（设备永不上报这三条）。
  timeout: '超时未完成',
  superseded: '已被新的下发取代',
  unexpected_version: '设备上的版本与本次升级无关'
}

/** 原因码 → 文案；未知或缺失返回空串（页面渲染成「—」，不编造结论）。 */
export function reasonText(code?: string | null): string {
  if (!code) return ''
  return REASON_TEXT[code] ?? ''
}

/** 相位 → 文案；无目标（空串）返回空串。 */
export function phaseText(phase?: UpgradePhase): string {
  switch (phase) {
    case 'pending':
      return '待升级'
    case 'running':
      return '升级中'
    case 'achieved':
      return '已达成'
    default:
      return ''
  }
}

/** 升级终态（int8）→ 文案；0（无终态）返回空串。 */
export function resultText(state?: number | null): string {
  switch (state) {
    case UPGRADE_RESULT.achieved:
      return '已达成'
    case UPGRADE_RESULT.failed:
      return '失败'
    case UPGRADE_RESULT.rolledBack:
      return '已回滚'
    default:
      return ''
  }
}

/** 语义色等级（页面用它挑 Element Plus 的 tag 类型）。 */
export type Tone = 'success' | 'warning' | 'danger' | 'info' | 'muted'

/**
 * Tone → ElTag 的 `type`。
 *
 * 单独一层映射的理由：`muted`（灰、不引人注目）是本模块的语义（「不支持远程升级」
 * 这类**不是错误**的状态），而 Element Plus 的 tag 只有 5 个类型、没有灰色语义 ——
 * 让每个页面各自写 `tone === 'muted' ? 'info' : tone` 就是同一份判断抄 N 遍。
 */
export function tagTypeOf(tone: Tone): 'success' | 'warning' | 'danger' | 'info' | 'primary' {
  return tone === 'muted' ? 'info' : tone
}

/** 列表行上的升级标记（版本列旁边那一个小标签）。 */
export interface UpgradeBadge {
  text: string
  tone: Tone
  /** 悬浮解释（可空）：只在「为什么不动」这类需要说明时给。 */
  hint: string
}

/** 列表行需要的升级字段（与后端 `DeviceListItem` 的升级字段一一对应）。 */
export interface UpgradeRow {
  agentVersion?: string
  agentUpgradeSupported?: boolean
  targetVersion?: string
  targetFromGlobal?: boolean
  /**
   * 相位。用 `string` 而不是联合类型：后端字段就是字符串，**未知取值必须
   * 被容忍**（新版本后端加了相位时，老前端应当退回「无标记」而不是报错）。
   */
  upgradePhase?: string
  upgradeResult?: number
  upgradeReason?: string
}

/**
 * 合成列表行上的升级标记。
 *
 * 优先级（顺序即语义优先级，改顺序前先想清楚「什么情况下两个都成立」）：
 *  1. 没有目标 → 不显示标记（页面显示「—」，不伪造相位）；
 *  2. 设备不支持远程升级 → 明确的「不支持」（点按钮也没用，必须说清楚）；
 *  3. 升级中 → 进度由明细页看，列表只给「升级中」；
 *  4. 待升级 → 「待升级」；
 *  5. 已达成（版本 == 目标）→「已达成」；
 *  6. 有失败/回滚终态但目标未达成 → 那一档（含原因）。
 */
export function upgradeBadge(row: UpgradeRow): UpgradeBadge | null {
  const target = row.targetVersion ?? ''
  if (!target) return null
  if (!row.agentUpgradeSupported) {
    return {
      text: '不支持远程升级',
      tone: 'muted',
      hint: '该 Agent 版本不支持远程升级，需先手工安装新版本'
    }
  }
  if (row.upgradePhase === 'running') return { text: '升级中', tone: 'warning', hint: '' }
  if (row.upgradePhase === 'pending') return { text: '待升级', tone: 'info', hint: '' }
  if (row.upgradePhase === 'achieved') return { text: '已达成', tone: 'success', hint: '' }
  // 没有相位但已有终态（例如设备被停用、或平台无产物）——按终态给结论。
  const result = resultText(row.upgradeResult)
  if (result) {
    const reason = reasonText(row.upgradeReason)
    return {
      text: result,
      tone: row.upgradeResult === UPGRADE_RESULT.rolledBack ? 'danger' : 'danger',
      hint: reason
    }
  }
  return { text: '待升级', tone: 'info', hint: '' }
}

/** 阶段轨的渲染视图。 */
export interface StageView {
  /** 四个节点（顺序固定）。 */
  keys: readonly StageKey[]
  /** 当前所处节点下标；-1 = 未开工（等待设备上线）。 */
  index: number
  /** 当前节点名（未开工时为空）。 */
  label: string
  /** 结论式进度描述（例：「下载中 62%」「已重启，等待新版本上线」）。 */
  description: string
  /** 真实字节百分比；null = 本阶段没有百分比（不是 0%）。 */
  progress: number | null
  /** 是否已失败（该行显示 ✕ 与原因）。 */
  failed: boolean
  /** 是否已回滚（终态之一，与失败分开显示）。 */
  rolledBack: boolean
  /** 是否终态成功。 */
  succeeded: boolean
}

/**
 * 把一次尝试（状态 + 进度 + 原因）折成阶段轨视图。
 *
 * 失败与回滚**都**保留阶段轨的「卡在哪一步」信息：`failed` 时把 index 停在
 * 最后一个已知阶段（后端上报 failed 时不再给阶段，故取不到就显示整轨未完成 + ✕）。
 */
export function stageView(
  state?: string,
  progress?: number | null,
  reasonCode?: string
): StageView {
  const base: StageView = {
    keys: STAGE_ORDER,
    index: -1,
    label: '',
    description: '',
    progress: null,
    failed: false,
    rolledBack: false,
    succeeded: false
  }
  switch (state) {
    case 'pending':
      base.description = '等待设备上线'
      return base
    case 'downloading': {
      const pct = typeof progress === 'number' ? progress : null
      base.index = 0
      base.label = STAGE_LABEL.download
      base.progress = pct
      base.description = pct === null ? '下载中' : `下载中 ${pct}%`
      return base
    }
    case 'verifying':
      base.index = 1
      base.label = STAGE_LABEL.verify
      base.description = '校验通过，正在准备替换'
      return base
    case 'installing':
      base.index = 2
      base.label = STAGE_LABEL.install
      base.description = '正在替换程序文件'
      return base
    case 'restarting':
      base.index = 3
      base.label = STAGE_LABEL.restart
      base.description = '已重启，等待新版本上线'
      return base
    case 'failed':
      base.failed = true
      base.description = reasonText(reasonCode) || '升级失败'
      return base
    case 'rolled_back':
      base.rolledBack = true
      base.description = reasonText(reasonCode) || '已自动回滚'
      return base
    case 'succeeded':
      base.succeeded = true
      base.index = STAGE_ORDER.length
      base.description = '已完成'
      return base
    case 'timeout':
      base.failed = true
      base.description = '超时未完成'
      return base
    case 'superseded':
      base.description = '已被新的下发取代'
      return base
    default:
      return base
  }
}

/** 「第 N/4 步：<阶段>」——阶段轨的读屏/色弱用户的文字形式。 */
export function stageText(view: StageView): string {
  if (view.index < 0 || view.index >= STAGE_ORDER.length) return ''
  return `第 ${view.index + 1}/${STAGE_ORDER.length} 步：${view.label}`
}

/** 发布物（`AgentReleaseItem` 的子集）。 */
export interface ReleaseLike {
  version: string
  os: string
  arch: string
  status: number
}

/** 设备平台（详情/列表里能拿到的两个字段）。 */
export interface PlatformLike {
  os?: string
  arch?: string
}

/** 已发布状态（后端 1 = 已发布，0 = 草稿）。 */
export const RELEASE_PUBLISHED = 1

/**
 * 过滤出「可被这批设备升级到的版本」。
 *
 * 规则：**已发布** ∩ **每个选中设备的平台都有产物**。
 * 不满足第二条的版本会在下发时被跳过（后端 classify 的 no_artifact），
 * 与其让操作员点完才知道，不如根本不列出来 —— 但「有设备平台不匹配」这件事
 * 本身不需要在 UI 上解释（选择设备的人自己清楚它们的平台）。
 */
export function availableVersions(
  publishedVersions: readonly string[],
  releases: readonly ReleaseLike[],
  devices: readonly PlatformLike[]
): string[] {
  if (devices.length === 0) return []
  const published = new Set(publishedVersions)
  const byVersion = new Map<string, Set<string>>()
  for (const rel of releases) {
    if (rel.status !== RELEASE_PUBLISHED) continue
    if (!published.has(rel.version)) continue
    const key = `${rel.os}/${rel.arch}`
    const set = byVersion.get(rel.version) ?? new Set<string>()
    set.add(key)
    byVersion.set(rel.version, set)
  }
  return publishedVersions.filter((v) => {
    const set = byVersion.get(v)
    if (!set) return false
    return devices.every((d) => set.has(`${d.os ?? ''}/${d.arch ?? ''}`))
  })
}

/** 目标来源标注：跟随全站 / 设备指定 / 无目标。 */
export function targetSourceText(fromGlobal?: boolean, hasTarget?: boolean): string {
  if (!hasTarget) return '—'
  return fromGlobal ? '跟随全站' : '设备指定'
}

/** 升级记录里一行的时间文案（unix 秒 → 本地时间字符串）。 */
export function formatUnixTime(sec?: number | null): string {
  if (typeof sec !== 'number' || !Number.isFinite(sec) || sec <= 0) return '—'
  const d = new Date(sec * 1000)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/** 任务明细的状态分布（后端 `AgentUpgradeCounts`）。 */
export interface CountsLike {
  pending?: number
  running?: number
  succeeded?: number
  failed?: number
  rolledBack?: number
  timeout?: number
  superseded?: number
}

/**
 * 任务行的计数文案（页面上最显眼的一行）。
 *
 * 「等待上线」（pending）与「升级中」（running）**分开报**：前者是还没轮到，
 * 后者是正在进行 —— 混成一个数会让运维去等一台根本没开始的机器。
 * 零值项不出现（只报「有内容的」），但全零时给「—」而不是空串。
 */
export function taskCountsText(counts?: CountsLike | null): string {
  if (!counts) return '—'
  const parts: string[] = []
  const push = (n: number | undefined, label: string) => {
    if (n && n > 0) parts.push(`${label} ${n}`)
  }
  push(counts.succeeded, '已达成')
  push(counts.running, '升级中')
  push(counts.pending, '等待上线')
  push(counts.failed, '失败')
  push(counts.rolledBack, '已回滚')
  push(counts.timeout, '超时')
  // superseded 不上行：它只说明「这条被新的下发取代了」，不是一种状态分布。
  return parts.length > 0 ? parts.join(' · ') : '—'
}

/** 任务/预演的跳过明细（后端 `DeviceUpgradeSkip`）。 */
export interface SkipLike {
  alreadyOnTarget?: number
  unsupported?: number
  noArtifact?: number
  disabled?: number
}

/** 跳过明细 → 一行结论（四类分开报，避免「跳过 7 台」这种没法处置的汇总）。 */
export function skipText(skip?: SkipLike | null): string {
  if (!skip) return ''
  const parts: string[] = []
  const push = (n: number | undefined, label: string) => {
    if (n && n > 0) parts.push(`${label} ${n}`)
  }
  push(skip.alreadyOnTarget, '已在该版本')
  push(skip.unsupported, '不支持远程升级')
  push(skip.noArtifact, '无该平台程序包')
  push(skip.disabled, '已停用')
  return parts.join(' · ')
}

/** 跳过总数（预演里「不会下发」的台数）。 */
export function skipTotal(skip?: SkipLike | null): number {
  if (!skip) return 0
  return (
    (skip.alreadyOnTarget ?? 0) +
    (skip.unsupported ?? 0) +
    (skip.noArtifact ?? 0) +
    (skip.disabled ?? 0)
  )
}

/** 任务来源 → 文案。 */
export function sourceText(source?: string | null): string {
  switch (source) {
    case 'manual':
      return '单台'
    case 'batch':
      return '多选'
    case 'filter':
      return '按筛选'
    case 'global':
      return '全站'
    default:
      return '—'
  }
}

/** 发布物状态 → 文案。 */
export function releaseStatusText(status?: number | null): string {
  return status === RELEASE_PUBLISHED ? '已发布' : '草稿'
}

/** 字节数 → 可读大小（KB/MB，保留一位小数）。 */
export function formatSize(bytes?: number | null): string {
  if (typeof bytes !== 'number' || !Number.isFinite(bytes) || bytes <= 0) return '—'
  if (bytes >= 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(1)} MB`
  if (bytes >= 1024) return `${(bytes / 1024).toFixed(0)} KB`
  return `${bytes} B`
}
