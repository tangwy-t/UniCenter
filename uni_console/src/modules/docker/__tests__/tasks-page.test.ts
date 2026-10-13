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
 * 本波（任务中心 UI 美化）增补两组钉子：列式表形态（表头/行共用同一组列 class、
 * 单行结构、跨列展开区）与列式表的样式约定（表头吸顶、固定列宽、让位内边距的
 * 选择器层级、窄视口整列让位）—— 后者是源码断言，理由与 layout-reachability
 * 同款：jsdom 没有布局引擎，真值走 playwright 真滚轮探针。
 *
 * 桩掉两类外部依赖：'../api'（网络）与 Art* 全局组件（unplugin 自动注册在测试
 * 环境不存在，留 slot 透传）；El* 用真件（radio 交互与 empty 文案都要真渲染）。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
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
    分页器读同一份 total；缺省形态 = 一页装得下全部）。
    页大小缺省 20：与页面默认档一致（页面以服务端回显为准，回显 10 会把本地
    默认档顶回去 —— 这正是本文件曾经漏掉的一处耦合）。 */
function taskResp(items: DockerTaskItem[], total: number = items.length, page = 1, pageSize = 20) {
  return { items, total, page, pageSize }
}

/** Art* 全局组件替身：保留 slot（default + left —— 工具条内容在 ArtTableHeader 的
 *  #left 里，替身不渲染它的话过滤/返回控件整块看不见）。 */
const passthrough = (name: string) =>
  defineComponent({
    name,
    setup:
      (_, { slots }) =>
      () =>
        h('div', { 'data-stub': name }, [slots.default?.(), slots.left?.()])
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
        ArtTableHeader: passthrough('ArtTableHeader'),
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
  it('挂载冒烟：页面与工具条（返回 + 状态过滤）渲染到位', async () => {
    api.fetchDockerTasks.mockResolvedValue(taskResp([taskItem()]))
    const w = await mountTasks()
    expect(w.find('.docker-tasks').exists()).toBe(true)
    // 列表页纯化后页头（hero：图标+标题）已删：页面的身份由工具条的返回钮与
    // 状态过滤（全部/进行中/已结束）承载。
    expect(w.findAll('.el-radio-button').map((b) => b.text())).toEqual(['全部', '进行中', '已结束'])
    const back = w
      .findAll('[data-stub="ArtButtonTable"]')
      .find((b) => b.attributes('title') === '返回上一页')
    expect(back, '工具条应有返回钮').toBeTruthy()
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
      pageSize: 20
    })

    await clickFilter(w, '已结束')
    expect(w.find('.el-empty__description').text()).toBe('没有已结束的任务')
    expect(api.fetchDockerTasks).toHaveBeenLastCalledWith({ status: 'done', page: 1, pageSize: 20 })
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

  it('自己的在途任务 + docker:manage：展开后出现取消入口（收起语义收在展开开关的 title）', async () => {
    authState.perms = ['docker:manage']
    userState.username = 'alice'
    api.fetchDockerTasks.mockResolvedValue(taskResp([taskItem({ status: 'pending' })]))
    api.openDockerPullStream.mockResolvedValue({ ok: false, status: 404 }) // 门控用例不需真流
    const w = await mountTasks()
    await expandFirst(w)

    expect(w.text()).toContain('取消拉取')
    // 「收起不取消任务」的语义在展开开关的 title 上（可见正文不再重复一句）。
    expect(w.find('.tk-item__toggle').attributes('title')).toBe('收起（不取消任务）')
    expect(w.text()).not.toContain('收起不取消任务')
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
      taskResp([taskItem()], total, q.page ?? 1, q.pageSize ?? 20)
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
    expect(pages.length, '共 25 条 / 每页 20 条应有 2 页').toBe(2)
    await pages.find((li) => li.text() === '2')!.trigger('click')
    await flush()
    expect(api.fetchDockerTasks).toHaveBeenLastCalledWith({ page: 2, pageSize: 20 })
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
      pageSize: 20
    })
  })

  it('越界页自愈：页码落空（总数变小）时拉回最后一页，不自称「没有任务」', async () => {
    api.fetchDockerTasks.mockResolvedValueOnce(taskResp([taskItem()], 45))
    // 第 3 页的这一次：总数已随保留清理缩到 25（每页 20 → 只剩 2 页），本页落空。
    api.fetchDockerTasks.mockResolvedValueOnce(taskResp([], 25, 3))
    api.fetchDockerTasks.mockImplementation(dynamicResp(25))
    const w = await mountTasks()
    await w
      .findAll('.el-pager li')
      .find((li) => li.text() === '3')!
      .trigger('click')
    await flush()
    expect(api.fetchDockerTasks).toHaveBeenLastCalledWith({ page: 2, pageSize: 20 })
    expect(w.find('.tk-page__hint').text()).toBe('第 2 页 / 共 25 条')
    expect(w.findAll('.tk-item').length).toBe(1)
    expect(w.text()).not.toContain('没有已结束的任务')
  })

  it('空列表：不出分页条；机制说明句（跨主机/自动刷新节拍）随 hero 删除 —— 三档空态话术不受影响', async () => {
    api.fetchDockerTasks.mockResolvedValue(taskResp([]))
    const w = await mountTasks()
    expect(w.find('.el-pagination').exists()).toBe(false)
    expect(w.find('.el-empty__description').text()).toBe('还没有受理过任何任务')
    // 零解释文案纪律：页面上不再有「跨主机 · 每 5 秒自动刷新」这类机制句。
    expect(w.text()).not.toContain('跨主机')
    expect(w.text()).not.toContain('自动刷新')
  })
})

