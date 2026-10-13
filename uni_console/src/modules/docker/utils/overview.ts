/**
 * 总览页（控制塔）的纯展示口径：页面状态机、KPI 磁贴数据、主机结论句、异常清单口径、
 * 磁盘面板（6a）的行模型。
 *
 * 为什么放纯函数而不是写在视图里：device 模块的同一取舍（device/utils/overview.ts）
 * —— 这些口径出错都不抛异常，只表现为「页面说错话」（失败被渲染成空态、停止数
 * 误报成异常数），写在视图里就测不住；抽成纯函数后每条口径都能直接断言。
 */
import { formatCapacityMb } from '@/modules/device/utils/display'
import type { DockerContainerItem, DockerHostItem, DockerOverviewResp } from '../api'
import { containerStateZh } from './display'
import { syncText } from './host'

export type OverviewPageState = 'loading' | 'error' | 'empty' | 'ready'

/**
 * 页面四态的优先级：error > loading > empty > ready。
 *
 * hostCount 约定：meta 还没到（且无错）时传 **-1**，让 empty 不可达 —— 请求失败时
 * hosts 是空数组，若直接把 0 当「没有主机」渲染空态，那就是「失败伪装成没有主机」
 * （device 总览页同一条纪律）。
 */
export function overviewPageState(
  hasError: boolean,
  firstLoading: boolean,
  hostCount: number
): OverviewPageState {
  if (hasError) return 'error'
  if (firstLoading) return 'loading'
  if (hostCount < 0) return 'loading'
  if (hostCount === 0) return 'empty'
  return 'ready'
}

/** KPI 磁贴副行的一个片段；tone 只借既有语义色（EP 变量），不新增调色板。 */
export interface OverviewKpiSegment {
  text: string
  tone?: 'success' | 'warning' | 'danger'
}

export interface OverviewKpiTile {
  key: string
  /** 图标名：有下钻页面的五枚与菜单同源（见 buildOverviewKpis 的 iconByPath）。 */
  icon: string
  /** 图标方块渐变的基色（EP/theme 变量字符串，磁贴以 --tile 消费）。全部磁贴共用
   * NEUTRAL_TILE 一枚中性色（去装饰化：图标不再承载分类语义，见该常量）。 */
  tile: string
  /** 主数值：六个资源维度都是**计数**（number，跨主机合计）。 */
  value: number
  label: string
  sub: OverviewKpiSegment[]
  /** 下钻目标：模块内列表页的 path（router.push 字符串）。 */
  to?: string
  /** 本页锚点（与 to 互斥）：主机磁贴没有「主机列表页」，下钻目标是下方的分区面板。 */
  anchor?: string
  /** 悬停说明：只写账目口径（这块数字是什么账）。原「点击进入/滚动到…」去向句已按
   * 「零解释文案」纪律删除 —— 磁贴整块可点，去向由点击行为本身表达，不需要文案预告。 */
  title: string
}

/** menuIconByPath 的输入节点：菜单树本函数只消费这三个字段（便于单测构造）。 */
export interface MenuIconNode {
  path: string
  meta?: { icon?: string }
  children?: MenuIconNode[]
}

/**
 * 从菜单树（useMenuStore().menuList，MenuProcessor 的输出）收集 path → meta.icon。
 *
 * KPI 磁贴图标与侧边栏同源的输入 —— 后端菜单模式下 meta.icon 即 sys_menu.icon，
 * 运维在「系统管理 → 菜单管理」改一次，侧边栏/页签/磁贴一起变；前端菜单模式下它
 * 来自插件清单（modules/<name>/index.ts），同样是侧边栏所见的那一个。
 *
 * 遍历/覆盖口径：父先于子，同 path 时**子节点覆盖父节点** —— 顶层目录的 path 由
 * 首个子菜单推导（与总览菜单同为 /docker），「该页面的图标」应以菜单节点为准；
 * icon 为空/缺省的节点不进映射（既不覆盖父值，调用侧也据此回退硬编码值）。
 */
