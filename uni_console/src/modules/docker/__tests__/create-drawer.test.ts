// @vitest-environment jsdom
/**
 * 创建抽屉（4a 前端半边）的接线测试。
 *
 * 纯函数层（校验规则/载荷形状/命令预览）在 container-create.test.ts；这里钉的是
 * **组件把哪条数据接到哪个控件**：
 *   - 打开 → 预选首个 Docker 可用主机 + 拉它的快照（镜像选项的唯一来源）；
 *   - 镜像不在所选主机本地 → 引导句 + 不进确认弹窗（不发指令）；
 *   - 表单有问题 → 「创建」不弹确认（协议规则在前端先挡一道）；
 *   - 提交链路 → 标准档确认（目标卡 = 镜像 → 容器名）→ 指令通道按所选主机派发
 *     container:create（**无 target**，options 平铺）；
 *   - 成功 → 关抽屉 + created 事件（载荷 id/short_id/started 边界折叠）+ 重拉；
 *   - 失败（镜像缺失结论句）→ 抽屉留着可重试。
 *
 * ── 怎么驱动表单 ───────────────────────────────────────────────────
 * ElInput 走原生 input 事件（setValue）；ElSelect 走组件的 update:modelValue
 * （workload-drawer 测试对 ElTabs 的同款手法 —— 断言的是父组件的 v-model 落点，
 * 不赌 EP 内部交互）。抽屉与确认弹窗都 teleport 到 body，断言查 document.body。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import { DOMWrapper, mount, type VueWrapper } from '@vue/test-utils'

const api = vi.hoisted(() => ({
  fetchDockerState: vi.fn(),
  sendDockerCmd: vi.fn(),
  fetchDockerCmdResult: vi.fn()
}))
vi.mock('../api', () => ({ ...api, default: undefined }))

vi.mock('@/hooks/core/useAuth', () => ({
  useAuth: () => ({ hasAuth: () => true, hasAnyAuth: () => true })
}))

import CreateContainerDrawer from '../components/create-container-drawer.vue'
import type { DockerHostItem } from '../api'

/** 三台主机：前两台可用，第三台 dockerOk=false（不该出现在主机下拉里）。 */
const HOSTS: DockerHostItem[] = [
  {
    id: 'h1',
    hostname: 'bogon',
    primaryIp: '192.168.12.105',
    online: true,
    dockerOk: true,
    containers: 3,
    images: 10,
    stale: false
  },
  {
    id: 'h2',
    hostname: 'nas',
    primaryIp: '192.168.12.200',
    online: true,
    dockerOk: true,
    containers: 1,
    images: 5,
    stale: false
  },
  {
    id: 'h3',
    hostname: 'broken',
    primaryIp: '192.168.12.9',
    online: true,
    dockerOk: false,
    containers: 0,
    images: 0,
    stale: false
  }
]

/** h1 的快照：两镜像（其一悬空）、一卷、四网络（none 应被排除）。 */
const STATE_H1 = {
  stale: false,
  ageSeconds: 3,
  neverReported: false,
  dockerOk: true,
  containers: [],
  images: [
    {
      id: 'sha256:1111111111111111111111111111111111111111111111111111111111111111',
      repoTags: ['mysql:8'],
      sizeMb: 223,
      inUse: false,
      dangling: false
    },
    {
      id: 'sha256:2222222222222222222222222222222222222222222222222222222222222222',
      repoTags: [],
      sizeMb: 40,
      inUse: false,
      dangling: true
    }
  ],
  volumes: [{ name: 'dbdata', inUse: false, protected: false }],
  networks: [
    { name: 'bridge', driver: 'bridge', scope: 'local', internal: false, containersCount: 3 },
    { name: 'none', driver: 'null', scope: 'local', internal: true, containersCount: 0 },
    { name: 'app-net', driver: 'bridge', scope: 'local', internal: false, containersCount: 1 }
  ],
  projects: []
}

const STATE_H2 = {
  ...STATE_H1,
  images: [{ id: 'sha256:3333', repoTags: ['redis:7'], sizeMb: 30, inUse: false, dangling: false }]
}

const CREATE_OK_RESULT = {
  status: 'succeeded',
  detail: '已创建容器 abc123',
  payload: { id: 'abcdef1234567890abcdef', short_id: 'abcdef123456', started: true }
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
  api.fetchDockerState.mockImplementation(async (hostId: string) =>
    hostId === 'h2' ? STATE_H2 : STATE_H1
  )
  api.sendDockerCmd.mockResolvedValue({ ref: 'r1' })
  api.fetchDockerCmdResult.mockResolvedValue(CREATE_OK_RESULT)
  refresh.mockClear()
})

afterEach(() => {
  for (const w of mounted.splice(0)) w.unmount()
  vi.clearAllMocks()
  vi.unstubAllGlobals()
})

