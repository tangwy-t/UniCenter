// @vitest-environment jsdom
/**
 * 推送进度对话框（P2 分发闭环）的接线测试：组件把哪条数据接到哪个控件、
 * 双通道生命周期。
 *
 * 纯函数层（推送语义的层折叠 flavor）在 pull-progress.test.ts 的推送组；这里钉的是：
 *   - 输入面：预填引用（详情页入口）、选项来自该主机镜像清单（可搜索）、
 *     手输兜底与引用校验（非法引用不派发）；
 *   - 载荷形状：{ action: image:push, target, options }，凭据选中才带 registry；
 *   - 凭据面（4c 同款）：无 docker:config 不渲染下拉不拉清单、清单空/失败的就地
 *     说明、选择不跨场次；
 *   - 开始推送：受理（注册表现状 = 无确认档，直接派发）→ **pending 期间即开推送流**
 *     + 立即轮询 result（双通道并行）；
 *   - 逐层渲染：同层折叠、Pushing 字节条、Pushed 终态勾、「已上传」汇总；
 *   - 终态收尾：流 eof 先到 → result 终态 → 结论句；**推送不重拉**（本地清单没变）；
 *   - 取消：断流（Abort）→ 「已取消」→ result 结论句收尾；
 *   - 关对话框：断流 + 停轮询；重新打开：整表重置（预填与凭据选择回到初始态）。
 *
 * 流替身与轮询脚本同 build-dialog.test.ts 的一套（读尽后挂住、abort 唤醒）。
 * ElSelect 的断言走 findComponent（VTU 的 setValue 对组件 = 发 update:modelValue）。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import { defineComponent, h } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'
import { HttpError } from '@/utils/http/error'

const api = vi.hoisted(() => ({
  sendDockerCmd: vi.fn(),
  fetchDockerCmdResult: vi.fn(),
  openDockerPushStream: vi.fn(),
  // 凭据下拉的清单来源（仅 docker:config 用户才会真调）。
  fetchDockerRegistries: vi.fn()
}))
vi.mock('../api', () => ({ ...api, default: undefined }))

/**
 * useAuth 的可配置替身：凭据下拉（4c）只对 docker:config 渲染。**默认关** ——
 * 输入面用例全部跑在「无凭据权限」的基线上：下拉不出现、载荷 options 逐字是 {}
 *（与 pull-dialog.test.ts 同一条基线纪律）。
 */
const auth = vi.hoisted(() => ({ config: false }))
vi.mock('@/hooks/core/useAuth', () => ({
  useAuth: () => ({
    hasAuth: (perm: string) => perm === 'docker:config' && auth.config,
    hasAnyAuth: (perms: string[]) => perms.length === 0
  })
}))

import PushProgressDialog from '../components/push-progress-dialog.vue'

/** ArtSvgIcon 的轻量替身：保住可断言的 DOM（真组件靠 unplugin 注册）。 */
const STUBS = {
  ArtSvgIcon: defineComponent({
    name: 'ArtSvgIcon',
    props: { icon: { type: String, default: '' } },
    setup: (props) => () => h('i', { class: 'sp-icon-stub', 'data-icon': props.icon })
  })
}

const enc = new TextEncoder()

/** 一条进度行（t 给合法默认；行尾带 \n —— NDJSON 每行以换行收尾）。 */
function jf(o: Record<string, unknown>): string {
  return `${JSON.stringify({ t: 1700000000000, ...o })}\n`
}

const LA = 'aaaa1111bbbb2222cccc3333dddd4444eeee5555ffff66667777888899990000'
const LB = '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef'

/** 该主机的镜像清单（options 的数据源；页面级快照的形态）。 */
const IMAGES = [
  { id: 'i1', repoTags: ['app:v1', 'app:v2'], sizeMb: 91.8, inUse: true, dangling: false },
  { id: 'i2', repoTags: ['mysql:8'], sizeMb: 596.2, inUse: false, dangling: false }
]

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

