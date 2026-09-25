import { describe, expect, it } from 'vitest'
import { containerStateText, layersTotalMB, memText, portsText } from '../utils/display'
import type { DockerContainerItem } from '../api'

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
