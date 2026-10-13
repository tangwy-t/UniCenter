// @vitest-environment jsdom
/**
 * 泛化后的批量栏（资源无关）的钉子：动作集从 utils/actions 注册表推导、确认档按危险度
 * 分三档（删除家族逐字 DELETE / 普通动作一次确认 / 需要参数的输入档）、行投影里的
 * 页面侧结论（blocked）与保护档分流。
 *
 * 容器页的回归在 workload-batch.test.ts（同一组件的默认动作集）；本文件专钉**换一套
 * 动作与行投影之后**仍然成立的东西 —— 即「同一个组件接不同资源」这件事本身。
 *
 * 指令通道（useDockerCmds）经 mock 整个 '../api' 接管；ElMessage 桩掉（其余 EP 组件
 * 用真件）。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'

const api = vi.hoisted(() => ({
  sendDockerCmd: vi.fn(),
  fetchDockerCmdResult: vi.fn()
}))
vi.mock('../api', () => ({ ...api, default: undefined }))

const elMessage = vi.hoisted(() => {
  const fn = vi.fn()
  return Object.assign(fn, { success: vi.fn(), warning: vi.fn(), error: vi.fn() })
})
vi.mock('element-plus', async (importOriginal) => {
  const actual = await importOriginal<typeof import('element-plus')>()
  return { ...actual, ElMessage: elMessage }
})

vi.mock('@/hooks/core/useAuth', () => ({
  useAuth: () => ({ hasAuth: () => true, hasAnyAuth: () => true })
}))

import WorkloadBatchBar from '../components/workload-batch-bar.vue'
import {
  IMAGE_BATCH_ACTIONS,
  NETWORK_BATCH_ACTIONS,
  VOLUME_BATCH_ACTIONS,
  type BatchRow
} from '../components/batch-actions'

/** 一行（投影后的最小行形态）。 */
function row(overrides: Partial<BatchRow> = {}): BatchRow {
  return { hostId: 'h1', hostname: 'bogon', name: 'nginx:latest', ...overrides }
}

const mounted: VueWrapper[] = []
const refresh = vi.fn()

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
  refresh.mockReset()
  elMessage.mockReset()
  elMessage.success.mockReset()
  elMessage.warning.mockReset()
  elMessage.error.mockReset()
  api.sendDockerCmd.mockImplementation(async (_hostId: string, body: { target?: string }) => ({
    ref: body.target ?? 'r'
  }))
  api.fetchDockerCmdResult.mockResolvedValue({ status: 'succeeded' })
})

afterEach(() => {
  for (const w of mounted.splice(0)) w.unmount()
  vi.clearAllMocks()
  vi.unstubAllGlobals()
})

async function flush(rounds = 20) {
  for (let i = 0; i < rounds; i++) {
    await Promise.resolve()
    await nextTick()
  }
}

async function mountBar(selected: BatchRow[], props: Record<string, unknown> = {}) {
  const w = mount(WorkloadBatchBar, {
    props: { selected, busy: false, refresh, ...props } as never,
    global: {}
  })
  mounted.push(w)
  await flush()
  return w
}

/** 栏上的动作钮（带 `…`：点了先过确认档）。 */
function actionBtn(w: VueWrapper, label: string) {
  return Array.from(w.findAll('button')).find((b) => (b.text() ?? '').trim() === `${label}…`)
}

describe('批量栏 · 动作集来自注册表', () => {
  it('镜像动作集：按钮标签/危险度随注册表（删除=危险描边、导出=中性）', async () => {
    const w = await mountBar([row()], { noun: '镜像', actions: IMAGE_BATCH_ACTIONS })
    expect(actionBtn(w, '删除')).toBeTruthy()
    expect(actionBtn(w, '导出')).toBeTruthy()
    expect(actionBtn(w, '启动'), '容器动作不该出现在镜像页').toBeUndefined()
    // 危险动作走 danger 描边（视觉语汇与注册表一致）。
    expect(actionBtn(w, '删除')!.element.className).toContain('el-button--danger')
    expect(actionBtn(w, '导出')!.element.className).not.toContain('el-button--danger')
  })

  it('数据卷/网络动作集：单条删除各用各的 action 码，确认弹窗逐字 DELETE', async () => {
    const volumes = await mountBar([row({ name: 'dbdata' })], {
      noun: '数据卷',
      actions: VOLUME_BATCH_ACTIONS
    })
    expect(actionBtn(volumes, '删除')).toBeTruthy()
    const volumesInput = await openWordDialog(volumes, '删除')
    await volumesInput.setValue('DELETE')
    await clickConfirm(volumes, '删除')
    await flush()
    expect(api.sendDockerCmd).toHaveBeenCalledWith(
      'h1',
      expect.objectContaining({ action: 'volume:remove', target: 'dbdata' })
    )

    api.sendDockerCmd.mockClear()
    const networks = await mountBar([row({ name: 'bridge', hostId: 'h2', hostname: 'nas' })], {
      noun: '网络',
      actions: NETWORK_BATCH_ACTIONS
    })
    const netInput = await openWordDialog(networks, '删除')
    await netInput.setValue('DELETE')
    await clickConfirm(networks, '删除')
    await flush()
    expect(api.sendDockerCmd).toHaveBeenCalledWith(
      'h2',
      expect.objectContaining({ action: 'network:remove', target: 'bridge' })
    )
  })
})

