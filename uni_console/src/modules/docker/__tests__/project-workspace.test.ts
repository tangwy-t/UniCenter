// @vitest-environment jsdom
/**
 * 项目工作台（5b）的装配测试：渲染冒烟 + 服务卡动作派发 + 聚合日志接线（指令载荷、
 * 流消费、服务过滤）+ 配置区集成（编辑器 mock 往返）。
 *
 * 挂法照 page-render.test.ts 的同一套纪律：只桩 `../api`（网络）与
 * `@/hooks/core/useAuth`（权限全给），不桩 host-context；Art* 全局组件用轻量替身。
 * 分区组件（hero/网元卡/聚合日志/配置）**不桩** —— 工作台的价值恰在重组后的接线，
 * 桩掉它们就只剩「页面挂了四个组件」这一句空话。唯一例外是 ComposeEditor：
 * 它自带完整指令闭环（载入/校验/diff/确认），那条闭环的测试在编辑器自己的测试里；
 * 这里用替身接住 `saved` 事件，验证**页面侧**的往返（重拉备份 + 刷新快照）。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'

const api = vi.hoisted(() => ({
  fetchDockerHosts: vi.fn(),
  fetchDockerState: vi.fn(),
  sendDockerCmd: vi.fn(),
  fetchDockerCmdResult: vi.fn(),
  // 聚合日志流端点（compose:logs 与 container:logs 同端点）。
  openDockerLogStream: vi.fn()
}))
vi.mock('../api', () => ({
  ...api,
  // 类型别名在运行期不存在，这里只需函数面
  default: undefined
}))
vi.mock('@/hooks/core/useAuth', () => ({
  useAuth: () => ({ hasAuth: () => true, hasAnyAuth: () => true })
}))

import ProjectWorkspace from '../views/project-workspace.vue'
import Projects from '../views/projects.vue'
import type { DockerContainerItem } from '../api'
import { COMPOSE_LOGS_ACTIONS } from '../utils/cmd'
import { filterComposeLogLines, composeLogLineService } from '../utils/log'
import { deriveServiceRows } from '../utils/projects'

const HOSTS = {
  list: [
    {
      id: 'h1',
      hostname: 'bogon',
      primaryIp: '192.168.12.105',
      online: true,
      dockerOk: true,
      agentVersion: '0.5.4'
    }
  ]
}

/** 快照：一个项目（2 网元 2 容器，都不受保护 —— 直接派发路径不受保护档分流）。 */
const STATE = {
  lastSync: 1790600000,
  stale: false,
  ageSeconds: 3,
  neverReported: false,
  dockerOk: true,
  compose: { flavor: 'plugin', version: 'v2.27.0' },
  containers: [
    {
      id: 'c1',
      name: 'uni-center-core',
      image: 'uni-center-core:latest',
      state: 'running',
      statusText: 'Up 16 hours',
      cpuPercent: 0.6,
      memUsageMb: 91,
      memLimitMb: 1024,
      netRxBytesSec: 0,
      netTxBytesSec: 0,
      protected: false,
      composeProject: 'uni-center',
      composeService: 'core'
    },
    {
      id: 'c2',
      name: 'uni-center-web',
      image: 'nginx:1.27',
      state: 'exited',
      statusText: 'Exited (0) 2 days ago',
      cpuPercent: 0,
      memUsageMb: 0,
      memLimitMb: 256,
      netRxBytesSec: 0,
      netTxBytesSec: 0,
      protected: false,
      composeProject: 'uni-center',
      composeService: 'web'
    }
  ],
  projects: [
    {
      name: 'uni-center',
      configFiles: ['/data/UniCenter/docker-compose.yml'],
      state: 'partial',
      services: 3,
      containersCount: 2,
      protected: false
    }
  ]
}

/** compose.file:read 的结果载荷（备份 1 份；snake_case 是线上形态）。 */
const COMPOSE_FILE_PAYLOAD = {
  content: 'services:\n  core:\n    image: uni-center-core:latest\n',
  hash: 'hash-1',
  path: '/data/UniCenter/docker-compose.yml',
  backups: [{ token: '20260930-101010', hash: 'hash-0', size_bytes: 12, at: 1790000000 }]
}

