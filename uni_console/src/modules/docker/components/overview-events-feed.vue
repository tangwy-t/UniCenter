<template>
  <!-- 单根（single-root 守卫在库：布局的 Transition 只支持单根，双根切页白屏）。 -->
  <div class="dov-card dov-feed">
    <!-- 工具行：连接状态（结论句）+ 致命失败的重试 + 账目口径。 -->
    <div class="dov-feed__head">
      <span class="dov-feed__status" :class="`is-${phase}`" role="status">
        <span class="dov-feed__dot" aria-hidden="true" />
        {{ statusText }}
      </span>
      <ElButton
        v-if="phase === 'stopped' && stopReason"
        size="small"
        class="dov-feed__retry"
        @click="retry"
        >重试</ElButton
      >
      <span class="dov-feed__meta">{{ hint }}</span>
    </div>

    <!-- 滚动区：live tail 的身体。新事件落底 + 自动贴底；上滚即暂停（见 onScroll）。 -->
    <div ref="bodyRef" class="dov-feed__body" @scroll="onScroll">
      <!-- 暂停条：吸顶常驻（即使新事件为 0 —— 「回到底部」的出口必须一直在）；
           点击整条或按钮都恢复（与 log-viewer 的「继续滚动」同一语义）。 -->
      <div v-if="paused" class="dov-feed__paused" @click="resume">
        <span class="dov-feed__paused-text">{{ pausedText }}</span>
        <ElButton size="small" class="dov-feed__resume" @click.stop="resume">回到底部</ElButton>
      </div>

      <ul v-if="rows.length" class="dov-feed__list">
        <li v-for="r in rows" :key="r.item.seq" class="dov-feed__row">
          <ArtSvgIcon :icon="r.icon" class="dov-feed__type" :class="`is-${r.tone}`" />
          <span class="dov-feed__action">{{ r.action }}</span>
          <span class="dov-feed__actor dov-mono" :title="r.actor">{{ r.actor }}</span>
          <span class="dov-feed__host" :title="`主机 ${r.host}`">{{ r.host }}</span>
          <!-- 相对时间随 1 秒心跳走；悬停给绝对时刻（title，最小侵入 —— 不加列）。
               两个时间同源（r.tSec 由条目毫秒戳除千得来，见行模型）。 -->
          <span class="dov-feed__time" :title="r.clock">{{
            eventRelativeTime(r.tSec, nowSec)
          }}</span>
        </li>
      </ul>
      <!-- 零条目时的说明：连接中 / 已断开 / 舰队安静各有其句，不给一块空框让人猜。 -->
      <div v-else class="dov-feed__note">{{ bodyNote }}</div>
    </div>
  </div>
</template>

