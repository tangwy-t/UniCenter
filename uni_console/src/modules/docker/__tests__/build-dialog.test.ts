// @vitest-environment jsdom
/**
 * 构建进度对话框（P2 分发闭环）的接线测试：组件把哪条数据接到哪个控件、
 * 双通道生命周期。
 *
 * 纯函数层（行解析/播报折叠/表单校验的协议镜像）在 build-progress.test.ts；
 * 这里钉的是：
 *   - 输入校验挡提交（非法镜像引用 / 非法上下文文件名 / dockerfile 穿越形态 /
 *     build-arg 键不是标识符，都发不出指令）；
 *   - 载荷形状：options 带 tag/context，可选项（dockerfile/args）缺省时**不发字段**，
 *     且**不带 target**（协议 validateDockerBuild 显式拒绝 —— 与 create 同型）；
 *   - 开始构建：受理 image:build（注册表现状 = 无确认档，直接派发）→ **pending 期间
 *     即开构建流** + 立即轮询 result（双通道并行）；
 *   - 播报渲染：文本行按到达序、步骤行按 id 折叠、行数上限的丢弃提示；
 *   - 受理失败（409 在飞）：分类句就地显示、留在输入态可重试；
 *   - 终态双通道收尾：流 eof 先到 → result 终态 → 结论句（成功 = 引用 + 耗时）→
 *     关闭后双次重拉（新镜像要进列表）；
 *   - 取消：断流（Abort）→ 播报态显示「已取消」→ result 结论句收尾；
 *   - 关对话框：断流 + 停轮询 + 不重拉；重新打开：整表重置。
 *
 * ── 流替身怎么造 ───────────────────────────────────────────────────
 * 与 pull-dialog.test.ts 同一套：openDockerBuildStream 的 mock 返回按脚本逐行吐
 * chunk 的 Response 替身，读尽后挂住（模拟「连着但暂无数据」），signal abort 时以
 * AbortError 拒绝挂住的 read。result 轮询的 1 秒间隔（pollDelay）是真实计时，
 * 涉轮询的用例各等 ~1.1s。
 *
 * ElDialog 默认**原地**渲染（append-to-body 未开），断言走 wrapper 查询。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import { defineComponent, h } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'
import { HttpError } from '@/utils/http/error'

const api = vi.hoisted(() => ({
  sendDockerCmd: vi.fn(),
  fetchDockerCmdResult: vi.fn(),
  openDockerBuildStream: vi.fn()
}))
vi.mock('../api', () => ({ ...api, default: undefined }))
// 构建没有凭据面（4c 的凭据属于 pull/push），useAuth 只为 module import 面存在。
vi.mock('@/hooks/core/useAuth', () => ({
  useAuth: () => ({ hasAuth: () => false, hasAnyAuth: () => false })
}))

import BuildProgressDialog from '../components/build-progress-dialog.vue'

/** ArtSvgIcon 的轻量替身：保住可断言的 DOM（真组件靠 unplugin 注册）。 */
const STUBS = {
  ArtSvgIcon: defineComponent({
    name: 'ArtSvgIcon',
    props: { icon: { type: String, default: '' } },
    setup: (props) => () => h('i', { class: 'bp-icon-stub', 'data-icon': props.icon })
  })
}

const enc = new TextEncoder()

/** 一条构建行（t 给合法默认；行尾带 \n —— NDJSON 每行以换行收尾）。 */
function jf(o: Record<string, unknown>): string {
  return `${JSON.stringify({ t: 1700000000000, ...o })}\n`
}

/**
 * 流替身：按脚本逐行吐 chunk；读尽后挂住，abort 唤醒（AbortError）。
 * state.signal 记下组件传入的 signal —— 断言「取消 = 断流」靠它。
 */
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
            // signal 已断开时 abort 事件不会再发 —— 当场拒绝（不然挂住的 read 永远不醒）。
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

/** 把流替身接进 openDockerBuildStream 的 mock（signal 一并捕获）。 */
function useStream(lines: string[]) {
  const scripted = scriptedStream(lines)
  api.openDockerBuildStream.mockImplementation(
    async (_hostId: string, _ref: string, signal: AbortSignal) => {
      scripted.state.signal = signal
      return scripted.response
    }
  )
  return scripted
}

