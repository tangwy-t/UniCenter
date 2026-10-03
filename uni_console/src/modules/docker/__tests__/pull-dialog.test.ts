// @vitest-environment jsdom
/**
 * 拉取进度对话框（4b）的接线测试：组件把哪条数据接到哪个控件、双通道生命周期。
 *
 * 纯函数层（行解析/折叠/校验/文案）在 pull-progress.test.ts；这里钉的是：
 *   - 输入校验挡提交（非法引用不派发指令）；
 *   - 开始拉取：受理 image:pull（注册表现状 = 无确认档，直接派发）→ **pending 期间
 *     即开进度流** + 立即轮询 result（双通道并行）；
 *   - 逐层渲染：同层折叠成一行、确定/不确定两种进度条、终态勾；
 *   - 受理失败（409 在飞）：分类句就地显示、留在输入态可重试；
 *   - 终态双通道收尾：流 eof 先到（等 result 的提示可见）→ result 终态 → 结论句
 *     （成功 = 镜像名 + 耗时）→ 关闭后双次重拉（立即 + 1.5s 落定）；
 *   - 取消：调显式取消端点（**不断流**）→ 进度态说「已请求取消」→ result 结论句收尾；
 *   - 关对话框：断流（只是停止观看，**不下发取消**）+ 停轮询 + 不重拉；
 *   - 非模态：不锁滚动、点页面别处不收窗（包壳挂指针穿透锚点类）；
 *   - 重新打开：整表重置（上一次的层表/结论不进新一场）。
 *
 * ── 流替身怎么造 ───────────────────────────────────────────────────
 * openDockerPullStream 的 mock 返回一个 Response 替身：按脚本逐行吐 chunk，读尽后
 * **挂住**（模拟「连着但暂无数据」的长连接），signal abort 时以 AbortError 拒绝
 * 挂住的 read —— 与真 fetch 在 signal 断开时的行为同形（取消路径全靠它验证）。
 * result 轮询的 1 秒间隔（pollDelay）是真实计时，涉轮询的用例各等 ~1.1s。
 *
 * ElDialog 默认**原地**渲染（append-to-body 未开，与 action-confirm 同挂法），
 * 断言走 wrapper 查询（w.text / w.findAll），不必查 document.body。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import { defineComponent, h } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'
import { HttpError } from '@/utils/http/error'

const api = vi.hoisted(() => ({
  sendDockerCmd: vi.fn(),
  fetchDockerCmdResult: vi.fn(),
  cancelDockerCmd: vi.fn(),
  openDockerPullStream: vi.fn(),
  // 4c 凭据下拉的清单来源（仅 docker:config 用户才会真调）。
  fetchDockerRegistries: vi.fn()
}))
vi.mock('../api', () => ({ ...api, default: undefined }))

/**
 * useAuth 的可配置替身：凭据下拉（4c）只对 docker:config 渲染。**默认关** ——
 * 既有用例全部跑在「无凭据权限」的基线上：下拉不出现、载荷 options 逐字是 {}，
 * 与 4b 的行为一字不差（权限门控的回归由 registry-creds.test.ts 的开关用例守）。
 */
const auth = vi.hoisted(() => ({ config: false }))
vi.mock('@/hooks/core/useAuth', () => ({
  useAuth: () => ({
    hasAuth: (perm: string) => perm === 'docker:config' && auth.config,
    hasAnyAuth: (perms: string[]) => perms.length === 0
  })
}))

import PullProgressDialog from '../components/pull-progress-dialog.vue'

/** ArtSvgIcon 的轻量替身：保住可断言的 DOM（真组件靠 unplugin 注册）。 */
const STUBS = {
  ArtSvgIcon: defineComponent({
    name: 'ArtSvgIcon',
    props: { icon: { type: String, default: '' } },
    setup: (props) => () => h('i', { class: 'pp-icon-stub', 'data-icon': props.icon })
  })
}

const enc = new TextEncoder()

