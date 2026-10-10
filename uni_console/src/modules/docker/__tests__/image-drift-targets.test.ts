// @vitest-environment jsdom
/**
 * 声明式目标版本 + 按待对齐分发（Wave 4c 前端）的行为钉子：
 *
 *   - 声明入口（漂移页操作列）按 docker:manage 门控（无权限整列不渲染）；
 *   - 声明对话框：候选摘要（多数派 + 少数派）点选 / 粘贴 sha256:… 两条路；
 *     提交走 POST /docker/image-targets，body 逐字（repoTag + 目标摘要）；
 *     手输非法（不 sha256: 开头）给显式结论句且禁提交；
 *   - 目标已声明行的显示：目标短显 + 已对齐 X/范围总数；
 *   - 分发入口仅在「有目标且有 pendingHosts」时出现；确认档列出影响面主机名，
 *     并如实区分「更新」与「新拉」（taggedPending = hostCount − alignedCount），
 *     措辞只描述**按标签拉取**（不再说「更新到 <摘要>」）；
 *   - 分发对**每台待对齐主机**各发一次 image:pull（target = repoTag，按标签拉取，
 *     不含 `@sha256:` 摘要）；一轮结束后重拉漂移逐台复核：收敛报「已对齐」、
 *     未收敛如实报「仍是 <实际摘要>，与目标 <目标摘要> 不同」；
 *   - 失败可重试：只对失败集再发一轮；
 *   - 取消目标发 DELETE（repoTag 在 query，显式 urlencode），且结论句不谎称「回滚」；
 *   - 无目标行不显示目标、不显示待对齐。
 *
 * 挂载方式沿用 image-drift-page.test.ts：mock '../api'，Art* 用轻量替身；行内事实
 * 从页面传给 ArtTable 的 columns 的 formatter vnode 断言（render→文本提取）。分发/
 * 取消目标的确认弹窗与声明对话框替身化（真实交互在各自的组件用例里钉）。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { mount, DOMWrapper, type VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'

const api = vi.hoisted(() => ({
  fetchDockerHosts: vi.fn(),
  fetchImageDrift: vi.fn(),
  fetchImageTargets: vi.fn(),
  setImageTarget: vi.fn(),
  clearImageTarget: vi.fn(),
  sendDockerCmd: vi.fn(),
  fetchDockerCmdResult: vi.fn()
}))
vi.mock('../api', () => ({ ...api, default: undefined }))

/** 权限持有者（可变）：门控用例在挂载前翻转 docker:manage。 */
const auth = vi.hoisted(() => ({ manage: true }))
vi.mock('@/hooks/core/useAuth', () => ({
  useAuth: () => ({
    hasAuth: (p: string) => (p === 'docker:manage' ? auth.manage : true),
    hasAnyAuth: () => true
  })
}))

/** ElMessage 桩掉（分发完成/取消目标走 toast）——须可调用（ElMessage({...})）且带 .success 等。 */
const elMessage = vi.hoisted(() => {
  const fn = vi.fn() as unknown as {
    (...args: unknown[]): void
    success: ReturnType<typeof vi.fn>
    error: ReturnType<typeof vi.fn>
    warning: ReturnType<typeof vi.fn>
  }
  fn.success = vi.fn()
  fn.error = vi.fn()
  fn.warning = vi.fn()
  return fn
})
vi.mock('element-plus', async (importOriginal) => {
  const actual = await importOriginal<typeof import('element-plus')>()
  return { ...actual, ElMessage: elMessage }
})

import ImageDrift, { dispatchTiming } from '../views/image-drift.vue'
import ImageTargetDialog from '../components/image-target-dialog.vue'
import type { DockerHostItem, DockerImageDriftItem } from '../api'

const HOSTS: DockerHostItem[] = [
  {
    id: '9',
    hostname: 'uni-109',
    primaryIp: '192.168.12.109',
    group: 'prod',
    online: true,
    dockerOk: true,
    containers: 0,
    images: 0,
    stale: false
  }
]

const TARGET = 'sha256:aaaa0000000000000000000000000000000000000000000000000000000000aa'
const OTHER = 'sha256:bbbb1111111111111111111111111111111111111111111111111111111111bb'

