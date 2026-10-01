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

/**
 * 头部同步状态文案（含快照拉取失败，D-1 的修复口径）。
 *
 * 为什么失败要压过新鲜度结论：拉取失败时快照可能是 null（从未拿到）也可能是上一次
 * 成功的旧值 —— 无论哪种，都**不能**回落到「刚刚同步」：那会把「网络失败」渲染成
 * 「数据最新鲜」，运维唯一的时间判据就没了。规则：
 *   - 失败且没有快照 → 失败结论句（页头本来就有刷新按钮，点了即重试）；
 *   - 失败但还有上一次的快照 → 保留基于该快照的同步结论（ageSeconds 是上次成功
 *     拉取时服务端算好的，即「上次」的口径），再标注本次刷新失败；
 *   - 没有失败 → 三句结论原样交给 {@link syncText}（三种不同的事实，不能合并）。
 *
 * 与另外两条独立路径不混淆：主机离线（Agent 离线，数据为最后已知状态）与
 * dockerOk=false 的整页空态各有各的文案，不从这里出。
 */
export function syncTextWithError(
  loadError: boolean,
  hasState: boolean,
  stale: boolean,
  ageSeconds: number,
  neverReported: boolean
): string {
  if (!loadError) return syncText(stale, ageSeconds, neverReported)
  if (!hasState) return '数据获取失败'
  return `${syncText(stale, ageSeconds, neverReported)}，本次刷新失败`
}

/** 陈旧时的样式类（琥珀色提示）。 */
export function staleClass(stale: boolean): string {
  return stale ? 'docker-sync--warn' : ''
}

/** 主机下拉的显示名：主机名 → 地址 → 「主机 <id>」。 */
export function hostLabel(host: Pick<DockerHostItem, 'id' | 'hostname' | 'primaryIp'>): string {
  return host.hostname || host.primaryIp || `主机 ${host.id}`
}
