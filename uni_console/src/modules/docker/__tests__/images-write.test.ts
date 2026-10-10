// @vitest-environment jsdom
/**
 * 镜像页三个输入型写动作（打标签 / 导出 tar / 载入镜像）的接线测试 ——
 * prompt 输入形态归一后，它们全部走 DockerActionConfirm 的**输入档**（原自建
 * ElMessageBox.prompt 已删，同页不再有两套输入形态）。这里钉的是：
 *
 *   - 弹窗内收集参数：校验挡提交（打标签的非法镜像引用、空文件名都发不出指令），
 *     就地错误句在非空不合法时可见（空串不挨骂）；
 *   - 载荷口径（对 sendDockerCmd 的断言）：
 *     · 打标签 → { src, dst }，**不带 target**（协议必填就是 src/dst）；
 *     · 载入 → { filename }，无 target；
 *     · 导出第一段 → { filename } 不带覆盖标记；
 *     · 导出第二段（alreadyExists 两段式原样保留）→ 同一只弹窗**就地**切到逐字档
 *       （输入被清空、须重新照抄文件名），重发 { filename, overwrite: true } +
 *       confirm=照抄值；
 *   - 漂移守卫：源码扫描钉住「不再自建 prompt」（ElMessageBox / promptText 不回潮）。
 *
 * 纯函数层（输入档描述/校验/形态推导）在 action-confirm.test.ts；页面级收敛不变量
 * （聚合数据源/主机筛选/合计口径/失败口径）在 resources.test.ts。
 *
 * 拆分切片：原「镜像 tab」升格为独立整页 views/images.vue —— 动作入口是
 * 工具条 ArtButtonTable 图标钮（title 即悬浮说明、禁用理由也写在 title）：
 * 「拉取/载入」恒在（canManage）且缺选定主机时禁用；「打标签/导出 tar」恰选中一行
 * 时出现、指令在途时禁用。本文件的点击/断言口径随之改为按 title 找工具条按钮。
 *
 * 确认弹窗（action-confirm）现走 append-to-body：DOM 挂到 document.body，弹窗内查询
 * 经 dialogFind/dialogButtons 双处命中（props 仍从组件实例读）。表格行勾选走真 ElTable
 * 的 toggleRowSelection（工具条按钮的「出现 + 可点」以恰选中一行为前提）——需传**与表
 * 数据同一个对象引用**（ElTable 按引用比对；选中的是 fetchDockerImages 返回的那一行）。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { ElTable, ElTableColumn } from 'element-plus'
import { DOMWrapper, mount, type VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'

// 源码扫描用 ?raw 直接取 SFC 原文（jsdom 环境里 import.meta.url 解析到文档基址，
// new URL()/fileURLToPath 都拿不到真实路径 —— vite 的 ?raw 与环境无关）。
import pageSrc from '../views/images.vue?raw'

const api = vi.hoisted(() => ({
  fetchDockerHosts: vi.fn(),
  fetchDockerImages: vi.fn(),
  sendDockerCmd: vi.fn(),
  fetchDockerCmdResult: vi.fn(),
  // 拉取/构建进度对话框与凭据对话框挂载（不打开）需要的 import 面。
  openDockerPullStream: vi.fn(),
  openDockerBuildStream: vi.fn(),
  // P3 构建上下文上传（构建对话框的上传形态；本文件不触发，import 面补齐）。
  uploadDockerBuildContext: vi.fn(),
  fetchDockerRegistries: vi.fn()
}))
vi.mock('../api', () => ({ ...api, default: undefined }))
vi.mock('@/hooks/core/useAuth', () => ({
  useAuth: () => ({ hasAuth: () => true, hasAnyAuth: () => true })
}))

import ImagesPage from '../views/images.vue'
import type { DockerHostItem, DockerImageListItem } from '../api'

/** 可管主机清单（页面自拉的 GET /docker/hosts 替身；单主机让整体动作的 host 落定）。 */
const HOSTS: DockerHostItem[] = [
  {
    id: 'h1',
    hostname: 'bogon',
    primaryIp: '192.168.12.105',
    online: true,
    dockerOk: true,
    containers: 0,
    images: 1,
    stale: false
  }
]

