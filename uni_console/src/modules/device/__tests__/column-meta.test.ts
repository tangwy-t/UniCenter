import { describe, expect, it } from 'vitest'
import {
  METRIC_GROUP_ORDER,
  groupMetricColumns,
  metricGroup,
  metricLabel,
  metricMeta,
  metricUnit
} from '../utils/column-meta'

/**
 * 这些用例钉住的核心是**前后端分工边界**：
 *   后端 `available_metrics` 决定「有没有这一列」，
 *   本表只决定「这一列叫什么」。
 * 因此「未登记列必须回退原名」是最重要的一条 —— 它保证后端新增列时
 * 前端不会把它藏起来。
 */
describe('metricMeta 回退语义', () => {
  it('未登记的列回退为「原名 + 无单位 + 其他组」', () => {
    const m = metricMeta('brand_new_metric_col')
    expect(m.label).toBe('brand_new_metric_col')
    expect(m.unit).toBe('')
    expect(m.group).toBe('其他')
  })

  it('未登记列不返回空 label（否则图例会出现空白项）', () => {
    expect(metricLabel('some_unknown')).toBe('some_unknown')
    expect(metricLabel('some_unknown')).not.toBe('')
  })

  it('metricUnit 未登记时为空串而不是 undefined', () => {
    expect(metricUnit('some_unknown')).toBe('')
  })
})

describe('已登记列的元数据', () => {
  it('整机宽表列有中文名与单位', () => {
    expect(metricLabel('cpu_used_percent')).toBe('CPU 使用率')
    expect(metricUnit('cpu_used_percent')).toBe('%')
    expect(metricGroup('cpu_used_percent')).toBe('CPU')
  })

  it('下钻子表列名与宽表列名**不同名**且都要覆盖', () => {
    // 两套列名是既有事实（宽表 cpu_used_percent / 子表 used_percent），
    // 只覆盖一套会让下钻面板显示英文列名。
    expect(metricLabel('cpu_used_percent')).toBe('CPU 使用率')
    expect(metricLabel('used_percent')).toBe('使用率')
    expect(metricLabel('read_bytes_per_sec')).toBe('读速率')
    expect(metricLabel('nic_rx_bytes_sec')).toBe('网卡接收速率')
  })

  it('下钻白名单里的真实列名全部已登记', () => {
    // 取自 uni_core/internal/repository/device_metric.go 的 resourceTableColumns
    const drillColumns = [
      // disk
      'used_percent',
      'used_gb',
      'total_gb',
      'inodes_used_percent',
      // disk_io
      'read_bytes_per_sec',
      'write_bytes_per_sec',
      'read_ops_per_sec',
      'write_ops_per_sec',
      'io_time_percent',
      // nic
      'rx_bytes_per_sec',
      'tx_bytes_per_sec',
      'rx_packets_per_sec',
      'tx_packets_per_sec',
      'rx_errors_per_sec',
      'tx_errors_per_sec',
      'rx_dropped_per_sec',
      // sensor
      'temperature_c'
    ]
    for (const c of drillColumns) {
      // 未登记时 metricLabel 会回退原名，等于该列在下钻面板里显示英文
      expect(metricLabel(c), `${c} 未登记展示名`).not.toBe(c)
    }
  })
})

describe('groupMetricColumns', () => {
  it('按固定组序返回，且不含空组', () => {
    const groups = groupMetricColumns(['mem_used_percent', 'cpu_used_percent', 'nic_rx_bytes_sec'])
    // CPU 在内存之前（METRIC_GROUP_ORDER 的顺序），与传入顺序无关
    expect(groups.map((g) => g.group)).toEqual(['CPU', '内存', '网络'])
  })

  it('只传入一类时只返回一组', () => {
    const groups = groupMetricColumns(['used_percent', 'used_gb'])
    expect(groups).toHaveLength(1)
    expect(groups[0].group).toBe('磁盘')
    expect(groups[0].columns).toEqual(['used_percent', 'used_gb'])
  })

  it('组内保持传入顺序（后端 available_metrics 的顺序是稳定的）', () => {
    const input = ['load15', 'cpu_used_percent', 'load1']
    const groups = groupMetricColumns(input)
    const cpu = groups.find((g) => g.group === 'CPU')
    expect(cpu?.columns).toEqual(input)
  })

  it('该档位无此列时不会凭空造出资料', () => {
    // 「有没有这一列」由后端决定，本函数只负责归类
    expect(groupMetricColumns([])).toEqual([])
  })

  it('未登记列归入「其他」而不是被丢弃', () => {
    const groups = groupMetricColumns(['mystery_col'])
    expect(groups).toHaveLength(1)
    expect(groups[0].group).toBe('其他')
    expect(groups[0].columns).toEqual(['mystery_col'])
  })

  it('「其他」在组序中排最后', () => {
    expect(METRIC_GROUP_ORDER[METRIC_GROUP_ORDER.length - 1]).toBe('其他')
  })
})

describe('分组完整性', () => {
  it('所有已登记列的分组都在 METRIC_GROUP_ORDER 内', () => {
    // 分组不在枚举里会导致该列在下拉中**静默消失**（groupMetricColumns
    // 只按 METRIC_GROUP_ORDER 过滤）
    const columns = [
      'cpu_used_percent',
      'mem_used_percent',
      'disk_used_percent',
      'nic_rx_bytes_sec',
      'proc_count',
      'temperature_c',
      'used_percent'
    ]
    const groups = groupMetricColumns(columns)
    const covered = groups.flatMap((g) => g.columns)
    expect(covered.sort()).toEqual(columns.sort())
  })
})
