// @vitest-environment jsdom
/**
 * 任务中心（6b 前端半边）的接线测试：drawer 的列表渲染 / 过滤 / 轮询生命周期 /
 * 拉取进度内联（展开连流、收起断流）/ 入口权限门控。
 *
 * 沿用 pull-dialog / create-drawer 的 vi.mock 模式（mock 整个 '../api'）：进度流用
 * 可控的假 NDJSON 响应（脚本化 reader：按行吐 chunk、读尽挂住、abort 以
 * AbortError 唤醒 —— 与真 fetch 同形）。入口门控在 DockerPage（主机条按钮）上
 * 验证 —— overview hero 的入口同码同构（canList + v-if），门控行为由这里代表钉住。
 *
 * 轮询用例跑假定时器（5s interval 是被测的表）；其余用例真定时器 + 微任务冲刷。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, nextTick, ref, KeepAlive } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'

const api = vi.hoisted(() => ({
  fetchDockerTasks: vi.fn(),
  openDockerPullStream: vi.fn(),
  fetchDockerHosts: vi.fn()
}))
vi.mock('../api', () => ({ ...api, default: undefined }))

// 入口的权限门（docker:list）可开关：drawer 自身不做权限判定（入口不渲染即打不开），
// 门控用例在最后的 describe 里切换。
const auth = vi.hoisted(() => ({ list: true }))
vi.mock('@/hooks/core/useAuth', () => ({
  useAuth: () => ({
    hasAuth: (perm: string) => perm === 'docker:list' && auth.list,
    hasAnyAuth: (perms: string[]) => perms.length === 0
  })
}))

import TaskCenterDrawer from '../components/task-center-drawer.vue'
import DockerPage from '../components/docker-page.vue'
import { provideDockerHost } from '../utils/host-context'
import type { DockerTaskItem } from '../api'

/** ArtSvgIcon 的轻量替身：保住可断言的 DOM（真组件靠 unplugin 注册）。 */
const ART_ICON = defineComponent({
  name: 'ArtSvgIcon',
  props: { icon: { type: String, default: '' } },
  setup: (props) => () => h('i', { class: 'tcd-icon-stub', 'data-icon': props.icon })
})

/** 一条任务（默认值取最常见形态；覆盖项逐用例给）。 */
function task(overrides: Partial<DockerTaskItem> = {}): DockerTaskItem {
  return {
    ref: 'r1',
    hostId: 'h1',
    hostname: 'bogon',
    action: 'container:start',
    target: 'uni-center-core',
    username: 'admin',
    createdAt: Date.now() - 90_000, // unix 毫秒（与指令记录同单位）
    status: 'succeeded',
    summary: '执行成功',
    ...overrides
  }
}

const FOUR_ITEMS: DockerTaskItem[] = [
  task({ ref: 'r1', status: 'succeeded', action: 'container:start', target: 'uni-center-core' }),
  task({ ref: 'r2', status: 'pending', action: 'image:pull', target: 'nginx:latest', summary: '' }),
  task({
    ref: 'r3',
    status: 'failed',
    action: 'container:remove',
    target: 'old-app',
    summary: '容器处于运行状态，需先停止再删除'
  }),
  task({ ref: 'r4', status: 'timeout', action: 'compose:up', target: 'web', summary: '操作超时' })
]

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
  api.fetchDockerTasks.mockReset()
  api.openDockerPullStream.mockReset()
  api.fetchDockerHosts.mockReset()
  api.fetchDockerHosts.mockResolvedValue({ list: [] })
  auth.list = true
})