/** Art* 全局组件替身：保留 slot，页面内容仍会被渲染（否则断言看不到页面结构）。 */
const passthrough = (name: string) =>
  defineComponent({
    name,
    setup:
      (_, { slots }) =>
      () =>
        h('div', { 'data-stub': name }, [slots.default?.(), slots.left?.(), slots.table?.()])
  })

/**
 * ComposeEditor 替身：接住页面集成的两个事件面 —— `update:modelValue`（受控开关）
 * 与 `saved`（保存/回滚成功 → 页面要重拉备份与快照）。真实闭环在编辑器自己的测试里。
 */
const ComposeEditorStub = defineComponent({
  name: 'ComposeEditor',
  props: { modelValue: Boolean, project: String, projectProtected: Boolean, autoAdd: Boolean },
  emits: ['update:modelValue', 'saved', 'applied'],
  setup(props, { emit }) {
    return () =>
      h('div', { class: 'stub-compose-editor' }, [
        h('span', { class: 'stub-compose-editor__project' }, `编辑器:${props.project ?? ''}`),
        h(
          'button',
          { class: 'stub-editor-close', onClick: () => emit('update:modelValue', false) },
          'stub: 关闭'
        ),
        h('button', { class: 'stub-editor-saved', onClick: () => emit('saved') }, 'stub: 已保存')
      ])
  }
})

const STUBS = {
  ArtTableHeader: passthrough('ArtTableHeader'),
  ArtButtonTable: passthrough('ArtButtonTable'),
  ArtButtonMore: passthrough('ArtButtonMore'),
  ArtSvgIcon: passthrough('ArtSvgIcon'),
  ComposeEditor: ComposeEditorStub
}

/**
 * 把聚合日志文本编成一条条 NDJSON 帧。**每帧必须以 `\n` 结尾**（NDJSON 的 D 就是
 * 行分隔符；忘了它时拆包器会把整帧扣成半行，一帧都解析不出来 —— 这正是第一版
 * 测试自己踩过的坑，真实流的 agent 合帧是带行尾的）。
 */
function ndjsonOf(text: string): string[] {
  const chunks = text.split('\n')
  return chunks.map(
    (line, i) =>
      JSON.stringify({
        seq: i + 1,
        data: btoa(`${line}${i === chunks.length - 1 ? '' : '\n'}`),
        eof: i === chunks.length - 1
      }) + '\n'
  )
}

/** openDockerLogStream 的替身返回值（页面只读 ok/status/body）。 */
function streamResponseOf(lines: string[]): { ok: boolean; status: number; body: ReadableStream } {
  const encoder = new TextEncoder()
  let i = 0
  const body = new ReadableStream<Uint8Array>({
    pull(controller) {
      if (i < lines.length) controller.enqueue(encoder.encode(lines[i++]!))
      else controller.close()
    }
  })
  return { ok: true, status: 200, body }
}

/** 聚合日志的样本（compose CLI 的行形：`服务名(对齐空白) | 正文`）。 */
const LOG_TEXT = [
  'core  | 2026-09-30T12:00:00Z core line 1',
  'web   | 2026-09-30T12:00:01Z web line 1',
  'core  | 2026-09-30T12:00:02Z core line 2'
].join('\n')

async function makeRouter(): Promise<Router> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/docker/projects', name: 'DockerProjects', component: Projects },
      {
        path: '/docker/projects/:name',
        name: 'DockerProjectWorkspace',
        component: ProjectWorkspace
      },
      // 容器行点击的目标路由（占位组件即可 —— 断言的是目标地址，不是那页的渲染）。
      { path: '/docker/containers', component: { template: '<div />' } }
    ]
  })
  await router.push('/')
  await router.isReady()
  return router
}

/** 最近一次 mountWorkspace 用的路由（断言「点行进详情」的目标时读它）。 */
let currentRouter: Router | null = null

async function mountWorkspace(name = 'uni-center'): Promise<VueWrapper> {
  const router = await makeRouter()
  await router.push({ path: `/docker/projects/${name}`, query: { host: 'h1' } })
  await router.isReady()
  currentRouter = router
  const wrapper = mount(ProjectWorkspace, {
    global: { plugins: [router], stubs: STUBS }
  })
  mounted.push(wrapper as VueWrapper)
  await flushPromises()
  await nextTick()
  return wrapper
}

/** 已挂载的页面：用例结束后逐个卸载（Element Plus 的监听/定时器不留给下一个用例）。 */
const mounted: VueWrapper[] = []

