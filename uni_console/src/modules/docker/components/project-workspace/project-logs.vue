<template>
  <!-- 单根（single-root 守卫：布局的 Transition 只支持单根，双根切页白屏）。 -->
  <div class="pwl">
    <div class="pwl__bar">
      <span class="pwl__label">行数</span>
      <ElSelect
        v-model="tail"
        class="pwl__tail"
        size="small"
        :disabled="logsLoading || logsFollowing"
      >
        <ElOption :value="100" label="最近 100 行" />
        <ElOption :value="500" label="最近 500 行" />
        <ElOption :value="2000" label="最近 2000 行" />
      </ElSelect>
      <ElButton size="small" :loading="logsLoading" :disabled="logsFollowing" @click="pullLogs">
        拉取
      </ElButton>
      <!-- 服务过滤：纯前端过滤聚合流（会话不分服务开流，勾选不重开会话）。
           选项来自当前归纳出的网元行；CLI 的行前缀匹配在 utils/log 的纯函数里。 -->
      <ElSelect
        v-model="serviceFilter"
        class="pwl__filter"
        size="small"
        multiple
        collapse-tags
        collapse-tags-tooltip
        clearable
        placeholder="全部网元"
      >
        <ElOption v-for="s in services" :key="s" :label="s" :value="s" />
      </ElSelect>
      <span v-if="logsError" class="pwl__error">{{ logsError }}</span>
      <span v-else-if="logsNote" class="pwl__note">{{ logsNote }}</span>
    </div>
    <!-- 日志正文交给查看器：缓冲上限、查找、复制、下载、上滚自动暂停、跟随都在它里面。 -->
    <LogViewer
      v-model:following="logsFollowing"
      :lines="displayLines"
      :truncated="logTruncated"
      :followable="followable"
      :total-lines="displayTotal"
    />
  </div>
</template>