afterEach(() => {
  for (const w of mounted.splice(0)) w.unmount()
  vi.clearAllMocks()
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

/** 微任务 + 渲染冲刷（假定时器下也成立：微任务与 nextTick 不被接管）。 */
async function flush(rounds = 12) {
  for (let i = 0; i < rounds; i++) {
    await Promise.resolve()
    await nextTick()
  }
}

async function mountDrawer() {
  const w = mount(TaskCenterDrawer, {
    props: { modelValue: true },
    global: { stubs: { ArtSvgIcon: ART_ICON } }
  })
  mounted.push(w)
  await flush()
  return w
}

/** 抽屉 teleport 到 body：断言一律查 body。 */
const bodyText = () => document.body.textContent ?? ''
const bodyRows = () => document.body.querySelectorAll('.tcd-item')
const bodyToggles = () =>
  Array.from(document.body.querySelectorAll('.tcd-item__toggle')) as HTMLButtonElement[]

async function clickToggle(index: number) {
  const btn = bodyToggles()[index]
  expect(btn, `第 ${index} 个展开开关应已渲染`).toBeTruthy()
  btn.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  await flush()
}

/* ── 可控的假拉取进度流（脚本化 reader，abort 以 AbortError 唤醒挂住的 read） ── */
const enc = new TextEncoder()

function jf(o: Record<string, unknown>): string {
  return `${JSON.stringify({ t: 1700000000000, ...o })}\n`
}

const LA = 'aaaa1111bbbb2222cccc3333dddd4444eeee5555ffff66667777888899990000'
const LB = '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef'

function scriptedPullStream(lines: string[]) {
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

function usePullStream(lines: string[]) {
  const scripted = scriptedPullStream(lines)
  api.openDockerPullStream.mockImplementation(
    async (_hostId: string, _ref: string, signal: AbortSignal) => {
      scripted.state.signal = signal
      return scripted.response
    }
  )
  return scripted
}

describe('任务中心 · 列表渲染', () => {
  it('打开即拉（跨主机、不带过滤参数）并渲染状态点/动作 label/目标/元信息', async () => {
    api.fetchDockerTasks.mockResolvedValue({ items: FOUR_ITEMS })
    await mountDrawer()

    expect(api.fetchDockerTasks).toHaveBeenCalledTimes(1)
    expect(api.fetchDockerTasks).toHaveBeenCalledWith({})
    expect(bodyRows()).toHaveLength(4)

    // 状态点：绿 = 成功、红 = 失败与超时（两类事实同色、词分开）、pending 走动画位。
    expect(document.body.querySelectorAll('.tcd-item__dot.is-ok')).toHaveLength(1)
    expect(document.body.querySelectorAll('.tcd-item__dot.is-fail')).toHaveLength(2)
    expect(document.body.querySelectorAll('.tcd-item__dot.is-pending')).toHaveLength(1)
    // 状态词（timeout 不冒充 failed：词分开说）。
    expect(bodyText()).toContain('成功')
    expect(bodyText()).toContain('失败')
    expect(bodyText()).toContain('超时')
    expect(bodyText()).toContain('进行中')

    // 动作：注册表 label（页面只说人话）。
    expect(bodyText()).toContain('启动')
    expect(bodyText()).toContain('拉取镜像')
    expect(bodyText()).toContain('删除')
    expect(bodyText()).toContain('启动项目')
    // 目标与元信息行：主机/发起人/相对时间（受理时刻 90 秒前）。
    expect(bodyText()).toContain('nginx:latest')
    expect(bodyText()).toContain('bogon')
    expect(bodyText()).toContain('admin')
    expect(bodyText()).toContain('1 分钟前')
  })

  it('注册表查不到的 action 原样等宽显示（只读/终端/编辑类不在二期写注册表里）', async () => {
    api.fetchDockerTasks.mockResolvedValue({
      items: [task({ ref: 'rx', action: 'container:inspect', target: 'any' })]
    })
    await mountDrawer()

    const raw = document.body.querySelector('.tcd-item__action.is-raw')
    expect(raw).toBeTruthy()
    expect(raw?.textContent).toContain('container:inspect')

    // 反向：已登记的 action 用 label，不挂等宽 class（label 是人话，不是原始码）。
    api.fetchDockerTasks.mockResolvedValue({ items: [task()] })
    mounted.pop()?.unmount()
    await mountDrawer()
    expect(document.body.querySelector('.tcd-item__action.is-raw')).toBeNull()
  })

  it('失败/超时行展开给结论句原文；成功行不展开（成功不是排障线索）', async () => {
    api.fetchDockerTasks.mockResolvedValue({ items: FOUR_ITEMS })
    await mountDrawer()

    // r1（成功）无开关；r2（进行中拉取）、r3（失败）、r4（超时）各有一个。
    expect(bodyToggles()).toHaveLength(3)
    expect(bodyText()).not.toContain('容器处于运行状态')

    await clickToggle(1) // r3：失败行的开关
    expect(bodyText()).toContain('容器处于运行状态，需先停止再删除')

    await clickToggle(1) // 再点收起
    expect(bodyText()).not.toContain('容器处于运行状态')
  })

  it('空态与首拉失败态分开说（引导句 / 结论句 + 重试）', async () => {
    api.fetchDockerTasks.mockResolvedValue({ items: [] })
    await mountDrawer()
    expect(bodyText()).toContain('还没有受理过任何任务')

    api.fetchDockerTasks.mockRejectedValue(new Error('network down'))
    const w = mounted.pop()
    w?.unmount()
    await mountDrawer()
    expect(bodyText()).toContain('任务列表获取失败')

    api.fetchDockerTasks.mockResolvedValue({ items: [task()] })
    const retry = Array.from(document.body.querySelectorAll('button')).find(
      (b) => (b.textContent ?? '').trim() === '重试'
    )
    expect(retry).toBeTruthy()
    retry!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await flush()
    expect(bodyText()).toContain('uni-center-core')
  })
})

describe('任务中心 · 状态过滤（服务端过滤）', () => {
  it('切到「进行中」带 status=pending 重拉，「已结束」带 status=done', async () => {
    api.fetchDockerTasks.mockResolvedValue({ items: FOUR_ITEMS })
    const w = await mountDrawer()

    const radio = w.findComponent({ name: 'ElRadioGroup' })
    expect(radio.exists()).toBe(true)

    // 服务端过滤：切换即带 query 重拉；空结果给筛选后的空态文案。
    api.fetchDockerTasks.mockResolvedValue({ items: [] })
    radio.vm.$emit('update:modelValue', 'pending')
    await flush()
    expect(api.fetchDockerTasks).toHaveBeenLastCalledWith({ status: 'pending' })
    expect(bodyRows()).toHaveLength(0)
    expect(bodyText()).toContain('没有进行中的任务')

    api.fetchDockerTasks.mockResolvedValue({ items: [task(), task({ ref: 'r2' })] })
    radio.vm.$emit('update:modelValue', 'done')
    await flush()
    expect(api.fetchDockerTasks).toHaveBeenLastCalledWith({ status: 'done' })
    expect(bodyRows()).toHaveLength(2)
  })

  it('抽屉关着时筛选不触发请求（筛选控件只在打开时可见可点）', async () => {
    api.fetchDockerTasks.mockResolvedValue({ items: [] })
    const w = mount(TaskCenterDrawer, {
      props: { modelValue: false },
      global: { stubs: { ArtSvgIcon: ART_ICON } }
    })
    mounted.push(w)
    await flush()
    expect(api.fetchDockerTasks).not.toHaveBeenCalled()
  })
})

describe('任务中心 · 5 秒轮询（打开期间；关抽屉/失活停表）', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    api.fetchDockerTasks.mockResolvedValue({ items: FOUR_ITEMS })
  })

  async function mountAndFlush() {
    const w = mount(TaskCenterDrawer, {
      props: { modelValue: true },
      global: { stubs: { ArtSvgIcon: ART_ICON } }
    })
    mounted.push(w)
    await flush()
    return w
  }

  it('打开即拉一次，此后每 5 秒静默一轮；关抽屉停表', async () => {
    const w = await mountAndFlush()
    expect(api.fetchDockerTasks).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(5_000)
    expect(api.fetchDockerTasks).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(5_000)
    expect(api.fetchDockerTasks).toHaveBeenCalledTimes(3)

    await w.setProps({ modelValue: false })
    await vi.advanceTimersByTimeAsync(15_000)
    expect(api.fetchDockerTasks).toHaveBeenCalledTimes(3)

    // 重新打开：立即补拉 + 重起表（不是等下一个 5 秒）。
    await w.setProps({ modelValue: true })
    await flush()
    expect(api.fetchDockerTasks).toHaveBeenCalledTimes(4)
    await vi.advanceTimersByTimeAsync(5_000)
    expect(api.fetchDockerTasks).toHaveBeenCalledTimes(5)
  })

  it('后台标签页（document.hidden）跳过 tick，回前台恢复', async () => {
    await mountAndFlush()
    expect(api.fetchDockerTasks).toHaveBeenCalledTimes(1)

    Object.defineProperty(document, 'hidden', { value: true, configurable: true })
    await vi.advanceTimersByTimeAsync(10_000)
    expect(api.fetchDockerTasks).toHaveBeenCalledTimes(1)

    Object.defineProperty(document, 'hidden', { value: false, configurable: true })
    await vi.advanceTimersByTimeAsync(5_000)
    expect(api.fetchDockerTasks).toHaveBeenCalledTimes(2)
  })

  it('keep-alive 失活停表、激活回来补拉；卸载停表', async () => {
    let toggle = () => {}
    const host = defineComponent({
      setup() {
        const show = ref(true)
        toggle = () => (show.value = !show.value)
        return () =>
          h(KeepAlive, null, () => (show.value ? h(TaskCenterDrawer, { modelValue: true }) : null))
      }
    })
    const w = mount(host, { global: { stubs: { ArtSvgIcon: ART_ICON } } })
    mounted.push(w)
    await flush()
    expect(api.fetchDockerTasks).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(5_000)
    expect(api.fetchDockerTasks).toHaveBeenCalledTimes(2)

    toggle() // 失活（worktab 缓存页面实例）
    await vi.advanceTimersByTimeAsync(10_000)
    expect(api.fetchDockerTasks).toHaveBeenCalledTimes(2)

    toggle() // 激活回来：补拉 + 重起表
    await flush()
    expect(api.fetchDockerTasks).toHaveBeenCalledTimes(3)
    await vi.advanceTimersByTimeAsync(5_000)
    expect(api.fetchDockerTasks).toHaveBeenCalledTimes(4)

    w.unmount()
    mounted.splice(mounted.indexOf(w), 1)
    await vi.advanceTimersByTimeAsync(10_000)
    expect(api.fetchDockerTasks).toHaveBeenCalledTimes(4)
  })
})

