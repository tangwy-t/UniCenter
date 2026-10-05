<template>
  <!-- 单根（single-root 守卫在库：布局的 Transition 只支持单根，双根切页白屏）。
       本页是「数据卷」整页列表（原「镜像与存储」收敛页的数据卷 tab 平移）：搜索栏在
       卡片外，动作工具条与表格在卡片内；确认弹窗随页收在同一根里。 -->
  <div class="docker-volumes-page art-full-height">
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
            <!-- 常驻动作（权限门控：不渲染 ≠ 禁用）：清理未使用卷是强档（弹窗里逐字
                 输入 DELETE，由组件的确认档推导）；该动作没有 options 变体，弹窗里
                 不放任何勾选项。跨主机表没有「当前主机」：整体动作面向**主机筛选
                 选定的那台**（没选且全场只有一台可管主机时就是它；否则禁用，禁用
                 理由写进 title —— 不设常驻提示句）。 -->
            <ArtButtonTable
              v-if="canDelete"
              icon="ri:brush-line"
              iconClass="bg-danger/12 text-danger"
              :disabled="hostActionDisabled"
              :title="pruneTitle"
              @click="openPrune"
            />

            <!-- 硬数据文本：计数（total 如实报全量、items 截 500，截断必须说出口）+
                 合计（与表格里的行同源）+ 用量补充结论（未知/未使用）+ 静默刷新失败标注。
                 「未知」与「未使用」是两件不同的事，分开计数：前者是**量不出来**，
                 后者是**没人用**（可回收的候选），有才出现，避免常驻一串 0。
                 计数/合计/补充结论只在 pageState=ready（握有数据）时报 —— 首拉/失败/
                 无可管主机时报「共 0 个」是假话（数据面不得伪装新鲜）；刷新失败标注
                 不受它管（那是「上次数据仍在」的另一种事实）。 -->
            <span v-if="pageState === 'ready'" class="docker-count">
              共 {{ total }} 个数据卷<template v-if="truncated">
                · 列表显示前 {{ rows.length }} 条</template
              >
            </span>
            <span v-if="pageState === 'ready'" class="docker-total">
              合计 {{ totals.count }} 个 · {{ formatByUnit('MB', totals.totalMB) }}
            </span>
            <span v-if="pageState === 'ready' && totalsNote" class="docker-total__sub">{{
              totalsNote
            }}</span>
            <span v-if="refreshError" class="docker-refresh-note">
              刷新失败，正在显示上次结果
            </span>
          </div>
        </template>
      </ArtTableHeader>

      <!-- 首拉失败（还没有任何行可给）：空态区里的紧凑失败态 —— 结论句 + 重试，
           不显示旧数据（失败不得伪装成新鲜）。 -->
      <ElEmpty v-if="pageState === 'error'" class="docker-empty" description="数据卷清单获取失败">
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
        <ElEmpty v-else-if="hasFilter" class="docker-empty" description="没有符合筛选条件的数据卷">
          <ElButton size="small" @click="onReset">清除筛选</ElButton>
        </ElEmpty>
        <ElEmpty v-else class="docker-empty" description="还没有数据卷" />
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
   * 数据卷列表页（由「镜像与存储」收敛页的数据卷 tab 平移为独立整页，菜单平铺）。
   *
   * 数据源 GET /docker/volumes（跨主机聚合、服务端过滤）。主机维度是**筛选下拉**
   * 的一项；行归属由主机列给出；`?host=` 深链作主机筛选初始值（单向：用户改筛选
   * 不回写 query）。
   *
   * 删除按**行主机**派发（指令通道 hostId 绑定该行）；清理未使用卷（无行目标）
   * 面向主机筛选选定的那台（没选且只有一台可管主机时就是它，否则禁用 + title 里
   * 的禁用理由）。一次操作属于发起时锁定的那台主机 —— 没有「切了主机要清什么」
   * 的重置纪律。
   */
  import { computed, h, ref, watch } from 'vue'
  import { useRoute } from 'vue-router'
  import { ElButton, ElCard, ElEmpty, ElMessage } from 'element-plus'
  import { useAuth } from '@/hooks/core/useAuth'
  import { useTableColumns } from '@/hooks/core/useTableColumns'
  import { PermDockerDelete, PermDockerExec } from '@/enums/permission'
  import { formatByUnit } from '@/modules/device/utils/display'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtSearchBar from '@/components/core/forms/art-search-bar/index.vue'
  import ArtSvgIcon from '@/components/core/base/art-svg-icon/index.vue'
  import ArtTable from '@/components/core/tables/art-table/index.vue'
  import ArtTableHeader from '@/components/core/tables/art-table-header/index.vue'
  import DockerActionConfirm from '../components/action-confirm.vue'
  import DockerActionMenu from '../components/action-menu.vue'
  import { fetchDockerVolumes, type DockerVolumeListItem } from '../api'
  import { runErrorMessage, useDockerCmds } from '../composables/useDockerCmds'
  import { useHostList, useResourceList } from '../composables/useResourceList'
  import { protectedGate } from '../utils/actions'
  import { volumeTotals } from '../utils/snapshot'
  import { hostLabel } from '../utils/host'

  defineOptions({ name: 'DockerVolumes' })

  const route = useRoute()
  const { hasAuth } = useAuth()

  const showSearchBar = ref(false)
  const searchForm = ref<{ keyword?: string; unusedOnly?: boolean; host?: string }>({})

  const canDelete = computed(() => hasAuth(PermDockerDelete))
  const canExec = computed(() => hasAuth(PermDockerExec))

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
      placeholder: '数据卷名',
      clearable: true
    },
    { key: 'unusedOnly', label: '未被使用', type: 'switch' },
    {
      // 主机维度是筛选项：全部主机 + 各台。
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

  /** 工具条合计：与表格里的行同源（服务端过滤后的这批；截断口径见计数文本）。 */
  const totals = computed(() => volumeTotals(rows.value))

  /** 补充结论：有未知用量/有未使用才出现，避免工具条常驻一串 0。 */
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
   * 工具条刷新（内置钮）：刷新 = 把眼前这页看新 —— 主机筛选源与清单一起重拉
   * （主机可能刚入库，只看清单会留下过期的筛选源；旧资源页 hero 的刷新即此语义）。
   * 清单走非静默 refresh：表头 loading 语义不变。
   */
  function refreshAll() {
    void loadHosts()
    void refresh()
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
   * 没选且全场只有一台可管主机时就是它；否则留空 —— 按钮禁用，禁用理由写进
   * title（悬浮说明），页面不设常驻提示句。行内删除不吃它（行自带 hostId）。
   */
  const targetHostId = computed(() => {
    if (searchForm.value.host) return searchForm.value.host
    return hosts.value.length === 1 ? hosts.value[0]!.id : ''
  })

  /** 清理未使用卷的禁用门槛：指令在途，或没选定主机。 */
  const hostActionDisabled = computed(() => busy.value || !targetHostId.value)

  /** 清理未使用卷的 title：禁用理由随禁用状态写进悬浮说明。 */
  const pruneTitle = computed(() => {
    if (busy.value) return '清理未使用卷…（指令在途，暂不可用）'
    if (!targetHostId.value) return '清理未使用卷…（需先在「主机」筛选中选定一台主机）'
    return '清理未使用卷…'
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

  // 列设置经 useTableColumns（对齐系统管理，列弹层可用）：工厂只在 setup 求值一次
  //（权限在会话内不变化）；行内菜单的禁用态/保护结论由 formatter 在渲染时读取
  // pendingId/busy —— 每次指令完成后 refreshSilent 重拉清单，表格随之重渲染。
  const { columns, columnChecks } = useTableColumns<DockerVolumeListItem>(() => {
    // 列优先级：手机横屏（<768）只留「名称 / 使用 / 操作」，序号让位；
    // 平板竖屏（>=768）补上主机（跨主机表的行归属）与安全标记（保护）——「使用」是
    // 卷的状态与回收决策依据；驱动与大小是摘要元数据（合计在工具条），桌面（>=1024）
    // 才展示。数据列一律 minWidth，固定宽度只留给 index/操作这类结构性列。
    return [
      { type: 'index' as const, width: 60, label: '序号', hideBelow: 'tablet' },
      { prop: 'name', label: '名称', minWidth: 240, showOverflowTooltip: true },
      {
        // 主机列是跨主机表的核心：同名卷可能散在多台机器，排查时
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
        // 大小是元数据（合计在工具条，未知用量另有计数），桌面才展示。
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
</script>

<style lang="scss" scoped>
  .docker-volumes-page {
    /* 次要文字对比度 AA（逐页口径，与收敛页同款处置）：EP 默认
       --el-text-color-secondary(#909399) 对白底只有 3.08:1，低于 AA 正文线 →
       页面范围内把它升到 regular 档（浅色 6.1:1、暗色随主题同样达标）。 */
    --el-text-color-secondary: var(--el-text-color-regular);

    // 工具条：动作钮 + 硬数据文本同排，换行不挤压。
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

    // 合计（主数字）与补充结论（未知/未使用）随工具条同排、补充结论弱化。
    .docker-total {
      font-size: 13px;
    }

    .docker-total__sub {
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

    // 保护列的锁 + 文字（图标与文字一条基线 —— 取代旧 🔒 emoji 的豆腐块风险；
    // formatter vnode 在 ArtTable 的渲染上下文里取不到 scope id，走 :global）。
    :global(.dv-lock) {
      display: inline-flex;
      gap: 4px;
      align-items: center;
    }

    // 空态渲染在本页（ArtTable 不转发 `#empty`）：给它接近表格空态的留白。
    .docker-empty {
      padding: 56px 0;
    }
  }
</style>
