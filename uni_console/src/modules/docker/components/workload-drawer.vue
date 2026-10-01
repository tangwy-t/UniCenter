<template>
  <ElDrawer
    v-model="visibleModel"
    class="wkl-drawer"
    direction="rtl"
    append-to-body
    :size="drawerSize"
    :close-on-press-escape="!writeConfirmLoading"
  >
    <!-- 头部：容器名 + 短 id + 状态 + 行操作（与行菜单同一套注册表与确认档）。
         抽屉是「列表上多看一眼」的轻量入口，全功能仍在详情页（头部给链接）。 -->
    <template #header>
      <div v-if="row" class="wkl-drawer__head">
        <div class="wkl-drawer__title-row">
          <h3 class="wkl-drawer__title">{{ row.name }}</h3>
          <ElTag v-if="stateText" size="small" :type="stateTagType">{{ stateText }}</ElTag>
          <ElTag v-if="row.protected" size="small" type="warning">受保护</ElTag>
        </div>
        <p class="wkl-drawer__sub">
          <span class="wkl-drawer__id" :title="row.id">{{ shortId }}</span>
          <span class="wkl-drawer__host">@ {{ row.hostname }}</span>
          <a
            v-if="canInspect"
            class="wkl-drawer__full"
            title="打开全功能详情页（概览 / 日志 / 终端 / 环境变量）"
            @click.prevent="openFullDetail"
          >
            在详情页打开
            <ArtSvgIcon icon="ri:external-link-line" class="wkl-drawer__full-icon" />
          </a>
        </p>
        <div class="wkl-drawer__actions">
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
          <span v-if="protectedBlockedConclusion" class="wkl-drawer__blocked">
            {{ protectedBlockedConclusion }}
          </span>
        </div>
      </div>
    </template>

    <ElTabs v-model="activeTab" class="wkl-drawer__tabs">
      <!-- 概览：container:inspect 读取 + 实时 stats 曲线（五期监控面），展示口径
           复刻详情页概览 Tab（同样的 ElDescriptions 字段集与挂载块；快照态字段
           ——保护/状态句——直接用行数据）。 -->
      <ElTabPane label="概览" name="overview">
        <!-- 实时统计：独立于 inspect 的流通道（inspect 失败不该连累曲线）。挂载
             纪律同终端 Tab —— 概览 Tab 激活 + 抽屉开着才挂（v-if），切走/关抽屉/
             换行即卸载 → 组件卸载时自己 abort 断流；:key 随行换。权限与日志同档
             （docker:inspect），无权限不渲染（spec §11.0）。 -->
        <ContainerStats
          v-if="canInspect && activeTab === 'overview' && modelValue && row"
          :key="`${row.hostId}:${row.id}`"
          :host-id="row.hostId"
          :container-id="row.id"
          :running="isRunning"
          :snapshot="row"
        />
        <ElSkeleton v-if="loading && !view" :rows="5" animated />
        <ElEmpty v-else-if="errorText" :description="errorText">
          <ElButton size="small" @click="loadInspect">重试</ElButton>
        </ElEmpty>
        <template v-else-if="view">
          <ElDescriptions :column="2" border size="small">
            <ElDescriptionsItem label="状态">{{ stateText || '—' }}</ElDescriptionsItem>
            <ElDescriptionsItem label="镜像">{{ row?.image || '—' }}</ElDescriptionsItem>
            <ElDescriptionsItem label="创建时刻">{{ createdText }}</ElDescriptionsItem>
            <ElDescriptionsItem label="启动时刻">{{ startedText }}</ElDescriptionsItem>
            <ElDescriptionsItem label="重启策略">{{
              view.restartPolicy || '—'
            }}</ElDescriptionsItem>
            <ElDescriptionsItem v-if="healthText" label="健康">{{ healthText }}</ElDescriptionsItem>
            <ElDescriptionsItem label="端口">{{ portsTextValue }}</ElDescriptionsItem>
            <ElDescriptionsItem label="网络">{{ networksText }}</ElDescriptionsItem>
          </ElDescriptions>

          <div class="wkl-drawer__block">
            <div class="wkl-drawer__block-title">挂载</div>
            <ul v-if="mounts.length" class="wkl-drawer__mounts">
              <li v-for="(m, i) in mounts" :key="i" class="wkl-drawer__mount">
                <span class="wkl-drawer__mount-type">{{ m.type || '挂载' }}</span>
                <code class="wkl-drawer__mount-path">{{ mountText(m) }}</code>
              </li>
            </ul>
            <ElEmpty v-else description="这个容器没有挂载" />
          </div>
        </template>
      </ElTabPane>

      <!-- 日志：一次性拉取（行数下拉）+ Follow 流（host 取行主机 —— 抽屉没有
           provide 主机上下文，行自带归属）。 -->
      <ElTabPane label="日志" name="logs">
        <div class="wkl-drawer__logs-bar">
          <span class="wkl-drawer__logs-label">行数</span>
          <ElSelect
            v-model="tail"
            class="wkl-drawer__logs-tail"
            size="small"
            :disabled="logsLoading || logsFollowing"
          >
            <ElOption :value="100" label="最近 100 行" />
            <ElOption :value="500" label="最近 500 行" />
            <ElOption :value="2000" label="最近 2000 行" />
          </ElSelect>
          <ElButton size="small" :loading="logsLoading" :disabled="logsFollowing" @click="loadLogs">
            拉取
          </ElButton>
          <!-- 跟随中改行数不会重开会话，故行数/拉取都在跟随期间锁住（跟随开关本身不受限）。 -->
          <span v-if="logsError" class="wkl-drawer__logs-error">{{ logsError }}</span>
          <span v-else-if="logsNote" class="wkl-drawer__logs-note">{{ logsNote }}</span>
        </div>
        <LogViewer
          v-model:following="logsFollowing"
          :lines="logLines"
          :truncated="logTruncated"
          :followable="canInspect"
          :total-lines="logTotal"
        />
      </ElTabPane>

      <!-- 终端是三期能力：仅具备执行权限时渲染（不渲染 ≠ 禁用，spec §11.0）。
           首次切到本 Tab 才建立会话（v-if 挂 activeTab）：开抽屉不会顺手起 shell；
           切走/关抽屉即卸载 → 组件在卸载时发 cancel 并释放会话。 -->
      <ElTabPane v-if="canExec" label="终端" name="pty">
        <!-- 首次切到本 Tab 才建立会话；切走或**关抽屉**即卸载 → 组件在卸载时发 cancel
             并释放会话（会话生命周期 = 抽屉打开期，spec §11.0 的「不为看不见的终端
             留一个 shell」）。 -->
        <PtyTerminal
          v-if="activeTab === 'pty' && modelValue && row"
          :key="`${row.hostId}:${row.id}`"
          :host-id="row.hostId"
          :container-id="row.id"
        />
      </ElTabPane>
    </ElTabs>

    <!-- 写操作的确认弹窗：与行菜单/详情页同一套（注册表档 + 保护档的「强制操作」开关）。 -->
    <DockerActionConfirm
      v-model="writeConfirmVisible"
      :action="writeConfirmAction"
      :target="row?.name ?? ''"
      :target-protected="row?.protected === true"
      target-kind="容器"
      :loading="writeConfirmLoading"
      @confirm="onWriteConfirm"
    />
  </ElDrawer>
