<template>
  <!-- 单根（single-root 守卫在库：布局的 Transition 只支持单根，双根切页白屏）。 -->
  <div class="docker-containers-page art-full-height overflow-y-auto">
    <div class="wkl-page__inner p-4 pb-8 md:p-5">
      <!-- ============ 页头：图标 + 标题 + 副标题 + 自动刷新（对齐 overview.vue）============ -->
      <div class="wkl-hero mb-4 flex flex-wrap items-center gap-3">
        <div class="wkl-hero__icon flex-cc">
          <ArtSvgIcon :icon="pageIcon" />
        </div>
        <div class="min-w-0">
          <h2 class="text-lg font-semibold text-[var(--el-text-color-primary)]">容器</h2>
          <p class="mt-0.5 flex flex-wrap items-center gap-1.5 text-xs text-g-600">
            <span
              class="live-dot inline-block h-1.5 w-1.5 rounded-full bg-success"
              :class="{ 'is-loading': loading }"
            />
            <span>{{ subtitle }}</span>
          </p>
        </div>
        <div class="ml-auto flex items-center gap-1">
          <!-- 创建容器（4a 创建面）：从本地镜像到运行容器的入口；与启停同级
               docker:manage 门控（无权限不渲染 —— 分期控件矩阵的取向）。 -->
          <ElButton
            v-if="canManage"
            size="small"
            type="primary"
            :disabled="busy || batchBusy"
            @click="openCreate"
          >
            创建容器…
          </ElButton>
          <!-- 自动刷新：容器表是轻量清单（不是总览的全量聚合），盯表的核心姿势仍是
               「开着不动」；默认关、开着时每 10 秒静默重拉（对齐 overview / 设备总览）。 -->
          <ArtButtonTable
            :icon="'ri:timer-2-line'"
            :iconClass="autoRefresh ? 'bg-theme text-white shadow-sm' : 'bg-theme/12 text-theme'"
            :title="autoRefresh ? '关闭自动刷新（每 10 秒）' : '开启自动刷新（每 10 秒）'"
            @click="autoRefresh = !autoRefresh"
          />
          <ArtButtonTable
            icon="ri:refresh-line"
            iconClass="bg-theme/12 text-theme"
            title="刷新"
            @click="refresh"
          />
        </div>
      </div>

      <!-- ============ 加载（首次，还没有任何行可给） ============ -->
      <div v-if="pageState === 'loading'" class="wkl-card wkl-state flex-cc flex-col gap-3 py-16">
        <div class="wkl-state__icon flex-cc">
          <ArtSvgIcon icon="ri:loader-4-line" class="wkl-state__spin" />
        </div>
        <div class="text-sm font-medium text-[var(--el-text-color-regular)]">
          正在拉取跨主机容器清单…
        </div>
        <div class="text-xs text-g-600">跨全部可管主机聚合，首次加载请稍候</div>
      </div>

      <!-- ============ 错误（首拉失败：结论句 + 重试，不显示旧数据） ============ -->
      <div v-else-if="pageState === 'error'" class="wkl-card wkl-state">
        <ElResult icon="error" title="容器清单获取失败" sub-title="统一清单接口暂时不可用">
          <template #extra>
            <ElButton type="primary" @click="reloadAll">重试</ElButton>
          </template>
        </ElResult>
      </div>

      <!-- ============ 空态（没有可管主机 —— 引导，不是一块空白） ============ -->
      <div v-else-if="pageState === 'empty'" class="wkl-card wkl-state">
        <ElEmpty description="尚无可管主机">
          <div class="wkl-state__hint">
            可管主机来自已安装 agent 并上报 Docker 能力信号的设备；设备入库并上报后，
            这里会列出它上面的容器。
          </div>
          <ElButton type="primary" @click="reloadAll">刷新</ElButton>
        </ElEmpty>
      </div>

      <!-- ============ 正常态：筛选 + 统一表 + 批量栏 ============ -->
      <template v-else>
        <ArtSearchBar
          v-show="showSearchBar"
          v-model="searchForm"
          :items="searchItems"
          @search="onSearch"
          @reset="onReset"
        />

        <ElCard class="art-table-card" shadow="never">
          <ArtTableHeader
            v-model:showSearchBar="showSearchBar"
            :loading="loading"
            @refresh="refresh"
          >
            <template #left>
              <!-- total 如实报全量、items 截 500：截断必须说出口，否则用户以为
                   「筛选完了就这么多」而漏看排在 500 名之后的行。 -->
              <span class="docker-count">
                共 {{ total }} 个容器<template v-if="truncated">
                  · 列表显示前 {{ rows.length }} 条</template
                >
              </span>
            </template>
          </ArtTableHeader>

          <!-- 表格 + 空态 + 行菜单在子组件（列集合守卫测试照旧从 ArtTable 断言）。 -->
          <WorkloadTable
            :rows="rows"
            :loading="loading"
            :has-filter="hasFilter"
            :pending-id="pendingId"
            :batch-busy="batchBusy"
            @menu-select="onMenuSelect"
            @open-detail="(row) => openDrawer(row, 'overview')"
            @selection-change="onSelectionChange"
            @reset-filter="onReset"
          />

          <!-- 批量栏（含批量删除确认弹窗与逐行派发循环）在子组件。 -->
          <WorkloadBatchBar
            :selected="selected"
            :busy="busy"
            :refresh="refreshSilent"
            @busy-change="batchBusy = $event"
          />
        </ElCard>
      </template>
    </div>

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

    <!-- 详情抽屉：行「详情 / 日志」的原地入口（概览 + 日志 + 终端，host 取行主机）。 -->
    <WorkloadDrawer
      v-model="drawerVisible"
      :row="drawerRow"
      :initial-tab="drawerTab"
      :refresh="refreshSilent"
    />

    <!-- 创建抽屉（4a 创建面）：跨主机表没有「当前主机」的概念 —— 主机维度交给抽屉
         自己的下拉（打开时预选筛选里的那台主机）；成功后的表重拉走 refreshSilent
         （useDockerCmds 内部：立即一次 + 落定一次，双次重拉纪律）。 -->
    <CreateContainerDrawer
      v-model="createVisible"
      :hosts="hosts"
      :initial-host-id="createHostId"
      :refresh="refreshSilent"
    />
  </div>
