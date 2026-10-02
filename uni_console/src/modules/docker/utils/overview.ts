/**
 * 总览页（控制塔）的纯展示口径：页面状态机、KPI 磁贴数据、主机结论句、异常清单口径、
 * 磁盘面板（6a）的行模型。
 *
 * 为什么放纯函数而不是写在视图里：device 模块的同一取舍（device/utils/overview.ts）
 * —— 这些口径出错都不抛异常，只表现为「页面说错话」（失败被渲染成空态、停止数
 * 误报成异常数），写在视图里就测不住；抽成纯函数后每条口径都能直接断言。
 */
import { formatCapacityMb } from '@/modules/device/utils/display'
import type { DockerHostItem, DockerOverviewResp } from '../api'
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
  icon: string
  /** 图标方块渐变的基色（EP/theme 变量字符串，磁贴以 --tile 消费）。 */
  tile: string
  /**
   * 主数值：六个资源维度的磁贴是**计数**（number）；磁盘磁贴是**容量**，直接给
   * 已格式化的文本（"38 GB"）—— 让模板硬塞一个 MB 原始数（39168）读不动，
   * 而为它另开字段会让磁贴组件长出第二种值形态。
   */
  value: number | string
  label: string
  sub: OverviewKpiSegment[]
  /** 下钻目标：模块内列表页的 path（router.push 字符串）。 */
  to?: string
  /** 本页锚点（与 to 互斥）：主机/磁盘磁贴没有「主机列表页」，下钻目标是下方面板。 */
  anchor?: string
  /** 悬停说明：把「点了会发生什么」写出来（磁贴整块可点，需要预告去向）。 */
  title: string
}

/**
 * 舰队 KPI 磁贴：每个资源维度一块，主数值是跨主机合计，副行给分项。
 *
 * 七块 = 六个资源维度 + 一块磁盘占用（6a）。分项数字的着色是**信息**不是装饰：
 * 停止数 > 0 用 danger、未用数 > 0 用 warning、全部可用用 success —— 0 时不上色，
 * 颜色恒与「需要注意」绑定，运维扫一眼就知道哪块有事。
 *
 * 磁盘磁贴的三条纪律：
 *   - 主数值是容量文本（formatCapacityMb，MB 自动进位 GB/TB）—— 磁盘账的读法
 *     天然是容量不是计数；
 *   - 无任何主机上报 df 时显示「—」+「数据不可用」（**不是 0**：「没有数据」与
 *     「没有占用」是两个相反的结论，与卷条目 sizeMb=null 同一纪律）；
 *   - 部分主机未上报（df.hosts < hosts.total）时副行给 warning 计数 —— 合计是
 *     **已知主机的账**，读者必须知道它可能偏小。
 */
