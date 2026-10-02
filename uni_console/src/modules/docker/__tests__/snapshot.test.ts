import { describe, expect, it } from 'vitest'
import {
  filterContainers,
  filterImages,
  filterNetworks,
  filterVolumes,
  imageReclaimTotals,
  imageTotals,
  volumeTotals
} from '../utils/snapshot'
import type {
  DockerContainerItem,
  DockerImageItem,
  DockerNetworkItem,
  DockerVolumeItem
} from '../api'

const c = (name: string, over: Partial<DockerContainerItem> = {}): DockerContainerItem =>
  ({
    id: name,
    name,
    image: 'img:1',
    state: 'running',
    statusText: 'Up 1h',
    cpuPercent: 1,
    memUsageMb: 10,
    memLimitMb: 100,
    netRxBytesSec: 0,
    netTxBytesSec: 0,
    protected: false,
    ...over
  }) as DockerContainerItem

describe('容器筛选', () => {
  const list = [
    c('uni-center-core', { composeProject: 'uni-center', protected: true, cpuPercent: 0.6 }),
    c('mysql', { composeProject: 'uni-center' }),
    c('zentao', { state: 'exited', statusText: 'Exited (0) 8 months ago' }),
    c('musing_bhabha')
  ]

  it('名称与镜像模糊匹配（大小写不敏感）', () => {
    expect(filterContainers(list, { keyword: 'MYSQL' }).map((x) => x.name)).toEqual(['mysql'])
    expect(filterContainers(list, { keyword: 'img' })).toHaveLength(4)
  })

  it('状态精确匹配；「仅运行中」只看 running', () => {
    expect(filterContainers(list, { state: 'exited' }).map((x) => x.name)).toEqual(['zentao'])
    expect(filterContainers(list, { runningOnly: true }).map((x) => x.name)).toHaveLength(3)
  })

  it('空条件返回原列表（不复制顺序）', () => {
    expect(filterContainers(list, {}).map((x) => x.name)).toEqual(list.map((x) => x.name))
  })
})

describe('镜像截图与筛选', () => {
  const list: DockerImageItem[] = [
    {
      id: 'sha256:a',
      repoTags: ['uni-center-core:latest'],
      sizeMb: 91.8,
      inUse: true,
      dangling: false
    },
    { id: 'sha256:b', repoTags: [], sizeMb: 1200, inUse: false, dangling: true },
    { id: 'sha256:c', repoTags: ['mysql:8.0.22'], sizeMb: 545, inUse: false, dangling: false }
  ]

  it('合计是本页的入口数字（「空间去哪了」）：总数与 Σ 条目 SizeMB', () => {
    const t = imageTotals(list)
    expect(t.count).toBe(3)
    expect(t.totalMB).toBeCloseTo(1836.8, 1)
  })

  it('可回收合计读后端账目：逐主机求和，不是 Σ 悬空行 SizeMB（共享层不承诺）', () => {
    // 两条账目故意与 list 的悬空行（1200MB）不同：账目是后端 df 对账的独占层
    //（prune 真会释放的量），前端只做求和。
    const t = imageReclaimTotals([
      { hostId: 'h1', danglingCount: 1, danglingMb: 0.002 },
      { hostId: 'h2', danglingCount: 2, danglingMb: 12.5 }
    ])
    expect(t?.count).toBe(3)
    expect(t?.mb).toBeCloseTo(12.502, 3)
  })

  it('没有任何主机的账目（空数组/字段缺席）→ null：说「不可用」而不是折算成 0', () => {
    expect(imageReclaimTotals([])).toBeNull()
    expect(imageReclaimTotals(undefined)).toBeNull()
  })

  it('按仓库名搜、按悬空/未使用筛', () => {
    expect(filterImages(list, { keyword: 'mysql' }).map((x) => x.id)).toEqual(['sha256:c'])
    expect(filterImages(list, { danglingOnly: true }).map((x) => x.id)).toEqual(['sha256:b'])
    expect(filterImages(list, { unusedOnly: true }).map((x) => x.id)).toEqual([
      'sha256:b',
      'sha256:c'
    ])
  })

  it('卷的合计把未知用量排除在大小之外（只计数）', () => {
    const vols: DockerVolumeItem[] = [
      { name: 'a', sizeMb: 10, inUse: true, protected: false },
      { name: 'b', inUse: false, protected: false }, // 用量未知
      { name: 'c', sizeMb: 5, inUse: false, protected: false }
    ]
    const t = volumeTotals(vols)
    expect(t.count).toBe(3)
    expect(t.totalMB).toBe(15)
    expect(t.unknownSizeCount).toBe(1)
    expect(t.unusedCount).toBe(2)
  })
})

describe('卷与网络的筛选', () => {
  // 大小未知（null / 缺省）的卷也要能搜能筛 —— 筛选只看名称与使用状态。
  const vols: DockerVolumeItem[] = [
    {
      name: 'uni-center_uploads',
      driver: 'local',
      sizeMb: 12,
      inUse: true,
      mountedBy: ['core'],
      protected: true
    },
    { name: 'uni-center_mysql-data', driver: 'local', sizeMb: null, inUse: true, protected: false },
    { name: 'orphan-vol', driver: 'local', inUse: false, protected: false }
  ]
  const nets: DockerNetworkItem[] = [
    { name: 'bridge', driver: 'bridge', scope: 'local', internal: false, containersCount: 2 },
    {
      name: 'uni-center_default',
      driver: 'bridge',
      scope: 'local',
      internal: false,
      containersCount: 2
    },
    { name: 'internal-net', driver: 'bridge', scope: 'local', internal: true, containersCount: 0 }
  ]

  it('卷按名称搜（大小写不敏感）、按未使用筛', () => {
    expect(filterVolumes(vols, { keyword: 'MYSQL' }).map((x) => x.name)).toEqual([
      'uni-center_mysql-data'
    ])
    expect(filterVolumes(vols, { unusedOnly: true }).map((x) => x.name)).toEqual(['orphan-vol'])
    expect(filterVolumes(vols, {})).toHaveLength(3)
  })

  it('网络按名称搜、按仅内部网络筛', () => {
    expect(filterNetworks(nets, { keyword: 'uni-center' }).map((x) => x.name)).toEqual([
      'uni-center_default'
    ])
    expect(filterNetworks(nets, { internalOnly: true }).map((x) => x.name)).toEqual([
      'internal-net'
    ])
    expect(filterNetworks(nets, {})).toHaveLength(3)
  })
})
