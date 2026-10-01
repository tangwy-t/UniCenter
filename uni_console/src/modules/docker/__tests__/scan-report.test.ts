/**
 * P3·安全面纯逻辑测试：扫描报告载荷的解析（snake_case 镜像 → camelCase 视图）、
 * severity 归一化（白名单外折「未知」）、全量计数、截断口径的常量镜像。
 *
 * 报告线上形状的事实源是 uni_protocol/docker.go 的 DockerScanReport（json tag
 * snake_case；core 原样透传，缓存回放同形）；组件把哪条数据接到哪个控件在
 * image-scan-panel.test.ts（jsdom）。
 */
import { describe, expect, it } from 'vitest'
import {
  MAX_SCAN_VULN_ENTRIES,
  parseDockerScanReport,
  scanSeverityClass,
  scanSeverityLabel,
  scanTotalCount,
  SCAN_SEVERITIES,
  type DockerScanCountsView
} from '../utils/scan'

/** 一份最小合法报告（字段名用线上 snake_case —— 这正是被测的那一层）。 */
function reportOf(over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    image_id: 'sha256:aaaa1111bbbb2222cccc3333dddd4444eeee5555ffff66667777888899990000',
    scanned_at: 1790600000,
    counts: { critical: 2, high: 3, medium: 5, low: 7, unknown: 1 },
    vulns: [
      {
        id: 'CVE-2023-1234',
        pkg: 'openssl',
        severity: 'critical',
        fixed_version: '3.0.12',
        title: 'OpenSSL vulnerability'
      },
      { id: 'CVE-2024-5678', pkg: 'bash', severity: 'low' }
    ],
    truncated: false,
    ...over
  }
}

describe('parseDockerScanReport：线上形状 → 视图', () => {
  it('完整报告：snake_case 键名逐字段映射（fixed_version/scanned_at/image_id）', () => {
    const r = parseDockerScanReport(reportOf())!
    expect(r).toBeTruthy()
    expect(r.imageId).toBe(
      'sha256:aaaa1111bbbb2222cccc3333dddd4444eeee5555ffff66667777888899990000'
    )
    expect(r.scannedAt).toBe(1790600000)
    expect(r.counts).toEqual({ critical: 2, high: 3, medium: 5, low: 7, unknown: 1 })
    expect(r.vulns).toHaveLength(2)
    expect(r.vulns[0]).toEqual({
      id: 'CVE-2023-1234',
      pkg: 'openssl',
      severity: 'critical',
      fixedVersion: '3.0.12',
      title: 'OpenSSL vulnerability'
    })
    // 无修复档：fixed_version 缺席 =「修复未发布/不适用」，视图给空串（缺席本身是信息）。
    expect(r.vulns[1]).toEqual({
      id: 'CVE-2024-5678',
      pkg: 'bash',
      severity: 'low',
      fixedVersion: '',
      title: ''
    })
    expect(r.truncated).toBe(false)
  })

  it('truncated 如实透传（计数全量 + 条目只有前 500 —— 两者不同正是要披露的）', () => {
    const r = parseDockerScanReport(reportOf({ truncated: true }))!
    expect(r.truncated).toBe(true)
  })

  it('severity 归一化：白名单五档直过；空串/大小写漂移/新档/类型错都折 unknown', () => {
    const r = parseDockerScanReport(
      reportOf({
        vulns: [
          { id: '1', pkg: 'a', severity: 'high' },
          { id: '2', pkg: 'a', severity: '' },
          { id: '3', pkg: 'a', severity: 'CRITICAL' },
          { id: '4', pkg: 'a', severity: 'extreme' },
          { id: '5', pkg: 'a', severity: 7 },
          { id: '6', pkg: 'a' }
        ]
      })
    )!
    expect(r.vulns.map((v) => v.severity)).toEqual([
      'high',
      'unknown',
      'unknown',
      'unknown',
      'unknown',
      'unknown'
    ])
  })

  it('计数归一化：缺席/非数/负数当 0（计数是行动依据，坏一档不给 NaN）', () => {
    const r = parseDockerScanReport(reportOf({ counts: { critical: 4, high: 'x', medium: -1 } }))!
    expect(r.counts).toEqual({ critical: 4, high: 0, medium: 0, low: 0, unknown: 0 })
  })

  it('条目字段的类型兜底：坏字段给空串而不是抛错（页面不因一行坏数据白屏）', () => {
    const r = parseDockerScanReport(
      reportOf({ vulns: [{ id: 9, pkg: null, severity: 'low', fixed_version: 1, title: false }] })
    )!
    expect(r.vulns[0]).toEqual({
      id: '',
      pkg: '',
      severity: 'low',
      fixedVersion: '',
      title: ''
    })
  })

  it('形状不认识时返回 null：null 载荷 / 非对象 / 缺 counts / scanned_at 非正数', () => {
    expect(parseDockerScanReport(null)).toBeNull()
    expect(parseDockerScanReport(undefined)).toBeNull()
    expect(parseDockerScanReport('x')).toBeNull()
    expect(parseDockerScanReport([1, 2])).toBeNull()
    expect(parseDockerScanReport(reportOf({ counts: undefined }))).toBeNull()
    expect(parseDockerScanReport(reportOf({ scanned_at: 0 }))).toBeNull()
    expect(parseDockerScanReport(reportOf({ scanned_at: 'x' }))).toBeNull()
    // vulns 缺席不算「不认识」（干净镜像可以没有条目字段之外的坏形状）：给空数组。
    expect(parseDockerScanReport(reportOf({ vulns: undefined }))!.vulns).toEqual([])
  })
})

describe('severity 展示元数据', () => {
  it('五档顺序 = severity 降序（计数行与表头的展示顺序共用这份事实）', () => {
    expect(SCAN_SEVERITIES.map((s) => s.key)).toEqual([
      'critical',
      'high',
      'medium',
      'low',
      'unknown'
    ])
    expect(SCAN_SEVERITIES.map((s) => s.label)).toEqual(['严重', '高危', '中危', '低危', '未知'])
  })

  it('档名与着色类名：白名单内映射，白名单外折「未知」', () => {
    expect(scanSeverityLabel('critical')).toBe('严重')
    expect(scanSeverityLabel('high')).toBe('高危')
    expect(scanSeverityLabel('whatever')).toBe('未知')
    expect(scanSeverityLabel('')).toBe('未知')
    expect(scanSeverityClass('high')).toBe('is-high')
    expect(scanSeverityClass('nonsense')).toBe('is-unknown')
  })
})

describe('计数口径', () => {
  it('scanTotalCount = 五档求和（截断时它与条目数不同 —— 「共 N 条」以它为准）', () => {
    const c: DockerScanCountsView = { critical: 3200, high: 0, medium: 12, low: 3, unknown: 0 }
    expect(scanTotalCount(c)).toBe(3215)
  })

  it('MAX_SCAN_VULN_ENTRIES 镜像协议常量 500（截断提示里的数与后端同一个数）', () => {
    expect(MAX_SCAN_VULN_ENTRIES).toBe(500)
  })
})
