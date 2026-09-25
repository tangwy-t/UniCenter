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

      <!-- 两种空态分开：主机上没有数据卷 vs 筛选没命中（后者给「清除筛选」）。
           纪律与容器/镜像页一致（「没有」与「筛没了」说成一句会让人以为机器空了）；
           空态渲染在本页、不写进 ArtTable 的 `#empty` 插槽：ArtTable 不转发该插槽
           （内部把 ElTable 的空态写死成「暂无数据」），写进去会被静默丢弃。
           清单还没到时也不喊「没有数据卷」（那时还不知道有没有主机），故 v-if 把
           主机清单的加载态一并算进来。 -->
      <ArtTable v-if="showTable" :loading="listLoading" :data="filtered" :columns="columns" />
      <!-- host-context.ts 的 reload 注释承诺：清单拉不到时页面显示「没有可管理的主机」 -->
      <ElEmpty v-else-if="!ctx.hosts.length" class="docker-empty" description="没有可管理的主机" />
      <ElEmpty v-else-if="hasFilter" class="docker-empty" description="没有符合筛选条件的数据卷">
        <ElButton size="small" @click="onReset">清除筛选</ElButton>
      </ElEmpty>
      <ElEmpty v-else class="docker-empty" description="该主机上还没有数据卷" />
    </template>

    <template #footer>
      <!-- 底部合计跟着筛选走（与镜像页同一取向）：底栏与表格里的行必须自洽。
           「未知」与「未使用」是两件不同的事，分开计数：前者是**量不出来**（旧版
           Docker 没给用量），后者是**没人用**（可回收的候选）。 -->
      <div class="docker-total">
        <span>合计 {{ totals.count }} 个 · {{ formatByUnit('MB', totals.totalMB) }}</span>
        <span v-if="totalsNote" class="docker-total__sub">{{ totalsNote }}</span>
      </div>
    </template>
  </DockerPage>
</template>

<script setup lang="ts">
  import { computed, ref, watch } from 'vue'
  import { ElButton, ElEmpty } from 'element-plus'
  import { formatByUnit } from '@/modules/device/utils/display'
  import ArtSearchBar from '@/components/core/forms/art-search-bar/index.vue'
  import ArtTable from '@/components/core/tables/art-table/index.vue'
  import ArtTableHeader from '@/components/core/tables/art-table-header/index.vue'
  import DockerPage from '../components/docker-page.vue'
  import { fetchDockerState, type DockerStateResp, type DockerVolumeItem } from '../api'
  import { filterVolumes, volumeTotals } from '../utils/snapshot'
  import { provideDockerHost } from '../utils/host-context'

  // 主机上下文是**页面级** provide/inject：DockerPage 与 HostSwitcher 都用 useDockerHost() 取它，
  // 而模块里没有别的 provide 调用方 —— 页面就是这一层的提供者，故在这里 provide 并直接用其返回值。
  // 不要解构：上下文字段是 getter，解构会把 hostId 定格成进入页面时的 ''（主机清单尚未到达）。
  const ctx = provideDockerHost()

  const loading = ref(false)
  const showSearchBar = ref(false)
  const state = ref<DockerStateResp | null>(null)
  const searchForm = ref<{ keyword?: string; unusedOnly?: boolean }>({})
  /** 请求序号：只有最新一次请求的响应能落盘（见 loadState 的备注）。 */
  let loadSeq = 0

  const searchItems = computed(() => [
    {
      key: 'keyword',
      label: '名称',
      type: 'input',
      placeholder: '数据卷名',
      clearable: true
    },
    { key: 'unusedOnly', label: '未被使用', type: 'switch' }
  ])

  const hasFilter = computed(
    () => Boolean(searchForm.value.keyword) || Boolean(searchForm.value.unusedOnly)
  )

  const filtered = computed(() => filterVolumes(state.value?.volumes ?? [], searchForm.value))

  /** 底栏合计：与表格里的行同源（筛选后合计的是筛出来的这批）。 */
  const totals = computed(() => volumeTotals(filtered.value))

  /** 补充结论：有未知用量/有未使用才出现，避免底栏常驻一串 0。 */
  const totalsNote = computed(() => {
    const notes: string[] = []
    if (totals.value.unknownSizeCount) notes.push(`· ${totals.value.unknownSizeCount} 个大小未知`)
    if (totals.value.unusedCount) notes.push(`· ${totals.value.unusedCount} 个未使用`)
    return notes.join(' ')
  })

  /** 表格的加载态：快照在拉，或主机清单还没到（后者尚不知有没有主机，不能先喊「没有数据卷」）。 */
  const listLoading = computed(() => loading.value || ctx.loading)

  /** 有命中行就渲染表格；纯加载中也用表格的 loading 遮罩，空态只在加载结束后判断。 */
  const showTable = computed(() => listLoading.value || filtered.value.length > 0)

  const columns = computed(() => [
    { type: 'index' as const, width: 60, label: '序号' },
    { prop: 'name', label: '名称', minWidth: 240, showOverflowTooltip: true },
    {
      prop: 'driver',
      label: '驱动',
      width: 120,
      formatter: (row: DockerVolumeItem) => row.driver || '—'
    },
    {
      prop: 'sizeMb',
      label: '大小',
      width: 120,
      // 未知用量显示「—」而不是 0：0 与「量不出来」在「空间去哪了」上是相反的结论。
      formatter: (row: DockerVolumeItem) =>
        row.sizeMb === undefined || row.sizeMb === null ? '—' : formatByUnit('MB', row.sizeMb)
    },
    {
      prop: 'inUse',
      label: '使用',
      minWidth: 220,
      formatter: (row: DockerVolumeItem) =>
        row.inUse ? `被 ${(row.mountedBy ?? []).join('、') || '容器'} 挂载` : '未使用'
    }
    // 一期卷没有只读动作（详情/查看都是二期起），故**不渲染操作列** ——
    // 空操作列会让人以为「有东西没加载出来」（分期控件矩阵，spec §11.0）。
  ])

  /**
   * 拉一次快照。静默失败：陈旧/离线由页面头部标注，不弹错（与设备页同一取向）。
   *
   * seq 守卫（照 system-monitor/views/server.vue 的既有模式）：主机切换会并发两份请求，
   * 旧主机那份可能**晚于**新主机返回 —— 直接落盘会把列表换回上一台机器的数据卷。
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

  // 底栏合计：主数字（总量）与补充结论（未知/未使用）同一行、补充结论弱化。
  .docker-total {
    display: flex;
    align-items: baseline;
    gap: 12px;
    padding: 12px 16px 0;
    font-size: 14px;

    &__sub {
      // Element Plus 的次要文字色：补充结论是解释，不与主数字争夺注意力。
      color: var(--el-text-color-secondary);
      font-size: 13px;
    }
  }
</style>