describe('布局与滚动的回归守卫（本波布局链修复）', () => {
  it('每页条数默认 20，可选档仍是 10/20/50/100（与列表页 50 档同向）', async () => {
    api.fetchDockerTasks.mockResolvedValue(taskResp([taskItem()], 25))
    const w = await mountTasks()
    // 默认页大小直接决定**首拉请求**（服务端分页，前端不本地切）。
    expect(api.fetchDockerTasks).toHaveBeenLastCalledWith({ page: 1, pageSize: 20 })
    const pag = w.findComponent({ name: 'ElPagination' })
    expect(pag.exists(), '分页器在场').toBe(true)
    expect(pag.props('pageSize')).toBe(20)
    expect(pag.props('pageSizes')).toEqual([10, 20, 50, 100])
  })

  it('已成功的任务（summary 非空）也可展开：点开看得到收尾结论原文', async () => {
    api.fetchDockerTasks.mockResolvedValue(
      taskResp([taskItem({ status: 'succeeded', action: 'image:pull', summary: '拉取完成：alpine:3.20' })])
    )
    const w = await mountTasks()
    // 修前只认 failed/timeout 两态：成功行既没有展开开关、也没有详情入口 ——
    // 用户点不动就以为「没有输出」（P1 实测评审）。
    const toggle = w.find('.tk-item__toggle')
    expect(toggle.exists(), '已成功且有结论句的行应有展开开关').toBe(true)

    await toggle.trigger('click')
    await flush()
    expect(w.find('.tk-item__detail').exists()).toBe(true)
    expect(w.find('.tk-item__summary').text()).toBe('拉取完成：alpine:3.20')
  })

  it('已成功但无结论句（summary 为空）的行不展开（没有可展示的事实，不给空开关）', async () => {
    api.fetchDockerTasks.mockResolvedValue(
      taskResp([taskItem({ status: 'succeeded', action: 'container:remove', summary: '' })])
    )
    const w = await mountTasks()
    expect(w.find('.tk-item__toggle').exists()).toBe(false)
  })
})

// ── 列式表形态（本波「任务中心 UI 美化」）────────────────────────────────
// 改造前每行是自绘两行块（动作+目标一行、元信息一行），目标短时中间一大片空白、
// 时间被 margin-left:auto 推到极右 —— 没有列对齐，1920 下是一条没排版的清单。
// 现在是列式表：可见表头 + 单行行高 + 展开区跨列铺满。这一组钉住「列对齐」赖以
// 成立的结构（表头与行共用同一组列 class），以及被删掉的两行块不会回来。

/** 表头的列名（顺序即列顺序；最后一列是只读屏可见的「展开」）。 */
const HEAD_LABELS = ['状态', '动作', '目标', '主机', '发起人', '时间', '展开']

/** 与表头一一对应的列 class（列宽与响应式让位都挂在这组 class 上）。 */
const COL_CLASSES = [
  'tk-col--status',
  'tk-col--action',
  'tk-col--target',
  'tk-col--host',
  'tk-col--user',
  'tk-col--time',
  'tk-col--toggle'
]

