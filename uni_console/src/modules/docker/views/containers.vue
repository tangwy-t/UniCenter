<template>
  <!-- ⚠ 单根包装：页面**必须只有一个根节点**（布局把页面放进 `<Transition mode="out-in">`，
       而 Transition 只支持单根元素）。二期起本页多了确认弹窗，与 projects.vue 同一处理：
       页面与弹窗收进一个根 div —— 双根会在切页时白屏（single-root.test.ts 扫描钉住）。 -->
  <div class="docker-containers-page">
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
        <ArtTableHeader v-model:showSearchBar="showSearchBar" :loading="loading" @refresh="refresh">
          <template #left>
            <span class="docker-count">共 {{ filtered.length }} 个容器</span>
          </template>
        </ArtTableHeader>

        <!-- 两种空态分开：主机上没有容器 vs 筛选没命中（后者给「清除筛选」）。
             这是 overview.vue 的既有纪律：把「没有」与「筛没了」说成一句会让人以为机器空了。
             空态渲染在本页、不写进 ArtTable 的 `#empty` 插槽：ArtTable 不转发该插槽
             （组件内部把 ElTable 的空态写死成「暂无数据」），写进去会被静默丢弃 ——
             与 overview.vue 记的 ElEmpty `#extra` 陷阱同类。
             清单还没到时也不喊「没有容器」（那时还不知道有没有主机），故 v-if 把
             主机清单的加载态一并算进来。 -->
        <ArtTable
          v-if="showTable"
          :loading="listLoading"
          :data="filtered"
          :columns="columns"
          @selection-change="onSelectionChange"
        />
        <!-- host-context.ts 的 reload 注释承诺：清单拉不到时页面显示「没有可管理的主机」 -->
        <ElEmpty
          v-else-if="!ctx.hosts.length"
          class="docker-empty"
          description="没有可管理的主机"
        />
        <ElEmpty v-else-if="hasFilter" class="docker-empty" description="没有符合筛选条件的容器">
          <ElButton size="small" @click="onReset">清除筛选</ElButton>
        </ElEmpty>
        <ElEmpty v-else class="docker-empty" description="该主机上还没有容器" />
      </template>

      <!-- 勾选栏（spec §11.1）：批量动作的入口。只有选中时才有意义，未选中时不占版面。 -->
      <template #footer>
        <div v-if="selected.length" class="docker-batch">
          <span class="docker-batch__count">已选 {{ selected.length }} 项</span>
          <ElButton
            v-if="canManage"
            size="small"
            :disabled="batchDisabled"
            @click="runBatch('container:start', selected)"
          >
            启动
          </ElButton>
          <ElButton
            v-if="canManage"
            size="small"
            :disabled="batchDisabled"
            @click="runBatch('container:stop', selected)"
          >
            停止
          </ElButton>
          <ElButton
            v-if="canManage"
            size="small"
            :disabled="batchDisabled"
            @click="runBatch('container:restart', selected)"
          >
            重启
          </ElButton>
          <ElButton
            v-if="canDelete"
            size="small"
            type="danger"
            plain
            :disabled="batchDisabled"
            @click="openBatchRemove"
          >
            删除…
          </ElButton>
        </div>
      </template>
    </DockerPage>

    <!-- 单个操作的确认弹窗：删除是标准档必走它；受保护目标上的写动作也走它 ——
         「强制操作」开关是它唯一的输入面（开关是否渲染由组件按 docker:exec 权限判定，
         页面只传目标事实）。 -->
    <DockerActionConfirm
      v-model="rowConfirm.visible"
      :action="rowConfirm.action"
      :target="rowConfirm.target"
      :target-protected="rowConfirm.protected"
      target-kind="容器"
      :loading="confirmLoading"
      @confirm="onConfirmSubmit"
    />

    <!-- 批量删除：后端单条删除是标准档，但一次动多个目标，前端自加一道 DELETE 摩擦
         （计划 D2 / spec §11.6 —— 纯前端确认，不往协议加字段）。
         未复用 DockerActionConfirm：它的形态由动作注册表推导（container:remove 是标准档），
         表达不了「同一条动作在批量场景要更强确认」；缺口与建议见 2B 执行报告。 -->
    <ElDialog
      v-model="batchRemove.visible"
      title="删除确认"
      width="460px"
      :close-on-click-modal="false"
    >
      <div class="batch-del">
        <div class="batch-del__line">将删除以下 {{ batchRemove.rows.length }} 个容器：</div>
        <ul class="batch-del__list">
          <li v-for="row in batchRemove.rows" :key="row.id">
            <span>{{ row.name }}</span>
            <span v-if="row.protected" class="batch-del__lock">🔒</span>
          </li>
        </ul>
        <div class="batch-del__line">此操作不可恢复。输入 DELETE 以确认：</div>
        <ElInput
          v-model="batchRemove.input"
          class="batch-del__input"
          placeholder="DELETE"
          @keyup.enter="confirmBatchRemove"
        />
      </div>
      <template #footer>
        <ElButton @click="batchRemove.visible = false">取消</ElButton>
        <ElButton
          type="danger"
          :disabled="batchRemove.input !== 'DELETE'"
          @click="confirmBatchRemove"
        >
          删除
        </ElButton>
      </template>
    </ElDialog>
  </div>
