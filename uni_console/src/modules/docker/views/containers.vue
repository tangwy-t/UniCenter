<template>
  <DockerPage
    :loading="loading"
    :stale="state?.stale ?? false"
    :age-seconds="state?.ageSeconds ?? 0"
    :never-reported="state?.neverReported ?? false"
    @refresh="loadState"
  >
    <template #search>
      <ArtSearchBar
        v-show="showSearchBar"
        v-model="searchForm"
        :items="searchItems"
        @search="onSearch"
        @reset="onReset"
      />
    </template>

    <template #table>
      <ArtTableHeader v-model:showSearchBar="showSearchBar" :loading="loading" @refresh="loadState">
        <template #left>
          <span class="docker-count">共 {{ filtered.length }} 个容器</span>
        </template>
      </ArtTableHeader>

      <!-- 两种空态分开：主机上没有容器 vs 筛选没命中（后者给「清除筛选」）。
           这是 overview.vue 的既有纪律：把「没有」与「筛没了」说成一句会让人以为机器空了。
           空态渲染在本页、不写进 ArtTable 的 `#empty` 插槽：ArtTable 不转发该插槽
           （组件内部把 ElTable 的空态写死成「暂无数据」），写进去会被静默丢弃 ——
           与 overview.vue 记的 ElEmpty `#extra` 陷阱同类。
           清单还没到时也不喊「没有容器」（那时还不知道有没有主机），故 v-if 把
           主机清单的加载态一并算进来。 -->
      <ArtTable v-if="showTable" :loading="listLoading" :data="filtered" :columns="columns" />
      <!-- host-context.ts 的 reload 注释承诺：清单拉不到时页面显示「没有可管理的主机」 -->
      <ElEmpty v-else-if="!ctx.hosts.length" class="docker-empty" description="没有可管理的主机" />
      <ElEmpty v-else-if="hasFilter" class="docker-empty" description="没有符合筛选条件的容器">
        <ElButton size="small" @click="onReset">清除筛选</ElButton>
      </ElEmpty>
      <ElEmpty v-else class="docker-empty" description="该主机上还没有容器" />
    </template>
  </DockerPage>
</template>

