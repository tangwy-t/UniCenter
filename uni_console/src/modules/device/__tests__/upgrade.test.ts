import { describe, expect, it } from 'vitest'
import {
  availableVersions,
  formatSize,
  formatUnixTime,
  phaseText,
  reasonText,
  releaseStatusText,
  resultText,
  skipText,
  skipTotal,
  sourceText,
  stageText,
  stageView,
  taskCountsText,
  targetSourceText,
  upgradeBadge,
  UPGRADE_RESULT
} from '../utils/upgrade'

/**
 * 升级域展示层的测试。
 *
 * 重点不是「函数能跑」，而是三条纪律：
 *  1. **12 键原因码全覆盖**且未知码返回空串（不编造结论）；
 *  2. **进度三层**：只在下载阶段给百分比，其余阶段给阶段结论；
 *  3. 「等待上线 ≠ 升级中」这类**语义边界**（混起来运维会去等一台没开始的机器）。
 */

describe('原因码 → 结论', () => {
  it('协议白名单 9 个 + 服务端推导 3 个，共 12 键全覆盖', () => {
    const codes = [
      'download_failed',
      'checksum_mismatch',
      'smoke_test_failed',
      'no_write_permission',
      'replace_failed',
      'exec_failed',
      'not_connected_after_upgrade',
      'platform_unsupported',
      'artifact_missing',
      'timeout',
      'superseded',
      'unexpected_version'
    ]
    for (const code of codes) {
      expect(reasonText(code), `${code} 应有翻译`).not.toBe('')
    }
    expect(codes.length).toBe(12)
  })

  it('未知码返回空串（页面渲染成「—」，不编造「未知错误」）', () => {
    expect(reasonText('made_up')).toBe('')
    expect(reasonText('')).toBe('')
    expect(reasonText(null)).toBe('')
    expect(reasonText(undefined)).toBe('')
  })

  it('文案里不出现机器码本身', () => {
    for (const code of ['download_failed', 'no_write_permission', 'rolled_back']) {
      expect(reasonText(code)).not.toContain(code)
    }
  })
})

describe('相位与终态', () => {
  it('相位文案', () => {
    expect(phaseText('pending')).toBe('待升级')
    expect(phaseText('running')).toBe('升级中')
    expect(phaseText('achieved')).toBe('已达成')
    expect(phaseText('')).toBe('')
    expect(phaseText(undefined)).toBe('')
  })

  it('终态文案（0 = 无终态 → 空串）', () => {
    expect(resultText(UPGRADE_RESULT.achieved)).toBe('已达成')
    expect(resultText(UPGRADE_RESULT.failed)).toBe('失败')
    expect(resultText(UPGRADE_RESULT.rolledBack)).toBe('已回滚')
    expect(resultText(0)).toBe('')
    expect(resultText(null)).toBe('')
  })
})

describe('列表标记（upgradeBadge）', () => {
  it('无目标不显示标记（不伪造相位）', () => {
    expect(upgradeBadge({ agentVersion: '0.2.4' })).toBeNull()
  })

  it('不支持远程升级是明确的结论（不给按钮留悬念）', () => {
    const b = upgradeBadge({ targetVersion: '0.2.5', agentUpgradeSupported: false })
    expect(b?.text).toBe('不支持远程升级')
    expect(b?.hint).not.toBe('')
    expect(b?.hint).not.toContain('upgrade_supported')
  })

  it('相位优先于终态（当前状态比历史结论重要）', () => {
    expect(
      upgradeBadge({
        targetVersion: '0.2.5',
        agentUpgradeSupported: true,
        upgradePhase: 'running',
        upgradeResult: UPGRADE_RESULT.failed
      })?.text
    ).toBe('升级中')
    expect(
      upgradeBadge({
        targetVersion: '0.2.5',
        agentUpgradeSupported: true,
        upgradePhase: 'achieved',
        upgradeResult: UPGRADE_RESULT.failed
      })?.text
    ).toBe('已达成')
  })

  it('有终态无相位时给终态 + 原因（页面不必进详情就知道为什么）', () => {
    const b = upgradeBadge({
      targetVersion: '0.2.5',
      agentUpgradeSupported: true,
      upgradePhase: '',
      upgradeResult: UPGRADE_RESULT.rolledBack,
      upgradeReason: 'not_connected_after_upgrade'
    })
    expect(b?.text).toBe('已回滚')
    expect(b?.tone).toBe('danger')
    expect(b?.hint).toBe('升级后未能连上服务端，已自动回滚')
    expect(b?.hint).not.toContain('not_connected_after_upgrade')
  })
})

