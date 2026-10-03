// @vitest-environment jsdom
/**
 * 任务中心页（views/tasks.vue）的渲染钉子（P2 打磨批 · QA 路 1 的前端三处）：
 *   ① 时间列不再出浮点文案 —— createdAt 毫秒除千的浮点秒在 eventRelativeTime
 *      内整段取整（纯函数的钉子另在 events.test.ts；这里钉页面真实调用形态：
 *      「4.411… 秒前」正是 QA 实测的显示）；
 *   ② 三档筛选的空态话术各说各的（已结束档曾错说全量档的话）；
 *   ③ 长十六进制目标短显（12 位）+ title 全值（模块「长 ID 短显 + title 全显」惯例）。
 * 只读动作的服务端剔除（uni_core 的 docker_tasks.go）在 Go 测试里钉，本文件不管。
 *
 * 桩掉两类外部依赖：'../api'（网络）与 Art* 全局组件（unplugin 自动注册在测试
 * 环境不存在，留 slot 透传）；El* 用真件（radio 交互与 empty 文案都要真渲染）。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'

const api = vi.hoisted(() => ({
  fetchDockerTasks: vi.fn(),
  // 进度子组件的 import 面（展开行才接流 —— 门控用例不展开到真流）。
  openDockerPullStream: vi.fn(),
  openDockerPushStream: vi.fn(),
  openDockerBuildStream: vi.fn(),
  cancelDockerCmd: vi.fn()
}))
vi.mock('../api', () => ({ ...api, default: undefined }))

// 当前用户与权限：取消入口的可见性判据（页面用 useAuth + user store 两处读）。
// 真件是 pinia store（本文件不装 pinia），按模块惯例桩掉整条 import 链。
const authState = vi.hoisted(() => ({ perms: [] as string[] }))
vi.mock('@/hooks/core/useAuth', () => ({
  useAuth: () => ({
    hasAuth: (p: string) => authState.perms.includes(p),
    hasAnyAuth: () => false
  })
}))
const userState = vi.hoisted(() => ({ username: '' }))
vi.mock('@/store/modules/user', () => ({
  useUserStore: () => ({ getUserInfo: userState })
}))

import TasksPage from '../views/tasks.vue'
import type { DockerTaskItem } from '../api'

/** 固定「现在」（秒）：与假定时器同基准，时间文案可精确断言。 */
const BASE_SEC = 1_790_600_000

/** 一条任务条目（默认对齐真实读面形状：毫秒 createdAt、名称目标、终态）。 */
function taskItem(overrides: Partial<DockerTaskItem> = {}): DockerTaskItem {
  return {
    ref: 'r1',
    hostId: 'h1',
    hostname: 'bogon',
    action: 'image:pull',
    target: 'alpine:3.20',
    username: 'alice',
    createdAt: BASE_SEC * 1000 - 60_000,
    status: 'succeeded',
    summary: '',
    ...overrides
  }
}

/** 任务列表响应（8d 起带分页信封：total/page/pageSize —— 页面读 total 报「共 N 条」、
    分页器读同一份 total；缺省形态 = 一页装得下全部）。 */
function taskResp(items: DockerTaskItem[], total: number = items.length, page = 1, pageSize = 10) {
  return { items, total, page, pageSize }
}

/** Art* 全局组件替身：保留 slot，页面内容仍会被渲染。 */
const passthrough = (name: string) =>
  defineComponent({
    name,
    setup:
      (_, { slots }) =>
      () =>
        h('div', { 'data-stub': name }, [slots.default?.()])
  })

const mounted: VueWrapper[] = []

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(BASE_SEC * 1000)
  api.fetchDockerTasks.mockReset()
  api.openDockerPullStream.mockReset()
  api.cancelDockerCmd.mockReset()
  authState.perms = []
  userState.username = ''
})

afterEach(() => {
  for (const w of mounted.splice(0)) w.unmount()
  vi.useRealTimers()
})

/** 微任务 + 渲染冲刷（假定时器下也成立：微任务与 nextTick 不被接管）。 */
async function flush(rounds = 12) {
  for (let i = 0; i < rounds; i++) {
    await Promise.resolve()
    await nextTick()
  }
}

async function mountTasks() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/', component: { template: '<div />' } }]
  })
  await router.push('/')
  await router.isReady()
  const w = mount(TasksPage, {
    global: {
      plugins: [router],
      stubs: {
        ArtButtonTable: passthrough('ArtButtonTable'),
        ArtSvgIcon: passthrough('ArtSvgIcon')
      }
    }
  })
  mounted.push(w as VueWrapper)
  await flush()
  return w as VueWrapper
}

/** 点一档筛选（ElRadioButton 真件：置 radio 并触发 change，服务端过滤按 watch 重拉）。 */
async function clickFilter(w: VueWrapper, label: string) {
  const btn = w.findAll('.el-radio-button').find((b) => b.text() === label)
  expect(btn, `筛选按钮「${label}」应存在`).toBeTruthy()
  await btn!.find('input').setValue()
  await flush()
}