<script setup lang="ts">
  import { computed, h, ref, watch } from 'vue'
  import { useRouter } from 'vue-router'
  import { ElButton, ElEmpty } from 'element-plus'
  import { useAuth } from '@/hooks/core/useAuth'
  import { PermDockerInspect } from '@/enums/permission'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtButtonMore from '@/components/core/forms/art-button-more/index.vue'
  import ArtSearchBar from '@/components/core/forms/art-search-bar/index.vue'
  import ArtTable from '@/components/core/tables/art-table/index.vue'
  import ArtTableHeader from '@/components/core/tables/art-table-header/index.vue'
  import DockerPage from '../components/docker-page.vue'
  import { fetchDockerState, type DockerContainerItem, type DockerStateResp } from '../api'
  import { filterContainers } from '../utils/snapshot'
  import { containerStateText, cpuText, memText, netText, portsText } from '../utils/display'
  import { provideDockerHost } from '../utils/host-context'

  const router = useRouter()
  // 主机上下文是**页面级** provide/inject：DockerPage 与 HostSwitcher 都用 useDockerHost() 取它，
  // 而模块里没有别的 provide 调用方 —— 页面就是这一层的提供者，故在这里 provide 并直接用其返回值。
  // 不要解构：上下文字段是 getter，解构会把 hostId 定格成进入页面时的 ''（主机清单尚未到达）。
  const ctx = provideDockerHost()
  const { hasAuth } = useAuth()

  const loading = ref(false)
  const showSearchBar = ref(false)
  const state = ref<DockerStateResp | null>(null)
  const searchForm = ref<{ keyword?: string; state?: string; runningOnly?: boolean }>({})
  /** 请求序号：只有最新一次请求的响应能落盘（见 loadState 的备注）。 */
  let loadSeq = 0

  const searchItems = computed(() => [
    {
      key: 'keyword',
      label: '名称',
      type: 'input',
      placeholder: '容器名或镜像名',
      clearable: true
    },
    {
      key: 'state',
      label: '状态',
      type: 'select',
      placeholder: '全部状态',
      clearable: true,
      options: [
        { label: '运行中', value: 'running' },
        { label: '已停止', value: 'exited' },
        { label: '已创建', value: 'created' },
        { label: '重启中', value: 'restarting' },
        { label: '已暂停', value: 'paused' }
      ]
    },
    { key: 'runningOnly', label: '仅运行中', type: 'switch' }
  ])

  const hasFilter = computed(
    () =>
      Boolean(searchForm.value.keyword) ||
      Boolean(searchForm.value.state) ||
      Boolean(searchForm.value.runningOnly)
  )

  const filtered = computed(() => filterContainers(state.value?.containers ?? [], searchForm.value))

  /** 表格的加载态：快照在拉，或主机清单还没到（后者尚不知有没有主机，不能先喊「没有容器」）。 */
  const listLoading = computed(() => loading.value || ctx.loading)

  /** 有命中行就渲染表格；纯加载中也用表格的 loading 遮罩，空态只在加载结束后判断。 */
  const showTable = computed(() => listLoading.value || filtered.value.length > 0)

  const columns = computed(() => [
    { type: 'index' as const, width: 60, label: '序号' },
    { prop: 'name', label: '名称', minWidth: 200, showOverflowTooltip: true },
    {
      prop: 'statusText',
      label: '状态',
      width: 190,
      formatter: (row: DockerContainerItem) => containerStateText(row)
    },
    {
      prop: 'cpuPercent',
      label: 'CPU',
      width: 90,
      formatter: (row: DockerContainerItem) => cpuText(row)
    },
    {
      prop: 'memUsageMb',
      label: '内存',
      width: 170,
      formatter: (row: DockerContainerItem) => memText(row)
    },
    {
      prop: 'netTxBytesSec',
      label: '网络',
      width: 190,
      formatter: (row: DockerContainerItem) => netText(row)
    },
    { prop: 'image', label: '镜像', minWidth: 180, showOverflowTooltip: true },
    {
      prop: 'ports',
      label: '端口',
      width: 160,
      formatter: (row: DockerContainerItem) => portsText(row.ports)
    },
    {
      prop: 'protected',
      label: '保护',
      width: 80,
      formatter: (row: DockerContainerItem) => (row.protected ? '受保护' : '—')
    },
    {
      prop: 'operation',
      label: '操作',
      width: 100,
      fixed: 'right' as const,
      formatter: (row: DockerContainerItem) =>
        // 一期唯一操作是「看」：详情为常驻按钮，次要操作（日志）收进 ⋯。
        // 二期起才在 list 里追加启停/重启/删除（分期控件矩阵，spec §11.0）。
        h('div', { class: 'flex items-center' }, [
          hasAuth(PermDockerInspect)
            ? h(ArtButtonTable, {
                type: 'view',
                title: '详情',
                onClick: () =>
                  router.push({
                    name: 'DockerContainerDetail',
                    params: { id: row.id },
                    query: { host: ctx.hostId }
                  })
              })
            : null,
          h(ArtButtonMore, {
            list: [{ key: 'logs', label: '日志', icon: 'ri:file-list-3-line' }],
            onClick: () =>
              router.push({
                name: 'DockerContainerDetail',
                params: { id: row.id },
                query: { host: ctx.hostId, tab: 'logs' }
              })
          })
        ])
    }
  ])

  /**
   * 拉一次快照。静默失败：陈旧/离线由页面头部标注，不弹错（与设备页同一取向）。
   *
   * seq 守卫（照 system-monitor/views/server.vue 的既有模式）：主机切换会并发两份请求，
   * 旧主机那份可能**晚于**新主机返回 —— 直接落盘会把列表换回上一台机器的容器。
   * 故递增序号，回头发现已被更新的请求取代就丢弃（loading 也由最新那次收尾）。
   */
  async function loadState() {
    if (!ctx.hostId) return
    const seq = ++loadSeq
    loading.value = true
    try {
      const res = await fetchDockerState(ctx.hostId)
      if (seq !== loadSeq) return
      state.value = res
    } catch {
      if (seq !== loadSeq) return
      state.value = null
    } finally {
      if (seq === loadSeq) loading.value = false
    }
  }

  function onSearch() {
    // 搜索是**本地筛选**（快照已在手）：不打接口，故不需要分页重置。
    showSearchBar.value = true
  }
  function onReset() {
    searchForm.value = {}
  }

  // 主机切换 = 换一台机器：清空筛选并重新拉快照。
  watch(
    () => ctx.hostId,
    () => {
      searchForm.value = {}
      void loadState()
    }
  )

  // 首次进入：先拉主机清单（query 里的主机写回/落到第一台），再拉快照。
  // 若 hostId 要等清单到达才从 '' 变成首台 id，上面的 watch 会补一次。
  void ctx.reload().then(loadState)
</script>

<style lang="scss" scoped>
  // 空态渲染在本页（ArtTable 不转发 `#empty`）：给它接近表格空态的留白。
  .docker-empty {
    padding: 56px 0;
  }
</style>
