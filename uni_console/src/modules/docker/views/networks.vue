<template>
  <!-- ⚠ 单根包装：页面**必须只有一个根节点**（布局把页面放进 `<Transition mode="out-in">`，
       而 Transition 只支持单根元素）。二期起本页多了确认弹窗，与容器页同一处理：
       页面与弹窗收进一个根 div —— 双根会在切页时白屏（single-root.test.ts 扫描钉住）。 -->
  <div class="docker-networks-page">
    <DockerPage
      :loading="loading"
      :stale="stale"
      :age-seconds="ageSeconds"
      :never-reported="neverReported"
      @refresh="refresh"
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
        <!-- 提示行：解释「为什么看到的网络比预期多」——bridge/host/none 是 Docker 自建的，
             不是谁在本页创建的。讲清能力边界，不写「敬请期待」这类空话。 -->
        <div class="docker-hint">默认网络由 Docker 自建，删除它们不在本页能力范围</div>

        <ArtTableHeader
          v-model:showSearchBar="showSearchBar"
          :loading="loading"
          @refresh="refresh"
        />

        <!-- 两种空态分开：主机上没有网络 vs 筛选没命中（后者给「清除筛选」）。
             纪律与容器/镜像页一致（「没有」与「筛没了」说成一句会让人以为机器空了）；
             空态渲染在本页、不写进 ArtTable 的 `#empty` 插槽：ArtTable 不转发该插槽
             （内部把 ElTable 的空态写死成「暂无数据」），写进去会被静默丢弃。
             清单还没到时也不喊「没有网络」（那时还不知道有没有主机），故 v-if 把
             主机清单的加载态一并算进来。 -->
        <ArtTable v-if="showTable" :loading="listLoading" :data="filtered" :columns="columns" />
        <!-- host-context.ts 的 reload 注释承诺：清单拉不到时页面显示「没有可管理的主机」 -->
        <ElEmpty
          v-else-if="!ctx.hosts.length"
          class="docker-empty"
          description="没有可管理的主机"
        />
        <ElEmpty v-else-if="hasFilter" class="docker-empty" description="没有符合筛选条件的网络">
          <ElButton size="small" @click="onReset">清除筛选</ElButton>
        </ElEmpty>
        <ElEmpty v-else class="docker-empty" description="该主机上还没有网络" />
      </template>
    </DockerPage>

    <!-- 删除网络的确认弹窗：标准档（普通确认，无逐字输入）。网络没有保护粒度
         （快照 DTO 里没有 protected 字段），不需要 🔒/强制操作的处理。 -->
    <DockerActionConfirm
      v-model="removeConfirm.visible"
      action="network:remove"
      :target="removeConfirm.target"
      target-kind="网络"
      :loading="confirmLoading"
      @confirm="onConfirmSubmit"
    />
  </div>
</template>

