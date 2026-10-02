<template>
  <!-- 单根（single-root 守卫扫全模块的 .vue）：展开区本体是唯一根。 -->
  <div class="tpp">
    <!-- 阶段消息行（"Pulling from …" / "Digest: …" / "Status: …"）的最新一条：
         与 pull-progress-dialog 同一口径 —— 全量消息历史属于日志/活动流。 -->
    <p v-if="feedView.note" class="tpp__note">{{ feedView.note }}</p>
    <p v-if="summaryText" class="tpp__summary">{{ summaryText }}</p>

    <ul v-if="feedView.layers.length" class="tpp-layers">
      <li
        v-for="layer in feedView.layers"
        :key="layer.id"
        class="tpp-layer"
        :class="{ 'is-done': layer.done }"
      >
        <span class="tpp-layer__id" :title="layer.id">{{ layer.shortId }}</span>
        <span class="tpp-layer__status" :title="layer.status">{{ layer.status }}</span>
        <!-- 进度条与字节文案：与 pull-progress-dialog 逐字同口径（无 total 走不确定态，
             层终态后不再画条）。 -->
        <ElProgress
          v-if="!layer.done"
          class="tpp-layer__bar"
          :percentage="layer.total > 0 ? layerPercent(layer.current, layer.total) : 100"
          :indeterminate="layer.total <= 0"
          :stroke-width="6"
          :show-text="false"
        />
        <span v-if="!layer.done && layer.total > 0" class="tpp-layer__bytes">
          {{ formatPullBytes(layer.current) }} / {{ formatPullBytes(layer.total) }}
        </span>
        <ArtSvgIcon v-if="layer.done" icon="ri:check-line" class="tpp-layer__check" />
      </li>
    </ul>
    <p v-else class="tpp__empty">正在等待进度…</p>

    <!-- 收尾提示（接入失败/断开/eof）：任务页的 5s 轮询会把条目带向终态，这里只说
         「流这边怎么了」，结论句以任务行的 summary 为准。 -->
    <p v-if="hint" class="tpp__hint">{{ hint }}</p>
    <!-- 取消语义的出口说明：本组件只读无输入态，收起（卸载）即 Abort —— 但断开
         这条流只是不再观看（core 会 best-effort 下发 cancel；cancel 与完成有竞态，
         是否真被截止按拉取的**实际结局**结算，见后端 pull_progress.go 的竞态注释），
         措辞不承诺因果 —— 结局以任务行结论为准。 -->
    <p class="tpp__cancel">收起后不再显示进度；这场拉取是否被截止，以任务行结论为准。</p>
  </div>
</template>