export function menuIconByPath(list: MenuIconNode[]): Record<string, string> {
  const out: Record<string, string> = {}
  const walk = (nodes: MenuIconNode[]) => {
    for (const node of nodes) {
      const icon = node.meta?.icon
      if (icon) out[node.path] = icon
      if (node.children?.length) walk(node.children)
    }
  }
  walk(list)
  return out
}

/**
 * KPI 磁贴图标底的中性基色（**集中一处**防漂移 —— device buildOverviewStats 的
 * STAT_TILE 同款取舍：五块磁贴各写一遍渐变色必然漂移）。
 *
 * 全部磁贴共用这一枚中性色：图标**不再承载分类语义**。此前七块各自取
 * primary / secondary / warning / info / primary / success / warning —— 把 Element 的
 * 语义色名当分类色板用，结果是蓝×2（容器与 Compose 无从区分）、琥珀×2（数据卷与
 * 磁盘占用）、而「网络」拿到 info 灰，在浅底上读起来像禁用态。语义改由 sub[].tone
 * 的文字色承载（那块才是「需要注意」的信号，图标只做识别）。
 *
 * 取值用 primary（本产品的「中性/总量」色，device 概览磁贴同款语义），**不用灰** ——
 * device 的 STAT_TILE.disabled 正是灰（#94a3b8），灰在本产品调色板里 = 未启用/禁用，
 * 拿它当中性会重蹈「读成禁用」的覆辙。颜色全走 EP/theme 变量零写死（overview-tokens 硬约束）。
 */
const NEUTRAL_TILE = 'var(--el-color-primary)'

/**
 * 舰队 KPI 磁贴：每个资源维度一块，主数值是跨主机合计，副行给分项。
 *
 * **六块 = 六个资源维度**（磁盘占用本波移出 KPI 栅格）：磁盘是「跨资源的存储总账」，
 * 与逐资源计数不同类，混进栅格会让 2/3/6 列全都除不尽（6 才是 2/3/6 的公倍数）；
 * 它已有独立分区（#dov-disk 的磁盘面板），「N 台已上报」的缺口由分区副标题
 *（diskPanelSubtitle）如实给出，不再占用一块磁贴。
 *
 * 分项数字的着色是**信息**不是装饰：停止数 > 0 用 danger、未用数 > 0 用 warning、
 * 全部可用用 success —— 0 时不上色，颜色恒与「需要注意」绑定，运维扫一眼就知道哪块有事。
 *
 * 图标同源（本波）：五枚有下钻页面（to）的磁贴 —— 容器/镜像/数据卷/网络/项目 ——
 * 图标取**菜单同路径的 meta.icon**（iconByPath 入参，视图侧用 menuIconByPath 从
 * 菜单树收集）：运维改「菜单管理」里的图标，磁贴与侧边栏/页签一起变。菜单里查不到
 * 或 icon 为空则回退硬编码的主题图标值（下方各磁贴的字面量，与 v015 种子同源），
 * 磁贴永远有图标。主机一枚无对应菜单（锚到本页面板），不参与同源。
 */
