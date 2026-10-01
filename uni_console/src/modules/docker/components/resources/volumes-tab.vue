<template>
  <!-- 单根（single-root 守卫在库：布局的 Transition 只支持单根，双根切页白屏）。
       本组件是 resources 页「数据卷」tab 的内容（7a 由原 views/volumes.vue 平移）：
       页面级的主机条/快照/四态在 views/resources.vue，这里只持有卷表自己的筛选、
       行内删除与底栏清理。 -->
  <div class="docker-volumes-tab">
    <ArtSearchBar
      v-show="showSearchBar"
      v-model="searchForm"
      :items="searchItems"
      @search="onSearch"
      @reset="onReset"
    />

    <!-- 结构对齐 DockerPage 的单列表形态：搜索栏在卡片外，表格与页脚在卡片内
         （平移前的页面走 DockerPage 的 search/table/footer 插槽，形态一致）。 -->
    <ElCard class="art-table-card" shadow="never">
      <ArtTableHeader v-model:showSearchBar="showSearchBar" :loading="loading" @refresh="refresh" />

      <!-- 两种空态分开：主机上没有数据卷 vs 筛选没命中（后者给「清除筛选」）。
           纪律与容器/镜像页一致（「没有」与「筛没了」说成一句会让人以为机器空了）；
           空态渲染在本组件、不写进 ArtTable 的 `#empty` 插槽：ArtTable 不转发该插槽
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

      <!-- 底部合计跟着筛选走（与镜像页同一取向）：底栏与表格里的行必须自洽。
           「未知」与「未使用」是两件不同的事，分开计数：前者是**量不出来**（旧版
           Docker 没给用量），后者是**没人用**（可回收的候选）。 -->
      <div class="docker-total">
        <span>合计 {{ totals.count }} 个 · {{ formatByUnit('MB', totals.totalMB) }}</span>
        <span v-if="totalsNote" class="docker-total__sub">{{ totalsNote }}</span>
      </div>

      <!-- 底栏写操作（spec §11.3 的分期矩阵）：清理未使用卷是强档（弹窗里逐字
           输入 DELETE，由组件的确认档推导）；该动作没有 options 变体，弹窗里
           不放任何勾选项。指令在途时禁用，防重复提交。 -->
      <div v-if="canDelete" class="docker-bar">
        <ElButton size="small" type="danger" plain :disabled="busy" @click="openPrune">
          清理未使用卷…
        </ElButton>
      </div>
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
   * 数据卷 tab（7a 平移自 views/volumes.vue，逻辑零改动）：
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
  import { PermDockerDelete, PermDockerExec } from '@/enums/permission'
  import { formatByUnit } from '@/modules/device/utils/display'
  import ArtSearchBar from '@/components/core/forms/art-search-bar/index.vue'
  import ArtTable from '@/components/core/tables/art-table/index.vue'
  import ArtTableHeader from '@/components/core/tables/art-table-header/index.vue'
  import DockerActionConfirm from '../action-confirm.vue'
  import DockerActionMenu from '../action-menu.vue'
  import type { ColumnOption } from '@/types/component'
  import type { DockerStateResp, DockerVolumeItem } from '../../api'
  import { runErrorMessage, useDockerCmds } from '../../composables/useDockerCmds'
  import { protectedGate } from '../../utils/actions'
  import { filterVolumes, volumeTotals } from '../../utils/snapshot'
  import { useDockerHost } from '../../utils/host-context'

  defineOptions({ name: 'DockerVolumesTab' })

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
  const searchForm = ref<{ keyword?: string; unusedOnly?: boolean }>({})

  const canDelete = computed(() => hasAuth(PermDockerDelete))
  const canExec = computed(() => hasAuth(PermDockerExec))

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
      placeholder: '数据卷名',
      clearable: true
    },
    { key: 'unusedOnly', label: '未被使用', type: 'switch' }
  ])

  const hasFilter = computed(
    () => Boolean(searchForm.value.keyword) || Boolean(searchForm.value.unusedOnly)
  )

  const filtered = computed(() => filterVolumes(props.state?.volumes ?? [], searchForm.value))

  /** 底栏合计：与表格里的行同源（筛选后合计的是筛出来的这批）。 */
  const totals = computed(() => volumeTotals(filtered.value))

  /** 补充结论：有未知用量/有未使用才出现，避免底栏常驻一串 0。 */
  const totalsNote = computed(() => {
    const notes: string[] = []
    if (totals.value.unknownSizeCount) notes.push(`· ${totals.value.unknownSizeCount} 个大小未知`)
    if (totals.value.unusedCount) notes.push(`· ${totals.value.unusedCount} 个未使用`)
    return notes.join(' ')
  })

  /** 有命中行就渲染表格；纯加载中也用表格的 loading 遮罩，空态只在加载结束后判断。 */
  const showTable = computed(() => listLoading.value || filtered.value.length > 0)

  /**
   * 保护列的结论句（受保护是「动手前必须看见」的事实，spec §11.1 草图的 🔒）：
   * agent 算好的 protected 结论 + 本账号能否操作合在一处显示 ——
   * 没有强制权限时禁用的删除项在菜单里，用户需要在这里看到「为什么」。
   */
  function protectionText(row: DockerVolumeItem): string {
    if (row.protected !== true) return '—'
    const gate = protectedGate(row, canExec.value)
    return gate.allowed ? '🔒 受保护' : `🔒 ${gate.conclusion}`
  }

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

  /** ⋯ 菜单选中：删除走确认弹窗（受保护时弹窗里多出「强制操作」开关）。 */
  function onRowMenu(row: DockerVolumeItem, item: { action: string }) {
    if (item.action !== 'volume:remove') return
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

  const columns = computed<ColumnOption<DockerVolumeItem>[]>(() => {
    // 显式建立依赖：行内菜单的禁用态与保护结论来自这些信号，而 formatter 要到表格
    // 渲染时才执行 —— 不在这里读一次，列配置就不会随它们变化而重算（表格会停在旧状态）。
    void pendingId.value
    void busy.value
    void canExec.value
    // 列优先级：手机横屏（<768）只留「名称 / 使用 / 操作」，序号让位；
    // 平板竖屏（>=768）补上安全标记（保护）——「使用」是卷的状态与回收决策依据；
    // 驱动与大小是摘要元数据（合计在底栏），桌面（>=1024）才展示。数据列一律 minWidth，
    // 固定宽度只留给 index/操作这类结构性列，见 responsive-columns.ts 的约定。
    return [
      { type: 'index' as const, width: 60, label: '序号', hideBelow: 'tablet' },
      { prop: 'name', label: '名称', minWidth: 240, showOverflowTooltip: true },
      {
        prop: 'driver',
        label: '驱动',
        minWidth: 120,
        // 驱动是存储后端的分类摘要，非排查首看，桌面才展示。
        hideBelow: 'desktop',
        formatter: (row: DockerVolumeItem) => row.driver || '—'
      },
      {
        prop: 'sizeMb',
        label: '大小',
        minWidth: 120,
        // 大小是元数据（合计在底栏，未知用量另有计数），桌面才展示。
        hideBelow: 'desktop',
        // 未知用量显示「—」而不是 0：0 与「量不出来」在「空间去哪了」上是相反的结论。
        formatter: (row: DockerVolumeItem) =>
          row.sizeMb === undefined || row.sizeMb === null ? '—' : formatByUnit('MB', row.sizeMb)
      },
      {
        prop: 'inUse',
        label: '使用',
        minWidth: 200,
        formatter: (row: DockerVolumeItem) =>
          row.inUse ? `被 ${(row.mountedBy ?? []).join('、') || '容器'} 挂载` : '未使用'
      },
      {
        prop: 'protected',
        label: '保护',
        minWidth: 180,
        // 「动手前必须看见」的安全标记（与容器页同口径）：小屏横屏让位后，
        // 结论仍会出现在 ⋯ 菜单（禁用条目）与确认弹窗里。
        hideBelow: 'tablet',
        formatter: (row: DockerVolumeItem) => protectionText(row)
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
              formatter: (row: DockerVolumeItem) =>
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
  }
</style>
