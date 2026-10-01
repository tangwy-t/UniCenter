/**
 * 跨主机统一工作负载表的数据生命周期（从 containers.vue 收口，地位对齐单主机
 * 页的 useDockerHostState）：拉取 + seq 守卫 + 四态 + 截断结论 + 10s 自动刷新
 * （含 keep-alive 失活停表 —— 照抄 overview.vue 刚落地的实现）。
 *
 * 与 useDockerHostState 的分野（为什么另立而不是加参数）：
 *   - 数据源是 GET /docker/containers（服务端过滤，参数从筛选表单来）而不是
 *     单主机快照 —— 没有「当前主机」与「主机切换重置」的概念，host 从页面级
 *     上下文降为筛选参数之一；
 *   - 失败口径沿用 D-1 的精神但形状不同：首拉失败 → 整页错误态 + 重试（没有
 *     旧数据可保）；其后静默刷新失败 → 保留最后已知数据并在页头标注
 *     （「数据获取失败」与「上次数据仍在」是两种事实，不共用一句话）；
 *   - total 如实报全量、items 截 500：truncated 结论交给页面说出口。
 *
 * 自动刷新的清理覆盖两种「离开」：整页卸载与 keep-alive 失活（worktab 缓存页面
 * 实例）。onActivated 回来时重启计时器并补一次静默拉取 —— 后台标签页的
 * setInterval 会被浏览器限流，切回来时数据已是旧的。
 */
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'
import {
  fetchDockerContainers,
  fetchDockerHosts,
  type DockerHostItem,
  type DockerWorkloadItem
} from '../api'

/** 统一表查询参数的响应式来源（页面持有筛选表单，这里只读快照）。 */
export type WorkloadParamsSource = () => {
  hostId?: string
  state?: string
  keyword?: string
}

export interface UseWorkloadListOptions {
  /** 筛选参数来源（每次拉取时现取，页面改筛选后触发 reload 即生效）。 */
  params: WorkloadParamsSource
  /** 自动刷新间隔（默认 10s；测试可调大避免与用例竞速）。 */
  autoRefreshMs?: number
}

export function useWorkloadList(options: UseWorkloadListOptions) {
  const rows = ref<DockerWorkloadItem[]>([])
  const total = ref(0)
  /** 主机清单（筛选下拉的数据源；拉不到时静默降级为只有「全部主机」）。 */
  const hosts = ref<DockerHostItem[]>([])
  const loading = ref(false)
  const hostsLoading = ref(false)
  /** 首拉失败（还没有任何数据可给）：整页错误态 + 重试。 */
  const hasError = ref(false)
  /** 是否曾成功拉到过数据：其后静默刷新失败时保留最后已知数据并标注。 */
  const hasData = ref(false)
  const refreshError = ref(false)
  /** 请求序号：手动刷新、自动刷新、筛选变化并发时，旧响应晚到不得覆盖新数据。 */
  let loadSeq = 0

  /** 服务端截断（items 上限 500）：截断必须说出口，否则用户以为筛选完了就这么多。 */
  const truncated = computed(() => total.value > rows.value.length)

  const pageState = computed<'loading' | 'error' | 'empty' | 'ready'>(() => {
    if (hasError.value) return 'error'
    // 首次加载：清单还没到时不能先喊「没有」（那时还不知道有没有主机）。
    if ((loading.value || hostsLoading.value) && !hasData.value) return 'loading'
    // 两个数据源都说没有：主机清单与容器计数一致指向「没有可管主机」。
    if (!hasData.value && hosts.value.length === 0 && total.value === 0) return 'empty'
    return 'ready'
  })

  /** 页头副标题：三种事实（在拉 / 刷新失败 / 正常），与总览页 subtitle 同一取向。 */
  const subtitle = computed(() => {
    if (pageState.value === 'loading') return '正在拉取容器清单'
    if (refreshError.value) return '上次数据仍在，本次刷新失败'
    return `跨 ${hosts.value.length} 台主机`
  })

  /**
   * 拉取统一表。
   *
   * @param silent 自动刷新与操作后的重拉为 true：不显示加载态 —— 每次刷新都把
   *   表格切成骨架，自动刷新反而不可用（设备总览同一取舍）。
   */
  async function load(silent = false) {
    const seq = ++loadSeq
    if (!silent) loading.value = true
    try {
      const p = options.params()
      const resp = await fetchDockerContainers({
        hostId: p.hostId || undefined,
        state: p.state as 'running' | 'stopped' | undefined,
        keyword: p.keyword || undefined
      })
      if (seq !== loadSeq) return
      rows.value = resp.items ?? []
      total.value = resp.total ?? 0
      hasData.value = true
      hasError.value = false
      refreshError.value = false
    } catch {
      if (seq !== loadSeq) return
      // D-1 的口径沿用到统一表：握有数据时失败不清列表（最后已知数据仍可见），
      // 只在页头标注；从未成功过才走整页错误态。
      if (hasData.value) refreshError.value = true
      else hasError.value = true
    } finally {
      if (seq === loadSeq) loading.value = false
    }
  }

  /** 主机清单（筛选下拉）：静默失败 —— 下拉退化成「全部主机」，不打断列表本身。 */
  async function loadHosts() {
    hostsLoading.value = true
    try {
      const res = await fetchDockerHosts()
      hosts.value = res.list ?? []
    } catch {
      hosts.value = []
    } finally {
      hostsLoading.value = false
    }
  }

  function refresh() {
    return load()
  }

  /** 静默重拉（自动刷新 tick、写操作成功后的重拉都走它）。 */
  function refreshSilent() {
    return load(true)
  }

  /** 错误/空态的「重试 / 刷新」：两个数据源都重拉。 */
  function reloadAll() {
    void load()
    void loadHosts()
  }

  // ── 自动刷新（默认关） ────────────────────────────────────
  const AUTO_REFRESH_MS = options.autoRefreshMs ?? 10_000
  const autoRefresh = ref(false)
  let timer: number | undefined

  function stopTimer() {
    if (timer !== undefined) {
      clearInterval(timer)
      timer = undefined
    }
  }

  function restartTimer() {
    stopTimer()
    if (autoRefresh.value) {
      // 隐藏期间跳过 tick：后台标签页的 setInterval 会被浏览器限流，切回来时
      // 数据已是旧的（onActivated 里补一次拉取）。
      timer = window.setInterval(() => {
        if (!document.hidden) void load(true)
      }, AUTO_REFRESH_MS)
    }
  }

  watch(autoRefresh, restartTimer)

  onMounted(() => {
    void loadHosts()
  })

  onBeforeUnmount(stopTimer)
  onDeactivated(stopTimer)

  onActivated(() => {
    restartTimer()
    if (autoRefresh.value) void load(true)
  })

  return {
    rows,
    total,
    hosts,
    loading,
    hostsLoading,
    hasError,
    refreshError,
    truncated,
    pageState,
    subtitle,
    autoRefresh,
    /** 拉一次（搜索/筛选变化、手动刷新走它）。 */
    load,
    loadHosts,
    refresh,
    refreshSilent,
    reloadAll
  }
}