/** 跨主机镜像条目（行带归属两列 —— 写操作按行主机派发）。 */
const IMAGE: DockerImageListItem = {
  id: 'sha256:aaaa1111bbbb2222cccc3333dddd4444eeee5555ffff66667777888899990000',
  repoTags: ['mysql:8.0'],
  sizeMb: 596.2,
  inUse: false,
  dangling: false,
  hostId: 'h1',
  hostname: 'bogon'
}

/** 聚合端点（GET /docker/images）的响应形态：total 全量 + items。 */
const IMAGES = { items: [IMAGE], total: 1 }

/** Art* 全局组件替身（真组件靠 unplugin 注册，测试环境里没有；保住 slot ——
 *  `left` 槽承载工具条动作钮，必须渲染出来）。 */
const passthrough = (name: string) =>
  defineComponent({
    name,
    inheritAttrs: false,
    setup:
      (_, { slots }) =>
      () =>
        h('div', { 'data-stub': name }, [slots.default?.(), slots.left?.()])
  })

/**
 * ArtTable 的替身：真 ElTable + 从 columns 配置里渲染**勾选列**（本文件只需要
 * 「选中一行」这条路径 —— 行内操作列/普通列的渲染由 page-render 与 resources 的
 * 用例覆盖）。selection-change 监听器经 attrs 透传给 ElTable。
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
            ((attrs.columns as { type?: string }[] | undefined) ?? [])
              .filter((c) => c.type === 'selection')
              .map(() => h(ElTableColumn, { type: 'selection' }))
        }
      )
  }
})

/**
 * ArtButtonTable 的替身：渲染成**真按钮** —— title（悬浮说明，禁用时带禁用理由）
 * 与 disabled 都落在元素上；click 的活性口径与真组件一致（禁用吞点击、可点发 emit）。
 */
const artButtonTableStub = defineComponent({
  name: 'ArtButtonTable',
  inheritAttrs: false,
  props: {
    title: { type: String, default: '' },
    disabled: { type: Boolean, default: false }
  },
  emits: ['click'],
  setup(props, { emit, attrs }) {
    return () =>
      h('button', {
        ...attrs,
        title: props.title,
        disabled: props.disabled,
        onClick: () => {
          if (!props.disabled) emit('click')
        }
      })
  }
})

const STUBS = {
  ArtTable: artTableStub,
  ArtSearchBar: passthrough('ArtSearchBar'),
  ArtTableHeader: passthrough('ArtTableHeader'),
  ArtButtonTable: artButtonTableStub,
  ArtSvgIcon: passthrough('ArtSvgIcon')
}

/** 已挂载的组件：用例结束后逐个卸载（Element Plus 的监听不留给下个用例）。 */
const mounted: VueWrapper[] = []

afterEach(() => {
  for (const w of mounted.splice(0)) w.unmount()
})

/** 指令链跨几个微任务（submit → emit → run → 受理/轮询），用宏任务兜平。 */
const flush = async () => {
  await new Promise((r) => setTimeout(r, 0))
  await nextTick()
}

/**
 * 挂镜像页（拆分切片后为独立整页 views/images.vue）：主机清单由页面自己经
 * GET /docker/hosts 拉（替身回单主机，让「整体动作」的 host 落定 —— 载入/清理
 * 这类无行目标动作面向筛选主机）；行数据来自 fetchDockerImages（替身）。指令替身
 * 不挑主机，但断言仍看 hostId 是否按**行主机/整体主机**派发。
 */
async function mountPage() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/', component: { template: '<div />' } }]
  })
  await router.push('/')
  await router.isReady()
  const wrapper = mount(ImagesPage, {
    global: { plugins: [router], stubs: STUBS }
  })
  mounted.push(wrapper as VueWrapper)
  await flush()
  return wrapper as VueWrapper
}

/** 勾选镜像表的第一行（工具条「打标签 / 导出 tar」以恰选中一行为前提）。
 *  传的是 fetchDockerImages 返回的同一个对象引用 —— ElTable 按引用比对。 */
