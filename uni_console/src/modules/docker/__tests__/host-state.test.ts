/**
 * useDockerHostState 的快照拉取失败语义（D-1 的钉子）。
 *
 * 修复前的行为：loadState 的 catch 把 state 清成 null —— 派生值随之回落到
 * stale=false / ageSeconds=0，页头文案变成「刚刚同步」：**网络失败被渲染成
 * 最新鲜状态**，运维唯一的时间判据失真。这里钉住修复后的四件事：
 *   ① 首拉失败：置 loadError、state 为空，结论是「数据获取失败」；
 *   ② 同主机刷新失败：state 保留上一次的快照（最后已知数据）；
 *   ③ 失败后重试成功：loadError 清除；
 *   ④ 切换主机后失败：不把上一台主机的快照留在新主机名下（宁可清空）。
 *
 * 沿用 composables/page-render 的 vi.mock 模式（mock 整个 '../api'）；
 * 传假的主机上下文绕开 provide/inject，测试因此不需要组件与 DOM。
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'

const api = vi.hoisted(() => ({
  fetchDockerHosts: vi.fn(),
  fetchDockerState: vi.fn(),
  sendDockerCmd: vi.fn(),
  fetchDockerCmdResult: vi.fn()
}))
vi.mock('../api', () => ({ ...api }))

import { useDockerHostState } from '../composables/useDockerHostState'
import { syncTextWithError } from '../utils/host'
import type { DockerStateResp } from '../api'
import type { DockerHostContext } from '../utils/host-context'

const flush = () => new Promise<void>((r) => setTimeout(r, 0))

function makeState(overrides: Partial<DockerStateResp> = {}): DockerStateResp {
  return {
    lastSync: 1790600000,
    stale: false,
    ageSeconds: 12,
    neverReported: false,
    dockerOk: true,
    containers: [],
    images: [],
    volumes: [],
    networks: [],
    projects: [],
    ...overrides
  }
}

/**
 * 假主机上下文：hostId 用真 ref（composable 的 watch 靠它感知主机切换 ——
 * 平面对象的 getter 不具响应性，切了也不会触发重拉）。
 */
function makeCtx() {
  const hostId = ref('h1')
  const ctx: DockerHostContext = {
    hosts: [],
    host: undefined,
    get hostId() {
      return hostId.value
    },
    loading: false,
    reload: vi.fn(() => Promise.resolve()),
    selectHost: vi.fn()
  }
  return { ctx, switchHost: (id: string) => (hostId.value = id) }
}

/** 页头会展示的结论句：把 composable 的五个口径喂给文案函数（与各页 hero 同一喂法）。 */
function conclusion(s: ReturnType<typeof useDockerHostState>): string {
  return syncTextWithError(
    s.loadError.value,
    s.hasState.value,
    s.stale.value,
    s.ageSeconds.value,
    s.neverReported.value
  )
}

beforeEach(() => {
  vi.resetAllMocks()
  api.fetchDockerState.mockResolvedValue(makeState())
})

describe('useDockerHostState · 拉取失败不再伪装成「刚刚同步」（D-1）', () => {
  it('① 首拉失败：loadError 置位、state 为空，结论是失败句而不是「刚刚同步」', async () => {
    api.fetchDockerState.mockRejectedValue(new Error('network down'))
    const { ctx } = makeCtx()
    const s = useDockerHostState({ host: ctx })
    await flush()
    await flush()

    expect(s.loadError.value).toBe(true)
    expect(s.state.value).toBeNull()
    expect(s.hasState.value).toBe(false)
    expect(conclusion(s)).toBe('数据获取失败')
  })

  it('② 同主机刷新失败：state 保留上一次的快照，结论标注本次刷新失败', async () => {
    // Vue 的 ref 会深代理对象（state.value 读出来是 Proxy，引用比较必失败），断言用内容；
    // ageSeconds 取 30（与 beforeEach 默认的 12 不同）以区分「保留」与「被默认值顶替」。
    const first = makeState({ ageSeconds: 30 })
    api.fetchDockerState.mockResolvedValue(first)
    const { ctx } = makeCtx()
    const s = useDockerHostState({ host: ctx })
    await flush()
    await flush()
    expect(s.state.value).toStrictEqual(first)
    expect(s.loadError.value).toBe(false)

    api.fetchDockerState.mockRejectedValue(new Error('network down'))
    await s.refresh()

    expect(s.loadError.value).toBe(true)
    // 不清空：最后已知数据保留（表头说「上次同步…，本次刷新失败」，表格还是这批数据）
    expect(s.state.value).toStrictEqual(first)
    expect(s.ageSeconds.value).toBe(30)
    expect(conclusion(s)).toBe('同步于 30 秒前，本次刷新失败')
  })

  it('③ 失败后重试成功：loadError 清除、state 换成新快照，结论回到三句之一', async () => {
    api.fetchDockerState.mockRejectedValue(new Error('network down'))
    const { ctx } = makeCtx()
    const s = useDockerHostState({ host: ctx })
    await flush()
    await flush()
    expect(s.loadError.value).toBe(true)

    const fresh = makeState({ ageSeconds: 5 })
    api.fetchDockerState.mockResolvedValue(fresh)
    await s.refresh()

    expect(s.loadError.value).toBe(false)
    expect(s.state.value).toStrictEqual(fresh)
    expect(conclusion(s)).toBe('同步于 5 秒前')
  })

  it('④ 切换主机后拉取失败：不把上一台的快照留在新主机名下（宁可清空走失败句）', async () => {
    const h1State = makeState({ ageSeconds: 3 })
    api.fetchDockerState.mockResolvedValue(h1State)
    const { ctx, switchHost } = makeCtx()
    const s = useDockerHostState({ host: ctx })
    await flush()
    await flush()
    expect(s.state.value).toStrictEqual(h1State)

    api.fetchDockerState.mockRejectedValue(new Error('network down'))
    switchHost('h2') // watch 感知切换 → 自动重拉 h2
    await flush()
    await flush()

    expect(s.loadError.value).toBe(true)
    expect(s.state.value).toBeNull()
    expect(conclusion(s)).toBe('数据获取失败')
  })

  it('无失败的首拉：现状语义不变（state 落盘、结论按快照的三句走）', async () => {
    const { ctx } = makeCtx()
    const s = useDockerHostState({ host: ctx })
    await flush()
    await flush()

    expect(s.loadError.value).toBe(false)
    expect(s.state.value).not.toBeNull()
    expect(conclusion(s)).toBe('同步于 12 秒前')
  })
})
