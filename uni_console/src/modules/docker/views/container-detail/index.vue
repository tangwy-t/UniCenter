<template>
  <!-- 单根（single-root 守卫在库：布局的 Transition 只支持单根，双根切页白屏）。 -->
  <div class="container-detail art-full-height overflow-y-auto">
    <div class="container-detail__inner p-4 pb-8 md:p-5">
      <!-- ============ 实体头：返回 + 名称 + 状态 + 行操作（与设备详情同一骨架）============ -->
      <div class="cd-hero">
        <div class="cd-hero__identity">
          <div class="cd-hero__title-row">
            <ArtButtonTable
              icon="ri:arrow-left-line"
              icon-class="bg-g-300/55 text-g-700"
              title="返回容器列表"
              @click="back"
            />
            <h2 class="cd-hero__title">{{ title }}</h2>
            <ElTag v-if="stateText" size="small" :type="stateTagType">{{ stateText }}</ElTag>
            <ElTag v-if="protectedFlag" size="small" type="warning">受保护</ElTag>
          </div>
          <p class="cd-hero__sub">
            <span class="cd-hero__id" :title="containerId">{{ shortId }}</span>
            <span v-if="hostLabelText" class="cd-hero__host">@ {{ hostLabelText }}</span>
          </p>
        </div>
        <div class="cd-hero__actions">
          <!-- 写操作与行菜单同一套注册表与确认档：启停按状态二选一；受保护目标统一经
               确认弹窗（在里面勾「强制操作」才带 force），无强制权限时全部禁用并给
               结论句（判定与列表页同一来源：protectedGate）。指令在途时一并禁用。 -->
          <ElButton v-if="canManage" size="small" :disabled="writeDisabled" @click="onToggle">
            {{ isRunning ? '停止' : '启动' }}
          </ElButton>
          <ElButton v-if="canManage" size="small" :disabled="writeDisabled" @click="onRestart">
            重启
          </ElButton>
          <ElButton
            v-if="canDelete"
            size="small"
            type="danger"
            plain
            :disabled="writeDisabled"
            @click="openRemove"
          >
            删除…
          </ElButton>
          <span v-if="protectedBlockedConclusion" class="cd-hero__blocked">
            {{ protectedBlockedConclusion }}
          </span>
          <ElButton size="small" :loading="loading" @click="reloadAll">刷新</ElButton>
        </div>
      </div>

      <!-- 摘要行：镜像 / 创建于 / 端口 / 保护 —— 第一屏只回答「这是什么」。
           保护标记只来自主机快照（inspect 里没有这个事实），快照未到时显示「—」
           而不是猜一个「未受保护」：猜错的代价是用户以为可以放心动它。 -->
      <div class="cd-facts">
        <div class="cd-fact">
          <span class="cd-fact__label">镜像</span>
          <span class="cd-fact__value" :title="imageText">{{ imageText }}</span>
        </div>
        <div class="cd-fact">
          <span class="cd-fact__label">创建于</span>
          <span class="cd-fact__value">{{ createdText }}</span>
        </div>
        <div class="cd-fact">
          <span class="cd-fact__label">端口</span>
          <span class="cd-fact__value" :title="portsTextValue">{{ portsTextValue }}</span>
        </div>
        <div class="cd-fact">
          <span class="cd-fact__label">保护</span>
          <span class="cd-fact__value" :class="{ 'is-warn': protectedFlag }">{{
            protectedText
          }}</span>
        </div>
      </div>

      <ElCard class="art-table-card" shadow="never">
        <ElTabs v-model="activeTab" class="cd-tabs" @tab-change="onTabChange">
          <!-- 概览：container:inspect 读取 + 实时 stats 曲线（五期监控面）。inspect
               与环境 Tab 共用同一次读取（同一份事实不说两种话）。 -->
          <ElTabPane label="概览" name="overview">
            <!-- 实时统计：独立于 inspect 的流通道（inspect 失败不该连累曲线）。Tab
                 激活才挂（v-if），切走/离开页面即卸载 → 组件自己 abort 断流；:key
                 随主机/容器/运行态换（换了就要新会话）。权限与日志同档
                 （docker:inspect），无权限不渲染（spec §11.0）。 -->
            <ContainerStats
              v-if="canInspect && activeTab === 'overview'"
              :key="`${ctx.hostId}:${containerId}:${isRunning}`"
              :host-id="ctx.hostId"
              :container-id="containerId"
              :running="isRunning"
              :snapshot="statsSnapshot"
            />
            <ElSkeleton v-if="loading && !view" :rows="5" animated />
            <ElEmpty v-else-if="errorText" :description="errorText">
              <ElButton size="small" @click="loadInspect">重试</ElButton>
            </ElEmpty>
            <template v-else-if="view">
              <ElDescriptions :column="descriptionColumns" border size="small">
                <ElDescriptionsItem label="状态">{{ stateText || '—' }}</ElDescriptionsItem>
                <ElDescriptionsItem label="镜像">{{ imageText }}</ElDescriptionsItem>
                <ElDescriptionsItem label="创建时刻">{{ createdText }}</ElDescriptionsItem>
                <ElDescriptionsItem label="启动时刻">{{ startedText }}</ElDescriptionsItem>
                <ElDescriptionsItem label="重启策略">{{
                  view.restartPolicy || '—'
                }}</ElDescriptionsItem>
                <!-- 健康检查只在运行中才有意义（停止的容器没有「现在健不健康」）。 -->
                <ElDescriptionsItem v-if="healthText" label="健康">{{
                  healthText
                }}</ElDescriptionsItem>
                <ElDescriptionsItem label="端口">{{ portsTextValue }}</ElDescriptionsItem>
                <ElDescriptionsItem label="网络">{{ networksText }}</ElDescriptionsItem>
              </ElDescriptions>

              <div class="cd-block">
                <div class="cd-block__title">挂载</div>
                <ul v-if="mounts.length" class="cd-mounts">
                  <li v-for="(m, i) in mounts" :key="i" class="cd-mounts__item">
                    <span class="cd-mounts__type">{{ m.type || '挂载' }}</span>
                    <code class="cd-mounts__path">{{ mountText(m) }}</code>
                  </li>
                </ul>
                <ElEmpty v-else description="这个容器没有挂载" />
              </div>
            </template>
          </ElTabPane>

          <!-- 日志：一次性拉取（行数下拉）+ Follow 流。首次切到本 Tab 才拉一次，
              ?tab=logs 直接进来也算。 -->
          <ElTabPane label="日志" name="logs">
            <div class="cd-logs__bar">
              <span class="cd-logs__label">行数</span>
              <ElSelect
                v-model="tail"
                class="cd-logs__tail"
                size="small"
                :disabled="logsLoading || logsFollowing"
              >
                <ElOption :value="100" label="最近 100 行" />
                <ElOption :value="500" label="最近 500 行" />
                <ElOption :value="2000" label="最近 2000 行" />
              </ElSelect>
              <ElButton
                size="small"
                :loading="logsLoading"
                :disabled="logsFollowing"
                @click="loadLogs"
              >
                拉取
              </ElButton>
              <!-- 跟随中改行数不会重开会话，故行数/拉取都在跟随期间锁住（跟随开关本身不受限）。 -->
              <span v-if="logsError" class="cd-logs__error">{{ logsError }}</span>
              <span v-else-if="logsNote" class="cd-logs__note">{{ logsNote }}</span>
            </div>
            <!-- 日志正文交给查看器：缓冲上限、查找、复制、下载、上滚自动暂停、跟随都在它里面。 -->
            <LogViewer
              v-model:following="logsFollowing"
              :lines="logLines"
              :truncated="logTruncated"
              :followable="canInspect"
              :total-lines="logTotal"
            />
          </ElTabPane>

          <!-- 终端是三期能力：仅具备执行权限时渲染（不渲染 ≠ 禁用，spec §11.0）。 -->
          <ElTabPane v-if="canExec" label="终端" name="pty">
            <!-- 首次切到本 Tab 才建立会话（v-if 挂 activeTab）：进详情页不会顺手起 shell；
                 切走/离开页面即卸载 → 组件在卸载时发 cancel 并释放会话。 -->
            <PtyTerminal
              v-if="activeTab === 'pty'"
              :key="`${ctx.hostId}:${containerId}`"
              :host-id="ctx.hostId"
              :container-id="containerId"
            />
          </ElTabPane>

          <!-- 环境与启动配置（7b 从环境变量页平移、8a 随抽屉整页化）：明文环境变量/
               标签 + 入口点/命令等宽块，复用同一次 inspect。展示口径原样：不做掩码
               —— 掩码会让「这个变量到底配了什么」无从核对（管理员视图的取舍），风险
               提示用一行 Alert 说清楚。 -->
          <ElTabPane label="环境" name="env">
            <ElAlert
              class="cd-env__notice"
              type="warning"
              :closable="false"
              show-icon
              title="环境变量常含口令类变量，本页仅对具备查看权限的用户可见"
            />
            <ElSkeleton v-if="loading && !view" :rows="5" animated />
            <ElEmpty v-else-if="errorText" :description="errorText">
              <ElButton size="small" @click="loadInspect">重试</ElButton>
            </ElEmpty>
            <template v-else-if="view">
              <div class="cd-block">
                <div class="cd-block__title">环境变量（{{ envRows.length }} 项）</div>
                <ArtTable v-if="envRows.length" :data="envRows" :columns="envColumns" />
                <ElEmpty v-else description="这个容器没有环境变量" />
              </div>

              <div class="cd-block">
                <div class="cd-block__title">标签（{{ labelRows.length }} 项）</div>
                <ArtTable v-if="labelRows.length" :data="labelRows" :columns="labelColumns" />
                <ElEmpty v-else description="这个容器没有标签" />
              </div>

              <div class="cd-block">
                <div class="cd-block__title">入口点与命令</div>
                <div class="cd-code">
                  <span class="cd-code__label">入口点</span>
                  <code class="cd-code__value">{{ joinArgs(view.entrypoint) }}</code>
                </div>
                <div class="cd-code">
                  <span class="cd-code__label">命令</span>
                  <code class="cd-code__value">{{ joinArgs(view.cmd) }}</code>
                </div>
              </div>
            </template>
          </ElTabPane>
        </ElTabs>
      </ElCard>
    </div>

    <!-- 写操作的确认弹窗：删除是标准档必走它；受保护目标上的启停/重启也走它 ——
         「强制操作」开关是这个弹窗唯一的输入面（开关是否渲染由组件按 docker:exec
         权限判定，页面只传目标事实）。删除成功 → 回列表页（列表挂载时自己拉最新快照）。 -->
    <DockerActionConfirm
      v-model="writeConfirmVisible"
      :action="writeConfirmAction"
      :target="actionTarget"
      :target-protected="protectedFlag"
      target-kind="容器"
      :loading="writeConfirmLoading"
      @confirm="onWriteConfirm"
    />
  </div>
