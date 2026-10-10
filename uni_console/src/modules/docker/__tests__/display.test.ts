import { describe, expect, it } from 'vitest'
import {
  containerStateText,
  containerStateZh,
  cpuText,
  formatRelativeTime,
  inUseText,
  layersTotalMB,
  memText,
  memUsage,
  midEllipsis,
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

  it('中文状态句：状态码本地化 + 时长/health 从原生串解析（Wave 1 守卫）', () => {
    // 运行中：Up <时长> (health) —— 英文时长与括号里的 health 都译成中文补充。
    expect(containerStateZh(c({ state: 'running', statusText: 'Up 32 hours (healthy)' }))).toBe(
      '运行中 32 小时（健康）'
    )
    expect(containerStateZh(c({ state: 'running', statusText: 'Up 2 hours (unhealthy)' }))).toBe(
      '运行中 2 小时（异常）'
    )
    expect(
      containerStateZh(c({ state: 'running', statusText: 'Up 5 seconds (health: starting)' }))
    ).toBe('运行中 5 秒（健康检查中）')
    // 没有 health 段时只给「运行中 <时长>」。
    expect(containerStateZh(c({ state: 'running', statusText: 'Up 3 minutes' }))).toBe(
      '运行中 3 分钟'
    )
    // 已退出：退出码与「N 小时前」都保留（退出码是判因的第一条线索）。
    expect(containerStateZh(c({ state: 'exited', statusText: 'Exited (143) 26 hours ago' }))).toBe(
      '已退出（退出码 143）26 小时前'
    )
    expect(containerStateZh(c({ state: 'exited', statusText: 'Exited (0) 8 months ago' }))).toBe(
      '已退出（退出码 0）8 月前'
    )
    // 非 0 退出码同样如实给出（不能只报 0）。
    expect(
      containerStateZh(c({ state: 'exited', statusText: 'Exited (1) About an hour ago' }))
    ).toBe('已退出（退出码 1）约 1 小时前')
    // 其余状态码直接本地化。
    expect(containerStateZh(c({ state: 'created', statusText: 'Created' }))).toBe('已创建')
    expect(containerStateZh(c({ state: 'paused' }))).toBe('已暂停')
    expect(containerStateZh(c({ state: 'restarting' }))).toBe('重启中')
    expect(containerStateZh(c({ state: 'removing' }))).toBe('移除中')
    expect(containerStateZh(c({ state: 'dead' }))).toBe('已死亡')
    // 认不出的状态码原样返回（不编措辞）；状态为空给「—」。
    expect(containerStateZh(c({ state: 'weird' }))).toBe('weird')
    expect(containerStateZh(c({ state: '' }))).toBe('—')
  })

  it('非运行容器的用量显示「—」而不是 0（0% 会被当成「真的很闲」）', () => {
    expect(memText(c({ state: 'exited' }))).toBe('—')
    expect(memText(c({ memUsageMb: 91, memLimitMb: 1024 }))).toContain('91')
  })

  it('内存文案「已用 / 上限」（Wave 3a：单位 MB 进列头，正文只留数值）', () => {
    expect(memText(c({ memUsageMb: 1536, memLimitMb: 2048 }))).toBe('1536 / 2048')
    // 缺分母（老 agent / limit 为 0）只给已用，不编一个假分母。
    expect(memText(c({ memUsageMb: 512, memLimitMb: 0 }))).toBe('512')
  })

  it('CPU 文案只留数值（单位 % 进列头；未运行「—」）', () => {
    expect(cpuText(c({ cpuPercent: 12.34 }))).toBe('12.3')
    expect(cpuText(c({ state: 'exited' }))).toBe('—')
  })

  it('内存分解给 meter 用（未运行 null —— 缺值画「—」不画空条；分母缺省 pct 记 0）', () => {
    expect(memUsage(c({ state: 'exited' }))).toBeNull()
    const u = memUsage(c({ memUsageMb: 91, memLimitMb: 1024 }))
    expect(u).toMatchObject({ usedMb: 91, limitMb: 1024, pct: 9 })
    expect(memUsage(c({ memUsageMb: 91, memLimitMb: 0 }))!.pct).toBe(0)
  })

  it('中段省略：超长时保留头 + 尾、切掉中间（保住 tag 与 /tcp）', () => {
    const ref = 'quay.io/minio/minio:RELEASE.2024-01-16T22-43-05Z'
    const out = midEllipsis(ref, 20, 12)
    expect(out.startsWith('quay.io/minio/minio:')).toBe(true)
    expect(out.endsWith('16T22-43-05Z')).toBe(true)
    expect(out).toContain('…')
    expect(out.length).toBeLessThan(ref.length)

    const longPort = '20080 → 6379/tcp +7'
    expect(midEllipsis(longPort, 6, 8).endsWith('/tcp +7')).toBe(true)
  })

  it('中段省略：未超预算时原样返回（不打省略号）', () => {
    expect(midEllipsis('mysql:8.0', 12, 8)).toBe('mysql:8.0')
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
  it('相对时间分四档（分钟/小时/天/月）', () => {
    const now = 1_700_000_000
    expect(formatRelativeTime(now - 90, now)).toBe('1 分钟前')
    expect(formatRelativeTime(now - 3600 * 5, now)).toBe('5 小时前')
    expect(formatRelativeTime(now - 86400 * 3, now)).toBe('3 天前')
    expect(formatRelativeTime(now - 86400 * 30 * 4, now)).toBe('4 月前')
  })

  it('异常时间戳给稳定兜底，绝不渲染成巨大月数（Wave 1 守卫）', () => {
    const now = 1_700_000_000
    // Go 零时刻哨兵（0001-01-01）——反算 diff 恰好会得到「24663 月前」这个荒唐字面。
    const GO_ZERO = -62135596800
    expect(formatRelativeTime(GO_ZERO, now)).toBe('未知')
    expect(formatRelativeTime(GO_ZERO, now)).not.toContain('月')
    // 非正（epoch 之前 / 0）与未来（时钟偏差或坏值）同样兜底，不出负数也不出巨大月数。
    expect(formatRelativeTime(0, now)).toBe('未知')
    expect(formatRelativeTime(-1, now)).toBe('未知')
    expect(formatRelativeTime(now + 600, now)).toBe('未知')
    expect(formatRelativeTime(Number.NaN, now)).toBe('未知')
    expect(formatRelativeTime(Number.POSITIVE_INFINITY, now)).toBe('未知')
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
