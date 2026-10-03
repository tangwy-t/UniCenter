<template>
  <!-- 单根（single-root 守卫在库：布局的 Transition 只支持单根，双根切页白屏）。
       本组件是 resources 页「数据卷」tab 的内容（9b 起为跨主机聚合表）：主机清单
       经 props 由页面下发（页面级一份，三 tab 共用），清单数据走自己的聚合端点
       （GET /docker/volumes，服务端过滤）；本组件只持有卷表自己的筛选、行内删除
       与底栏清理。 -->
  <div class="docker-volumes-tab">
    <ArtSearchBar
      v-show="showSearchBar"
      v-model="searchForm"
      :items="searchItems"
      @search="onSearch"
      @reset="onReset"
    />

    <!-- 结构沿用收敛页范式：搜索栏在卡片外，表格与页脚在卡片内。 -->
    <ElCard class="art-table-card" shadow="never">
      <ArtTableHeader v-model:showSearchBar="showSearchBar" :loading="loading" @refresh="refresh">
        <template #left>
          <!-- total 如实报全量、items 截 500：截断必须说出口（与容器/镜像页同一句）。 -->
          <span class="docker-count">
            共 {{ total }} 个数据卷<template v-if="truncated">
              · 列表显示前 {{ rows.length }} 条</template
            >
          </span>
          <!-- 静默刷新失败：保留最后已知清单并说出口（与容器页副标题同一口径）。 -->
          <span v-if="refreshError" class="docker-refresh-note">刷新失败，正在显示上次结果</span>
        </template>
      </ArtTableHeader>

      <!-- 首拉失败（还没有任何行可给）：结论句 + 重试，不显示旧数据。 -->
      <div v-if="pageState === 'error'" class="docker-tab-error">
        <ElResult icon="error" title="数据卷清单获取失败" sub-title="统一清单接口暂时不可用">
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
        <ElEmpty v-else-if="hasFilter" class="docker-empty" description="没有符合筛选条件的数据卷">
          <ElButton size="small" @click="onReset">清除筛选</ElButton>
        </ElEmpty>
        <ElEmpty v-else class="docker-empty" description="还没有数据卷" />

        <!-- 底部合计跟着**当前清单**走（服务端过滤后的这批；截断时 header 已另行
             说明）：底栏与表格里的行必须自洽。「未知」与「未使用」是两件不同的事，
             分开计数：前者是**量不出来**（旧版 Docker 没给用量），后者是**没人用**
             （可回收的候选）。 -->
        <div class="docker-total">
          <span>合计 {{ totals.count }} 个 · {{ formatByUnit('MB', totals.totalMB) }}</span>
          <span v-if="totalsNote" class="docker-total__sub">{{ totalsNote }}</span>
        </div>

        <!-- 底栏写操作（spec §11.3 的分期矩阵）：清理未使用卷是强档（弹窗里逐字
             输入 DELETE，由组件的确认档推导）；该动作没有 options 变体，弹窗里
             不放任何勾选项。跨主机表没有「当前主机」：整体动作面向**主机筛选选定的
             那台**（没选且全场只有一台可管主机时就是它；否则禁用 + 结论句）。
             指令在途时禁用，防重复提交。 -->
        <div v-if="canDelete" class="docker-bar">
          <ElButton
            size="small"
            type="danger"
            plain
            :disabled="busy || !targetHostId"
            @click="openPrune"
          >
            清理未使用卷…
          </ElButton>
          <span v-if="!targetHostId" class="docker-bar__hint">
            清理未使用卷需先在「主机」筛选中选定一台主机
          </span>
        </div>
      </template>
    </ElCard>

    <!-- 确认弹窗覆盖两个入口：单卷删除（标准档）与清理未使用卷（强档逐字 DELETE）。
         形态、逐字期望值与保护提示全部由组件按动作注册表推导；页面只传事实。
         受保护卷的「强制操作」开关也由组件按 docker:exec 权限判定后渲染。 -->
    <DockerActionConfirm
      v-model="confirmState.visible"
      :action="confirmState.action"
      :target="confirmState.target"
      :target-protected="confirmState.protected"
      target-kind="数据卷"
      :loading="confirmLoading"
      @confirm="onConfirmSubmit"
    />
  </div>
</template>

