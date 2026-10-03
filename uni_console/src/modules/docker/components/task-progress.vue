<template>
  <!-- 单根（single-root 守卫扫全模块的 .vue）：展开区本体是唯一根。 -->
  <div class="tp">
    <!-- ── 进度面（按 action 分两形：层表 pull/push、文本播报 build）── -->
    <template v-if="family !== 'build'">
      <!-- 阶段消息行（"Pulling from …" / "The push refers to …"）的最新一条：与进度
           对话框同一口径 —— 全量消息历史属于日志/活动流。 -->
      <p v-if="layerView.note" class="tp__note">{{ layerView.note }}</p>
      <p v-if="summaryText" class="tp__summary">{{ summaryText }}</p>

      <ul v-if="layerView.layers.length" class="tp-layers">
        <li
          v-for="layer in layerView.layers"
          :key="layer.id"
          class="tp-layer"
          :class="{ 'is-done': layer.done }"
        >
          <span class="tp-layer__id" :title="layer.id">{{ layer.shortId }}</span>
          <span class="tp-layer__status" :title="layer.status">{{ layer.status }}</span>
          <!-- 进度条与字节文案：与进度对话框逐字同口径（无 total 走不确定态，
               层终态后不再画条）。 -->
          <ElProgress
            v-if="!layer.done"
            class="tp-layer__bar"
            :percentage="layer.total > 0 ? layerPercent(layer.current, layer.total) : 100"
            :indeterminate="layer.total <= 0"
            :stroke-width="6"
            :show-text="false"
          />
          <span v-if="!layer.done && layer.total > 0" class="tp-layer__bytes">
            {{ formatPullBytes(layer.current) }} / {{ formatPullBytes(layer.total) }}
          </span>
          <ArtSvgIcon v-if="layer.done" icon="ri:check-line" class="tp-layer__check" />
        </li>
      </ul>
      <p v-else class="tp__empty">正在等待进度…</p>
    </template>

    <template v-else>
      <p v-if="buildDroppedLines > 0" class="tp__note">
        输出较长，已丢弃前 {{ buildDroppedLines }} 行（只保留最近一段）
      </p>
      <ul ref="linesEl" class="tp-lines">
        <li
          v-for="l in buildLines"
          :key="l.key"
          class="tp-line"
          :class="{ 'is-step': l.step }"
        >
          <span v-if="l.id" class="tp-line__id">{{ l.id }}</span>
          <span class="tp-line__text">{{ l.text }}</span>
        </li>
      </ul>
      <p v-if="buildLines.length === 0" class="tp__empty">正在等待构建输出…</p>
    </template>

    <!-- 流的收口提示（接入失败/断开/已全部到达）：任务页的 5s 轮询会把条目带向
         终态，这里只说「流这边怎么了」，结论句以任务行的 summary 为准。 -->
    <p v-if="hint" class="tp__hint">{{ hint }}</p>

    <!-- 取消：独立于观看的动作（调显式取消端点）。收起本行只是收起 —— 任务照常跑；
         这里下发的是向 agent 的 cancel 帧（best-effort，能否真被截止以任务行结论
         为准，故措辞是「已请求取消」）。 -->
    <div class="tp__ops">
      <ElButton
        v-if="canCancel"
        size="small"
        :loading="canceling"
        :disabled="cancelRequested"
        @click="cancelTask"
      >
        {{ cancelRequested ? '已请求取消' : cancelLabel }}
      </ElButton>
      <span class="tp__collapse-note">收起不取消任务。</span>
    </div>
    <!-- 取消请求失败的就地结论句（403 无权取消 / 409 该任务已结束 / 500 未送达）。 -->
    <p v-if="cancelError" class="tp__error">{{ cancelError }}</p>
  </div>
</template>

