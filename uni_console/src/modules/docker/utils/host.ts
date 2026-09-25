/** 主机上下文与同步状态的展示口径（纯函数，便于单测）。 */
import type { DockerHostItem } from '../api'

/**
 * 头部同步状态文案。
 *
 * 三句是**三种不同的事实**，不能合并：从未上报（还没连上过）、陈旧（连过但数据老了）、
 * 新鲜。把它们说成同一句话（例如都叫「无数据」）会让运维失去唯一的判据。
 */
export function syncText(stale: boolean, ageSeconds: number, neverReported: boolean): string {
  if (neverReported) return '尚未收到该主机的数据'
  if (stale) return `数据陈旧 ${Math.max(1, Math.ceil(ageSeconds / 60))} 分钟`
  if (ageSeconds <= 1) return '刚刚同步'
  if (ageSeconds < 60) return `同步于 ${ageSeconds} 秒前`
  return `同步于 ${Math.floor(ageSeconds / 60)} 分钟前`
}

/** 陈旧时的样式类（琥珀色提示）。 */
export function staleClass(stale: boolean): string {
  return stale ? 'docker-sync--warn' : ''
}

/** 主机下拉的显示名：主机名 → 地址 → 「主机 <id>」。 */
export function hostLabel(host: Pick<DockerHostItem, 'id' | 'hostname' | 'primaryIp'>): string {
  return host.hostname || host.primaryIp || `主机 ${host.id}`
}
