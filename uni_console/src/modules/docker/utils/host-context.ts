/**
 * 主机上下文（全模块共享）：当前选中的主机、主机清单、以及「主机切换后的重置纪律」。
 *
 * 为什么用 provide/inject 而不是每页各自取：主机是**页面级上下文**（同一个页面里
 * 列表、筛选、详情都属于同一台主机），而路由 query 是它的唯一事实源 —— 刷新与
 * 分享链接都能还原现场。
 */
import { computed, inject, provide, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { fetchDockerHosts, type DockerHostItem } from '../api'

const HOST_KEY = Symbol('docker-host')

export interface DockerHostContext {
  hosts: DockerHostItem[]
  hostId: string
  host: DockerHostItem | undefined
  loading: boolean
  /** 拉一次主机清单（**只读**：不写路由，见 reload 的注释）。 */
  reload: () => Promise<void>
  /** 切换主机：写 route query（替换历史，避免后退键在主机间跳来跳去）。 */
  selectHost: (id: string) => void
}

export function provideDockerHost(): DockerHostContext {
  const route = useRoute()
  const router = useRouter()
  const hosts = ref<DockerHostItem[]>([])
  const loading = ref(false)

  const hostId = computed(() => String(route.query.host ?? hosts.value[0]?.id ?? ''))
  const host = computed(() => hosts.value.find((h) => h.id === hostId.value))

  async function reload() {
    loading.value = true
    try {
      const res = await fetchDockerHosts()
      hosts.value = res.list ?? []
      // ⚠ 这里**绝不能**顺手把 host 写回 query（曾经这么写过，是个真 bug）：
      // 每个页面挂载时都会调本函数，而「从侧边栏进入」时 query 里本来就没有 host ——
      // 于是页面刚挂载就立刻发起一次「同路径、仅改 query」的第二次导航。它与控制台的
      // 工作标签 + 页面出口区的 out-in 过渡相撞：从第 3 次切页起，出口区再也不挂载
      // 组件（表现为「切到别的页面一片空白，必须刷新」，而 route 已变、`?host=` 始终
      // 补不上、控制台无报错）。其它模块的页面挂载时只拉数据、不碰路由，所以只有本模块
      // 出这个现象。
      //
      // 现在的纪律：**路由 query 只在用户显式切换主机时写**（selectHost），
      // 读取时装不上就退到第一台（hostId 的兜底），页面功能不受影响。
    } catch {
      // 静默失败：清单拉不到时页面显示「没有可管理的主机」，不弹错（与轮询同一取向）。
      hosts.value = []
    } finally {
      loading.value = false
    }
  }

  function selectHost(id: string) {
    // 切换主机 = 换一台机器看：列表要重新拉，筛选与分页由调用方按需重置
    //（筛选保留、分页重置 —— 见各列表页的 watch）。
    // 这是**用户动作**（点击切换器），发起导航是安全的；挂载时不行（见 reload 的注释）。
    if (!id || id === hostId.value) return
    void router.replace({ query: { ...route.query, host: id } })
  }

  // hostId/host 是 computed：用 getter 暴露，避免调用方拿到切换前的旧值。
  const ctx: DockerHostContext = {
    get hosts() {
      return hosts.value
    },
    get hostId() {
      return hostId.value
    },
    get host() {
      return host.value
    },
    get loading() {
      return loading.value
    },
    reload,
    selectHost
  }
  provide(HOST_KEY, ctx)
  return ctx
}

/** 取主机上下文（必须在 provideDockerHost 之后调用）。 */
export function useDockerHost(): DockerHostContext {
  const ctx = inject<DockerHostContext | null>(HOST_KEY, null)
  if (!ctx) throw new Error('useDockerHost 必须在 provideDockerHost 之后调用')
  return ctx
}
