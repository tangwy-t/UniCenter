// @vitest-environment jsdom
/**
 * 任务行进度组件（components/task-progress.vue）的钉子（本波「观看/执行解耦」）：
 *   ① 三族各接自己的流端点、按族渲染 —— pull/push 走层表（语义字面量不同：
 *      已下载/已上传），build 走文本播报；
 *   ② **收起 ≠ 取消**：卸载只断流（aborted=true），绝不下发取消请求；
 *   ③ **展开即重接**：重新挂载再开一次流（同一条句柄，进度可续 —— 服务端把断线
 *      期间的帧缓冲在会话里，这一侧只需重接）；
 *   ④ 取消是**独立动作**：按钮调取消端点、成败就地给结论句，且**不断流**
 *      （观看继续，结局由服务端结算）；
 *   ⑤ canCancel=false（别人的任务 / 无权限 / 已终态）时不渲染取消入口。
 *
 * 流替身与三个进度对话框测试同一套（按脚本逐行吐 chunk，读尽后挂住，signal abort
 * 时以 AbortError 拒绝）；ArtSvgIcon 用轻量替身（unplugin 自动注册在测试环境不存在）。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'
import { HttpError } from '@/utils/http/error'

const api = vi.hoisted(() => ({
  openDockerPullStream: vi.fn(),
  openDockerPushStream: vi.fn(),
  openDockerBuildStream: vi.fn(),
  cancelDockerCmd: vi.fn()
}))
vi.mock('../api', () => ({ ...api, default: undefined }))

import TaskProgress from '../components/task-progress.vue'

const STUBS = {
  ArtSvgIcon: defineComponent({
    name: 'ArtSvgIcon',
    props: { icon: { type: String, default: '' } },
    setup: (props) => () => h('i', { class: 'tp-icon-stub', 'data-icon': props.icon })
  })
}

const enc = new TextEncoder()

/** 一条进度/构建行（t 给合法默认；行尾带 \n —— NDJSON 每行以换行收尾）。 */
function jf(o: Record<string, unknown>): string {
  return `${JSON.stringify({ t: 1700000000000, ...o })}\n`
}

/** 流替身：按脚本逐行吐 chunk；读尽后挂住，abort 唤醒（AbortError）。 */
function scriptedStream(lines: string[]) {
  let i = 0
  const state = { signal: undefined as AbortSignal | undefined }
  let rejectHanging: ((e: Error) => void) | null = null
  const response = {
    ok: true,
    status: 200,
    body: {
      getReader: () => ({
        read: () => {
          if (i < lines.length) {
            const value = enc.encode(lines[i++]!)
            return Promise.resolve({ done: false, value })
          }
          return new Promise((_resolve: unknown, reject: (e: Error) => void) => {
            rejectHanging = reject
            const abort = () => {
              const err = new Error('The operation was aborted')
              err.name = 'AbortError'
              rejectHanging?.(err)
            }
            if (state.signal?.aborted) {
              abort()
              return
            }
            state.signal?.addEventListener('abort', abort, { once: true })
          })
        }
      })
    }
  }
  return { response: response as unknown as Response, state }
}

/** 把替身接给指定族的端点，返回它（断言 aborted 用）。 */
function useStream(family: 'pull' | 'push' | 'build', lines: string[]) {
  const scripted = scriptedStream(lines)
  const fn =
    family === 'pull'
      ? api.openDockerPullStream
      : family === 'push'
        ? api.openDockerPushStream
        : api.openDockerBuildStream
  fn.mockImplementation(async (_hostId: string, _ref: string, signal: AbortSignal) => {
    scripted.state.signal = signal
    return scripted.response
  })
  return scripted
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
  api.openDockerPullStream.mockReset()
  api.openDockerPushStream.mockReset()
  api.openDockerBuildStream.mockReset()
  api.cancelDockerCmd.mockReset()
  api.cancelDockerCmd.mockResolvedValue({ ref: 'r1' })
})

afterEach(() => {
  for (const w of mounted.splice(0)) w.unmount()
  vi.clearAllMocks()
})

async function flush() {
  await nextTick()
  await new Promise((r) => setTimeout(r, 0))
  await nextTick()
}

async function mountProgress(props: Record<string, unknown> = {}) {
  const w = mount(TaskProgress, {
    props: { hostId: 'h1', cmdRef: 'r1', action: 'image:pull', ...props },
    global: { stubs: STUBS }
  })
  mounted.push(w)
  await flush()
  return w
}

function buttons(w: VueWrapper) {
  return w.findAll('button').map((b) => ({ el: b, text: (b.text() ?? '').trim() }))
}

async function click(w: VueWrapper, label: string) {
  const btn = buttons(w).find((b) => b.text === label)
  expect(btn, `按钮「${label}」应已渲染`).toBeTruthy()
  await btn!.el.trigger('click')
  await flush()
}