export function buildOverviewKpis(fleet: Api.Docker.DockerOverviewFleet): OverviewKpiTile[] {
  const stopped = fleet.containers.stopped
  const stoppedTone = stopped > 0 ? 'danger' : undefined
  const imagesUnused = fleet.images.unused
  const volumesUnused = fleet.volumes.unused
  const projectsRunning = fleet.projects.running
  const projectsAllRunning = fleet.projects.total > 0 && projectsRunning === fleet.projects.total
  const hostsOk = fleet.hosts.dockerOk
  const disk = fleet.disk
  const diskTotalMB = disk.imagesTotalMb + disk.volumesTotalMb + disk.buildCacheMb
  const diskNotReported = fleet.hosts.total - disk.hosts

  return [
    {
      key: 'containers',
      icon: 'ri:ship-line',
      tile: 'var(--el-color-primary)',
      value: fleet.containers.total,
      label: '容器',
      sub: [
        { text: `运行 ${fleet.containers.running}`, tone: 'success' },
        { text: `停止 ${stopped}`, tone: stoppedTone },
        { text: `受保护 ${fleet.containers.protected}` }
      ],
      to: '/docker/containers',
      title: '跨全部主机合计的容器账目 —— 点击进入容器列表'
    },
    {
      key: 'images',
      icon: 'ri:box-1-line',
      tile: 'var(--art-secondary)',
      value: fleet.images.total,
      label: '镜像',
      sub: [{ text: `未使用 ${imagesUnused}`, tone: imagesUnused > 0 ? 'warning' : undefined }],
      // 7a：三个旧列表页收敛为 /docker/resources 的 tab，下钻地址带 ?tab=（字符串
      // 里的 query 由 router.push 解析；磁贴不带 host —— 账目本来就是跨主机合计）。
      to: '/docker/resources?tab=images',
      title: '跨全部主机合计的镜像账目 —— 点击进入镜像表'
    },
    {
      key: 'volumes',
      icon: 'ri:hard-drive-3-line',
      tile: 'var(--el-color-warning)',
      value: fleet.volumes.total,
      label: '数据卷',
      sub: [{ text: `未使用 ${volumesUnused}`, tone: volumesUnused > 0 ? 'warning' : undefined }],
      to: '/docker/resources?tab=volumes',
      title: '跨全部主机合计的数据卷账目 —— 点击进入数据卷表'
    },
    {
      key: 'networks',
      icon: 'ri:share-forward-line',
      tile: 'var(--el-color-info)',
      value: fleet.networks.total,
      label: '网络',
      sub: [],
      to: '/docker/resources?tab=networks',
      title: '跨全部主机合计的网络数 —— 点击进入网络表'
    },
    {
      key: 'projects',
      icon: 'ri:stack-line',
      tile: 'var(--el-color-primary)',
      value: fleet.projects.total,
      label: '项目',
      sub: [
        {
          text: `运行 ${projectsRunning}`,
          tone: projectsAllRunning ? 'success' : fleet.projects.total > 0 ? 'warning' : undefined
        }
      ],
      to: '/docker/projects',
      title: '跨全部主机合计的编排项目 —— 点击进入项目列表'
    },
    {
      key: 'hosts',
      icon: 'ri:server-line',
      tile: 'var(--el-color-success)',
      value: fleet.hosts.total,
      label: '主机',
      sub: [{ text: `可用 ${hostsOk}`, tone: hostsOk < fleet.hosts.total ? 'warning' : 'success' }],
      anchor: 'dov-hosts',
      title: '可管主机数 —— 点击滚动到下方的主机卡片'
    },
    {
      key: 'disk',
      icon: 'ri:database-2-line',
      tile: 'var(--el-color-warning)',
      value: disk.hosts > 0 ? formatCapacityMb(diskTotalMB) : '—',
      label: '磁盘占用',
      sub:
        disk.hosts > 0
          ? [
              { text: `镜像 ${formatCapacityMb(disk.imagesTotalMb)}` },
              { text: `卷 ${formatCapacityMb(disk.volumesTotalMb)}` },
              { text: `缓存 ${formatCapacityMb(disk.buildCacheMb)}` },
              ...(diskNotReported > 0
                ? [{ text: `${diskNotReported} 台未上报`, tone: 'warning' as const }]
                : [])
            ]
          : [{ text: '数据不可用', tone: 'warning' }],
      anchor: 'dov-disk',
      title: '已上报主机的镜像/卷/构建缓存占用合计 —— 点击滚动到磁盘面板'
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
  if (hasError) return '总览拉取失败 —— 点击右侧刷新重试'
  if (!resp) return loading ? '正在拉取舰队总览…' : '暂无数据'
  const parts = [
    `共 ${resp.fleet.hosts.total} 台主机 · ${resp.fleet.hosts.dockerOk} 台可用`,
    `容器 ${resp.fleet.containers.total}（运行 ${resp.fleet.containers.running} / 停止 ${resp.fleet.containers.stopped}）`
  ]
  if (autoRefresh) parts.push('每 10 秒自动刷新')
  return parts.join(' · ')
}

/* ── 磁盘面板（6a 磁盘治理）：行模型与条形刻度 ─────────────────────────── */

/** 磁盘面板的一条数据条（镜像/卷/构建缓存之一）。 */
export interface DiskBarModel {
  key: 'images' | 'volumes' | 'buildCache'
  label: string
  /** 数值文本（条旁直读 —— 条形只比行内相对比例，绝对量由文字承载）。 */
  text: string
  /** 0-100：相对该行最大值的条长。 */
  percent: number
}

/** 磁盘面板的一行（一台主机的磁盘账目）。 */
export interface DiskRowModel {
  host: DockerHostItem
  /** available=false = 该主机无 df 数据（显示「数据不可用」，不渲染条形与结论）。 */
  available: boolean
  /** 三类占用合计的文本（「这台主机的 docker 吃了多少磁盘」的那一句答案）。 */
  totalText: string
  bars: DiskBarModel[]
  /** 结论行：悬空镜像体积/计数 + 未用卷计数（「怎么安全收回」的两条线索）。 */
  reclaimText: string
}

/**
 * 行内条长刻度：以该行三类的最大占用为 100%。
 *
 * 为什么是**行内相对**刻度而不是全舰队统一刻度：条形的任务是回答「这台主机的磁盘
 * 花在哪」（行内构成）；跨主机比大小交给条旁的数值文本 —— 两个问题两种编码，
 * 用全舰队刻度会让小占用主机的条形全部归零（读图失败），用行内刻度则每行最长
 * 那条永远可见。全零行各条 0% 是合法状态（df 有数据、占用确为零）。
 */
export function diskBarPercent(value: number, rowMax: number): number {
  if (!(rowMax > 0) || !(value > 0)) return 0
  return Math.min(100, (value / rowMax) * 100)
}

/**
 * 磁盘面板一行的展示模型。
 *
 * 条形用**单一色相**（EP primary，面板样式侧）：三类占用是量级比较不是状态区分，
 * 同色 + 每条自带文字标签即可（身份不靠颜色承载）；悬空镜像那部分是「可回收」的
 * 状态语义，只出现在结论行的文字里，不上色不画条 —— 把它画成 warning 色条会让
 * 「已占用」与「可回收」两种量在同一根轴上打架。
 *
 * host.disk 为 nil（旧版 agent / 那帧 df 失败）：available=false，不给任何数字 ——
 * 「数据不可用」是行级结论，不能折算成零（与 KPI 磁贴的「—」同一纪律）。
 */
export function diskRowModel(host: DockerHostItem): DiskRowModel {
  const d = host.disk
  const na: DiskRowModel = { host, available: false, totalText: '—', bars: [], reclaimText: '' }
  if (!d) return na
  const rowMax = Math.max(d.imagesMb, d.volumesMb, d.buildCacheMb)
  const bars: DiskBarModel[] = [
    {
      key: 'images',
      label: '镜像',
      text: formatCapacityMb(d.imagesMb),
      percent: diskBarPercent(d.imagesMb, rowMax)
    },
    {
      key: 'volumes',
      label: '数据卷',
      text: formatCapacityMb(d.volumesMb),
      percent: diskBarPercent(d.volumesMb, rowMax)
    },
    {
      key: 'buildCache',
      label: '构建缓存',
      text: formatCapacityMb(d.buildCacheMb),
      percent: diskBarPercent(d.buildCacheMb, rowMax)
    }
  ]
  // 结论行只摆事实 + 入口，不在面板里执行清理（确认档流在镜像/卷列表页）：
  // 悬空镜像的体积是 image:prune 默认目标的大小（「能收回多少」），未用卷只给
  // 计数（volume:prune 只回收其中的匿名卷，合计口径拿不到，不编数）。
  const parts: string[] = []
  if (d.imagesDanglingMb > 0 || d.danglingImages > 0) {
    parts.push(`悬空镜像 ${d.danglingImages} 个 · ${formatCapacityMb(d.imagesDanglingMb)}`)
  }
  if (d.unusedVolumes > 0) {
    parts.push(`未用卷 ${d.unusedVolumes} 个`)
  }
  return {
    host,
    available: true,
    totalText: formatCapacityMb(d.imagesMb + d.volumesMb + d.buildCacheMb),
    bars,
    reclaimText: parts.length ? parts.join(' · ') : '无可回收项'
  }
}

/** 磁盘面板全部行的模型（hosts 顺序即后端行序，不重排 —— 与主机卡片区同一来源）。 */
export function diskRowModels(hosts: DockerHostItem[]): DiskRowModel[] {
  return hosts.map(diskRowModel)
}

/**
 * 磁盘面板的分区副标题：「N 台主机 · M 台已上报磁盘账」。
 *
 * 如实分开两个数而不是只给一个：M<N 时读者必须知道合计缺了几台的账
 *（缺的那台不是 0 占用，是不知道）。
 */
export function diskPanelSubtitle(hosts: DockerHostItem[]): string {
  const reported = hosts.filter((h) => h.disk).length
  return `共 ${hosts.length} 台 · ${reported} 台已上报磁盘账`
}
