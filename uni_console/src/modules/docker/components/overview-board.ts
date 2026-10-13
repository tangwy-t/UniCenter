/**
 * Docker 总览 · 主机就绪板（100 台规模）的纯展示口径：分组、筛选、组内排序、折叠默认态，
 * 以及磁盘面板折叠态的取行规则。
 *
 * 为什么放纯函数：与本模块其它口径同一条取舍（见 utils/overview.ts 文件头）—— 这些判断
 * 出错都不抛异常，只表现为「页面说错话」（把 100 台分错组、把离线/异常的那台藏在折叠里、
 * 排序把最该看的排到最后），写在视图里就测不住。放在 components/ 下与视图同目录，是
 * workload-columns.ts / batch-actions.ts 的既有先例（页面专属的口径与页面放在一起，不
 * 外溢成模块公共 utils）。
 */
import type { DockerHostItem } from '../api'
import { hostLabel } from '../utils/host'
import { hostVerdict, type DiskWaterModel } from '../utils/overview'

/* ── 主机级事实（筛选 / 组头计数 / 排序共用的两把尺） ─────────────────── */

/**
 * 离线：agent 最后已知的在线标志为 false。**与「异常」正交** —— 一台离线的主机若
 * 最后一次上报时 Docker 是好的，它是「离线」而不是「异常」，两个筛选项各答一问。
 */
export function hostIsOffline(host: DockerHostItem): boolean {
  return !host.online
}

/**
 * 异常：**主机级**的 Docker 结论非正常（`error` 或 `dockerOk` 为 false）—— 与主机
 * 卡片上那条结论行（hostVerdict 的 danger 档）逐字同源，读者在卡上看到的红字就是
 * 这里的判据，两处不会互相打脸。
 *
 * 为什么不用「该主机有几个异常容器」当判据：总览的异常清单是**截断到 50 条**的
 * （后端 DockerOverviewAnomalies 的 Total 是截断前全量、Items 至多 50），按它逐主机
 * 计数在 100 台规模下会把 50 条以外的机器全报成 0 个 —— 那是把一个「不知道」写成
 * 「没有」。主机级结论句不带这个截断，100 台也好 2 台也好都是同一把尺。
 */
export function hostIsAbnormal(host: DockerHostItem): boolean {
  return hostVerdict(host).tone === 'danger'
}

/* ── 筛选 ─────────────────────────────────────────────── */

/** 就绪板的筛选条件（三条各自独立，「只看离线」+「只看有异常」可叠加=与）。 */
export interface HostFilter {
  /** 主机名 / IP 的子串（大小写不敏感）。 */
  query: string
  onlyOffline: boolean
  onlyAbnormal: boolean
}

export const EMPTY_HOST_FILTER: HostFilter = { query: '', onlyOffline: false, onlyAbnormal: false }

/** 是否处于「筛选中」（用于分区副标题切换成分母口径与空态文案）。 */
export function hostFilterActive(filter: HostFilter): boolean {
  return filter.query.trim() !== '' || filter.onlyOffline || filter.onlyAbnormal
}

/**
 * 一台主机是否命中筛选：三条是**与**关系（都满足才留下）。
 *
 * 搜索同时匹配主机名与主 IP（运维手里拿到的定位信息要么是名字要么是地址）；匹配
 * 走**大小写不敏感的子串**（主机名/IP 都可能是大小写混杂的 FQDN 形态）。
 * 名字口径复用 hostLabel（主机名 → 地址 → 「主机 <id>」），与卡片上显示的那一行
 * 逐字同源 —— 看到什么就能搜到什么。
 */
export function hostMatches(host: DockerHostItem, filter: HostFilter): boolean {
  if (filter.onlyOffline && !hostIsOffline(host)) return false
  if (filter.onlyAbnormal && !hostIsAbnormal(host)) return false
  const q = filter.query.trim().toLowerCase()
  if (!q) return true
  return (
    hostLabel(host).toLowerCase().includes(q) || (host.primaryIp ?? '').toLowerCase().includes(q)
  )
}

export function filterHosts(hosts: DockerHostItem[], filter: HostFilter): DockerHostItem[] {
  if (!hostFilterActive(filter)) return [...hosts]
  return hosts.filter((h) => hostMatches(h, filter))
}

/* ── 组内排序 ─────────────────────────────────────────── */

/**
 * 组内排序键。
 *
 * - `attention`（默认）「异常优先」：异常主机 → 离线主机 → 其余，同级按名称。
 *   本页是控制塔，「先看见出事的」是它的本职；100 台时这一条把需要动手的机器顶到
 *   每组最前面，不必翻页去找。
 * - `disk`「磁盘水位」：整机已用百分比降序（未上报的殿后 —— 「不知道」不抢占「知道
 *   且很高」的位置），同级按名称。
 * - `name`「名称」：名称升序。
 *
 * 注：主机级的「异常」是**布尔事实**（Docker 结论 danger 与否），故没有「按异常数」
 * 的数值序，`attention` 即它的落地形态（异常者优先）。
 */
export type HostSortKey = 'attention' | 'disk' | 'name'

export interface HostSortOption {
  key: HostSortKey
  label: string
}

export const HOST_SORTS: HostSortOption[] = [
  { key: 'attention', label: '异常优先' },
  { key: 'disk', label: '磁盘水位' },
  { key: 'name', label: '名称' }
]

/** 名称比较：中文走 localeCompare（有 ICU 时按拼音，无则退化成码位序，恒确定）。 */
function byName(a: DockerHostItem, b: DockerHostItem): number {
  return hostLabel(a).localeCompare(hostLabel(b), 'zh')
}