async function mountDrawer(props: Record<string, unknown> = {}) {
  const w = mount(CreateContainerDrawer, {
    props: { modelValue: true, hosts: HOSTS, refresh, ...props }
  })
  mounted.push(w)
  await new Promise((r) => setTimeout(r, 0))
  await nextTick()
  return w
}

/** 抽屉/确认弹窗都 teleport 到 body：断言一律查 body。 */
const bodyText = () => document.body.textContent ?? ''
const bodyButtons = () =>
  Array.from(document.body.querySelectorAll('button')).map((b) => ({
    el: b,
    text: (b.textContent ?? '').trim()
  }))

async function clickButton(text: string) {
  const btn = bodyButtons().find((b) => b.text === text)
  expect(btn, `按钮「${text}」应已渲染`).toBeTruthy()
  await btn!.el.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  await nextTick()
  await new Promise((r) => setTimeout(r, 0))
  await nextTick()
}

/**
 * ElSelect 的落点（组件顺序 = 模板顺序；行内 select 只在加了行之后出现）：
 *   0 = 主机、1 = 镜像、2 = 重启策略、3 = 网络；端口行的协议 select、挂载行的源
 *   select 在其后（按行序追加）。
 *
 * 同时发 update:modelValue 与 change：前者是 v-model 的落点，后者是主机下拉
 * 的 @change（换主机重拉快照的钩子）—— 真实 EP 在用户选择时两个都发。
 */
async function setSelect(w: VueWrapper, index: number, value: string) {
  const selects = w.findAllComponents({ name: 'ElSelect' })
  expect(selects.length, '应有足够的下拉可用').toBeGreaterThan(index)
  selects[index]!.vm.$emit('update:modelValue', value)
  selects[index]!.vm.$emit('change', value)
  await nextTick()
}

/** ElInput 走原生 input（placeholder 定位；抽屉 teleport 到 body，w.find 看不见）。 */
async function setInput(placeholder: string, value: string) {
  const el = document.body.querySelector(`input[placeholder="${placeholder}"]`)
  expect(el, `输入框「${placeholder}」应已渲染`).toBeTruthy()
  await new DOMWrapper(el as HTMLInputElement).setValue(value)
  await nextTick()
}

describe('打开与主机（dockerOk 过滤 + 预选 + 快照来源）', () => {
  it('打开即预选首个 Docker 可用主机并拉它的快照；不可用主机不进下拉', async () => {
    await mountDrawer()
    expect(api.fetchDockerState).toHaveBeenCalledWith('h1')
    expect(bodyText()).not.toContain('broken')
    expect(bodyText()).toContain('创建容器')
  })

  it('切换主机按新主机拉快照（镜像/卷/网络都是主机事实）', async () => {
    const w = await mountDrawer()
    api.fetchDockerState.mockClear()
    await setSelect(w, 0, 'h2')
    expect(api.fetchDockerState).toHaveBeenCalledWith('h2')
  })

  it('镜像详情入口：预填主机与镜像（快照按预填主机拉）', async () => {
    await mountDrawer({ initialHostId: 'h2', initialImage: 'redis:7' })
    expect(api.fetchDockerState).toHaveBeenCalledWith('h2')
    expect(bodyText()).toContain('docker run redis:7')
  })

  it('预填主机不可用时落到首个可用主机（不猜一个创建必失败的地方）', async () => {
    await mountDrawer({ initialHostId: 'h3' })
    expect(api.fetchDockerState).toHaveBeenCalledWith('h1')
  })
})

describe('镜像缺失引导（create 不自动拉取 —— 提前替 agent 说这句结论）', () => {
  it('填了不在所选主机本地的镜像 → 引导句 + 点创建不弹确认不发指令', async () => {
    const w = await mountDrawer()
    await setSelect(w, 1, 'nginx:1.25')
    expect(bodyText()).toContain('该主机本地没有这个镜像')
    expect(bodyText()).toContain('拉取')

    await clickButton('创建…')
    expect(api.sendDockerCmd).not.toHaveBeenCalled()
    // 确认弹窗没出现（标准档只在表单干净时才弹）。
    expect(bodyText()).not.toContain('创建容器确认')
  })

  it('悬空镜像按完整 id 引用（短 id 只是展示惯例）也放行', async () => {
    const w = await mountDrawer()
    await setSelect(w, 1, 'sha256:1111111111111111111111111111111111111111111111111111111111111111')
    expect(bodyText()).not.toContain('该主机本地没有这个镜像')
  })
})

describe('表单校验挡提交（不发指令）', () => {
  it('半行端口当场显错，点创建不弹确认', async () => {
    const w = await mountDrawer()
    await setSelect(w, 1, 'mysql:8')
    await clickButton('＋ 添加端口')
    await setInput('宿主端口', '8080')

    expect(bodyText()).toContain('宿主与容器端口都要填')
    await clickButton('创建…')
    expect(api.sendDockerCmd).not.toHaveBeenCalled()
  })

  it('容器名非法显错', async () => {
    const w = await mountDrawer()
    await setSelect(w, 1, 'mysql:8')
    await setInput('不填则由 Docker 自动命名', '_bad')
    expect(bodyText()).toContain('容器名须以字母数字开头')
  })
})

