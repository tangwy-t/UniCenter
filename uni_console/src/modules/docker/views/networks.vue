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
        @refresh="refreshAll"
      >
        <template #left>
          <div class="toolbar-left">
            <!-- 硬数据文本：计数（total 如实报全量、items 截 500，截断必须说出口）
                 + 静默刷新失败标注。计数只在 pageState=ready（握有数据）时报 ——
                 首拉/失败/无可管主机时报「共 0 个」是假话（数据面不得伪装新鲜）；
                 刷新失败标注不受它管（那是「上次数据仍在」的另一种事实）。 -->
            <span v-if="pageState === 'ready'" class="docker-count">
              共 {{ total }} 个网络<template v-if="truncated">
                · 列表显示前 {{ rows.length }} 条</template
              >
            </span>
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
  import { computed, h, ref, watch } from 'vue'
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
  import { fetchDockerNetworks, type DockerNetworkListItem } from '../api'
  import { runErrorMessage, useDockerCmds } from '../composables/useDockerCmds'
  import { useHostList, useResourceList } from '../composables/useResourceList'
  import { hostLabel } from '../utils/host'

  defineOptions({ name: 'DockerNetworks' })

  const route = useRoute()
  const { hasAuth } = useAuth()

  const showSearchBar = ref(false)
  const searchForm = ref<{ keyword?: string; internalOnly?: boolean; host?: string }>({})

  const canDelete = computed(() => hasAuth(PermDockerDelete))

  // ── 可管主机清单（筛选下拉与「尚无可管主机」判定）────────────────────
  const { hosts, hostsLoading, loadHosts } = useHostList()

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
    hosts: () => hosts.value,
    hostsLoading: () => hostsLoading.value
  })

  /** 表格的加载态：清单在拉，或主机清单还没到。 */
  const listLoading = computed(() => loading.value || hostsLoading.value)

  /** 有命中行就渲染表格；纯加载中也用表格的 loading 遮罩，空态只在加载结束后判断。 */
  const showTable = computed(() => listLoading.value || rows.value.length > 0)

  // `?host=` 深链：query 里的主机作为**主机筛选初始值** —— 单向（用户改筛选不回写
  // query）。watch 同时兜住 worktab 缓存实例的「带 host 返回」场景。
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

  const hostOptions = computed(() => hosts.value.map((h) => ({ label: hostLabel(h), value: h.id })))

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
    // 服务端过滤：搜索按钮即重拉（无分页可重置）。
    showSearchBar.value = true
    void load()
  }
  function onReset() {
    searchForm.value = {}
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
      { type: 'index' as const, width: 60, label: '序号', hideBelow: 'tablet' },
      { prop: 'name', label: '名称', minWidth: 240, showOverflowTooltip: true },
      {
        // 主机列是跨主机表的核心：同名网络（bridge/host/none 每台都有）
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
</script>

<style lang="scss" scoped>
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