/** result 轮询脚本：按序吐，耗尽后停在最后一个（默认 pending = 一直等）。 */
function usePolls(results: Record<string, unknown>[]) {
  let i = 0
  api.fetchDockerCmdResult.mockImplementation(async () => {
    if (i < results.length) return results[i++]!
    return results[results.length - 1] ?? { status: 'pending' }
  })
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
  api.openDockerBuildStream.mockReset()
  refresh.mockClear()
  api.sendDockerCmd.mockResolvedValue({ ref: 'r200' })
  api.fetchDockerCmdResult.mockResolvedValue({ status: 'pending' })
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

async function mountDialog() {
  const w = mount(BuildProgressDialog, {
    props: { modelValue: true, hostId: 'h1', refresh },
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

function inputOf(w: VueWrapper, placeholder: string) {
  const el = w.find(`input[placeholder="${placeholder}"]`)
  expect(el.exists(), `输入框「${placeholder}」应已渲染`).toBe(true)
  return el
}

async function setField(w: VueWrapper, placeholder: string, value: string) {
  await inputOf(w, placeholder).setValue(value)
  await flush()
}

/** 填一张最小合法表单（tag + 上下文；dockerfile/args 留空 = 可选项缺省）。 */
async function fillForm(w: VueWrapper) {
  await setField(w, '例如 registry.example.com/app:v1', 'registry.example.com/app:v1')
  await setField(w, '例如 app.tar', 'app.tar')
}

/** 等一轮 result 轮询间隔（pollDelay(0) = 1s，真实计时）。 */
const waitPoll = () => new Promise((r) => setTimeout(r, 1100))

describe('输入态：校验挡提交', () => {
  it('非法镜像引用当场显错，点开始不派发（与拉取同一把协议尺）', async () => {
    const w = await mountDialog()
    await setField(w, '例如 registry.example.com/app:v1', '_bad')
    await setField(w, '例如 app.tar', 'app.tar')
    expect(w.text()).toContain('镜像引用不合法')
    await click(w, '开始构建')
    expect(api.sendDockerCmd).not.toHaveBeenCalled()
  })

  it('上下文文件名：空串靠按钮禁用表达；带路径/错后缀当场显错不派发', async () => {
    const w = await mountDialog()
    await setField(w, '例如 registry.example.com/app:v1', 'app:v1')
    // 空：主按钮禁用（不发指令）。
    const start = buttons(w).find((b) => b.text === '开始构建')
    expect(start, '「开始构建」应已渲染').toBeTruthy()
    expect((start!.el.element as HTMLButtonElement).disabled).toBe(true)
    // 带路径成分：协议白名单外的形态先挡在表单里（分钟级失败等不起）。
    await setField(w, '例如 app.tar', 'dir/app.tar')
    expect(w.text()).toContain('文件名不合法')
    await click(w, '开始构建')
    expect(api.sendDockerCmd).not.toHaveBeenCalled()
  })

  it('dockerfile 的穿越形态（../ 与反斜杠）当场显错不派发；合法相对路径放行', async () => {
    const w = await mountDialog()
    await fillForm(w)
    await setField(w, '例如 docker/Dockerfile（缺省 = 上下文根的 Dockerfile）', '../etc/passwd')
    expect(w.text()).toContain('路径不合法')
    await click(w, '开始构建')
    expect(api.sendDockerCmd).not.toHaveBeenCalled()

    await setField(
      w,
      '例如 docker/Dockerfile（缺省 = 上下文根的 Dockerfile）',
      'docker\\Dockerfile'
    )
    expect(w.text()).toContain('路径不合法')

    await setField(w, '例如 docker/Dockerfile（缺省 = 上下文根的 Dockerfile）', 'docker/Dockerfile')
    expect(w.text()).not.toContain('路径不合法')
  })

  it('build-args：键非标识符当场显错；整行清空不算错（提交时跳过）', async () => {
    const w = await mountDialog()
    await fillForm(w)
    await click(w, '＋添加参数')
    await setField(w, '参数名', '1BAD')
    await setField(w, '值', 'x')
    expect(w.text()).toContain('参数名不合法')
    await click(w, '开始构建')
    expect(api.sendDockerCmd).not.toHaveBeenCalled()

    // 只剩值没有键同样要键（「填了一半」不是空行）：报错指回参数名。
    await setField(w, '参数名', '')
    expect(w.text()).toContain('参数名不合法')
    // 键值都清空：整行跳过，不算错。
    await setField(w, '值', '')
    expect(w.text()).not.toContain('参数名不合法')
  })
})

describe('开始构建（受理 → 双通道并行）', () => {
  it('载荷形状：options 带 tag/context，可选项缺省不发字段，不带 target', async () => {
    useStream([])
    usePolls([{ status: 'pending' }])
    const w = await mountDialog()
    await fillForm(w)
    await click(w, '开始构建')

    expect(api.sendDockerCmd).toHaveBeenCalledTimes(1)
    expect(api.sendDockerCmd).toHaveBeenCalledWith('h1', {
      action: 'image:build',
      options: { tag: 'registry.example.com/app:v1', context: 'app.tar' }
    })
    expect(api.openDockerBuildStream).toHaveBeenCalledWith('h1', 'r200', expect.any(AbortSignal))
    expect(api.fetchDockerCmdResult).toHaveBeenCalledWith('h1', 'r200')
  })

  it('dockerfile 与 args 填了才发：空表单字段不进载荷（协议对空串与缺省同判非法）', async () => {
    useStream([])
    usePolls([{ status: 'pending' }])
    const w = await mountDialog()
    await fillForm(w)
    await setField(w, '例如 docker/Dockerfile（缺省 = 上下文根的 Dockerfile）', 'docker/Dockerfile')
    await click(w, '＋添加参数')
    await setField(w, '参数名', '  VERSION ')
    await setField(w, '值', ' 1.2 ')
    await click(w, '开始构建')

    expect(api.sendDockerCmd).toHaveBeenCalledWith('h1', {
      action: 'image:build',
      options: {
        tag: 'registry.example.com/app:v1',
        context: 'app.tar',
        dockerfile: 'docker/Dockerfile',
        args: { VERSION: '1.2' } // 键值裁剪首尾空白（表单约定）
      }
    })
  })

  it('受理失败（409 在飞）：分类句就地显示，留在输入态可重试，不开流', async () => {
    api.sendDockerCmd.mockRejectedValue(new HttpError('该目标上已有同一条操作在执行', 409))
    const w = await mountDialog()
    await fillForm(w)
    await click(w, '开始构建')
    expect(w.text()).toContain('该目标上已有同一条操作在执行')
    expect(w.text()).toContain('开始构建') // 输入态还在（可改可重试）
    expect(api.openDockerBuildStream).not.toHaveBeenCalled() // 没受理就不开流
  })

  it('hostId 受理时钉死：切 prop 不改在途指令的主机（一次指令只属于受理它的主机）', async () => {
    useStream([])
    usePolls([{ status: 'pending' }])
    const w = await mountDialog()
    await fillForm(w)
    await click(w, '开始构建')
    await w.setProps({ hostId: 'h2' })
    await flush()
    expect(api.fetchDockerCmdResult).toHaveBeenCalledWith('h1', 'r200')
    expect(api.fetchDockerCmdResult).not.toHaveBeenCalledWith('h2', 'r200')
  })
})

describe('播报渲染', () => {
  it('文本行按到达序、步骤行按 id 折叠（后帧更新既有行）；丢弃量提示在明处', async () => {
    useStream([
      jf({ stream: '#1 [internal] load build definition from Dockerfile' }),
      jf({ id: 'Step 1/2', status: 'FROM node:20' }),
      jf({ id: 'Step 1/2', status: 'FROM node:21' }), // 同步骤抖动只留最新
      jf({ stream: '#3 [2/2] RUN npm install' })
    ])
    usePolls([{ status: 'pending' }])
    const w = await mountDialog()
    await fillForm(w)
    await click(w, '开始构建')
    await flush()

    const text = w.text()
    expect(text).toContain('registry.example.com/app:v1') // 播报态头部的目标引用
    expect(text).toContain('#1 [internal] load build definition from Dockerfile')
    expect(text).toContain('FROM node:21')
    expect(text).not.toContain('FROM node:20') // 旧帧被折叠掉
    expect(text).toContain('#3 [2/2] RUN npm install')
    expect(text).not.toContain('已丢弃') // 没到上限就没有丢弃提示
    expect(buttons(w).some((b) => b.text === '取消构建')).toBe(true)
  })
})

describe('终态双通道收尾', () => {
  it('流 eof 先到（等 result 提示可见）→ result 终态 → 结论句；关闭后双次重拉', async () => {
    useStream([
      jf({ stream: '#1 [internal] load build definition' }),
      jf({ stream: 'naming to registry.example.com/app:v1' }),
      jf({ done: true, eof: true })
    ])
    usePolls([{ status: 'pending' }, { status: 'succeeded', detail: '' }])
    const w = await mountDialog()
    await fillForm(w)
    await click(w, '开始构建')

    // 双通道时序：eof 已到、result 还在 pending —— 对话框说清「在等什么」。
    expect(w.text()).toContain('正在等待指令结果')

    await waitPoll() // 第二轮轮询（pollDelay(0) = 1s）
    await flush()
    expect(api.fetchDockerCmdResult).toHaveBeenCalledTimes(2)
    expect(w.text()).toContain('已构建 registry.example.com/app:v1')
    expect(w.text()).toContain('耗时')

    // 关闭：结论落定后退出，页脚路径（不依赖父组件回写 prop）。
    await click(w, '关闭')
    expect(w.emitted('update:modelValue')?.at(-1)).toEqual([false])
    expect(refresh).toHaveBeenCalledTimes(1) // 立即重拉（新镜像要进列表）
    await new Promise((r) => setTimeout(r, 1600))
    expect(refresh).toHaveBeenCalledTimes(2) // 1.5s 落定重拉（双次纪律）
  })

  it('失败：结论句原文（服务端 error 透传），关闭不重拉', async () => {
    useStream([jf({ error: 'failed to solve: exit code 1', eof: true })])
    usePolls([{ status: 'failed', error: '构建镜像失败： failed to solve: exit code 1' }])
    const w = await mountDialog()
    await fillForm(w)
    await click(w, '开始构建')
    await flush()

    expect(w.text()).toContain('构建镜像失败： failed to solve: exit code 1')
    await click(w, '关闭')
    expect(refresh).not.toHaveBeenCalled() // 失败不重拉
  })
})

describe('取消路径', () => {
  it('取消构建：断流（Abort）→ 播报态显示已取消 → result 结论句收尾', async () => {
    const scripted = useStream([jf({ stream: '#2 [1/2] RUN npm install' })])
    usePolls([{ status: 'pending' }, { status: 'failed', error: '构建已取消' }])
    const w = await mountDialog()
    await fillForm(w)
    await click(w, '开始构建')

    await click(w, '取消构建')
    expect(w.text()).toContain('已取消，正在等待指令收尾')
    expect(scripted.state.signal?.aborted, '取消 = 断流（服务端随之终止构建）').toBe(true)

    await waitPoll() // result 的「构建已取消」结论句落地
    await flush()
    expect(w.text()).toContain('构建已取消')
    expect(refresh).not.toHaveBeenCalled()
  })
})

describe('关闭与重开（生命周期收口）', () => {
  it('构建在途关对话框：断流 + 停轮询 + 不重拉', async () => {
    const scripted = useStream([jf({ stream: '#2 RUN npm install' })])
    usePolls([{ status: 'pending' }])
    const w = await mountDialog()
    await fillForm(w)
    await click(w, '开始构建')
    expect(api.fetchDockerCmdResult).toHaveBeenCalledTimes(1) // 第一轮已发

    await w.setProps({ modelValue: false }) // 父组件置 false
    await flush()
    expect(scripted.state.signal?.aborted, '关对话框即断流').toBe(true)

    await new Promise((r) => setTimeout(r, 2100)) // 跨过下一个轮询间隔
    expect(api.fetchDockerCmdResult, '轮询应已停').toHaveBeenCalledTimes(1)
    expect(refresh).not.toHaveBeenCalled()
  })

  it('重新打开：整表重置（上一次的播报表/结论/表单不进新一场）', async () => {
    useStream([jf({ done: true, eof: true })])
    usePolls([{ status: 'succeeded', detail: '' }])
    const w = await mountDialog()
    await fillForm(w)
    await click(w, '开始构建')
    await flush()
    expect(w.text()).toContain('已构建')

    await w.setProps({ modelValue: false })
    await w.setProps({ modelValue: true })
    await flush()
    expect(w.text()).toContain('开始构建')
    expect(w.text()).not.toContain('已构建')
    for (const placeholder of ['例如 registry.example.com/app:v1', '例如 app.tar']) {
      expect((inputOf(w, placeholder).element as HTMLInputElement).value).toBe('')
    }
    expect(api.openDockerBuildStream).toHaveBeenCalledTimes(1) // 新一场没有自动开流
  })
})