/** 有目标 + 2 台待对齐（hostCount 13 − aligned 12 = 1 台「摘要不同」+ 1 台「无该标签」）。 */
function targetedRow(over: Partial<DockerImageDriftItem> = {}): DockerImageDriftItem {
  return {
    repoTag: 'mysql:8.0.22',
    hostCount: 13,
    distinctDigests: 2,
    dominantDigest: TARGET,
    dominantHostCount: 12,
    outliers: [{ digest: OTHER, hostCount: 1, hosts: [{ hostId: '5', hostname: 'uni-105' }] }],
    targetDigest: TARGET,
    alignedCount: 12,
    pendingHosts: [
      { hostId: 'ok', hostname: 'uni-107' },
      { hostId: 'fail', hostname: 'uni-108' }
    ],
    ...over
  }
}

/** 无目标行（targetDigest/pendingHosts/alignedCount 三者缺席 —— 不臆造）。 */
function plainRow(over: Partial<DockerImageDriftItem> = {}): DockerImageDriftItem {
  return {
    repoTag: 'redis:7',
    hostCount: 4,
    distinctDigests: 2,
    dominantDigest: OTHER,
    dominantHostCount: 3,
    outliers: [{ digest: TARGET, hostCount: 1, hosts: [{ hostId: '9', hostname: 'uni-109' }] }],
    ...over
  }
}

function driftResp(items: DockerImageDriftItem[]) {
  return { items, total: items.length, page: 1, pageSize: 50, unavailableHosts: [] }
}

const passthrough = (name: string) =>
  defineComponent({
    name,
    setup:
      (_, { slots }) =>
      () =>
        h('div', { 'data-stub': name }, [slots.default?.(), slots.left?.()])
  })

/** 确认弹窗替身：把 override.conclusion（影响面/语义结论）与默认插槽（主机清单）渲染出来。 */
const ConfirmStub = defineComponent({
  name: 'DockerActionConfirm',
  props: {
    modelValue: Boolean,
    action: String,
    target: String,
    override: Object,
    loading: Boolean
  },
  setup(props, { slots }) {
    return () =>
      props.modelValue
        ? h('div', { class: 'stub-confirm', 'data-target': props.target }, [
            (props.override as { conclusion?: string } | undefined)?.conclusion ?? '',
            slots.default?.()
          ])
        : null
  }
})

const TargetDialogStub = defineComponent({
  name: 'ImageTargetDialog',
  props: { modelValue: Boolean, row: Object },
  setup: () => () => h('div', { 'data-stub': 'ImageTargetDialog' })
})

const STUBS = {
  ArtTable: passthrough('ArtTable'),
  ArtTableHeader: passthrough('ArtTableHeader'),
  ArtSearchBar: passthrough('ArtSearchBar'),
  ArtButtonTable: passthrough('ArtButtonTable'),
  ArtSvgIcon: passthrough('ArtSvgIcon'),
  DockerActionConfirm: ConfirmStub,
  ImageTargetDialog: TargetDialogStub
}

const mounted: VueWrapper[] = []

beforeEach(() => {
  vi.clearAllMocks()
  // 落定复核的时间缝置 0：立即复核，单测不等待真实 1.5s 采帧延迟。
  dispatchTiming.verifySettleMs = 0
  auth.manage = true
  api.fetchDockerHosts.mockResolvedValue({ list: HOSTS, snapshotInterval: 30 })
  api.fetchImageDrift.mockResolvedValue(driftResp([targetedRow(), plainRow()]))
  api.setImageTarget.mockResolvedValue({ repoTag: 'mysql:8.0.22', targetDigest: TARGET })
  api.clearImageTarget.mockResolvedValue(undefined)
  api.sendDockerCmd.mockImplementation((hostId: string) =>
    Promise.resolve({ ref: `ref-${hostId}` })
  )
  api.fetchDockerCmdResult.mockImplementation((hostId: string) =>
    Promise.resolve({ status: hostId === 'fail' ? 'failed' : 'succeeded', error: '拉取失败' })
  )
})

afterEach(() => {
  for (const w of mounted.splice(0)) w.unmount()
})

async function flush(rounds = 8) {
  for (let i = 0; i < rounds; i++) {
    await new Promise((r) => setTimeout(r, 0))
    await nextTick()
  }
}

async function mountDrift(): Promise<VueWrapper> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/docker/image-drift', component: ImageDrift }
    ]
  })
  await router.push('/docker/image-drift')
  await router.isReady()
  const w = mount(ImageDrift, { global: { plugins: [router], stubs: STUBS } })
  mounted.push(w)
  await flush()
  return w
}

type Col = {
  prop?: string
  type?: string
  label?: string
  formatter?: (row: DockerImageDriftItem) => unknown
}

function columnsOf(w: VueWrapper): Col[] {
  const table = w.findComponent({ name: 'ArtTable' })
  return ((table.vm as unknown as { $attrs: Record<string, unknown> }).$attrs.columns ??
    []) as Col[]
}