<script setup lang="ts">
  import { computed, h, ref } from 'vue'
  import { ElButton, ElEmpty, ElMessage } from 'element-plus'
  import { useAuth } from '@/hooks/core/useAuth'
  import { PermDockerDelete } from '@/enums/permission'
  import ArtSearchBar from '@/components/core/forms/art-search-bar/index.vue'
  import ArtTable from '@/components/core/tables/art-table/index.vue'
  import ArtTableHeader from '@/components/core/tables/art-table-header/index.vue'
  import DockerActionConfirm from '../components/action-confirm.vue'
  import DockerActionMenu from '../components/action-menu.vue'
  import DockerPage from '../components/docker-page.vue'
  import type { DockerNetworkItem } from '../api'
  import { runErrorMessage, useDockerCmds } from '../composables/useDockerCmds'
  import { useDockerHostState } from '../composables/useDockerHostState'
  import { filterNetworks } from '../utils/snapshot'
  import { provideDockerHost } from '../utils/host-context'

  // 主机上下文是**页面级** provide/inject：DockerPage 与 HostSwitcher 都用 useDockerHost() 取它，
  // 而模块里没有别的 provide 调用方 —— 页面就是这一层的提供者，故在这里 provide 并直接用其返回值。
  // 不要解构：上下文字段是 getter，解构会把 hostId 定格成进入页面时的 ''（主机清单尚未到达）。
  const ctx = provideDockerHost()
  const { hasAuth } = useAuth()

  const showSearchBar = ref(false)
  const searchForm = ref<{ keyword?: string; internalOnly?: boolean }>({})

  const canDelete = computed(() => hasAuth(PermDockerDelete))

  // 快照与四态收口在 composable（hosts 清单、seq 守卫、主机切换后的重拉都在它里面）。
  // 主机切换 = 换一台机器：本页既有重置纪律是「清空筛选」。
  const { state, loading, listLoading, stale, ageSeconds, neverReported, refresh } =
    useDockerHostState({
      onHostSwitch: () => {
        searchForm.value = {}
      }
    })

  // 写指令通道：受理 + 轮询 + 成功后重拉（重拉就是上面的 refresh）。
  const { run, pendingId, busy } = useDockerCmds({ refresh })

  const searchItems = computed(() => [
    {
      key: 'keyword',
      label: '名称',
      type: 'input',
      placeholder: '网络名',
      clearable: true
    },
    { key: 'internalOnly', label: '仅内部网络', type: 'switch' }
  ])

  const hasFilter = computed(
    () => Boolean(searchForm.value.keyword) || Boolean(searchForm.value.internalOnly)
  )

  const filtered = computed(() => filterNetworks(state.value?.networks ?? [], searchForm.value))

  /** 有命中行就渲染表格；纯加载中也用表格的 loading 遮罩，空态只在加载结束后判断。 */
  const showTable = computed(() => listLoading.value || filtered.value.length > 0)

  /** 作用域：`local` 只在本机可见，其余（`swarm`/`global`）都是集群级。 */
  function scopeText(scope?: string): string {
    if (!scope) return '—'
    return scope === 'local' ? '本机' : '集群'
  }

  // ── 行内删除（标准确认档）──

  const removeConfirm = ref<{ visible: boolean; target: string }>({ visible: false, target: '' })
  const confirmLoading = ref(false)

  /** ⋯ 菜单选中：删除走确认弹窗（网络的确认档由注册表推导，标准档=普通确认）。 */
  function onRowMenu(row: DockerNetworkItem, item: { action: string }) {
    if (item.action !== 'network:remove') return
    removeConfirm.value = { visible: true, target: row.name }
  }

  async function onConfirmSubmit(payload: { confirm: string; force: boolean }) {
    const target = removeConfirm.value.target
    confirmLoading.value = true
    try {
      const res = await run({ action: 'network:remove', target, confirm: payload.confirm })
      if (res.ok) ElMessage.success(res.detail || '操作已完成')
      else ElMessage.error(runErrorMessage(res, '操作未完成'))
    } finally {
      confirmLoading.value = false
      removeConfirm.value = { visible: false, target: '' }
    }
  }

  // ── 表格列 ──

  const columns = computed(() => {
    // 显式建立依赖：行内菜单的禁用态来自这些信号，而 formatter 要到表格渲染时才执行 ——
    // 不在这里读一次，列配置就不会随它们变化而重算（表格会停在旧状态）。
    void pendingId.value
    void busy.value
    return [
      { type: 'index' as const, width: 60, label: '序号' },
      { prop: 'name', label: '名称', minWidth: 240, showOverflowTooltip: true },
      {
        prop: 'driver',
        label: '驱动',
        width: 120,
        formatter: (row: DockerNetworkItem) => row.driver || '—'
      },
      {
        prop: 'scope',
        label: '作用域',
        width: 110,
        formatter: (row: DockerNetworkItem) => scopeText(row.scope)
      },
      { prop: 'containersCount', label: '容器数', width: 100 },
      {
        prop: 'internal',
        label: '内部网络',
        width: 110,
        // 「仅内部」才是要点：内部网络没有对外出口，排障时这条决定了容器能不能出去。
        formatter: (row: DockerNetworkItem) => (row.internal ? '仅内部' : '—')
      },
      // 操作列只在有删除权限时出现：没有可执行的动作，空操作列会让人以为
      //「有东西没加载出来」（分期控件矩阵，spec §11.0）。
      ...(canDelete.value
        ? [
            {
              prop: 'operation',
              label: '操作',
              width: 100,
              fixed: 'right' as const,
              formatter: (row: DockerNetworkItem) =>
                h(DockerActionMenu, {
                  actions: ['network:remove'],
                  target: row.name,
                  pendingId: pendingId.value,
                  disabled: busy.value,
                  onSelect: (item: { action: string }) => onRowMenu(row, item)
                })
            }
          ]
        : [])
    ]
  })

  function onSearch() {
    // 搜索是**本地筛选**（快照已在手）：不打接口，故不需要分页重置。
    showSearchBar.value = true
  }
  function onReset() {
    searchForm.value = {}
  }
</script>

<style lang="scss" scoped>
  // 空态渲染在本页（ArtTable 不转发 `#empty`）：给它接近表格空态的留白。
  .docker-empty {
    padding: 56px 0;
  }

  .docker-hint {
    margin-bottom: 12px;
    font-size: 12px;
    color: var(--el-text-color-secondary);
  }
</style>