export function buildOverviewKpis(
  fleet: Api.Docker.DockerOverviewFleet,
  iconByPath?: Record<string, string>
): OverviewKpiTile[] {
  const stopped = fleet.containers.stopped
  const stoppedTone = stopped > 0 ? 'danger' : undefined
  const imagesUnused = fleet.images.unused
  const volumesUnused = fleet.volumes.unused
  const projectsRunning = fleet.projects.running
  const projectsAllRunning = fleet.projects.total > 0 && projectsRunning === fleet.projects.total
  const hostsOk = fleet.hosts.dockerOk
  /** 菜单覆盖优先、硬编码主题图标兜底（菜单 icon 为空串也走兜底）。 */
  const navIcon = (path: string, fallback: string) => iconByPath?.[path] || fallback

  return [
    {
      key: 'containers',
      icon: navIcon('/docker/containers', 'cib:docker'),
      tile: NEUTRAL_TILE,
      value: fleet.containers.total,
      label: '容器',
      sub: [
        { text: `运行 ${fleet.containers.running}`, tone: 'success' },
        { text: `停止 ${stopped}`, tone: stoppedTone },
        { text: `受保护 ${fleet.containers.protected}` }
      ],
      to: '/docker/containers',
      title: '跨全部主机合计的容器账目'
    },
    {
      key: 'images',
      icon: navIcon('/docker/images', 'pajamas:container-image'),
      tile: NEUTRAL_TILE,
      value: fleet.images.total,
      label: '镜像',
      sub: [{ text: `未使用 ${imagesUnused}`, tone: imagesUnused > 0 ? 'warning' : undefined }],
      // 拆分切片：镜像已是独立整页 /docker/images，下钻直达（磁贴不带 host ——
      // 账目本来就是跨主机合计，进入后是全部主机视图）。
      to: '/docker/images',
      title: '跨全部主机合计的镜像账目'
    },
    {
      key: 'volumes',
      icon: navIcon('/docker/volumes', 'ri:hard-drive-3-line'),
      tile: NEUTRAL_TILE,
      value: fleet.volumes.total,
      label: '数据卷',
      sub: [{ text: `未使用 ${volumesUnused}`, tone: volumesUnused > 0 ? 'warning' : undefined }],
      to: '/docker/volumes',
      title: '跨全部主机合计的数据卷账目'
    },
    {
      key: 'networks',
      icon: navIcon('/docker/networks', 'material-symbols:graph-2'),
      tile: NEUTRAL_TILE,
      value: fleet.networks.total,
      label: '网络',
      sub: [],
      to: '/docker/networks',
      title: '跨全部主机合计的网络数'
    },
    {
      key: 'projects',
      icon: navIcon('/docker/projects', 'catppuccin:docker-compose'),
      tile: NEUTRAL_TILE,
      value: fleet.projects.total,
      label: 'Compose',
      sub: [
        {
          text: `运行 ${projectsRunning}`,
          tone: projectsAllRunning ? 'success' : fleet.projects.total > 0 ? 'warning' : undefined
        }
      ],
      to: '/docker/projects',
      title: '跨全部主机合计的 Compose 项目'
    },
    {
      key: 'hosts',
      icon: 'ri:server-line',
      tile: NEUTRAL_TILE,
      value: fleet.hosts.total,
      label: '主机',
      sub: [{ text: `可用 ${hostsOk}`, tone: hostsOk < fleet.hosts.total ? 'warning' : 'success' }],
      anchor: 'dov-hosts',
      title: '可管主机数'
    }
  ]
}

export type HostVerdictTone = 'success' | 'warning' | 'danger'

/** 主机卡片第二条事实线（Docker 能力结论）。 */
export interface HostVerdict {
  tone: HostVerdictTone
  text: string
}

/**
 * 主机的 Docker 结论句。
 *
 * 只判断 error/dockerOk，**不碰 stale**：陈旧是时间事实，由第三条线（syncText 的
 * 「数据陈旧 N 分钟」）负责 —— 两条线各说一个事实，合并会出现「Docker 正常（数据
 * 三小时前的）」与「数据陈旧」反复互相顶替的句子。
 *
 * error 是服务端算好的结论句（dockerOk=false 的原因），前端只兜一个空串时的短句，
 * 不重复判断「为什么不可用」—— 那是 agent 的领域。
 */
export function hostVerdict(host: DockerHostItem): HostVerdict {
  if (host.error) return { tone: 'danger', text: host.error }
  if (!host.dockerOk) return { tone: 'danger', text: 'Docker 不可用' }
  return { tone: 'success', text: 'Docker 正常' }
}

/**
 * 主机卡片的同步文案：把 DockerHostItem（只有 lastSync unix 秒 + stale 结论）换算成
 * syncText 的入参。lastSync 缺省/为 0 = 从未上报（后端 DTO 的约定口径）。
 */