async function selectFirstImage(w: VueWrapper) {
  const table = w.findComponent(ElTable)
  expect(table.exists(), '镜像表未挂载').toBe(true)
  const vm = table.vm as unknown as {
    toggleRowSelection: (row: unknown, selected?: boolean) => void
  }
  expect(typeof vm.toggleRowSelection).toBe('function')
  vm.toggleRowSelection(IMAGE, true)
  await flush()
}

/** 工具条图标钮（按 title 前缀找 —— 禁用时 title 带禁用理由后缀）。
 *  替身渲染真按钮并复刻真组件的活性判据：disabled 时元素自身即禁用（click 吞掉）。
 *  点击若「按钮明明可点却什么都没发生」，成因见 src/test/setup.ts 的事件时间戳守卫豁免。 */
async function clickBar(w: VueWrapper, title: string) {
  const btn = w.findAll('button').find((b) => (b.attributes('title') ?? '').startsWith(title))
  expect(btn, `找不到工具条按钮「${title}」`).toBeTruthy()
  expect((btn!.element as HTMLButtonElement).disabled, `「${title}」不该被禁用`).toBe(false)
  await btn!.trigger('click')
  await flush()
}

/** 确认弹窗（真组件）—— props 查询（modelValue/action）走它：append-to-body 只把 DOM
 *  搬到 document.body，组件实例仍在 Vue 组件树上，findComponent 照常命中。弹窗内 DOM
 *  查询走下面的 dialogFind。 */
function confirmOf(w: VueWrapper) {
  const confirm = w.findComponent({ name: 'DockerActionConfirm' })
  expect(confirm.exists(), '确认弹窗未挂载').toBe(true)
  return confirm
}

/** 弹窗内元素查询：append-to-body 后弹窗挂到 document.body，wrapper 里查不到 DOM ——
 *  先 wrapper（就地渲染场景）、再 document.body（teleport 场景），双处查询范式照
 *  container-detail-page.test.ts。 */
function dialogFind(w: VueWrapper, selector: string): DOMWrapper<Element> {
  const local = w.find(selector)
  if (local.exists()) return local
  return new DOMWrapper(document.body).find(selector)
}

/** 弹窗内的按钮：teleport 后从 document.body 取（只认弹窗内的，避免命中工具条同名钮）。 */
function dialogButtons(w: VueWrapper): HTMLButtonElement[] {
  const inBody = Array.from(
    document.body.querySelectorAll('.el-dialog button')
  ) as HTMLButtonElement[]
  if (inBody.length > 0) return inBody
  return Array.from(w.element.querySelectorAll('.el-dialog button')) as HTMLButtonElement[]
}

/**
 * 弹窗里的输入框：等它**真的渲染出来**再返回。
 *
 * 为什么不是「拿到就 setValue/exists 断言」：开窗是一串异步（点工具条 → 页面开窗状态
 * → 弹窗 props → 内容区渲染），拿固定轮数的 flush 去兜在负载下会漂（全量首跑实测过
 * 「输入框还没进 DOM」的红）；判据换成**元素存在**这个事实。
 */
async function dialogInputOf(w: VueWrapper, placeholder: string) {
  const selector = `input[placeholder="${placeholder}"]`
  await vi.waitUntil(() => dialogFind(w, selector).exists(), { timeout: 5000 })
  return dialogFind(w, selector)
}

/** 等弹窗**关闭**这件事落地（提交 → 指令 → 轮询 → 关窗是一串异步；同样不数轮数）。 */
async function waitClosed(confirm: ReturnType<typeof confirmOf>) {
  await vi.waitUntil(() => confirm.props('modelValue') === false, { timeout: 5000 })
}

/** 弹窗主按钮（按文字找：打标签/导出/覆盖导出/载入镜像）—— teleport 后从 body 取。 */
function submitBtn(w: VueWrapper, label: string) {
  const btn = dialogButtons(w).find((b) => (b.textContent ?? '').includes(label))
  expect(btn, `弹窗里找不到主按钮「${label}」`).toBeTruthy()
  return new DOMWrapper(btn!)
}