<script setup lang="ts">
  /**
   * 活动流面板（六期 · 切片 3b 前端半边）：跨主机 docker 事件的实时信息流，回答
   * 「刚才发生了什么」。数据一条流（GET /docker/events，NDJSON），连上先回放各
   * 主机最近 50 条再进实时；行解析 / 排序窗口 / 重连去重 / action 中文化都在
   * utils/events 的纯函数里（那里有测试），本组件只做三件事：
   *
   *   1. 流生命周期：连接 = 页面激活期 —— 照抄总览自动刷新的 keep-alive 纪律
   *      （失活即断流、激活重连），外加断流自动重连（指数退避封顶）与致命失败
   *      （401/403）的停机结论句；
   *   2. live tail：新事件落底 + 自动贴底滚动；用户上滚看历史即暂停自动滚动，
   *      吸顶条报「新事件 N 条」，点「回到底部」恢复（log-viewer 的滚动暂停先例
   *      的同款语义：暂停只停自动滚动，不停流 —— 暂停期间到达的事件照常进窗口，
   *      只是不顶走阅读位置）；
   *   3. 相对时间的 1 秒节流刷新（tick 只动一个 nowSec ref，百行文本重算是
   *      可忽略的成本，换掉的是「09:41:23」这类要心算的绝对时刻）；悬停时间列
   *      给绝对时刻（title）——「多久之前」与「哪一刻」两个问题都要有答案，
   *      后者是排障对账的引用钥匙。注意条目 t 是**毫秒**戳（core 透传 agent 的
   *      UnixMilli），相对文案前必须除千（行模型的 tSec），否则恒「刚刚」。
   */
  import {
    computed,
    nextTick,
    onActivated,
    onBeforeUnmount,
    onDeactivated,
    onMounted,
    ref,
    watch
  } from 'vue'
  import { ElButton } from 'element-plus'
  import { openDockerEventsStream } from '../api'
  import {
    createEventsFeed,
    eventActionText,
    eventClockTime,
    eventFeedHint,
    eventRelativeTime,
    eventTypeMeta,
    type DockerEventItem
  } from '../utils/events'

  defineOptions({ name: 'DockerOverviewEventsFeed' })

  type Phase = 'connecting' | 'live' | 'reconnecting' | 'stopped'

  // ── 显示状态 ─────────────────────────────────────────────
  const phase = ref<Phase>('connecting')
  /** 致命失败（401/403）的结论句；空 = 不是致命失败（重连中/已停非致命）。 */
  const stopReason = ref('')

  const feed = createEventsFeed()
  /** 窗口内容（旧 → 新）。每次 pushRaw 后换数组引用驱动重渲染（不深改元素）。 */
  const entries = ref<readonly DockerEventItem[]>([])
  const totalSeen = ref(0)
  const hint = computed(() => eventFeedHint(totalSeen.value, entries.value.length))

  /** 相对时间的「现在」：1 秒一跳（tick 在生命周期里起停，见 startTick）。 */
  const nowSec = ref(Math.floor(Date.now() / 1000))

  // ── live tail 的滚动语义 ─────────────────────────────────
  /** 用户上滚 = 想看历史 → 暂停自动滚动（滚回底部或点「回到底部」复位）。 */
  const paused = ref(false)
  /** 暂停那一刻的累计数：暂停期间新事件 N 条 = 当前累计 − 它。 */
  const pausedBase = ref(0)
  const newCount = computed(() =>
    paused.value ? Math.max(0, totalSeen.value - pausedBase.value) : 0
  )
  const pausedText = computed(() =>
    newCount.value > 0 ? `已暂停 · 新事件 ${newCount.value} 条` : '已暂停 · 正在查看历史'
  )

  const bodyRef = ref<HTMLElement | null>(null)

  /** 行的渲染模型：图标 / 色槽 / 文案在纯函数里算一次，模板不再逐行调函数。 */
  interface EventRow {
    item: DockerEventItem
    icon: string
    tone: string
    action: string
    actor: string
    host: string
    /** 相对时间的秒值（条目 t 是毫秒戳，这里除千 —— 曾按秒直减，「多久之前」恒「刚刚」）。 */
    tSec: number
    /** 绝对时刻（悬停 title）：随条目定型，不随 1 秒心跳重算。 */
    clock: string
  }
  const rows = computed<EventRow[]>(() =>
    entries.value.map((e) => {
      const meta = eventTypeMeta(e.type)
      return {
        item: e,
        icon: meta.icon,
        tone: meta.tone,
        action: eventActionText(e.action),
        // 名字优先，其次短 id（sha256 前缀剥掉，容器 id 与镜像摘要同一形态）
        actor: e.actorName || e.actorId.replace(/^sha256:/, '').slice(0, 12) || '—',
        host: e.hostname || `主机 ${e.hostId}`,
        tSec: e.t / 1000,
        clock: eventClockTime(e.t)
      }
    })
  )

  const statusText = computed(() => {
    if (phase.value === 'stopped')
      return stopReason.value ? `已停止 · ${stopReason.value}` : '已停止'
    if (phase.value === 'reconnecting') return '已断开 · 重连中'
    if (phase.value === 'connecting') return '连接中…'
    return '实时'
  })

  /** 零条目时的说明句（连接中 / 断开 / 舰队安静各说各的，不给空框）。 */
  const bodyNote = computed(() => {
    if (phase.value === 'connecting') return '正在接入事件流…'
    if (phase.value === 'reconnecting') return '已断开 · 重连中…'
    if (phase.value === 'stopped') return stopReason.value || '事件流已停止。'
    return '舰队很安静 —— 回放与实时均无事件'
  })

  // ── 流生命周期（连接 = 页面激活期）─────────────────────────
  let connSeq = 0
  /** 断流手柄：非空 = 连接在飞（connecting/live）；重连等待期由 retryTimer 表达。 */
  let streamAbort: AbortController | null = null
  let retryTimer: number | undefined
  let tickTimer: number | undefined
  /** 重连退避档位（1s → 2s → 4s → … 封顶 10s；长稳过的流归零，见 scheduleReconnect）。 */
  let attempts = 0
  /** 本轮连接进入 live 的时刻（判「长稳」用；0 = 从未 live 过）。 */
  let liveSince = 0

  const RECONNECT_BASE_MS = 1_000
  const RECONNECT_MAX_MS = 10_000
  /** 连接存活超过该时长视为「长稳」：断掉后退避归零（不然挂了半小时的流断一次还要从 8s 爬起）。 */
  const STABLE_STREAM_MS = 30_000
  /** 贴底判定余量：亚像素滚动（缩放/触控板）到底时仍会留 1~2px（log-viewer 同款）。 */
  const SCROLL_BOTTOM_EPS = 8

  function clearRetry() {
    if (retryTimer !== undefined) {
      clearTimeout(retryTimer)
      retryTimer = undefined
    }
  }

  /** 接入失败的结论句：只有换不回结果的失败（登录/权限）才值得停下，其余重试。 */
  function openConclusion(status: number): string {
    if (status === 401) return '登录状态已失效，请重新登录后再试'
    if (status === 403) return '没有接入事件流的权限'
    return ''
  }

  /** 把 feed 的窗口同步进渲染状态（pushRaw 之后调用）。 */
  function syncView() {
    entries.value = feed.entries
    totalSeen.value = feed.totalSeen
  }

  async function connect() {
    const seq = ++connSeq
    phase.value = 'connecting'
    stopReason.value = ''
    const controller = new AbortController()
    streamAbort = controller
    try {
      const res = await openDockerEventsStream(controller.signal)
      if (seq !== connSeq) return
      if (!res.ok || !res.body) {
        const fatal = openConclusion(res.status)
        if (fatal) {
          // 401/403：重试换不回结果（要的是用户动作），停机 + 结论句 + 手动重试。
          phase.value = 'stopped'
          stopReason.value = fatal
          return
        }
        scheduleReconnect()
        return
      }
      phase.value = 'live'
      liveSince = Date.now()

      const reader = res.body.getReader()
      const decoder = new TextDecoder('utf-8')
      for (;;) {
        const { done, value } = await reader.read()
        // seq 失配 = 期间发生过 stop()：读循环立刻退（在飞的 reader 由 abort 收尾）
        if (seq !== connSeq || done) break
        feed.pushRaw(decoder.decode(value, { stream: true }))
        syncView()
      }
      if (seq !== connSeq) return
      // 流被对端收尾（core 重启 / 网关断开）：不是错误，但活动流的使命是「一直亮着」
      // → 走重连（回放会重放最近 50 条/主机，去重守卫在 feed 里）。
      feed.pushRaw(decoder.decode())
      syncView()
      scheduleReconnect()
    } catch {
      if (seq !== connSeq) return
      // 主动断开（失活/停机）的 AbortError 会被 seq 守卫拦在这之前；到这里的是
      // 网络类错误 → 重连。
      scheduleReconnect()
    } finally {
      if (seq === connSeq) streamAbort = null
    }
  }

  function scheduleReconnect() {
    // 长稳过的流（live 超 30s）才归零退避；从未 live 过（打开就失败）不归零 ——
    // 不然「服务端一直拒连」会被 1 秒一次地敲，退避就没了意义。
    if (liveSince > 0 && Date.now() - liveSince >= STABLE_STREAM_MS) attempts = 0
    attempts++
    const delay = Math.min(RECONNECT_BASE_MS * 2 ** (attempts - 1), RECONNECT_MAX_MS)
    phase.value = 'reconnecting'
    clearRetry()
    retryTimer = window.setTimeout(() => {
      retryTimer = undefined
      void connect()
    }, delay)
  }

  /** 断流（失活 / 卸载 / 手动重试前；幂等 —— 没有在飞就不扰动）。 */
  function stop() {
    connSeq++ // 在飞的连接与读循环全部失效（AbortError 也不会被当成断流去重连）
    clearRetry()
    stopTick()
    const controller = streamAbort
    streamAbort = null
    if (controller) controller.abort()
    if (phase.value !== 'stopped') phase.value = 'stopped'
  }

  /** 激活期接入（幂等：连接在飞或重连等待中则不动作 —— onMounted 与 onActivated 会都走这里）。 */
  function start() {
    if (streamAbort !== null || retryTimer !== undefined) return
    void connect()
  }

  /** 手动重试（致命停机后）：退避归零，立即接入。 */
  function retry() {
    if (phase.value !== 'stopped') return
    attempts = 0
    void connect()
  }

  // ── 相对时间的 1 秒节流 ─────────────────────────────────
  function startTick() {
    stopTick()
    tickTimer = window.setInterval(() => {
      // 后台标签页的 setInterval 会被浏览器限流：隐藏期间跳过 tick（与总览自动
      // 刷新同一纪律），回到前台后最多 1 秒内追上。
      if (!document.hidden) nowSec.value = Math.floor(Date.now() / 1000)
    }, 1_000)
  }

  function stopTick() {
    if (tickTimer !== undefined) {
      clearInterval(tickTimer)
      tickTimer = undefined
    }
  }

  // ── 滚动（log-viewer 的暂停先例同款语义）─────────────────
  async function scrollToBottom() {
    await nextTick()
    const el = bodyRef.value
    if (el) el.scrollTop = el.scrollHeight
  }

  /**
   * 上滚即暂停。暂停只停自动滚动，不停流：新事件照常进窗口、照常计数 —— 落底的
   * 行在阅读位置下方，不顶走正在读的内容；「新事件 N 条」给出回来的理由。
   */
  function onScroll() {
    const el = bodyRef.value
    if (!el) return
    const atBottom = el.scrollTop + el.clientHeight >= el.scrollHeight - SCROLL_BOTTOM_EPS
    if (atBottom) {
      paused.value = false
      return
    }
    if (!paused.value) {
      paused.value = true
      pausedBase.value = totalSeen.value
    }
  }

  function resume() {
    paused.value = false
    void scrollToBottom()
  }

  // 新事件到达时贴底 —— 除非已暂停：把正在读的位置顶走是不可接受的。
  watch(entries, () => {
    if (!paused.value) void scrollToBottom()
  })

  // ── 挂载 / keep-alive 失活 / 卸载 ────────────────────────
  // 清理要覆盖两种「离开」：整页卸载（普通路由切换）与 keep-alive 失活（worktab
  // 缓存页面实例）—— 只挂 onBeforeUnmount 的话，被缓存的页面会带着流一直跑，
  // 而这条流在服务端是常驻订阅的引用计数（最后一个断开才全量退订），泄漏代价
  // 尤其高（总览自动刷新的同一层失活钩子）。
  onMounted(() => {
    startTick()
    start()
  })

  onBeforeUnmount(stop)
  onDeactivated(stop)

  // 从缓存回来说明刚才看不见：重连事件流并恢复时间心跳（连接生命周期 = 激活期）。
  onActivated(() => {
    startTick()
    start()
  })
