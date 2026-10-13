<template>
  <!-- 单根（single-root 守卫在库：布局的 Transition 只支持单根，双根切页白屏）。
       本页是「网络」整页列表（原「镜像与存储」收敛页的网络 tab 平移）：搜索栏在
       卡片外，表格在卡片内（本页没有整体动作 —— 网络没有批量动作）；删除确认弹窗
       随页收在同一根里。 -->
  <div class="docker-networks-page art-full-height">
    <ArtSearchBar
      v-show="showSearchBar"
      v-model="searchForm"
      :items="searchItems"
      @search="onSearch"
      @reset="onReset"
    />

    <ElCard class="art-table-card" shadow="never">
      <ArtTableHeader
        v-model:columns="columnChecks"
        v-model:showSearchBar="showSearchBar"
        :loading="loading"
        :has-active-filter="hasFilter"
        @refresh="refreshAll"
      >
        <template #left>
          <div class="toolbar-left">
            <!-- 硬数据文本：计数（total = 过滤后全量，翻页由分页器表达 —— 不再有
                 「列表显示前 N 条」那半句）+ 不可用主机清单 + 静默刷新失败标注。
                 计数只在 pageState=ready（握有数据）时报 —— 首拉/失败/无可管主机时
                 报「共 0 个」是假话（数据面不得伪装新鲜）；刷新失败标注不受它管
                 （那是「上次数据仍在」的另一种事实）。 -->
            <span v-if="pageState === 'ready'" class="docker-count">共 {{ total }} 个网络</span>
            <!-- 部分主机不可用（Wave 2）：采集失败的主机是一次「数据不全」的硬事实，
                 只列主机名 + reason 原文，不加解释句（页面零解释文案）。零值不渲染。 -->
            <span v-if="unavailableText" class="docker-refresh-note">{{ unavailableText }}</span>
            <span v-if="refreshError" class="docker-refresh-note">
              刷新失败，正在显示上次结果
            </span>
          </div>
        </template>
      </ArtTableHeader>

      <!-- 首拉失败（还没有任何行可给）：空态区里的紧凑失败态 —— 结论句 + 重试，
           不显示旧数据（失败不得伪装成新鲜）。 -->
      <ElEmpty v-if="pageState === 'error'" class="docker-empty" description="网络清单获取失败">
        <ElButton size="small" @click="reloadAll">重试</ElButton>
      </ElEmpty>

      <template v-else>
        <!-- 两种空态分开：没有可管主机 vs 筛选没命中（后者给「清除筛选」）。
             纪律与容器/镜像页一致；空态渲染在本页、不写进 ArtTable 的 `#empty`
             插槽（ArtTable 不转发该插槽，写进去会被静默丢弃）。 -->
        <ArtTable
          v-if="showTable"
          :loading="listLoading"
          :data="rows"
          :columns="columns"
          :pagination="pagination"
          @pagination:size-change="handleSizeChange"
          @pagination:current-change="handleCurrentChange"
          @sort-change="handleSortChange"
          @selection-change="onSelectionChange"
        />
        <ElEmpty
          v-else-if="pageState === 'empty'"
          class="docker-empty"
          description="尚无可管主机"
        />
        <ElEmpty v-else-if="hasFilter" class="docker-empty" description="没有符合筛选条件的网络">
          <ElButton size="small" @click="onReset">清除筛选</ElButton>
        </ElEmpty>
        <ElEmpty v-else class="docker-empty" description="还没有网络" />

        <!-- 批量栏（资源无关组件：本页给的是网络动作集与行投影）——勾中即出现，
             承载批量删除（逐字 DELETE 档）。吸底贴表卡底缘不与分页器相叠（错位量由栏自量）。 -->
        <WorkloadBatchBar
          v-if="showTable"
          noun="网络"
          :selected="batchRows"
          :actions="NETWORK_BATCH_ACTIONS"
          :busy="busy"
          :refresh="refreshSilent"
          @busy-change="batchBusy = $event"
        />
      </template>
    </ElCard>

    <!-- 删除网络的确认弹窗：标准档（普通确认，无逐字输入）。网络没有保护粒度
         （快照 DTO 里没有 protected 字段），不需要保护标记/强制操作的处理。 -->
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
   * 网络列表页（由「镜像与存储」收敛页的网络 tab 平移为独立整页，菜单平铺）。
   *
   * 数据源 GET /docker/networks（跨主机聚合、服务端过滤）。主机维度是**筛选下拉**
   * 的一项；行归属由主机列给出；`?host=` 深链作主机筛选初始值（单向：用户改筛选
   * 不回写 query）。
   *
   * 删除按**行主机**派发（指令通道 hostId 绑定该行）—— 一次操作属于发起时锁定的
   * 那台主机；全部动作都有行目标，本页没有「整体动作缺主机」的形态。
   */
  import { computed, h, reactive, ref, watch } from 'vue'
  import { useRoute } from 'vue-router'
  import { ElButton, ElCard, ElEmpty, ElMessage } from 'element-plus'
  import { useAuth } from '@/hooks/core/useAuth'
  import { useTableColumns } from '@/hooks/core/useTableColumns'
  import { PermDockerDelete } from '@/enums/permission'
  import ArtSearchBar from '@/components/core/forms/art-search-bar/index.vue'
  import ArtTable from '@/components/core/tables/art-table/index.vue'
  import ArtTableHeader from '@/components/core/tables/art-table-header/index.vue'
  import DockerActionConfirm from '../components/action-confirm.vue'
  import DockerActionMenu from '../components/action-menu.vue'
  import WorkloadBatchBar from '../components/workload-batch-bar.vue'
  import { NETWORK_BATCH_ACTIONS, type BatchRow } from '../components/batch-actions'
  import { fetchDockerNetworks, type DockerNetworkListItem } from '../api'
  import { runErrorMessage, useDockerCmds } from '../composables/useDockerCmds'
  import { useHostList, useResourceList } from '../composables/useResourceList'
  import { hostLabel } from '../utils/host'

  defineOptions({ name: 'DockerNetworks' })

  const route = useRoute()
  const { hasAuth } = useAuth()

  // 筛选面板**默认展开**：主机（跨主机表的筛选源）与关键词都要看得见（P1-5 口径）。
  const showSearchBar = ref(true)
  const searchForm = ref<{ keyword?: string; internalOnly?: boolean; host?: string }>({})
  /** 当前勾选的行（批量栏的输入；行内删除不受它影响）。 */
  const selected = ref<DockerNetworkListItem[]>([])
  /** 批量在途（批量栏 emit 上报）：行菜单据此禁用，防与批量并发提交同一目标。 */
  const batchBusy = ref(false)

  /** 批量栏的行投影：网络条目自带 name（网络没有保护粒度，protected 恒 false）。 */
  const batchRows = computed<BatchRow[]>(() =>
    selected.value.map((row) => ({
      hostId: row.hostId,
      hostname: row.hostname,
      name: row.name
    }))
  )

  function onSelectionChange(rowsSel: DockerNetworkListItem[]) {
    selected.value = rowsSel
  }

  const canDelete = computed(() => hasAuth(PermDockerDelete))

  // ── 可管主机清单（筛选下拉与「尚无可管主机」判定）────────────────────
  const { hosts, hostsLoading, loadHosts } = useHostList()

  // ── 数据源（跨主机聚合端点；服务端过滤，参数从筛选表单现取）──────────

  const {
    rows,
    total,
    unavailableHosts,
    loading,
    refreshError,
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
        internal: p.internal ? true : undefined,
        // 服务端分页/排序：pageSize 显式传；未选定排序时不发两键。
        page: p.page,
        pageSize: p.pageSize,
        sortBy: p.sortBy as 'containers' | 'hostId' | 'name' | undefined,
        sortOrder: p.sortBy ? p.sortOrder : undefined
      }),
    // UI 的 key（internalOnly）在这里映射成端点参数（internal）—— 不让筛选项的
    // key 泄漏进数据层（与容器页同一句分工）。分页/排序态一并在快照里现取。
    params: () => ({
      hostId: searchForm.value.host,
      keyword: searchForm.value.keyword,
      internal: searchForm.value.internalOnly === true ? true : undefined,
      page: pagination.current,
      pageSize: pagination.size,
      sortBy: sortBy.value || undefined,
      sortOrder: sortBy.value ? sortOrder.value : undefined
    }),
    hosts: () => hosts.value,
    hostsLoading: () => hostsLoading.value
  })

  // ── 服务端分页 / 排序（Wave 2）────────────────────────────────────
  // 分页态照 device：page/pageSize 显式发（pageSize 取 50，与后端默认一致）；
  // 载入后 total 从响应同步。排序只认白名单列（列 prop → 后端 sortBy 键），
  // 非法/未知 prop 一律忽略（非白名单列不该发请求）。
  const pagination = reactive({ current: 1, size: 50, total: 0 })
  watch(total, (v) => {
    pagination.total = v
  })
  /** 列 prop → 后端排序键（白名单：containers + 通用 hostId/name）。 */
  const SORT_KEYS: Record<string, string> = {
    name: 'name',
    hostname: 'hostId',
    containersCount: 'containers'
  }
  const sortBy = ref('')
  const sortOrder = ref<'asc' | 'desc'>('asc')
  function handleSortChange({ prop, order }: { prop?: string; order?: string | null }) {
    const key = prop ? SORT_KEYS[prop] : undefined
    if (!key) return
    if (order === 'ascending') {
      sortBy.value = key
      sortOrder.value = 'asc'
    } else if (order === 'descending') {
      sortBy.value = key
      sortOrder.value = 'desc'
    } else {
      // 三态循环的第三态：清回默认排序（sortBy 空即忽略方向）。
      sortBy.value = ''
    }
    pagination.current = 1
    void load()
  }
  function handleSizeChange(size: number) {
    pagination.size = size
    pagination.current = 1
    void load()
  }
  function handleCurrentChange(current: number) {
    pagination.current = current
    void load()
  }

  /** 部分主机不可用（Wave 2）：只列主机名 + reason 原文（页面零解释文案）。 */
  const unavailableText = computed(() =>
    unavailableHosts.value.length === 0
      ? ''
      : `${unavailableHosts.value.length} 台主机不可用：` +
        unavailableHosts.value.map((h) => `${h.hostname || h.hostId}（${h.reason}）`).join('、')
  )

  /** 表格的加载态：清单在拉，或主机清单还没到。 */
  const listLoading = computed(() => loading.value || hostsLoading.value)

  /** 有命中行就渲染表格；纯加载中也用表格的 loading 遮罩，空态只在加载结束后判断。 */
  const showTable = computed(() => listLoading.value || rows.value.length > 0)

  // `?host=&keyword=` 深链：query 里的主机与关键词作为**筛选初始值** —— 单向（用户改
  // 筛选不回写 query）。watch 同时兜住 worktab 缓存实例的「带 host 返回」场景；
  // ?keyword= 是**全局检索下钻**的出口（检索命中网络后跳本页并带上网络名）。
  let deepLinkInited = false
  watch(
    [() => route.query.host, () => route.query.keyword],
    ([rawHost, rawKeyword]) => {
      const id = rawHost ? String(rawHost) : ''
      const keyword = rawKeyword ? String(rawKeyword) : undefined
      const had = searchForm.value.host ?? ''
      const hadKeyword = searchForm.value.keyword ?? ''
      searchForm.value.host = id
      searchForm.value.keyword = keyword
      // 深链进入时自动展开搜索栏：从总览/检索结果下钻进来的人看到的是一张被过滤的
      // 表，筛选线索必须在明处。immediate 首跑也走这句。
      if (id || keyword) showSearchBar.value = true
      // 筛选变化（含深链首拉）回第一页：翻到第 3 页再改筛选会落到空页。
      pagination.current = 1
      if (id !== had || keyword !== hadKeyword || !deepLinkInited) void load()
      deepLinkInited = true
    },
    { immediate: true }
  )

  // 主机选项按分组渲染（ArtSearchBar 追加式分组能力：option 带 group 即分组）。
  // 无分组的归入「未分组」组；组名是数据分类，不是解释文案。
  const hostOptions = computed(() =>
    hosts.value.map((h) => ({ label: hostLabel(h), value: h.id, group: h.group || '未分组' }))
  )

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
      // 主机维度是筛选项：全部主机 + 各台。
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
    // 服务端过滤：搜索按钮即重拉，筛选变化回第一页。
    showSearchBar.value = true
    pagination.current = 1
    void load()
  }
  function onReset() {
    searchForm.value = {}
    pagination.current = 1
    void load()
  }

  /**
   * 工具条刷新（内置钮）：刷新 = 把眼前这页看新 —— 主机筛选源与清单一起重拉
   * （主机可能刚入库，只看清单会留下过期的筛选源；旧资源页 hero 的刷新即此语义）。
   * 清单走非静默 refresh：表头 loading 语义不变。
   */
  function refreshAll() {
    void loadHosts()
    void refresh()
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

  // ── 批量栏吸底 × 分页器 ──────────────────────────────────────────
  // 吸底块与分页器的错位量由**批量栏自己量**（只有栏知道自己在哪个滚动容器里，
  // 口径见 batch-actions 的 measureBatchOffset）—— 页面不再参与定位。

  // ── 表格列 ──

  // 列设置经 useTableColumns（对齐系统管理，列弹层可用）：工厂只在 setup 求值一次
  //（权限在会话内不变化）；行内菜单的禁用态由 formatter 在渲染时读取 pendingId/busy
  // —— 每次指令完成后 refreshSilent 重拉清单，表格随之重渲染。
  const { columns, columnChecks } = useTableColumns<DockerNetworkListItem>(() => {
    // 列优先级：手机横屏（<768）只留「名称 / 内部网络 / 操作」，序号让位
    //（内部网络决定容器能不能出去，是排障要看的事实）；平板竖屏（>=768）起补上
    // 主机（跨主机表的行归属）、驱动与作用域（排查上下文）；容器数是计数元数据，
    // 桌面（>=1024）才展示。数据列一律 minWidth，固定宽度只留给 index/操作这类结构性列。
    return [
      // 勾选列只在有删除权限时出现：批量删除是它唯一的用途（无权限的人勾它只会困惑）。
      ...(canDelete.value ? [{ type: 'selection' as const, width: 46 }] : []),
      { type: 'index' as const, width: 60, label: '序号', hideBelow: 'tablet' },
      {
        prop: 'name',
        label: '名称',
        minWidth: 240,
        showOverflowTooltip: true,
        sortable: 'custom' as const
      },
      {
        // 主机列是跨主机表的核心：同名网络（bridge/host/none 每台都有）
        // 散在所有机器上，行归属是排查第一线索（平板竖屏起可见）。
        // 渲染成胶囊 chip：主机是事实上的分组键，不再是最弱的灰文本。
        prop: 'hostname',
        label: '主机',
        minWidth: 120,
        showOverflowTooltip: true,
        hideBelow: 'tablet',
        sortable: 'custom' as const,
        formatter: (row: DockerNetworkListItem) =>
          h('span', { class: 'docker-host' }, hostLabel({ id: row.hostId, hostname: row.hostname }))
      },
      {
        prop: 'driver',
        label: '驱动',
        minWidth: 120,
        // 单行不变量（Wave 3c）：数据列一律 nowrap（showOverflowTooltip）。
        showOverflowTooltip: true,
        // bridge/overlay/host 决定容器互通方式，平板竖屏起保留。
        hideBelow: 'tablet',
        formatter: (row: DockerNetworkListItem) => row.driver || '—'
      },
      {
        prop: 'scope',
        label: '作用域',
        minWidth: 110,
        // 单行不变量（Wave 3c）：数据列一律 nowrap。
        showOverflowTooltip: true,
        // 本机 / 集群：排查网络可见范围时要看的事实，平板竖屏起保留。
        hideBelow: 'tablet',
        formatter: (row: DockerNetworkListItem) => scopeText(row.scope)
      },
      {
        prop: 'containersCount',
        label: '容器数',
        minWidth: 100,
        // 单行不变量（Wave 3c）：数据列一律 nowrap。
        showOverflowTooltip: true,
        align: 'right' as const,
        className: 'is-num',
        sortable: 'custom' as const,
        // 计数是元数据，低于桌面隐藏。
        hideBelow: 'desktop'
      },
      {
        prop: 'internal',
        label: '内部网络',
        minWidth: 110,
        // 单行不变量（Wave 3c）：数据列一律 nowrap。
        showOverflowTooltip: true,
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
                  // 指令在途或批量在途都禁用行菜单：防与批量并发提交同一目标。
                  disabled: busy.value || batchBusy.value,
                  onSelect: (item: { action: string }) => onRowMenu(row, item)
                })
            }
          ]
        : [])
    ]
  })
