import { describe, expect, it } from 'vitest'
import {
  containerStateText,
  formatRelativeTime,
  inUseText,
  layersTotalMB,
  memText,
  netText,
  portsText
} from '../utils/display'
import type { DockerContainerItem, DockerImageItem } from '../api'

const c = (over: Partial<DockerContainerItem> = {}): DockerContainerItem =>
  ({
    id: 'x',
    name: 'x',
    image: 'i',
    state: 'running',
    statusText: 'Up 16 hours',
    cpuPercent: 0,
    memUsageMb: 0,
    memLimitMb: 0,
    netRxBytesSec: 0,
    netTxBytesSec: 0,
    protected: false,
    ...over
  }) as DockerContainerItem

describe('容器状态与用量文案', () => {
  it('运行中显示原生状态句，停止的容器只说结论', () => {
    expect(containerStateText(c())).toBe('Up 16 hours')
    expect(containerStateText(c({ state: 'exited', statusText: 'Exited (0) 8 months ago' }))).toBe(
      'Exited (0) 8 months ago'
    )
  })

  it('非运行容器的用量显示「—」而不是 0（0% 会被当成「真的很闲」）', () => {
    expect(memText(c({ state: 'exited' }))).toBe('—')
    expect(memText(c({ memUsageMb: 91, memLimitMb: 1024 }))).toContain('91')
  })

  it('内存显示带换算单位（不出现裸字节数）', () => {
    expect(memText(c({ memUsageMb: 1536, memLimitMb: 2048 }))).toMatch(/GB|MB/)
  })

  it('端口按「宿主→容器」显示，无映射显示「—」', () => {
    expect(portsText([])).toBe('—')
    expect(portsText([{ privatePort: 8088, publicPort: 20080, type: 'tcp' }])).toBe(
      '20080 → 8088/tcp'
    )
    expect(portsText([{ privatePort: 3306, type: 'tcp' }])).toBe('3306/tcp')
  })

  it('多端口折叠为单行：首条 + 其余计数（列表行不再竖向堆高）', () => {
    expect(
      portsText([
        { privatePort: 80, publicPort: 8080, type: 'tcp' },
        { privatePort: 443, publicPort: 8443, type: 'tcp' },
        { privatePort: 53, type: 'udp' }
      ])
    ).toBe('8080 → 80/tcp +2')
    expect(
      portsText([
        { privatePort: 80, publicPort: 8080, type: 'tcp' },
        { privatePort: 443, type: 'tcp' }
      ])
    ).toBe('8080 → 80/tcp +1')
    // 首条无宿主映射时同样只留首条（折叠不改变「宿主→容器」的取数口径）。
    expect(
      portsText([
        { privatePort: 80, type: 'tcp' },
        { privatePort: 443, publicPort: 8443, type: 'tcp' }
      ])
    ).toBe('80/tcp +1')
  })

  it('网络文案自带速率单位，不再出现 B/s/s 双重后缀（投诉截图里的错字面）', () => {
    expect(netText(c({ netTxBytesSec: 10, netRxBytesSec: 2048 }))).toBe('↑10 B/s ↓2.0 KB/s')
    expect(netText(c({ state: 'exited' }))).toBe('—')
  })
})

describe('镜像分层合计', () => {
  it('空元数据层不计入合计（它们不占空间）', () => {
    const total = layersTotalMB([
      { sizeBytes: 12 * 1024 * 1024, createdBy: 'CMD' },
      { sizeBytes: 0, createdBy: 'ENV x=1', emptyLayer: true },
      { sizeBytes: 89 * 1024 * 1024, createdBy: 'ADD file' }
    ])
    expect(total).toBeCloseTo(101, 0)
  })
  it('无历史时返回 0（不产生 NaN）', () => {
    expect(layersTotalMB([])).toBe(0)
    expect(layersTotalMB(undefined)).toBe(0)
  })
})

const img = (over: Partial<DockerImageItem> = {}): DockerImageItem =>
  ({
    id: 'sha256:abc',
    repoTags: [],
    sizeMb: 10,
    inUse: false,
    dangling: false,
    ...over
  }) as DockerImageItem

describe('镜像使用状态与相对时间', () => {
  it('相对时间分四档（分钟/小时/天/月），未来时刻不说负数', () => {
    const now = 1_700_000_000
    expect(formatRelativeTime(now - 90, now)).toBe('1 分钟前')
    expect(formatRelativeTime(now - 3600 * 5, now)).toBe('5 小时前')
    expect(formatRelativeTime(now - 86400 * 3, now)).toBe('3 天前')
    expect(formatRelativeTime(now - 86400 * 30 * 4, now)).toBe('4 月前')
    // 时钟偏差：容器/镜像的时间来自远端主机，比浏览器快时绝不能显示「-N 分钟前」。
    expect(formatRelativeTime(now + 600, now)).toBe('1 分钟前')
  })

  it('「使用」列把悬空、未使用、在用说成三句不同的话（悬空就是可回收的那批）', () => {
    expect(inUseText(img({ dangling: true }))).toBe('可回收（无标签）')
    expect(inUseText(img({ inUse: false }))).toBe('未使用')
    expect(inUseText(img({ inUse: true }))).toBe('在用')
  })

  it('在用镜像带上占用它的容器名（清理前先看是谁在用）', () => {
    expect(inUseText(img({ inUse: true, inUseBy: ['mysql', 'redis'] }))).toBe(
      '在用（mysql、redis）'
    )
  })
})
