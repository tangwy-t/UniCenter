/**
 * 页面级的主机生命周期 + 快照拉取（列表页 5 份、主机切换模板 4 份重复逻辑的收口）。
 *
 * 收口的东西（此前在每个 view 里各写一遍，行号见 2B 计划 §D3）：
 *   1. 首次进入：先拉主机清单（query 里的主机写回/落到第一台），再拉快照；
 *   2. 主机切换：重置页面级状态（清空勾选、重置分页 —— **筛选保留**，spec §11.0）
 *      后重新拉快照；
 *   3. `loadState` 的 **seq 守卫**（照 system-monitor/views/server.vue 的既有模式）：
 *      主机切换会并发两份请求，旧主机那份可能晚于新主机返回 —— 直接落盘会把列表
 *      换回上一台机器；故递增序号，被更新的请求取代就丢弃（loading 也由最新那次收尾）。
 *
 * 视图用法：
 *   const ctx = provideDockerHost()          // 页面是上下文的提供者
 *   const { state, … } = useDockerHostState({ host: ctx })
 *
 * ⚠ **页面组件里必须把 `provideDockerHost()` 的返回值传进来（`host`）**，不能指望
 * 在本组件内部再 `inject` 一次：Vue 的 `inject()` 读的是 `instance.parent.provides`，
 * **同组件 `provide()` 之后 `inject()` 拿不到自己刚 provide 的值**（见 runtime-core
 * 的 apiInject:provide 写 currentInstance.provides / inject 读 parent.provides）。
 * 漏传的后果不是「主机选错」，而是 setup 直接抛错 → **整页渲染失败、内容区白屏**
 * —— 2026-09-29 生产实测（五个列表页全白，侧边栏还在）。
 *
 * 详情页用法（不拉整份快照，只借主机切换的时机做自己的重读）：
 *   useDockerHostState({ host: ctx, autoLoad: false, immediate: true, onHostReady: reloadDetail })
 */
import { computed, ref, watch } from 'vue'
import { fetchDockerState, type DockerStateResp } from '../api'
import { useDockerHost, type DockerHostContext } from '../utils/host-context'

export interface UseDockerHostStateOptions {
  /**
   * 页面自己的主机上下文（`provideDockerHost()` 的返回值）。
   *
   * **页面组件内调用时必须给**（理由见文件头）。子组件里调用可省略，此时退回
   * `inject` —— 子组件注入祖先的 provide 是合法的。
   */
  host?: DockerHostContext
  /**
   * 主机切换时的页面级重置：清空勾选、重置分页（**筛选保留** —— spec §11.0）。
   * 只在真正切换时调用；首次 hostId 从 '' 落定到首台不触发（列表此时本来就是空的）。
   * 快照重拉之前调用，页面可以先收起上一台主机的临时状态（如项目页的配置对话框）。
   */
  onHostSwitch?: () => void
  /**
   * hostId 落定/切换后要做的额外读取（详情页重读 inspect/日志）。
   * `autoLoad: false` 时它是唯一的数据动作。
   */
  onHostReady?: () => void | Promise<void>
  /** 是否自动拉取快照（默认 true）；详情页用不到整份快照时传 false。 */
  autoLoad?: boolean
  /** onHostReady 是否在 setup 时就立即执行一次（详情页的 immediate watch 语义）。 */
  immediate?: boolean
}

export function useDockerHostState(options: UseDockerHostStateOptions = {}) {
  // 先取显式传入的 host，再退回 inject：**都要在 setup 期完成**（此刻才有实例可注入）。
  const ctx = options.host ?? useDockerHost()
  const state = ref<DockerStateResp | null>(null)
  const loading = ref(false)
  /** 请求序号：只有最新一次请求的响应能落盘（见文件头说明）。 */
  let loadSeq = 0

  async function loadState() {
    if (!ctx.hostId) return
    const seq = ++loadSeq
    loading.value = true
    try {
      const res = await fetchDockerState(ctx.hostId)
      if (seq !== loadSeq) return
      state.value = res
    } catch {
      // 静默失败：陈旧/离线由页面头部标注，不弹错（与设备页同一取向）。
      if (seq !== loadSeq) return
      state.value = null
    } finally {
      if (seq === loadSeq) loading.value = false
    }
  }

  // 服务端算好的陈旧结论（现取现用，不再每页写一遍 `state?.stale ?? false`）。
  const stale = computed(() => state.value?.stale ?? false)
  const ageSeconds = computed(() => state.value?.ageSeconds ?? 0)
  const neverReported = computed(() => state.value?.neverReported ?? false)

  /** 表格的加载态：快照在拉，或主机清单还没到（后者尚不知有没有主机，不能先喊「没有」）。 */
  const listLoading = computed(() => loading.value || ctx.loading)

  watch(
    () => ctx.hostId,
    () => {
      if (!ctx.hostId) return
      options.onHostSwitch?.()
      void options.onHostReady?.()
      if (options.autoLoad !== false) void loadState()
    },
    { immediate: options.immediate === true }
  )

  // 首次进入：先拉主机清单（query 里的主机写回/落到第一台），再拉快照。
  // 若 hostId 要等清单到达才从 '' 变成首台 id，上面的 watch 会补一次（seq 守卫兜重复）。
  void ctx.reload().then(() => {
    if (options.autoLoad !== false) return loadState()
  })

  return {
    /** 主机上下文原样返回，页面模板仍用 ctx.host/ctx.hosts（避免二次 inject）。 */
    ctx,
    state,
    loading,
    listLoading,
    stale,
    ageSeconds,
    neverReported,
    /** 拉一次快照（页面刷新按钮/操作成功后的重拉都走它）。 */
    loadState,
    /** loadState 的别名：语义是「刷新」，与计划里的 refresh() 对齐。 */
    refresh: loadState
  }
}
