<template>
  <!-- 单根（single-root 守卫在库：布局的 Transition 只支持单根，双根切页白屏）。
       本组件是 resources 页「网络」tab 的内容（7a 由原 views/networks.vue 平移）：
       页面级的主机条/快照/四态在 views/resources.vue，这里只持有网络表自己的筛选
       与行内删除。 -->
  <div class="docker-networks-tab">
    <ArtSearchBar
      v-show="showSearchBar"
      v-model="searchForm"
      :items="searchItems"
      @search="onSearch"
      @reset="onReset"
    />

    <!-- 结构对齐 DockerPage 的单列表形态：搜索栏在卡片外，表格在卡片内
         （平移前的页面走 DockerPage 的 search/table 插槽，形态一致；
         本 tab 没有页脚操作区 —— 网络没有批量动作）。 -->
    <ElCard class="art-table-card" shadow="never">
      <!-- 提示行：解释「为什么看到的网络比预期多」——bridge/host/none 是 Docker 自建的，
           不是谁在本页创建的。讲清能力边界，不写「敬请期待」这类空话。 -->
      <div class="docker-hint">默认网络由 Docker 自建，删除它们不在本页能力范围</div>

      <ArtTableHeader v-model:showSearchBar="showSearchBar" :loading="loading" @refresh="refresh" />

      <!-- 两种空态分开：主机上没有网络 vs 筛选没命中（后者给「清除筛选」）。
           纪律与容器/镜像页一致（「没有」与「筛没了」说成一句会让人以为机器空了）；
           空态渲染在本组件、不写进 ArtTable 的 `#empty` 插槽：ArtTable 不转发该插槽
           （内部把 ElTable 的空态写死成「暂无数据」），写进去会被静默丢弃。
           清单还没到时也不喊「没有网络」（那时还不知道有没有主机），故 v-if 把
           主机清单的加载态一并算进来。 -->
      <ArtTable v-if="showTable" :loading="listLoading" :data="filtered" :columns="columns" />
      <!-- host-context.ts 的 reload 注释承诺：清单拉不到时页面显示「没有可管理的主机」 -->
      <ElEmpty v-else-if="!ctx.hosts.length" class="docker-empty" description="没有可管理的主机" />
      <ElEmpty v-else-if="hasFilter" class="docker-empty" description="没有符合筛选条件的网络">
        <ElButton size="small" @click="onReset">清除筛选</ElButton>
      </ElEmpty>
      <ElEmpty v-else class="docker-empty" description="该主机上还没有网络" />
    </ElCard>

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
  /**
   * 网络 tab（7a 平移自 views/networks.vue，逻辑零改动）：
   *
   * 数据源从「本组件自己拉快照（useDockerHostState）」换成页面级共享上下文 ——
   * props.state / props.loading / props.refresh 由 views/resources.vue 下发（一份快照
   * 三 tab 共用，切 tab 不重拉）；主机上下文仍是模块的 provide/inject（页面是提供者），
   * 本组件经 useDockerHost() 注入后取 ctx.hosts。
   *
   * 主机切换的重置纪律（清筛选）收拢为页面级一份（resources.vue 的 onHostSwitch
   * 调用本组件暴露的 resetForHostSwitch），不在每个 tab 里各写一份 watch。
   */
  import { computed, h, ref } from 'vue'
  import { ElButton, ElCard, ElEmpty, ElMessage } from 'element-plus'
  import { useAuth } from '@/hooks/core/useAuth'
  import { PermDockerDelete } from '@/enums/permission'
  import ArtSearchBar from '@/components/core/forms/art-search-bar/index.vue'
  import ArtTable from '@/components/core/tables/art-table/index.vue'
  import ArtTableHeader from '@/components/core/tables/art-table-header/index.vue'
  import DockerActionConfirm from '../action-confirm.vue'
  import DockerActionMenu from '../action-menu.vue'
  import type { ColumnOption } from '@/types/component'
  import type { DockerNetworkItem, DockerStateResp } from '../../api'
  import { runErrorMessage, useDockerCmds } from '../../composables/useDockerCmds'
  import { filterNetworks } from '../../utils/snapshot'
  import { useDockerHost } from '../../utils/host-context'

  defineOptions({ name: 'DockerNetworksTab' })

  const props = defineProps<{
    /** 页面级共享快照（resources.vue 的 useDockerHostState.state，三 tab 同源）。 */
    state: DockerStateResp | null
    /** 快照拉取在途（页面级一份；本 tab 的表格加载态还叠加主机清单加载）。 */
    loading: boolean
    /** 重拉共享快照（页面级 refresh：写指令成功后、表头刷新按钮都走它）。 */
    refresh: () => void | Promise<void>
  }>()

  // 主机上下文经 provide/inject 注入（页面 resources.vue 是提供者）。
  // 不要解构：上下文字段是 getter，解构会把 hostId 定格成 inject 那一刻的值。
  const ctx = useDockerHost()
  const { hasAuth } = useAuth()

  const showSearchBar = ref(false)
  const searchForm = ref<{ keyword?: string; internalOnly?: boolean }>({})

  const canDelete = computed(() => hasAuth(PermDockerDelete))

  /** 表格的加载态：快照在拉，或主机清单还没到（后者尚不知有没有主机，不能先喊「没有」）。 */
  const listLoading = computed(() => props.loading || ctx.loading)

  /** 主机切换的重置纪律（原页面 onHostSwitch 的正文，平移零改动）：清空筛选。 */
  function resetForHostSwitch() {
    searchForm.value = {}
  }
  defineExpose({ resetForHostSwitch })

  // 写指令通道：受理 + 轮询 + 成功后重拉（重拉就是页面级共享的 refresh）。
  // 主机来源走 inject（本组件是页面级 provide 的后代，setup 期注入合法）。
  const { run, pendingId, busy } = useDockerCmds({ refresh: () => props.refresh() })

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

  const filtered = computed(() => filterNetworks(props.state?.networks ?? [], searchForm.value))

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

  const columns = computed<ColumnOption<DockerNetworkItem>[]>(() => {
    // 显式建立依赖：行内菜单的禁用态来自这些信号，而 formatter 要到表格渲染时才执行 ——
    // 不在这里读一次，列配置就不会随它们变化而重算（表格会停在旧状态）。
    void pendingId.value
    void busy.value
    // 列优先级：手机横屏（<768）只留「名称 / 内部网络 / 操作」，序号让位
    //（内部网络决定容器能不能出去，是排障要看的事实）；驱动与作用域是平板竖屏
    //（>=768）起的排查上下文；容器数是计数元数据，桌面（>=1024）才展示。
    // 数据列一律 minWidth，固定宽度只留给 index/操作这类结构性列。
    return [
      { type: 'index' as const, width: 60, label: '序号', hideBelow: 'tablet' },
      { prop: 'name', label: '名称', minWidth: 240, showOverflowTooltip: true },
      {
        prop: 'driver',
        label: '驱动',
        minWidth: 120,
        // bridge/overlay/host 决定容器互通方式，平板竖屏起保留。
        hideBelow: 'tablet',
        formatter: (row: DockerNetworkItem) => row.driver || '—'
      },
      {
        prop: 'scope',
        label: '作用域',
        minWidth: 110,
        // 本机 / 集群：切换主机排查时要看网络可见范围，平板竖屏起保留。
        hideBelow: 'tablet',
        formatter: (row: DockerNetworkItem) => scopeText(row.scope)
      },
      {
        prop: 'containersCount',
        label: '容器数',
        minWidth: 100,
        // 计数是元数据，低于桌面隐藏。
        hideBelow: 'desktop'
      },
      {
        prop: 'internal',
        label: '内部网络',
        minWidth: 110,
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
  // 空态渲染在本组件（ArtTable 不转发 `#empty`）：给它接近表格空态的留白。
  .docker-empty {
    padding: 56px 0;
  }

  .docker-hint {
    margin-bottom: 12px;
    font-size: 12px;
    color: var(--el-text-color-secondary);
  }
</style>
