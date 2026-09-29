<template>
  <div class="container-detail art-full-height overflow-y-auto">
    <div class="container-detail__inner p-4 pb-8 md:p-5">
      <!-- ══════════ 实体头：返回 + 名称 + 状态 + 摘要行（与设备详情同一骨架）══════════ -->
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
          </div>
          <p v-if="view" class="cd-hero__sub">
            <span class="cd-hero__id" :title="containerId">{{ containerId }}</span>
          </p>
        </div>
        <div class="cd-hero__actions">
          <!-- 二期写操作：启停按状态二选一；受保护目标统一经确认弹窗（在里面勾「强制操作」才带
               force），无强制权限时全部禁用并给结论句（判定与列表页同一来源：protectedGate）。
               指令在途时一并禁用。 -->
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

      <!-- 摘要行：镜像 / 创建于 / 端口 / 保护 —— 详情页的第一屏只回答「这是什么」。
           保护标记只来自快照（inspect 里没有这个事实），快照未到时显示「—」而不是
           猜一个「未受保护」：猜错的代价是用户以为可以放心动它。 -->
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
        <!-- 三个 Tab：概览 / 日志 / 环境变量与配置。终端是三期能力，这里连 DOM 都不出现。 -->
        <ElTabs v-model="activeTab" class="cd-tabs" @tab-change="onTabChange">
          <ElTabPane label="概览" name="overview">
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
                 切走即卸载 → 组件在卸载时发 cancel 并释放会话。 -->
            <PtyTerminal
              v-if="activeTab === 'pty'"
              :key="`${ctx.hostId}:${containerId}`"
              :host-id="ctx.hostId"
              :container-id="containerId"
            />
          </ElTabPane>

          <ElTabPane label="环境变量与配置" name="env">
            <!-- 一行结论：这里会看到什么、为什么只有部分人能看到。
                 不做掩码 —— 掩码会让「这个变量到底配了什么」无从核对（管理员视图的取舍）。 -->
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
  import DockerActionConfirm from '../components/action-confirm.vue'
  import LogViewer from '../components/log-viewer.vue'
  import {
    fetchDockerCmdResult,
    fetchDockerState,
    openDockerLogStream,
    sendDockerCmd,
    type DockerCmdResultResp,
    type DockerContainerItem,
    type DockerPortItem
  } from '../api'
  import { runErrorMessage, useDockerCmds } from '../composables/useDockerCmds'
  import { actionGuarded, needsConfirm, protectedGate } from '../utils/actions'
  import {
    parseContainerInspectPayload,
    parseLogsPayload,
    pollDelay,
    type ContainerInspectView,
    type Phase1Action
  } from '../utils/cmd'
  import { containerStateText, portsText } from '../utils/display'
  import { provideDockerHost } from '../utils/host-context'
  import { createLogFeed } from '../utils/stream'

  defineOptions({ name: 'DockerContainerDetail' })

  type TabName = 'overview' | 'logs' | 'env' | 'pty'

  // 终端组件按需加载：xterm 的体积不小，只有真的要开终端时才把这块代码拉下来
  // （与「首次切到终端 Tab 才建立会话」是同一取向）。
  const PtyTerminal = defineAsyncComponent(() => import('../components/pty-terminal.vue'))

  /** 详情视图：F1 的解析结果 + 端口（概览要显示端口，而解析视图里没有这一项）。 */
  interface ContainerDetailView extends ContainerInspectView {
    ports: DockerPortItem[]
  }

  const route = useRoute()
  const router = useRouter()
  // 主机上下文是**页面级** provide/inject（与列表页同一约定）：直接打开详情链接时主机要从
  // query/清单还原，返回列表时又要把它带回去。不要解构：上下文字段是 getter，
  // 解构会把 hostId 定格成进入页面时的 ''（主机清单尚未到达）。
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
  // 日志（含 Follow）的权限码与 container:logs 同一档：docker:inspect。
  const canInspect = computed(() => hasAuth(PermDockerInspect))

  /** 路由参数里的容器：列表页传的就是容器 id（与快照里的 id 同一份口径）。 */
  const containerId = computed(() => String(route.params.id ?? ''))

  // 列表页的「日志」入口会带 ?tab=logs：直接落在日志 Tab，其余情况落在概览。
  const activeTab = ref<TabName>(tabFromQuery(route.query.tab))

  // ── 详情（概览与环境变量 Tab 共用同一次读取）──
  const view = ref<ContainerDetailView | null>(null)
  const loading = ref(false)
  const errorText = ref('')

  // ── 同一容器在主机快照里的那一行（只用于头部摘要：保护标记与列表的状态句）──
  const snapshotItem = ref<DockerContainerItem | null>(null)

  // ── 日志：按需拉取（一期非流式）+ 跟随（三期 NDJSON 流）──
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
  /** 跟随的生命周期序号：断开/换主机会让在飞的「建立 + 读循环」失效。 */
  let followSeq = 0

  // 请求序号（照列表页的既有模式）：主机切换会并发两份请求，旧的那份可能**晚于**新的返回 ——
  // 直接落盘会把页面换回上一台机器的事实。
  let inspectSeq = 0
  let snapshotSeq = 0
  let logsSeq = 0

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

  const title = computed(() => view.value?.name || containerId.value)

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

  const isRunning = computed(() => (view.value?.state || snapshotItem.value?.state) === 'running')

  /**
   * 状态结论。
   *
   * - 运行中：用快照的原生状态句（"Up 16 hours"）—— 与列表逐字一致。
   * - 已停止：说结论与退出码（「已退出（退出码 0）」）—— 退出码是判因的第一条线索，
   *   而 Docker 的原生句子把它混在一串相对时间里（"Exited (0) 8 months ago"）。
   */
  const stateText = computed(() => {
    const state = view.value?.state || snapshotItem.value?.state || ''
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
    const state = view.value?.state || snapshotItem.value?.state
    if (state === 'running') return 'success'
    if (state === 'exited' || state === 'dead') return 'info'
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
   * 载荷**原样来自 agent**（core 只做透传，见 service/docker_cmd.go 的「载荷原样透出」），
   * 而协议侧这些字段的 json 标签是 snake_case（created / started_at / finished_at /
   * exit_code / restart_policy，见 uni_protocol/docker.go 的 DockerContainerInspectPayload）。
   * F1 的解析入口按 camelCase 取那五个多词字段，故在边界上先把两种写法折成一种 ——
   * 不折的话概览里的创建时刻/启动时刻/重启策略/退出码会**静默显示为空**，而页面看不出
   * 是「这个容器没有这个值」还是「我们读错了键」。端口不在解析视图里，单独取。
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
   * pollDelay 给（1 秒起指数退避、5 秒封顶）—— 轻量读取通常在 1 秒内就有结果，固定 1 秒
   * 轮询会把亚秒级的读取拖成 1 秒的体感。超时（约 20 次）给一句结论句而不是继续等。
   */
  async function runRead(
    action: Phase1Action,
    options: Record<string, unknown> = {}
  ): Promise<DockerCmdResultResp> {
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
        return { status: 'timeout', error: '读取超时，请稍后重试' } as DockerCmdResultResp
      }
    }
  }

  /** 受理期失败（无权限/设备离线/参数不合法）的文案：服务端给的结论句优先。 */
  function errMsg(e: unknown, fallback: string): string {
    const msg = (e as { message?: string })?.message
    return msg && msg.trim() !== '' ? msg : fallback
  }

  /**
   * 读容器详情（概览与环境变量共用这一次读取）。
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

  /**
   * 读主机快照，取同一容器的那一行。
   *
   * 为什么详情页还需要快照：**保护标记与列表的状态句只存在于快照** —— inspect 载荷里
   * 没有保护这个事实（保护是 agent 按 `sys.docker.protected` 算出来的）。静默失败：
   * 拿不到就降级（保护显示「—」、状态用 inspect 自己的 state），不弹错。
   */
  async function loadSnapshot() {
    if (!ctx.hostId) return
    const seq = ++snapshotSeq
    try {
      const res = await fetchDockerState(ctx.hostId)
      if (seq !== snapshotSeq) return
      const id = containerId.value
      const name = view.value?.name
      snapshotItem.value =
        (res.containers ?? []).find((c) => c.id === id || (name && c.name === name)) ?? null
    } catch {
      if (seq !== snapshotSeq) return
      snapshotItem.value = null
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
   * 开启跟随（三期）：建立日志流会话 → 接入 NDJSON 流 → 持续喂进行缓冲。
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
      // 轮询到会话建立（结果只给会话句柄）；节奏与一期 runRead 同一套。
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

  /** 首次切到日志 Tab 自动拉一次；之后由「拉取」按钮驱动（一期按需拉取，非流式）。 */
  function ensureLogs() {
    if (logsPulled.value || logsLoading.value || logsFollowing.value) return
    void loadLogs()
  }

  function reloadAll() {
    void loadInspect()
    void loadSnapshot()
    // 跟随中不重拉：一次性拉取会把流缓冲整段替换掉（跟随按钮一关即可回到拉取模式）。
    if (logsPulled.value && !logsFollowing.value) void loadLogs()
  }

  /** 返回容器列表：带上当前主机，列表页据此还原到同一台机器。 */
  function back() {
    router.push({ name: 'DockerContainers', query: { host: ctx.hostId } })
  }

  // ── 二期写操作（头部按钮区）──────────────────────────────────────────
  // 确认档/保护档复用列表页的同一套判定（utils/actions + components/action-confirm）：
  // 删除是标准档；受保护目标上的启停/重启也必须带 force，统一走弹窗里的「强制操作」开关；
  // 普通容器的启停/重启无确认档，点了直接发。
  // （canManage/canDelete/canExec 在页面顶部已声明：终端 Tab 的渲染要先于它们。）

  /** 写指令通道：受理 + 轮询 + 成功后重读（重读就是 reloadAll：inspect/快照/日志一起刷新）。 */
  const { run, busy } = useDockerCmds({ refresh: reloadAll })

  /** 写操作的 target：优先快照/详情里的容器名（与列表页同一口径），名字未知时退化用路由里的 id。 */
  const actionTarget = computed(
    () => snapshotItem.value?.name || view.value?.name || containerId.value
  )

  /** 受保护档判定（agent 算好的结论）：受保护 + 没有强制权限 = 动作不可执行。 */
  const protectionGate = computed(() =>
    protectedGate({ protected: protectedFlag.value }, canExec.value)
  )
  const protectedBlockedConclusion = computed(() =>
    protectionGate.value.allowed ? '' : protectionGate.value.conclusion
  )
  /** 头部写按钮的禁用条件：指令在途，或保护档不允许。 */
  const writeDisabled = computed(() => busy.value || !protectionGate.value.allowed)

  const writeConfirmVisible = ref(false)
  const writeConfirmAction = ref('')
  const writeConfirmLoading = ref(false)

  /** 发一条写指令并把结论给用户；成功后的重读由 composable 统一触发。 */
  async function runWrite(action: string) {
    const res = await run({ action, target: actionTarget.value })
    if (res.ok) ElMessage.success(res.detail || '操作已完成')
    else ElMessage.error(runErrorMessage(res, '操作未完成'))
    return res
  }

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
        // 删除成功 → 回列表页：列表挂载时会自己拉最新快照（删掉的容器不会再出现）。
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
   * Tab 写进 URL（与设备详情同款）：刷新与分享链接能回到同一屏，列表页的「日志」入口
   * 也正是靠 ?tab=logs 直接落在日志 Tab。概览是默认屏，故不必在 URL 里留 tab。
   */
  function onTabChange(name: string | number) {
    const t = tabFromQuery(name)
    activeTab.value = t
    const query = { ...route.query }
    if (t === 'overview') delete query.tab
    else query.tab = t
    void router.replace({ query })
  }

  // 前进/后退键改 query 时同步 Tab（用户按后台键回到上一屏不该看到另一屏的数据）。
  watch(
    () => route.query.tab,
    (raw) => {
      activeTab.value = tabFromQuery(raw)
    }
  )

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

  // 主机到达/切换：重新读详情与快照；日志已拉过（或正停在日志 Tab）就一并重拉。
  // 正在跟随的流属于旧主机 —— 先断开（换主机后由用户决定要不要重新跟随）。
  watch(
    () => ctx.hostId,
    () => {
      stopLogFollow()
      if (!ctx.hostId) return
      void loadInspect()
      void loadSnapshot()
      if (logsPulled.value || activeTab.value === 'logs') void loadLogs()
    },
    { immediate: true }
  )

  // 首次进入：拉主机清单（query 里的主机写回；query 里没有就落到第一台并写回）。
  // hostId 从 '' 变成首台 id 时会走上面的 watch，故这里不直接发读取指令。
  void ctx.reload()
</script>

<style lang="scss" scoped>
  @use '@styles/core/breakpoints.scss' as *;

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
      margin: 6px 0 0;
      font-size: 12px;
    }

    &__id {
      color: var(--el-text-color-secondary);
      font-family: var(--el-font-family-mono, ui-monospace, 'SFMono-Regular', Consolas, monospace);
    }

    &__actions {
      display: flex;
      flex-wrap: wrap;
      gap: 8px;
      align-items: center;
    }

    // 保护档结论句：与陈旧标注同一套颜色语言（琥珀色 = 需要注意的结论）。
    &__blocked {
      color: var(--el-color-warning);
      font-size: 12px;
    }
  }

  // 摘要行：宽度不够就换行（窄屏下不挤压）。
  .cd-facts {
    display: flex;
    flex-wrap: wrap;
    gap: 12px 32px;
    margin-top: 12px;
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
      color: var(--el-color-warning);
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
    color: var(--el-color-danger);
    font-size: 13px;
  }

  // 流动的结论句（例如「日志流已结束」）：次要文字色，不与错误同色。
  .cd-logs__note {
    color: var(--el-text-color-secondary);
    font-size: 13px;
  }

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
