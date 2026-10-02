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
               docker:manage 门控（无权限不渲染 —— 分期控件矩阵的取向）。
               形态收进 ArtButtonTable 家族（布局族统一）：此前是 primary small 的
               ElButton —— 24px 高/基础圆角 vs 两枚圆钮的 32px/6px，高度、圆角与
               视觉重量都异族（投诉截图）；本模块列表页 hero 的按钮簇只有一个家族
               （对齐总览 hero 的三枚图标钮）。语义不变：仍是创建入口、同一门控、
               同一 openCreate（带当前筛选主机）。 -->
          <ArtButtonTable
            v-if="canManage"
            type="add"
            title="创建容器"
            :disabled="busy || batchBusy"
            @click="openCreate"
          />
          <!-- 任务中心入口（移栽自被删的 docker-page 主机条，8c 整页路由）：长任务的
               跨主机收口页，与总览 hero 的入口同一形态、同一权限档（docker:list ——
               GET /docker/tasks 与 /docker/tasks 路由 authMark 同码），无权限不渲染。
               不挂 pending 徽标：计数要为它单独拉列表且必是过期数字，进行中的数量进
               任务页里看（与总览 hero 同一口径）。 -->
          <ArtButtonTable
            v-if="canList"
            icon="ri:task-line"
            iconClass="bg-theme/12 text-theme"
            title="任务中心"
            @click="goTasks"
          />
          <!-- 自动刷新：容器表是轻量清单（不是总览的全量聚合），盯表的核心姿势仍是
               「开着不动」；默认关、开着时每 10 秒静默重拉（对齐 overview / 设备总览）。 -->
          <ArtButtonTable
            :icon="'ri:timer-2-line'"
            :iconClass="autoRefresh ? 'bg-theme text-white shadow-sm' : 'bg-theme/12 text-theme'"
            :title="autoRefresh ? '关闭自动刷新（每 10 秒）' : '开启自动刷新（每 10 秒）'"
            @click="autoRefresh = !autoRefresh"
          />
          <!-- 刷新：loading 态与总览 hero 同款（图标自转 + 禁点 + 标题换「刷新中…」——
               手动刷新期间按钮自己就是进行中反馈，不再只靠 live-dot 一闪）。 -->
          <ArtButtonTable
            icon="ri:refresh-line"
            :iconClass="`bg-theme/12 text-theme${loading ? ' wkl-hero-refresh--loading' : ''}`"
            :title="loading ? '刷新中…' : '刷新'"
            :disabled="loading"
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

          <!-- 表格 + 空态 + 行菜单在子组件（列集合守卫测试照旧从 ArtTable 断言）。
               行「详情 / 日志」是 navigation（8a 页面化）：本页只 emit，跳转由页面做
               —— 表格不认识路由。 -->
          <WorkloadTable
            :rows="rows"
            :loading="loading"
            :has-filter="hasFilter"
            :pending-id="pendingId"
            :batch-busy="batchBusy"
            @menu-select="onMenuSelect"
            @open-detail="(row) => openDetail(row)"
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
   * 的既有链路不动）。写操作按**行主机**派发：指令通道的 hostId 绑定
   * 当前操作行（useDockerCmds 给了 hostId 就不碰 provide 上下文）。路由、菜单零变更。
   *
   * 8a 页面化：容器详情与创建都成了整页路由（展示面一律整页，不用抽屉）—— 本页只剩
   * 「统一的表」这一件事：`?id=` 深链与行桩（7b 的抽屉外部打开模式）**整体删除**，
   * 行「详情 / 日志」与 hero 的创建钮一律走 router.push（详情页/创建页自己有
   * inspect 兜底与主机下拉，本页不必替它们兜底）。
   */
  import { computed, ref, watch } from 'vue'
  import { useRoute, useRouter } from 'vue-router'
  import { ElButton, ElCard, ElEmpty, ElMessage, ElResult } from 'element-plus'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtSearchBar from '@/components/core/forms/art-search-bar/index.vue'
  import ArtTableHeader from '@/components/core/tables/art-table-header/index.vue'
  import { usePageIcon } from '@/hooks/core/usePageIcon'
  import { useAuth } from '@/hooks/core/useAuth'
  import { PermDockerList, PermDockerManage } from '@/enums/permission'
  import DockerActionConfirm from '../components/action-confirm.vue'
  import WorkloadBatchBar from '../components/workload-batch-bar.vue'
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
      // filterable：主机多了要能敲名字找（QA 实测不可搜），与镜像/卷/网络/项目页同款。
      key: 'host',
      label: '主机',
      type: 'select',
      placeholder: '全部主机',
      clearable: true,
      filterable: true,
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

  // `/docker/containers?host=` 深链兼容（总览主机卡片的既有链路）：query 里的主机
  // 作为**主机筛选初始值** —— 单向（用户改筛选不回写 query，host 从此是筛选状态
  // 而不是页面状态）。8a 起 ?host 不再与 ?id 结伴（详情已是独立页），host 语义不变。
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
    // 「日志」是只读入口（权限门在表格的菜单条目上，auth=docker:inspect）：8a 起它
    // 也是 navigation —— 详情页读 ?tab=logs 直接落在日志 Tab（能力没丢，只是换页）。
    if (key === 'logs') {
      openDetail(row, 'logs')
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

  // ── 详情 / 创建：整页路由（8a/8b 页面化）─────────────────────────
  // 本页不再为它们兜底：详情页自己有 inspect（名称/状态/镜像由它给出，7b 的「行桩 +
  // inspect 兜底」随页面化自然消解 —— 路由参数里就有容器 id），创建页自己有主机下拉
  // 与镜像清单。深链一律带 host：容器/创建都是主机作用域的事实。
  const router = useRouter()

  /** 详情页入口：行「详情」落概览，行菜单「日志」落日志 Tab（?tab=）。 */
  function openDetail(row: DockerWorkloadItem, tab: 'overview' | 'logs' = 'overview') {
    const query: Record<string, string> = { host: row.hostId }
    if (tab !== 'overview') query.tab = tab
    void router.push({ path: `/docker/containers/${row.id}`, query })
  }

  // ── 创建容器（4a 创建面 → 8b 整页路由）────────────────────────
  // 权限与 container:start 同档（docker:manage —— 创建不删不停任何现存目标）；
  // 入口预选当前筛选里的那台主机（跨主机表没有「当前主机」的概念，筛选就是最近的
  // 意图；没筛选时创建页自己落到第一台可用主机）。
  const { hasAuth } = useAuth()
  const canManage = computed(() => hasAuth(PermDockerManage))

  function openCreate() {
    const host = searchForm.value.host
    void router.push({
      path: '/docker/containers/create',
      query: host ? { host } : {}
    })
  }

  // ── 任务中心（移栽自被删的 docker-page 主机条；8c 整页路由）────────────
  // 权限与 /docker/tasks 路由 authMark 及 GET /docker/tasks 同档（docker:list）；
  // 入口是 navigation（任务页自己有页面态，跨主机任务史不随本页筛选收窄）。
  const canList = computed(() => hasAuth(PermDockerList))

  function goTasks() {
    void router.push({ path: '/docker/tasks' })
  }
</script>

<style lang="scss" scoped>
  /* 页面骨架（hero/三态/动效降级）下沉在 views/wkl-shell.scss（本模块两页共用的
   * 范式样式）；这里只留本页特有的布局。令牌出处见 overview-tokens.scss 文件头。 */
  @use './wkl-shell';

  /* 次要文字对比度 AA（P2 打磨批，与总览页同款处置）：EP 默认
     --el-text-color-secondary(#909399) 对白底只有 3.08:1（QA 实测 2.97–3.08），低于
     AA 正文线 → 页面范围内把它升到 regular 档（浅色 6.1:1、暗色随主题同样达标）。
     只重定义变量值，页面内所有消费该变量的元素一起达标，不碰元素样式与布局。 */
  .docker-containers-page {
    --el-text-color-secondary: var(--el-text-color-regular);

    /* hero 副标题/g-600 辅助字的对比度 AA（终审 QA D2·浅色实测 3.5:1）：
       text-g-600 的工具变量指向 --art-gray-600(#7987a1) —— 对页底 #fafbfc 3.5:1，
       低于 AA 正文线。页面范围内抬一档到 g-700（浅色 #4d5875 对页底 ≈6.8:1；
       暗色 #ababba 对暗底 ≈8.9:1，随主题自适应）。只重定义工具变量值，页面内
       所有 text-g-600 文字（hero 副标题、加载提示）一起达标，页面外无副作用。 */
    --color-g-600: var(--art-gray-700);
  }

  /* 刷新钮 loading 态：图标自转（复用 wkl-shell 的 wkl-spin 关键帧 —— 同块内定义与
     引用会被 scoped 一致重命名；svg 在 ArtButtonTable 内部渲染，:deep 穿透）。 */
  .wkl-hero-refresh--loading :deep(svg) {
    animation: wkl-spin 1.1s linear infinite;
  }
</style>
