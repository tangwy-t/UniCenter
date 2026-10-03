<template>
  <!-- 单根（single-root 守卫在库：布局的 Transition 只支持单根，双根切页白屏）。
       本组件是 resources 页「网络」tab 的内容（9b 起为跨主机聚合表）：主机清单
       经 props 由页面下发（页面级一份，三 tab 共用），清单数据走自己的聚合端点
       （GET /docker/networks，服务端过滤）；本组件只持有网络表自己的筛选与行内删除。 -->
  <div class="docker-networks-tab">
    <ArtSearchBar
      v-show="showSearchBar"
      v-model="searchForm"
      :items="searchItems"
      @search="onSearch"
      @reset="onReset"
    />

    <!-- 结构沿用收敛页范式：搜索栏在卡片外，表格在卡片内
         （本 tab 没有页脚操作区 —— 网络没有批量动作）。 -->
    <ElCard class="art-table-card" shadow="never">
      <!-- 提示行：解释「为什么看到的网络比预期多」——bridge/host/none 是 Docker 自建的，
           不是谁在本页创建的。讲清能力边界，不写「敬请期待」这类空话。 -->
      <div class="docker-hint">默认网络由 Docker 自建，删除它们不在本页能力范围</div>

      <ArtTableHeader v-model:showSearchBar="showSearchBar" :loading="loading" @refresh="refresh">
        <template #left>
          <!-- total 如实报全量、items 截 500：截断必须说出口（与容器/镜像页同一句）。 -->
          <span class="docker-count">
            共 {{ total }} 个网络<template v-if="truncated">
              · 列表显示前 {{ rows.length }} 条</template
            >
          </span>
          <!-- 静默刷新失败：保留最后已知清单并说出口（与容器页副标题同一口径）。 -->
          <span v-if="refreshError" class="docker-refresh-note">刷新失败，正在显示上次结果</span>
        </template>
      </ArtTableHeader>

      <!-- 首拉失败（还没有任何行可给）：结论句 + 重试，不显示旧数据。 -->
      <div v-if="pageState === 'error'" class="docker-tab-error">
        <ElResult icon="error" title="网络清单获取失败" sub-title="统一清单接口暂时不可用">
          <template #extra>
            <ElButton type="primary" @click="reloadAll">重试</ElButton>
          </template>
        </ElResult>
      </div>

      <template v-else>
        <!-- 两种空态分开：没有可管主机 vs 筛选没命中（后者给「清除筛选」）。
             纪律与容器/镜像页一致；空态渲染在本组件、不写进 ArtTable 的 `#empty`
             插槽（ArtTable 不转发该插槽，写进去会被静默丢弃）。 -->
        <ArtTable v-if="showTable" :loading="listLoading" :data="rows" :columns="columns" />
        <ElEmpty
          v-else-if="pageState === 'empty'"
          class="docker-empty"
          description="尚无可管主机"
        />
        <ElEmpty v-else-if="hasFilter" class="docker-empty" description="没有符合筛选条件的网络">
          <ElButton size="small" @click="onReset">清除筛选</ElButton>
        </ElEmpty>
        <ElEmpty v-else class="docker-empty" description="还没有网络" />
      </template>
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
   * 网络 tab（9b：跨主机化，同容器统一表范式）。
   *
   * 数据源从「页面级共享的单主机快照 + 本地过滤」换成 GET /docker/networks
   * （跨主机聚合、服务端过滤）。主机维度从页面级上下文降为**筛选下拉**的一项；
   * 行归属由新增的主机列给出；`/docker/resources?host=` 深链照旧有效（作为
   * 主机筛选初始值）。
   *
   * 删除按**行主机**派发（指令通道 hostId 绑定该行）—— 一次操作属于发起时
   * 锁定的那台主机；全部动作都有行目标，本 tab 没有「整体动作缺主机」的形态。
   */
  import { computed, h, ref, watch } from 'vue'
  import { useRoute } from 'vue-router'
  import { ElButton, ElCard, ElEmpty, ElMessage, ElResult } from 'element-plus'
  import { useAuth } from '@/hooks/core/useAuth'
  import { PermDockerDelete } from '@/enums/permission'
  import ArtSearchBar from '@/components/core/forms/art-search-bar/index.vue'
  import ArtTable from '@/components/core/tables/art-table/index.vue'
  import ArtTableHeader from '@/components/core/tables/art-table-header/index.vue'
  import DockerActionConfirm from '../action-confirm.vue'
  import DockerActionMenu from '../action-menu.vue'
  import type { ColumnOption } from '@/types/component'
  import { fetchDockerNetworks, type DockerHostItem, type DockerNetworkListItem } from '../../api'
  import { runErrorMessage, useDockerCmds } from '../../composables/useDockerCmds'
  import { useResourceList } from '../../composables/useResourceList'
  import { hostLabel } from '../../utils/host'

  defineOptions({ name: 'DockerNetworksTab' })

  const props = defineProps<{
    /** 页面级主机清单（resources.vue 的 useHostList，三 tab 共用）。 */
    hosts: DockerHostItem[]
    /** 主机清单在拉：首拉期间不能先喊「没有」（那时还不知道有没有主机）。 */
    hostsLoading: boolean
  }>()

  const route = useRoute()
  const { hasAuth } = useAuth()

  const showSearchBar = ref(false)
  const searchForm = ref<{ keyword?: string; internalOnly?: boolean; host?: string }>({})

  const canDelete = computed(() => hasAuth(PermDockerDelete))

  // ── 数据源（跨主机聚合端点；服务端过滤，参数从筛选表单现取）──────────

  const {
    rows,
    total,
    loading,
    refreshError,
    truncated,
    pageState,
    load,
    refresh,
    refreshSilent,
    reloadAll
  } = useResourceList<DockerNetworkListItem>({
    fetcher: (p) =>
      fetchDockerNetworks({
        hostId: p.hostId || undefined,
        keyword: p.keyword || undefined,
        // 开关只发 true（「仅内部网络」是单侧开关）；false 侧语义留给端点。
        internal: p.internal ? true : undefined
      }),
    // UI 的 key（internalOnly）在这里映射成端点参数（internal）—— 不让筛选项的
    // key 泄漏进数据层（与容器页同一句分工）。
    params: () => ({
      hostId: searchForm.value.host,
      keyword: searchForm.value.keyword,
      internal: searchForm.value.internalOnly === true ? true : undefined
    }),
    hosts: () => props.hosts,
    hostsLoading: () => props.hostsLoading
  })

  /** 表格的加载态：清单在拉，或主机清单还没到。 */
  const listLoading = computed(() => loading.value || props.hostsLoading)

  /** 有命中行就渲染表格；纯加载中也用表格的 loading 遮罩，空态只在加载结束后判断。 */
  const showTable = computed(() => listLoading.value || rows.value.length > 0)

  // `/docker/resources?host=` 深链兼容：query 里的主机作为**主机筛选初始值** ——
  // 单向（用户改筛选不回写 query）。watch 同时兜住 worktab 缓存实例的
  // 「带 host 返回」场景。
  watch(
    () => route.query.host,
    (raw, old) => {
      const id = raw ? String(raw) : ''
      const had = searchForm.value.host ?? ''
      searchForm.value.host = id
      if (id !== had || old === undefined) void load()
    },
    { immediate: true }
  )

  const hostOptions = computed(() => props.hosts.map((h) => ({ label: hostLabel(h), value: h.id })))

  const searchItems = computed(() => [
    {
      key: 'keyword',
      label: '名称',
      type: 'input',
      placeholder: '网络名',
      clearable: true
    },
    { key: 'internalOnly', label: '仅内部网络', type: 'switch' },
    {
      // 主机维度从页面级上下文（HostSwitcher）降为筛选项：全部主机 + 各台。
      // filterable：主机多了要能敲名字找（QA 实测不可搜），与容器/镜像/卷/项目页同款。
      key: 'host',
      label: '主机',
      type: 'select',
      placeholder: '全部主机',
      clearable: true,
      filterable: true,
      options: hostOptions.value
    }
  ])

  const hasFilter = computed(
    () =>
      Boolean(searchForm.value.keyword) ||
      Boolean(searchForm.value.internalOnly) ||
      Boolean(searchForm.value.host)
  )

  function onSearch() {
    // 服务端过滤：搜索按钮即重拉（无分页可重置）。
    showSearchBar.value = true
    void load()
  }
  function onReset() {
    searchForm.value = {}
    void load()
  }

  /** 作用域：`local` 只在本机可见，其余（`swarm`/`global`）都是集群级。 */
  function scopeText(scope?: string): string {
    if (!scope) return '—'
    return scope === 'local' ? '本机' : '集群'
  }

  // ── 行内删除（标准确认档）────────────────────────────────

  /** 指令通道的主机来源：删除在**入口处锁定到该行的 hostId**（一次操作属于发起时锁定的那台主机）。 */
  const cmdHostId = ref('')
  const { run, pendingId, busy } = useDockerCmds({
    hostId: () => cmdHostId.value,
    refresh: refreshSilent
  })

  const removeConfirm = ref<{ visible: boolean; target: string }>({ visible: false, target: '' })
  const confirmLoading = ref(false)

  /** ⋯ 菜单选中：删除走确认弹窗（网络的确认档由注册表推导，标准档=普通确认），主机取行主机。 */
  function onRowMenu(row: DockerNetworkListItem, item: { action: string }) {
    if (item.action !== 'network:remove') return
    cmdHostId.value = row.hostId
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

  const columns = computed<ColumnOption<DockerNetworkListItem>[]>(() => {
    // 显式建立依赖：行内菜单的禁用态来自这些信号，而 formatter 要到表格渲染时才执行 ——
    // 不在这里读一次，列配置就不会随它们变化而重算（表格会停在旧状态）。
    void pendingId.value
    void busy.value
    // 列优先级：手机横屏（<768）只留「名称 / 内部网络 / 操作」，序号让位
    //（内部网络决定容器能不能出去，是排障要看的事实）；平板竖屏（>=768）起补上
    // 主机（跨主机表的行归属）、驱动与作用域（排查上下文）；容器数是计数元数据，
    // 桌面（>=1024）才展示。数据列一律 minWidth，固定宽度只留给 index/操作这类结构性列。
    return [
      { type: 'index' as const, width: 60, label: '序号', hideBelow: 'tablet' },
      { prop: 'name', label: '名称', minWidth: 240, showOverflowTooltip: true },
      {
        // 主机列是跨主机表的核心新增：同名网络（bridge/host/none 每台都有）
        // 散在所有机器上，行归属是排查第一线索（平板竖屏起可见）。
        prop: 'hostname',
        label: '主机',
        minWidth: 120,
        showOverflowTooltip: true,
        hideBelow: 'tablet',
        formatter: (row: DockerNetworkListItem) => row.hostname || row.hostId
      },
      {
        prop: 'driver',
        label: '驱动',
        minWidth: 120,
        // bridge/overlay/host 决定容器互通方式，平板竖屏起保留。
        hideBelow: 'tablet',
        formatter: (row: DockerNetworkListItem) => row.driver || '—'
      },
      {
        prop: 'scope',
        label: '作用域',
        minWidth: 110,
        // 本机 / 集群：排查网络可见范围时要看的事实，平板竖屏起保留。
        hideBelow: 'tablet',
        formatter: (row: DockerNetworkListItem) => scopeText(row.scope)
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
        formatter: (row: DockerNetworkListItem) => (row.internal ? '仅内部' : '—')
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
              formatter: (row: DockerNetworkListItem) =>
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

  // 页面级刷新入口（resources.vue 的 hero 刷新按钮对当前 tab 调它）。
  defineExpose({ refresh, reloadAll })
</script>

<style lang="scss" scoped>
  @use '../../views/overview-tokens' as t;

  /* 矮视口单滚动链的 tab 根段（链的其余部分：app.scss 的页根/卡片/表 +
     resources.vue 的 ElTabs 两层）：让本组件根吃满 pane 的高度。 */
  .docker-networks-tab {
    @include t.fill-chain-root;
  }


  // 空态渲染在本组件（ArtTable 不转发 `#empty`）：给它接近表格空态的留白。
  .docker-empty {
    padding: 56px 0;
  }

  // 首拉失败的错误块：ElResult 自带留白，这里只补卡片内边距。
  .docker-tab-error {
    padding: 12px 0;
  }

  // 表头计数：辅助信息，小号次要色（与容器页的 docker-count 同款）。
  .docker-count {
    font-size: 12px;
    color: var(--el-text-color-secondary);
  }

  // 静默刷新失败标注：琥珀即「需要注意」（与模块内陈旧/离线标注同一套颜色语言）。
  .docker-refresh-note {
    margin-left: 10px;
    font-size: 12px;
    // 文字对比度 AA（收尾批）：原 el-color-warning（白底 1.85）改走 token，
    // 数字见 @styles/core/aa-text.scss。
    color: var(--aa-warning-text);
  }

  .docker-hint {
    margin-bottom: 12px;
    font-size: 12px;
    color: var(--el-text-color-secondary);
  }

  /* 手机横屏紧凑档：底栏收薄（放在本块最后 = 同权重按源码序盖住上面的基础留白）。 */
  @include t.compact-bottom-bars;
</style>