function columnByProp(w: VueWrapper, prop: string): Col | undefined {
  return columnsOf(w).find((c) => c.prop === prop)
}

/** 递归提取 vnode 文本（formatter 返回值可能是 span/div 或纯字符串/数组）。 */
function vnodeText(node: unknown): string {
  if (node == null || typeof node === 'boolean') return ''
  if (typeof node === 'string' || typeof node === 'number') return String(node)
  if (Array.isArray(node)) return node.map(vnodeText).join('')
  const v = node as { children?: unknown }
  return vnodeText(v.children)
}

/** 递归收集带 onClick 的 vnode（行内动作按钮）：文本 + 点击回调。 */
function collectButtons(
  node: unknown,
  out: { text: string; click: () => void }[] = []
): { text: string; click: () => void }[] {
  if (node == null || typeof node !== 'object') return out
  if (Array.isArray(node)) {
    for (const n of node) collectButtons(n, out)
    return out
  }
  const v = node as { props?: { onClick?: () => void }; children?: unknown }
  if (v.props && typeof v.props.onClick === 'function') {
    out.push({ text: vnodeText(v), click: v.props.onClick })
  }
  collectButtons(v.children, out)
  return out
}

/** 页面操作列的按钮（未渲染操作列时给空）。 */
function opButtons(
  w: VueWrapper,
  row: DockerImageDriftItem
): { text: string; click: () => void }[] {
  const col = columnByProp(w, 'operation')
  if (!col?.formatter) return []
  return collectButtons(col.formatter(row))
}

function confirmVm(w: VueWrapper) {
  return w.findComponent({ name: 'DockerActionConfirm' }).vm
}

describe('漂移页 · 声明目标：入口门控与目标显示', () => {
  it('操作列（声明/分发/取消目标入口）按 docker:manage 门控：无权限整列不渲染', async () => {
    auth.manage = false
    const w = await mountDrift()
    expect(columnByProp(w, 'operation'), '无 docker:manage 时不应有操作列').toBeUndefined()
  })

  it('有权限时有操作列；无目标行只给「声明目标」，有目标行给「改目标 / 分发 / 取消目标」', async () => {
    const w = await mountDrift()
    expect(columnByProp(w, 'operation')).toBeTruthy()

    const plain = opButtons(w, plainRow()).map((b) => b.text)
    expect(plain).toContain('声明目标')
    expect(plain.some((t) => t.includes('分发'))).toBe(false)

    const targeted = opButtons(w, targetedRow()).map((b) => b.text)
    expect(targeted).toContain('改目标')
    expect(targeted).toContain('分发到待对齐 2 台')
    expect(targeted).toContain('取消目标')
  })

  it('点「声明目标」开对话框（把该行交给对话框）', async () => {
    const w = await mountDrift()
    opButtons(w, plainRow())
      .find((b) => b.text === '声明目标')!
      .click()
    await flush()
    const dlg = w.findComponent({ name: 'ImageTargetDialog' })
    expect(dlg.props('modelValue')).toBe(true)
    expect(dlg.props('row')).toMatchObject({ repoTag: 'redis:7' })
  })

  it('目标列：有目标给「目标短显 + 已对齐 X/范围总数」，无目标「—」', async () => {
    const w = await mountDrift()
    const col = columnByProp(w, 'targetDigest')
    expect(col?.formatter).toBeTruthy()
    // 目标短显剥 sha256: 取前 12 位；范围总数 = 已对齐 + 待对齐 = 12 + 2。
    expect(col!.formatter!(targetedRow())).toBe('目标 aaaa00000000 · 已对齐 12/14')
    expect(col!.formatter!(plainRow())).toBe('—')
  })
})

