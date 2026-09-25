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

/** 网络文案：上/下行速率（B/s 由 formatByUnit 换算成 KB/s、MB/s）。 */
export function netText(c: DockerContainerItem): string {
  if (c.state !== 'running') return '—'
  return `↑${formatByUnit('B/s', c.netTxBytesSec)}/s ↓${formatByUnit('B/s', c.netRxBytesSec)}/s`
}

/** 端口映射：「20080 → 8088/tcp」，多行用「、」连接；无映射「—」。 */
export function portsText(ports: DockerPortItem[] | undefined): string {
  if (!ports || ports.length === 0) return '—'
  return ports
    .map((p) =>
      p.publicPort
        ? `${p.publicPort} → ${p.privatePort}/${p.type ?? 'tcp'}`
        : `${p.privatePort}/${p.type ?? 'tcp'}`
    )
    .join('、')
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
