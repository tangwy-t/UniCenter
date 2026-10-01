/**
 * 项目快照的派生口径（5b 项目工作台）：容器 → 网元（服务）行、项目容器子集。
 *
 * ── 为什么快照里没有现成的服务清单 ──────────────────────────────────
 * 快照的项目条目只带数量（services），服务名只挂在容器的 composeProject /
 * composeService 标签上。故服务行从这两枚标签归纳，「容器 N/M」由同一份容器算出 ——
 * 不额外发明判断。
 *
 * ── 与列表页的关系（漂移防线）────────────────────────────────────────
 * 这份逻辑逐行平移自 views/projects.vue 的 servicesByProject / projectContainers
 * 两个 computed。列表页按「本切片只加入口、不重构列表页」的纪律保留自己的副本 ——
 * 改动语义时**两处必须同步**（后续收敛另议，见切片 5b 的说明）。
 */
import type { DockerContainerItem } from '../api'

export interface ProjectServiceRow {
  name: string
  /** 运行中的容器数 / 容器总数。 */
  running: number
  total: number
  /**
   * 服务级保护由**该服务下容器的 protected** 承载（项目条目的 protected 只到
   * `project:<名>` 粒度）。取不到容器就不显示锁，不自己发明判断。
   */
  protected: boolean
  containers: DockerContainerItem[]
}

/**
 * 项目名 → 该项目的容器（按 composeProject 标签归纳，裸容器不进项目视图）。
 */
export function projectContainersOf(
  containers: DockerContainerItem[],
  project: string
): DockerContainerItem[] {
  return containers.filter((c) => c.composeProject === project)
}

/**
 * 项目名 → 网元行（按 composeService 归纳，按名字排序保证卡片区的顺序稳定）。
 */
export function deriveServiceRows(
  containers: DockerContainerItem[],
  project: string
): ProjectServiceRow[] {
  const rows: ProjectServiceRow[] = []
  for (const c of projectContainersOf(containers, project)) {
    if (!c.composeService) continue
    let row = rows.find((s) => s.name === c.composeService)
    if (!row) {
      row = { name: c.composeService, running: 0, total: 0, protected: false, containers: [] }
      rows.push(row)
    }
    row.containers.push(c)
    row.total += 1
    if (c.state === 'running') row.running += 1
    if (c.protected) row.protected = true
  }
  rows.sort((a, b) => a.name.localeCompare(b.name))
  return rows
}
