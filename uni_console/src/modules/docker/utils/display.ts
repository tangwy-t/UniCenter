/** 展示文案（纯函数）。数值一律经 formatByUnit 换算 —— 不在这里另造一套单位换算。 */
import { formatByUnit } from '@/modules/device/utils/display'
import type { DockerContainerItem, DockerImageItem, DockerPortItem } from '../api'

/** 容器状态句：直接用 Docker 的原生状态（"Up 16 hours"），它比我们编的措辞更准。 */
export function containerStateText(c: DockerContainerItem): string {
  return c.statusText || c.state
}

/** 内存用量：「91 MB / 1 GB（9%）」；未运行时返回「—」。 */
export function memText(c: DockerContainerItem): string {
  if (c.state !== 'running') return '—'
  if (!c.memLimitMb) return `${formatByUnit('MB', c.memUsageMb)}`
  const pct = c.memLimitMb > 0 ? Math.round((c.memUsageMb / c.memLimitMb) * 100) : 0
  return `${formatByUnit('MB', c.memUsageMb)} / ${formatByUnit('MB', c.memLimitMb)}（${pct}%）`
}

/** CPU 文案：未运行时「—」。 */
export function cpuText(c: DockerContainerItem): string {
  if (c.state !== 'running') return '—'
  return `${c.cpuPercent.toFixed(1)}%`
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
 * 新旧，逐档细化只会占满一列。未来时刻（时钟偏差）夹到 0，避免出现负数。
 */
export function formatRelativeTime(unixSec: number, now = Date.now() / 1000): string {
  const diff = Math.max(0, now - unixSec)
  if (diff < 3600) return `${Math.max(1, Math.floor(diff / 60))} 分钟前`
  if (diff < 86400) return `${Math.floor(diff / 3600)} 小时前`
  if (diff < 86400 * 30) return `${Math.floor(diff / 86400)} 天前`
  return `${Math.floor(diff / (86400 * 30))} 月前`
}
