/** 快照的筛选与合计（纯函数）。 */
import type {
  DockerContainerItem,
  DockerImageItem,
  DockerImageListResp,
  DockerNetworkItem,
  DockerVolumeItem
} from '../api'

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

export interface VolumeQuery {
  keyword?: string
  unusedOnly?: boolean
}

/** 卷筛选：名称模糊 + 未被任何容器使用。 */
export function filterVolumes(list: DockerVolumeItem[], q: VolumeQuery): DockerVolumeItem[] {
  const kw = q.keyword?.trim().toLowerCase()
  return list.filter((v) => {
    if (q.unusedOnly && v.inUse) return false
    if (kw && !v.name.toLowerCase().includes(kw)) return false
    return true
  })
}

export interface NetworkQuery {
  keyword?: string
  internalOnly?: boolean
}

/**
 * 网络筛选：名称模糊 + 仅内部网络。
 *
 * 按名称搜而不是按 id：`docker network ls` 里人认的就是名字（默认网络 `bridge`
 * 等也是名字），id 只用于命令行。
 */
export function filterNetworks(list: DockerNetworkItem[], q: NetworkQuery): DockerNetworkItem[] {
  const kw = q.keyword?.trim().toLowerCase()
  return list.filter((n) => {
    if (q.internalOnly && !n.internal) return false
    if (kw && !n.name.toLowerCase().includes(kw)) return false
    return true
  })
}

/**
 * 镜像合计（列表底部主行）：总数 / 总大小。
 *
 * 总大小是 Σ 条目 SizeMB（共享层按引用它的镜像重复计入 —— 与 `docker system df`
 * 的层存储口径不同），它是「眼前这批行」的入口数字（合计跟着清单走，截断的口径
 * 由表头计数另行说明）。
 *
 * 可回收（悬空）账**不在这里算**：旧实现取 Σ 悬空行 SizeMB，既重复计入共享层、
 * 又承诺 prune 释放不了的空间（实测承诺 3.04GB、prune 只回收 2052 字节），已改读
 * 后端的主机级账目（DockerImageListResp.disk），见 imageReclaimTotals。
 */
export function imageTotals(list: DockerImageItem[]) {
  let totalMB = 0
  for (const i of list) {
    totalMB += i.sizeMb ?? 0
  }
  return { count: list.length, totalMB }
}

/**
 * 可回收镜像合计（列表底部副行）：后端主机级账目（disk 数组）的求和。
 *
 * 每一项是一台主机「执行 image:prune 真会释放」的账（悬空镜像的独占层之和，
 * df 对账口径）—— 与可见行无关：keyword/dangling/unused 过滤只过筛行、500 条
 * 截断只砍行，都影响不到账目（账目跟着**主机范围**走：服务端按 hostId 参数收窄，
 * 选了某台 → 数组只剩那台；未筛选 → 全部）。
 *
 * 空数组 = 没有任何主机的账目可用（无 df 数据 / 无可管主机）—— 返回 null：调用
 * 方必须说「不可用」而不是折算成 0（「没有数据」与「没有可回收」是两个相反的
 * 结论，与总览磁盘 KPI 的「—」同一条纪律）。
 */
export function imageReclaimTotals(disk: DockerImageListResp['disk'] | undefined) {
  if (!disk || disk.length === 0) return null
  let count = 0
  let mb = 0
  for (const d of disk) {
    count += d.danglingCount
    mb += d.danglingMb
  }
  return { count, mb }
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