describe('任务中心 · 拉取进度内联（pending 且 image:pull 可展开）', () => {
  const PULL_ITEMS: DockerTaskItem[] = [
    task({
      ref: 'rp',
      status: 'pending',
      action: 'image:pull',
      target: 'nginx:latest',
      summary: ''
    })
  ]

  beforeEach(() => {
    api.fetchDockerTasks.mockResolvedValue({ items: PULL_ITEMS })
  })

  it('展开即连进度流（pending 期间即可接入）；逐层折叠渲染；收起即断流', async () => {
    usePullStream([
      jf({ id: LA, status: 'Downloading', current: 1024, total: 4096 }),
      jf({ id: LB, status: 'Pull complete' })
    ])
    await mountDrawer()

    // 非拉取行/已终态行不出现进度区；pending 拉取行有展开开关。
    expect(bodyText()).not.toContain('正在等待进度')

    await clickToggle(0)
    expect(api.openDockerPullStream).toHaveBeenCalledTimes(1)
    const [hostId, cmdRef, signal] = api.openDockerPullStream.mock.calls[0] as unknown as [
      string,
      string,
      AbortSignal
    ]
    expect(hostId).toBe('h1')
    expect(cmdRef).toBe('rp')
    expect(signal.aborted).toBe(false)

    // 逐层折叠：两行、同层 id 短形态、终态层不再画条。
    await flush()
    expect(document.body.querySelectorAll('.tpp-layer')).toHaveLength(2)
    expect(bodyText()).toContain('aaaa1111bbbb')
    expect(bodyText()).toContain('Pull complete')
    expect(bodyText()).toContain('1/2 层完成')
    // 收起即取消的语义说在明处（端点契约：客户端断开 = 服务端取消拉取）。
    expect(bodyText()).toContain('收起即取消该拉取')

    await clickToggle(0) // 收起 = Abort
    expect(signal.aborted).toBe(true)
    expect(document.body.querySelectorAll('.tpp-layer')).toHaveLength(0)
  })

  it('关抽屉不断进度流（监控窗口关掉 ≠ 放弃拉取）；卸载才断流（= 取消）', async () => {
    const scripted = usePullStream([
      jf({ id: LA, status: 'Downloading', current: 512, total: 1024 })
    ])
    const w = await mountDrawer()
    await clickToggle(0)
    expect(api.openDockerPullStream).toHaveBeenCalledTimes(1)

    await w.setProps({ modelValue: false })
    await flush()
    expect(scripted.state.signal?.aborted).toBe(false)

    // 组件卸载（页面销毁/收起）走 abort —— 断开即取消（端点契约）。
    w.unmount()
    mounted.splice(mounted.indexOf(w), 1)
    expect(scripted.state.signal?.aborted).toBe(true)
  })

  it('进行中的拉取落定后，展开态就地接上终态结论句（不用再点一次）', async () => {
    const seq: DockerTaskItem[][] = [PULL_ITEMS]
    api.fetchDockerTasks.mockImplementation(async () => ({ items: seq[seq.length - 1]! }))
    usePullStream([jf({ id: LA, status: 'Downloading', current: 8, total: 16 })])
    await mountDrawer()
    await clickToggle(0)
    expect(document.body.querySelectorAll('.tpp-layer')).toHaveLength(1)

    // 下一轮列表（手动刷新驱动，等价于轮询到的下一帧）：同一 ref 已成功、结论句就位。
    seq.push([
      task({
        ref: 'rp',
        status: 'succeeded',
        action: 'image:pull',
        target: 'nginx:latest',
        summary: '已拉取 nginx:latest'
      })
    ])
    const refresh = Array.from(document.body.querySelectorAll('button')).find(
      (b) => (b.textContent ?? '').trim() === '刷新'
    )
    expect(refresh).toBeTruthy()
    refresh!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await flush()
    expect(bodyText()).toContain('已拉取 nginx:latest')
    // 进度区退位（终态后不再挂着流视图），结论句留在展开位。
    expect(document.body.querySelectorAll('.tpp-layer')).toHaveLength(0)
  })
})