<script setup lang="ts">
  /**
   * 聚合日志区（5b 工作台）：消费 compose:logs（5a）。
   *
   * ── 与 workload-drawer 日志 Tab 的同与不同 ──────────────────────────
   * 同一条流端点（`/cmds/:ref/stream`）与同一 NDJSON 行形（`{seq,data,eof}`），
   * 建会话的两步（受理 + 轮询到会话句柄）也逐字同款（该契约的完整论证在被删的
   * container-detail 时代即已定型，workload-drawer 的 startLogFollow 现持有同一份）。
   * 不同的一点：compose:logs **没有一次性取回的形态**（agent 起的是 compose CLI
   * 进程，不传 --follow 时打完历史即退出、读到 eof 收摊）—— 故「拉取」也走
   * 建会话 + 读流的路径，区别只在 options 里带不带 follow。
   *
   * ── 服务过滤（纯前端）──────────────────────────────────────────────
   * 会话是整个项目的聚合流（CLI 不按服务开流），过滤 = 对已缓冲文本按行前缀
   * 匹配（utils/log 的 filterComposeLogLines）；不重开会话、不打断跟随。
   */
  import { computed, onBeforeUnmount, ref, watch } from 'vue'
  import { ElButton, ElOption, ElSelect } from 'element-plus'
  import LogViewer from '../log-viewer.vue'
  import { COMPOSE_LOGS_ACTIONS, pollDelay } from '../../utils/cmd'
  import { fetchDockerCmdResult, openDockerLogStream, sendDockerCmd } from '../../api'
  import { filterComposeLogLines, splitLogLines } from '../../utils/log'
  import { createLogFeed } from '../../utils/stream'
  import { useDockerHost } from '../../utils/host-context'

  defineOptions({ name: 'DockerProjectLogs' })

  const props = withDefaults(
    defineProps<{
      /** 项目名（compose:logs 的 target）。 */
      project: string
      /** 过滤下拉的选项（当前归纳出的网元行；缩到 0 的服务不在其中，如实按现状给）。 */
      services: string[]
      /** Follow 开关是否渲染（权限与 container:logs 同档：docker:inspect）。 */
      followable?: boolean
    }>(),
    { followable: false }
  )

  // 子组件注入页面级主机上下文（页面是提供者，注入是合法路径，见 host-context 文件头）。
  const ctx = useDockerHost()

  /** compose:logs 的动作名（清单在 utils/cmd.ts；phase-gate 从那里对齐白名单与条数）。 */
  const COMPOSE_LOGS = COMPOSE_LOGS_ACTIONS[0]

  const tail = ref(100)
  const logLines = ref('')
  const logTruncated = ref(false)
  /** 跟随缓冲的累计行数（不受本地上限影响；暂停计数与「共 N 行」用）。 */
  const logTotal = ref(0)
  const logsLoading = ref(false)
  const logsError = ref('')
  /** 流动的结论句（例如「日志流已结束。」），与错误分开显示。 */
  const logsNote = ref('')
  const logsFollowing = ref(false)
  /** 流的断开手柄：abort 即触发服务端取消会话。 */
  let streamAbort: AbortController | null = null
  /** 流的生命周期序号：断开/换主机会让在飞的「建立 + 读循环」失效。 */
  let streamSeq = 0

  /** 服务过滤（纯前端：会话不分服务开流，勾选不重开会话、不打断跟随）。 */
  const serviceFilter = ref<string[]>([])
  /** 过滤后的渲染文本（未选服务 = 原文；行前缀匹配在 utils/log 的纯函数里）。 */
  const displayLines = computed(() => filterComposeLogLines(logLines.value, serviceFilter.value))
  /** 「共 N 行」与暂停计数跟着**过滤后的文本**走：数字与眼前内容同源。 */
  const displayTotal = computed(() =>
    serviceFilter.value.length > 0 ? splitLogLines(displayLines.value).length : logTotal.value
  )

  /**
   * 拉一次（不带 follow）：建会话 → 接流 → 读到 eof 收摊（CLI 打完历史即退出）。
   * 跟随中的流先断开 —— 一条流端点只接一条会话（409 的语义见 streamOpenConclusion）。
   */
  function pullLogs() {
    stopLogs()
    void startLogs(false)
  }

  /**
   * 开一条聚合日志流（受理 → 轮询到会话句柄 → 接 NDJSON → 持续喂进缓冲）。
   * follow=false 是「拉取」（CLI 打完历史退出），true 是「跟随」（--follow 长流）。
   */
  async function startLogs(follow: boolean) {
    if (!ctx.hostId || !props.project) {
      logsFollowing.value = false
      return
    }
    const seq = ++streamSeq
    logsLoading.value = true
    logsError.value = ''
    logsNote.value = ''
    try {
      const accepted = await sendDockerCmd(ctx.hostId, {
        action: COMPOSE_LOGS,
        target: props.project,
        options: follow ? { tail: tail.value, follow: true } : { tail: tail.value }
      })
      // 轮询到会话建立（结果只给会话句柄）；节奏与详情页 runRead 同一套。
      let attempt = 0
      for (;;) {
        if (seq !== streamSeq) return
        const res = await fetchDockerCmdResult(ctx.hostId, accepted.ref)
        if (res.status !== 'pending') {
          if (res.status !== 'succeeded') {
            throw new Error(res.error || '聚合日志未能建立，请稍后重试')
          }
          break
        }
        await new Promise((r) => setTimeout(r, pollDelay(attempt++)))
        if (attempt > 20) throw new Error('聚合日志建立超时，请重试')
      }
      if (seq !== streamSeq) return

      // 接入流之前先把缓冲清空：会话自己会从 tail 行开始发，不叠上旧的日志。
      const feed = createLogFeed()
      logLines.value = ''
      logTotal.value = 0
      logTruncated.value = false

      const controller = new AbortController()
      streamAbort = controller
      const res = await openDockerLogStream(ctx.hostId, accepted.ref, controller.signal)
      if (!res.ok || !res.body) {
        throw new Error(streamOpenConclusion(res.status))
      }
      logsLoading.value = false

      const reader = res.body.getReader()
      const chunkDecoder = new TextDecoder('utf-8')
      for (;;) {
        const { done, value } = await reader.read()
        if (done || seq !== streamSeq) break
        feed.pushRaw(chunkDecoder.decode(value, { stream: true }))
        logLines.value = feed.text
        logTotal.value = feed.totalLines
        logTruncated.value = feed.truncated
        if (feed.eof) break
      }
      if (seq !== streamSeq) return
      // 流自然收尾（agent 发了 eof）：最后一帧可能还带着数据。
      feed.pushRaw(chunkDecoder.decode())
      logLines.value = feed.text
      logTotal.value = feed.totalLines
      logTruncated.value = feed.truncated
      streamAbort = null
      // eof 是流自然收尾；没有 eof 的结束是连接被中途掐断，说清区别。
      if (follow) {
        logsFollowing.value = false
        logsNote.value = feed.eof ? '日志流已结束。' : '日志流已断开。'
      } else {
        logsNote.value = feed.eof ? '日志已拉取完毕。' : '日志流已断开。'
      }
    } catch (e) {
      if (seq !== streamSeq) return
      // 主动断开（关跟随/离开页面）不是错误：不弹结论句。
      if ((e as { name?: string })?.name === 'AbortError') return
      streamAbort = null
      logsFollowing.value = false
      logsError.value = errMsg(e, '聚合日志失败，请稍后重试')
    } finally {
      if (seq === streamSeq) {
        logsLoading.value = false
        streamAbort = null
      }
    }
  }

  /** 流端点接入失败的结论句（状态码是排障线索，不进页面）。 */
  function streamOpenConclusion(status: number): string {
    if (status === 401) return '登录状态已失效，请重新登录后再试。'
    if (status === 403) return '没有接入该日志流的权限。'
    if (status === 404) return '该日志会话已过期，请重新拉取。'
    if (status === 409) return '该日志会话已有连接，请稍后重试。'
    return '聚合日志未能建立，请稍后重试'
  }

  /** 断开流（关跟随/换主机/离开页面都会走到这里；幂等）。 */
  function stopLogs() {
    const controller = streamAbort
    // 只在真的有东西在飞时让序号失效（幂等调用——例如 eof 后开关回调再走一次——不扰动别的流程）。
    const inFlight = controller !== null || logsFollowing.value
    streamAbort = null
    if (!inFlight) return
    streamSeq++ // 让在飞的建立流程与读循环失效
    logsLoading.value = false
    if (controller) controller.abort()
    if (logsFollowing.value) logsFollowing.value = false
  }

  /** 受理期/结果期失败（无权限/设备离线/CLI 报错）的文案：服务端给的结论句优先。 */
  function errMsg(e: unknown, fallback: string): string {
    const msg = (e as { message?: string })?.message
    return msg && msg.trim() !== '' ? msg : fallback
  }

  // 跟随开关的翻转 = 建立/断开流。结束（eof）或失败时 startLogs 自己把开关拨回，
  // 于是这里再走一次 stopLogs（幂等）。
  watch(logsFollowing, (on) => {
    if (on) {
      stopLogs() // 在飞的拉取流先断：一个会话只接一条连接
      void startLogs(true)
    } else {
      stopLogs()
    }
  })

  // 离开页面：断开流（服务端随之下发取消）。
  onBeforeUnmount(() => stopLogs())

  // 主机到达/切换（或项目名变）：断开旧流、清缓冲、自动拉一次 ——
  // 工作台的聚合日志是「进来就能看」的第一屏，但只拉一次历史（跟随由用户决定）。
  //
  // ⚠ 多源 watch（按源逐个比较）而不是 `[hostId, project]` 的单 getter：后者每次
  // 求值都产新数组（身份永不相等），依赖一触发就重放 —— 会把「快照重拉」放大成
  // 反复重建日志会话（project-config 的同款死循环教训）。
  watch(
    [() => ctx.hostId, () => props.project],
    () => {
      stopLogs()
      logLines.value = ''
      logTotal.value = 0
      logTruncated.value = false
      logsError.value = ''
      logsNote.value = ''
      if (ctx.hostId && props.project) void startLogs(false)
    },
    { immediate: true }
  )
</script>