export function hostSyncText(host: DockerHostItem, nowSec = Math.floor(Date.now() / 1000)): string {
  const last = host.lastSync ?? 0
  const age = last > 0 ? Math.max(0, nowSec - last) : 0
  return syncText(host.stale, age, last <= 0)
}

/** 同步文案的语义色：陈旧/从未上报是打折信号（琥珀），新鲜不上色。 */
export function hostSyncTone(host: DockerHostItem): 'warning' | 'none' {
  return host.stale || !(host.lastSync ?? 0) ? 'warning' : 'none'
}

export type AnomalyStateTone = 'danger' | 'warning' | 'info'

/**
 * 异常状态文案：词汇与容器详情页的 STATE_TEXT 同一套（已停止/已创建/已暂停/重启中），
 * 仅 dead 独立成「已死亡」—— 在这张「要闻」清单里，「死了」与「停了」是两种严重度
 * 的事实，合并会丢掉最该被看见的那条。未知态回退显示 Docker 原始值（不编词）。
 */
const ANOMALY_STATE_TEXT: Record<string, string> = {
  created: '已创建',
  restarting: '重启中',
  paused: '已暂停',
  exited: '已停止',
  dead: '已死亡',
  removing: '移除中'
}

export function anomalyStateText(state: string): string {
  return ANOMALY_STATE_TEXT[state] ?? state
}

/**
 * 异常状态的色点：dead/removing → danger（真坏），restarting/paused → warning
 * （需要盯），exited/created/未知 → info（常态非运行 —— 详情页对 exited 同样用
 * info；退出常是一次性任务跑完，不是故障）。清单只收非 running 态，故无 success 档。
 */
export function anomalyStateTone(state: string): AnomalyStateTone {
  if (state === 'dead' || state === 'removing') return 'danger'
  if (state === 'restarting' || state === 'paused') return 'warning'
  return 'info'
}

/**
 * 异常清单「状态句」列的中文：**复用容器列表/镜像详情同一把尺**（display.ts 的
 * `containerStateZh`），把原生状态句里的退出码/英文时长/health 译成中文
 * （「已停止（退出码 143）4 天前」），不再把 Docker 的原生串「Exited (143) 4 days ago」
 * 直接渲染在中文界面里（同屏两种语言的病灶）。
 *
 * 为什么按结构性收窄（Pick）喂进去而不是照抄一份转换：异常行 DTO
 *（DockerOverviewAnomalyItem）只带 state/statusText 两个状态字段，而
 * `containerStateZh` 恰好只读这两个 —— 本地化口径（状态码词表、时长、退出码、health）
 * 保持**单一事实源**在 display.ts，这里只做接线不另造措辞。
 *
 * 与同表「状态」列（`anomalyStateText`，exited→「已停止」）刻意不同词：那张表是
 * 「要闻」词表（已停止/已创建/已暂停/重启中 + dead 独立成「已死亡」），本列是
 * 与列表逐字同源的**状态句**（含退出码与时长）。两列各说一个事实，不合并。
 */
export function anomalyStatusZh(item: Pick<DockerContainerItem, 'state' | 'statusText'>): string {
  return containerStateZh(item as DockerContainerItem)
}

/**
 * 异常清单的截断口径：后端 Total 是**截断前**的全量数、Items 至多 50 条，两者不等
 * 即「截断了」。不用 `total > 50` 字面判断 —— 上限若调整，这条文案依然成立。
 * 未截断返回 null（表尾不渲染）。
 */
export function anomalyTruncation(total: number, shown: number): string | null {
  if (total <= shown) return null
  return `共 ${total} 条，仅显示前 ${shown}`
}