/** 把流替身接进 openDockerPushStream 的 mock（signal 一并捕获）。 */
function useStream(lines: string[]) {
  const scripted = scriptedStream(lines)
  api.openDockerPushStream.mockImplementation(
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

beforeEach(() => {
  // ElSelect 依赖 popper 的尺寸观测，jsdom 里没有 —— 桩掉（同款手法）。
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
  api.openDockerPushStream.mockReset()
  api.fetchDockerRegistries.mockReset()
  auth.config = false
  api.sendDockerCmd.mockResolvedValue({ ref: 'r300' })
  api.fetchDockerCmdResult.mockResolvedValue({ status: 'pending' })
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

/**
 * 挂载形态对齐详情页的真实接线：**不传 refresh** —— 推送不改本地清单，无物可重
 * 读（这条接线口径由 phase-gate 的源码断言钉住；对话框组件的 refresh 是留给
 * 将来确有页面事实会变的入口的可选项）。
 */
async function mountDialog(props: Record<string, unknown> = {}) {
  const w = mount(PushProgressDialog, {
    props: { modelValue: true, hostId: 'h1', ...props },
    global: { stubs: STUBS }
  })
  mounted.push(w)
  await flush()
  return w
}

/** 挂「详情页入口」形态：该主机清单 + 预填镜像引用。 */
async function mountDetail() {
  return mountDialog({ images: IMAGES, initialTarget: 'app:v1' })
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

/** 镜像选择框（凭据下拉在场时它是第一个 ElSelect —— 模板顺序在前）。 */
function targetSelect(w: VueWrapper) {
  const select = w.findComponent({ name: 'ElSelect' })
  expect(select.exists(), '镜像选择框应已渲染').toBe(true)
  return select
}

/** 凭据下拉（第二个 ElSelect；仅 canConfig 且清单非空时在场）。 */
function credsSelect(w: VueWrapper) {
  return w.findAllComponents({ name: 'ElSelect' })[1]
}

async function choose(w: VueWrapper, value: string) {
  await targetSelect(w).setValue(value)
  await flush()
}

/** 等一轮 result 轮询间隔（pollDelay(0) = 1s，真实计时）。 */
const waitPoll = () => new Promise((r) => setTimeout(r, 1100))

describe('输入面：本地镜像选择', () => {
  it('预填引用（详情页入口）：选项来自该主机清单的全部仓库标签', async () => {
    const w = await mountDetail()
    expect(targetSelect(w).props('modelValue')).toBe('app:v1')
    // 一枚镜像的多个标签各自是独立的推送引用，全部进选项。
    const options = w.findAllComponents({ name: 'ElOption' })
    expect(options.map((o) => o.props('value'))).toEqual(['app:v1', 'app:v2', 'mysql:8'])
  })

  it('清单未含预填引用时也并入选项（快照陈旧/未到时详情页入口仍可用）', async () => {
    const w = await mountDialog({ images: [], initialTarget: 'app:v1' })
    const options = w.findAllComponents({ name: 'ElOption' })
    expect(options.map((o) => o.props('value'))).toEqual(['app:v1'])
    expect(w.text()).toContain('主机镜像清单未就绪')
  })

  it('空选择靠按钮禁用表达；手输的非法引用当场显错、不派发', async () => {
    const w = await mountDialog({ images: [], initialTarget: '' })
    const start = buttons(w).find((b) => b.text === '开始推送')
    expect(start, '「开始推送」应已渲染').toBeTruthy()
    expect((start!.el.element as HTMLButtonElement).disabled).toBe(true)

    await choose(w, 'bad ref!')
    expect(w.text()).toContain('镜像引用不合法')
    await click(w, '开始推送')
    expect(api.sendDockerCmd).not.toHaveBeenCalled()

    await choose(w, 'harbor.example.com/app:v1')
    expect(w.text()).not.toContain('镜像引用不合法')
  })

  it('选了别的镜像再开始：target 是选中值（预填只是初始态，选择权在用户）', async () => {
    useStream([])
    usePolls([{ status: 'pending' }])
    const w = await mountDetail()
    await choose(w, 'mysql:8')
    await click(w, '开始推送')
    expect(api.sendDockerCmd).toHaveBeenCalledWith('h1', {
      action: 'image:push',
      target: 'mysql:8',
      options: {}
    })
  })
})

describe('开始推送（受理 → 双通道并行）', () => {
  it('受理 image:push（无确认档，直接派发），pending 期间即开流 + 立即轮询', async () => {
    useStream([])
    usePolls([{ status: 'pending' }])
    const w = await mountDetail()
    await click(w, '开始推送')

    expect(api.openDockerPushStream).toHaveBeenCalledWith('h1', 'r300', expect.any(AbortSignal))
    expect(api.fetchDockerCmdResult).toHaveBeenCalledWith('h1', 'r300')
    expect(w.text()).toContain('app:v1') // 进度态头部
    expect(w.text()).toContain('正在等待进度') // 还没有帧
    expect(buttons(w).some((b) => b.text === '取消推送')).toBe(true)
  })

  it('hostId 受理时钉死：切 prop 不改在途指令的主机（一次指令只属于受理它的主机）', async () => {
    useStream([])
    usePolls([{ status: 'pending' }])
    const w = await mountDetail()
    await click(w, '开始推送')
    await w.setProps({ hostId: 'h2' })
    await flush()
    expect(api.fetchDockerCmdResult).toHaveBeenCalledWith('h1', 'r300')
    expect(api.fetchDockerCmdResult).not.toHaveBeenCalledWith('h2', 'r300')
  })

  it('受理失败（409 在飞）：分类句就地显示，留在输入态可重试，不开流', async () => {
    api.sendDockerCmd.mockRejectedValue(new HttpError('该目标上已有同一条操作在执行', 409))
    const w = await mountDetail()
    await click(w, '开始推送')
    expect(w.text()).toContain('该目标上已有同一条操作在执行')
    expect(w.text()).toContain('开始推送') // 输入态还在（可改可重试）
    expect(api.openDockerPushStream).not.toHaveBeenCalled() // 没受理就不开流
  })
})

describe('逐层渲染（推送语义：Pushing 条 / Pushed 勾 / 已上传汇总）', () => {
  it('同层折叠成一行（后帧是更新不是新行）；终态勾与汇总各就各位', async () => {
    useStream([
      jf({ status: 'The push refers to repository [harbor.example.com/app]' }),
      jf({ id: LA, status: 'Preparing' }),
      jf({ id: LA, status: 'Pushing', current: 500, total: 1000 }),
      jf({ id: LB, status: 'Layer already exists' })
    ])
    usePolls([{ status: 'pending' }])
    const w = await mountDetail()
    await click(w, '开始推送')
    await flush()

    const text = w.text()
    expect(text).toContain('aaaa1111bbbb') // 层短 id（等宽列）
    expect(text).toContain('The push refers to repository [harbor.example.com/app]') // 消息行提示
    expect(text).toContain('已上传 500 B / 1000 B') // Pushing 段汇总（动词换「上传」）
    expect(text).toContain('1/2 层完成')
    expect(text).toContain('Pushing') // LA 的最新一帧
    expect(text).not.toContain('Preparing') // 旧帧被折叠掉

    const bars = w.findAllComponents({ name: 'ElProgress' })
    expect(bars.length).toBe(1) // LA 未终态有条；LB 终态无条
    expect(bars[0]!.props('percentage')).toBe(50)
    expect(bars[0]!.props('indeterminate')).toBe(false)
    // LB 的终态勾（Layer already exists 是复用仓库既有层的等价终态）。
    expect(w.find('.sp-layer__check').exists()).toBe(true)
  })

  it('无 total 的层走不确定态动画（Preparing 还没有字节口径）', async () => {
    useStream([jf({ id: LA, status: 'Preparing' })])
    usePolls([{ status: 'pending' }])
    const w = await mountDetail()
    await click(w, '开始推送')
    await flush()

    const bars = w.findAllComponents({ name: 'ElProgress' })
    expect(bars.length).toBe(1)
    expect(bars[0]!.props('indeterminate')).toBe(true)
    expect(w.text()).not.toContain('已上传') // 没有字节口径就不给汇总
  })
})

describe('私库凭据选择（4c 同款面）', () => {
  /** 本组的轻量通道替身：流接入失败（404）→ 退「无进度、等结果」；result 直接终态。 */
  function lightChannels() {
    api.openDockerPushStream.mockResolvedValue({ ok: false, status: 404 })
    api.fetchDockerCmdResult.mockResolvedValue({ status: 'succeeded', detail: '' })
  }

  it('无 docker:config：下拉不渲染、清单不拉取、载荷 options 逐字是 {}', async () => {
    lightChannels()
    const w = await mountDetail()
    expect(w.find('.sp-creds').exists()).toBe(false)
    expect(api.fetchDockerRegistries).not.toHaveBeenCalled()

    await click(w, '开始推送')
    expect(api.sendDockerCmd).toHaveBeenCalledWith('h1', {
      action: 'image:push',
      target: 'app:v1',
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
    const w = await mountDetail()
    expect(api.fetchDockerRegistries).toHaveBeenCalledTimes(1)
    expect(w.find('.sp-creds').exists()).toBe(true)

    await credsSelect(w)!.setValue('harbor.example.com')
    await click(w, '开始推送')
    expect(api.sendDockerCmd).toHaveBeenCalledWith('h1', {
      action: 'image:push',
      target: 'app:v1',
      options: { registry: 'harbor.example.com' }
    })
  })

  it('清单为空 / 读取失败：就地一句说明，推送不受影响', async () => {
    lightChannels()
    auth.config = true
    // 空清单：说明「还没有」，不渲染下拉（渲染一个空的也没得选）。
    const w = await mountDetail()
    expect(w.find('.sp-creds__hint').text()).toContain('还没有已保存的仓库凭据')
    expect(credsSelect(w)).toBeUndefined()

    // 读取失败：换成「读取失败」的说明（凭据只是增强，失败不挡推送）。
    api.fetchDockerRegistries.mockRejectedValue(new Error('network down'))
    await w.setProps({ modelValue: false })
    await w.setProps({ modelValue: true })
    await flush()
    expect(w.find('.sp-creds__hint').text()).toContain('已保存凭据读取失败')

    await click(w, '开始推送')
    expect(api.sendDockerCmd).toHaveBeenCalledWith('h1', {
      action: 'image:push',
      target: 'app:v1',
      options: {}
    })
  })

  it('凭据选择不跨场次：重新打开回到「不使用」', async () => {
    auth.config = true
    api.fetchDockerRegistries.mockResolvedValue({
      list: [{ registry: 'harbor.example.com', username: 'deploy', password: '****' }]
    })
    const w = await mountDetail()
    await credsSelect(w)!.setValue('harbor.example.com')
    // 不点「开始推送」：这里只钉「选择是会话内的」。

    await w.setProps({ modelValue: false })
    await w.setProps({ modelValue: true })
    await flush()
    expect(credsSelect(w)!.props('modelValue')).toBe('') // 上一场的选择清空（resetAll）
    expect(api.fetchDockerRegistries).toHaveBeenCalledTimes(2) // 每次打开重拉清单
  })
})

describe('终态收尾与关闭', () => {
  it('流 eof 先到 → result 终态 → 结论句；关闭收口（不传 refresh —— 推送不改本地清单）', async () => {
    useStream([
      jf({ id: LA, status: 'Pushing', current: 1000, total: 1000 }),
      jf({ id: LA, status: 'Pushed' }),
      jf({ done: true, eof: true })
    ])
    usePolls([{ status: 'pending' }, { status: 'succeeded', detail: '' }])
    const w = await mountDetail()
    await click(w, '开始推送')

    expect(w.text()).toContain('正在等待指令结果') // eof 已到、result 还在 pending

    await waitPoll() // 第二轮轮询（pollDelay(0) = 1s）
    await flush()
    expect(api.fetchDockerCmdResult).toHaveBeenCalledTimes(2)
    expect(w.text()).toContain('已推送 app:v1')
    expect(w.text()).toContain('耗时')

    // 关闭：结论落定后退出。详情页不传 refresh（推送不改变本地任何事实 —— 推的
    // 是副本，镜像/使用/关联容器都不动；这条接线口径由 phase-gate 的源码断言钉住），
    // 与拉取/构建（成功后世界变了）的差别就在这一点。
    await click(w, '关闭')
    expect(w.emitted('update:modelValue')?.at(-1)).toEqual([false])
  })

  it('失败：结论句原文（服务端 error 透传）', async () => {
    useStream([jf({ error: 'denied: requested access to the resource is denied', eof: true })])
    usePolls([
      { status: 'failed', error: '推送镜像失败： denied: requested access to the resource' }
    ])
    const w = await mountDetail()
    await click(w, '开始推送')
    await flush()
    expect(w.text()).toContain('推送镜像失败： denied: requested access to the resource')
  })
})

describe('取消路径', () => {
  it('取消推送：断流（Abort）→ 进度态显示已取消 → result 结论句收尾', async () => {
    const scripted = useStream([jf({ id: LA, status: 'Pushing', current: 100, total: 1000 })])
    usePolls([{ status: 'pending' }, { status: 'failed', error: '推送已取消' }])
    const w = await mountDetail()
    await click(w, '开始推送')

    await click(w, '取消推送')
    expect(w.text()).toContain('已取消，正在等待指令收尾')
    expect(scripted.state.signal?.aborted, '取消 = 断流（服务端随之终止推送）').toBe(true)

    await waitPoll()
    await flush()
    expect(w.text()).toContain('推送已取消')
  })
})

describe('关闭与重开（生命周期收口）', () => {
  it('推送在途关对话框：断流 + 停轮询', async () => {
    const scripted = useStream([jf({ id: LA, status: 'Pushing', current: 100, total: 1000 })])
    usePolls([{ status: 'pending' }])
    const w = await mountDetail()
    await click(w, '开始推送')
    expect(api.fetchDockerCmdResult).toHaveBeenCalledTimes(1) // 第一轮已发

    await w.setProps({ modelValue: false })
    await flush()
    expect(scripted.state.signal?.aborted, '关对话框即断流').toBe(true)

    await new Promise((r) => setTimeout(r, 2100)) // 跨过下一个轮询间隔
    expect(api.fetchDockerCmdResult, '轮询应已停').toHaveBeenCalledTimes(1)
  })

  it('重新打开：整表重置（层表/结论不进新一场；预填与凭据回到初始态）', async () => {
    useStream([jf({ done: true, eof: true })])
    usePolls([{ status: 'succeeded', detail: '' }])
    const w = await mountDetail()
    await click(w, '开始推送')
    await flush()
    expect(w.text()).toContain('已推送 app:v1')

    await w.setProps({ modelValue: false })
    await w.setProps({ modelValue: true })
    await flush()
    expect(w.text()).toContain('开始推送')
    expect(w.text()).not.toContain('已推送')
    // 预填引用来自 props（详情页的入口语义），重开照旧预填。
    expect(targetSelect(w).props('modelValue')).toBe('app:v1')
    expect(api.openDockerPushStream).toHaveBeenCalledTimes(1) // 新一场没有自动开流
  })

  it('预填晚到（详情页快照异步落定）而对话框已开且未选过：补上预填', async () => {
    const w = await mountDialog({ images: [], initialTarget: '' })
    expect(targetSelect(w).props('modelValue')).toBe('')
    await w.setProps({ images: IMAGES, initialTarget: 'mysql:8' })
    await flush()
    expect(targetSelect(w).props('modelValue')).toBe('mysql:8')
  })

  it('用户已选过：晚到的预填不覆盖选择（选择权在用户）', async () => {
    const w = await mountDialog({ images: IMAGES, initialTarget: 'app:v1' })
    await choose(w, 'mysql:8')
    await w.setProps({ initialTarget: 'app:v2' })
    await flush()
    expect(targetSelect(w).props('modelValue')).toBe('mysql:8')
  })
})