describe('漂移页 · 声明目标对话框（真件）', () => {
  /** append-to-body 后弹窗挂 document.body（VTU 的 teleport 桩不渲染子节点，故走 body 查询）。 */
  function dialogButtons(): HTMLButtonElement[] {
    const root = document.body.querySelector('.el-dialog') as HTMLElement | null
    return root ? (Array.from(root.querySelectorAll('button')) as HTMLButtonElement[]) : []
  }
  async function clickDialogButton(label: string) {
    const btn = dialogButtons().find((b) => (b.textContent ?? '').trim() === label)
    expect(btn, `按钮「${label}」应已渲染`).toBeTruthy()
    btn!.click()
    await flush()
  }
  /** 手输框按 placeholder 定位（ElRadio 自带一个隐藏 input，按序取首会拿错）。 */
  async function fillManual(value: string) {
    const input = document.body.querySelector('input[placeholder="sha256:…"]')
    expect(input, '手输输入框应已渲染').toBeTruthy()
    await new DOMWrapper(input!).setValue(value)
    await flush()
  }

  async function mountDialog(row: DockerImageDriftItem): Promise<VueWrapper> {
    const w = mount(ImageTargetDialog, { props: { modelValue: true, row } })
    mounted.push(w)
    await flush()
    return w
  }

  it('候选摘要 = 多数派 + 少数派；有目标时预选当前目标', async () => {
    const row = targetedRow()
    const w = await mountDialog(row)
    const radios = w.findAllComponents({ name: 'ElRadio' })
    expect(radios).toHaveLength(1 + (row.outliers?.length ?? 0))
    const group = w.findComponent({ name: 'ElRadioGroup' })
    expect(group.props('modelValue')).toBe(TARGET)
  })

  it('点选一个候选摘要 → 提交发 POST，body = repoTag + 选中摘要', async () => {
    const row = targetedRow({
      targetDigest: undefined,
      alignedCount: undefined,
      pendingHosts: undefined
    })
    const w = await mountDialog(row)
    w.findComponent({ name: 'ElRadioGroup' }).vm.$emit('update:modelValue', OTHER)
    await flush()
    await clickDialogButton('声明目标')
    expect(api.setImageTarget).toHaveBeenCalledWith({
      repoTag: 'mysql:8.0.22',
      targetDigest: OTHER
    })
    expect(w.emitted('saved')).toBeTruthy()
  })

  it('粘贴手输覆盖点选：body = 手输摘要', async () => {
    const manual = 'sha256:cafe0000000000000000000000000000000000000000000000000000000000cc'
    await mountDialog(targetedRow())
    await fillManual(manual)
    await clickDialogButton('修改目标')
    expect(api.setImageTarget).toHaveBeenCalledWith({
      repoTag: 'mysql:8.0.22',
      targetDigest: manual
    })
  })

  it('手输非法（非 sha256: 开头）：给显式结论句且禁提交', async () => {
    await mountDialog(targetedRow())
    await fillManual('deadbeef')
    const error = document.body.querySelector('.itd__error')
    expect(error?.textContent).toBe('目标摘要必须以 sha256: 开头')
    const submit = dialogButtons().find((b) => (b.textContent ?? '').trim() === '修改目标')
    expect(submit?.disabled).toBe(true)
    expect(api.setImageTarget).not.toHaveBeenCalled()
  })
})