describe('任务中心 · 入口权限门控（docker-page 主机条按钮）', () => {
  async function mountPage(): Promise<{ w: VueWrapper; router: Router }> {
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: '/', component: { template: '<div />' } }]
    })
    await router.push('/')
    await router.isReady()
    // DockerPage 是子组件：主机上下文由宿主 provide（页面即提供者的常态形态）。
    const host = defineComponent({
      name: 'HostRoot',
      setup() {
        provideDockerHost()
        return () => h(DockerPage, { loading: false })
      }
    })
    const w = mount(host, {
      global: { plugins: [router], stubs: { ArtSvgIcon: ART_ICON } }
    })
    mounted.push(w)
    await flush()
    return { w, router }
  }

  it('有 docker:list：主机条渲染「任务」按钮，点击打开抽屉并拉列表', async () => {
    auth.list = true
    api.fetchDockerTasks.mockResolvedValue({ items: [task()] })
    const { w } = await mountPage()

    const btn = Array.from(w.findAll('button')).find((b) => (b.text() ?? '').trim() === '任务')
    expect(btn).toBeTruthy()
    expect(w.findComponent({ name: 'DockerTaskCenterDrawer' }).exists()).toBe(true)
    expect(api.fetchDockerTasks).not.toHaveBeenCalled() // 挂着 ≠ 拉过（打开才拉）

    btn!.element.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await flush()
    expect(api.fetchDockerTasks).toHaveBeenCalledTimes(1)
    expect(document.body.textContent).toContain('任务中心')
  })

  it('无 docker:list：按钮与抽屉实例都不渲染（不渲染 ≠ 禁用）', async () => {
    auth.list = false
    const { w } = await mountPage()

    expect(Array.from(w.findAll('button')).map((b) => (b.text() ?? '').trim())).not.toContain(
      '任务'
    )
    expect(w.findComponent({ name: 'DockerTaskCenterDrawer' }).exists()).toBe(false)
  })
})
