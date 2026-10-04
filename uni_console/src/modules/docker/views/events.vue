<template>
  <!-- 单根（single-root 守卫在库：布局的 Transition 只支持单根，双根切页白屏）。 -->
  <div class="docker-events art-full-height overflow-y-auto">
    <div class="ev-inner p-4 pb-8 md:p-5">
      <!-- ============ 页头：返回 + 图标 + 标题 + 跟随/刷新（对齐其余页面 hero）============ -->
      <div class="ev-hero mb-4 flex flex-wrap items-center gap-3">
        <ArtButtonTable
          icon="ri:arrow-left-line"
          icon-class="bg-g-300/55 text-g-700"
          title="返回上一页"
          @click="back"
        />
        <div class="wkl-hero__icon flex-cc">
          <ArtSvgIcon :icon="pageIcon" />
        </div>
        <div class="min-w-0">
          <h2 class="text-lg font-semibold text-[var(--el-text-color-primary)]">事件流</h2>
          <p class="mt-0.5 flex flex-wrap items-center gap-1.5 text-xs text-g-600">
            <span class="ev-dot" :class="`is-${phase}`" aria-hidden="true" />
            <span>{{ statusText }}</span>
          </p>
        </div>
        <div class="ml-auto flex flex-wrap items-center gap-2">
          <!-- 致命失败（401/403）的重试：与活动流面板同款（重试换不回结果时才停机）。 -->
          <ElButton v-if="phase === 'stopped' && stopReason" size="small" @click="retryStream">
            重试
          </ElButton>
          <!-- 实时跟随 / 暂停：暂停 = **冻结列表**（新事件只计数不插行）——列表是
               「最新在前」的表格，插行会把正在读的位置整段顶下去；恢复 = 把暂停期间
               的 N 条一次并回来。流本身不暂停（暂停只停「列表动」，不停「收事实」）。 -->
          <ElButton size="small" class="ev-follow" @click="togglePause">
            {{ paused ? `继续跟随${pausedCount > 0 ? `（${pausedCount} 条）` : ''}` : '暂停' }}
          </ElButton>
          <ArtButtonTable
            icon="ri:refresh-line"
            iconClass="bg-theme/12 text-theme"
            title="刷新"
            @click="manualRefresh"
          />
        </div>
      </div>

      <!-- ============ 过滤条（主机 / 类型 / 关键字，全部交给服务端）============ -->
      <div class="wkl-card ev-filters mb-4 flex flex-wrap items-center gap-3">
        <span class="ev-filters__label">主机</span>
        <ElSelect v-model="filterHost" class="ev-filters__host" placeholder="全部主机">
          <ElOption label="全部主机" value="" />
          <ElOption v-for="h in hosts" :key="h.id" :label="h.hostname || h.id" :value="h.id" />
        </ElSelect>
        <!-- 类型四档与订阅维度一一对应（core 只订这四类）——「全部」之外没有兜底档，
             未知类型（协议扩类）在列表里原样直显，筛它得等这里加档。 -->
        <ElRadioGroup v-model="filterType" size="small">
          <ElRadioButton value="">全部</ElRadioButton>
          <ElRadioButton value="container">容器</ElRadioButton>
          <ElRadioButton value="image">镜像</ElRadioButton>
          <ElRadioButton value="volume">卷</ElRadioButton>
          <ElRadioButton value="network">网络</ElRadioButton>
        </ElRadioGroup>
        <ElInput
          v-model="filterKeyword"
          class="ev-filters__kw"
          size="small"
          placeholder="对象 / 动作"
          clearable
        />
      </div>

      <!-- ── 首拉加载（还没有任何行可给） ── -->
      <div v-if="state === 'loading'" class="wkl-card ev-state">
        <ElSkeleton :rows="6" animated />
      </div>

      <!-- ── 首拉失败：结论句 + 重试（不显示旧列表 —— 那时还没有旧列表可显示） ── -->
      <div v-else-if="state === 'error'" class="wkl-card ev-state">
        <p class="ev-state__text">事件历史获取失败</p>
        <ElButton size="small" @click="manualRefresh">重试</ElButton>
      </div>

      <!-- ── 空态（「窗口里没有事件」与「筛完了没有」分开说，两种事实不是一句话） ── -->
      <div v-else-if="state === 'empty'" class="wkl-card ev-state">
        <ElEmpty :description="emptyText" />
      </div>

      <!-- ── 正常态：静默失败标注 + 事件表 + 翻页 ── -->
      <div v-else class="wkl-card ev-body">
        <!-- 静默刷新失败：保留最后已知列表并说出口（与列表页「上次数据仍在」同一口径）。 -->
        <p v-if="refreshError" class="ev-refresh-error">刷新失败，正在显示上次结果</p>
        <!-- 暂停提示条：吸顶常驻（同活动流面板的暂停条），点它恢复跟随。 -->
        <div v-if="paused" class="ev-paused" @click="togglePause">
          <span class="ev-paused__text">
            {{ pausedCount > 0 ? `已暂停 · 新事件 ${pausedCount} 条` : '已暂停' }}
          </span>
          <ElButton size="small" @click.stop="togglePause">继续跟随</ElButton>
        </div>
        <!-- 高度上限（本页自测口径）。1600×900 实测（数字见下）：
             口径 = 表顶距视口顶 265（hero 顶 124 + hero 46 + 间距 16 + 过滤条 58 +
             间距 16 + 卡上衬 5）+ 暂停条 49（条高 33 + 上下边距 8×2，实测表顶被它推低
             49）+ 表底以下 60（游标条 29 + 卡底 11 + 页底 20）+ 余量 16 = 390。
             为什么把暂停条算常备：暂停是本页主操作之一，它出现时也不许把
             「账目句／加载更早」挤出屏；代价只是未暂停时多 49px 空白（页面反而
             完全不滚、chrome 全程静止）。max(240px,…) 下限：矮视口（横屏）下
             calc 会剩负值，表体整个塌掉。ElTable 原生 max-height 提供
             表头固定、表体内滚——本页的翻页是游标式（无分页器），行数不能顶走底栏。 -->
        <ElTable
          :data="rows"
          size="small"
          max-height="max(240px, calc(100vh - 390px))"
          :row-class-name="() => 'ev-row'"
        >
          <ElTableColumn label="时刻" width="190">
            <template #default="{ row }">
              <!-- 绝对 + 相对（同一个 t 的两种读法；写在同一格的两行里）。 -->
              <div class="ev-time">
                <span class="ev-time__abs">{{ row.clock }}</span>
                <span class="ev-time__rel">{{ eventRelativeTime(row.tSec, nowSec) }}</span>
              </div>
            </template>
          </ElTableColumn>
          <ElTableColumn label="类型" width="96">
            <template #default="{ row }">
              <span class="ev-type" :class="`is-${row.tone}`">
                <ArtSvgIcon :icon="row.icon" />
                {{ row.typeLabel }}
              </span>
            </template>
          </ElTableColumn>
          <ElTableColumn label="动作" min-width="200">
            <template #default="{ row }">
              <!-- 中文读法 + 原始动作码（事实直显，排障引用不必悬停）。 -->
              <span class="ev-action">{{ row.action }}</span>
              <span class="ev-raw ev-mono">{{ row.rawAction }}</span>
            </template>
          </ElTableColumn>
          <ElTableColumn label="对象" min-width="200">
            <template #default="{ row }">
              <span class="ev-actor ev-mono" :title="row.actorTitle">{{ row.actor }}</span>
            </template>
          </ElTableColumn>
          <ElTableColumn label="主机" min-width="130">
            <template #default="{ row }">
              <span class="ev-host" :title="`主机 ${row.hostId}`">{{ row.host }}</span>
            </template>
          </ElTableColumn>
          <ElTableColumn label="退出码" width="90">
            <template #default="{ row }">
              <!-- die 系才有；0 中性、≠0 红染；nil 显示「—」（不可考不是 0）。 -->
              <span v-if="row.exitText" class="ev-exit" :class="`is-${row.exitTone}`">
                {{ row.exitText }}
              </span>
              <span v-else class="ev-exit is-none">—</span>
            </template>
          </ElTableColumn>
        </ElTable>

        <!-- 翻页（游标）：只报两个硬事实 —— 已加载多少、窗口共多少；还有更早的
             才给「加载更早」（服务端 nextCursor 为空的结束态）。 -->
        <div class="ev-more">
          <span class="ev-more__hint"> 已加载 {{ rows.length }} 条 · 窗口共 {{ total }} 条 </span>
          <ElButton v-if="nextCursor" size="small" :loading="loadingMore" @click="loadMore">
            加载更早
          </ElButton>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
  /**
   * 事件流详版页（本波，`/docker/events`）：活动流面板的整页形态 —— 跨主机 docker
   * 事件的**历史 + 实时**一张表，回答「刚才到现在都发生了什么、谁在哪台机器上退出成
   * 什么样」。照 views/tasks.vue 先例：hidden 路由（无菜单种子，入口长在总览的活动流
   * 面板头部）＋ docker:list（与两条端点同档），整页化换来筛选、翻历史与自己的 URL。
   *
   * 数据是**两条腿**（core 的同一份保留窗口）：
   *   - 历史：GET /docker/events/history（t 降序、游标分页、服务端过滤）；
   *   - 实时：GET /docker/events（NDJSON；连接回放各主机最近 50 条 + 实时推送）。
   * 加载序固定为**先历史、后实时**：列表先有「刚才」的完整一段（含连接前几秒的空窗），
   * 再接流 —— 反过来的话，回放段会先于历史段到达，列表要先被重放灌一遍再被历史覆盖。
   * 两段合成一条时间轴、按内容复合键去重（utils/events 的 createEventsLog，那里有
   * 论证与测试）：流的回放与刚拉的历史大量重叠，不去重就是双份。
   *
   * 过滤是**服务端过滤**（端点参数）＋ 实时行的**客户端过滤**（流是全量的，页面筛了
   * 就得自己拦下不合的行；谓词与 core 的 keyword 同款，见 matchesEventFilter）。
   * 过滤条件变化 = 重建时间轴（旧列表属于旧筛选，留着只会让人以为「筛了没生效」）
   * ＋重拉历史页 1；流不动（它本来就不带筛选）。
   *
   * 实时跟随 / 暂停：暂停**冻结列表**（新事件只计数），恢复一次性并回 —— 列表是
   * 「最新在前」的表，插行会把阅读位置整段顶下去；流始终不停（暂停停的是「列表动」，
   * 不是「收事实」）。断流的自动重连与页面的 keep-alive 生命周期照活动流面板同款
   * 纪律（连接 = 激活期；失活即断流，回来重连 + 补拉历史）。
   *
   * 时刻列给的是**事件的 agent 挂钟**（core 透传的 t，与活动流面板同源）—— 它可能在
   * 时钟偏斜的主机上偏移；窗口裁剪走的是 core 接收时刻（后端侧），列表不拿两种时间
   * 混排（同一列只画一个口径）。
   */
  import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'
  import { useRouter } from 'vue-router'
  import {
    ElButton,
    ElEmpty,
    ElInput,
    ElOption,
    ElRadioButton,
    ElRadioGroup,
    ElSelect,
    ElSkeleton,
    ElTable,
    ElTableColumn
  } from 'element-plus'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtSvgIcon from '@/components/core/base/art-svg-icon/index.vue'
  import { usePageIcon } from '@/hooks/core/usePageIcon'
  import {
    fetchDockerEventHistory,
    fetchDockerHosts,
    openDockerEventsStream,
    type DockerEventHistoryItem,
    type DockerHostItem
  } from '../api'
  import {
    createEventsLog,
    eventActionText,
    eventClockTime,
    eventRelativeTime,
    eventTypeLabel,
    eventTypeMeta,
    exitCodeText,
    exitCodeTone,
    matchesEventFilter,
    parseHistoryItem,
    type DockerEventFields,
    type DockerEventItem
  } from '../utils/events'

  defineOptions({ name: 'DockerEventsPage' })

  const router = useRouter()
  const pageIcon = usePageIcon('ri:pulse-line')

  /** 返回上一页：本页没有菜单入口（从活动流面板头部进），返回键就是它的出口。 */
  function back() {
    router.back()
  }

  // ── 过滤（三项服务端过滤 + 实时行客户端过滤）────────────────────
  const hosts = ref<DockerHostItem[]>([])
  const filterHost = ref('')
  const filterType = ref('')
  const filterKeyword = ref('')

  /** 当前过滤（发给端点的形态：空值不发）。 */
  const activeFilter = computed(() => ({
    hostId: filterHost.value,
    type: filterType.value,
    keyword: filterKeyword.value.trim()
  }))

  const isFiltered = computed(
    () => filterHost.value !== '' || filterType.value !== '' || filterKeyword.value.trim() !== ''
  )

  // ── 时间轴（历史 ∪ 实时，t 降序，内容复合键去重）─────────────────
  // log 用 let 而非 const：过滤变化时整条重建（旧筛选的行不该留在新列表里）。
  let log = createEventsLog({ accept: (f) => matchesEventFilter(f, activeFilter.value) })
  const entries = ref<readonly DockerEventItem[]>([])
  /** 冻结态的行快照（暂停期间列表不动；恢复时丢弃它、回到 log.entries）。 */
  const frozen = ref<readonly DockerEventItem[] | null>(null)
  /** 暂停起点：pausedCount = 暂停以来的接受条数（不拿「列表长度」相减 —— 上限
      裁剪会让长度不再等量增长，差值会凭空变大）。 */
  let pausedBase = 0

  /** 窗口共 N 条 = 端点报的窗口全量（抓取时刻）+ 此后实时接受的条数。 */
  const total = ref(0)
  /** 最近一次历史抓取完成时的 log 计数（实时增量的基准）。 */
  let liveBase = 0
  let windowTotal = 0
  const nextCursor = ref('')

  const paused = ref(false)
  const pausedCount = ref(0)

  /** 相对时间的「现在」：1 秒一跳（tick 在生命周期里起停）。 */
  const nowSec = ref(Math.floor(Date.now() / 1000))

  // ── 四态与账目 ───────────────────────────────────────────
  const loading = ref(false)
  const loadingMore = ref(false)
  const firstError = ref(false)
  const refreshError = ref(false)
  const hasLoaded = ref(false)
  /** 请求序号：手动刷新、过滤切换、轮询并发时，旧响应晚到不得覆盖新数据。 */
  let loadSeq = 0

  const state = computed<'loading' | 'error' | 'empty' | 'ready'>(() => {
    if (firstError.value) return 'error'
    // 骨架态覆盖两种「列表正在被替换」：首拉（还没有任何行）与筛选重建（旧筛选的
    // 行属于旧查询，重建期间给骨架比给一片旧行诚实）。
    if (loading.value) return 'loading'
    if (hasLoaded.value && entries.value.length === 0 && total.value === 0) return 'empty'
    return 'ready'
  })

  const emptyText = computed(() => (isFiltered.value ? '没有匹配的事件' : '窗口内没有事件'))

  // ── 行渲染模型（纯函数算一次，模板不逐行调函数）──────────────────
  interface EventRow {
    seq: number
    icon: string
    tone: string
    typeLabel: string
    action: string
    rawAction: string
    actor: string
    actorTitle: string
    host: string
    hostId: string
    exitText: string
    exitTone: string
    tSec: number
    clock: string
  }

  function shortActorId(id: string): string {
    return id.replace(/^sha256:/, '').slice(0, 12)
  }

  const rows = computed<EventRow[]>(() => {
    const src = frozen.value ?? entries.value
    return src.map((e) => {
      const meta = eventTypeMeta(e.type)
      const shortId = shortActorId(e.actorId)
      return {
        seq: e.seq,
        icon: meta.icon,
        tone: meta.tone,
        typeLabel: eventTypeLabel(e.type),
        action: eventActionText(e.action),
        rawAction: e.action,
        actor: e.actorName || shortId || '—',
        actorTitle:
          e.actorName && shortId ? `${e.actorName} · ${shortId}` : e.actorId || e.actorName || '—',
        host: e.hostname || `主机 ${e.hostId}`,
        hostId: e.hostId,
        exitText: e.exitCode !== null ? exitCodeText(e.exitCode) : '',
        exitTone: e.exitCode !== null ? exitCodeTone(e.exitCode) : 'neutral',
        tSec: e.t / 1000,
        clock: eventClockTime(e.t)
      }
    })
  })

  // ── 历史加载（首拉 / 静默刷新 / 筛选重建 / 翻页）──────────────────
  /**
   * 拉一页历史。三种形态共用一个函数（差别只在 rebuild 与游标）：
   *   - 首拉 / 筛选切换：rebuild=true —— **重建时间轴**（旧筛选的行属于旧查询，
   *     留着只会让人以为「筛了没生效」），骨架态显示；
   *   - 手动刷新：rebuild=false、游标清空 —— 抓窗口最新一页并进现有时间轴
   *     （重复由内容键去重），列表**不清空**（静默刷新：已有行不闪）；
   *   - 翻页（加载更早）：rebuild=false、带上游标。
   */
  async function loadHistory(opts: { rebuild?: boolean; cursor?: string } = {}) {
    const rebuild = opts.rebuild === true
    const cursor = opts.cursor ?? ''
    const seq = ++loadSeq
    if (rebuild) {
      loading.value = true
      loadingMore.value = false
    } else if (cursor !== '') {
      loadingMore.value = true
    }
    try {
      const f = activeFilter.value
      const resp = await fetchDockerEventHistory({
        ...(f.hostId ? { hostId: f.hostId } : {}),
        ...(f.type ? { type: f.type } : {}),
        ...(f.keyword ? { keyword: f.keyword } : {}),
        ...(cursor ? { cursor } : {})
      })
      if (seq !== loadSeq) return
      const items: DockerEventFields[] = []
      for (const raw of resp.items ?? []) {
        const fields = parseHistoryItem(raw as DockerEventHistoryItem)
        if (fields) items.push(fields) // 形状漂移的行跳过，不打断整页
      }
      if (rebuild) {
        // 重建时间轴：新 log 的 accept 闭包读的是**当前**过滤，历史种子与随后
        // 到的实时行走同一把尺（见 utils/events 的 createEventsLog）。
        log = createEventsLog({ accept: (ff) => matchesEventFilter(ff, activeFilter.value) })
        frozen.value = null
        paused.value = false
        pausedCount.value = 0
        entries.value = []
      }
      log.seedHistory(items)
      liveBase = log.totalSeen // 实时增量的基准：这次抓取之后到的才算「新增」
      windowTotal = resp.total ?? 0
      nextCursor.value = resp.nextCursor ?? ''
      hasLoaded.value = true
      firstError.value = false
      refreshError.value = false
      nowSec.value = Math.floor(Date.now() / 1000)
      syncView()
    } catch {
      if (seq !== loadSeq) return
      if (hasLoaded.value && !rebuild) {
        // 已有列表（静默刷新/翻页失败）：保留旧数据 + 标注，不整页切错误态 ——
        // 页面开着，把刚看完的列表整个换成错误屏反而丢上下文。
        refreshError.value = true
      } else {
        firstError.value = true
      }
    } finally {
      if (seq === loadSeq) {
        loading.value = false
        loadingMore.value = false
      }
    }
  }

  function manualRefresh() {
    void loadHistory()
  }

  function loadMore() {
    if (!nextCursor.value || loadingMore.value) return
    void loadHistory({ cursor: nextCursor.value })
  }

  /** 过滤变化即重拉（服务端过滤没有本地回退；实时行由新 log 的 accept 拦）。 */
  watch(activeFilter, () => {
    void loadHistory({ rebuild: true })
  })

  // ── 实时流（fetch + ReadableStream，断流指数退避重连）──────────────
  type Phase = 'connecting' | 'live' | 'reconnecting' | 'stopped'
  const phase = ref<Phase>('connecting')
  const stopReason = ref('')

  let connSeq = 0
  let streamAbort: AbortController | null = null
  let retryTimer: number | undefined
  let tickTimer: number | undefined
  let attempts = 0
  let liveSince = 0

  const RECONNECT_BASE_MS = 1_000
  const RECONNECT_MAX_MS = 10_000
  const STABLE_STREAM_MS = 30_000

  /** 把 log 的当前状态同步进渲染（行与两个账目）。暂停时行不动、只累计新事件数。 */
  function syncView() {
    // 「窗口共 N 条」= 端点报的窗口全量 + 此后实时接受的条数（两个数说的是同一
    // 窗口、同一过滤，只是抓取时刻不同 —— 相加不越界：实时行同样在窗口里）。
    total.value = windowTotal + Math.max(0, log.totalSeen - liveBase)
    if (paused.value) {
      pausedCount.value = Math.max(0, log.totalSeen - pausedBase)
      return
    }
    entries.value = log.entries
  }

  function stopTick() {
    if (tickTimer !== undefined) {
      clearInterval(tickTimer)
      tickTimer = undefined
    }
  }

  function startTick() {
    stopTick()
    tickTimer = window.setInterval(() => {
      // 后台标签页的 setInterval 会被浏览器限流：隐藏期间跳过 tick（与总览同一纪律）。
      if (!document.hidden) nowSec.value = Math.floor(Date.now() / 1000)
    }, 1_000)
  }

  function clearRetry() {
    if (retryTimer !== undefined) {
      clearTimeout(retryTimer)
      retryTimer = undefined
    }
  }

  function openConclusion(status: number): string {
    if (status === 401) return '登录状态已失效，请重新登录后再试'
    if (status === 403) return '没有接入事件流的权限'
    return ''
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
        if (seq !== connSeq || done) break
        log.pushRaw(decoder.decode(value, { stream: true }))
        syncView()
      }
      if (seq !== connSeq) return
      log.pushRaw(decoder.decode())
      syncView()
      scheduleReconnect()
    } catch {
      if (seq !== connSeq) return
      scheduleReconnect()
    } finally {
      if (seq === connSeq) streamAbort = null
    }
  }

  function scheduleReconnect() {
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

  /** 断流（失活 / 卸载；幂等 —— 没有在飞就不扰动）。 */
  function stopStream() {
    connSeq++
    clearRetry()
    stopTick()
    const controller = streamAbort
    streamAbort = null
    if (controller) controller.abort()
  }

  const statusText = computed(() => {
    if (phase.value === 'stopped')
      return stopReason.value ? `已停止 · ${stopReason.value}` : '已停止'
    if (phase.value === 'reconnecting') return '已断开 · 重连中'
    if (phase.value === 'connecting') return '连接中…'
    return '实时'
  })

  // ── 跟随 / 暂停 ─────────────────────────────────────────
  function togglePause() {
    if (paused.value) {
      paused.value = false
      frozen.value = null
      pausedCount.value = 0
      entries.value = log.entries // 恢复：把暂停期间的 N 条一次并回来
      return
    }
    paused.value = true
    pausedBase = log.totalSeen // 暂停起点：新事件数从这一刻起算
    pausedCount.value = 0
    frozen.value = entries.value // 冻结列表：行不动，计数照走
  }

  // ── 生命周期（连接 = 页面激活期）────────────────────────────
  /**
   * 起一次「历史 + 实时」：先拉历史，再接流（加载序见文件头）。
   * 幂等：流已在飞（挂载与首次激活会同时走到这里）时只补一次历史，不重复接流；
   * startSeq 兜住「两次 start 交错」—— 前一次的 connect 不再执行（否则两条流并存）。
   */
  let startSeq = 0
  async function start() {
    const seq = ++startSeq
    startTick()
    if (streamAbort !== null || retryTimer !== undefined) {
      void loadHistory()
      return
    }
    await loadHistory({ rebuild: !hasLoaded.value })
    if (seq !== startSeq) return
    await connect()
  }

  function stop() {
    startSeq++ // 在飞的 start 作废（其 connect 不再执行）
    stopStream()
  }

  function retryStream() {
    if (phase.value !== 'stopped') return
    attempts = 0
    void connect()
  }

  async function loadHosts() {
    try {
      const resp = await fetchDockerHosts()
      hosts.value = resp.list ?? []
    } catch {
      // 主机清单只服务过滤下拉：读失败不阻断主列表（过滤仍可只按类型/关键字）。
      hosts.value = []
    }
  }

  // 挂载 / keep-alive 失活 / 激活 —— 与活动流面板同一套纪律（连接 = 激活期），
  // 外加「激活回来补拉历史」（失活期间的历史缺口由这一次抓取补上）。
  let activatedOnce = false

  onMounted(() => {
    void loadHosts()
    void start()
  })

  onDeactivated(stop)
  onBeforeUnmount(stop)
  onActivated(() => {
    // 首次激活与挂载同帧（KeepAlive 内挂载）：onMounted 已经起过，不重复。
    if (!activatedOnce) {
      activatedOnce = true
      return
    }
    void start()
  })
</script>

<style lang="scss" scoped>
  @use '@styles/core/breakpoints.scss' as *;
  /* 页面骨架（hero/三态/动效降级）复用列表页范式样式（本模块四页共用）。 */
  @use './wkl-shell';
  @use './overview-tokens' as t;

  // 「重试」等默认档按钮的主色文字对比度 AA：病灶与处方见 overview-tokens
  // 的 primary-text-aa（终审 QA D2·浅色实测 3.68:1）。
  @include t.primary-text-aa;

  /* 页面级对比度令牌（与 tasks 页同款收口）：
     - --el-text-color-secondary 升 regular：表内次要文字（时间/主机/退出码中性档）
       在白卡上吃 EP 默认 #909399 只有 3.08:1；
     - --color-g-600 抬一档到 g-700：hero 副标题的对比度（浅色 ≈6.8:1、暗色随主题）。 */
  .docker-events {
    --el-text-color-secondary: var(--el-text-color-regular);
    --color-g-600: var(--art-gray-700);
  }

  .ev-mono {
    font-family: var(--el-font-family-mono, monospace);
  }

  /* ---------- 状态点（实时呼吸 / 断连警示 / 停机） ---------- */
  .ev-dot {
    flex: none;
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--el-text-color-secondary);

    &.is-live {
      background: var(--el-color-success);
    }

    &.is-connecting {
      background: var(--el-text-color-secondary);
    }

    &.is-reconnecting {
      background: var(--el-color-warning);
    }

    &.is-stopped {
      background: var(--el-color-danger);
    }
  }

  /* ---------- 过滤条 ---------- */
  .ev-filters {
    padding: 10px 12px;

    &__label {
      color: var(--el-text-color-regular);
      font-size: 13px;
    }

    &__host {
      width: 200px;
    }

    &__kw {
      width: 200px;
    }
  }

  /* ---------- 三态 ---------- */
  .ev-state {
    display: flex;
    flex-direction: column;
    gap: 10px;
    align-items: center;
    padding: 32px 16px;

    &__text {
      margin: 0;
      color: var(--el-text-color-regular);
      font-size: 13px;
    }
  }

  .ev-body {
    padding: 4px 12px 10px;
  }

  /* 静默刷新失败标注：琥珀即「需要注意」（与 tasks 页同一套颜色语言）。 */
  .ev-refresh-error {
    margin: 8px 0;
    color: var(--aa-warning-text);
    font-size: 12px;
    line-height: 1.6;
  }

  /* ---------- 暂停提示条（吸顶；整条可点） ---------- */
  .ev-paused {
    position: sticky;
    top: 0;
    z-index: 1;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
    margin: 8px -12px;
    padding: 4px 12px;
    cursor: pointer;
    background: color-mix(in srgb, var(--el-color-warning) 10%, transparent);
    border-bottom: 1px solid var(--default-border);

    &__text {
      color: var(--aa-warning-text);
      font-size: 12px;
    }

    .el-button {
      margin-left: auto;
    }
  }

  /* ---------- 表 ---------- */
  .ev-row {
    cursor: default;
  }

  .ev-time {
    display: flex;
    flex-direction: column;
    line-height: 1.4;

    &__abs {
      color: var(--el-text-color-primary);
      font-size: 12px;
      font-variant-numeric: tabular-nums;
    }

    &__rel {
      color: var(--el-text-color-secondary);
      font-size: 11px;
    }
  }

  /* 类型徽标：四类订阅维度的图标 + 中文名，色即类别（未知走中性）。 */
  .ev-type {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-size: 12px;

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

  .ev-action {
    margin-right: 8px;
    color: var(--el-text-color-primary);
    font-size: 13px;
  }

  /* 原始动作码：等宽小字（等宽即「这是原始码」的提示），对比度走 regular 档。 */
  .ev-raw {
    color: var(--el-text-color-regular);
    font-size: 11px;
    overflow-wrap: anywhere;
  }

  .ev-actor {
    color: var(--el-text-color-regular);
    font-size: 12px;
    overflow-wrap: anywhere;
  }

  .ev-host {
    color: var(--el-text-color-regular);
    font-size: 12px;
  }

  /* 退出码：0 中性、≠0 红染；无值（nil）给「—」（不可考不是 0）。 */
  .ev-exit {
    font-family: var(--el-font-family-mono, monospace);
    font-size: 12px;

    &.is-neutral {
      color: var(--el-text-color-regular);
    }

    &.is-danger {
      color: var(--aa-danger-text);
    }

    &.is-none {
      color: var(--el-text-color-secondary);
    }
  }

  /* ---------- 翻页（游标） ---------- */
  .ev-more {
    display: flex;
    flex-wrap: wrap;
    gap: 10px;
    align-items: center;
    justify-content: space-between;
    padding-top: 10px;
    border-top: 1px solid var(--el-border-color-lighter);

    &__hint {
      color: var(--el-text-color-regular);
      font-size: 12px;
    }
  }

  // 手机横屏（<768）：过滤条各项占满一行（hero 的动作组换行铺开，详情页同一口径）。
  @include respond-below('tablet') {
    .ev-hero > .ml-auto {
      width: 100%;
    }

    .ev-filters__host,
    .ev-filters__kw {
      width: 100%;
    }
  }
</style>