describe('任务中心页 · QA 路 1 的前端三处', () => {
  it('挂载冒烟：页面与页头（任务中心）渲染到位', async () => {
    api.fetchDockerTasks.mockResolvedValue(taskResp([taskItem()]))
    const w = await mountTasks()
    expect(w.find('.docker-tasks').exists()).toBe(true)
    expect(w.text()).toContain('任务中心')
  })

  it('时间列：毫秒除千的浮点秒整段取整，「4.41100001335144 秒前」不再出现', async () => {
    // QA 实测形态：now - createdAt = 4411ms 多一点（浮点除千后是 4.41100001335144）。
    api.fetchDockerTasks.mockResolvedValue(
      taskResp([taskItem({ createdAt: BASE_SEC * 1000 - 4411 })])
    )
    const w = await mountTasks()
    const time = w.find('.tk-item__time')
    expect(time.text()).toBe('4 秒前')
    expect(time.text()).not.toContain('.')
  })

  it('空态话术三档各说各的：全部 / 进行中 / 已结束', async () => {
    api.fetchDockerTasks.mockResolvedValue(taskResp([]))
    const w = await mountTasks()
    expect(w.find('.el-empty__description').text()).toBe('还没有受理过任何任务')

    await clickFilter(w, '进行中')
    expect(w.find('.el-empty__description').text()).toBe('没有进行中的任务')
    // 服务端过滤的 query 如实透传（前端不做本地过滤的既有纪律）。
    expect(api.fetchDockerTasks).toHaveBeenLastCalledWith({
      status: 'pending',
      page: 1,
      pageSize: 10
    })

    await clickFilter(w, '已结束')
    expect(w.find('.el-empty__description').text()).toBe('没有已结束的任务')
    expect(api.fetchDockerTasks).toHaveBeenLastCalledWith({ status: 'done', page: 1, pageSize: 10 })
  })

  it('目标长 ID 短显（12 位，含 sha256: 前缀形态）+ title 全值；名称与引用原样', async () => {
    const hex64 = 'a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2'
    api.fetchDockerTasks.mockResolvedValue(
      taskResp([
        taskItem({ ref: 'r1', action: 'container:restart', target: hex64 }),
        taskItem({ ref: 'r2', action: 'image:remove', target: `sha256:${hex64}` }),
        taskItem({ ref: 'r3', action: 'image:pull', target: 'alpine:3.20' })
      ])
    )
    const w = await mountTasks()
    const targets = w.findAll('.tk-item__target')
    expect(targets).toHaveLength(3)

    expect(targets[0]!.text()).toBe(hex64.slice(0, 12))
    expect(targets[0]!.attributes('title')).toBe(hex64)
    // sha256: 前缀剥掉后再短显（与 container-detail / events-feed 的口径同款）。
    expect(targets[1]!.text()).toBe(hex64.slice(0, 12))
    expect(targets[1]!.attributes('title')).toBe(`sha256:${hex64}`)
    // 名称 / 镜像引用不截。
    expect(targets[2]!.text()).toBe('alpine:3.20')
    expect(targets[2]!.attributes('title')).toBe('alpine:3.20')
  })
})

describe('展开行的取消入口（观看与执行解耦：取消是独立动作）', () => {
  /** 展开第一条行（点行内的展开开关；进度子组件随即挂载并接流）。 */
  async function expandFirst(w: VueWrapper) {
    const toggle = w.find('.tk-item__toggle')
    expect(toggle.exists(), '在途进度族任务应可展开').toBe(true)
    await toggle.trigger('click')
    await flush()
  }

  it('自己的在途任务 + docker:manage：展开后出现取消入口（收起说明同时在场）', async () => {
    authState.perms = ['docker:manage']
    userState.username = 'alice'
    api.fetchDockerTasks.mockResolvedValue(taskResp([taskItem({ status: 'pending' })]))
    api.openDockerPullStream.mockResolvedValue({ ok: false, status: 404 }) // 门控用例不需真流
    const w = await mountTasks()
    await expandFirst(w)

    expect(w.text()).toContain('取消拉取')
    expect(w.text()).toContain('收起不取消任务')
  })

  it('别人的在途任务：进度照看，但不给取消入口（服务端归属校验的可见性一档）', async () => {
    authState.perms = ['docker:manage']
    userState.username = 'alice'
    api.fetchDockerTasks.mockResolvedValue(
      taskResp([taskItem({ status: 'pending', username: 'bob' })])
    )
    api.openDockerPullStream.mockResolvedValue({ ok: false, status: 404 })
    const w = await mountTasks()
    await expandFirst(w)

    expect(w.text()).not.toContain('取消拉取')
  })

  it('无 docker:manage：不给取消入口（服务端必 403）', async () => {
    authState.perms = ['docker:list']
    userState.username = 'alice'
    api.fetchDockerTasks.mockResolvedValue(taskResp([taskItem({ status: 'pending' })]))
    api.openDockerPullStream.mockResolvedValue({ ok: false, status: 404 })
    const w = await mountTasks()
    await expandFirst(w)

    expect(w.text()).not.toContain('取消拉取')
  })

  it('构建/推送的在途任务同样可展开（进度三族同一条观看面）', async () => {
    authState.perms = ['docker:manage']
    userState.username = 'alice'
    api.fetchDockerTasks.mockResolvedValue(
      taskResp([
        taskItem({ ref: 'r1', action: 'image:build', status: 'pending', target: 'app:1' }),
        taskItem({ ref: 'r2', action: 'image:push', status: 'pending', target: 'app:1' })
      ])
    )
    api.openDockerBuildStream.mockResolvedValue({ ok: false, status: 404 })
    api.openDockerPushStream.mockResolvedValue({ ok: false, status: 404 })
    const w = await mountTasks()
    const toggles = w.findAll('.tk-item__toggle')
    expect(toggles.length, '两条在途进度族任务都应可展开').toBe(2)

    await toggles[0]!.trigger('click')
    await flush()
    expect(api.openDockerBuildStream).toHaveBeenCalledWith('h1', 'r1', expect.anything())
    expect(w.text()).toContain('取消构建')

    await toggles[1]!.trigger('click')
    await flush()
    expect(api.openDockerPushStream).toHaveBeenCalledWith('h1', 'r2', expect.anything())
    expect(w.text()).toContain('取消推送')
  })

  it('非进度族在途任务（如 container:restart）：不可展开（没有可看的流）', async () => {
    authState.perms = ['docker:manage']
    userState.username = 'alice'
    api.fetchDockerTasks.mockResolvedValue(
      taskResp([taskItem({ action: 'container:restart', status: 'pending' })])
    )
    const w = await mountTasks()
    expect(w.find('.tk-item__toggle').exists()).toBe(false)
  })
})

