/**
 * 主机上下文（全模块共享）：当前选中的主机、主机清单、以及「主机切换后的重置纪律」。
 *
 * 为什么用 provide/inject 而不是每页各自取：主机是**页面级上下文**（同一个页面里
 * 列表、筛选、详情都属于同一台主机），而路由 query 是它的唯一事实源 —— 刷新与
 * 分享链接都能还原现场。
 *
 * ⚠ **同组件 provide 之后再 inject 是拿不到的**（Vue 的 `inject()` 读
 * `instance.parent.provides`，而 `provide()` 把值写进 `instance.provides`；见
 * runtime-core 的 apiInject）。而本模块的常态恰恰是「页面 = 提供者，页面自己也要用」
 * —— 页面 provide 之后调 `useDockerHostState()` 就在页面组件内部再取一次上下文，
 * 于是 setup 直接抛错、**整页白屏**（2026-09-29 生产实测：五个列表页全白，
 * 侧边栏与面包屑还在，因为那是布局渲染的）。
 *
 * 处置：`ownProvides` 记住「本组件刚 provide 的上下文」，`useDockerHost()` 在 inject
 * 落空时先自查这张表，再抛错。为什么不改成「页面把 ctx 显式传给每个 composable」：
 * 那要求每个新页面都记得传，而漏传的代价是全页白屏；把兜底收在这一处，页面的用法
 * 保持自然（`provideDockerHost()` 拿 ctx 直接用 + `useDockerHost…()` 照常调用）。
 *
 * WeakMap 以**组件实例**为键（worktab 会同时保留多个页面实例，模块级单例会让两台
 * 主机串台）；键是实例对象，页面卸载后自动回收。
 */
import { computed, getCurrentInstance, inject, provide, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { fetchDockerHosts, type DockerHostItem } from '../api'

const HOST_KEY = Symbol('docker-host')

/** 组件实例 → 它自己 provide 的主机上下文（同组件自查用；见文件头）。 */
const ownProvides = new WeakMap<object, DockerHostContext>()

export interface DockerHostContext {
  hosts: DockerHostItem[]
  hostId: string
  host: DockerHostItem | undefined
  loading: boolean
  /** 拉一次主机清单：路由 query 里有主机就用它，否则落到第一台并写回 query。 */
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
      // 首次进入（query 里没有 host）时落到第一台，并写回 query —— 之后所有请求
      // 都从 query 取主机，刷新也能还原。
      if (!route.query.host && hosts.value[0]) {
        void router.replace({ query: { ...route.query, host: hosts.value[0].id } })
      }
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
  // 同组件自查表：页面既 provide 又立刻要用（见文件头）。
  const self = getCurrentInstance()
  if (self) ownProvides.set(self, ctx)
  return ctx
}

/**
 * 取主机上下文。
 *
 * 解析顺序：**本组件自己 provide 的**（自查表）→ 祖先 provide 的（inject）。
 *
 * ⚠ 必须在 **setup 期**调用（或把结果在 setup 期存下来后再用）：`inject()` 只在
 * 有当前实例时有效 —— 在事件回调里调用会静默失败并抛「必须在 provideDockerHost
 * 之后调用」。指令类 composable（useDockerCmds）因此在构造时就把上下文定住。
 */
export function useDockerHost(): DockerHostContext {
  const self = getCurrentInstance()
  if (self) {
    const own = ownProvides.get(self)
    if (own) return own
  }
  const ctx = inject<DockerHostContext | null>(HOST_KEY, null)
  if (!ctx) {
    throw new Error('useDockerHost 必须在 setup 期、且在该组件或祖先 provideDockerHost 之后调用')
  }
  return ctx
}