<script setup lang="ts">
  /**
   * 数据卷 tab（9b：跨主机化，同容器统一表范式）。
   *
   * 数据源从「页面级共享的单主机快照 + 本地过滤」换成 GET /docker/volumes
   * （跨主机聚合、服务端过滤）。主机维度从页面级上下文降为**筛选下拉**的一项；
   * 行归属由新增的主机列给出；`/docker/resources?host=` 深链照旧有效（作为
   * 主机筛选初始值 —— 总览磁盘面板的 goHostVolumes 既有链路不动）。
   *
   * 删除按**行主机**派发（指令通道 hostId 绑定该行）；清理未使用卷（无行目标）
   * 面向主机筛选选定的那台（没选且只有一台可管主机时就是它，否则禁用 + 结论句）。
   * 一次操作属于发起时锁定的那台主机 —— 没有「切了主机要清什么」的重置纪律。
   */
  import { computed, h, ref, watch } from 'vue'
  import { useRoute } from 'vue-router'
  import { ElButton, ElCard, ElEmpty, ElMessage, ElResult } from 'element-plus'
  import { useAuth } from '@/hooks/core/useAuth'
  import { PermDockerDelete, PermDockerExec } from '@/enums/permission'
  import { formatByUnit } from '@/modules/device/utils/display'
  import ArtSearchBar from '@/components/core/forms/art-search-bar/index.vue'
  import ArtSvgIcon from '@/components/core/base/art-svg-icon/index.vue'
  import ArtTable from '@/components/core/tables/art-table/index.vue'
  import ArtTableHeader from '@/components/core/tables/art-table-header/index.vue'
  import DockerActionConfirm from '../action-confirm.vue'
  import DockerActionMenu from '../action-menu.vue'
  import type { ColumnOption } from '@/types/component'
  import { fetchDockerVolumes, type DockerHostItem, type DockerVolumeListItem } from '../../api'
  import { runErrorMessage, useDockerCmds } from '../../composables/useDockerCmds'
  import { useResourceList } from '../../composables/useResourceList'
  import { protectedGate } from '../../utils/actions'
  import { volumeTotals } from '../../utils/snapshot'
  import { hostLabel } from '../../utils/host'

  defineOptions({ name: 'DockerVolumesTab' })

  const props = defineProps<{
    /** 页面级主机清单（resources.vue 的 useHostList，三 tab 共用）。 */
    hosts: DockerHostItem[]
    /** 主机清单在拉：首拉期间不能先喊「没有」（那时还不知道有没有主机）。 */
    hostsLoading: boolean
  }>()

  const route = useRoute()
  const { hasAuth } = useAuth()

  const showSearchBar = ref(false)
  const searchForm = ref<{ keyword?: string; unusedOnly?: boolean; host?: string }>({})

  const canDelete = computed(() => hasAuth(PermDockerDelete))
  const canExec = computed(() => hasAuth(PermDockerExec))

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
  } = useResourceList<DockerVolumeListItem>({
    fetcher: (p) =>
      fetchDockerVolumes({
        hostId: p.hostId || undefined,
        keyword: p.keyword || undefined,
        // 开关只发 true（「未被使用」是单侧开关）；false 侧语义留给端点。
        unused: p.unused ? true : undefined
      }),
    // UI 的 key（unusedOnly）在这里映射成端点参数（unused）—— 不让筛选项的 key
    // 泄漏进数据层（与容器页同一句分工）。
    params: () => ({
      hostId: searchForm.value.host,
      keyword: searchForm.value.keyword,
      unused: searchForm.value.unusedOnly === true ? true : undefined
    }),
    hosts: () => props.hosts,
    hostsLoading: () => props.hostsLoading
  })

  /** 表格的加载态：清单在拉，或主机清单还没到。 */
  const listLoading = computed(() => loading.value || props.hostsLoading)

  /** 有命中行就渲染表格；纯加载中也用表格的 loading 遮罩，空态只在加载结束后判断。 */
  const showTable = computed(() => listLoading.value || rows.value.length > 0)

  // `/docker/resources?host=` 深链兼容（总览磁盘面板的既有链路）：query 里的主机
  // 作为**主机筛选初始值** —— 单向（用户改筛选不回写 query）。watch 同时兜住
  // worktab 缓存实例的「带 host 返回」场景。
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
      placeholder: '数据卷名',
      clearable: true
    },
    { key: 'unusedOnly', label: '未被使用', type: 'switch' },
    {
      // 主机维度从页面级上下文（HostSwitcher）降为筛选项：全部主机 + 各台。
      // filterable：主机多了要能敲名字找（QA 实测不可搜），与容器/镜像/网络/项目页同款。
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
      Boolean(searchForm.value.unusedOnly) ||
      Boolean(searchForm.value.host)
  )

  /** 底栏合计：与表格里的行同源（服务端过滤后的这批；截断口径见 header 计数）。 */
  const totals = computed(() => volumeTotals(rows.value))

  /** 补充结论：有未知用量/有未使用才出现，避免底栏常驻一串 0。 */
  const totalsNote = computed(() => {
    const notes: string[] = []
    if (totals.value.unknownSizeCount) notes.push(`· ${totals.value.unknownSizeCount} 个大小未知`)
    if (totals.value.unusedCount) notes.push(`· ${totals.value.unusedCount} 个未使用`)
    return notes.join(' ')
  })

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
   * 保护列的结论句（受保护是「动手前必须看见」的事实，spec §11.1 草图的 🔒）：
   * agent 算好的 protected 结论 + 本账号能否操作合在一处显示 ——
   * 没有强制权限时禁用的删除项在菜单里，用户需要在这里看到「为什么」。
   * 标记用锁图标 + 文字（不用 🔒 emoji：无 emoji 字体的环境里是豆腐块）；
   * formatter 产出的 vnode 在 ArtTable 的渲染上下文里取不到本组件的 scope id，
   * 布局类走 :global（与 workload-table 的 wkl-lock 同一手法）。
   */
  function protectionText(row: DockerVolumeListItem) {
    if (row.protected !== true) return '—'
    const gate = protectedGate(row, canExec.value)
    return h('span', { class: 'dv-lock' }, [
      h(ArtSvgIcon, { icon: 'ri:lock-2-line' }),
      gate.allowed ? '受保护' : gate.conclusion
    ])
  }

  // ── 主机来源（跨主机表的写操作按发起时锁定的主机派发）───────────────

  /**
   * 无行目标的整体动作（清理未使用卷）面向哪台主机：主机筛选选定的那台；
   * 没选且全场只有一台可管主机时就是它；否则留空 —— 按钮禁用，结论句由
   * 按钮旁的 hint 给出。行内删除不吃它（行自带 hostId）。
   */
  const targetHostId = computed(() => {
    if (searchForm.value.host) return searchForm.value.host
    return props.hosts.length === 1 ? props.hosts[0]!.id : ''
  })

  /** 指令通道的主机来源：行目标动作在**入口处锁定到该行的 hostId**（一次操作属于发起时锁定的那台主机）。 */
  const cmdHostId = ref('')
  const { run, pendingId, busy } = useDockerCmds({
    hostId: () => cmdHostId.value,
    refresh: refreshSilent
  })

  // ── 行内删除（标准确认档；受保护卷的额外要求见 action-menu/action-confirm）──

  interface ConfirmState {
    visible: boolean
    kind: 'row' | 'prune'
    action: string
    target: string
    /** 目标是否受保护（快照里 agent 算好的结论，前端不重复判断）。 */
    protected: boolean
  }

  const confirmState = ref<ConfirmState>({
    visible: false,
    kind: 'row',
    action: '',
    target: '',
    protected: false
  })
  const confirmLoading = ref(false)

  /** ⋯ 菜单选中：删除走确认弹窗（受保护时弹窗里多出「强制操作」开关），主机取行主机。 */
  function onRowMenu(row: DockerVolumeListItem, item: { action: string }) {
    if (item.action !== 'volume:remove') return
    cmdHostId.value = row.hostId
    confirmState.value = {
      visible: true,
      kind: 'row',
      action: 'volume:remove',
      target: row.name,
      protected: row.protected === true
    }
  }

  /** 清理未使用卷：强档（组件按注册表给逐字 DELETE 形态），没有 options 变体。 */
  function openPrune() {
    // 整体动作：主机在发起时锁定为筛选选定的那台（受理后不再随筛选变化）。
    cmdHostId.value = targetHostId.value
    confirmState.value = {
      visible: true,
      kind: 'prune',
      action: 'volume:prune',
      target: '',
      protected: false
    }
  }

  /** 确认弹窗提交：force 只在「强制操作」开关出现且被勾选时为 true。 */
  async function onConfirmSubmit(payload: { confirm: string; force: boolean }) {
    const st = confirmState.value
    const options: Record<string, unknown> = {}
    if (st.kind === 'row' && payload.force) options.force = true
    confirmLoading.value = true
    try {
      const res = await run({
        action: st.action,
        target: st.target || undefined,
        options,
        confirm: payload.confirm
      })
      if (res.ok) ElMessage.success(res.detail || '操作已完成')
      else ElMessage.error(runErrorMessage(res, '操作未完成'))
    } finally {
      confirmLoading.value = false
      confirmState.value = { ...st, visible: false }
    }
  }

  // ── 表格列 ──

  const columns = computed<ColumnOption<DockerVolumeListItem>[]>(() => {
    // 显式建立依赖：行内菜单的禁用态与保护结论来自这些信号，而 formatter 要到表格
    // 渲染时才执行 —— 不在这里读一次，列配置就不会随它们变化而重算（表格会停在旧状态）。
    void pendingId.value
    void busy.value
    void canExec.value
    // 列优先级：手机横屏（<768）只留「名称 / 使用 / 操作」，序号让位；
    // 平板竖屏（>=768）补上主机（跨主机表的行归属）与安全标记（保护）——「使用」是
    // 卷的状态与回收决策依据；驱动与大小是摘要元数据（合计在底栏），桌面（>=1024）
    // 才展示。数据列一律 minWidth，固定宽度只留给 index/操作这类结构性列。
    return [
      { type: 'index' as const, width: 60, label: '序号', hideBelow: 'tablet' },
      { prop: 'name', label: '名称', minWidth: 240, showOverflowTooltip: true },
      {
        // 主机列是跨主机表的核心新增：同名卷可能散在多台机器，排查时
        // 「先定位在哪台」是第一句（与容器统一表同一档：平板竖屏起可见）。
        prop: 'hostname',
        label: '主机',
        minWidth: 120,
        showOverflowTooltip: true,
        hideBelow: 'tablet',
        formatter: (row: DockerVolumeListItem) => row.hostname || row.hostId
      },
      {
        prop: 'driver',
        label: '驱动',
        minWidth: 120,
        // 驱动是存储后端的分类摘要，非排查首看，桌面才展示。
        hideBelow: 'desktop',
        formatter: (row: DockerVolumeListItem) => row.driver || '—'
      },
      {
        prop: 'sizeMb',
        label: '大小',
        minWidth: 120,
        // 大小是元数据（合计在底栏，未知用量另有计数），桌面才展示。
        hideBelow: 'desktop',
        // 未知用量显示「—」而不是 0：0 与「量不出来」在「空间去哪了」上是相反的结论。
        formatter: (row: DockerVolumeListItem) =>
          row.sizeMb === undefined || row.sizeMb === null ? '—' : formatByUnit('MB', row.sizeMb)
      },
      {
        prop: 'inUse',
        label: '使用',
        minWidth: 200,
        formatter: (row: DockerVolumeListItem) =>
          row.inUse ? `被 ${(row.mountedBy ?? []).join('、') || '容器'} 挂载` : '未使用'
      },
      {
        prop: 'protected',
        label: '保护',
        minWidth: 180,
        // 「动手前必须看见」的安全标记（与容器页同口径）：小屏横屏让位后，
        // 结论仍会出现在 ⋯ 菜单（禁用条目）与确认弹窗里。
        hideBelow: 'tablet',
        formatter: (row: DockerVolumeListItem) => protectionText(row)
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
              formatter: (row: DockerVolumeListItem) =>
                h(DockerActionMenu, {
                  actions: ['volume:remove'],
                  target: row.name,
                  protected: row.protected === true,
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

  // 「清理未使用卷…」等 plain danger 按钮的对比度 AA（浅色实测 2.87:1）：
  // EP 默认配色的病灶与处方见 overview-tokens 的 danger-plain-aa。
  @include t.danger-plain-aa;

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

  // 保护列的锁 + 文字（图标与文字一条基线 —— 取代旧 🔒 emoji 的豆腐块风险；
  // formatter vnode 在 ArtTable 的渲染上下文里取不到 scope id，走 :global）。
  :global(.dv-lock) {
    display: inline-flex;
    gap: 4px;
    align-items: center;
  }

  // 静默刷新失败标注：琥珀即「需要注意」（与模块内陈旧/离线标注同一套颜色语言）。
  .docker-refresh-note {
    margin-left: 10px;
    font-size: 12px;
    color: var(--el-color-warning);
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

  // 底栏操作区：与合计同一套留白，按钮换行不挤压。
  .docker-bar {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;
    padding: 10px 16px 12px;

    &__hint {
      color: var(--el-text-color-secondary);
      font-size: 13px;
    }
  }
</style>