const callsOf = (action: string) =>
  api.sendDockerCmd.mock.calls.filter(([, body]) => (body as { action: string }).action === action)

beforeEach(() => {
  vi.stubGlobal(
    'ResizeObserver',
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
  )
  api.fetchDockerHosts.mockClear()
  api.fetchDockerState.mockClear()
  api.sendDockerCmd.mockClear()
  api.fetchDockerCmdResult.mockClear()
  api.openDockerLogStream.mockClear()
  api.fetchDockerHosts.mockResolvedValue(HOSTS)
  api.fetchDockerState.mockResolvedValue(STATE)
  api.sendDockerCmd.mockResolvedValue({ ref: 'cmd-1' })
  api.fetchDockerCmdResult.mockResolvedValue({
    status: 'succeeded',
    payload: COMPOSE_FILE_PAYLOAD
  })
  api.openDockerLogStream.mockResolvedValue(streamResponseOf(ndjsonOf(LOG_TEXT)))
})

afterEach(() => {
  for (const w of mounted.splice(0)) w.unmount()
})

describe('纯函数：聚合日志的服务过滤（行前缀匹配）', () => {
  it('服务名 + 对齐空白 + | 是行前缀；前缀外（CLI 结论句）不归属任何服务', () => {
    expect(composeLogLineService('core  | 2026-09-30T12:00:00Z core line 1')).toBe('core')
    expect(composeLogLineService('web | x')).toBe('web')
    expect(composeLogLineService('no such service: xxx')).toBe('')
    // 服务名字符集与协议白名单同形（冒号/斜杠等不出现在前缀里）。
    expect(composeLogLineService('a.b-c_1 | x')).toBe('a.b-c_1')
  })

  it('过滤保留命中行，丢弃其余；未选服务 = 原文', () => {
    expect(filterComposeLogLines(LOG_TEXT, ['core'])).toBe(
      ['core  | 2026-09-30T12:00:00Z core line 1', 'core  | 2026-09-30T12:00:02Z core line 2'].join(
        '\n'
      )
    )
    expect(filterComposeLogLines(LOG_TEXT, [])).toBe(LOG_TEXT)
    expect(filterComposeLogLines(LOG_TEXT, ['core', 'web'])).toBe(LOG_TEXT)
    // 前缀子串不算命中（we ≠ web）。
    expect(filterComposeLogLines('web | x', ['we'])).toBe('')
  })

  it('CRLF 行尾同样参与过滤（与拆行口径一致）', () => {
    expect(filterComposeLogLines('core | a\r\nweb | b\r\n', ['core'])).toBe('core | a')
  })
})

describe('工作台渲染冒烟（挂载即验证，白屏类故障的守卫）', () => {
  it('hero/网元卡/聚合日志/配置四区都在，元信息与状态结论正确', async () => {
    const w = await mountWorkspace()
    expect(w.find('.project-workspace').exists()).toBe(true)
    // hero：项目名、状态（partial → 部分运行）、备份计数（配置区自动拉到的 1 份）、缺口提示。
    expect(w.find('.pwh__title').text()).toBe('uni-center')
    expect(w.find('.pwh__state').text()).toBe('部分运行')
    expect(w.find('.pwh__meta').text()).toContain('容器 1/2')
    expect(w.find('.pwh__meta').text()).toContain('备份 1 份')
    expect(w.find('.pwh__meta').text()).toContain('另有 1 个网元没有容器')
    // 网元卡：两张卡（core/web），副本与容器行都在。
    expect(w.findAll('.pws-card')).toHaveLength(2)
    expect(w.find('.pws-card__name').text()).toBe('core')
    expect(w.find('.pws-card__replicas').text()).toBe('副本 1/1')
    expect(w.findAll('.pws-container')).toHaveLength(2)
    expect(w.find('.pws-container__name').text()).toBe('uni-center-core')
    // 配置区：basename + 编辑器替身挂上（打开入口的往返在下面的配置区用例里验）。
    expect(w.find('.pwc__files').text()).toBe('docker-compose.yml')
    expect(w.find('.stub-compose-editor').exists()).toBe(true)
  })

  it('项目不在快照里：给结论句与返回入口（不留一块空白菜地）', async () => {
    const w = await mountWorkspace('no-such-project')
    expect(w.find('.el-empty__description').text()).toContain('该主机上没有项目「no-such-project」')
    expect(w.findAll('button').some((b) => b.text() === '返回项目列表')).toBe(true)
  })

  it('网元行归纳：与列表页同口径（按标签归纳、按名排序、running 计数）', () => {
    const rows = deriveServiceRows(
      STATE.containers as unknown as DockerContainerItem[],
      'uni-center'
    )
    expect(rows.map((r) => r.name)).toEqual(['core', 'web'])
    expect(rows[0]).toMatchObject({ running: 1, total: 1, protected: false })
    // 裸容器（无 composeProject 标签）不进项目视图。
    expect(
      deriveServiceRows(
        [{ ...STATE.containers[0]!, composeProject: '' }] as unknown as DockerContainerItem[],
        'uni-center'
      )
    ).toEqual([])
  })
})