<script setup lang="ts">
  /**
   * 任务行展开区里的**进度观看**视图（进度三族 pull/build/push 共用一份）：
   * 把三个进度对话框的进度态抽出来**只读复用** —— 同三条流端点
   *（cmds/:ref/pull|build|push）与同一批折叠器（utils/pull.ts 的 createPullFeed /
   * createPushFeed、utils/build-push.ts 的 createBuildFeed），没有 input/result 态：
   * 发起在各自的入口页，终态由任务行的 summary 承载，本组件只负责「展开时看得见」。
   *
   * 生命周期（本波语义收口后的完整口径）：
   *   - **挂载即接入**、**卸载即断流**：断流只是停止观看（core 只解除接入，不下发
   *     cancel）—— 收起行/离开页面都不影响任务本身；**再次展开即重接**（同一条句柄，
   *     断线期间到达的帧在会话缓冲里等着，进度可续）。一次观看与流会话是 N:1。
   *   - **失活不断流**：keep-alive 失活（任务页被 worktab 缓存）时组件仍挂载，
   *     连接保持 —— 流是进度的生命线，监控窗口关掉不等于要放弃观看。
   *     （任务页的 5s 轮询停表是另一回事：那是页面开销纪律，与流的存续无关。）
   *   - **取消是另一个动作**：调显式取消端点（cancelDockerCmd），与观看互不牵连
   *     （点了取消照常看得见它怎么收场）。可见性由调用方判定（canCancel：发起人 +
   *     权限 —— 服务端两道都会复核）。
   *
   * 为什么按 action 分两形而不是三份组件：层表（pull/push 同形，只有语义字面量
   * 不同）与文本播报（build）是两种渲染，不是三份逻辑 —— 折叠器已经在纯逻辑层
   * 分好了，这里只选渲染。
   */
  import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
  import { ElButton, ElProgress } from 'element-plus'
  import { cancelDockerCmd, openDockerBuildStream, openDockerPullStream, openDockerPushStream } from '../api'
  import { createBuildFeed, type BuildFeedLine } from '../utils/build-push'
  import {
    createPullFeed,
    createPushFeed,
    formatPullBytes,
    layerPercent,
    type PullFeed,
    type PullLayer
  } from '../utils/pull'

  defineOptions({ name: 'DockerTaskProgress' })

  const props = defineProps<{
    /** 目标主机（一次指令只属于受理它的那台主机）。 */
    hostId: string
    /** 指令号（进度流 & 取消端点的钥匙）。 */
    cmdRef: string
    /** 动作码（image:pull / image:build / image:push —— 选流端点与渲染形态的依据）。 */
    action: string
    /** 是否给取消入口（调用方判：发起人 + docker:manage；服务端仍会复核两道）。 */
    canCancel?: boolean
  }>()

  /** 进度族（与 core 的 progressSessionOf 同源的口径）：渲染形态与取消措辞都由它定。 */
  type Family = 'pull' | 'build' | 'push'

  const family = computed<Family>(() => {
    if (props.action === 'image:build') return 'build'
    if (props.action === 'image:push') return 'push'
    return 'pull'
  })

  const cancelLabel = computed(() =>
    family.value === 'build' ? '取消构建' : family.value === 'push' ? '取消推送' : '取消拉取'
  )

  interface LayerView {
    layers: PullLayer[]
    note: string
    movedBytes: number
    totalBytes: number
    doneLayers: number
  }

  const layerView = ref<LayerView>({
    layers: [],
    note: '',
    movedBytes: 0,
    totalBytes: 0,
    doneLayers: 0
  })
  const buildLines = ref<BuildFeedLine[]>([])
  const buildDroppedLines = ref(0)
  /** 播报容器（构建是直播：跟随到最新行；容器还没挂/已收起时跳过）。 */
  const linesEl = ref<HTMLUListElement | null>(null)

  const cancelRequested = ref(false)
  const canceling = ref(false)
  const cancelError = ref('')

  const streamEnded = ref(false)
  /** '' = 流还连着；'open' = 接入失败（任务仍在服务端跑）；'broken' = 连上后断开（只是不再观看）。 */
  const streamFailed = ref<'' | 'open' | 'broken'>('')
  const streamFailText = ref('')

  /** 生命周期序号：卸载让在飞的读循环失效（照进度对话框的 seq 纪律）。 */
  let readSeq = 0
  let streamAbort: AbortController | null = null

  /** 汇总（层族）：动词按语义侧换字（pull 已下载 / push 已上传）。 */
  const summaryText = computed(() => {
    const v = layerView.value
    if (v.layers.length === 0) return ''
    const parts: string[] = []
    if (v.totalBytes > 0) {
      const verb = family.value === 'push' ? '已上传' : '已下载'
      parts.push(`${verb} ${formatPullBytes(v.movedBytes)} / ${formatPullBytes(v.totalBytes)}`)
    }
    parts.push(`${v.doneLayers}/${v.layers.length} 层完成`)
    return parts.join(' · ')
  })

  const hint = computed(() => {
    if (streamFailed.value === 'open') {
      return `进度流未能建立（${streamFailText.value}），任务仍在进行`
    }
    if (streamFailed.value === 'broken') {
      return streamFailText.value ? `进度流已断开（${streamFailText.value}）` : '进度流已断开'
    }
    if (streamEnded.value) return '进度已全部到达'
    return ''
  })

  /** 流端点接入失败的结论句（与进度对话框同一张映射：状态码是排障线索）。 */
  function streamOpenConclusion(status: number): string {
    if (status === 401) return '登录状态已失效'
    if (status === 403) return '没有接入该进度流的权限'
    if (status === 404) return '该指令已不存在或会话已过期'
    if (status === 409) return '该进度会话已有连接（如入口页的进度对话框开着）'
    return '进度流未能建立'
  }

  function errMsg(e: unknown, fallback: string): string {
    const msg = (e as { message?: string } | null | undefined)?.message
    return msg && msg.trim() !== '' ? msg : fallback
  }

  /** 把折叠器的当前快照换进响应式视图（换数组引用驱动重渲染，与进度对话框同手法）。 */
  function syncLayerView(feed: PullFeed): void {
    layerView.value = {
      layers: feed.layers.slice(),
      note: feed.note,
      movedBytes: feed.downloadedBytes,
      totalBytes: feed.totalBytes,
      doneLayers: feed.doneLayers
    }
  }

  function openStream(signal: AbortSignal): Promise<Response> {
    switch (family.value) {
      case 'build':
        return openDockerBuildStream(props.hostId, props.cmdRef, signal)
      case 'push':
        return openDockerPushStream(props.hostId, props.cmdRef, signal)
      default:
        return openDockerPullStream(props.hostId, props.cmdRef, signal)
    }
  }

  async function runStream(): Promise<void> {
    const seq = ++readSeq
    const controller = new AbortController()
    streamAbort = controller
    try {
      const res = await openStream(controller.signal)
      if (seq !== readSeq) return
      if (!res.ok || !res.body) {
        // 接入失败（401/403/404/409…）：这条连接没建立过，不会触发任何服务端动作 ——
        // 任务仍在跑，任务行随任务页轮询走向终态，不打断任何东西。
        streamFailed.value = 'open'
        streamFailText.value = streamOpenConclusion(res.status)
        return
      }
      const feed = family.value === 'build' ? createBuildFeed() : createFeed()
      const reader = res.body.getReader()
      const dec = new TextDecoder('utf-8')
      for (;;) {
        const { done, value } = await reader.read()
        if (seq !== readSeq) return
        if (done) break
        pushChunk(feed, dec.decode(value, { stream: true }))
        if (feed.eof) break
      }
      if (seq !== readSeq) return
      // 收尾：把解码器里的残留字节冲进折叠器（终态行可能正好被切在 chunk 尾）。
      pushChunk(feed, dec.decode())
      if (feed.eof) {
        streamEnded.value = true
      } else {
        // 读尽但没见 eof：网络层收口、应用层没收官 —— 视同断流（只是不再观看：
        // 任务照常跑完，结论以任务行为准）。
        streamFailed.value = 'broken'
        streamFailText.value = ''
      }
    } catch (e) {
      if (seq !== readSeq) return
      // 主动断开（收起/卸载）不是故障：流是被自己掐断的 —— 掐断的是观看。
      if ((e as { name?: string })?.name === 'AbortError') return
      streamFailed.value = 'broken'
      streamFailText.value = errMsg(e, '进度流连接中断')
    } finally {
      if (streamAbort === controller) streamAbort = null
    }
  }

  /** 一个 chunk 进折叠器 + 同步到视图（两族的折叠器接口一致，只有视图字段不同）。 */
  function pushChunk(feed: ReturnType<typeof createBuildFeed> | PullFeed, chunk: string): void {
    feed.pushRaw(chunk)
    if (family.value === 'build') {
      const f = feed as ReturnType<typeof createBuildFeed>
      buildLines.value = f.lines.slice()
      buildDroppedLines.value = f.droppedLines
      void nextTick(() => {
        const el = linesEl.value
        if (el) el.scrollTop = el.scrollHeight
      })
      return
    }
    syncLayerView(feed as PullFeed)
  }

  function createFeed(): PullFeed {
    return family.value === 'push' ? createPushFeed() : createPullFeed()
  }

  /**
   * 取消（显式动作，独立于观看）：调取消端点，成败就地给结论句；流与任务行的
   * 轮询都照常 —— 结局由服务端结算（真被截止 = 任务行出现「已取消」结论句）。
   */
  async function cancelTask(): Promise<void> {
    if (cancelRequested.value || canceling.value) return
    canceling.value = true
    cancelError.value = ''
    try {
      await cancelDockerCmd(props.hostId, props.cmdRef)
      cancelRequested.value = true
    } catch (e) {
      cancelError.value = errMsg(e, '取消请求未发出，请稍后重试')
    } finally {
      canceling.value = false
    }
  }

  onMounted(() => {
    void runStream()
  })

  /** 卸载即断流 = **只是停止观看**（core 只解除接入，不下发 cancel；再展开即重接、
   * 断线期间的帧在会话缓冲里等着）：收起行、离开页面（组件销毁）都走这里。幂等。 */
  function abortStream(): void {
    readSeq++
    streamAbort?.abort()
    streamAbort = null
  }

  onBeforeUnmount(abortStream)