describe('按族接流与渲染', () => {
  it('pull：层表折叠 + 汇总说「已下载」；接入的是 pull 端点', async () => {
    useStream('pull', [
      jf({ id: 'sha256:aaa', status: 'Downloading', current: 500, total: 1000 }),
      jf({ id: 'sha256:bbb', status: 'Pull complete' })
    ])
    const w = await mountProgress()

    expect(api.openDockerPullStream).toHaveBeenCalledWith('h1', 'r1', expect.anything())
    expect(w.text()).toContain('已下载 500 B / 1000 B')
    expect(w.text()).toContain('1/2 层完成')
    expect(w.text()).not.toContain('sha256:aaa') // 短 id 展示（与对话框同口径）
    expect(w.findAll('.tp-layer').length).toBe(2)
  })

  it('push：同一张层表、动词换「已上传」；接入的是 push 端点', async () => {
    useStream('push', [jf({ id: 'sha256:aaa', status: 'Pushing', current: 300, total: 900 })])
    const w = await mountProgress({ action: 'image:push' })

    expect(api.openDockerPushStream).toHaveBeenCalledWith('h1', 'r1', expect.anything())
    expect(w.text()).toContain('已上传 300 B / 900 B')
  })

  it('build：文本播报（步骤 id + 正文），接入的是 build 端点', async () => {
    useStream('build', [
      jf({ id: '#2', status: 'RUN npm install' }),
      jf({ stream: '#2 [1/2] resolving packages' })
    ])
    const w = await mountProgress({ action: 'image:build' })

    expect(api.openDockerBuildStream).toHaveBeenCalledWith('h1', 'r1', expect.anything())
    expect(w.findAll('.tp-line').length).toBe(2)
    expect(w.text()).toContain('RUN npm install')
    expect(w.text()).toContain('resolving packages')
  })

  it('接入失败（404）：给结论句且不打断任何东西', async () => {
    api.openDockerPullStream.mockResolvedValue({ ok: false, status: 404 })
    const w = await mountProgress()
    expect(w.text()).toContain('该指令已不存在或会话已过期')
    expect(api.cancelDockerCmd).not.toHaveBeenCalled()
  })

  it('eof：收口提示「进度已全部到达」', async () => {
    useStream('pull', [jf({ done: true, eof: true })])
    const w = await mountProgress()
    expect(w.text()).toContain('进度已全部到达')
  })
})

describe('收起 ≠ 取消（断流只停止观看）', () => {
  it('卸载：只断流（aborted），不下发取消请求', async () => {
    const scripted = useStream('pull', [jf({ id: 'sha256:aaa', status: 'Downloading' })])
    const w = await mountProgress()
    expect(scripted.state.signal?.aborted).toBe(false)

    w.unmount()
    await flush()
    expect(scripted.state.signal?.aborted, '收起行只是停止观看').toBe(true)
    expect(api.cancelDockerCmd, '收起 ≠ 取消').not.toHaveBeenCalled()
  })

  it('展开即重接：重新挂载再开一次同一条流（进度可续）', async () => {
    const first = useStream('pull', [jf({ id: 'sha256:aaa', status: 'Downloading' })])
    const w = await mountProgress()
    w.unmount()
    await flush()
    expect(first.state.signal?.aborted).toBe(true)

    // 重接：同一条句柄（hostId/ref 不变），服务端把断线期间的帧缓冲在会话里。
    useStream('pull', [jf({ id: 'sha256:bbb', status: 'Extracting' })])
    const w2 = await mountProgress()
    expect(api.openDockerPullStream).toHaveBeenCalledTimes(2)
    expect(w2.text()).toContain('bbb')
  })
})

describe('取消（独立于观看的显式动作）', () => {
  it('点取消：调取消端点 + 按钮转「已请求取消」，流照常连着', async () => {
    const scripted = useStream('pull', [jf({ id: 'sha256:aaa', status: 'Downloading' })])
    const w = await mountProgress({ canCancel: true })

    await click(w, '取消拉取')
    expect(api.cancelDockerCmd).toHaveBeenCalledWith('h1', 'r1')
    expect(buttons(w).some((b) => b.text === '已请求取消')).toBe(true)
    expect(scripted.state.signal?.aborted, '取消 ≠ 停止观看').toBe(false)
  })

  it('取消失败（403）：就地结论句，按钮回到可重试状态', async () => {
    useStream('pull', [jf({ id: 'sha256:aaa', status: 'Downloading' })])
    api.cancelDockerCmd.mockRejectedValue(new HttpError('无权取消该指令', 403))
    const w = await mountProgress({ canCancel: true })

    await click(w, '取消拉取')
    expect(w.text()).toContain('无权取消该指令')
    expect(buttons(w).some((b) => b.text === '取消拉取')).toBe(true)
  })

  it('构建族：按钮措辞换「取消构建」', async () => {
    useStream('build', [jf({ stream: '#2 RUN npm install' })])
    const w = await mountProgress({ action: 'image:build', canCancel: true })
    expect(buttons(w).some((b) => b.text === '取消构建')).toBe(true)
  })

  it('canCancel=false（别人的任务/无权限）：不渲染取消入口，收起口径仍在', async () => {
    useStream('pull', [jf({ id: 'sha256:aaa', status: 'Downloading' })])
    const w = await mountProgress({ canCancel: false })
    expect(buttons(w).some((b) => b.text === '取消拉取')).toBe(false)
    expect(w.text()).toContain('收起不取消任务')
  })
})