</template>

<script setup lang="ts">
  /**
   * 容器列表页（切片 2 重构）：**跨主机统一工作负载表**。
   *
   * 数据源从「单主机快照（useDockerHostState + 本地过滤）」换成 GET /docker/containers
   * （服务端过滤，三项 query 全部透传）—— 跨主机的表没法在单主机快照上筛。主机维度
   * 从页面级上下文（provideDockerHost + HostSwitcher）降为**筛选下拉**的一项；
   * `/docker/containers?host=` 深链照旧有效（进入时作为主机筛选初始值，总览主机卡片
   * 与详情页返回的既有链路不动）。写操作按**行主机**派发：指令通道的 hostId 绑定
   * 当前操作行（useDockerCmds 给了 hostId 就不碰 provide 上下文）。路由、菜单零变更。
   */
  import { computed, ref, watch } from 'vue'
  import { useRoute } from 'vue-router'
  import { ElButton, ElCard, ElEmpty, ElMessage, ElResult } from 'element-plus'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtSearchBar from '@/components/core/forms/art-search-bar/index.vue'
  import ArtTableHeader from '@/components/core/tables/art-table-header/index.vue'
  import { usePageIcon } from '@/hooks/core/usePageIcon'
  import { useAuth } from '@/hooks/core/useAuth'
  import { PermDockerManage } from '@/enums/permission'
  import DockerActionConfirm from '../components/action-confirm.vue'
  import CreateContainerDrawer from '../components/create-container-drawer.vue'
  import WorkloadBatchBar from '../components/workload-batch-bar.vue'
  import WorkloadDrawer from '../components/workload-drawer.vue'
  import WorkloadTable from '../components/workload-table.vue'
  import type { DockerWorkloadItem } from '../api'
  import { runErrorMessage, useDockerCmds } from '../composables/useDockerCmds'
  import { useWorkloadList } from '../composables/useWorkloadList'
  import { actionGuarded, lookupDockerAction, needsConfirm } from '../utils/actions'
  import { hostLabel } from '../utils/host'

  defineOptions({ name: 'DockerContainers' })

  const route = useRoute()
  // 页头图标与侧边栏/页签同源（取菜单图标，改「菜单管理」即同步；见 usePageIcon）
  const pageIcon = usePageIcon('ri:ship-line')

  // ── 筛选（ArtSearchBar；三项全部作为 query 发给端点，服务端过滤）──
  const showSearchBar = ref(false)
  const searchForm = ref<{ keyword?: string; state?: string; host?: string }>({})

  // 数据与四态收口在 composable（拉取/seq 守卫/截断结论/10s 自动刷新含失活停表）：
  // 参数从筛选表单**现取**（表单字段是 host，端点参数是 hostId —— 映射收在这里，
  // 不让 ArtSearchBar 的 key 泄漏进数据层），改筛选后由页面触发 load 即生效。
  const {
    rows,
    total,
    hosts,
    loading,
    truncated,
    pageState,
    subtitle,
    autoRefresh,
    load,
    refresh,
    refreshSilent,
    reloadAll
  } = useWorkloadList({
    params: () => ({
      hostId: searchForm.value.host,
      state: searchForm.value.state,
      keyword: searchForm.value.keyword
    })
  })

  const hasFilter = computed(
    () =>
      Boolean(searchForm.value.keyword) ||
      Boolean(searchForm.value.state) ||
      Boolean(searchForm.value.host)
  )

  const hostOptions = computed(() => hosts.value.map((h) => ({ label: hostLabel(h), value: h.id })))

  const searchItems = computed(() => [
    {
      key: 'keyword',
      label: '名称',
      type: 'input',
      placeholder: '容器名或镜像名',
      clearable: true
    },
    {
      // 端点只有两档（stopped = 一切非 running，与总览 KPI 同一句）；
      // 旧的五状态 select 与「仅运行中」开关都折叠进这一项。
      key: 'state',
      label: '状态',
      type: 'select',
      placeholder: '全部状态',
      clearable: true,
      options: [
        { label: '运行中', value: 'running' },
        { label: '已停止', value: 'stopped' }
      ]
    },
    {
      // 主机维度从页面级上下文（HostSwitcher）降为筛选项：全部主机 + 各台。
      key: 'host',
      label: '主机',
      type: 'select',
      placeholder: '全部主机',
      clearable: true,
      options: hostOptions.value
    }
  ])

  function onSearch() {
    // 服务端过滤：搜索按钮即重拉（无分页可重置）。
    showSearchBar.value = true
    void load()
  }

  function onReset() {
    searchForm.value = {}
    void load()
  }

  // `/docker/containers?host=` 深链兼容（总览主机卡片、详情页「返回列表」的既有链路）：
  // query 里的主机作为**主机筛选初始值** —— 单向（用户改筛选不回写 query，
  // host 从此是筛选状态而不是页面状态）。
  watch(
    () => route.query.host,
    (raw, old) => {
      const id = raw ? String(raw) : ''
      const had = searchForm.value.host ?? ''
      searchForm.value.host = id
      // immediate 首跑（old 为 undefined）必拉一次；此后仅 query 真的变化时重拉 ——
      // 没 host 深链时 id 与 had 都是 ''，靠 old 区分「首次进入」与「同值重复」。
      if (id !== had || old === undefined) void load()
    },
    { immediate: true }
  )

  // ── 单行写操作（按行主机派发）────────────────────────────
  /**
   * 指令通道的主机来源：菜单/确认流发起前锁定到目标行的 hostId（useDockerCmds 在
   * run 入口处求值，换 ref 即切主机）。同表不同机的行各发各的主机，不再有「页面
   * 当前主机」的概念。
   */
  const cmdHostId = ref('')
  const { run, pendingId, busy } = useDockerCmds({
    hostId: () => cmdHostId.value,
    refresh: refreshSilent
  })

  /** 行菜单/确认弹窗的目标行（确认提交时主机与目标都取它）。 */
  interface RowConfirmState {
    visible: boolean
    action: string
    target: string
    protected: boolean
    row: DockerWorkloadItem | null
  }

  const rowConfirm = ref<RowConfirmState>({
    visible: false,
    action: '',
    target: '',
    protected: false,
    row: null
  })
  const confirmLoading = ref(false)
  /** 批量在途（批量栏 emit 上报）：行菜单据此禁用，防与批量并发提交同一目标。 */
  const batchBusy = ref(false)

  function onMenuSelect(payload: { row: DockerWorkloadItem; key: string }) {
    const { row, key } = payload
    if (key === 'logs') {
      openDrawer(row, 'logs')
      return
    }
    if (!lookupDockerAction(key)) return
    const options = { target: row.name }
    // 走弹窗的两类情况：注册表标了确认档的（删除的普通确认），以及**受保护目标**上的
    // 受保护档动作 —— 后者不带 force 会被 agent 拒绝，「强制操作」开关就是它的输入面
    // （启停/重启本身没有确认档，普通容器依然点了直接发）。
    const needsForceInput = row.protected === true && actionGuarded(key)
    if (!needsConfirm(key, options) && !needsForceInput) {
      void runWrite(key, row, options)
      return
    }
    rowConfirm.value = {
      visible: true,
      action: key,
      target: row.name,
      protected: row.protected === true,
      row
    }
  }

  /**
   * 发一条写指令并把结论给用户。主机在入口锁定为该行的 hostId（跨主机表的写操作
   * 就是「对那台机器上的那个容器」动手）；成功后的静默重拉在 composable 里。
   */
  async function runWrite(
    action: string,
    row: DockerWorkloadItem,
    options?: Record<string, unknown>,
    confirm?: string
  ) {
    cmdHostId.value = row.hostId
    const res = await run({ action, target: row.name, options, confirm })
    if (res.ok) ElMessage.success(res.detail || '操作已完成')
    else ElMessage.error(runErrorMessage(res, '操作未完成'))
    return res
  }

  /** 确认弹窗提交：force 只在「强制操作」开关出现且被勾选时为 true。 */
  async function onConfirmSubmit(payload: { confirm: string; force: boolean }) {
    const state = rowConfirm.value
    if (!state.row) return
    const options: Record<string, unknown> = { target: state.target }
    if (payload.force) options.force = true
    confirmLoading.value = true
    try {
      await runWrite(state.action, state.row, options, payload.confirm)
    } finally {
      confirmLoading.value = false
      rowConfirm.value = { ...state, visible: false }
    }
  }

  // ── 勾选与批量（栏与弹窗在 WorkloadBatchBar）────────────────
  const selected = ref<DockerWorkloadItem[]>([])

  function onSelectionChange(rows: DockerWorkloadItem[]) {
    selected.value = rows
  }

  // ── 详情抽屉 ─────────────────────────────────────────────
  const drawerVisible = ref(false)
  const drawerRow = ref<DockerWorkloadItem | null>(null)
  const drawerTab = ref<'overview' | 'logs' | 'pty'>('overview')

  /** 打开抽屉（行「详情」按钮落概览，行菜单「日志」落日志 Tab）。 */
  function openDrawer(row: DockerWorkloadItem, tab: 'overview' | 'logs' | 'pty') {
    drawerRow.value = row
    drawerTab.value = tab
    drawerVisible.value = true
  }

  // ── 创建容器（4a 创建面）────────────────────────────────────
  // 权限与 container:start 同档（docker:manage —— 创建不删不停任何现存目标）。
  const { hasAuth } = useAuth()
  const canManage = computed(() => hasAuth(PermDockerManage))

  const createVisible = ref(false)
  /** 打开抽屉时预选的主机（= 当前筛选的那台；没筛选则抽屉自己落到第一台可用主机）。 */
  const createHostId = ref('')

  function openCreate() {
    createHostId.value = searchForm.value.host ?? ''
    createVisible.value = true
  }
</script>

<style lang="scss" scoped>
  /* 页面骨架（hero/三态/动效降级）下沉在 views/wkl-shell.scss（本模块两页共用的
   * 范式样式）；这里只留本页特有的布局。令牌出处见 overview-tokens.scss 文件头。 */
  @use './wkl-shell';
</style>