const isDisabled = (btn: { element: Element }) => (btn.element as HTMLButtonElement).disabled

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
  api.fetchDockerCmdResult.mockResolvedValue({ status: 'succeeded', detail: '操作已完成' })
  // 主机清单替身（页面自拉）：单主机让「整体动作」的 host 落定。
  api.fetchDockerHosts.mockReset()
  api.fetchDockerHosts.mockResolvedValue({ list: HOSTS })
  // 聚合端点替身：行数据经它进表（勾选/指令都从这一行出发）。
  api.fetchDockerImages.mockResolvedValue(IMAGES)
})

describe('打标签：输入档收集新引用（校验挡提交）', () => {
  it('空串/非法镜像引用都禁用提交；合法值发出 {src, dst}，不带 target', async () => {
    const w = await mountPage()
    await selectFirstImage(w)
    await clickBar(w, '打标签…')

    const confirm = confirmOf(w)
    // 等「弹窗已打开」这个**事实**：点击 → 选中态 → 开窗状态 → 父组件渲染 → 子组件
    // props，是一串异步；判据用 props 本身（不数 flush 轮数 —— 负载下轮数会漂）。
    await vi.waitUntil(() => confirm.props('modelValue') === true, { timeout: 5000 })
    expect(confirm.props('modelValue')).toBe(true)
    expect(confirm.props('action')).toBe('image:tag')
    // 输入档形态：标签是「新的镜像引用」，无逐字期望（不发 confirm 值）。
    expect(dialogFind(w, '.ac-input__label').text()).toBe('新的镜像引用')
    const input = await dialogInputOf(w, '例如 仓库/名称:标签')
    expect(input.exists()).toBe(true)

    // 空串：主按钮禁用（不发指令）。
    expect(isDisabled(submitBtn(w, '打标签'))).toBe(true)
    // 非法引用：禁用 + 就地错误句可见（非空才显错）。
    await input.setValue('bad ref!')
    expect(isDisabled(submitBtn(w, '打标签'))).toBe(true)
    expect(dialogFind(w, '.ac-input__error').text()).toContain('镜像引用')
    // 合法引用（带首尾空白）：放行，值裁剪后进载荷。
    await input.setValue('  uni-center/core:v2  ')
    expect(isDisabled(submitBtn(w, '打标签'))).toBe(false)
    expect(dialogFind(w, '.ac-input__error').exists()).toBe(false)

    await submitBtn(w, '打标签').trigger('click')
    await flush()

    expect(api.sendDockerCmd).toHaveBeenCalledTimes(1)
    expect(api.sendDockerCmd).toHaveBeenCalledWith('h1', {
      action: 'image:tag',
      target: undefined,
      options: { src: 'mysql:8.0', dst: 'uni-center/core:v2' },
      confirm: undefined
    })
  })

  it('取消不发指令（弹窗关闭即中止，与原 prompt 取消同语义）', async () => {
    const w = await mountPage()
    await selectFirstImage(w)
    await clickBar(w, '打标签…')

    const confirm = confirmOf(w)
    await (await dialogInputOf(w, '例如 仓库/名称:标签')).setValue('nginx:v2')
    const cancel = dialogButtons(w).find((b) => (b.textContent ?? '').includes('取消'))
    await new DOMWrapper(cancel!).trigger('click')
    await flush()

    expect(api.sendDockerCmd).not.toHaveBeenCalled()
    await waitClosed(confirm)
    expect(confirm.props('modelValue')).toBe(false)
  })
})

describe('载入镜像：输入档收集文件名（必填，格式不前端拦）', () => {
  it('空串禁用提交；非空文件名发出 {filename}，无 target、无 confirm', async () => {
    const w = await mountPage()
    // 载入不需要选中行：文件名是唯一主参数。
    await clickBar(w, '载入镜像…')

    const confirm = confirmOf(w)
    expect(confirm.props('action')).toBe('image:load')
    expect(dialogFind(w, '.ac-input__label').text()).toBe('文件名')
    // 原 prompt 的正文口径搬进 hint：讲清文件须已放在 agent 下载目录。
    expect(dialogFind(w, '.ac-input__hint').text()).toContain('文件需已放在该主机的 agent 下载目录')

    const input = await dialogInputOf(w, '例如 镜像名.tar')
    expect(isDisabled(submitBtn(w, '载入镜像'))).toBe(true)
    // 沿用原 prompt 口径：任意非空文件名放行（格式由服务端兜）。
    await input.setValue('my backup.tar.gz')
    expect(isDisabled(submitBtn(w, '载入镜像'))).toBe(false)

    await submitBtn(w, '载入镜像').trigger('click')
    await flush()

    expect(api.sendDockerCmd).toHaveBeenCalledTimes(1)
    expect(api.sendDockerCmd).toHaveBeenCalledWith('h1', {
      action: 'image:load',
      target: undefined,
      options: { filename: 'my backup.tar.gz' },
      confirm: undefined
    })
  })
})