describe('run 命令预览（实时、未填项不出现）', () => {
  it('镜像与名称进命令；未填段不出现', async () => {
    const w = await mountDrawer()
    await setSelect(w, 1, 'mysql:8')
    await setInput('不填则由 Docker 自动命名', 'db')
    expect(bodyText()).toContain('docker run --name db mysql:8')
    expect(bodyText()).not.toContain('--restart')
    expect(bodyText()).not.toContain('--network')
  })

  it('关掉「创建后启动」→ docker create（CLI 的只创建写法）', async () => {
    const w = await mountDrawer()
    await setSelect(w, 1, 'mysql:8')
    const switchEl = w.findComponent({ name: 'ElSwitch' })
    switchEl.vm.$emit('update:modelValue', false)
    await nextTick()
    expect(bodyText()).toContain('docker create mysql:8')
  })

  it('列表编辑器加的行即时进命令', async () => {
    const w = await mountDrawer()
    await setSelect(w, 1, 'mysql:8')
    await clickButton('＋ 添加端口')
    await setInput('宿主端口', '3306')
    await setInput('容器端口', '3306')
    expect(bodyText()).toContain('-p 3306:3306')
    expect(bodyText()).not.toContain('/udp')
  })
})

describe('提交链路（标准档确认 → 指令通道）', () => {
  it('确认后按所选主机派发 container:create（无 target，options 平铺）', async () => {
    const w = await mountDrawer()
    await setSelect(w, 1, 'mysql:8')
    await setInput('不填则由 Docker 自动命名', 'db')
    await clickButton('＋ 添加端口')
    await setInput('宿主端口', '3306')
    await setInput('容器端口', '3306')
    await clickButton('＋ 添加变量')
    await setInput('变量名', 'MYSQL_ROOT_PASSWORD')
    await setInput('值', 'secret')

    await clickButton('创建…')
    // 标准档弹窗：标题与目标卡（镜像名 → 容器名），补充结论句说启动行为。
    expect(bodyText()).toContain('创建容器确认')
    expect(bodyText()).toContain('mysql:8 → db')
    expect(bodyText()).toContain('创建后将立即启动容器')
    expect(api.sendDockerCmd).not.toHaveBeenCalled()

    // 主按钮按注册表标签（4a 起 confirm 档不再硬编码「删除」）。
    await clickButton('创建容器')
    expect(api.sendDockerCmd).toHaveBeenCalledTimes(1)
    const [hostId, body] = api.sendDockerCmd.mock.calls[0] as [string, Record<string, unknown>]
    expect(hostId).toBe('h1')
    expect(body.action).toBe('container:create')
    // create 无 target：带上 target 是协议的字段归属错误。
    expect(body.target).toBeUndefined()
    expect(body.options).toEqual({
      image: 'mysql:8',
      name: 'db',
      ports: ['3306:3306'],
      env: ['MYSQL_ROOT_PASSWORD=secret']
    })
    expect(body.confirm).toBe('') // 标准档无逐字值
  })

  it('成功：结论句 + created 事件（载荷边界折叠）+ 关抽屉 + 重拉', async () => {
    const w = await mountDrawer()
    await setSelect(w, 1, 'mysql:8')
    await clickButton('创建…')
    await clickButton('创建容器')

    expect(api.fetchDockerCmdResult).toHaveBeenCalledWith('h1', 'r1')
    const created = w.emitted('created')
    expect(created).toBeTruthy()
    expect(created![0]![0]).toEqual({
      id: 'abcdef1234567890abcdef',
      shortId: 'abcdef123456',
      started: true
    })
    // 成功才关抽屉（update:modelValue 最后一次是 false）。
    const closed = w.emitted('update:modelValue')
    expect(closed![closed!.length - 1]).toEqual([false])
    // 双次重拉的「立即」那次（落定的 1.5s 那次在 composables.test 已钉）。
    expect(refresh).toHaveBeenCalled()
    expect(document.querySelector('.el-message--success')?.textContent).toContain(
      '已创建容器 abc123'
    )
  })

  it('失败（镜像缺失结论句）：抽屉留着可重试，不发 created', async () => {
    api.fetchDockerCmdResult.mockResolvedValue({
      status: 'failed',
      error: '本机没有这个镜像，请先拉取镜像后再创建容器'
    })
    const w = await mountDrawer()
    await setSelect(w, 1, 'mysql:8')
    await clickButton('创建…')
    await clickButton('创建容器')

    expect(w.emitted('created')).toBeUndefined()
    const closed = w.emitted('update:modelValue')
    expect(closed, '失败不关抽屉').toBeUndefined()
    expect(document.querySelector('.el-message--error')?.textContent).toContain('本机没有这个镜像')
    // （弹窗关闭是 v-show 隐藏、DOM 文本仍在 —— 不用文本断言它收起。）
  })
})