describe('分页（8d：服务端分页 + total 报价）', () => {
  /** 动态桩：按请求回显 page/pageSize（与真端点契约同形），total 固定。 */
  function dynamicResp(total: number) {
    return async (q: { page?: number; pageSize?: number } = {}) =>
      taskResp([taskItem()], total, q.page ?? 1, q.pageSize ?? 10)
  }

  it('页头「共 N 条」与页尾「第 X 页 / 共 N 条」同源，分页器与页大小选择器在场', async () => {
    api.fetchDockerTasks.mockImplementation(dynamicResp(25))
    const w = await mountTasks()
    expect(w.text()).toContain('共 25 条')
    expect(w.find('.tk-page__hint').text()).toBe('第 1 页 / 共 25 条')
    expect(w.find('.el-pagination').exists()).toBe(true)
    expect(w.find('.el-pagination__sizes').exists()).toBe(true)
  })

  it('点第 2 页：请求带 page=2，页码回显跟着走（服务端切页，前端不本地切）', async () => {
    api.fetchDockerTasks.mockImplementation(dynamicResp(25))
    const w = await mountTasks()
    const pages = w.findAll('.el-pager li')
    expect(pages.length, '共 25 条 / 每页 10 条应有 3 页').toBe(3)
    await pages.find((li) => li.text() === '2')!.trigger('click')
    await flush()
    expect(api.fetchDockerTasks).toHaveBeenLastCalledWith({ page: 2, pageSize: 10 })
    expect(w.find('.tk-page__hint').text()).toBe('第 2 页 / 共 25 条')
  })

  it('筛选切换回到第 1 页（旧页码属于旧集合）', async () => {
    api.fetchDockerTasks.mockImplementation(dynamicResp(25))
    const w = await mountTasks()
    await w
      .findAll('.el-pager li')
      .find((li) => li.text() === '2')!
      .trigger('click')
    await flush()
    await clickFilter(w, '进行中')
    expect(api.fetchDockerTasks).toHaveBeenLastCalledWith({
      status: 'pending',
      page: 1,
      pageSize: 10
    })
  })

  it('越界页自愈：页码落空（总数变小）时拉回最后一页，不自称「没有任务」', async () => {
    api.fetchDockerTasks.mockResolvedValueOnce(taskResp([taskItem()], 25))
    // 第 3 页的这一次：总数已随保留清理缩到 15（只剩 2 页），本页落空。
    api.fetchDockerTasks.mockResolvedValueOnce(taskResp([], 15, 3))
    api.fetchDockerTasks.mockImplementation(dynamicResp(15))
    const w = await mountTasks()
    await w
      .findAll('.el-pager li')
      .find((li) => li.text() === '3')!
      .trigger('click')
    await flush()
    expect(api.fetchDockerTasks).toHaveBeenLastCalledWith({ page: 2, pageSize: 10 })
    expect(w.find('.tk-page__hint').text()).toBe('第 2 页 / 共 15 条')
    expect(w.findAll('.tk-item').length).toBe(1)
    expect(w.text()).not.toContain('没有已结束的任务')
  })

  it('空列表：不出分页条；副标题报「共 0 条」（硬事实）—— 三档空态话术不受影响', async () => {
    api.fetchDockerTasks.mockResolvedValue(taskResp([]))
    const w = await mountTasks()
    expect(w.find('.el-pagination').exists()).toBe(false)
    expect(w.find('.el-empty__description').text()).toBe('还没有受理过任何任务')
    expect(w.text()).toContain('跨主机 · 共 0 条 · 每 5 秒自动刷新')
  })
})
