/**
 * 跨主机资源清单（镜像/卷/网络/项目）的数据生命周期 —— 与 useWorkloadList
 * （容器统一表）同一范式：拉取 + seq 守卫 + 四态 + 截断结论 + 静默重拉。
 *
 * 为什么另收一份而不是复用 useWorkloadList：那一个是容器统一表的专用件
 * （端点、参数形态、条目类型全定死）。四个资源端点（GET /docker/images|volumes|
 * networks|projects，9a）的过滤参数各异（dangling/unused/internal/state），
 * 这里把「拉、守、报」的公共纪律泛化 —— 端点调用与参数映射由使用方给
 * （fetcher/params），四个清单共用一份纪律，而不是各写一份近似复制。
 *
 * 与 useWorkloadList 的两处分野：
 *   - **没有自动刷新**：资源清单是轻量的表页（镜像/卷/网络/项目），进页拉一次、
 *     表头刷新随时手动 —— 自动刷新留给容器统一表与总览这些「盯表」场景，
 *     不在四个 tab 里各挂一枚开关；
 *   - 主机清单**不在**这里拉（见本文件下方的 useHostList）：resources 页三个 tab
 *     共用**页面级一份**（切 tab 不再各拉一次，7a 收敛收益的延续），projects 页
 *     单表面自拉一份 —— 共享范围由使用方决定，本件只经 getters 读它做
 *     「没有可管主机」的空态判定。
 *
 * 失败口径沿用 D-1 的精神（与统一工作负载表同一句）：首拉失败 → 整页/整 tab 的
 * 错误态 + 重试（没有旧数据可保）；其后静默刷新失败 → 保留最后已知数据并在
 * 表头标注（「数据获取失败」与「上次数据仍在」是两种事实，不共用一句话）。
 */
import { computed, onMounted, ref, type Ref } from 'vue'
import { fetchDockerHosts, type DockerHostItem } from '../api'

/**
 * 资源清单的公共查询参数（四个端点各取所需；键名与后端 form 名一致）。
 *
 * dangling/unused/internal 是「开关筛选项」：使用方把 UI 的开关映射成
 * true 或 undefined（false 侧保留给端点完整语义，UI 的开关只发 true ——
 * 与 DockerImageQuery 的指针布尔设计同一句）。
 */
export interface ResourceQueryParams {
  /** 限定单主机（留空 = 全部主机）。 */
  hostId?: string
  /** 名称类子串匹配（具体字段随端点：repoTag / 卷名 / 网络名 / 项目名）。 */
  keyword?: string
  /** 仅悬空镜像（镜像端点）。 */
  dangling?: boolean
  /** 仅未被使用的镜像 / 卷。 */
  unused?: boolean
  /** 仅内部网络（网络端点）。 */
  internal?: boolean
  /** 项目态：running / stopped（项目端点）。 */
  state?: string
}

/** 端点响应里本件用到的两个字段（四个 ListResp 同形）。 */
export interface ResourceListPage<T> {
  items: T[]
  total: number
}

export interface UseResourceListOptions<T> {
  /** 端点调用：把参数源映射到具体的 fetchDockerXxx（加弹簧在数据层不留 key 映射）。 */
  fetcher: (params: ResourceQueryParams) => Promise<ResourceListPage<T>>
  /** 筛选参数来源：每次拉取时**现取**（筛选项是页面状态，数据层只读快照）。 */
  params: () => ResourceQueryParams
  /** 主机清单（读快照；判定「没有可管主机」空态）。 */
  hosts: () => DockerHostItem[]
  /** 主机清单在拉：首拉期间不能先喊「没有」（那时还不知道有没有主机）。 */
  hostsLoading: () => boolean
}