/** 总览页副标题（页头 live-dot 旁边那行舰队摘要）。 */
export function overviewSubtitle(
  resp: DockerOverviewResp | null,
  loading: boolean,
  hasError: boolean,
  autoRefresh: boolean
): string {
  // 原「点击右侧刷新重试」去向/定位句已按「零解释文案」纪律删除（右侧刷新按钮
  // 本身即入口，副标题只留失败结论）。
  if (hasError) return '总览拉取失败'
  if (!resp) return loading ? '正在拉取舰队总览…' : '暂无数据'
  const parts = [
    `共 ${resp.fleet.hosts.total} 台主机 · ${resp.fleet.hosts.dockerOk} 台可用`,
    `容器 ${resp.fleet.containers.total}（运行 ${resp.fleet.containers.running} / 停止 ${resp.fleet.containers.stopped}）`
  ]
  if (autoRefresh) parts.push('每 10 秒自动刷新')
  return parts.join(' · ')
}

/* ── 磁盘面板（6a 磁盘治理 + Wave 4d 真实水位）：行模型与刻度 ─────────── */

/** 磁盘面板的一条数据条（镜像/卷/构建缓存之一）。 */
export interface DiskBarModel {
  key: 'images' | 'volumes' | 'buildCache'
  label: string
  /** 数值文本（条旁直读 —— 条形只比行内相对比例，绝对量由文字承载）。 */
  text: string
  /** 0-100：该条占本行合计（分母 = 三类占用之和）的比例 —— 构成（parts-of-whole）。
   *  **不承载严重度**：此处只回答「花在哪」，严重度归整机水位（diskWaterModel）。 */
  percent: number
}

export type DiskUsageTone = 'primary' | 'warning' | 'danger'

/**
 * 磁盘水位分档（device 概览的 usageTone 80/90 口径同源）。
 *
 * 入参是**宿主整机磁盘已用百分比**（`diskUsedPercent`，带分母）—— Wave 4d 起这是
 * 「这台机器盘快满了吗」的**真实水位**，不再是 Wave 3b 那个「单一类目占该机 docker
 * 合计」的代理信号（那个比例只回答「docker 花在哪」，见 DiskBarModel）。
 *
 * **缺省（从未上报/未采集）时不调用本函数** —— 调用方走「无数据」分支、显示「—」，
 * **绝不回落成 0%**（「没有指标」≠「0% 已用」）。
 */
export function diskUsageTone(percent: number): DiskUsageTone {
  if (percent >= 90) return 'danger'
  if (percent >= 80) return 'warning'
  return 'primary'
}

/**
 * 一台主机的整机磁盘水位（与 `disk`（docker `system df` 占用账目）**完全不同源、
 * 不可混用**：那个只有分子、没有宿主容量分母）。
 */
export interface DiskWaterModel {
  /** available=false = 该主机未上报整机水位（缺省，不是 0%）。 */
  available: boolean
  /** 0-100 整机已用比例（clamp；available=false 时无意义）。 */
  percent: number
  /** 数值文本（「已用 62% · 248.1 / 400 GB」；无分母「已用 62%」；缺省「—」）。 */
  text: string
  /** 严重度分档（diskUsageTone，按真实 diskUsedPercent）；缺省时无意义。 */
  tone: DiskUsageTone
}

/** 整机水位数值文本：已用百分比 + 分母（已用/总量 GB 或 TB，单位只写一次）。 */
function waterText(percent: number, totalGb?: number | null): string {
  const p = Math.round(percent)
  if (typeof totalGb !== 'number' || !Number.isFinite(totalGb) || totalGb <= 0) {
    return `已用 ${p}%`
  }
  const used = (totalGb * percent) / 100
  // 两侧同单位（进位规则同 device 的 formatGb：≥1024GB 记 TB），单位只写一次。
  if (totalGb >= 1024) {
    return `已用 ${p}% · ${(used / 1024).toFixed(2)} / ${(totalGb / 1024).toFixed(2)} TB`
  }
  return `已用 ${p}% · ${used.toFixed(1)} / ${totalGb.toFixed(0)} GB`
}

