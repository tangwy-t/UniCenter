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
      <ArtTableHeader
        v-model:showSearchBar="showSearchBar"
        :loading="loading"
        @refresh="loadState"
      />

      <!-- 两种空态分开：主机上没有镜像 vs 筛选没命中（后者给「清除筛选」）。
           纪律与容器页一致（「没有」与「筛没了」说成一句会让人以为机器空了）；
           空态渲染在本页、不写进 ArtTable 的 `#empty` 插槽：ArtTable 不转发该插槽
           （内部把 ElTable 的空态写死成「暂无数据」），写进去会被静默丢弃。
           清单还没到时也不喊「没有镜像」（那时还不知道有没有主机），故 v-if 把
           主机清单的加载态一并算进来。 -->
      <ArtTable v-if="showTable" :loading="listLoading" :data="filtered" :columns="columns" />
      <!-- host-context.ts 的 reload 注释承诺：清单拉不到时页面显示「没有可管理的主机」 -->
      <ElEmpty v-else-if="!ctx.hosts.length" class="docker-empty" description="没有可管理的主机" />
      <ElEmpty v-else-if="hasFilter" class="docker-empty" description="没有符合筛选条件的镜像">
        <ElButton size="small" @click="onReset">清除筛选</ElButton>
      </ElEmpty>
      <ElEmpty v-else class="docker-empty" description="该主机上还没有镜像" />
    </template>

    <template #footer>
      <!-- 底部合计是这一页的入口数字（「空间去哪了」）：总大小与可回收大小并列。
           合计跟着筛选走 —— 底栏与表格里的行必须自洽，否则「合计」会被当成
           与眼前行数无关的另一个数。 -->
      <div class="docker-total">
        <span>合计 {{ totals.count }} 个 · {{ formatByUnit('MB', totals.totalMB) }}</span>
        <span class="docker-total__sub">
          {{ totals.danglingCount }} 个可回收 · {{ formatByUnit('MB', totals.danglingMB) }}
        </span>
      </div>
    </template>
  </DockerPage>
</template>

<script setup lang="ts">
  import { computed, h, ref, watch } from 'vue'
  import { useRouter } from 'vue-router'
  import { ElButton, ElEmpty } from 'element-plus'
  import { formatByUnit } from '@/modules/device/utils/display'
  import { useAuth } from '@/hooks/core/useAuth'
  import { PermDockerInspect } from '@/enums/permission'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtSearchBar from '@/components/core/forms/art-search-bar/index.vue'
  import ArtTable from '@/components/core/tables/art-table/index.vue'
  import ArtTableHeader from '@/components/core/tables/art-table-header/index.vue'
  import DockerPage from '../components/docker-page.vue'
  import { fetchDockerState, type DockerImageItem, type DockerStateResp } from '../api'
  import { filterImages, imageTotals } from '../utils/snapshot'
  import { formatRelativeTime, imageRefText, inUseText } from '../utils/display'
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
  const searchForm = ref<{ keyword?: string; danglingOnly?: boolean; unusedOnly?: boolean }>({})
  /** 请求序号：只有最新一次请求的响应能落盘（见 loadState 的备注）。 */
  let loadSeq = 0

  const searchItems = computed(() => [
    {
      key: 'keyword',
      label: '仓库',
      type: 'input',
      placeholder: '仓库名或标签',
      clearable: true
    },
    { key: 'danglingOnly', label: '仅悬空', type: 'switch' },
    { key: 'unusedOnly', label: '未被使用', type: 'switch' }
  ])

  const hasFilter = computed(
    () =>
      Boolean(searchForm.value.keyword) ||
      Boolean(searchForm.value.danglingOnly) ||
      Boolean(searchForm.value.unusedOnly)
  )

  const filtered = computed(() => filterImages(state.value?.images ?? [], searchForm.value))

  /** 底栏合计：与表格里的行同源（筛选后合计的是筛出来的这批）。 */
  const totals = computed(() => imageTotals(filtered.value))

  /** 表格的加载态：快照在拉，或主机清单还没到（后者尚不知有没有主机，不能先喊「没有镜像」）。 */
  const listLoading = computed(() => loading.value || ctx.loading)

  /** 有命中行就渲染表格；纯加载中也用表格的 loading 遮罩，空态只在加载结束后判断。 */
  const showTable = computed(() => listLoading.value || filtered.value.length > 0)

  const columns = computed(() => [
    { type: 'index' as const, width: 60, label: '序号' },
    {
      prop: 'repoTags',
      label: '仓库:标签',
      minWidth: 240,
      showOverflowTooltip: true,
      formatter: (row: DockerImageItem) => imageRefText(row)
    },
    {
      prop: 'sizeMb',
      label: '大小',
      width: 110,
      formatter: (row: DockerImageItem) => formatByUnit('MB', row.sizeMb)
    },
    {
      prop: 'created',
      label: '创建于',
      width: 140,
      formatter: (row: DockerImageItem) => (row.created ? formatRelativeTime(row.created) : '—')
    },
    {
      prop: 'inUse',
      label: '使用',
      width: 150,
      showOverflowTooltip: true,
      formatter: (row: DockerImageItem) => inUseText(row)
    },
    {
      prop: 'operation',
      label: '操作',
      width: 90,
      fixed: 'right' as const,
      formatter: (row: DockerImageItem) =>
        // 一期：只有详情。打标签/导出/删除是二期（分期控件矩阵，spec §11.0），
        // 页面连它们的存在都不该暗示 —— 无权限时不渲染空操作列。
        hasAuth(PermDockerInspect)
          ? h('div', { class: 'flex items-center' }, [
              h(ArtButtonTable, {
                type: 'view',
                title: '详情',
                onClick: () =>
                  router.push({
                    name: 'DockerImageDetail',
                    // 镜像 id 带 `sha256:` 冒号，作为路由参数必须编码（详情页解码后回显短 id）。
                    params: { id: encodeURIComponent(row.id) },
                    query: { host: ctx.hostId }
                  })
              })
            ])
          : null
    }
  ])

  /**
   * 拉一次快照。静默失败：陈旧/离线由页面头部标注，不弹错（与设备页同一取向）。
   *
   * seq 守卫（照 system-monitor/views/server.vue 的既有模式）：主机切换会并发两份请求，
   * 旧主机那份可能**晚于**新主机返回 —— 直接落盘会把列表换回上一台机器的镜像。
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

  // 底栏合计：主数字（总量）与副行（可回收量）同一行、副行弱化。
  .docker-total {
    display: flex;
    align-items: baseline;
    gap: 12px;
    padding: 12px 16px 0;
    font-size: 14px;

    &__sub {
      // Element Plus 的次要文字色：副行是补充信息，不与主数字争夺注意力。
      color: var(--el-text-color-secondary);
      font-size: 13px;
    }
  }
</style>