</template>

<script setup lang="ts">
  /**
   * 容器详情页（8a 页面化，`/docker/containers/:id`）。
   *
   * 用户硬性设计原则：展示面一律整页路由、不用抽屉（多分辨率适配）—— 被删的
   * workload-drawer 四个 Tab（概览 + stats 曲线 / 日志 / 终端 / 环境）**原样**平移
   * 到这里，子组件（container-stats / stats-chart / log-viewer / pty-terminal）
   * 一个不改，只有宿主从抽屉换成页面。整页化顺带解掉抽屉时代的两处结构性限制：
   *   - 深链不再需要「行桩」（抽屉没有自己的 URL，靠 `?id=` + inspect 兜底填充名称/
   *     状态/镜像；页面化的容器 id 就在路由参数里，主机在 query，其余一律由本页的
   *     inspect + 快照给出）；
   *   - 布局不再受抽屉宽度限制（终端/日志本来就是被抽屉挤得最狠的两块）。
   *
   * 主机跟随（照抄旧 container-detail 页的既有纪律，落点是本模块的收口 composable）：
   * query host 是事实源（provideDockerHost），hostId 落定/切换 → 断跟随流 + 重读
   * inspect 与快照（useDockerHostState 的 onHostReady 钩子）。**不要解构 ctx**：
   * 上下文字段是 getter，解构会把 hostId 定格成进入页面时的 ''（主机清单尚未到达）。
   *
   * 两条与抽屉不同的纪律，都是「页面化」的必然结果：
   *   ① 写指令成功后要重读**本页**的 inspect（QA：抽屉版 watch 只看 id/host，
   *      在抽屉里点完停止，抽屉自己的状态停在旧值上 —— 页面不能带着旧状态往下走）。
   *      重读挂在 useDockerCmds 的 refresh 上（立即 + 落定各一次），与旧详情页的
   *      reloadAll 同源；
   *   ② Tab 进 URL（`?tab=logs|pty|env`，概览是默认屏故不占位）：刷新/分享回到同一
   *      屏，行菜单的「日志」入口也靠它直接落在日志 Tab（这是被删抽屉 initialTab
   *      的整页等价物）。
   */
  import { computed, defineAsyncComponent, onBeforeUnmount, ref, watch } from 'vue'
  import { useRoute, useRouter } from 'vue-router'
  import {
    ElAlert,
    ElButton,
    ElCard,
    ElDescriptions,
    ElDescriptionsItem,
    ElEmpty,
    ElMessage,
    ElOption,
    ElSelect,
    ElSkeleton,
    ElTabPane,
    ElTabs,
    ElTag
  } from 'element-plus'
  import {
    PermDockerDelete,
    PermDockerExec,
    PermDockerInspect,
    PermDockerManage
  } from '@/enums/permission'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtTable from '@/components/core/tables/art-table/index.vue'
  import { useAppBreakpoints } from '@/hooks/core/useAppBreakpoints'
  import { useAuth } from '@/hooks/core/useAuth'
  import { formatUnixSeconds } from '@/modules/device/utils/display'
  import DockerActionConfirm from '../../components/action-confirm.vue'
  import ContainerStats from '../../components/container-stats.vue'
  import LogViewer from '../../components/log-viewer.vue'
  import {
    fetchDockerCmdResult,
    openDockerLogStream,
    sendDockerCmd,
    type DockerPortItem,
    type DockerWorkloadItem
  } from '../../api'
  import { runErrorMessage, useDockerCmds } from '../../composables/useDockerCmds'
  import { useDockerHostState } from '../../composables/useDockerHostState'
  import { actionGuarded, needsConfirm, protectedGate } from '../../utils/actions'
  import {
    parseContainerInspectPayload,
    parseLogsPayload,
    pollDelay,
    type ContainerInspectView,
    type Phase1Action
  } from '../../utils/cmd'
  import { containerStateText, portsText } from '../../utils/display'
  import { hostLabel } from '../../utils/host'
  import { provideDockerHost } from '../../utils/host-context'
  import { createLogFeed } from '../../utils/stream'

  defineOptions({ name: 'DockerWorkloadDetailPage' })

  type TabName = 'overview' | 'logs' | 'pty' | 'env'

  // 终端组件按需加载：xterm 的体积不小，只有真的要开终端时才把这块代码拉下来
  // （与「首次切到终端 Tab 才建立会话」是同一取向）。
  const PtyTerminal = defineAsyncComponent(() => import('../../components/pty-terminal.vue'))

  /** 详情视图：F1 的解析结果 + 端口（概览要显示端口，而解析视图里没有这一项）。 */
  interface ContainerDetailView extends ContainerInspectView {
    ports: DockerPortItem[]
  }

  const route = useRoute()
  const router = useRouter()
  // 主机上下文是**页面级** provide/inject（与列表页/工作台同一约定）：直接打开详情
  // 链接时主机从 query/清单还原，返回列表与刷新时又要把它带回去。
  const ctx = provideDockerHost()
  const { hasAuth } = useAuth()

  // 概览信息表（ElDescriptions）的列数：平板竖屏以下（<768）改单列 —— 两列在手机横屏里
  // 会把「标签 + 值」挤成一条缝，长镜像引用/网络名会被截断。
  const { smaller } = useAppBreakpoints()
  const descriptionColumns = computed(() => (smaller('tablet').value ? 1 : 2))

  // 权限在 Tab 初始化**之前**就绪：终端 Tab 是否渲染、?tab=pty 落不落到终端都看它。
  const canManage = computed(() => hasAuth(PermDockerManage))
  const canDelete = computed(() => hasAuth(PermDockerDelete))
  const canExec = computed(() => hasAuth(PermDockerExec))
  // 日志（含 Follow）与实时统计的权限码同 docker:inspect（与路由 authMark 同档）。
  const canInspect = computed(() => hasAuth(PermDockerInspect))

  /** 路由参数里的容器 id（列表页/总览异常表/工作台容器行传的就是它，同一份口径）。 */
  const containerId = computed(() => String(route.params.id ?? ''))
  const shortId = computed(() => containerId.value.slice(0, 12))

  /** 头部行归属：主机清单未到时为空（那时还叫不出这台机器的名字，不猜）。 */
  const hostLabelText = computed(() => (ctx.host ? hostLabel(ctx.host) : ''))

  // ── 页面自身状态（**必须**声明在 useDockerHostState 注册之前）─────────
  // 顺序不是风格问题，是正确性问题：注册那一行的 immediate watch 会在**注册语句
  // 内部**就同步触发一次 onHostReady → reloadAll（query 带 host 时 hostId 在 setup
  // 期已是真值），而它调用的 loadInspect / loadLogs 都要写本页状态。凡声明在注册
  // 之后的绑定，在那一次调用里都还是 TDZ —— 首拉直接抛
  // 「Cannot access 'X' before initialization」，又因为它发生在 async 函数体里，
  // 只是一个 unhandled rejection：页面看着挂载成功，实则一次数据都没拉到
  // （2026-10-02 实测：inspectSeq / logsPulled / refreshSnapshot 三个绑定连中）。
  // 故本页所有状态与请求序号都在注册之前落地；注册之后只剩「接线」。

  // 「日志」入口带 ?tab=logs：直接落在日志 Tab，其余情况落在概览。
  const activeTab = ref<TabName>(tabFromQuery(route.query.tab))

  // ── 详情（概览与环境 Tab 共用同一次读取）──
  const view = ref<ContainerDetailView | null>(null)
  const loading = ref(false)
  const errorText = ref('')

  // ── 日志：一次性拉取（tail 下拉）+ Follow（会话制 NDJSON 流）──
  const tail = ref(100)
  const logLines = ref('')
  const logTruncated = ref(false)
  /** 跟随缓冲的累计行数（不受本地上限影响；暂停计数与「共 N 行」用）。 */
  const logTotal = ref(0)
  const logsLoading = ref(false)
  const logsError = ref('')
  /** 流动的结论句（例如「日志流已结束。」），与错误分开显示。 */
  const logsNote = ref('')
  const logsPulled = ref(false)
  const logsFollowing = ref(false)
  /** 跟随流的断开手柄：abort 即触发服务端取消会话。 */
  let logStreamAbort: AbortController | null = null
  /** 跟随的生命周期序号：断开/切主机会让在飞的「建立 + 读循环」失效。 */
  let followSeq = 0

  // 请求序号（照列表页的既有模式）：主机切换会并发两份请求，旧的那份可能**晚于**新的
  // 返回 —— 直接落盘会把页面换回上一台机器的事实。
  let inspectSeq = 0
  let logsSeq = 0

  /**
   * 快照重拉手柄：「先声明、后接线」的间接层，防的是上面同一条 TDZ 的最后一处 ——
   * 快照重拉函数是 useDockerHostState 的**解构产物**，而解构本身就是注册语句：
   * 注册期的同步首拉会在赋值落地之前调用 reloadAll，此刻直接引用解构出来的 const
   * 同样是 TDZ。故先用空操作占位，注册完立即接线，并把首拉缺的这一路补上（见下）。
   */
  let refreshSnapshot: () => void = () => {}

  // ── 主机与快照（收口在 composable：主机清单、seq 守卫、切换时机都在它里面）──
  // autoLoad: false —— 本页不消费整份快照的派生状态（陈旧文案/表格），只取「同一容器
  // 的那一行」；快照的重拉由 reloadAll 统一驱动（onHostReady 里那个动作），不再让
  // composable 自己多发一份。immediate: true 让 setup 期就有第一次 onHostReady
  // （此刻 hostId 可能还是 ''，reloadAll 里的守卫会挡住空主机）。
  const { state: snapshotState, refresh: refreshHostState } = useDockerHostState({
    host: ctx,
    autoLoad: false,
    immediate: true,
    onHostReady: () => reloadAll()
  })
  refreshSnapshot = refreshHostState
  // 接线后补上注册期那趟首拉跳过的一路（当时手柄还是空操作）；hostId 仍为空时由
  // loadState 自己的守卫挡下，不会白拉 —— 动作与 reloadAll 里那一句完全相同。
  void refreshSnapshot()

  /** 状态兜底文案：inspect 只有 state 的原始值（列表里的原生状态句在快照里）。 */
  const STATE_TEXT: Record<string, string> = {
    running: '运行中',
    created: '已创建',
    restarting: '重启中',
    paused: '已暂停',
    exited: '已停止',
    dead: '已停止'
  }

  const HEALTH_TEXT: Record<string, string> = {
    healthy: '正常',
    unhealthy: '异常',
    starting: '检查中'
  }

  const title = computed(() => view.value?.name || shortId.value || '容器详情')

  /** 同一容器在主机快照里的那一行：保护标记与原生状态句的**唯一**来源。 */
  const snapshotItem = computed(() => {
    const list = snapshotState.value?.containers ?? []
    const id = containerId.value
    const name = view.value?.name
    return list.find((c) => c.id === id || (name && c.name === name)) ?? null
  })

  /** 容器状态原始值（inspect 优先，快照兜底）。 */
  const stateRaw = computed(() => view.value?.state || snapshotItem.value?.state || '')
  const isRunning = computed(() => stateRaw.value === 'running')

  /** 镜像：快照里的引用与列表逐字一致，故优先它（同一个事实不写两种话）。 */
  const imageText = computed(() => snapshotItem.value?.image || view.value?.image || '—')

  const createdText = computed(() => formatUnixSeconds(view.value?.createdAt))
  const startedText = computed(() => formatUnixSeconds(view.value?.startedAt))

  const ports = computed(() => snapshotItem.value?.ports ?? view.value?.ports ?? [])
  const portsTextValue = computed(() => portsText(ports.value))

  const networksText = computed(() => {
    const list = view.value?.networks ?? []
    return list.length ? list.join('、') : '—'
  })

  const protectedFlag = computed(() => snapshotItem.value?.protected === true)

  /** 快照未到时是「—」：此时我们并不知道它受不受保护，猜一个是不诚实的。 */
  const protectedText = computed(() =>
    snapshotItem.value ? (protectedFlag.value ? '受保护' : '未受保护') : '—'
  )

  /**
   * 状态结论。
   *
   * - 运行中：用快照的原生状态句（"Up 16 hours"）—— 与列表逐字一致。
   * - 已停止：说结论与退出码（「已退出（退出码 0）」）—— 退出码是判因的第一条线索，
   *   而 Docker 的原生句子把它混在一串相对时间里（"Exited (0) 8 months ago"）。
   */
  const stateText = computed(() => {
    const state = stateRaw.value
    if (!state) return ''
    // 原生状态句走共用展示函数（列表页的「状态」列用的是同一个），不在这里另造一句。
    if (state === 'running' && snapshotItem.value) return containerStateText(snapshotItem.value)
    if (state === 'running') return STATE_TEXT.running
    if (state === 'exited' || state === 'dead') {
      const code = view.value?.exitCode
      return code === undefined || code === null ? STATE_TEXT[state] : `已退出（退出码 ${code}）`
    }
    return STATE_TEXT[state] ?? state
  })

  const stateTagType = computed<'success' | 'info' | 'warning'>(() => {
    if (stateRaw.value === 'running') return 'success'
    if (stateRaw.value === 'exited' || stateRaw.value === 'dead') return 'info'
    return 'warning'
  })

  /** 健康检查现状（只在运行中显示，且没配 healthcheck 时为空）。 */
  const healthText = computed(() => {
    const h = view.value?.health
    if (!isRunning.value || !h) return ''
    return HEALTH_TEXT[h] ?? h
  })

  const mounts = computed(() => view.value?.mounts ?? [])

  const envRows = computed(() => (view.value?.env ?? []).map(envPair))
  const labelRows = computed(() =>
    Object.entries(view.value?.labels ?? {}).map(([name, value]) => ({ name, value }))
  )

  // 数据列用 minWidth（口径见 components/core/tables/responsive-columns.ts）：
  // 窄屏下表格收缩到各自最小宽度后出现横向滚动，不裁掉变量名/值。
  const envColumns = [
    { prop: 'name', label: '变量名', minWidth: 260, showOverflowTooltip: true },
    { prop: 'value', label: '值', minWidth: 300, showOverflowTooltip: true }
  ]
  const labelColumns = [
    { prop: 'name', label: '标签', minWidth: 260, showOverflowTooltip: true },
    { prop: 'value', label: '值', minWidth: 300, showOverflowTooltip: true }
  ]

  /**
   * stats 组件的行快照（首帧到达前的读数占位）：形状是统一表的行
   * （DockerWorkloadItem），本页手上有的是快照 + inspect —— 在边界上拼一份。
   * 两个来源都没有时给零值而不是省略字段：零值的第一帧会被流样本立刻替换，
   * 缺字段则会在组件里读成 undefined。
   */
  const statsSnapshot = computed<DockerWorkloadItem>(() => {
    const item = snapshotItem.value
    return {
      id: containerId.value,
      name: item?.name || view.value?.name || '',
      image: item?.image || view.value?.image || '',
      state: stateRaw.value,
      statusText: item?.statusText,
      cpuPercent: item?.cpuPercent ?? 0,
      memUsageMb: item?.memUsageMb ?? 0,
      memLimitMb: item?.memLimitMb ?? 0,
      netRxBytesSec: item?.netRxBytesSec ?? 0,
      netTxBytesSec: item?.netTxBytesSec ?? 0,
      protected: protectedFlag.value,
      hostId: ctx.hostId,
      hostname: ctx.host?.hostname ?? ''
    }
  })

  /** 环境变量「名=值」拆开：值里允许再有 `=`，故只按**第一个** `=` 切。 */
  function envPair(raw: string): { name: string; value: string } {
    const i = raw.indexOf('=')
    return i < 0 ? { name: raw, value: '' } : { name: raw.slice(0, i), value: raw.slice(i + 1) }
  }

  /** 入口点/命令数组拼成一行（等宽展示），空数组给「—」。 */
  function joinArgs(args: string[] | undefined): string {
    return args && args.length ? args.join(' ') : '—'
  }

  function mountText(m: ContainerInspectView['mounts'][number]): string {
    const path = `${m.source || '—'} → ${m.destination || '—'}`
    return m.rw === false ? `${path}（只读）` : path
  }

  function tabFromQuery(raw: unknown): TabName {
    if (raw === 'logs') return 'logs'
    if (raw === 'env') return 'env'
    // 终端 Tab 只在有执行权限时存在：无权限的链接（或无权限账号）落回概览。
    if (raw === 'pty' && canExec.value) return 'pty'
    return 'overview'
  }

  /**
   * 载荷 → 视图。
   *
   * 载荷**原样来自 agent**（core 只做透传），而协议侧这些字段的 json 标签是
   * snake_case（created / started_at / finished_at / exit_code / restart_policy）。
   * F1 的解析入口按 camelCase 取那五个多词字段，故在边界上先把两种写法折成一种 ——
   * 不折的话概览里的创建时刻/启动时刻/重启策略/退出码会**静默显示为空**，而页面
   * 看不出是「这个容器没有这个值」还是「我们读错了键」。端口不在解析视图里，单独取。
   */
  function toView(payload: unknown): ContainerDetailView {
    const raw = (payload ?? {}) as Record<string, unknown>
    const base = parseContainerInspectPayload({
      ...raw,
      createdAt: raw.createdAt ?? raw.created,
      startedAt: raw.startedAt ?? raw.started_at,
      finishedAt: raw.finishedAt ?? raw.finished_at,
      exitCode: raw.exitCode ?? raw.exit_code,
      restartPolicy: raw.restartPolicy ?? raw.restart_policy
    })
    return { ...base, ports: Array.isArray(raw.ports) ? (raw.ports as DockerPortItem[]) : [] }
  }

  /**
   * 发一条只读指令并轮询到终态。
   *
   * 受理（202 + ref）与结果查询是两步：agent 是异步执行的，结果由轮询取回。节奏由
   * pollDelay 给（1 秒起指数退避、5 秒封顶）—— 轻量读取通常在 1 秒内就有结果，固定
   * 1 秒轮询会把亚秒级的读取拖成 1 秒的体感。超时给一句结论句而不是继续等。
   */
  async function runRead(
    action: Phase1Action,
    options: Record<string, unknown> = {}
  ): Promise<Awaited<ReturnType<typeof fetchDockerCmdResult>>> {
    const accepted = await sendDockerCmd(ctx.hostId, {
      action,
      target: containerId.value,
      options
    })
    let attempt = 0
    for (;;) {
      const res = await fetchDockerCmdResult(ctx.hostId, accepted.ref)
      if (res.status !== 'pending') return res
      await new Promise((r) => setTimeout(r, pollDelay(attempt++)))
      if (attempt > 20) {
        return { status: 'timeout', error: '读取超时，请稍后重试' } as Awaited<
          ReturnType<typeof fetchDockerCmdResult>
        >
      }
    }
  }

  /** 受理期失败（无权限/设备离线/参数不合法）的文案：服务端给的结论句优先。 */
  function errMsg(e: unknown, fallback: string): string {
    const msg = (e as { message?: string })?.message
    return msg && msg.trim() !== '' ? msg : fallback
  }

  /**
   * 读容器详情（概览与环境共用这一次读取）。
   *
   * 失败文案用结果里的 error：那是 agent 给出的结论句（「没有找到这个容器」这类），
   * 比前端编一句「读取失败」有用得多。静默不弹 toast —— 结论就在页面里。
   */
  async function loadInspect() {
    if (!ctx.hostId || !containerId.value) return
    const seq = ++inspectSeq
    loading.value = true
    try {
      const res = await runRead('container:inspect')
      if (seq !== inspectSeq) return
      if (res.status !== 'succeeded') {
        view.value = null
        errorText.value = res.error || '读取容器信息失败，请稍后重试'
        return
      }
      errorText.value = ''
      view.value = toView(res.payload)
    } catch (e) {
      if (seq !== inspectSeq) return
      view.value = null
      errorText.value = errMsg(e, '读取容器信息失败，请稍后重试')
    } finally {
      if (seq === inspectSeq) loading.value = false
    }
  }

  /** 拉一次日志（tail 由下拉决定）。失败同样用结果的 error。 */
  async function loadLogs() {
    if (!ctx.hostId || !containerId.value) return
    const seq = ++logsSeq
    logsLoading.value = true
    logsPulled.value = true
    try {
      const res = await runRead('container:logs', { tail: tail.value })
      if (seq !== logsSeq) return
      if (res.status !== 'succeeded') {
        logsError.value = res.error || '读取日志失败，请稍后重试'
        return
      }
      const payload = parseLogsPayload(res.payload)
      logsError.value = ''
      logsNote.value = ''
      logLines.value = payload.lines
      logTruncated.value = payload.truncated
      logTotal.value = 0
    } catch (e) {
      if (seq !== logsSeq) return
      logsError.value = errMsg(e, '读取日志失败，请稍后重试')
    } finally {
      if (seq === logsSeq) logsLoading.value = false
    }
  }

  /**
   * 开启跟随：建立日志流会话 → 接入 NDJSON 流 → 持续喂进缓冲。
   *
   * 两步与后端契约一一对应：`container:logs{follow:true}` 的结果**只有会话句柄**
   * （payload 为空），数据从 `cmds/:ref/stream` 以 NDJSON 收；这里的 AbortController
   * 一断开，服务端就会向 agent 下发取消（会话随之释放）。
   */
  async function startLogFollow() {
    if (!ctx.hostId || !containerId.value) {
      logsFollowing.value = false
      return
    }
    const seq = ++followSeq
    logsLoading.value = true
    logsError.value = ''
    logsNote.value = ''
    try {
      const accepted = await sendDockerCmd(ctx.hostId, {
        action: 'container:logs',
        target: containerId.value,
        options: { tail: tail.value, follow: true }
      })
      // 轮询到会话建立（结果只给会话句柄）；节奏与一次性拉取同一套。
      let attempt = 0
      for (;;) {
        if (seq !== followSeq) return
        const res = await fetchDockerCmdResult(ctx.hostId, accepted.ref)
        if (res.status !== 'pending') {
          if (res.status !== 'succeeded') {
            throw new Error(res.error || '日志跟随未能建立，请稍后重试')
          }
          break
        }
        await new Promise((r) => setTimeout(r, pollDelay(attempt++)))
        if (attempt > 20) throw new Error('日志跟随建立超时，请重试')
      }
      if (seq !== followSeq) return

      // 接入流之前先把缓冲清空：会话自己会从 tail 行开始发，不叠上页面上旧的日志。
      logsSeq++ // 在飞的一次性拉取（若有）作废：它的结果不该落进跟随缓冲
      const feed = createLogFeed()
      logLines.value = ''
      logTotal.value = 0
      logTruncated.value = false
      logsPulled.value = true

      const controller = new AbortController()
      logStreamAbort = controller
      const res = await openDockerLogStream(ctx.hostId, accepted.ref, controller.signal)
      if (!res.ok || !res.body) {
        throw new Error(streamOpenConclusion(res.status))
      }
      logsLoading.value = false

      const reader = res.body.getReader()
      const chunkDecoder = new TextDecoder('utf-8')
      for (;;) {
        const { done, value } = await reader.read()
        if (done || seq !== followSeq) break
        feed.pushRaw(chunkDecoder.decode(value, { stream: true }))
        logLines.value = feed.text
        logTotal.value = feed.totalLines
        logTruncated.value = feed.truncated
        if (feed.eof) break
      }
      if (seq !== followSeq) return
      // 流自然收尾（agent 发了 eof）：最后一帧可能还带着数据。
      feed.pushRaw(chunkDecoder.decode())
      logLines.value = feed.text
      logTotal.value = feed.totalLines
      logTruncated.value = feed.truncated
      logStreamAbort = null
      logsFollowing.value = false
      // eof 是流自然收尾；没有 eof 的结束是连接被中途掐断，说清区别。
      logsNote.value = feed.eof ? '日志流已结束。' : '日志流已断开。'
    } catch (e) {
      if (seq !== followSeq) return
      // 主动断开（关跟随/切 Tab/离开页面）不是错误：不弹结论句。
      if ((e as { name?: string })?.name === 'AbortError') return
      logStreamAbort = null
      logsFollowing.value = false
      logsError.value = errMsg(e, '日志跟随失败，请稍后重试')
    } finally {
      if (seq === followSeq) {
        logsLoading.value = false
        logStreamAbort = null
      }
    }
  }

  /** 流端点接入失败的结论句（状态码是排障线索，不进页面）。 */
  function streamOpenConclusion(status: number): string {
    if (status === 401) return '登录状态已失效，请重新登录后再试。'
    if (status === 403) return '没有接入该日志流的权限。'
    if (status === 404) return '该日志会话已过期，请重新跟随。'
    if (status === 409) return '该日志会话已有连接，请稍后重试。'
    return '日志跟随未能建立，请稍后重试'
  }

  /** 断开跟随（关开关/切 Tab/切主机/离开页面都会走到这里）。 */
  function stopLogFollow() {
    const controller = logStreamAbort
    // 只在真的有东西在飞时让序号失效（幂等调用——例如 eof 后开关回调再走一次——不扰动别的流程）。
    const inFlight = controller !== null || logsFollowing.value
    logStreamAbort = null
    if (!inFlight) return
    followSeq++ // 让在飞的建立流程与读循环失效
    logsLoading.value = false
    if (controller) controller.abort()
    if (logsFollowing.value) logsFollowing.value = false
  }

  /** 首次切到日志 Tab 自动拉一次；之后由「拉取」按钮驱动。 */
  function ensureLogs() {
    if (logsPulled.value || logsLoading.value || logsFollowing.value) return
    void loadLogs()
  }

  /**
   * 本页的全量重读：inspect + 快照 + 日志（不在跟随中时）。
   *
   * 它同时挂在两处：写指令成功后的 refresh（**QA 修正点**：抽屉版只重拉列表，
   * 抽屉自己的 inspect 停在旧状态；整页化后本页就是唯一表面，必须重读自己），
   * 与主机切换时的 onHostReady。跟随中不重拉一次性日志：那会整段替换流缓冲
   * （跟随按钮一关即可回到拉取模式）。
   *
   * 日志那一路的两种触发：**拉过**（重读就该回到最新）或**当下正落在日志 Tab**
   *（?tab=logs 深链的首拉时机就在这里 —— setup 期还叫不动它：query 里没带主机时
   * hostId 要等清单落定，而这次重读正是清单落定的落点；带主机的那条路径同样是
   * 注册期这次重读先发，随后的 Tab 自动拉取被 ensureLogs 的守卫挡掉，不会双拉）。
   */
  function reloadAll() {
    void loadInspect()
    void refreshSnapshot()
    if (!logsFollowing.value && (logsPulled.value || activeTab.value === 'logs')) void loadLogs()
  }

  /** 返回容器列表：带上当前主机，列表页据此还原到同一台机器。 */
  function back() {
    void router.push({ name: 'DockerContainers', query: { host: ctx.hostId } })
  }

  // ── 写操作（头部按钮区）：确认档/保护档复用列表页的同一套判定 ──
  const { run, busy } = useDockerCmds({ hostId: () => ctx.hostId, refresh: reloadAll })

  /**
   * 写操作的 target：优先快照/详情里的容器名（与列表页同一口径）。
   *
   * 名字未到时**不退回容器 id**：create/start 这类按名字寻址的动作拿到一个 id 只会
   * 换回「目标为空」的结论句，等 inspect 到达更好（按钮同时是禁用的）。
   */
  const actionTarget = computed(() => snapshotItem.value?.name || view.value?.name || '')

  /** 受保护档判定（agent 算好的结论）：受保护 + 没有强制权限 = 动作不可执行。 */
  const protectionGate = computed(() =>
    protectedGate({ protected: protectedFlag.value }, canExec.value)
  )
  const protectedBlockedConclusion = computed(() =>
    protectionGate.value.allowed ? '' : protectionGate.value.conclusion
  )
  /** 头部写按钮的禁用：指令在途、保护档不允许，或 target 还没有名字（inspect 未回来）。 */
  const writeDisabled = computed(
    () => busy.value || !protectionGate.value.allowed || !actionTarget.value
  )

  const writeConfirmVisible = ref(false)
  const writeConfirmAction = ref('')
  const writeConfirmLoading = ref(false)

  /** 该动作此刻是否必须经确认弹窗：注册表的确认档，或受保护目标的保护档。 */
  function needsDialog(action: string): boolean {
    return needsConfirm(action) || (protectedFlag.value && actionGuarded(action))
  }

  function openWriteConfirm(action: string) {
    writeConfirmAction.value = action
    writeConfirmVisible.value = true
  }

  function onToggle() {
    // 启停二选一由容器状态决定（与列表页同一口径）。
    const action = isRunning.value ? 'container:stop' : 'container:start'
    if (needsDialog(action)) openWriteConfirm(action)
    else void runWrite(action)
  }

  function onRestart() {
    if (needsDialog('container:restart')) openWriteConfirm('container:restart')
    else void runWrite('container:restart')
  }

  function openRemove() {
    // 删除是标准档：无论是否受保护都先过确认弹窗（受保护时弹窗里多出「强制操作」开关）。
    openWriteConfirm('container:remove')
  }

  /**
   * 发一条写指令并把结论给用户；成功后的重读由 composable 统一触发（refresh =
   * reloadAll，立即一次 + 落定一次）。
   *
   * options 与顶层 target 同值：协议侧 target 属于 options（core 在唯一入口把顶层
   * 并进去，但前端口径一直两处同给 —— 列表页、被删抽屉、本页的确认弹窗路径都
   * 如此），直发与经弹窗两条写路径的载荷因此逐字一致，不因走哪条路而不同。
   */
  async function runWrite(action: string) {
    const res = await run({
      action,
      target: actionTarget.value,
      options: { target: actionTarget.value }
    })
    if (res.ok) ElMessage.success(res.detail || '操作已完成')
    else ElMessage.error(runErrorMessage(res, '操作未完成'))
    return res
  }

  /** 确认弹窗提交：force 只在「强制操作」开关出现且被勾选时为 true。 */
  async function onWriteConfirm(payload: { confirm: string; force: boolean }) {
    const action = writeConfirmAction.value
    const options: Record<string, unknown> = { target: actionTarget.value }
    if (payload.force) options.force = true
    writeConfirmLoading.value = true
    try {
      const res = await run({
        action,
        target: actionTarget.value,
        options,
        confirm: payload.confirm
      })
      if (res.ok) {
        ElMessage.success(res.detail || '操作已完成')
        // 删除成功 → 回列表页：页面已经不存在了，留在原地只会给一片读不到的事实。
        if (action === 'container:remove') back()
      } else {
        ElMessage.error(runErrorMessage(res, '操作未完成'))
      }
    } finally {
      writeConfirmLoading.value = false
      writeConfirmVisible.value = false
    }
  }

  /**
   * Tab 写进 URL（与设备详情同款）：刷新与分享链接能回到同一屏，行菜单的「日志」
   * 入口也正是靠 ?tab=logs 直接落在日志 Tab。概览是默认屏，故不必在 URL 里留 tab。
   */
  function onTabChange(name: string | number) {
    const t = tabFromQuery(name)
    activeTab.value = t
    const query = { ...route.query }
    if (t === 'overview') delete query.tab
    else query.tab = t
    void router.replace({ query })
  }

  // 前进/后退键改 query 时同步 Tab（用户按后退键回到上一屏不该看到另一屏的数据）。
  watch(
    () => route.query.tab,
    (raw) => {
      activeTab.value = tabFromQuery(raw)
    }
  )

  // 换容器（同页不同 id 的深链，例如从详情里的关联项再进一台）：停跟随并重读 ——
  // 路由参数变了但组件实例可能被复用。
  watch(containerId, () => {
    stopLogFollow()
    reloadAll()
  })

  // 切到日志 Tab 就自动拉一次（?tab=logs 直接进来也算）；切走或切到终端就断开跟随
  //（「切换 Tab → AbortController 断开」，断流后开关回到可重连状态）。
  watch(activeTab, (t) => {
    if (t === 'logs') ensureLogs()
    else stopLogFollow()
  })

  // 跟随开关的翻转 = 建立/断开流。结束（eof）或失败时 startLogFollow 自己把开关拨回，
  // 于是这里再走一次 stopLogFollow（幂等）。
  watch(logsFollowing, (on) => {
    if (on) void startLogFollow()
    else stopLogFollow()
  })

  // 离开页面：断开跟随（服务端随之下发取消）。
  onBeforeUnmount(() => stopLogFollow())