/** 一条进度行（t 给合法默认；行尾带 \n —— NDJSON 每行以换行收尾）。 */
function jf(o: Record<string, unknown>): string {
  return `${JSON.stringify({ t: 1700000000000, ...o })}\n`
}

const LA = 'aaaa1111bbbb2222cccc3333dddd4444eeee5555ffff66667777888899990000'
const LB = '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef'

/**
 * 流替身：按脚本逐行吐 chunk；读尽后挂住，abort 唤醒（AbortError）。
 * state.signal 记下组件传入的 signal —— 断言「关对话框 = 断流（只是停止观看）」
 * 以及「取消 ≠ 断流」靠它。
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

/** 把流替身接进 openDockerPullStream 的 mock（signal 一并捕获）。 */
function useStream(lines: string[]) {
  const scripted = scriptedStream(lines)
  api.openDockerPullStream.mockImplementation(
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
  // ElSelect（4c 凭据下拉）依赖 popper 的尺寸观测，jsdom 里没有 —— 桩掉
  //（create-drawer / page-render 的同款手法）。
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
  api.cancelDockerCmd.mockReset()
  api.openDockerPullStream.mockReset()
  api.fetchDockerRegistries.mockReset()
  auth.config = false
  refresh.mockClear()
  api.sendDockerCmd.mockResolvedValue({ ref: 'r100' })
  api.fetchDockerCmdResult.mockResolvedValue({ status: 'pending' })
  api.cancelDockerCmd.mockResolvedValue({ ref: 'r100' })
  api.fetchDockerRegistries.mockResolvedValue({ list: [] })
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
  const w = mount(PullProgressDialog, {
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

/** 失败时才进断言信息的现场快照（相位 / 输入模型的活性 / 按钮 enabled 态）。 */
function dumpState(w: VueWrapper): string {
  const inputs = w.findAll('input').map((i) => ({
    ph: i.attributes('placeholder') ?? '',
    v: (i.element as HTMLInputElement).value
  }))
  const elInput = w.findComponent({ name: 'ElInput' })
  const elModel = elInput.exists() ? elInput.props('modelValue') : '(无输入框:非输入态)'
  const btns = buttons(w).map((b) => ({
    t: b.text,
    d: (b.el.element as HTMLButtonElement).disabled
  }))
  const phase = w.find('.pp-input').exists()
    ? 'input'
    : w.find('.pp-progress').exists()
      ? 'pulling'
      : 'result'
  return `｜phase=${phase}｜elModel=${JSON.stringify(elModel)}｜inputs=${JSON.stringify(inputs)}｜buttons=${JSON.stringify(btns)}`
}

async function click(w: VueWrapper, label: string, opts: { awaitEnabled?: boolean } = {}) {
  const btn = buttons(w).find((b) => b.text === label)
  expect(btn, `按钮「${label}」应已渲染${btn ? '' : dumpState(w)}`).toBeTruthy()
  // 等按钮**真的可点**再点（默认）：ElButton 在 disabled/loading 时会【吞掉 click】
  // （其 handleClick 直接 stopPropagation 返回，不 emit），而 dispatchEvent 不受 DOM
  // disabled 属性阻止 —— 状态晚一拍时点下去静默无事，断言随后在别处炸开。
  // 等判据（DOM 的 disabled = 最近一次渲染的 canStart/accepting）落地，再点。
  // 少数用例**要的就是「点被禁用的按钮」**（校验挡提交的负向面）：传 { awaitEnabled: false }。
  // 另一条历史成因（放大器下实测）是**宿主墙钟回拨**踩中 Vue 的事件时间戳守卫，
  // 合成事件被静默丢弃 —— 已由 src/test/setup.ts 全局豁免（成因、证据与回归守卫见那里）。
  if (opts.awaitEnabled !== false) {
    await vi.waitUntil(() => !(btn!.el.element as HTMLButtonElement).disabled, { timeout: 5000 })
  }
  await btn!.el.trigger('click')
  await flush()
}

async function setRef(w: VueWrapper, value: string) {
  const el = w.find('input[placeholder="例如 nginx:latest"]')
  expect(el.exists(), '镜像引用输入框应已渲染').toBe(true)
  await el.setValue(value)
  await flush()
}

/**
 * 等 result 轮询的第 n 次调用落地（1s 间隔是组件的真实计时）。
 *
 * 为什么不睡固定墙钟（旧写法 1.1s）：睡够买的是「时间过去了」，而不是「轮询到了」——
 * 机器一忙（全量套件并发抢核）计时器回调会被推迟，1.1s 就可能短于组件的 1s sleep，
 * 于是偶发「第二轮还没发」的假红。判据换成事实（调用数）后，慢就多等几轮。
 */
async function waitPolls(n: number) {
  await vi.waitUntil(() => api.fetchDockerCmdResult.mock.calls.length >= n, { timeout: 5000 })
  await flush()
}

describe('输入态', () => {
  it('非法引用当场显错，点开始不派发（校验在前端先挡一道）', async () => {
    const w = await mountDialog()
    await setRef(w, '_bad')
    expect(w.text()).toContain('镜像引用不合法')
    await click(w, '开始拉取', { awaitEnabled: false })
    expect(api.sendDockerCmd).not.toHaveBeenCalled()

    await setRef(w, 'nginx:latest')
    expect(w.text()).not.toContain('镜像引用不合法')
  })

  it('受理失败（409 在飞）：分类句就地显示，留在输入态可重试', async () => {
    api.sendDockerCmd.mockRejectedValue(new HttpError('该目标上已有同一条操作在执行', 409))
    const w = await mountDialog()
    await setRef(w, 'nginx:latest')
    await click(w, '开始拉取')
    expect(w.text()).toContain('该目标上已有同一条操作在执行')
    expect(w.text()).toContain('开始拉取') // 输入态还在（可改可重试）
    expect(api.openDockerPullStream).not.toHaveBeenCalled() // 没受理就不开流
  })
})

describe('开始拉取（受理 → 双通道并行）', () => {
  it('受理 image:pull（无确认档，直接派发），pending 期间即开流 + 立即轮询', async () => {
    useStream([])
    usePolls([{ status: 'pending' }])
    const w = await mountDialog()
    await setRef(w, 'nginx:latest')
    await click(w, '开始拉取')

    expect(api.sendDockerCmd, dumpState(w)).toHaveBeenCalledWith('h1', {
      action: 'image:pull',
      target: 'nginx:latest',
      options: {}
    })
    expect(api.openDockerPullStream).toHaveBeenCalledWith('h1', 'r100', expect.any(AbortSignal))
    expect(api.fetchDockerCmdResult).toHaveBeenCalledWith('h1', 'r100')
    expect(w.text()).toContain('nginx:latest') // 进度态头部
    expect(w.text()).toContain('正在等待进度') // 还没有帧
    expect(buttons(w).some((b) => b.text === '取消拉取')).toBe(true)
  })

  it('hostId 受理时钉死：切 prop 不改在途指令的主机（一次指令只属于受理它的主机）', async () => {
    useStream([])
    usePolls([{ status: 'pending' }])
    const w = await mountDialog()
    await setRef(w, 'nginx:latest')
    await click(w, '开始拉取')
    await w.setProps({ hostId: 'h2' })
    await flush()
    // 轮询与流都仍打 h1（受理时的主机），不追着 prop 跑。
    expect(api.fetchDockerCmdResult).toHaveBeenCalledWith('h1', 'r100')
    expect(api.fetchDockerCmdResult).not.toHaveBeenCalledWith('h2', 'r100')
  })
})

describe('逐层渲染', () => {
  it('同层折叠成一行（后帧是更新不是新行）；确定条 + 终态勾各就各位', async () => {
    useStream([
      jf({ status: 'Pulling from library/nginx:latest' }),
      jf({ id: LA, status: 'Pulling fs layer' }),
      jf({ id: LA, status: 'Downloading', current: 500, total: 1000 }),
      jf({ id: LB, status: 'Already exists' })
    ])
    usePolls([{ status: 'pending' }])
    const w = await mountDialog()
    await setRef(w, 'nginx:latest')
    await click(w, '开始拉取')
    await flush()

    const text = w.text()
    expect(text).toContain('aaaa1111bbbb') // 层短 id（等宽列）
    expect(text).toContain('0123456789ab')
    expect(text).toContain('Pulling from library/nginx:latest') // 消息行提示
    expect(text).toContain('已下载 500 B / 1000 B') // 下载段汇总
    expect(text).toContain('1/2 层完成')
    expect(text).toContain('Downloading') // LA 的最新一帧
    expect(text).not.toContain('Pulling fs layer') // 旧帧被折叠掉

    const bars = w.findAllComponents({ name: 'ElProgress' })
    expect(bars.length).toBe(1) // LA 未终态有条；LB 终态无条
    expect(bars[0]!.props('percentage')).toBe(50)
    expect(bars[0]!.props('indeterminate')).toBe(false)
    // LB 的终态勾（ArtSvgIcon 替身）
    expect(w.find('.pp-layer__check').exists()).toBe(true)
  })

  it('无 total 的层走不确定态动画（不是假装 0%）', async () => {
    useStream([jf({ id: LA, status: 'Pulling fs layer' })])
    usePolls([{ status: 'pending' }])
    const w = await mountDialog()
    await setRef(w, 'nginx:latest')
    await click(w, '开始拉取')
    await flush()

    const bars = w.findAllComponents({ name: 'ElProgress' })
    expect(bars.length).toBe(1)
    expect(bars[0]!.props('indeterminate')).toBe(true)
    expect(bars[0]!.props('percentage')).toBe(100) // EP 的不确定条按宽度铺满再流动
    expect(w.text()).not.toContain('已下载') // 没有字节口径就不给汇总
  })
})

describe('终态双通道收尾', () => {
  it('流 eof 先到（等 result 提示可见）→ result 终态 → 结论句；关闭后双次重拉', async () => {
    useStream([
      jf({ id: LA, status: 'Downloading', current: 1000, total: 1000 }),
      jf({ id: LA, status: 'Pull complete' }),
      jf({ status: 'Status: Downloaded newer image for nginx:latest' }),
      jf({ done: true, eof: true })
    ])
    usePolls([{ status: 'pending' }, { status: 'succeeded', detail: '' }])
    const w = await mountDialog()
    await setRef(w, 'nginx:latest')
    await click(w, '开始拉取')

    // 双通道时序：eof 已到、result 还在 pending —— 对话框说清「在等什么」。
    expect(w.text()).toContain('正在等待指令结果')

    await waitPolls(2) // 第二轮轮询落地（pollDelay(0) = 1s 的真计时）
    expect(api.fetchDockerCmdResult).toHaveBeenCalledTimes(2)
    expect(w.text()).toContain('已拉取 nginx:latest')
    expect(w.text()).toContain('耗时')

    // 关闭：结论落定后退出，页脚路径（不依赖父组件回写 prop）。
    await click(w, '关闭')
    expect(w.emitted('update:modelValue')?.at(-1)).toEqual([false])
    expect(refresh).toHaveBeenCalledTimes(1) // 立即重拉
    // 1.5s 落定重拉：等「第二次 refresh 到了」这个事实（不睡满 1.6s —— 墙钟在负载下会漂，
    // 而「到没到」不漂）。
    await vi.waitUntil(() => refresh.mock.calls.length >= 2, { timeout: 5000 })
    expect(refresh).toHaveBeenCalledTimes(2) // 落定重拉（双次纪律）
  })

  it('失败：结论句原文（服务端 error 透传），关闭不重拉', async () => {
    useStream([jf({ error: 'manifest unknown', eof: true })])
    usePolls([{ status: 'failed', error: '拉取镜像失败： manifest unknown' }])
    const w = await mountDialog()
    await setRef(w, 'nginx:latest')
    await click(w, '开始拉取')
    await flush()

    expect(w.text()).toContain('拉取镜像失败： manifest unknown')
    await click(w, '关闭')
    expect(refresh).not.toHaveBeenCalled() // 失败不重拉
  })
})

describe('非模态（进度不挡页面）', () => {
  it('对话框非模态 + 不锁滚动 + 点页面别处不收窗（包壳挂指针穿透锚点类）', async () => {
    const w = await mountDialog()
    const dlg = w.findComponent({ name: 'ElDialog' })
    expect(dlg.props('modal'), '去掉模态阻断').toBe(false)
    expect(dlg.props('lockScroll'), '不锁页面滚动').toBe(false)
    expect(dlg.props('closeOnClickModal'), '点页面别处不收起观看窗').toBe(false)
    expect(String(dlg.props('modalClass')), '包壳类名 = 指针穿透规则的锚点').toContain(
      'docker-progress-nonblocking'
    )
  })
})

describe('取消路径（独立于观看的显式动作）', () => {
  it('取消拉取：调取消端点（不断流）→ 进度态说「已请求取消」→ result 结论句收尾', async () => {
    const scripted = useStream([jf({ id: LA, status: 'Downloading', current: 100, total: 1000 })])
    usePolls([{ status: 'pending' }, { status: 'failed', error: '拉取已取消' }])
    const w = await mountDialog()
    await setRef(w, 'nginx:latest')
    await click(w, '开始拉取')

    await click(w, '取消拉取')
    expect(api.cancelDockerCmd, '取消是显式端点，不是断流').toHaveBeenCalledWith('h1', 'r100')
    expect(w.text()).toContain('已请求取消，正在等待指令收尾') // 不承诺因果的措辞
    expect(scripted.state.signal?.aborted, '取消 ≠ 停止观看：进度流照常连着').toBe(false)

    await waitPolls(2) // result 的「拉取已取消」结论句落地
    expect(w.text()).toContain('拉取已取消')
    expect(refresh).not.toHaveBeenCalled()
  })

  it('取消请求失败（403 无权取消）：就地结论句，按钮可重试、流不受扰', async () => {
    const scripted = useStream([jf({ id: LA, status: 'Downloading', current: 100, total: 1000 })])
    usePolls([{ status: 'pending' }])
    api.cancelDockerCmd.mockRejectedValue(new HttpError('无权取消该指令', 403))
    const w = await mountDialog()
    await setRef(w, 'nginx:latest')
    await click(w, '开始拉取')

    await click(w, '取消拉取')
    expect(w.text()).toContain('无权取消该指令')
    expect(scripted.state.signal?.aborted).toBe(false)
    expect(
      buttons(w).some((b) => b.text === '取消拉取'),
      '失败后按钮回到可点状态'
    ).toBe(true)
  })

  it('取消请求已发出后拉取恰好完成：结论句如实说成功（不代答「已取消」）', async () => {
    useStream([jf({ done: true, eof: true })])
    usePolls([{ status: 'pending' }, { status: 'succeeded', detail: '' }])
    const w = await mountDialog()
    await setRef(w, 'nginx:latest')
    await click(w, '开始拉取')
    await flush()
    await click(w, '取消拉取')
    expect(w.text()).toContain('已请求取消')
    await waitPolls(2)
    // 竞态的另一半：取消是 best-effort，记录的真终态（成功）说了算。
    expect(w.text()).toContain('已拉取 nginx:latest')
  })
})

describe('私库凭据选择（4c）', () => {
  /** 本组的轻量通道替身：流接入失败（404）→ 退「无进度、等结果」；result 直接终态
      （succeeded）→ 无挂起的轮询定时器。断言焦点在**受理载荷**，不在进度渲染。 */
  function lightChannels() {
    api.openDockerPullStream.mockResolvedValue({ ok: false, status: 404 })
    api.fetchDockerCmdResult.mockResolvedValue({ status: 'succeeded', detail: '' })
  }

  it('无 docker:config：下拉不渲染、清单不拉取、载荷与 4b 逐字一致（匿名拉取）', async () => {
    lightChannels()
    const w = await mountDialog()
    await setRef(w, 'nginx:latest')
    expect(w.find('.pp-creds').exists()).toBe(false)
    expect(api.fetchDockerRegistries).not.toHaveBeenCalled()

    await click(w, '开始拉取')
    expect(api.sendDockerCmd, dumpState(w)).toHaveBeenCalledWith('h1', {
      action: 'image:pull',
      target: 'nginx:latest',
      options: {} // 不多一个空值的 registry 字段：协议对空串与缺省同判非法
    })
  })

  it('有 docker:config：开框即拉清单，选中后载荷带 registry（凭据键，不含秘密）', async () => {
    lightChannels()
    auth.config = true
    api.fetchDockerRegistries.mockResolvedValue({
      list: [
        { registry: 'harbor.example.com', username: 'deploy', password: '****' },
        { registry: '192.168.1.10:5000', username: 'admin', password: '****' }
      ]
    })
    const w = await mountDialog()
    expect(api.fetchDockerRegistries).toHaveBeenCalledTimes(1)
    expect(w.find('.pp-creds').exists()).toBe(true)

    await setRef(w, 'harbor.example.com/app:v1')
    const select = w.findComponent({ name: 'ElSelect' })
    expect(select.exists()).toBe(true)
    await select.setValue('harbor.example.com')

    await click(w, '开始拉取')
    expect(api.sendDockerCmd, dumpState(w)).toHaveBeenCalledWith('h1', {
      action: 'image:pull',
      target: 'harbor.example.com/app:v1',
      options: { registry: 'harbor.example.com' }
    })
  })

  it('选了又改回「不使用」：载荷回到 {}（选择是会话内可逆的）', async () => {
    lightChannels()
    auth.config = true
    api.fetchDockerRegistries.mockResolvedValue({
      list: [{ registry: 'harbor.example.com', username: 'deploy', password: '****' }]
    })
    const w = await mountDialog()
    await setRef(w, 'nginx:latest')
    const select = w.findComponent({ name: 'ElSelect' })
    await select.setValue('harbor.example.com')
    await select.setValue('') // 改回「不使用（匿名拉取）」
    await click(w, '开始拉取')
    expect(api.sendDockerCmd, dumpState(w)).toHaveBeenCalledWith('h1', {
      action: 'image:pull',
      target: 'nginx:latest',
      options: {}
    })
  })

  it('清单为空 / 读取失败：就地一句说明，拉取不受影响（匿名照常可用）', async () => {
    lightChannels()
    auth.config = true
    // 空清单：说明「还没有」，不渲染下拉（渲染一个空的也没得选）。
    const w = await mountDialog()
    expect(w.find('.pp-creds__hint').text()).toContain('还没有已保存的仓库凭据')
    expect(w.findComponent({ name: 'ElSelect' }).exists()).toBe(false)

    // 读取失败：换成「读取失败」的说明（凭据只是增强，失败不挡拉取）。
    api.fetchDockerRegistries.mockRejectedValue(new Error('network down'))
    await w.setProps({ modelValue: false })
    await w.setProps({ modelValue: true })
    await flush()
    expect(w.find('.pp-creds__hint').text()).toContain('已保存凭据读取失败')

    await setRef(w, 'nginx:latest')
    await click(w, '开始拉取')
    expect(api.sendDockerCmd, dumpState(w)).toHaveBeenCalledWith('h1', {
      action: 'image:pull',
      target: 'nginx:latest',
      options: {}
    })
  })

  it('凭据选择不跨场次：重新打开回到「不使用」', async () => {
    auth.config = true
    api.fetchDockerRegistries.mockResolvedValue({
      list: [{ registry: 'harbor.example.com', username: 'deploy', password: '****' }]
    })
    const w = await mountDialog()
    await setRef(w, 'nginx:latest')
    await w.findComponent({ name: 'ElSelect' }).setValue('harbor.example.com')
    // 不点「开始拉取」：这里只钉「选择是会话内的」（点了再关会排下 1.5s 的落定重拉，
    // 那是关闭路径的既有语义，与本用例无关；卸载撤销那条纪律见「关闭与重开」）。

    await w.setProps({ modelValue: false })
    await w.setProps({ modelValue: true })
    await flush()
    const select = w.findComponent({ name: 'ElSelect' })
    expect(select.props('modelValue')).toBe('') // 上一场的选择清空（resetAll）
    expect(api.fetchDockerRegistries).toHaveBeenCalledTimes(2) // 每次打开重拉清单（挂载 + 重开）
  })
})

describe('关闭与重开（生命周期收口）', () => {
  it('成功关闭后 1.5s 内卸载：落定重拉随之撤销（页面没了，不留白发请求）', async () => {
    useStream([jf({ done: true, eof: true })])
    usePolls([{ status: 'succeeded', detail: '' }])
    const w = await mountDialog()
    await setRef(w, 'nginx:latest')
    await click(w, '开始拉取')
    await flush()
    await click(w, '关闭')
    expect(refresh).toHaveBeenCalledTimes(1) // 立即重拉

    // 页面收口（卸载）：还没到点的落定重拉随之撤销 —— 跨过 1.5s 窗口仍只有那一次。
    w.unmount()
    mounted.splice(mounted.indexOf(w), 1) // 已卸载：afterEach 不再重复卸载
    await new Promise((r) => setTimeout(r, 1700))
    expect(refresh, '卸载后不该再有白发请求').toHaveBeenCalledTimes(1)
  })

  it('拉取在途关对话框：断流（只是停止观看，不下发 cancel）+ 停轮询 + 不重拉', async () => {
    const scripted = useStream([jf({ id: LA, status: 'Downloading', current: 100, total: 1000 })])
    usePolls([{ status: 'pending' }])
    const w = await mountDialog()
    await setRef(w, 'nginx:latest')
    await click(w, '开始拉取')
    expect(api.fetchDockerCmdResult).toHaveBeenCalledTimes(1) // 第一轮已发

    await w.setProps({ modelValue: false }) // 父组件置 false（页面级收口）
    await flush()
    expect(scripted.state.signal?.aborted, '关对话框只是收起这场观看').toBe(true)
    expect(api.cancelDockerCmd, '关闭 ≠ 取消：取消只由显式按钮触发').not.toHaveBeenCalled()

    // 「轮询已停」是**反向判据**（什么都没发生），只能靠时间窗口举证 —— 这里的时长
    // 是窗口宽度而不是「等待步数」：跨两个轮询间隔（pollDelay(0)=1s）仍无新调用，
    // 窗口越宽结论越强；负载下的计时器推迟只会让窗口更强，不会把结论翻面。
    await new Promise((r) => setTimeout(r, 2200))
    expect(api.fetchDockerCmdResult, '轮询应已停').toHaveBeenCalledTimes(1)
    expect(refresh).not.toHaveBeenCalled()
  })

  it('重新打开：整表重置（上一次的层表/结论/刷新标记不进新一场）', async () => {
    useStream([jf({ done: true, eof: true })])
    usePolls([{ status: 'succeeded', detail: '' }])
    const w = await mountDialog()
    await setRef(w, 'nginx:latest')
    await click(w, '开始拉取')
    await flush()
    expect(w.text()).toContain('已拉取 nginx:latest')

    await w.setProps({ modelValue: false })
    await w.setProps({ modelValue: true })
    await flush()
    expect(w.text()).toContain('开始拉取')
    expect(w.text()).not.toContain('已拉取')
    expect(
      (w.find('input[placeholder="例如 nginx:latest"]').element as HTMLInputElement).value
    ).toBe('')
    expect(api.openDockerPullStream).toHaveBeenCalledTimes(1) // 新一场没有自动开流
  })
})
