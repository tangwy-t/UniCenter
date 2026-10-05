<template>
  <!-- 单根（single-root 守卫在库：布局的 Transition 只支持单根，双根切页白屏）。 -->
  <div class="docker-containers-page art-full-height">
    <ArtSearchBar
      v-show="showSearchBar"
      v-model="searchForm"
      :items="searchItems"
      @search="onSearch"
      @reset="onReset"
    />

    <ElCard class="art-table-card" shadow="never">
      <!-- 工具条刷新（内置钮）：刷新 = 把眼前这页看新 —— 主机清单（筛选源）与
           本页清单一起重拉（reloadAll = loadHosts + load；主机可能刚入库，
           旧资源页 hero 的刷新即此语义）。:loading 语义不变：重拉走非静默档，
           表头转圈照旧。 -->
      <!-- :layout 显式去掉 columns 段（组件默认含）：本页列集合在子组件
           WorkloadTable 里，工具条拿不到列 —— 保留列设置钮只会开出空弹层
           （死控件）；其余段与组件默认一致。 -->
      <ArtTableHeader
        v-model:showSearchBar="showSearchBar"
        :loading="loading"
        layout="search,refresh,size,fullscreen,settings"
        @refresh="reloadAll"
      >
        <template #left>
          <div class="toolbar-left">
            <!-- 创建容器（4a 创建面）：从本地镜像到运行容器的入口；与启停同级
                 docker:manage 门控（无权限不渲染 —— 分期控件矩阵的取向）。
                 形态收进 ArtButtonTable 家族（布局族统一，与任务中心/自动刷新同族）；
                 语义不变：仍是创建入口、同一门控、同一 openCreate（带当前筛选主机）。
                 禁用理由写进 title（写指令在途 busy 或批量在途 batchBusy 时不可并发创建）。 -->
            <ArtButtonTable
              v-if="canManage"
              type="add"
              title="创建容器"
              :disabled="busy || batchBusy"
              @click="openCreate"
            />
            <!-- 任务中心入口（8c 整页路由）：长任务的跨主机收口页，与总览 hero 的入口
                 同一形态、同一权限档（docker:list —— GET /docker/tasks 与路由 authMark
                 同码），无权限不渲染。不挂 pending 徽标：计数要为它单独拉列表且必是
                 过期数字，进行中的数量进任务页里看（与总览 hero 同一口径）。 -->
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
            <!-- total 如实报全量、items 截 500：截断必须说出口，否则用户以为
                 「筛选完了就这么多」而漏看排在 500 名之后的行。
                 只在 pageState=ready（握有数据）时报数 —— 首拉/失败/无可管主机时
                 报「共 0 个」是假话（数据面不得伪装新鲜）。 -->
            <span v-if="pageState === 'ready'" class="docker-count">
              共 {{ total }} 个容器<template v-if="truncated">
                · 列表显示前 {{ rows.length }} 条</template
              >
            </span>
            <!-- 静默刷新失败：保留最后已知清单并说出口（与镜像/卷/网络页 的
                 docker-refresh-note 同一形态：小字、琥珀 token）。 -->
            <span v-if="refreshError" class="docker-refresh-note">刷新失败，正在显示上次结果</span>
          </div>
        </template>
      </ArtTableHeader>

      <!-- 首拉失败（还没有任何行可给）：紧凑失败态 —— 结论句 + 重试，不显示旧数据
           （旧数据不存在，错误屏就是全部事实）；全局错误 toast 由 utils/http 的
           request() 统一弹出，这里不另做。 -->
      <ElEmpty v-if="pageState === 'error'" class="docker-empty" description="容器清单获取失败">
        <ElButton size="small" @click="reloadAll">重试</ElButton>
      </ElEmpty>

      <!-- 没有可管主机（主机清单与容器计数一起指向这个结论）：「筛没了」是另一种
           事实，在 WorkloadTable 里分开说。刷新重拉两个数据源（主机可能刚入库）。 -->
      <ElEmpty v-else-if="pageState === 'empty'" class="docker-empty" description="尚无可管主机">
        <ElButton size="small" @click="reloadAll">刷新</ElButton>
      </ElEmpty>

      <!-- 正常态：表 + 批量栏。首拉 loading 不再有页面级加载卡 —— WorkloadTable 在
           无行时把 loading 遮罩画在表体上（showTable = loading || rows>0），加载态
           交给表体自身。表格与列集合在子组件（列集合守卫测试照旧从 ArtTable 断言）；
           行「详情 / 日志」是 navigation（8a 页面化）：本页只 emit，跳转由页面做
           —— 表格不认识路由。 -->
      <template v-else>
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

        <!-- 批量栏（含批量删除确认弹窗与逐行派发循环）在子组件。**留表下、不进工具条**：
             页面级常驻动作（创建/任务中心/自动刷新）进工具条；批量栏承载选中态的批量
             动作、逐行波次与进度，是列表的选中态控制台 —— 随勾选出现、贴在被它控制的
             行下面（与波次反馈的阅读位置也一致）。 -->
        <WorkloadBatchBar
          :selected="selected"
          :busy="busy"
          :refresh="refreshSilent"
          @busy-change="batchBusy = $event"
        />
      </template>
    </ElCard>

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
   * 容器列表页（切片 2 重构）：**跨主机统一工作负载表**；本波「列表页纯化」后
   * 页头（hero）整块删除，页面 = 搜索栏 + 表卡（工具条内置钮 + 计数/刷新标注 +
   * 表 + 批量栏）的纯清单形态（对齐系统管理的列表形态；页面级常驻动作进工具条、
   * 选中态批量控制台留表下）。
   *
   * 数据源从「单主机快照（useDockerHostState + 本地过滤）」换成 GET /docker/containers
   * （服务端过滤，三项 query 全部透传）—— 跨主机的表没法在单主机快照上筛。主机维度
   * 从页面级上下文（provideDockerHost + HostSwitcher）降为**筛选下拉**的一项；
   * `/docker/containers?host=` 深链照旧有效（进入时作为主机筛选初始值，总览主机卡片
   * 的既有链路不动）。写操作按**行主机**派发：指令通道的 hostId 绑定
   * 当前操作行（useDockerCmds 给了 hostId 就不碰 provide 上下文）。路由、菜单零变更。
   *
   * 8a 页面化：容器详情与创建都成了整页路由（展示面一律整页，不用抽屉）。加载态
   * 交给表体自身（WorkloadTable 的 ArtTable :loading），首拉失败走紧凑失败态
   * （ElEmpty + 重试），刷新失败保留旧数据并在工具条标注「刷新失败，正在显示上次结果」。
   */
  import { computed, ref, watch } from 'vue'
  import { useRoute, useRouter } from 'vue-router'
  import { ElButton, ElCard, ElEmpty, ElMessage } from 'element-plus'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtSearchBar from '@/components/core/forms/art-search-bar/index.vue'
  import ArtTableHeader from '@/components/core/tables/art-table-header/index.vue'
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
    refreshError,
    pageState,
    autoRefresh,
    load,
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
  /* 工具条左组：图标钮 + 计数文本 + 刷新标注一行排开（对齐系统管理的 toolbar-left）。 */
  .toolbar-left {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;
  }

  /* 次要文字对比度 AA（P2 打磨批，与总览页同款处置）：EP 默认
     --el-text-color-secondary(#909399) 对白底只有 3.08:1（QA 实测 2.97–3.08），低于
     AA 正文线 → 页面范围内把它升到 regular 档（浅色 6.1:1、暗色随主题同样达标）。
     只重定义变量值，页面内所有消费该变量的元素一起达标，不碰元素样式与布局。 */
  .docker-containers-page {
    --el-text-color-secondary: var(--el-text-color-regular);
  }

  /* 工具条计数：辅助信息，小号次要色（与镜像/卷/网络页 的 docker-count 同款）。 */
  .docker-count {
    font-size: 12px;
    color: var(--el-text-color-secondary);
  }

  /* 静默刷新失败标注：琥珀即「需要注意」（与镜像/卷/网络页 / 任务页同一套颜色语言）。 */
  .docker-refresh-note {
    margin-left: 10px;
    font-size: 12px;
    // 文字对比度 AA（收尾批）：原 el-color-warning（白底 1.85）改走 token，
    // 数字见 @styles/core/aa-text.scss。
    color: var(--aa-warning-text);
  }

  /* 卡内错误/空态：给接近表格空态的留白（与镜像/卷/网络页 的 docker-empty 同款）。 */
  .docker-empty {
    padding: 56px 0;
  }
</style>