export function useResourceList<T>(options: UseResourceListOptions<T>) {
  const rows = ref([]) as Ref<T[]>
  const total = ref(0)
  const loading = ref(false)
  /** 首拉失败（还没有任何数据可给）：整块错误态 + 重试。 */
  const hasError = ref(false)
  /** 是否曾成功拉到过数据：其后静默刷新失败时保留最后已知数据并标注。 */
  const hasData = ref(false)
  const refreshError = ref(false)
  /** 请求序号：搜索/重置/静默重拉并发时，旧响应晚到不得覆盖新数据。 */
  let loadSeq = 0

  /** 服务端截断（items 上限 500）：截断必须说出口，否则用户以为「筛完就这么点」。 */
  const truncated = computed(() => total.value > rows.value.length)

  const pageState = computed<'loading' | 'error' | 'empty' | 'ready'>(() => {
    if (hasError.value) return 'error'
    if ((loading.value || options.hostsLoading()) && !hasData.value) return 'loading'
    // 没有可管主机：主机清单与条目计数一起指向这个结论；「有主机但筛完没有」
    // 是另一回事，由使用方按 hasFilter 分开说（「没有」与「筛没了」不是一句话）。
    if (!options.hostsLoading() && options.hosts().length === 0 && rows.value.length === 0) {
      return 'empty'
    }
    return 'ready'
  })

  /**
   * 拉一次清单。
   *
   * @param silent 写操作成功后的重拉为 true：不显示加载态（每次刷新都把表格切
   *   成骨架，写操作后的静默刷新反而不可用）。
   */
  async function load(silent = false) {
    const seq = ++loadSeq
    if (!silent) loading.value = true
    try {
      const resp = await options.fetcher(options.params())
      if (seq !== loadSeq) return
      rows.value = resp.items ?? []
      total.value = resp.total ?? 0
      hasData.value = true
      hasError.value = false
      refreshError.value = false
    } catch {
      if (seq !== loadSeq) return
      // 握有数据时失败不清列表（最后已知数据仍可见），只在表头标注；
      // 从未成功过才走错误态（与统一工作负载表同一句口径）。
      if (hasData.value) refreshError.value = true
      else hasError.value = true
    } finally {
      if (seq === loadSeq) loading.value = false
    }
  }

  function refresh() {
    return load()
  }

  /** 静默重拉（写操作成功后的重拉走它）。 */
  function refreshSilent() {
    return load(true)
  }

  /** 错误态的「重试」：本清单自己没有第二个数据源，重拉一次即全部。 */
  function reloadAll() {
    void load()
  }

  return {
    rows,
    total,
    loading,
    hasError,
    hasData,
    refreshError,
    truncated,
    pageState,
    load,
    refresh,
    refreshSilent,
    reloadAll
  }
}

/**
 * 可管主机清单（跨主机页的筛选下拉与「尚无可管主机」判定）。
 *
 * 调用范围由页面决定共享粒度：resources 页在**页面级**调用一次（三个 tab 经
 * props 共用这份清单 —— 切 tab 不再各拉一次）；projects 页单表面自拉一份。
 *
 * 静默失败：清单拉不到时退化为空，页面显示「尚无可管主机」，不弹错
 * （与 host-context 的 reload 同一取向 —— 删掉主机清单广告 toast 只会噪音）。
 */
export function useHostList() {
  const hosts = ref<DockerHostItem[]>([])
  const hostsLoading = ref(false)
  /** 主机清单也有 seq 守卫：手动重拉与激活补拉并发时旧响应不得覆盖新清单。 */
  let hostSeq = 0

  async function loadHosts() {
    const seq = ++hostSeq
    hostsLoading.value = true
    try {
      const res = await fetchDockerHosts()
      if (seq !== hostSeq) return
      hosts.value = res.list ?? []
    } catch {
      if (seq !== hostSeq) return
      hosts.value = []
    } finally {
      if (seq === hostSeq) hostsLoading.value = false
    }
  }

  // 进页面即拉：主机清单是筛选下拉与空态判定的共同前置。
  onMounted(() => {
    void loadHosts()
  })

  return { hosts, hostsLoading, loadHosts }
}