</script>

<style lang="scss" scoped>
  @use './overview-tokens' as t;

  // 主机 chip（胶囊）：主机是事实上的分组键，跨主机网络表里不再是最弱的灰文本
  // （与项目/容器/镜像/卷页同一全局类 docker-host + t.pill 制）。
  :global(.docker-host) {
    @include t.pill;
  }

  .docker-networks-page {
    /* 次要文字对比度 AA（逐页口径，与收敛页同款处置）：EP 默认
       --el-text-color-secondary(#909399) 对白底只有 3.08:1，低于 AA 正文线 →
       页面范围内把它升到 regular 档（浅色 6.1:1、暗色随主题同样达标）。 */
    --el-text-color-secondary: var(--el-text-color-regular);

    // 工具条：硬数据文本同排（本页没有动作钮），换行不挤压。
    .toolbar-left {
      display: flex;
      align-items: center;
      gap: 8px;
      flex-wrap: wrap;
    }

    // 表头计数：辅助信息，小号次要色。
    .docker-count {
      font-size: 12px;
      color: var(--el-text-color-secondary);
    }

    // 静默刷新失败标注：琥珀即「需要注意」（与模块内陈旧/离线标注同一套颜色语言）。
    .docker-refresh-note {
      font-size: 12px;
      // 文字对比度 AA（收尾批）：原 el-color-warning（白底 1.85）改走 token，
      // 数字见 @styles/core/aa-text.scss。
      color: var(--aa-warning-text);
    }

    // 空态渲染在本页（ArtTable 不转发 `#empty`）：给它接近表格空态的留白。
    .docker-empty {
      padding: 56px 0;
    }
  }
</style>
