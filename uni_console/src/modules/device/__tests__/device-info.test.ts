import { describe, expect, it } from 'vitest'
// 纯逻辑在 utils/ 下，可脱 DOM 单测 —— 不 import `.vue`（会连带求值 store 与
// localStorage，正是本项目既有约定把逻辑放 utils 的原因）。
import {
  countFilled,
  countFilledAll,
  countTotal,
  deviceInfoGroups,
  filterFilledGroups
} from '../utils/device-info'
import { EMPTY_TEXT } from '../utils/display'

/** 造一个最小可用的 DeviceResp（字段全部可选，故只给关心的项）。 */
function device(over: Partial<Api.Device.DeviceResp> = {}): Api.Device.DeviceResp {
  return {
    id: '2100492127631839232',
    hostname: 'e2e-probe-host',
    os: 'linux',
    arch: 'amd64',
    agentVersion: '0.1.0-live',
    status: 1,
    online: true,
    ...over
  } as Api.Device.DeviceResp
}

describe('deviceInfoGroups', () => {
  it('分成系统 / 硬件 / 运行 / 登记四组', () => {
    const groups = deviceInfoGroups(device())
    expect(groups.map((g) => g.name)).toEqual(['系统', '硬件', '运行', '登记'])
  })

  it('缺值一律是「—」而不是空串或 0', () => {
    // 这是全页最重要的约定：0 核 CPU 与「没上报」在排障时指向完全相反的结论
    const groups = deviceInfoGroups(device())
    const all = groups.flatMap((g) => g.items)
    // 只给了 os/arch/id，其余应为占位符
    const os = all.find((i) => i.label === '操作系统')
    expect(os?.value).toBe('linux')
    for (const item of all) {
      expect(item.value, `${item.label} 不得为空串`).not.toBe('')
    }
  })

  it('device 为 null / undefined 时不抛异常且全部为占位符', () => {
    for (const d of [null, undefined]) {
      const groups = deviceInfoGroups(d)
      const all = groups.flatMap((g) => g.items)
      expect(all.every((i) => i.value === EMPTY_TEXT)).toBe(true)
    }
  })

  it('cpuCores=0 视为缺值（0 核不是有效值）', () => {
    const groups = deviceInfoGroups(device({ cpuCores: 0 }))
    const cores = groups.flatMap((g) => g.items).find((i) => i.label === 'CPU 核数')
    expect(cores?.value).toBe(EMPTY_TEXT)
  })

  it('cpuCores 有值时带「核」单位', () => {
    const groups = deviceInfoGroups(device({ cpuCores: 8 }))
    const cores = groups.flatMap((g) => g.items).find((i) => i.label === 'CPU 核数')
    expect(cores?.value).toBe('8 核')
  })

  it('I-5：在线判定阈值来自后端字段，缺省时显示占位符', () => {
    const withTh = deviceInfoGroups(device({ offlineThresholdSec: 60 }))
      .flatMap((g) => g.items)
      .find((i) => i.label === '在线判定')
    expect(withTh?.value).toBe('60 秒内未上报即离线')

    // 后端未下发时**不能**编一个 30 秒出来：阈值可热更，写死会与后端静默矛盾
    const noTh = deviceInfoGroups(device())
      .flatMap((g) => g.items)
      .find((i) => i.label === '在线判定')
    expect(noTh?.value).toBe(EMPTY_TEXT)
  })

  it('I-1：设备 IP 来自 primaryIp，且标为可复制', () => {
    const ip = deviceInfoGroups(device({ primaryIp: '203.0.113.7' }))
      .flatMap((g) => g.items)
      .find((i) => i.label === '设备 IP')
    expect(ip?.value).toBe('203.0.113.7')
    expect(ip?.copyable).toBe(true)
  })

  it('磁盘挂载点计数：未传入时为占位符，传入 0 时显示「0 个」', () => {
    const none = deviceInfoGroups(device())
      .flatMap((g) => g.items)
      .find((i) => i.label === '磁盘挂载点')
    expect(none?.value).toBe(EMPTY_TEXT)

    const zero = deviceInfoGroups(device(), { diskCount: 0 })
      .flatMap((g) => g.items)
      .find((i) => i.label === '磁盘挂载点')
    // 0 个挂载点是**真实统计结果**（查询成功了），与「没查」不同
    expect(zero?.value).toBe('0 个')
  })

  it('不含任何臆造字段（只暴露后端 DTO 里真实存在的项）', () => {
    // 防止后续有人往信息表里加「厂商/机架/负责人」这类后端没有的字段
    const labels = deviceInfoGroups(device())
      .flatMap((g) => g.items)
      .map((i) => i.label)
    expect(labels).toEqual([
      '操作系统',
      '架构',
      '平台',
      '平台版本',
      '内核',
      'CPU 型号',
      'CPU 核数',
      '内存总量',
      '磁盘挂载点',
      '运行时长',
      '开机时间',
      '最后上报',
      '水位采样',
      '在线判定',
      '录入时间',
      '设备 IP',
      '设备 ID'
    ])
  })
})

describe('countFilled', () => {
  it('统计有值项数', () => {
    expect(
      countFilled({
        name: 'x',
        items: [
          { label: 'a', value: '1' },
          { label: 'b', value: EMPTY_TEXT }
        ]
      })
    ).toBe(1)
  })

  it('全空时为 0', () => {
    expect(countFilled({ name: 'x', items: [{ label: 'a', value: EMPTY_TEXT }] })).toBe(0)
  })
})

describe('filterFilledGroups（隐藏空字段）', () => {
  it('整组为空时整组消失', () => {
    const groups = deviceInfoGroups({ id: '1', hostname: 'h', os: 'linux' })
    const filtered = filterFilledGroups(groups)
    // 只填了 os（与 id），硬件组应整体消失 —— 只留一个「硬件」空标题更让人困惑
    expect(filtered.map((g) => g.name)).not.toContain('硬件')
  })

  it('不修改原数组（computed 会被重复求值）', () => {
    const groups = deviceInfoGroups({ id: '1', hostname: 'h', os: 'linux' })
    const before = JSON.stringify(groups)
    filterFilledGroups(groups)
    expect(JSON.stringify(groups)).toBe(before)
  })

  it('全部为空时返回空数组', () => {
    expect(filterFilledGroups(deviceInfoGroups(null))).toEqual([])
  })
})

describe('计数辅助', () => {
  it('countFilledAll / countTotal 反映真实缺失数量', () => {
    // 与线上实测一致：linxu 设备只有 os/arch/agentVersion，其余大面积缺失
    const groups = deviceInfoGroups({ id: '1', os: 'linux', arch: 'amd64' })
    const total = countTotal(groups)
    const filled = countFilledAll(groups)
    expect(total).toBe(17)
    expect(filled).toBe(3)
    expect(total - filled).toBe(14)
  })

  it('全填时缺失为 0', () => {
    const groups = deviceInfoGroups(
      {
        id: '1',
        os: 'linux',
        arch: 'amd64',
        platform: 'ubuntu',
        platformVer: '22.04',
        kernel: '5.15',
        cpuModel: 'Xeon',
        cpuCores: 8,
        memTotalMb: 16384,
        bootTime: 1758000000,
        lastSeenAt: 1758000000,
        watermarkAt: 1758000000,
        createdAt: 1758000000,
        primaryIp: '10.0.0.1',
        offlineThresholdSec: 30
      },
      { diskCount: 3 }
    )
    expect(countTotal(groups) - countFilledAll(groups)).toBe(0)
  })
})