describe('列式表形态（表头 / 单行 / 跨列展开区）', () => {
  it('表头一行七列，且与每行的单元格共用同一组列 class（列对齐的结构前提）', async () => {
    api.fetchDockerTasks.mockResolvedValue(taskResp([taskItem()]))
    const w = await mountTasks()

    const heads = w.findAll('.tk-head th')
    expect(heads.map((th) => th.text().trim())).toEqual(HEAD_LABELS)
    expect(heads.map((th) => COL_CLASSES.filter((c) => th.classes().includes(c)))).toEqual(
      COL_CLASSES.map((c) => [c])
    )

    // 行侧：单元格数量与顺序都同上 —— 表头与数据行落在同一组列线上（改造前没有
    // 表头，也没有任何列对齐）。
    const cells = w.findAll('.tk-item__line td')
    expect(cells).toHaveLength(HEAD_LABELS.length)
    expect(cells.map((td) => COL_CLASSES.filter((c) => td.classes().includes(c)))).toEqual(
      COL_CLASSES.map((c) => [c])
    )
  })

  it('行是单行结构：改造前的两行块（元信息行）已删，不留结构残壳', async () => {
    api.fetchDockerTasks.mockResolvedValue(taskResp([taskItem()]))
    const w = await mountTasks()
    expect(w.findAll('.tk-item__line')).toHaveLength(1)
    for (const dead of ['.tk-item__meta', '.tk-item__main', '.tk-item__line .tk-item__line']) {
      expect(w.find(dead).exists(), `${dead} 是改造前的两行块残壳`).toBe(false)
    }
    // 时间不再靠 margin-left:auto 单飞：它现在是时间列（右对齐由列 class 承担）。
    expect(w.find('.tk-item__time').classes()).toContain('tk-col--time')
  })

  it('状态单元格里圆点与状态词同列（颜色永不单独传达：文字就是结论）', async () => {
    api.fetchDockerTasks.mockResolvedValue(
      taskResp([taskItem({ status: 'failed', summary: '打开终端失败' })])
    )
    const w = await mountTasks()
    const status = w.find('.tk-item__status')
    expect(status.classes()).toContain('tk-col--status')
    expect(status.find('.tk-item__dot').exists(), '圆点仍在状态列内').toBe(true)
    expect(status.find('.tk-status__text').text(), '状态词在圆点旁边，不是靠颜色分别传达').toBe(
      '失败'
    )
  })

  it('展开区是整行（colspan 铺满七列），不是某一列的单元格', async () => {
    api.fetchDockerTasks.mockResolvedValue(
      taskResp([taskItem({ status: 'succeeded', summary: '拉取完成：alpine:3.20' })])
    )
    const w = await mountTasks()
    await w.find('.tk-item__toggle').trigger('click')
    await flush()

    const detail = w.find('.tk-item__detail')
    expect(detail.exists()).toBe(true)
    const cell = detail.find('td')
    expect(cell.attributes('colspan')).toBe(String(HEAD_LABELS.length))
    expect(detail.find('.tk-item__summary').text()).toBe('拉取完成：alpine:3.20')
  })
})

