// @vitest-environment jsdom
/**
 * 镜像安全扫描面板（P3·安全面）的接线测试：状态机（空态引导 → 扫描中 → 报告 /
 * 失败）与报告呈现（五档计数 / CVE 表 / 截断披露 / 缓存时间戳 / trivy 缺席的
 * 结论句原文）。
 *
 * 纯逻辑层（载荷解析 / severity 归一化 / 计数口径）在 scan-report.test.ts；
 * 这里钉的是组件把哪条数据接到哪个控件：
 *   - 空态：引导按钮发 image:scan（target 透传，manage 档无确认）；
 *   - 扫描中：加载态 + 「任务中心」提示（没有进度流 —— pending 期间的可见性
 *     由任务中心承担，面板只说清这一点）；
 *   - 报告：计数行（零档也展示）、共 N 条、扫描于 N 前、表列（编号等宽 /
 *     修复版本「无修复」如实）、truncated 的「仅显示前 500 条（共 N）」；
 *   - 失败：结论句原文（含未装 trivy 的安装提示）+ 重试；重新扫描失败不抹旧报告；
 *   - 无 manage 权限：不渲染触发按钮（不渲染 ≠ 禁用）。
 *
 * 轮询的 1 秒间隔（pollDelay）是真实计时：需要走到终态的用例让**第一次** fetch
 * 就返回终态（受理后立即轮询一次，不等 sleep）；要看「扫描中」形态的用例让
 * fetch 挂在 pending 上不收尾。
 *
 * ArtTable 用真 ElTable + columns 配置的替身（severity/修复版本两列的 formatter
 * 是 h() 节点，断言要走真实渲染）。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { ElTable, ElTableColumn } from 'element-plus'
import { mount, type VueWrapper } from '@vue/test-utils'

const api = vi.hoisted(() => ({
  sendDockerCmd: vi.fn(),
  fetchDockerCmdResult: vi.fn()
}))
vi.mock('../api', () => ({ ...api, default: undefined }))
// 权限做成可拨的开关：面板按 docker:manage 门控触发按钮（不渲染 ≠ 禁用），
// 用例里翻成 false 断「无权限不渲染」，比 doMock 二次替换已导入模块可靠。
const auth = vi.hoisted(() => ({ manage: true }))
vi.mock('@/hooks/core/useAuth', () => ({
  useAuth: () => ({
    hasAuth: (perm: string) => (perm === 'docker:manage' ? auth.manage : false),
    hasAnyAuth: () => auth.manage
  })
}))

import ImageScanPanel from '../components/image-scan-panel.vue'

/**
 * ArtTable 替身：真 ElTable + 从 columns 配置渲染全部列（formatter 透传 ——
 * severity 徽标 / 修复版本两列的 h() 节点要真实渲染才能断言）。
 */
const artTableStub = defineComponent({
  name: 'ArtTable',
  inheritAttrs: false,
  setup(_, { attrs }) {
    return () =>
      h(
        ElTable,
        { ...attrs, data: (attrs.data as unknown[]) ?? [] },
        {
          default: () =>
            (
              (attrs.columns as {
                prop?: string
                label?: string
                minWidth?: number
                formatter?: unknown
              }[]) ?? []
            ).map((c) => {
              // 无 formatter 的列**不传该 prop**：formatter 恒在场（哪怕返回
              // undefined）会让 EP 走格式化路径渲染空单元格；minWidth 可选同理。
              // props 收进一个宽对象再传 —— h() 对具体组件的 props 推导不接受
              // 「可能为 undefined」的可选字段（运行期 undefined 与缺省同义）。
              const colProps: Record<string, unknown> = { prop: c.prop, label: c.label }
              if (c.minWidth != null) colProps.minWidth = c.minWidth
              if (typeof c.formatter === 'function') {
                colProps.formatter = (row: unknown) => (c.formatter as (r: unknown) => unknown)(row)
              }
              return h(ElTableColumn, colProps)
            })
        }
      )
  }
})

const STUBS = { ArtTable: artTableStub }