/**
 * 一台主机的整机水位模型。**缺省即缺省**：`diskUsedPercent` 不是有限数 → available=
 * false、text「—」，不拿 docker 占用折算成水位（两者不同源）。
 */
export function diskWaterModel(host: DockerHostItem): DiskWaterModel {
  const pct = host.diskUsedPercent
  if (typeof pct !== 'number' || !Number.isFinite(pct)) {
    return { available: false, percent: 0, text: '—', tone: 'primary' }
  }
  const clamped = Math.min(100, Math.max(0, pct))
  return {
    available: true,
    percent: clamped,
    text: waterText(clamped, host.diskTotalGb),
    tone: diskUsageTone(clamped)
  }
}

/** 磁盘面板的一行（一台主机的磁盘账目）。 */
export interface DiskRowModel {
  host: DockerHostItem
  /** available=false = 该主机无 df 数据（显示「数据不可用」，不渲染条形与结论）。 */
  available: boolean
  /** 三类占用合计的文本（「这台主机的 docker 吃了多少磁盘」的那一句答案）。 */
  totalText: string
  /** 整机磁盘水位（宿主盘快满了吗）—— 与上面 docker 占用账目是两个独立的账。 */
  water: DiskWaterModel
  bars: DiskBarModel[]
  /** 结论行：悬空镜像体积/计数 + 未用卷计数（「怎么安全收回」的两条线索）。 */
  reclaimText: string
}

/**
 * 行内条长刻度：以**该行三类占用合计**为分母（parts-of-whole，三条加起来 = 100%）。
 *
 * 本波从「以该行最大占用为 100%」改为「占本行合计的比例」：条形的任务是回答
 * 「这台主机的 docker 盘花在哪」（构成），构成天然是**部分对整体**的编码 —— 三条
 * 相加回 100% 才读得出「谁占了多大盘」。分母 = 该主机三类之和（**不是**宿主磁盘
 * 容量：那个分母属于整机水位 diskWaterModel，与 docker 占用账目不同源）；全零行各条
 * 0% 是合法状态（df 有数据、占用确为零 —— 面板靠可见的未填充轨道把「0」和「没有
 * 这条」区分开）。
 */
export function diskBarPercent(value: number, rowTotal: number): number {
  if (!(rowTotal > 0) || !(value > 0)) return 0
  return Math.min(100, (value / rowTotal) * 100)
}

/**
 * 磁盘面板一行的展示模型。
 *
 * 条形用**单一色相为底**、**不随比例着色**：三类占用是「这台主机的 docker 盘花在
 * 哪」的构成，构成不承载严重度（Wave 3b 曾拿这个比例冒充水位上 warning/danger，
 * Wave 4d 废除 —— 严重度归整机水位 water）。同色 + 每条自带文字标签即可（身份不靠
 * 颜色承载）。条旁数值恒随行直读，色觉缺陷/打印下无信息丢失。悬空镜像那部分是
 * 「可回收」的状态语义，只出现在结论行的文字里，不画条 —— 把它画成条会让「已占用」
 * 与「可回收」两种量在同一根轴上打架。
 *
 * 两笔账各归其位（And 而不同）：**整机水位**（water）答「盘快满了吗」、按真实
 * diskUsedPercent 分档；**docker 占用**（bars）答「花在哪」、单一色相。水位缺省时，
 * 行内照常保留 docker 占用条（那是已知事实），水位列显示「—」—— 读者能区分「这块
 * 说的是 docker 占用」与「这台机器盘快满了」。
 *
 * host.disk 为 nil（旧版 agent / 那帧 df 失败）：available=false，不给任何数字 ——
 * 「数据不可用」是行级结论，不能折算成零（没有数据 ≠ 没有占用）。水位独立于 df：
 * 即便 df 缺席，整机水位照常按其自身是否有值呈现。
 */