</script>

<style lang="scss" scoped>
  @use '@styles/core/breakpoints.scss' as *;

  // 内联进度区：嵌在任务行下方的一格缩进块（行内容已经说了目标，这里只剩层级账目）。
  .tp {
    margin: 4px 0 2px;
    padding: 8px 10px;
    border-radius: 4px;
    background: var(--el-fill-color-light);

    &__note,
    &__summary,
    &__empty,
    &__hint,
    &__error,
    &__collapse-note {
      margin: 0 0 4px;
      font-size: 12px;
      line-height: 1.6;
    }

    &__note {
      color: var(--el-text-color-secondary);
      word-break: break-all;
    }

    &__summary {
      color: var(--el-text-color-regular);
    }

    &__empty,
    &__hint {
      color: var(--el-text-color-secondary);
    }

    // 取消请求失败的就地结论句（与进度对话框同一形态：红字）。
    &__error {
      margin-bottom: 0;
      // 文字对比度 AA（收尾批）：原 el-color-danger（白底 3.27）改走 token。
      color: var(--aa-danger-text);
    }

    // 操作行：取消按钮 + 收起语义的一句硬事实（收起就是收起，与取消是两件事）。
    // 收起说明用 regular 档（AA：placeholder 档 #a8abb2 对浅灰底仅 ≈2.1:1，升档
    // 口径与终审 QA D2 同批；「最弱化」由位置与字号承载，不再用低对比色承载）。
    &__ops {
      display: flex;
      flex-wrap: wrap;
      gap: 8px;
      align-items: center;
      margin: 6px 0 2px;
    }

    &__collapse-note {
      margin: 0;
      color: var(--el-text-color-regular);
    }
  }

  // 层列表：行式布局与进度对话框的层表同构（短 id / 状态 / 条 / 字节）。
  .tp-layers {
    max-height: 200px;
    margin: 4px 0 6px;
    padding: 0;
    list-style: none;
    overflow-y: auto;
  }

  .tp-layer {
    display: flex;
    gap: 10px;
    align-items: center;
    padding: 4px 0;
    font-size: 12px;

    &__id {
      // 等宽 12 位：ch 单位在等宽字体下就是一位，层 id 之间可纵向对读。
      flex: none;
      width: 12ch;
      overflow: hidden;
      color: var(--el-text-color-secondary);
      font-family: var(--el-font-family-mono, ui-monospace, 'SFMono-Regular', Consolas, monospace);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    &__status {
      flex: none;
      width: 9.5em;
      overflow: hidden;
      color: var(--el-text-color-regular);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    &__bar {
      flex: 1;
      min-width: 48px;
    }

    &__bytes {
      flex: none;
      color: var(--el-text-color-secondary);
      font-family: var(--el-font-family-mono, ui-monospace, 'SFMono-Regular', Consolas, monospace);
      text-align: right;
      white-space: nowrap;
    }

    &__check {
      flex: none;
      font-size: 14px;
      color: var(--el-color-success);
    }

    // 终态行整体弱化（完成的事不与进行中的事抢注意力）。
    &.is-done &__status {
      color: var(--el-text-color-secondary);
    }
  }

  // 构建播报：限高滚动的等宽清单（与构建对话框的 bp-lines 同构）。
  .tp-lines {
    max-height: 200px;
    margin: 4px 0 6px;
    padding: 6px 8px;
    list-style: none;
    overflow-y: auto;
    border-radius: 4px;
    background: var(--el-fill-color-lighter, var(--el-fill-color-light));
  }

  .tp-line {
    display: flex;
    gap: 8px;
    font-size: 12px;
    line-height: 1.6;

    &__id {
      flex: none;
      color: var(--el-text-color-secondary);
      font-family: var(--el-font-family-mono, ui-monospace, 'SFMono-Regular', Consolas, monospace);
    }

    &__text {
      min-width: 0;
      color: var(--el-text-color-regular);
      font-family: var(--el-font-family-mono, ui-monospace, 'SFMono-Regular', Consolas, monospace);
      word-break: break-all;
      white-space: pre-wrap;
    }

    // 步骤行（带 id 的 legacy 行）：正文是主信息，等宽同款。
    &.is-step &__text {
      color: var(--el-text-color-primary);
    }
  }

  // 平板竖屏以下（整页窄屏）：字节列让位给进度条（条是主信息）。
  @include respond-below('tablet') {
    .tp-layer__status {
      width: 7em;
    }

    .tp-layer__bytes {
      display: none;
    }
  }
</style>