</template>

<script setup lang="ts">
  /**
   * 工作负载抽屉：跨主机统一表的详情入口（列表页「详情 / 日志」不再跳详情页，
   * 而是原地右滑打开本抽屉 —— 换页面会丢掉筛选现场，抽屉保住它）。
   *
   * 三个 Tab 与容器详情页同源同口径（概览 = container:inspect + 详情页概览 Tab 的
   * 展示字段 + 实时 stats 曲线（五期监控面）；日志 = log-viewer + 行数 + Follow；
   * 终端 = pty-terminal 懒挂载），区别只有一件事：**host 取行主机**。统一表的行来自
   * 任意主机，抽屉没有页面级 provide 主机上下文可用 —— 行数据自带 hostId，所有指令
   * 都按它发。
   *
   * 生命周期纪律照抄详情页：Tab 懒挂载（日志首切才拉、终端首切才建会话）、切走断流、
   * 关抽屉断流（日志 AbortController + 终端/stats 走 v-if 卸载断流）。全功能详情页
   * 保留（头部「在详情页打开」链接），环境变量/配置这类重读取仍在那边 —— 抽屉只收
   * 「看一眼、动一下」的高频动作。
   */
  import { computed, defineAsyncComponent, ref, watch } from 'vue'
  import { useRouter } from 'vue-router'
  import {
    ElButton,
    ElDescriptions,
    ElDescriptionsItem,
    ElDrawer,
    ElEmpty,
    ElMessage,
    ElOption,
    ElSelect,
    ElSkeleton,
    ElTabPane,
    ElTabs,
    ElTag
  } from 'element-plus'
  // ArtSvgIcon 走 unplugin-vue-components 自动注册（与 overview.vue 同款用法）。
  import { formatUnixSeconds } from '@/modules/device/utils/display'
  import { useAppBreakpoints } from '@/hooks/core/useAppBreakpoints'
  import { useAuth } from '@/hooks/core/useAuth'
  import {
    PermDockerDelete,
    PermDockerExec,
    PermDockerInspect,
    PermDockerManage
  } from '@/enums/permission'
  import DockerActionConfirm from './action-confirm.vue'
  import ContainerStats from './container-stats.vue'
  import LogViewer from './log-viewer.vue'
  import {
    fetchDockerCmdResult,
    openDockerLogStream,
    sendDockerCmd,
    type DockerWorkloadItem
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
  import { portsText } from '../utils/display'
  import { createLogFeed } from '../utils/stream'

  defineOptions({ name: 'DockerWorkloadDrawer' })

  type TabName = 'overview' | 'logs' | 'pty'

  const props = withDefaults(
    defineProps<{
      /** 抽屉开关（v-model）。 */
      modelValue: boolean
      /** 当前行（null = 关闭态兜底，模板全部 v-if 守卫）。 */
      row: DockerWorkloadItem | null
      /** 打开时落在哪个 Tab（行菜单「日志」带 'logs'；缺省概览）。 */
      initialTab?: TabName
      /** 写操作成功后的列表重拉（useDockerCmds 的 refresh）。 */
      refresh?: () => void | Promise<void>
    }>(),
    { initialTab: 'overview', refresh: undefined }
  )

  const emit = defineEmits<{ 'update:modelValue': [value: boolean] }>()

  // 终端组件按需加载：xterm 的体积不小，只有真的要开终端时才把这块代码拉下来
  // （与「首次切到终端 Tab 才建立会话」是同一取向）。
  const PtyTerminal = defineAsyncComponent(() => import('./pty-terminal.vue'))

  const router = useRouter()
  const { hasAuth } = useAuth()
  const { smaller } = useAppBreakpoints()

  // 抽屉宽度：日志/终端需要横向空间（终端行宽由容器盒子决定）；平板竖屏以下全屏 ——
  // 半宽抽屉里塞 xterm 只会得到一条窄缝。
  const drawerSize = computed(() => (smaller('tablet').value ? '100%' : '720px'))

  const canManage = computed(() => hasAuth(PermDockerManage))
  const canDelete = computed(() => hasAuth(PermDockerDelete))
  const canExec = computed(() => hasAuth(PermDockerExec))
  // 日志（含 Follow）的权限码与 container:logs 同一档：docker:inspect。
  const canInspect = computed(() => hasAuth(PermDockerInspect))

  const visibleModel = computed({
    get: () => props.modelValue,
    set: (value: boolean) => emit('update:modelValue', value)
  })

  // ── 概览（container:inspect，host = 行主机）──────────────────────
  const activeTab = ref<TabName>(props.initialTab)
  /** 详情视图：F1 解析结果（+ 端口/挂载；快照态字段直接用行数据）。 */
  const view = ref<ContainerInspectView | null>(null)
  const loading = ref(false)
  const errorText = ref('')
  let inspectSeq = 0

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

  const shortId = computed(() => props.row?.id.slice(0, 12) ?? '')
  const isRunning = computed(() => props.row?.state === 'running')

  /**
   * 状态结论：运行中优先行上的原生状态句（与列表列逐字一致，同一个事实不写两种话）；
   * 已停止说结论与退出码（判因的第一条线索）——口径复刻详情页。
   */
  const stateText = computed(() => {
    const state = props.row?.state ?? ''
    if (!state) return ''
    if (state === 'running') return props.row?.statusText || STATE_TEXT.running
    if (state === 'exited' || state === 'dead') {
      const code = view.value?.exitCode
      return code === undefined || code === null ? STATE_TEXT[state] : `已退出（退出码 ${code}）`
    }
    return STATE_TEXT[state] ?? state
  })

  const stateTagType = computed<'success' | 'info' | 'warning'>(() => {
    const state = props.row?.state
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

  const createdText = computed(() => formatUnixSeconds(view.value?.createdAt))
  const startedText = computed(() => formatUnixSeconds(view.value?.startedAt))
  const portsTextValue = computed(() => portsText(props.row?.ports))
  const networksText = computed(() => {
    const list = view.value?.networks ?? []
    return list.length ? list.join('、') : '—'
  })
  const mounts = computed(() => view.value?.mounts ?? [])

  function mountText(m: ContainerInspectView['mounts'][number]): string {
    const path = `${m.source || '—'} → ${m.destination || '—'}`
    return m.rw === false ? `${path}（只读）` : path
  }

  /**
   * 发一条只读指令并轮询到终态（hostId 取行主机；与详情页 runRead 同一套节奏：
   * pollDelay 指数退避，轻量读取通常 1 秒内有结果）。
   */
  async function runRead(action: Phase1Action, options: Record<string, unknown> = {}) {
    const hostId = props.row?.hostId ?? ''
    const accepted = await sendDockerCmd(hostId, {
      action,
      target: props.row?.id ?? '',
      options
    })
    let attempt = 0
    for (;;) {
      const res = await fetchDockerCmdResult(hostId, accepted.ref)
      if (res.status !== 'pending') return res
      await new Promise((r) => setTimeout(r, pollDelay(attempt++)))
      if (attempt > 20) {
        return { status: 'timeout', error: '读取超时，请稍后重试' } as Awaited<
          ReturnType<typeof fetchDockerCmdResult>
        >
      }
    }
  }

  /** 受理期失败的文案：服务端给的结论句优先。 */
  function errMsg(e: unknown, fallback: string): string {
    const msg = (e as { message?: string })?.message
    return msg && msg.trim() !== '' ? msg : fallback
  }

  /**
   * 载荷 → 视图。载荷原样来自 agent（协议侧多词字段是 snake_case），边界上折成
   * camelCase —— 不折的话创建时刻/重启策略/退出码会**静默显示为空**（详情页同一坑）。
   */
  function toView(payload: unknown): ContainerInspectView {
    const raw = (payload ?? {}) as Record<string, unknown>
    return parseContainerInspectPayload({
      ...raw,
      createdAt: raw.createdAt ?? raw.created,
      startedAt: raw.startedAt ?? raw.started_at,
      finishedAt: raw.finishedAt ?? raw.finished_at,
      exitCode: raw.exitCode ?? raw.exit_code,
      restartPolicy: raw.restartPolicy ?? raw.restart_policy
    })
  }

  /**
   * 读容器详情（打开抽屉 / 换行时）。失败文案用结果里的 error（agent 的结论句），
   * 静默不弹 toast —— 结论就在抽屉里。
   */
  async function loadInspect() {
    if (!props.row) return
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

  // ── 日志（一次性拉取 + Follow 流，host = 行主机）──────────────────
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
  /** 跟随的生命周期序号：断开/换行会让在飞的「建立 + 读循环」失效。 */
  let followSeq = 0
  let logsSeq = 0

  /** 拉一次日志（tail 由下拉决定）。失败同样用结果的 error。 */
  async function loadLogs() {
    if (!props.row) return
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
   * 开启跟随：建立日志流会话 → 接入 NDJSON 流 → 持续喂进缓冲（与详情页同款两步，
   * 契约见 container-detail.vue 的注释；AbortController 一断，服务端即下发取消）。
   */
  async function startLogFollow() {
    if (!props.row) {
      logsFollowing.value = false
      return
    }
    const seq = ++followSeq
    logsLoading.value = true
    logsError.value = ''
    logsNote.value = ''
    try {
      const accepted = await sendDockerCmd(props.row.hostId, {
        action: 'container:logs',
        target: props.row.id,
        options: { tail: tail.value, follow: true }
      })
      let attempt = 0
      for (;;) {
        if (seq !== followSeq) return
        const res = await fetchDockerCmdResult(props.row.hostId, accepted.ref)
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

      // 接入流之前先把缓冲清空：会话自己会从 tail 行开始发，不叠上抽屉里旧的日志。
      logsSeq++ // 在飞的一次性拉取（若有）作废：它的结果不该落进跟随缓冲
      const feed = createLogFeed()
      logLines.value = ''
      logTotal.value = 0
      logTruncated.value = false
      logsPulled.value = true

      const controller = new AbortController()
      logStreamAbort = controller
      const res = await openDockerLogStream(props.row.hostId, accepted.ref, controller.signal)
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
      logsNote.value = feed.eof ? '日志流已结束。' : '日志流已断开。'
    } catch (e) {
      if (seq !== followSeq) return
      // 主动断开（关跟随/切 Tab/关抽屉）不是错误：不弹结论句。
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

  /** 断开跟随（关开关/切 Tab/换行/关抽屉都会走到这里）。 */
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

  // ── 写操作（头部按钮，与行菜单同一套确认档/保护档）─────────────────
  // hostId 直接绑行主机：useDockerCmds 在 run 入口处对 hostId 求值，
  // 行切换后（抽屉未关就换目标的场景不存在——每次打开都换行重置）发出的即新行主机。
  const { run, busy } = useDockerCmds({
    hostId: () => props.row?.hostId ?? '',
    refresh: () => props.refresh?.()
  })

  /** 受保护档判定（agent 算好的结论）：受保护 + 没有强制权限 = 动作不可执行。 */
  const protectionGate = computed(() =>
    protectedGate({ protected: props.row?.protected === true }, canExec.value)
  )
  const protectedBlockedConclusion = computed(() =>
    protectionGate.value.allowed ? '' : protectionGate.value.conclusion
  )
  const writeDisabled = computed(() => busy.value || !protectionGate.value.allowed)

  const writeConfirmVisible = ref(false)
  const writeConfirmAction = ref('')
  const writeConfirmLoading = ref(false)

  /** 该动作此刻是否必须经确认弹窗：注册表的确认档，或受保护目标的保护档。 */
  function needsDialog(action: string): boolean {
    return needsConfirm(action) || (props.row?.protected === true && actionGuarded(action))
  }

  function openWriteConfirm(action: string) {
    writeConfirmAction.value = action
    writeConfirmVisible.value = true
  }

  function onToggle() {
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

  /** 发一条写指令并把结论给用户；成功后的列表重拉由 composable 统一触发。 */
  async function runWrite(action: string, confirm?: string, force?: boolean) {
    if (!props.row) return
    const options: Record<string, unknown> = { target: props.row.name }
    if (force) options.force = true
    const res = await run({ action, target: props.row.name, options, confirm })
    if (res.ok) ElMessage.success(res.detail || '操作已完成')
    else ElMessage.error(runErrorMessage(res, '操作未完成'))
  }

  /** 确认弹窗提交：force 只在「强制操作」开关出现且被勾选时为 true。 */
  async function onWriteConfirm(payload: { confirm: string; force: boolean }) {
    const action = writeConfirmAction.value
    writeConfirmLoading.value = true
    try {
      await runWrite(action, payload.confirm, payload.force)
    } finally {
      writeConfirmLoading.value = false
      writeConfirmVisible.value = false
    }
  }

  // ── 生命周期：懒挂载 + 断流（照抄详情页纪律）───────────────────────
  // 打开 / 换行：重置三 Tab 的就地状态（概览数据、日志缓冲、跟随流），落 initialTab，
  // 概览立即读（它是默认第一屏）；终端的「换行即换会话」由 :key 完成。
  watch(
    () => [props.modelValue, props.row?.hostId ?? '', props.row?.id ?? ''],
    ([visible]) => {
      stopLogFollow()
      if (!visible) return
      activeTab.value = props.initialTab
      view.value = null
      errorText.value = ''
      logLines.value = ''
      logTruncated.value = false
      logTotal.value = 0
      logsError.value = ''
      logsNote.value = ''
      logsPulled.value = false
      inspectSeq++
      void loadInspect()
      if (activeTab.value === 'logs') ensureLogs()
    },
    { immediate: true }
  )

  // 切到日志 Tab 自动拉一次；切走（含切到终端）就断开跟随 —— 终端会话与日志流
  // 同时挂着没有意义，且两条流都吃 agent 的会话槽位。
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

  /** 全功能详情页（本切片保留）：带 host query，返回列表页时现场也能还原。 */
  function openFullDetail() {
    if (!props.row) return
    visibleModel.value = false
    void router.push({
      name: 'DockerContainerDetail',
      params: { id: props.row.id },
      query: { host: props.row.hostId }
    })
  }
</script>

<style lang="scss" scoped>
  @use '@styles/core/breakpoints.scss' as *;

  .wkl-drawer__head {
    display: flex;
    flex-direction: column;
    gap: 6px;
    min-width: 0;
    padding-right: 24px; // 给 EP 自带的关闭按钮让位（它在 header 右上角）
  }

  .wkl-drawer__title-row {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;
  }

  .wkl-drawer__title {
    margin: 0;
    font-size: 16px;
    font-weight: 600;
    word-break: break-all;
  }

  .wkl-drawer__sub {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: baseline;
    margin: 0;
    font-size: 12px;
  }

  .wkl-drawer__id {
    color: var(--el-text-color-secondary);
    font-family: var(--el-font-family-mono, ui-monospace, 'SFMono-Regular', Consolas, monospace);
  }

  // 行归属（跨主机表的行来自任意一台机器）：次要文字色，与短 id 并排。
  .wkl-drawer__host {
    color: var(--el-text-color-secondary);
  }

  // 「在详情页打开」：抽屉的补充出口（不是主路径），主题色弱化呈现。
  .wkl-drawer__full {
    display: inline-flex;
    gap: 4px;
    align-items: center;
    color: var(--el-color-primary);
    cursor: pointer;
  }

  .wkl-drawer__full-icon {
    font-size: 12px;
  }

  .wkl-drawer__actions {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;
  }

  // 保护档结论句：琥珀色 = 需要注意的结论（与模块内陈旧/保护标注同一套颜色语言）。
  .wkl-drawer__blocked {
    color: var(--el-color-warning);
    font-size: 12px;
  }

  .wkl-drawer__tabs {
    :deep(.el-tabs__header) {
      margin-bottom: 16px;
    }
  }

  .wkl-drawer__block {
    margin-top: 16px;

    &-title {
      margin-bottom: 8px;
      font-size: 14px;
      font-weight: 600;
    }
  }

  .wkl-drawer__mounts {
    margin: 0;
    padding: 0;
    list-style: none;
  }

  .wkl-drawer__mount {
    display: flex;
    gap: 12px;
    align-items: baseline;
    padding: 6px 0;
    border-bottom: 1px solid var(--el-border-color-lighter);
    font-size: 13px;

    &-type {
      flex: none;
      min-width: 56px;
      color: var(--el-text-color-secondary);
      font-size: 12px;
    }

    &-path {
      font-family: var(--el-font-family-mono, ui-monospace, 'SFMono-Regular', Consolas, monospace);
      word-break: break-all;
    }
  }

  .wkl-drawer__logs-bar {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;
    margin-bottom: 12px;
  }

  .wkl-drawer__logs-label {
    color: var(--el-text-color-secondary);
    font-size: 13px;
  }

  .wkl-drawer__logs-tail {
    width: 140px;
  }

  .wkl-drawer__logs-error {
    color: var(--el-color-danger);
    font-size: 13px;
  }

  // 流动的结论句（例如「日志流已结束」）：次要文字色，不与错误同色。
  .wkl-drawer__logs-note {
    color: var(--el-text-color-secondary);
    font-size: 13px;
  }

  // 手机横屏（<768）：头部动作组换行铺开，日志工具条允许换行（详情页同一口径）。
  @include respond-below('tablet') {
    .wkl-drawer__logs-bar {
      flex-wrap: wrap;
    }
  }
</style>