describe('服务卡动作派发（语义平移自列表页：按容器逐个发）', () => {
  it('停一个运行中的网元 → 对它的容器逐个发 container:stop', async () => {
    const w = await mountWorkspace()
    const coreCard = w.findAll('.pws-card')[0]!
    const stopBtn = coreCard.findAll('button').find((b) => b.text() === '停止')
    expect(stopBtn).toBeTruthy()
    await stopBtn!.trigger('click')
    await flushPromises()

    const calls = callsOf('container:stop')
    expect(calls).toHaveLength(1)
    expect(calls[0]![0]).toBe('h1')
    expect(calls[0]![1]).toMatchObject({ action: 'container:stop', target: 'uni-center-core' })
  })

  it('已停止的网元点「启动」→ 对非运行容器发 container:start', async () => {
    const w = await mountWorkspace()
    const webCard = w.findAll('.pws-card')[1]!
    const startBtn = webCard.findAll('button').find((b) => b.text() === '启动')
    expect(startBtn).toBeTruthy()
    await startBtn!.trigger('click')
    await flushPromises()

    const calls = callsOf('container:start')
    expect(calls).toHaveLength(1)
    expect(calls[0]![1]).toMatchObject({ action: 'container:start', target: 'uni-center-web' })
  })

  it('点容器行 → 容器列表页 ?host=&id=（7b：深链改指统一表抽屉，旧详情路由已删）', async () => {
    const w = await mountWorkspace()
    await w.findAll('.pws-container')[0]!.trigger('click')
    await flushPromises()
    expect(currentRouter!.currentRoute.value.path).toBe('/docker/containers')
    expect(currentRouter!.currentRoute.value.query.host).toBe('h1')
    expect(currentRouter!.currentRoute.value.query.id).toBe('c1')
  })

  it('受保护网元的写动作先走确认弹窗（不直接发指令，弹窗里有「强制操作」开关）', async () => {
    // 快照换成受保护容器：服务行的 protected 由成员容器承载。
    api.fetchDockerState.mockResolvedValue({
      ...STATE,
      containers: STATE.containers.map((c) => ({ ...c, protected: true }))
    })
    const w = await mountWorkspace()
    const coreCard = w.findAll('.pws-card')[0]!
    await coreCard
      .findAll('button')
      .find((b) => b.text() === '停止')!
      .trigger('click')
    await flushPromises()

    // needForce 分流：container:stop 一条都没发（没勾「强制操作」前发出去必被拒）。
    expect(callsOf('container:stop')).toHaveLength(0)
    // 确认弹窗已打开，且「强制操作」开关在场（受保护 + 有 exec 权限的唯一输入面）。
    expect(w.find('.el-dialog').exists()).toBe(true)
    expect(w.find('.ac-force').exists()).toBe(true)
  })
})