describe('漂移页 · 分发（对每台待对齐主机各发一次 image:pull，按标签拉取）', () => {
  async function openDistribute(
    w: VueWrapper,
    row: DockerImageDriftItem = targetedRow()
  ): Promise<void> {
    opButtons(w, row)
      .find((b) => b.text.includes('分发到待对齐'))!
      .click()
    await flush()
  }

  it('分发入口仅在「有目标且有 pendingHosts」时出现', async () => {
    const w = await mountDrift()
    // 有目标、无待对齐 → 不出入口。
    const noPending = targetedRow({ pendingHosts: [], alignedCount: 13 })
    expect(opButtons(w, noPending).some((b) => b.text.includes('分发'))).toBe(false)
    // 无目标 → 不出入口（也不显示待对齐）。
    expect(opButtons(w, plainRow()).some((b) => b.text.includes('分发'))).toBe(false)
  })

  it('确认档列出影响面主机名，如实区分「更新」与「新拉」，措辞只讲按标签拉取（不出现摘要钉住/回滚）', async () => {
    const w = await mountDrift()
    await openDistribute(w)
    const text = w.text()
    expect(text).toContain('uni-107')
    expect(text).toContain('uni-108')
    // hostCount 13 − aligned 12 = 1 台已有该标签（按标签更新）；待对齐 2 − 1 = 1 台无该标签（按标签新拉取）。
    expect(text).toContain('1 台已有该标签（按标签更新）')
    expect(text).toContain('1 台本机无该标签（按标签新拉取）')
    // 旧措辞（钉摘要的「更新到 <digest>」）已失真 —— 不得复现；也不得出现「回滚」。
    expect(text).not.toContain('更新到')
    expect(text).not.toContain('回滚')
    // 分发引用不含 @sha256:（防回归到旧的摘要钉住写法）。
    expect(text).not.toContain('@sha256:')
  })

  it('确认后：对每台待对齐主机各发一次 image:pull，target = repoTag（不含 @摘要）', async () => {
    const w = await mountDrift()
    await openDistribute(w)
    api.sendDockerCmd.mockClear()
    confirmVm(w).$emit('confirm')
    await flush(12)

    const calls = api.sendDockerCmd.mock.calls
    expect(calls).toHaveLength(2)
    const hosts = calls.map((c) => c[0]).sort()
    expect(hosts).toEqual(['fail', 'ok'])
    for (const c of calls) {
      // 按标签拉取：引用就是 repoTag，不拼 `@sha256:` 摘要。
      expect(c[1]).toMatchObject({ action: 'image:pull', target: 'mysql:8.0.22' })
      expect((c[1] as { target: string }).target).not.toContain('@')
      expect((c[1] as { target: string }).target).not.toContain('sha256')
    }
  })

  it('对齐复核（收敛）：拉取成功后重拉漂移，摘要 == 目标报「已对齐」', async () => {
    // 默认夹具：dominantDigest = TARGET、少数派只有别的 hostId → 拉取成功的 uni-107 落多数派 = TARGET。
    const w = await mountDrift()
    await openDistribute(w)
    confirmVm(w).$emit('confirm')
    await flush(12)

    expect(w.text()).toContain('uni-107：已对齐')
    expect(w.text()).toContain('分发完成：1 台已对齐 / 1 台失败')
  })

  it('对齐复核（未收敛）：复核读到实际摘要 != 目标 → 如实报实际摘要与目标（不谎报成功）', async () => {
    // 复核读到 uni-107 的当前摘要仍是 OTHER（≠ 目标）——仓库的标签没给出期望内容。
    const notAligned = targetedRow({
      pendingHosts: [{ hostId: 'ok', hostname: 'uni-107' }],
      outliers: [{ digest: OTHER, hostCount: 1, hosts: [{ hostId: 'ok', hostname: 'uni-107' }] }]
    })
    api.fetchDockerCmdResult.mockResolvedValue({ status: 'succeeded' })
    api.fetchImageDrift.mockResolvedValue(driftResp([notAligned]))

    const w = await mountDrift()
    await openDistribute(w, notAligned)
    confirmVm(w).$emit('confirm')
    await flush(12)

    // 实际摘要（OTHER 短显）与目标（TARGET 短显）都如实给出。
    expect(w.text()).toContain('uni-107：仍是 bbbb11111111，与目标 aaaa00000000 不同')
    expect(w.text()).toContain('分发完成：0 台已对齐 / 1 台仍是其他摘要')
  })

  it('失败可见并如实结算：完成态报「已对齐 / 失败」，重试只对失败集再发一轮', async () => {
    const w = await mountDrift()
    await openDistribute(w)
    confirmVm(w).$emit('confirm')
    await flush(12)

    // 一行成功且复核对齐（ok）、一行拉取失败（fail）。
    expect(w.text()).toContain('分发完成：1 台已对齐 / 1 台失败')

    api.sendDockerCmd.mockClear()
    const retry = w.findAll('button').find((b) => b.text().includes('重试失败的 1 台'))
    expect(retry, '失败后应出现重试入口').toBeTruthy()
    await retry!.trigger('click')
    await flush(12)

    const calls = api.sendDockerCmd.mock.calls
    expect(calls).toHaveLength(1)
    expect(calls[0]![0]).toBe('fail')
    expect(calls[0]![1]).toMatchObject({ action: 'image:pull', target: 'mysql:8.0.22' })
  })
})

describe('漂移页 · 取消目标（DELETE，幂等，且不谎称回滚）', () => {
  async function openClear(w: VueWrapper): Promise<void> {
    opButtons(w, targetedRow())
      .find((b) => b.text === '取消目标')!
      .click()
    await flush()
  }

  it('确认档结论句不谎称「回滚」：明确只停判定、不回退已拉取的镜像', async () => {
    const w = await mountDrift()
    await openClear(w)
    const text = w.text()
    expect(text).toContain('取消目标只停止「待对齐」的判定')
    expect(text).toContain('不会回退任何已拉取到主机的镜像')
    expect(text).not.toContain('回滚')
  })

  it('确认后发 DELETE（clearImageTarget 收到 repoTag）', async () => {
    const w = await mountDrift()
    await openClear(w)
    confirmVm(w).$emit('confirm')
    await flush()
    expect(api.clearImageTarget).toHaveBeenCalledWith('mysql:8.0.22')
  })
})

describe('漂移页 · 无目标行：不显示目标、不显示待对齐', () => {
  it('无目标行的目标列「—」，操作列无「分发」入口', async () => {
    const w = await mountDrift()
    expect(columnByProp(w, 'targetDigest')!.formatter!(plainRow())).toBe('—')
    expect(opButtons(w, plainRow()).map((b) => b.text)).toEqual(['声明目标'])
  })
})