describe('阶段轨（进度三层）', () => {
  it('下载阶段带真实字节百分比', () => {
    const v = stageView('downloading', 62)
    expect(v.index).toBe(0)
    expect(v.progress).toBe(62)
    expect(v.description).toBe('下载中 62%')
  })

  it('进度缺失时只说「下载中」（0 与「没有百分比」是两件事）', () => {
    expect(stageView('downloading', null).progress).toBeNull()
    expect(stageView('downloading', null).description).toBe('下载中')
    expect(stageView('downloading', 0).progress).toBe(0)
  })

  it('其余阶段**不给百分比**，只给阶段结论（不造连续假进度）', () => {
    for (const [state, idx] of [
      ['verifying', 1],
      ['installing', 2],
      ['restarting', 3]
    ] as const) {
      const v = stageView(state)
      expect(v.index).toBe(idx)
      expect(v.progress).toBeNull()
      expect(v.description).not.toMatch(/\d+%/)
    }
  })

  it('等待上线 ≠ 升级中：pending 停在第 0 步之前', () => {
    const v = stageView('pending')
    expect(v.index).toBe(-1)
    expect(v.description).toBe('等待设备上线')
    expect(stageText(v)).toBe('')
  })

  it('失败与回滚保留原因结论，且与成功互斥', () => {
    const failed = stageView('failed', null, 'no_write_permission')
    expect(failed.failed).toBe(true)
    expect(failed.description).toBe('设备上没有写权限')
    const rolled = stageView('rolled_back', null, 'not_connected_after_upgrade')
    expect(rolled.rolledBack).toBe(true)
    expect(rolled.failed).toBe(false)
    const ok = stageView('succeeded')
    expect(ok.succeeded).toBe(true)
    expect(ok.description).toBe('已完成')
  })

  it('阶段轨有文字形式（读屏与色弱用户不能只靠图形）', () => {
    expect(stageText(stageView('verifying'))).toBe('第 2/4 步：校验')
    expect(stageText(stageView('restarting'))).toBe('第 4/4 步：重启')
  })
})

describe('可用版本过滤', () => {
  const releases = [
    { version: '0.2.0', os: 'linux', arch: 'amd64', status: 1 },
    { version: '0.2.0', os: 'linux', arch: 'arm64', status: 1 },
    { version: '0.3.0', os: 'linux', arch: 'amd64', status: 1 },
    { version: '0.4.0', os: 'linux', arch: 'amd64', status: 0 } // 草稿
  ]
  const published = ['0.2.0', '0.3.0', '0.4.0']

  it('只列「已发布 ∩ 所有选中平台都有产物」', () => {
    expect(availableVersions(published, releases, [{ os: 'linux', arch: 'amd64' }])).toEqual([
      '0.2.0',
      '0.3.0'
    ])
    // 选中设备里有 arm64：只有 0.2.0 两个平台都齐。
    expect(
      availableVersions(published, releases, [
        { os: 'linux', arch: 'amd64' },
        { os: 'linux', arch: 'arm64' }
      ])
    ).toEqual(['0.2.0'])
  })

  it('草稿不进候选（撤回过/未发布的版本不该被选中）', () => {
    expect(availableVersions(published, releases, [{ os: 'linux', arch: 'amd64' }])).not.toContain(
      '0.4.0'
    )
  })

  it('没选设备时返回空（调用方据此禁用按钮）', () => {
    expect(availableVersions(published, releases, [])).toEqual([])
  })
})

describe('文案与计数', () => {
  it('目标来源三态', () => {
    expect(targetSourceText(true, true)).toBe('跟随全站')
    expect(targetSourceText(false, true)).toBe('设备指定')
    expect(targetSourceText(false, false)).toBe('—')
  })

  it('任务计数把「等待上线」与「升级中」分开报', () => {
    const text = taskCountsText({ succeeded: 6, running: 1, pending: 2, failed: 1 })
    expect(text).toBe('已达成 6 · 升级中 1 · 等待上线 2 · 失败 1')
  })

  it('计数全零给「—」而不是空串（页面不能出现空白格）', () => {
    expect(taskCountsText({})).toBe('—')
    expect(taskCountsText(null)).toBe('—')
  })

  it('跳过明细四类分开报，且总数可算', () => {
    const skip = { alreadyOnTarget: 4, unsupported: 2, noArtifact: 1, disabled: 1 }
    expect(skipText(skip)).toBe('已在该版本 4 · 不支持远程升级 2 · 无该平台程序包 1 · 已停用 1')
    expect(skipTotal(skip)).toBe(8)
    expect(skipText({})).toBe('')
  })

  it('来源与发布状态文案', () => {
    expect(sourceText('manual')).toBe('单台')
    expect(sourceText('filter')).toBe('按筛选')
    expect(sourceText('unknown')).toBe('—')
    expect(releaseStatusText(1)).toBe('已发布')
    expect(releaseStatusText(0)).toBe('草稿')
  })

  it('大小与时间：缺值一律「—」', () => {
    expect(formatSize(24117248)).toBe('23.0 MB')
    expect(formatSize(2048)).toBe('2 KB')
    expect(formatSize(null)).toBe('—')
    expect(formatSize(0)).toBe('—')
    expect(formatUnixTime(1790158726)).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$/)
    expect(formatUnixTime(null)).toBe('—')
  })
})