</template>

<script setup lang="ts">
  import { computed, h, ref } from 'vue'
  import { useRouter } from 'vue-router'
  import { ElButton, ElDialog, ElEmpty, ElInput, ElMessage } from 'element-plus'
  import { useAuth } from '@/hooks/core/useAuth'
  import {
    PermDockerDelete,
    PermDockerExec,
    PermDockerInspect,
    PermDockerManage
  } from '@/enums/permission'
  import ArtButtonMore from '@/components/core/forms/art-button-more/index.vue'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtSearchBar from '@/components/core/forms/art-search-bar/index.vue'
  import ArtTable from '@/components/core/tables/art-table/index.vue'
  import ArtTableHeader from '@/components/core/tables/art-table-header/index.vue'
  import DockerActionConfirm from '../components/action-confirm.vue'
  import DockerPage from '../components/docker-page.vue'
  import type { ColumnOption } from '@/types/component'
  import type { DockerContainerItem } from '../api'
  import { runErrorMessage, useDockerCmds } from '../composables/useDockerCmds'
  import { useDockerHostState } from '../composables/useDockerHostState'
  import { actionGuarded, lookupDockerAction, needsConfirm, protectedGate } from '../utils/actions'
  import { containerStateText, cpuText, memText, netText, portsText } from '../utils/display'
  import { provideDockerHost } from '../utils/host-context'
  import { filterContainers } from '../utils/snapshot'

  const router = useRouter()
  // 主机上下文是**页面级** provide/inject：DockerPage 与 HostSwitcher 都用 useDockerHost() 取它，
  // 而模块里没有别的 provide 调用方 —— 页面就是这一层的提供者，故在这里 provide 并直接用其返回值。
  // 不要解构：上下文字段是 getter，解构会把 hostId 定格成进入页面时的 ''（主机清单尚未到达）。
  const ctx = provideDockerHost()
  const { hasAuth } = useAuth()

  const showSearchBar = ref(false)
  const searchForm = ref<{ keyword?: string; state?: string; runningOnly?: boolean }>({})
  /** 当前勾选的行（批量动作的输入；行对象由表格给出）。 */
  const selected = ref<DockerContainerItem[]>([])

  const canManage = computed(() => hasAuth(PermDockerManage))
  const canDelete = computed(() => hasAuth(PermDockerDelete))
  const canExec = computed(() => hasAuth(PermDockerExec))
  /** 至少有一个写权限才渲染勾选列：没有可执行的动作，勾选只是困惑（照设备页纪律）。 */
  const canWrite = computed(() => canManage.value || canDelete.value)

  // 快照与四态收口在 composable（hosts 清单、seq 守卫、主机切换后的重拉都在它里面）。
  // 主机切换 = 换一台机器：本页既有重置纪律是「清空筛选 + 清空勾选」，故挂在 onHostSwitch。
  const { state, loading, listLoading, stale, ageSeconds, neverReported, refresh } =
    useDockerHostState({
      onHostSwitch: () => {
        selected.value = []
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
      placeholder: '容器名或镜像名',
      clearable: true
    },
    {
      key: 'state',
      label: '状态',
      type: 'select',
      placeholder: '全部状态',
      clearable: true,
      options: [
        { label: '运行中', value: 'running' },
        { label: '已停止', value: 'exited' },
        { label: '已创建', value: 'created' },
        { label: '重启中', value: 'restarting' },
        { label: '已暂停', value: 'paused' }
      ]
    },
    { key: 'runningOnly', label: '仅运行中', type: 'switch' }
  ])

  const hasFilter = computed(
    () =>
      Boolean(searchForm.value.keyword) ||
      Boolean(searchForm.value.state) ||
      Boolean(searchForm.value.runningOnly)
  )

  const filtered = computed(() => filterContainers(state.value?.containers ?? [], searchForm.value))

  /** 有命中行就渲染表格；纯加载中也用表格的 loading 遮罩，空态只在加载结束后判断。 */
  const showTable = computed(() => listLoading.value || filtered.value.length > 0)

  // ── 单个操作 ──

  interface RowConfirm {
    visible: boolean
    action: string
    target: string
    /** 目标是否受保护（快照里 agent 算好的结论，前端不重复判断）。 */
    protected: boolean
  }

  const rowConfirm = ref<RowConfirm>({ visible: false, action: '', target: '', protected: false })
  const confirmLoading = ref(false)

  /** ⋯ 菜单条目：只读的「日志」+ 注册表里的写动作；标签/权限/危险色全部取自注册表。 */
  interface RowMenuItem {
    key: string
    label: string
    icon?: string
    auth?: string
    color?: string
    disabled?: boolean
  }

  function rowMenuItems(row: DockerContainerItem): RowMenuItem[] {
    // 受保护 + 无强制权限：受保护档约束的动作不可执行 —— 条目禁用并把结论写在条目上
    // （禁用的条目点不动，结论只能在看得见的地方给）。
    const blocked = !protectedGate({ protected: row.protected }, canExec.value).allowed
    const pending = pendingId.value === row.name
    const writeItem = (action: string): RowMenuItem | null => {
      const entry = lookupDockerAction(action)
      // 注册表里没有的动作不进菜单：宁可少一项，也不生成一个没权限/没确认档的裸按钮。
      if (!entry) return null
      return {
        key: action,
        label: blocked ? `🔒 ${entry.label}（需要更高权限）` : entry.label,
        icon: entry.icon,
        auth: entry.perm,
        color: entry.danger === 'normal' ? undefined : 'var(--art-danger)',
        disabled: pending || blocked || batchRunning.value
      }
    }
    return [
      { key: 'logs', label: '日志', icon: 'ri:file-list-3-line' },
      // 启动/停止按容器状态二选一（spec §11.1）；其余状态（已停止/已创建）给「启动」。
      writeItem(row.state === 'running' ? 'container:stop' : 'container:start'),
      writeItem('container:restart'),
      writeItem('container:remove')
    ].filter((item): item is RowMenuItem => item !== null)
  }

  function onRowMenu(row: DockerContainerItem, item: { key: string | number }) {
    const key = String(item.key)
    if (key === 'logs') {
      router.push({
        name: 'DockerContainerDetail',
        params: { id: row.id },
        query: { host: ctx.hostId, tab: 'logs' }
      })
      return
    }
    if (!lookupDockerAction(key)) return
    const options = { target: row.name }
    // 走弹窗的两类情况：注册表标了确认档的（删除的普通确认），以及**受保护目标**上的
    // 受保护档动作 —— 后者不带 force 会被 agent 拒绝，「强制操作」开关就是它的输入面
    // （启停/重启本身没有确认档，普通容器依然点了直接发）。
    const needsForceInput = row.protected === true && actionGuarded(key)
    if (!needsConfirm(key, options) && !needsForceInput) {
      void runWrite(key, row.name, options)
      return
    }
    rowConfirm.value = {
      visible: true,
      action: key,
      target: row.name,
      protected: row.protected === true
    }
  }

  /**
   * 发一条写指令并把结论给用户。
   *
   * 成功后的快照重拉在 composable 里（立即 + 落定各一次，见其注释）；这里只补一句
   * 结果 —— detail（agent 的结论句）优先，它比「操作已完成」具体。
   */
  async function runWrite(
    action: string,
    target: string,
    options?: Record<string, unknown>,
    confirm?: string
  ) {
    const res = await run({ action, target, options, confirm })
    if (res.ok) ElMessage.success(res.detail || '操作已完成')
    else ElMessage.error(runErrorMessage(res, '操作未完成'))
    return res
  }

  /** 确认弹窗提交：force 只在「强制操作」开关出现且被勾选时为 true。 */
  async function onConfirmSubmit(payload: { confirm: string; force: boolean }) {
    const { action, target } = rowConfirm.value
    const options: Record<string, unknown> = { target }
    if (payload.force) options.force = true
    confirmLoading.value = true
    try {
      await runWrite(action, target, options, payload.confirm)
    } finally {
      confirmLoading.value = false
      rowConfirm.value = { ...rowConfirm.value, visible: false }
    }
  }

  // ── 勾选栏批量（spec §11.1 / §11.6）──

  interface BatchFailure {
    name: string
    message: string
  }

  const batchRunning = ref(false)
  const batchDisabled = computed(() => batchRunning.value || busy.value)
  const batchRemove = ref<{ visible: boolean; input: string; rows: DockerContainerItem[] }>({
    visible: false,
    input: '',
    rows: []
  })

  function onSelectionChange(rows: DockerContainerItem[]) {
    selected.value = rows
  }

  function openBatchRemove() {
    batchRemove.value = { visible: true, input: '', rows: [...selected.value] }
  }

  function confirmBatchRemove() {
    if (batchRemove.value.input !== 'DELETE') return
    const rows = batchRemove.value.rows
    batchRemove.value = { visible: false, input: '', rows: [] }
    void runBatch('container:remove', rows)
  }

  /**
   * 批量 = 循环发指令（后端没有批量动作，照计划 D2 前端循环）。
   *
   * 逐条 await、单条失败不中断：「3 项成功、1 项失败」是可解释的结果，中途放弃会让
   * 剩下的项处于说不清的状态。固定行键 'batch'：批量期间 pendingId 不属于任何一行，
   * 界面另用 batchRunning 禁用整条栏防重复提交。
   *
   * 受保护的目标批量不发送：批量没有「强制操作」开关可勾，发出去必被拒 ——
   * 直接按保护档的结论句计入失败，不浪费一条指令，也让原因可读。
   */
  async function runBatch(action: string, rows: DockerContainerItem[]) {
    if (batchRunning.value || rows.length === 0) return
    // 复制一份：批量期间表格的勾选可能随重拉变化，正在执行的这一批不能跟着变。
    const targets = [...rows]
    // 批量跨主机是**同名不同机**的错误操作：主机一换，剩余项就不再发送。
    const hostAtStart = ctx.hostId
    batchRunning.value = true
    let done = 0
    const failures: BatchFailure[] = []
    try {
      for (const row of targets) {
        if (ctx.hostId !== hostAtStart) {
          failures.push({ name: row.name, message: '主机已切换，未执行' })
          continue
        }
        const gate = protectedGate({ protected: row.protected }, canExec.value)
        if (gate.protected) {
          failures.push({ name: row.name, message: gate.conclusion })
          continue
        }
        const res = await run({ action, target: row.name, key: 'batch' })
        if (res.ok) done += 1
        else failures.push({ name: row.name, message: runErrorMessage(res, '操作未完成') })
      }
    } finally {
      batchRunning.value = false
    }
    const message = batchSummary(done, failures)
    if (failures.length > 0) ElMessage({ type: 'warning', message, duration: 5000 })
    else ElMessage.success(message)
  }

  /** 结论句汇总：成功数 + 失败数 + 前三条失败原因（再多也读不完，余下只计数）。 */
  function batchSummary(done: number, failures: BatchFailure[]): string {
    const head = `已执行 ${done} 项`
    if (failures.length === 0) return head
    const shown = failures.slice(0, 3).map((f) => `${f.name}（${f.message}）`)
    if (failures.length > shown.length) shown.push(`另有 ${failures.length - shown.length} 项`)
    return `${head}，${failures.length} 项失败：${shown.join('；')}`
  }

  // ── 表格列 ──

  const columns = computed<ColumnOption<DockerContainerItem>[]>(() => {
    // 显式建立依赖：行内菜单的禁用态来自这些信号，而 formatter 要到表格渲染时才执行 ——
    // 不在这里读一次，列配置就不会随它们变化而重算（表格会停在旧状态）。
    void pendingId.value
    void batchRunning.value
    void canExec.value
    // 列优先级：手机横屏（<768）只留「名称 / 状态 / 操作」与勾选列，序号让位；
    // 平板竖屏（>=768）补上排查要看的事实（CPU、内存、镜像、端口、保护）；
    // 网络吞吐只在桌面（>=1024）展示。数据列一律 minWidth（宽屏按比例分摊），
    // 固定宽度只留给 selection/index/操作这类结构性列，见 responsive-columns.ts 的约定。
    return [
      // 勾选列只在有写权限时出现：没这个权限的人看到一个用不上的勾选框只会困惑。
      ...(canWrite.value ? [{ type: 'selection' as const, width: 46 }] : []),
      { type: 'index' as const, width: 60, label: '序号', hideBelow: 'tablet' },
      { prop: 'name', label: '名称', minWidth: 200, showOverflowTooltip: true },
      {
        prop: 'statusText',
        label: '状态',
        minWidth: 190,
        formatter: (row: DockerContainerItem) => containerStateText(row)
      },
      {
        prop: 'cpuPercent',
        label: 'CPU',
        minWidth: 90,
        hideBelow: 'tablet',
        formatter: (row: DockerContainerItem) => cpuText(row)
      },
      {
        prop: 'memUsageMb',
        label: '内存',
        minWidth: 170,
        hideBelow: 'tablet',
        formatter: (row: DockerContainerItem) => memText(row)
      },
      {
        prop: 'netTxBytesSec',
        label: '网络',
        minWidth: 190,
        // 吞吐是三类资源指标里最次要的（列也最宽），平板竖屏也隐藏，桌面起展示。
        hideBelow: 'desktop',
        formatter: (row: DockerContainerItem) => netText(row)
      },
      {
        prop: 'image',
        label: '镜像',
        minWidth: 180,
        showOverflowTooltip: true,
        // 版本/来源是排查身份的一部分，平板竖屏保留。
        hideBelow: 'tablet'
      },
      {
        prop: 'ports',
        label: '端口',
        minWidth: 160,
        // 连通性排查的第一线索（服务为什么进不去），平板竖屏保留。
        hideBelow: 'tablet',
        formatter: (row: DockerContainerItem) => portsText(row.ports)
      },
      {
        prop: 'protected',
        label: '保护',
        minWidth: 96,
        // 保护是「动手前必须看见」的事实（spec §11.1 草图的 🔒）：列表上给可见标记，
        // 权限不足时的结论句写在被禁用的菜单条目上（那里才是用户看得到的地方）。
        // 小屏横屏让位后，这条结论仍会出现在 ⋯ 菜单（禁用条目）与确认弹窗里。
        hideBelow: 'tablet',
        formatter: (row: DockerContainerItem) => (row.protected ? '🔒 受保护' : '—')
      },
      {
        prop: 'operation',
        label: '操作',
        width: 100,
        fixed: 'right' as const,
        formatter: (row: DockerContainerItem) =>
          // 主操作「详情」常驻图标按钮，次要操作收进 ⋯（spec §11.0：不把详情埋进下拉）。
          h('div', { class: 'flex items-center' }, [
            hasAuth(PermDockerInspect)
              ? h(ArtButtonTable, {
                  type: 'view',
                  title: '详情',
                  onClick: () =>
                    router.push({
                      name: 'DockerContainerDetail',
                      params: { id: row.id },
                      query: { host: ctx.hostId }
                    })
                })
              : null,
            h(ArtButtonMore, {
              list: rowMenuItems(row),
              onClick: (item: { key: string | number }) => onRowMenu(row, item)
            })
          ])
      }
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

  // 勾选栏：与底栏合计同一套留白，只有选中时出现。
  .docker-batch {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;
    padding: 12px 16px 0;

    &__count {
      color: var(--el-text-color-secondary);
      font-size: 13px;
    }
  }

  // 批量删除弹窗：目标清单一屏看完（多了内部滚动），用户才知道这一下动的是哪些。
  .batch-del {
    &__line {
      font-size: 13px;
      line-height: 1.6;
    }

    &__list {
      margin: 8px 0;
      padding-left: 18px;
      max-height: 180px;
      overflow: auto;
      color: var(--el-text-color-secondary);
      font-size: 13px;
    }

    &__lock {
      margin-left: 6px;
    }

    &__input {
      margin-top: 8px;
    }
  }
</style>