export function diskRowModel(host: DockerHostItem): DiskRowModel {
  const water = diskWaterModel(host)
  const d = host.disk
  const na: DiskRowModel = {
    host,
    available: false,
    totalText: '—',
    water,
    bars: [],
    reclaimText: ''
  }
  if (!d) return na
  const rowTotal = d.imagesMb + d.volumesMb + d.buildCacheMb
  /** 一条数据条：比例走 share-of-total（构成），不驱动颜色。 */
  const bar = (key: DiskBarModel['key'], label: string, value: number): DiskBarModel => ({
    key,
    label,
    text: formatCapacityMb(value),
    percent: diskBarPercent(value, rowTotal)
  })
  const bars: DiskBarModel[] = [
    bar('images', '镜像', d.imagesMb),
    bar('volumes', '数据卷', d.volumesMb),
    bar('buildCache', '构建缓存', d.buildCacheMb)
  ]
  // 结论行只摆事实 + 入口，不在面板里执行清理（确认档流在镜像/卷列表页）：
  // 悬空镜像的体积是 image:prune 默认目标的大小（「能收回多少」），未用卷只给
  // 计数（volume:prune 只回收其中的匿名卷，合计口径拿不到，不编数）。
  const parts: string[] = []
  if (d.imagesDanglingMb > 0 || d.danglingImages > 0) {
    parts.push(`悬空镜像 ${d.danglingImages} 个 · ${formatCapacityMb(d.imagesDanglingMb)}`)
  }
  if (d.unusedVolumes > 0) {
    parts.push(`未用数据卷 ${d.unusedVolumes} 个`)
  }
  return {
    host,
    available: true,
    totalText: formatCapacityMb(rowTotal),
    water,
    bars,
    reclaimText: parts.length ? parts.join(' · ') : '无可回收项'
  }
}

/**
 * 磁盘面板全部行的模型，按**值得注意程度**排序（本波改：原「hosts 顺序不重排」）。
 *
 * 排序键：该主机 docker 三类占用合计（MB）降序 —— 占用越大越靠前（「哪台机器吃了
 * 最多磁盘」第一眼可见）；合计相等时按可回收量（悬空镜像体积）降序 —— 同样大小时，
 * 能立刻收回更多的更值得先看。**无 df 数据的主机（available=false）恒排最后** ——
 * 「不知道」不抢占「知道且很大」的位置，它们只占一行「数据不可用」的结论行、不参与
 * 大小比较。排序只改呈现顺序，不改任何行内事实（totalText/reclaimText 照旧）。
 *
 * 整机水位（water）不参与排序：其严重度由**颜色**（warning/danger 填充）承载，且在
 * 面板这个行数不多的分区里一眼可扫，不靠位置表达「盘快满」—— 排序键仍是「谁占了最多
 * docker 盘」，与「行内两类账各说一个事实」的口径一致。
 */
export function diskRowModels(hosts: DockerHostItem[]): DiskRowModel[] {
  const totalMb = (r: DiskRowModel): number => {
    const d = r.host.disk
    return d ? d.imagesMb + d.volumesMb + d.buildCacheMb : -1
  }
  const reclaimMb = (r: DiskRowModel): number => r.host.disk?.imagesDanglingMb ?? -1
  return hosts
    .map(diskRowModel)
    .sort((a, b) => totalMb(b) - totalMb(a) || reclaimMb(b) - reclaimMb(a))
}

/**
 * 磁盘面板的分区副标题：「N 台主机 · M 台已上报磁盘账 · K 台已上报整机水位」。
 *
 * 两笔账各自如实分开两个数（M、K 都可能 <N）：缺账的那台不是 0 占用、缺水位的那台
 * 不是 0% 已用 —— 都是「不知道」。两个缺口分开说，读者才知道面板里哪些数是缺的。
 */
export function diskPanelSubtitle(hosts: DockerHostItem[]): string {
  const reported = hosts.filter((h) => h.disk).length
  const water = hosts.filter((h) => typeof h.diskUsedPercent === 'number').length
  return `共 ${hosts.length} 台 · ${reported} 台已上报磁盘账 · ${water} 台已上报整机水位`
}