</script>

<style lang="scss" scoped>
  /* 令牌数值复制自 monitor-tokens（经本模块 views/overview-tokens，见其文件头注释） */
  @use '../views/overview-tokens' as t;
  @use '@styles/core/breakpoints.scss' as *;

  // 「重试」等默认档按钮的主色文字对比度 AA：病灶与处方见 overview-tokens
  // 的 primary-text-aa（终审 QA D2·浅色实测 3.68:1）。
  @include t.primary-text-aa;

  @include t.pulse-keyframes;

  .dov-feed {
    @include t.card;
    @include t.rise;
  }

  .dark .dov-feed {
    @include t.card-dark;
  }

  .dov-mono {
    font-family: var(--el-font-family-mono, monospace);
  }

  /* ---------- 工具行：状态 + 重试 + 账目 ---------- */
  .dov-feed__head {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px 10px;
    margin-bottom: 10px;
  }

  .dov-feed__status {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 12px;

    &.is-live {
      color: var(--el-color-success);
    }

    &.is-connecting {
      color: var(--el-text-color-secondary);
    }

    &.is-reconnecting {
      color: var(--el-color-warning);
    }

    &.is-stopped {
      color: var(--el-color-danger);
    }
  }

  .dov-feed__dot {
    flex: none;
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: currentColor;
  }

  // 「实时」的呼吸点（2s，与页头 live-dot 同一节奏；投影色由关键帧从 --art-success 派生）
  .dov-feed__status.is-live .dov-feed__dot {
    @include t.live-dot;
  }

  .dov-feed__retry {
    margin-left: 2px;
  }

  .dov-feed__meta {
    margin-left: auto;
    // 账目口径句的对比度：placeholder 档（#a8abb2）在浅色主题下于白卡实测
    // 2.3:1，远低于 AA 4.5:1 —— 升到 regular 档（#606266 于白卡约 6.1:1；暗色
    // 主题的 regular 是 85% 白，同样只升不降）。只换色值，形态不动。
    color: var(--el-text-color-regular);
    font-size: 12px;
    white-space: nowrap;
  }

  /* ---------- 滚动区（live tail 的身体） ---------- */
  .dov-feed__body {
    position: relative;
    min-height: 8rem; // 空态也要有存在感，不给一条细缝
    max-height: 24rem; // 与异常表 24rem 同一密度：封顶的清单不把页面撑到两屏，区内滚动
    overflow-y: auto;
    border: 1px solid var(--default-border);
    border-radius: 8px;
    background: var(--el-fill-color-extra-light);
  }

  /* ---------- 暂停条（吸顶；整条可点） ---------- */
  .dov-feed__paused {
    position: sticky;
    top: 0;
    z-index: 1;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px 8px;
    padding: 4px 10px;
    cursor: pointer;
    background: color-mix(in srgb, var(--el-color-warning) 10%, transparent);
    border-bottom: 1px solid var(--default-border);
  }

  .dov-feed__paused-text {
    color: var(--el-color-warning);
    font-size: 12px;
  }

  .dov-feed__resume {
    margin-left: auto;
  }

  /* ---------- 行 ---------- */
  .dov-feed__list {
    margin: 0;
    padding: 0;
    list-style: none;
  }

  .dov-feed__row {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px 8px;
    padding: 5px 10px;
    border-bottom: 1px solid var(--el-border-color-lighter);

    &:last-child {
      border-bottom: none;
    }
  }

  // 四类资源的语义色槽（未知走中性）：图标与 action 同槽同色，色即类别。
  .dov-feed__type {
    flex: none;
    font-size: 14px;

    &.is-container {
      color: var(--el-color-primary);
    }

    &.is-image {
      color: var(--el-color-warning);
    }

    &.is-volume {
      color: var(--el-color-info);
    }

    &.is-network {
      color: var(--el-color-success);
    }

    &.is-other {
      color: var(--el-text-color-secondary);
    }
  }

  .dov-feed__action {
    flex: none;
    color: var(--el-text-color-primary);
    font-size: 13px;
    font-weight: 500;
  }

  // 动作者名（容器名/镜像名）：吃掉剩余宽度，超长省略（全名进 title）
  .dov-feed__actor {
    flex: 1 1 9rem;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--el-text-color-regular);
    font-size: 12px;
  }

  // 主机名标签：小圆角胶囊（清单里叫什么就是什么 —— 归属是 core 注入的）
  .dov-feed__host {
    flex: none;
    max-width: 10rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    padding: 0 7px;
    border-radius: 999px;
    background: var(--el-fill-color);
    color: var(--el-text-color-secondary);
    font-size: 11px;
    line-height: 18px;
  }

  // 相对时间：右对齐 + 表格数字 —— 1 秒一跳的文案在固定槽位里跳，不推挤邻居
  .dov-feed__time {
    flex: none;
    margin-left: auto;
    min-width: 4.5rem;
    text-align: right;
    color: var(--el-text-color-secondary);
    font-size: 12px;
    font-variant-numeric: tabular-nums;
  }

  .dov-feed__note {
    display: flex;
    align-items: center;
    justify-content: center;
    min-height: 8rem;
    color: var(--el-text-color-secondary);
    font-size: 13px;
    text-align: center;
    padding: 0 12px;
  }

  // 窄屏（<768）：行内字段换行收尾 —— 主机标签与时间挤不下时落到第二行，不再互相争抢
  @include respond-below('tablet') {
    .dov-feed__actor {
      flex-basis: 6rem;
    }

    .dov-feed__host {
      max-width: 8rem;
    }
  }

  @include t.reduced-motion('.dov-feed', '.dov-feed__dot');
</style>