describe('导出 tar：alreadyExists 两段式（同一只弹窗内切档）', () => {
  it('第一段输入档收集文件名；产物已存在时就地切逐字档，照抄后带 overwrite 重发', async () => {
    api.fetchDockerCmdResult
      .mockResolvedValueOnce({ status: 'failed', error: '产物已存在', alreadyExists: true })
      .mockResolvedValueOnce({ status: 'succeeded', detail: '已导出' })

    const w = await mountPage()
    await selectFirstImage(w)
    await clickBar(w, '导出 tar…')

    const confirm = confirmOf(w)
    expect(confirm.props('action')).toBe('image:save')
    // 第一段：输入档（收集文件名），提示讲产物落点。
    expect(dialogFind(w, '.ac-input__label').text()).toBe('文件名')
    expect(dialogFind(w, '.ac-input__hint').text()).toContain('产物落在该主机的 agent 下载目录')

    const input = await dialogInputOf(w, '例如 镜像名.tar')
    expect(isDisabled(submitBtn(w, '导出'))).toBe(true)
    await input.setValue('mysql.tar')
    await submitBtn(w, '导出').trigger('click')
    await flush()

    // 第一段载荷：不带覆盖标记、不带 confirm（协议只在覆盖重发时要求照抄）。
    expect(api.sendDockerCmd).toHaveBeenCalledTimes(1)
    expect(api.sendDockerCmd).toHaveBeenCalledWith('h1', {
      action: 'image:save',
      target: 'mysql:8.0',
      options: { filename: 'mysql.tar' },
      confirm: undefined
    })

    // alreadyExists → 弹窗**不关**，就地切到逐字档：标签换成「输入文件名以确认」，
    // 输入被清空（第二段必须重新照抄，不能沿用第一段已填的值直接点确定）。
    // 逐字档的 placeholder 来自形态推导（「例如 backup.tar」），与输入档的说明文字不同档。
    expect(confirm.props('modelValue')).toBe(true)
    expect(dialogFind(w, '.ac-input__label').text()).toBe('输入文件名以确认')
    // 第二段的输入框是**另一个节点**（形态切换后重渲染）：等它渲染出来再读值。
    const literalInput = await dialogInputOf(w, '例如 backup.tar')
    expect((literalInput.element as HTMLInputElement).value).toBe('')
    expect(isDisabled(submitBtn(w, '覆盖导出'))).toBe(true)

    // 照抄不一致不通过；一致才放行。
    await literalInput.setValue('mysql')
    expect(isDisabled(submitBtn(w, '覆盖导出'))).toBe(true)
    await literalInput.setValue('mysql.tar')
    expect(isDisabled(submitBtn(w, '覆盖导出'))).toBe(false)

    await submitBtn(w, '覆盖导出').trigger('click')
    await flush()

    // 第二段载荷：overwrite=true + confirm=照抄的文件名（服务端逐字校验后才覆盖）。
    expect(api.sendDockerCmd).toHaveBeenCalledTimes(2)
    expect(api.sendDockerCmd).toHaveBeenLastCalledWith('h1', {
      action: 'image:save',
      target: 'mysql:8.0',
      options: { filename: 'mysql.tar', overwrite: true },
      confirm: 'mysql.tar'
    })
    // 成功后弹窗关闭。
    await waitClosed(confirm)
    expect(confirm.props('modelValue')).toBe(false)
  })

  it('第一段直接成功：报告结论并关弹窗，不进第二段', async () => {
    const w = await mountPage()
    await selectFirstImage(w)
    await clickBar(w, '导出 tar…')

    const confirm = confirmOf(w)
    await (await dialogInputOf(w, '例如 镜像名.tar')).setValue('mysql.tar')
    await submitBtn(w, '导出').trigger('click')
    await flush()

    expect(api.sendDockerCmd).toHaveBeenCalledTimes(1)
    // 关闭判定只看 modelValue（关闭过渡播放期间内容节点还在 DOM 里，不能拿它当凭据）。
    await waitClosed(confirm)
    expect(confirm.props('modelValue')).toBe(false)
  })

  it('第一段失败（非 alreadyExists）：报告结论并关弹窗', async () => {
    api.fetchDockerCmdResult.mockResolvedValueOnce({
      status: 'failed',
      error: '磁盘不可写'
    })
    const w = await mountPage()
    await selectFirstImage(w)
    await clickBar(w, '导出 tar…')

    const confirm = confirmOf(w)
    await (await dialogInputOf(w, '例如 镜像名.tar')).setValue('mysql.tar')
    await submitBtn(w, '导出').trigger('click')
    await flush()

    expect(api.sendDockerCmd).toHaveBeenCalledTimes(1)
    await waitClosed(confirm)
    expect(confirm.props('modelValue')).toBe(false)
  })
})