describe('聚合日志接线（compose:logs：指令载荷 + 流消费 + 服务过滤）', () => {
  it('进页面自动拉一次：先发 compose:logs（tail 缺省 100），再接同一条 NDJSON 流端点', async () => {
    const w = await mountWorkspace()

    const calls = callsOf(COMPOSE_LOGS_ACTIONS[0])
    expect(calls).toHaveLength(1)
    expect(calls[0]![0]).toBe('h1')
    expect(calls[0]![1]).toMatchObject({
      action: 'compose:logs',
      target: 'uni-center',
      options: { tail: 100 }
    })
    // 轮询过会话建立，然后接入流端点（与 container:logs 同端点）。
    expect(api.fetchDockerCmdResult).toHaveBeenCalledWith('h1', 'cmd-1')
    expect(api.openDockerLogStream).toHaveBeenCalledWith('h1', 'cmd-1', expect.anything())
    // 帧被解码进缓冲：NDJSON 行（base64）→ 文本，三行都在。
    const body = w.find('.log-viewer__body').text()
    expect(body).toContain('core line 1')
    expect(body).toContain('web line 1')
    expect(body).toContain('core line 2')
    // eof 收尾的结论句（拉取模式说「拉取完毕」，不说「流已结束」）。
    expect(w.find('.pwl__note').text()).toContain('日志已拉取完毕')
  })

  it('服务过滤：多选只留命中行，纯前端过滤不打断缓冲', async () => {
    const w = await mountWorkspace()
    await flushPromises()
    expect(w.find('.log-viewer__body').text()).toContain('web line 1')

    // 过滤下拉（多选）是工具条里的第二个 ElSelect：v-model 经 update:modelValue 驱动。
    const selects = w.findAllComponents({ name: 'ElSelect' })
    expect(selects.length).toBeGreaterThanOrEqual(2)
    const filterSelect = selects[selects.length - 1]!
    ;(filterSelect.vm as unknown as { $emit: (e: string, v: unknown) => void }).$emit(
      'update:modelValue',
      ['core']
    )
    await nextTick()

    const body = w.find('.log-viewer__body').text()
    expect(body).toContain('core line 1')
    expect(body).toContain('core line 2')
    expect(body).not.toContain('web line')
  })

  it('点「拉取」重开会话：再发一条 compose:logs', async () => {
    const w = await mountWorkspace()
    await flushPromises()
    // ⚠ 在日志区工具条里找：hero 的动作条也有一个「拉取」（compose:pull）——
    // 全页 findAll 会先撞上它（写错目标不是组件缺陷，是选择器写歪了）。
    const pullBtn = w
      .find('.pwl__bar')
      .findAll('button')
      .find((b) => b.text() === '拉取')
    expect(pullBtn).toBeTruthy()
    await pullBtn!.trigger('click')
    await flushPromises()
    expect(callsOf(COMPOSE_LOGS_ACTIONS[0])).toHaveLength(2)
  })
})

describe('配置区集成（编辑器 mock 往返）', () => {
  it('就位自动拉备份：compose.file:read 一次，hero 计数来自载荷', async () => {
    await mountWorkspace()
    const calls = callsOf('compose.file:read')
    expect(calls).toHaveLength(1)
    expect(calls[0]![1]).toMatchObject({ action: 'compose.file:read', target: 'uni-center' })
  })

  it('编辑器 saved 往返：重拉备份 + 重拉快照', async () => {
    const w = await mountWorkspace()
    await flushPromises()
    expect(callsOf('compose.file:read')).toHaveLength(1)
    // 首次备份读取成功会触发一次快照重拉（run 的成功回调）+ 首拉本身。
    expect(api.fetchDockerState.mock.calls.length).toBeGreaterThanOrEqual(2)

    await w.find('.stub-editor-saved').trigger('click')
    await flushPromises()

    expect(callsOf('compose.file:read')).toHaveLength(2)
    expect(api.fetchDockerState.mock.calls.length).toBeGreaterThanOrEqual(3)
  })

  it('打开编辑器把项目事实带全（受控开关随 update:modelValue 往返）', async () => {
    const w = await mountWorkspace()
    // 点的是**页面自己的**「编辑」入口（stub 只接事件面，不代替页面逻辑）。
    const editBtn = w.findAll('button').find((b) => b.text() === '编辑')
    expect(editBtn).toBeTruthy()
    await editBtn!.trigger('click')
    await nextTick()
    const editor = w.findComponent({ name: 'ComposeEditor' })
    expect(editor.exists()).toBe(true)
    expect(editor.props('project')).toBe('uni-center')
    expect(editor.props('projectProtected')).toBe(false)
    expect(editor.props('modelValue')).toBe(true)
    // 受控开关：编辑器请求关闭 → 页面收掉「添加服务」意图并回 false。
    await w.find('.stub-editor-close').trigger('click')
    await nextTick()
    expect(w.findComponent({ name: 'ComposeEditor' }).props('modelValue')).toBe(false)
  })
})
