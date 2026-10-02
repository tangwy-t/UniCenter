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
  // 拉取进度子组件的 import 面（本文件不展开拉取行，不需真流）。
  openDockerPullStream: vi.fn()
}))
vi.mock('../api', () => ({ ...api, default: undefined }))

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
    api.fetchDockerTasks.mockResolvedValue({ items: [taskItem()] })
    const w = await mountTasks()
    expect(w.find('.docker-tasks').exists()).toBe(true)
    expect(w.text()).toContain('任务中心')
  })

  it('时间列：毫秒除千的浮点秒整段取整，「4.41100001335144 秒前」不再出现', async () => {
    // QA 实测形态：now - createdAt = 4411ms 多一点（浮点除千后是 4.41100001335144）。
    api.fetchDockerTasks.mockResolvedValue({
      items: [taskItem({ createdAt: BASE_SEC * 1000 - 4411 })]
    })
    const w = await mountTasks()
    const time = w.find('.tk-item__time')
    expect(time.text()).toBe('4 秒前')
    expect(time.text()).not.toContain('.')
  })

  it('空态话术三档各说各的：全部 / 进行中 / 已结束', async () => {
    api.fetchDockerTasks.mockResolvedValue({ items: [] })
    const w = await mountTasks()
    expect(w.find('.el-empty__description').text()).toBe('还没有受理过任何任务')

    await clickFilter(w, '进行中')
    expect(w.find('.el-empty__description').text()).toBe('没有进行中的任务')
    // 服务端过滤的 query 如实透传（前端不做本地过滤的既有纪律）。
    expect(api.fetchDockerTasks).toHaveBeenLastCalledWith({ status: 'pending' })

    await clickFilter(w, '已结束')
    expect(w.find('.el-empty__description').text()).toBe('没有已结束的任务')
    expect(api.fetchDockerTasks).toHaveBeenLastCalledWith({ status: 'done' })
  })

  it('目标长 ID 短显（12 位，含 sha256: 前缀形态）+ title 全值；名称与引用原样', async () => {
    const hex64 = 'a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2'
    api.fetchDockerTasks.mockResolvedValue({
      items: [
        taskItem({ ref: 'r1', action: 'container:restart', target: hex64 }),
        taskItem({ ref: 'r2', action: 'image:remove', target: `sha256:${hex64}` }),
        taskItem({ ref: 'r3', action: 'image:pull', target: 'alpine:3.20' })
      ]
    })
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