describe('工具条按钮的活性与出现条件（新形态）', () => {
  it('没有可管主机：整体动作全渲染但禁用，禁用理由写进 title（不设常驻提示句）', async () => {
    api.fetchDockerHosts.mockResolvedValue({ list: [] })
    api.fetchDockerImages.mockResolvedValue({ items: [], total: 0 })
    const w = await mountPage()

    for (const label of ['拉取镜像…', '构建镜像…', '载入镜像…', '清理悬空镜像…']) {
      const btn = w.findAll('button').find((b) => (b.attributes('title') ?? '').startsWith(label))
      expect(btn, `工具条缺「${label}」（有权限就该渲染，禁用 ≠ 不渲染）`).toBeTruthy()
      expect((btn!.element as HTMLButtonElement).disabled, `「${label}」应禁用`).toBe(true)
      expect(btn!.attributes('title'), `「${label}」的禁用理由应进 title`).toContain(
        '需先在「主机」筛选中选定一台主机'
      )
    }
    // 仓库凭据是**全局**面（不属于任何一台主机）：与主机筛选无关，可点。
    const reg = w
      .findAll('button')
      .find((b) => (b.attributes('title') ?? '').startsWith('仓库凭据…'))
    expect(reg, '仓库凭据入口应渲染').toBeTruthy()
    expect((reg!.element as HTMLButtonElement).disabled).toBe(false)
  })

  it('打标签/导出 tar 恰选中一行才出现（未选中前不占版面），出现即可点', async () => {
    const w = await mountPage()
    const findBtn = (label: string) =>
      w.findAll('button').find((b) => (b.attributes('title') ?? '').startsWith(label))

    expect(findBtn('打标签…'), '未选中任何行时不该出现').toBeUndefined()
    expect(findBtn('导出 tar…'), '未选中任何行时不该出现').toBeUndefined()

    await selectFirstImage(w)
    const tag = findBtn('打标签…')
    const save = findBtn('导出 tar…')
    expect(tag, '恰选中一行后应出现').toBeTruthy()
    expect(save, '恰选中一行后应出现').toBeTruthy()
    expect((tag!.element as HTMLButtonElement).disabled).toBe(false)
    expect((save!.element as HTMLButtonElement).disabled).toBe(false)
  })
})

describe('输入形态归一的漂移守卫（源码扫描）', () => {
  it('镜像页不再自建 prompt：ElMessageBox / promptText 已删，三动作都接确认弹窗', () => {
    expect(pageSrc).not.toContain('ElMessageBox')
    expect(pageSrc).not.toContain('promptText')
    // 三处入口都改接确认弹窗的提交分支（kind 标记进 confirmState）。
    for (const needle of ["kind: 'tag'", "kind: 'load'", "kind: 'save'"]) {
      expect(pageSrc, `镜像页缺输入档接线标记 ${needle}`).toContain(needle)
    }
    // 两段式的就地切档：alreadyExists 分支换 confirmState（不关弹窗）。
    expect(pageSrc).toContain('alreadyExists === true')
    expect(pageSrc).toContain('overwrite: true')
  })
})