/** 整机水位排序值：未上报 = -1（殿后），有值用真实百分比。 */
function waterValue(host: DockerHostItem): number {
  const p = host.diskUsedPercent
  return typeof p === 'number' && Number.isFinite(p) ? p : -1
}

/**
 * 组内排序。**返回新数组**，绝不原地排序 —— 入参是页面 computed 的 hosts
 * （源自 meta.value.hosts），原地排序会改掉服务端给的顺序。
 */
export function sortHosts(hosts: DockerHostItem[], key: HostSortKey): DockerHostItem[] {
  const out = [...hosts]
  if (key === 'name') return out.sort(byName)
  if (key === 'disk') return out.sort((a, b) => waterValue(b) - waterValue(a) || byName(a, b))
  return out.sort(
    (a, b) =>
      Number(hostIsAbnormal(b)) - Number(hostIsAbnormal(a)) ||
      Number(hostIsOffline(b)) - Number(hostIsOffline(a)) ||
      byName(a, b)
  )
}

/* ── 分组 ─────────────────────────────────────────────── */

/** 未命名分组（host.group 空/全空白）的显示名。 */
export const HOST_GROUP_UNNAMED = '未分组'

/** 分组名键（host.group 去空白后即为组键，空串 = 未分组）。 */
export function hostGroupKey(host: DockerHostItem): string {
  return (host.group ?? '').trim()
}

/** 就绪板的一个分组：组头要的三个数（台数 / 离线 / 异常）在建模时就算好。 */
export interface HostGroupModel {
  /** 组键（未分组为 ''）。 */
  key: string
  /** 显示名（未分组为 HOST_GROUP_UNNAMED）。 */
  name: string
  /** 组内主机（已按 sortKey 排好序）。 */
  hosts: DockerHostItem[]
  offline: number
  abnormal: number
}

/** 组序：命名组按名称升序，未分组恒殿后（命名组是运维主动划的账，先看它们）。 */
function byGroupName(a: HostGroupModel, b: HostGroupModel): number {
  if (a.key === '' || b.key === '') return a.key === b.key ? 0 : a.key === '' ? 1 : -1
  return a.name.localeCompare(b.name, 'zh')
}

/**
 * 把筛选后的主机切成有序分组：先筛、再按组内排序键排、再分组，组头计数在**筛选后**
 * 的集合上算 —— 组头的数就是用户当下看得见的那些机器的账，筛选一动它跟着动。
 */
export function buildHostGroups(
  hosts: DockerHostItem[],
  filter: HostFilter,
  sortKey: HostSortKey
): HostGroupModel[] {
  const kept = sortHosts(filterHosts(hosts, filter), sortKey)
  const map = new Map<string, HostGroupModel>()
  for (const h of kept) {
    const key = hostGroupKey(h)
    let g = map.get(key)
    if (!g) {
      g = { key, name: key || HOST_GROUP_UNNAMED, hosts: [], offline: 0, abnormal: 0 }
      map.set(key, g)
    }
    g.hosts.push(h)
    if (hostIsOffline(h)) g.offline += 1
    if (hostIsAbnormal(h)) g.abnormal += 1
  }
  return [...map.values()].sort(byGroupName)
}

/* ── 折叠默认态 ───────────────────────────────────────── */

/**
 * 分组默认折叠的规模阈值。
 *
 * 裁定理由（默认展开 → 规模自适应折叠）：判据是「100 台时首屏看清全局账目，且不把
 * 信息藏到需要多点一次才看见」。12 台 ≈ 三行卡片，与 KPI 栅格一起大体落在首屏内
 * （超出部分本来就要滚动去看，「展开」并没有额外藏起什么）；超过 12 台的组则一整屏
 * 也放不下，此时**组头那行的三个数（共 N 台 · 离线 M · 异常 K）就是该组在首屏的账**，
 * 比一屏塞不下的卡片墙更接近「一眼看清全局」。真实 2 台生产环境（单组 2 台）远在
 * 阈值内 → 全展开，行为与改造前逐字一致（零回归）。
 */
export const GROUP_COLLAPSE_AT = 12

export function groupCollapsedByDefault(size: number): boolean {
  return size > GROUP_COLLAPSE_AT
}

/* ── 磁盘面板：折叠态取行 ─────────────────────────────── */

/** 磁盘面板折叠态显示的行数上限（行高比卡片矮，仍取 10：一屏能扫完且不淹没水位告警）。 */
export const DISK_TOP_N = 10

/**
 * 磁盘面板折叠态显示哪些行。
 *
 * 规则：`diskRowModels` 的**前 N 行**（它就是「谁吃了最多 docker 盘」的既有排序），
 * **外加全部水位告警行**（整机水位 ≥80 / ≥90，tone 非 primary）。两条并起来是为了
 * 「不把最危险的那一行藏进展开里」—— 一台 docker 占用很小但盘已满 95% 的机器，按占用
 * 排永远进不了 TOP-N，而它恰恰是最该被看见的。
 *
 * 保持原序（不做二次排序）：折叠态与展开态的行序一致，展开只是「看见更多」，不会
 * 让已经读过的行跳位置；也保证 2 台生产环境（行数 ≤ N）与改造前逐字一致。
 */
export function diskVisibleRows<T extends { water: DiskWaterModel }>(
  rows: T[],
  topN: number = DISK_TOP_N
): T[] {
  if (rows.length <= topN) return [...rows]
  return rows.filter((r, i) => i < topN || (r.water.available && r.water.tone !== 'primary'))
}