</script>

<style lang="scss" scoped>
  @use '@styles/core/breakpoints.scss' as *;
  @use '../../views/overview-tokens' as t;

  // hero 的「删除…」是 plain danger：对比度 AA 的病灶与处方见 overview-tokens
  // 的 danger-plain-aa（浅色 QA 实测 2.87:1）。
  @include t.danger-plain-aa;

  /* 次要文字对比度 AA（终审 QA D2 同源盘点 · 六页批漏网页补齐）：EP 默认
     --el-text-color-secondary(#909399) 对白底只有 3.08:1，低于 AA 正文线 —— 本页
     hero 短 id/主机、元信息标签、日志口径句等 12–14px 次要文字全吃它。页面范围内
     升到 regular 档（浅色 6.1:1、暗色随主题同样达标；与其余 docker 页同款处置）。
     只重定义变量值，不碰元素样式与布局。 */
  .container-detail {
    --el-text-color-secondary: var(--el-text-color-regular);
  }

  // 实体头（与设备详情的 hero 同一骨架：左身份、右动作）。
  .cd-hero {
    display: flex;
    flex-wrap: wrap;
    gap: 12px;
    align-items: flex-start;
    justify-content: space-between;
    padding: 16px;
    background: var(--default-box-color);
    border-radius: 8px;

    &__identity {
      min-width: 0;
    }

    &__title-row {
      display: flex;
      flex-wrap: wrap;
      gap: 8px;
      align-items: center;
    }

    &__title {
      margin: 0;
      font-size: 18px;
      font-weight: 600;
      word-break: break-all;
    }

    &__sub {
      display: flex;
      flex-wrap: wrap;
      gap: 8px;
      align-items: baseline;
      margin: 6px 0 0;
      font-size: 12px;
    }

    &__id {
      color: var(--el-text-color-secondary);
      font-family: var(--el-font-family-mono, ui-monospace, 'SFMono-Regular', Consolas, monospace);
    }

    // 行归属：次要文字色，与短 id 并排（详情页的主机也是 query host 那一台）。
    &__host {
      color: var(--el-text-color-secondary);
    }

    &__actions {
      display: flex;
      flex-wrap: wrap;
      gap: 8px;
      align-items: center;
    }

    // 保护档结论句：与陈旧标注同一套颜色语言（琥珀色 = 需要注意的结论）。
    &__blocked {
      // 文字对比度 AA（收尾批）：原 el-color-warning（白底 1.85）改走 token。
      color: var(--aa-warning-text);
      font-size: 12px;
    }
  }

  // 摘要行：宽度不够就换行（窄屏下不挤压）。
  .cd-facts {
    display: flex;
    flex-wrap: wrap;
    gap: 12px 32px;
    margin: 12px 0;
    padding: 12px 16px;
    background: var(--default-box-color);
    border-radius: 8px;
  }

  .cd-fact {
    display: flex;
    gap: 8px;
    align-items: baseline;
    min-width: 0;

    &__label {
      color: var(--el-text-color-secondary);
      font-size: 12px;
    }

    &__value {
      font-size: 13px;
      word-break: break-all;
    }

    // 保护是这一屏唯一需要被「看见」的事实：琥珀色即结论（与陈旧标注同一套颜色语言）。
    &__value.is-warn {
      // 文字对比度 AA（收尾批）：原 el-color-warning 改走 token。
      color: var(--aa-warning-text);
    }
  }

  .cd-tabs {
    :deep(.el-tabs__header) {
      margin-bottom: 16px;
    }
  }

  .cd-block {
    margin-top: 16px;

    &__title {
      margin-bottom: 8px;
      font-size: 14px;
      font-weight: 600;
    }
  }

  .cd-mounts {
    margin: 0;
    padding: 0;
    list-style: none;

    &__item {
      display: flex;
      gap: 12px;
      align-items: baseline;
      padding: 6px 0;
      border-bottom: 1px solid var(--el-border-color-lighter);
      font-size: 13px;
    }

    &__type {
      flex: none;
      min-width: 56px;
      color: var(--el-text-color-secondary);
      font-size: 12px;
    }

    &__path {
      font-family: var(--el-font-family-mono, ui-monospace, 'SFMono-Regular', Consolas, monospace);
      word-break: break-all;
    }
  }

  .cd-logs__bar {
    display: flex;
    gap: 8px;
    align-items: center;
    margin-bottom: 12px;
  }

  .cd-logs__label {
    color: var(--el-text-color-secondary);
    font-size: 13px;
  }

  .cd-logs__tail {
    width: 140px;
  }

  .cd-logs__error {
    // 文字对比度 AA（收尾批）：原 el-color-danger 改走 token。
    color: var(--aa-danger-text);
    font-size: 13px;
  }

  // 流动的结论句（例如「日志流已结束」）：次要文字色，不与错误同色。
  .cd-logs__note {
    color: var(--el-text-color-secondary);
    font-size: 13px;
  }

  // 环境 Tab 的风险提示行（明文口径的告示牌，见模板注释）。
  .cd-env__notice {
    margin-bottom: 12px;
  }

  .cd-code {
    display: flex;
    gap: 12px;
    align-items: baseline;
    padding: 6px 0;
    font-size: 13px;

    &__label {
      flex: none;
      min-width: 56px;
      color: var(--el-text-color-secondary);
      font-size: 12px;
    }

    &__value {
      font-family: var(--el-font-family-mono, ui-monospace, 'SFMono-Regular', Consolas, monospace);
      word-break: break-all;
    }
  }

  /* ── 响应式 ─────────────────────────────────────── */

  // 窄屏（<1024，覆盖平板竖屏与手机横屏）：身份与动作分成上下两段 ——
  // 并排时右侧按钮组会把容器名/短 id 挤成窄条；日志工具条允许换行。
  @include respond-below('desktop') {
    .cd-hero__identity,
    .cd-hero__actions {
      width: 100%;
    }

    .cd-logs__bar {
      flex-wrap: wrap;
    }
  }

  // 手机横屏（<768）：摘要行每条占满一行，避免两条挤在一行里互相截断。
  @include respond-below('tablet') {
    .cd-hero {
      padding: 12px;
    }

    .cd-facts {
      gap: 8px 16px;
      padding: 12px;
    }

    .cd-fact {
      flex: 1 1 100%;
    }
  }
</style>