// ── 列式表的样式约定（jsdom 没有布局引擎，钉住让探针变绿的结构）──────────
// 真值验证走 playwright 真滚轮探针（.tmp-ux-20261010/redesign-tasks/：四档视口
// 滚到底分页器可见 + elementFromPoint 可点 + 第 2 页真的翻过去）；这里钉的是
// 这些结论背后的**结构**不改回去 —— 上一轮正是这类结构无声回归过。
describe('列式表的样式约定（表头吸顶 / 固定列宽 / 单元格不换行）', () => {
  const src = readFileSync(resolve(process.cwd(), 'src/modules/docker/views/tasks.vue'), 'utf8')

  /** 剥注释：文档里引用旧写法不该被当成真实声明。 */
  function stripComments(source: string): string {
    return source
      .replace(/<!--[\s\S]*?-->/g, '')
      .replace(/\/\*[\s\S]*?\*\//g, '')
      .replace(/\/\/[^\n]*/g, '')
  }

  /** 取一段 SCSS 块（从 from 起、花括号配平到结束）。 */
  function block(source: string, from: string): string {
    const start = source.indexOf(from)
    expect(start, `未找到块：${from}`).toBeGreaterThanOrEqual(0)
    let depth = 0
    for (let i = start; i < source.length; i++) {
      if (source[i] === '{') depth++
      else if (source[i] === '}') {
        depth--
        if (depth === 0) return source.slice(start, i + 1)
      }
    }
    throw new Error(`块未闭合：${from}`)
  }

  const CODE = stripComments(src)

  it('表头吸顶（表体自滚的 .tk-list 里，滚动时「这列是什么」始终在屏上）', () => {
    const head = block(CODE, '.tk-head th {')
    expect(head).toMatch(/position:\s*sticky/)
    expect(head).toMatch(/top:\s*0/)
    // 吸顶必须有实心底色，否则行从表头下面穿过去时文字叠在一起。
    expect(head).toMatch(/background:/)
  })

  it('固定列宽 + 单元格不换行截断（「一行就是一行」是这张表的前提）', () => {
    const table = block(CODE, '.tk-table {')
    expect(table).toMatch(/table-layout:\s*fixed/)
    // separate + 0：sticky 表头的底边不被边框折叠吃掉。
    expect(table).toMatch(/border-collapse:\s*separate/)
    const line = block(CODE, '.tk-item__line {')
    expect(line).toMatch(/white-space:\s*nowrap/)
    expect(line).toMatch(/text-overflow:\s*ellipsis/)
    // 目标列的全值挂在 title 上（完整引用是排障钥匙）—— 页面侧的钉子见上文用例。
  })

  it('开关/状态列的内边距让位写在 .tk-item__line 层级（否则被单元格公共内边距盖回）', () => {
    const line = block(CODE, '.tk-item__line {')
    // 这两条是「同优先级才生效」的典型：漏写层级时 24px 的展开开关放不进窄列，
    // 格子右缘会多出一个省略号（844 实测），状态列则把「失败」截成「失…」。
    expect(block(line, '.tk-col--toggle {')).toMatch(/padding-left:\s*0/)
    expect(block(line, '.tk-item__status {')).toMatch(/padding-right:\s*0/)
  })

  it('窄视口整列让位：发起人 < 1024、主机 < 768（阈值走断点单源）', () => {
    expect(CODE).toMatch(
      /respond-below\('desktop'\)\s*\{\s*\.tk-col--user\s*\{\s*display:\s*none/
    )
    expect(CODE).toMatch(/respond-below\('tablet'\)\s*\{\s*\.tk-col--host\s*\{\s*display:\s*none/)
  })
})

// ── 展开区的参数块（TaskOptions：受理时下发的动作参数原文）──────────────────
// 语义（后端已核实）：options 是**受理时**下发的整份 DockerCmdOptions 的 JSON 文本；
//   - **字段缺席 = 没有看这笔任务参数的资格**（不是空串、不是掩码）—— 缺席即不渲染；
//   - 超 16 KiB 截断，尾部带 `...(truncated)`（截断后不是合法 JSON，解析必须容错）。
// exec 的 command 是 argv（缺省 ["/bin/sh"]），要成一行可读命令行。
describe('展开区的参数块（TaskOptions）', () => {
  /** 展开第 n 条行（点展开开关）。 */
  async function expandRow(w: VueWrapper, n = 0) {
    const toggle = w.findAll('.tk-item__toggle')[n]
    expect(toggle, `第 ${n + 1} 行应有展开开关`).toBeTruthy()
    await toggle!.trigger('click')
    await flush()
  }

  it('exec：command 数组渲染成一行可读命令行（非 JSON 原文）', async () => {
    api.fetchDockerTasks.mockResolvedValue(
      taskResp([
        taskItem({
          action: 'container:exec',
          target: 'web',
          status: 'succeeded',
          summary: '执行成功',
          options: '{"command":["ls","-la"]}'
        })
      ])
    )
    const w = await mountTasks()
    await expandRow(w)

    expect(w.find('.to').exists(), '有 options 应有参数块').toBe(true)
    expect(w.find('.to__cmd').text()).toBe('ls -la')
    // 结论句仍在（参数块是追加的一块，不改动既有内容）。
    expect(w.find('.tk-item__summary').text()).toBe('执行成功')
  })

  it('exec：含空格/引号的参数做展示用转义（单引号包裹），不拼可执行串', async () => {
    api.fetchDockerTasks.mockResolvedValue(
      taskResp([
        taskItem({
          action: 'container:exec',
          status: 'succeeded',
          summary: '执行成功',
          options: '{"command":["sh","-c","echo hello world"]}'
        })
      ])
    )
    const w = await mountTasks()
    await expandRow(w)
    expect(w.find('.to__cmd').text()).toBe("sh -c 'echo hello world'")
  })

  it('exec：command 缺席时照实给出协议缺省 argv（实际会跑的东西）', async () => {
    api.fetchDockerTasks.mockResolvedValue(
      taskResp([
        taskItem({
          action: 'container:exec',
          status: 'succeeded',
          summary: '执行成功',
          options: '{"target":"web"}'
        })
      ])
    )
    const w = await mountTasks()
    await expandRow(w)
    expect(w.find('.to__cmd').text()).toBe('/bin/sh')
  })

  it('其它动作：参数渲染成键值清单（中文标签优先，长值走等宽块）', async () => {
    api.fetchDockerTasks.mockResolvedValue(
      taskResp([
        taskItem({
          ref: 'r1',
          action: 'compose.file:write',
          target: 'app',
          status: 'succeeded',
          summary: '已写入',
          options: JSON.stringify({ target: 'app', content: 'services:\n  web:\n    image: nginx' })
        })
      ])
    )
    const w = await mountTasks()
    await expandRow(w)

    expect(w.find('.to__cmd').exists(), '非 exec 不出命令行形态').toBe(false)
    const keys = w.findAll('.to__key').map((k) => k.text())
    expect(keys).toEqual(['目标', '文件内容'])
    // YAML 正文（多行）走等宽块。
    const block = w.find('.to__val.is-block')
    expect(block.exists()).toBe(true)
    expect(block.text()).toContain('image: nginx')
  })

  it('查不到标签的键原样键名（等宽）+ 等宽值', async () => {
    api.fetchDockerTasks.mockResolvedValue(
      taskResp([
        taskItem({
          action: 'container:create',
          status: 'succeeded',
          summary: '已创建',
          options: JSON.stringify({ image: 'nginx:1.27', unknown_key: 'v1', start: false })
        })
      ])
    )
    const w = await mountTasks()
    await expandRow(w)
    const keys = w.findAll('.to__key')
    expect(keys.map((k) => k.text())).toEqual(['镜像', 'unknown_key', '创建后启动'])
    expect(keys[1]!.classes()).toContain('is-raw')
    // 布尔照实说人话（false → 否）。
    expect(w.findAll('.to__val')[2]!.text()).toBe('否')
  })

  it('截断：尾标行如实标注「已截断」并走原文兜底（半截 JSON 不当完整）', async () => {
    const full = JSON.stringify({ content: 'x'.repeat(200), base_hash: 'a'.repeat(64) })
    const cut = `${full.slice(0, 40)}...(truncated)`
    api.fetchDockerTasks.mockResolvedValue(
      taskResp([
        taskItem({
          action: 'compose.file:write',
          status: 'succeeded',
          summary: '已写入',
          options: cut
        })
      ])
    )
    const w = await mountTasks()
    await expandRow(w)

    expect(w.find('.to__trunc').text()).toBe('已截断')
    expect(w.find('.to__raw').exists(), '截断的半截 JSON 走原文兜底').toBe(true)
    expect(w.find('.to__kv').exists(), '解析不出就不硬解成键值').toBe(false)
  })

  it('无 options 的行：展开区只有原有内容，不多出任何空块/占位', async () => {
    api.fetchDockerTasks.mockResolvedValue(
      taskResp([
        taskItem({ action: 'image:pull', status: 'succeeded', summary: '拉取完成：alpine:3.20' })
      ])
    )
    const w = await mountTasks()
    await expandRow(w)

    expect(w.find('.tk-item__detail').exists()).toBe(true)
    expect(w.find('.tk-item__summary').text()).toBe('拉取完成：alpine:3.20')
    // 缺席 = 没有资格（不是空串）：参数块整块不出现，也不写「无权限」这类解释句。
    expect(w.find('.to').exists()).toBe(false)
    expect(w.text()).not.toContain('无权限')
  })

  it('有 options 但 summary 为空的终态行可展开（缺判据时它点不动）', async () => {
    api.fetchDockerTasks.mockResolvedValue(
      taskResp([
        taskItem({
          action: 'container:exec',
          status: 'succeeded',
          summary: '',
          options: '{"command":["ls"]}'
        })
      ])
    )
    const w = await mountTasks()
    expect(w.find('.tk-item__toggle').exists(), '有 options 的行必须可展开').toBe(true)
    await expandRow(w)
    expect(w.find('.to__cmd').text()).toBe('ls')
  })
})
