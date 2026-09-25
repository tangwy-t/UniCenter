/** 快照的筛选与合计（纯函数）。 */
import type { DockerContainerItem, DockerImageItem, DockerVolumeItem } from '../api'

export interface ContainerQuery {
  keyword?: string
  state?: string
  runningOnly?: boolean
}

/** 容器筛选：名称/镜像模糊 + 状态精确 + 仅运行中。 */
export function filterContainers(
  list: DockerContainerItem[],
  q: ContainerQuery
): DockerContainerItem[] {
  const kw = q.keyword?.trim().toLowerCase()
  return list.filter((c) => {
    if (q.runningOnly && c.state !== 'running') return false
    if (q.state && c.state !== q.state) return false
    if (kw && !`${c.name} ${c.image}`.toLowerCase().includes(kw)) return false
    return true
  })
}

export interface ImageQuery {
  keyword?: string
  danglingOnly?: boolean
  unusedOnly?: boolean
}

/** 镜像筛选：仓库名模糊 + 悬空 + 未被使用。 */
export function filterImages(list: DockerImageItem[], q: ImageQuery): DockerImageItem[] {
  const kw = q.keyword?.trim().toLowerCase()
  return list.filter((i) => {
    if (q.danglingOnly && !i.dangling) return false
    if (q.unusedOnly && i.inUse) return false
    if (kw && !(i.repoTags ?? []).join(' ').toLowerCase().includes(kw)) return false
    return true
  })
}

/** 镜像合计（列表底部）：总数/总大小/悬空数/悬空大小。 */
export function imageTotals(list: DockerImageItem[]) {
  let totalMB = 0
  let danglingMB = 0
  let danglingCount = 0
  for (const i of list) {
    totalMB += i.sizeMb ?? 0
    if (i.dangling) {
      danglingCount++
      danglingMB += i.sizeMb ?? 0
    }
  }
  return { count: list.length, totalMB, danglingCount, danglingMB }
}

/** 卷合计：未知用量的卷**只计数不计入大小**（把未知当 0 会让合计悄悄偏小）。 */
export function volumeTotals(list: DockerVolumeItem[]) {
  let totalMB = 0
  let unknownSizeCount = 0
  let unusedCount = 0
  for (const v of list) {
    if (v.sizeMb === undefined || v.sizeMb === null) unknownSizeCount++
    else totalMB += v.sizeMb
    if (!v.inUse) unusedCount++
  }
  return { count: list.length, totalMB, unknownSizeCount, unusedCount }
}