describe('批量栏 · 确认档按危险度分档', () => {
  it('普通动作（启停）也要过一次确认：列出目标 + 主机，未过档前不发指令', async () => {
    const w = await mountBar([row({ name: 'web' }), row({ name: 'db', hostId: 'h2', hostname: 'nas' })], {
      actions: [{ action: 'container:start' }]
    })
    const btn = actionBtn(w, '启动')
    btn!.element.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await flush()
    await vi.waitUntil(() => w.find('.wkl-batch-del__list').exists(), { timeout: 5000 })
    const text = w.find('.wkl-batch-del').text()
    expect(text, '确认弹窗要列清目标').toContain('web')
    expect(text, '确认弹窗要列清归属主机').toContain('bogon')
    expect(text, '清单要说清动的是哪种资源').toContain('个容器')
    expect(api.sendDockerCmd, '未过确认档不该发指令').not.toHaveBeenCalled()
  })

  it('输入档（导出 tar）：前缀留空也能提交，文件名逐行按镜像引用生成', async () => {
    const w = await mountBar([row({ name: 'quay.io/minio/minio:RELEASE' })], {
      noun: '镜像',
      actions: IMAGE_BATCH_ACTIONS
    })
    const input = await openWordDialog(w, '导出')
    expect(input.exists(), '导出弹窗应有文件名前缀输入').toBe(true)
    await clickConfirm(w, '导出')
    await flush()
    expect(api.sendDockerCmd).toHaveBeenCalledWith(
      'h1',
      expect.objectContaining({
        action: 'image:save',
        target: 'quay.io/minio/minio:RELEASE',
        options: { filename: 'quay.io_minio_minio_RELEASE.tar' }
      })
    )
  })
})

describe('批量栏 · 保护档与页面侧结论', () => {
  it('受保护行默认不发（结论句进失败明细）；勾「强制操作」后带 force 发出', async () => {
    const w = await mountBar([row({ name: 'dbdata', protected: true })], {
      noun: '数据卷',
      actions: VOLUME_BATCH_ACTIONS
    })
    const input = await openWordDialog(w, '删除')
    await input.setValue('DELETE')
    // 受保护 + 有 docker:exec：弹窗里出现「强制操作」开关。
    const force = w.find('.wkl-batch-del__force input')
    expect(force.exists(), '受保护目标应给强制操作开关').toBe(true)
    await force.setValue(true)
    await clickConfirm(w, '删除')
    await flush()
    expect(api.sendDockerCmd).toHaveBeenCalledWith(
      'h1',
      expect.objectContaining({ action: 'volume:remove', target: 'dbdata', options: { force: true } })
    )
  })

  it('页面侧 blocked 结论（如使用中的镜像）：该行不发、结论句原样计入失败', async () => {
    const w = await mountBar(
      [row({ name: 'mysql:8.0', blocked: (a) => (a === 'image:remove' ? '镜像正被容器使用' : null) })],
      { noun: '镜像', actions: IMAGE_BATCH_ACTIONS }
    )
    const input = await openWordDialog(w, '删除')
    await input.setValue('DELETE')
    await clickConfirm(w, '删除')
    await flush()
    expect(api.sendDockerCmd, '被拦下的行不发指令').not.toHaveBeenCalled()
    expect(w.text()).toContain('mysql:8.0（镜像正被容器使用）')
  })
})

/* ── 弹窗操作小工具（两档共用一只弹窗，查询口径也统一）──────────── */

/** 打开确认弹窗（不提交），等输入框渲染出来并返回它。 */
async function openWordDialog(w: VueWrapper, label: string) {
  const btn = actionBtn(w, label)
  expect(btn, `「${label}…」应已渲染`).toBeTruthy()
  btn!.element.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  await flush()
  await vi.waitUntil(() => w.find('.wkl-batch-del__input input').exists(), { timeout: 5000 })
  return w.find('.wkl-batch-del__input input')
}

/** 弹窗主按钮（label = 动作名；与栏上的 `label…` 区分开）。 */
async function clickConfirm(w: VueWrapper, label: string) {
  const confirm = Array.from(w.findAll('button')).find((b) => (b.text() ?? '').trim() === label)
  expect(confirm, `弹窗主按钮「${label}」应存在`).toBeTruthy()
  confirm!.element.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  await flush()
}