/** 一份报告载荷（线上 snake_case —— 面板里的解析层正是被测面之一）。 */
function payloadOf(over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    image_id: 'sha256:aaaa1111bbbb2222cccc3333dddd4444eeee5555ffff66667777888899990000',
    scanned_at: Math.floor(Date.now() / 1000) - 7200, // 2 小时前（缓存时间戳断言用）
    counts: { critical: 2, high: 3, medium: 0, low: 1, unknown: 0 },
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

const mounted: VueWrapper[] = []

beforeEach(() => {
  vi.stubGlobal(
    'ResizeObserver',
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
  )
  api.sendDockerCmd.mockReset()
  api.fetchDockerCmdResult.mockReset()
  api.sendDockerCmd.mockResolvedValue({ ref: 'r1' })
  api.fetchDockerCmdResult.mockResolvedValue({ status: 'pending' })
  auth.manage = true
})

afterEach(() => {
  for (const w of mounted.splice(0)) w.unmount()
  vi.clearAllMocks()
})

const flush = async () => {
  await new Promise((r) => setTimeout(r, 0))
  await nextTick()
}

async function mountPanel() {
  const w = mount(ImageScanPanel, {
    props: { hostId: 'h1', target: 'mysql:8.0' },
    global: { stubs: STUBS }
  })
  mounted.push(w)
  await flush()
  return w
}

function clickBtn(w: VueWrapper, label: string) {
  const btn = w.findAll('button').find((b) => (b.text() ?? '').includes(label))
  expect(btn, `按钮「${label}」应已渲染`).toBeTruthy()
  return btn!.trigger('click')
}

describe('空态与触发', () => {
  it('未扫过：空态文案 + 引导按钮；不自动扫描（打开 Tab 不该启动一场分钟级任务）', async () => {
    const w = await mountPanel()
    expect(w.text()).toContain('这个镜像还没有安全报告')
    expect(w.text()).toContain('扫描镜像')
    expect(api.sendDockerCmd).not.toHaveBeenCalled()
  })

  it('引导按钮发 image:scan：target 透传、无确认值（manage 档、协议无 Confirm 要求）', async () => {
    api.fetchDockerCmdResult.mockResolvedValueOnce({ status: 'succeeded', payload: payloadOf() })
    const w = await mountPanel()
    await clickBtn(w, '扫描镜像')
    await flush()

    expect(api.sendDockerCmd).toHaveBeenCalledTimes(1)
    expect(api.sendDockerCmd).toHaveBeenCalledWith('h1', {
      action: 'image:scan',
      target: 'mysql:8.0',
      options: undefined,
      confirm: undefined
    })
  })

  it('无 manage 权限：不渲染触发按钮（不渲染 ≠ 禁用）', async () => {
    auth.manage = false
    const w = await mountPanel()
    expect(w.text()).toContain('这个镜像还没有安全报告')
    expect(w.findAll('button').some((b) => (b.text() ?? '').includes('扫描镜像'))).toBe(false)
  })
})

describe('扫描中', () => {
  it('加载态 + 任务中心提示（pending 期间的可见性归任务中心，面板把这一点说清）', async () => {
    api.fetchDockerCmdResult.mockResolvedValue({ status: 'pending' })
    const w = await mountPanel()
    await clickBtn(w, '扫描镜像')
    await flush()

    expect(api.sendDockerCmd).toHaveBeenCalledTimes(1)
    expect(w.text()).toContain('正在扫描')
    expect(w.text()).toContain('任务中心')
    // 触发按钮在途不可重复点（重复触发只会吃到 409）。
    expect(w.findAll('button').some((b) => (b.text() ?? '').includes('扫描镜像'))).toBe(false)
  })
})

describe('报告呈现', () => {
  async function scanOnce(w: VueWrapper, over: Record<string, unknown> = {}) {
    api.fetchDockerCmdResult.mockResolvedValueOnce({
      status: 'succeeded',
      payload: payloadOf(over)
    })
    await clickBtn(w, '扫描镜像')
    await flush()
  }

  it('计数行：五档（含零档）都渲染，数字来自全量计数；共 N 条 = 五档求和', async () => {
    const w = await mountPanel()
    await scanOnce(w)
    // chip 文本 =「档名 + 计数」（模板换行归一后逐档断言 —— 模板空白不该让断言碰运气）。
    expect(w.findAll('.isc-chip').map((c) => c.text().replace(/\s+/g, ''))).toEqual([
      '严重2',
      '高危3',
      '中危0',
      '低危1',
      '未知0'
    ])
    expect(w.text()).toContain('共 6 条')
  })

  it('缓存时间戳：扫描于 N 小时前（scanned_at 是服务端重盖的收帧时刻）', async () => {
    const w = await mountPanel()
    await scanOnce(w)
    expect(w.text()).toContain('扫描于 2 小时前')
  })

  it('CVE 表：编号/包渲染，有修复给版本、无修复如实说「无修复」；空报告给「没有发现漏洞」', async () => {
    const w = await mountPanel()
    await scanOnce(w)
    const text = w.text()
    expect(text).toContain('CVE-2023-1234')
    expect(text).toContain('openssl')
    expect(text).toContain('3.0.12')
    expect(text).toContain('CVE-2024-5678')
    expect(text).toContain('无修复')
    expect(w.findComponent(ElTable).exists()).toBe(true)

    // 干净镜像：计数全零 + 空表结论（不渲染表格）。
    const clean = await mountPanel()
    await scanOnce(clean, {
      counts: { critical: 0, high: 0, medium: 0, low: 0, unknown: 0 },
      vulns: []
    })
    expect(clean.text()).toContain('共 0 条')
    expect(clean.text()).toContain('没有发现漏洞')
  })

  it('截断披露：truncated 时说「仅显示前 500 条（共 N 条）」，计数仍是全量', async () => {
    const w = await mountPanel()
    await scanOnce(w, { truncated: true })
    expect(w.text()).toContain('仅显示前 500 条')
    expect(w.text()).toContain('共 6 条')
    // 未截断不说这句（计数行自己已经说了总数）。
    const full = await mountPanel()
    await scanOnce(full)
    expect(full.text()).not.toContain('仅显示前')
  })

  it('报告态有「重新扫描」；重新扫描失败不抹旧报告（结论降为警示行）', async () => {
    const w = await mountPanel()
    await scanOnce(w)
    expect(w.text()).toContain('重新扫描')

    api.sendDockerCmd.mockRejectedValueOnce(new Error('该目标上已有同一条操作在执行'))
    await clickBtn(w, '重新扫描')
    await flush()
    // 旧报告仍在（扫描于/计数都在），失败结论以警示行并列 —— 不是顶替。
    expect(w.text()).toContain('扫描于 2 小时前')
    expect(w.text()).toContain('共 6 条')
    expect(w.text()).toContain('该目标上已有同一条操作在执行')
  })
})

describe('失败态', () => {
  it('未装 trivy：结论句原文（含安装提示）+ 重试按钮', async () => {
    api.fetchDockerCmdResult.mockResolvedValueOnce({
      status: 'failed',
      error: '主机未安装 trivy，无法扫描镜像（请先在这台主机上安装 trivy）'
    })
    const w = await mountPanel()
    await clickBtn(w, '扫描镜像')
    await flush()

    expect(w.text()).toContain('主机未安装 trivy，无法扫描镜像（请先在这台主机上安装 trivy）')
    expect(w.text()).toContain('重试')
    // 失败不是报告：计数行不该出现。
    expect(w.text()).not.toContain('共 ')
  })

  it('扫描成功但报告读不出：如实说结论，不伪装成「0 条」', async () => {
    api.fetchDockerCmdResult.mockResolvedValueOnce({ status: 'succeeded', payload: null })
    const w = await mountPanel()
    await clickBtn(w, '扫描镜像')
    await flush()
    expect(w.text()).toContain('报告未能读取')
    expect(w.text()).not.toContain('没有发现漏洞')
  })
})