<script setup lang="ts">
  /**
   * 任务中心里的拉取进度内联视图（6b）：把 pull-progress-dialog 的进度态抽出来
   * **只读复用** —— 同一条流端点（cmds/:ref/pull）与同一个折叠器（utils/pull.ts），
   * 没有 input/result 态：发起在镜像页（输入态的输入面在那边），任务行自己会随
   * 任务页轮询走向终态，本组件只负责「展开时逐层看得见」。
   *
   * 生命周期是本组件的关键取舍（与 pull-progress-dialog 的分岔）：
   *   - **卸载即 Abort**：收起行/离开页面（组件卸载）断流 —— 端点契约是「客户端
   *     断开 → 服务端 best-effort 下发 cancel」（后端 6b 的取消纪律：流任务复用
   *     进度流 Abort，不新造取消动作）；但 cancel 与完成有竞态，终态按拉取的
   *     **实际结局**结算（pull_progress.go 的「取消与完成的竞态」注释），故断面
   *     文案不承诺「断开 = 取消」（是否被截止以任务行结论为准）；
   *   - **失活不断流**：keep-alive 失活（任务页被 worktab 缓存）时组件仍挂载，
   *     连接保持 —— 这里断流会把一场与服务端无关的「切个页签」变成一次断开；
   *     流是拉取进度的生命线，监控窗口关掉不等于要放弃观看。
   *     （任务页的 5s 轮询停表是另一回事：那是页面开销纪律，与流的存续无关。）
   */
  import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
  import { ElProgress } from 'element-plus'
  import { openDockerPullStream } from '../api'
  import {
    createPullFeed,
    formatPullBytes,
    layerPercent,
    type PullFeed,
    type PullLayer
  } from '../utils/pull'

  defineOptions({ name: 'DockerTaskPullProgress' })

  const props = defineProps<{
    /** 目标主机（一次指令只属于受理它的那台主机）。 */
    hostId: string
    /** 指令号（进度流端点的钥匙）。 */
    cmdRef: string
  }>()

  interface FeedView {
    layers: PullLayer[]
    note: string
    downloadedBytes: number
    totalBytes: number
    doneLayers: number
  }

  const feedView = ref<FeedView>({
    layers: [],
    note: '',
    downloadedBytes: 0,
    totalBytes: 0,
    doneLayers: 0
  })
  const streamEnded = ref(false)
  /** '' = 流还连着；'open' = 接入失败（拉取仍在服务端跑）；'broken' = 连上后断开（是否被截止按实际结局结算，不承诺因果）。 */
  const streamFailed = ref<'' | 'open' | 'broken'>('')
  const streamFailText = ref('')

  /** 生命周期序号：卸载让在飞的读循环失效（照 pull-progress-dialog 的 pullSeq）。 */
  let readSeq = 0
  let streamAbort: AbortController | null = null

  const summaryText = computed(() => {
    const f = feedView.value
    if (f.layers.length === 0) return ''
    const parts: string[] = []
    if (f.totalBytes > 0) {
      parts.push(`已下载 ${formatPullBytes(f.downloadedBytes)} / ${formatPullBytes(f.totalBytes)}`)
    }
    parts.push(`${f.doneLayers}/${f.layers.length} 层完成`)
    return parts.join(' · ')
  })

  const hint = computed(() => {
    if (streamFailed.value === 'open') {
      return `进度流未能建立（${streamFailText.value}），拉取仍在进行`
    }
    if (streamFailed.value === 'broken') {
      return streamFailText.value ? `进度流已断开（${streamFailText.value}）` : '进度流已断开'
    }
    if (streamEnded.value) return '进度已全部到达'
    return ''
  })

  /** 流端点接入失败的结论句（与 pull-progress-dialog 同一张映射：状态码是排障线索）。 */
  function streamOpenConclusion(status: number): string {
    if (status === 401) return '登录状态已失效'
    if (status === 403) return '没有接入该进度流的权限'
    if (status === 404) return '该指令已不存在或会话已过期'
    if (status === 409) return '该进度会话已有连接（如镜像页进度对话框开着）'
    return '进度流未能建立'
  }

  function errMsg(e: unknown, fallback: string): string {
    const msg = (e as { message?: string })?.message
    return msg && msg.trim() !== '' ? msg : fallback
  }

  /** 把折叠器的当前快照换进响应式视图（换数组引用驱动重渲染，与 container-stats 同手法）。 */
  function syncFeed(feed: PullFeed): void {
    feedView.value = {
      layers: feed.layers.slice(),
      note: feed.note,
      downloadedBytes: feed.downloadedBytes,
      totalBytes: feed.totalBytes,
      doneLayers: feed.doneLayers
    }
  }

  async function runStream(): Promise<void> {
    const seq = ++readSeq
    const controller = new AbortController()
    streamAbort = controller
    try {
      const res = await openDockerPullStream(props.hostId, props.cmdRef, controller.signal)
      if (seq !== readSeq) return
      if (!res.ok || !res.body) {
        // 接入失败（401/403/404/409…）：这条连接没建立过，不会触发服务端取消 ——
        // 拉取仍在跑，任务行随任务页轮询走向终态，不打断任何东西。
        streamFailed.value = 'open'
        streamFailText.value = streamOpenConclusion(res.status)
        return
      }
      const feed = createPullFeed()
      const reader = res.body.getReader()
      const dec = new TextDecoder('utf-8')
      for (;;) {
        const { done, value } = await reader.read()
        if (seq !== readSeq) return
        if (done) break
        feed.pushRaw(dec.decode(value, { stream: true }))
        syncFeed(feed)
        if (feed.eof) break
      }
      if (seq !== readSeq) return
      // 收尾：把解码器里的残留字节冲进折叠器（终态行可能正好被切在 chunk 尾）。
      feed.pushRaw(dec.decode())
      syncFeed(feed)
      if (feed.eof) {
        streamEnded.value = true
      } else {
        // 读尽但没见 eof：网络层收口、应用层没收官 —— 视同断流（不再观看；
        // 是否被截止按拉取的实际结局结算）。
        streamFailed.value = 'broken'
        streamFailText.value = ''
      }
    } catch (e) {
      if (seq !== readSeq) return
      // 主动断开（收起/卸载）不是故障：流是被自己掐断的。
      if ((e as { name?: string })?.name === 'AbortError') return
      streamFailed.value = 'broken'
      streamFailText.value = errMsg(e, '进度流连接中断')
    } finally {
      if (streamAbort === controller) streamAbort = null
    }
  }

  onMounted(() => {
    void runStream()
  })

  /** 卸载即断流（core 会就此 best-effort 下发 cancel；是否真被截止按拉取的实际
   * 结局结算，不承诺因果）：收起行、离开页面（组件销毁）都走这里。幂等。 */
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
  .tpp {
    margin: 4px 0 2px;
    padding: 8px 10px;
    border-radius: 4px;
    background: var(--el-fill-color-light);

    &__note,
    &__summary,
    &__empty,
    &__hint,
    &__cancel {
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

    // 取消语义的出口说明：最后一句、最弱化 —— 读它的人是准备收起的人。
    // 对比度 AA（终审 QA D2 同源 #a8abb2 盘点）：placeholder 档（#a8abb2）对浅灰底
    // 仅 ≈2.1:1 —— 升到 regular 档（#606266 对浅灰底 ≈5.7:1；暗色只升不降）。
    // 「最弱化」的表意由位置与字号承载，不再用低对比色承载。只换色值，形态不动。
    &__cancel {
      margin-bottom: 0;
      color: var(--el-text-color-regular);
    }
  }

  // 层列表：行式布局与 pull-progress-dialog 的 pp-layer 同构（短 id / 状态 / 条 / 字节）。
  .tpp-layers {
    max-height: 200px;
    margin: 4px 0 6px;
    padding: 0;
    list-style: none;
    overflow-y: auto;
  }

  .tpp-layer {
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

  // 平板竖屏以下（整页窄屏）：字节列让位给进度条（条是主信息）。
  @include respond-below('tablet') {
    .tpp-layer__status {
      width: 7em;
    }

    .tpp-layer__bytes {
      display: none;
    }
  }
</style>
