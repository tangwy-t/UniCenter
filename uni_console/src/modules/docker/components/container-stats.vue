<template>
  <section class="wkl-stats">
    <div class="wkl-stats__head">
      <span class="wkl-stats__title">实时统计</span>
      <!-- 状态行：连接中 / 实时 / 已结束（结论句，次要色）；失败才用错误色 + 重试。 -->
      <span v-if="phase === 'connecting'" class="wkl-stats__note">正在连接…</span>
      <ElTag v-else-if="phase === 'live'" size="small" effect="plain" class="wkl-stats__live">
        实时
      </ElTag>
      <span v-else-if="phase === 'ended'" class="wkl-stats__note">统计流已结束。</span>
      <template v-else-if="phase === 'error'">
        <span class="wkl-stats__error">{{ errorText }}</span>
        <ElButton size="small" class="wkl-stats__retry" @click="restart">重试</ElButton>
      </template>
      <span class="wkl-stats__meta">{{ metaText }}</span>
    </div>

    <!-- 未运行：曲线区缺席要有解释（而不是留下一个空框让人猜）。 -->
    <p v-if="!running" class="wkl-stats__off">容器未运行，暂无实时统计。</p>

    <template v-else>
      <!-- 读数行 = 曲线的直标通道（浅色模式 teal/amber 对比度 < 3:1 时的缓解层）：
           数值穿 text token，序列身份由色点承载，不由给文字上色承载。 -->
      <div class="wkl-stats__tiles">
        <div class="wkl-stats__tile">
          <div class="wkl-stats__tile-head">
            <span class="wkl-stats__key" style="background: var(--stat-cpu)" />
            <span class="wkl-stats__label">CPU 使用率</span>
          </div>
          <span class="wkl-stats__value">{{ cpuText }}</span>
        </div>
        <div class="wkl-stats__tile">
          <div class="wkl-stats__tile-head">
            <span class="wkl-stats__key" style="background: var(--stat-mem)" />
            <span class="wkl-stats__label">内存用量</span>
          </div>
          <span class="wkl-stats__value">{{ memUsageText }}</span>
          <span class="wkl-stats__sub">{{ memSubText }}</span>
        </div>
      </div>

      <!-- 资源曲线：CPU（%）与内存（MB）量纲不同 —— 双轴图是第一反模式，故拆成
           两张单序列小图各配各的 y 轴（小倍数），单序列不设图例（标题即点名）。 -->
      <div class="wkl-stats__charts">
        <StatsChart :series="cpuSeries" :times="times" unit="%" area />
        <StatsChart :series="memSeries" :times="times" unit="MB" area />
      </div>

      <!-- 网络速率 = 第二组（默认折叠）：CPU / 内存回答「这台容器稳不稳」，网络
           是次要细节；折叠不影响流（样本照常累积，展开即见）。rx/tx 同量纲
           （B/s）才允许同图双序列，须配图例。P2 起本组多一条如实交代：历史
           端点不回网络字段，这张图只有实时段（CPU/内存图才有 30 分钟回看）。 -->
      <ElCollapse v-model="netOpen" class="wkl-stats__net">
        <ElCollapseItem name="net" title="网络速率">
          <p class="wkl-stats__net-note">网络速率仅实时段 —— 历史回看端点不回网络字段。</p>
          <div class="wkl-stats__tiles wkl-stats__tiles--net">
            <div class="wkl-stats__tile">
              <div class="wkl-stats__tile-head">
                <span class="wkl-stats__key" style="background: var(--stat-rx)" />
                <span class="wkl-stats__label">接收（下行）</span>
              </div>
              <span class="wkl-stats__value">{{ netRxText }}</span>
            </div>
            <div class="wkl-stats__tile">
              <div class="wkl-stats__tile-head">
                <span class="wkl-stats__key" style="background: var(--stat-tx)" />
                <span class="wkl-stats__label">发送（上行）</span>
              </div>
              <span class="wkl-stats__value">{{ netTxText }}</span>
            </div>
          </div>
          <StatsChart :series="netSeries" :times="netTimes" unit="B/s" />
        </ElCollapseItem>
      </ElCollapse>
    </template>
  </section>
</template>

<script setup lang="ts">
  /**
   * 容器 stats 实时统计（五期监控面 · 前端半边）：读数行 + 三张曲线 + 流生命周期。
   *
   * 流程与日志 Follow 同一条两步走（契约见 workload-drawer 的 startLogFollow 注释）：
   *   1. 发 container:stats（会话制 action）→ 轮询 cmds/:ref 到终态 —— 终态里的
   *      session_id 即「会话已建立」的信号（样本走流通道，不走结果载荷）；
   *   2. GET cmds/:ref/stats 接入 NDJSON 样本流（首帧即当前值，1 秒采样，eof 收尾）。
   *
   * P2 历史半边：上面两步之前先拉一次 stats-history（过去约 30 分钟，一次性
   * GET）预填曲线 —— 受理/建流还在路上时读数与曲线就有形状，「昨晚为什么慢」
   * 有回看窗口。历史失败静默降级为纯实时（= 原行为）；两段数据按 t 去重衔接
   * 成一条序列，口径见 utils/stats 的 mergeStatsHistory。
   *
   * 生命周期纪律照抄终端 Tab（pty-terminal）：本组件由抽屉 `v-if`（概览 Tab 激活 +
   * 抽屉开着）+ `:key`（行主机:容器）挂载 —— 切 Tab / 关抽屉 / 换行都是**卸载**，
   * 卸载即 abort；AbortController 一断，服务端就向 agent 下发 cancel 释放会话。
   * 会话生命周期 = 组件生命周期，不为看不见的曲线留一条流。
   */
  import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
  import { ElButton, ElCollapse, ElCollapseItem, ElTag } from 'element-plus'
  import { formatByUnit } from '@/modules/device/utils/display'
  import {
    fetchDockerCmdResult,
    fetchDockerStatsHistory,
    openDockerStatsStream,
    sendDockerCmd,
    type DockerWorkloadItem
  } from '../api'
  import {
    createStatsFeed,
    MAX_STATS_SAMPLES,
    mergeStatsHistory,
    type StatsHistorySample,
    type StatsSample
  } from '../utils/stats'
  import { pollDelay } from '../utils/cmd'
  import StatsChart, { type StatsSeries } from './stats-chart.vue'

  defineOptions({ name: 'DockerContainerStats' })

  const props = defineProps<{
    /** 行主机（统一表的行来自任意主机，指令按它发）。 */
    hostId: string
    /** 容器 id（stats 的 target）。 */
    containerId: string
    /** 行的运行态：未运行不发起流（空转的指令只会换回一个错误结论）。 */
    running: boolean
    /** 行快照：首帧到达前的单点占位（cpuPercent / memUsageMb 等同名字段）。 */
    snapshot: DockerWorkloadItem
  }>()

  type Phase = 'connecting' | 'live' | 'ended' | 'error'

  const phase = ref<Phase>('connecting')
  const errorText = ref('')
  /** 窗口内样本（旧 → 新）。每帧换一次数组引用驱动重绘（不深改内部元素）。 */
  const samples = ref<StatsSample[]>([])
  /** 断流手柄：abort 即触发服务端取消会话。 */
  let streamAbort: AbortController | null = null
  /** 生命周期序号：断开/重试让在飞的「建立 + 读循环」失效。 */
  let statsSeq = 0
  /**
   * 历史段（P2 预填）：start() 拉一次留在本次会话里，实时流的每一帧都与它
   * 衔接合并（mergeStatsHistory）。拉不到就是空数组 —— 纯实时，原行为。
   */
  let history: StatsHistorySample[] = []

  const netOpen = ref<string[]>([])

  // ── 读数（首帧前的单点占位 = 行快照；首帧即当前值，很快替换）─────────
  const latest = computed(() => samples.value[samples.value.length - 1] ?? null)
  const cpuValue = computed(() => latest.value?.cpuPercent ?? props.snapshot.cpuPercent)
  const memUsage = computed(() => latest.value?.memUsageMb ?? props.snapshot.memUsageMb)
  const memLimit = computed(() => latest.value?.memLimitMb ?? props.snapshot.memLimitMb)
  const netRx = computed(() => latest.value?.netRxBytesSec ?? props.snapshot.netRxBytesSec)
  const netTx = computed(() => latest.value?.netTxBytesSec ?? props.snapshot.netTxBytesSec)

  const cpuText = computed(() => formatByUnit('%', cpuValue.value))
  const memUsageText = computed(() => formatByUnit('MB', memUsage.value))
  const netRxText = computed(() => formatByUnit('B/s', netRx.value))
  const netTxText = computed(() => formatByUnit('B/s', netTx.value))
  const memSubText = computed(() => {
    const limit = memLimit.value
    if (!limit || limit <= 0) return '未设内存上限'
    const pct = Math.round((memUsage.value / limit) * 100)
    return `上限 ${formatByUnit('MB', limit)} · ${pct}%`
  })

  // ── 曲线数据（序列颜色走本组件的 CSS 变量，明暗两模式在样式块里换档）──
  const times = computed(() => samples.value.map((s) => s.t))
  const cpuSeries = computed<StatsSeries[]>(() => [
    { name: 'CPU 使用率', color: 'var(--stat-cpu)', values: samples.value.map((s) => s.cpuPercent) }
  ])
  const memSeries = computed<StatsSeries[]>(() => [
    { name: '内存用量', color: 'var(--stat-mem)', values: samples.value.map((s) => s.memUsageMb) }
  ])

  // ── 网络曲线的数据视图：只画真实流样本 ─────────────────────────────
  // 历史端点不回网络字段（契约只有 t/cpu/mem），这张图不拿假 0 充历史 ——
  // 带网络字段的样本才进序列与时刻，历史段自然缺席（折叠组里的说明句交代）。
  const netView = computed(() => {
    const rows: { t: number; rx: number; tx: number }[] = []
    for (const s of samples.value) {
      if (s.netRxBytesSec !== undefined && s.netTxBytesSec !== undefined) {
        rows.push({ t: s.t, rx: s.netRxBytesSec, tx: s.netTxBytesSec })
      }
    }
    return rows
  })
  const netTimes = computed(() => netView.value.map((r) => r.t))
  const netSeries = computed<StatsSeries[]>(() => [
    {
      name: '接收（下行）',
      color: 'var(--stat-rx)',
      values: netView.value.map((r) => r.rx)
    },
    {
      name: '发送（上行）',
      color: 'var(--stat-tx)',
      values: netView.value.map((r) => r.tx)
    }
  ])

  /** 采样口径摘要：窗内还有历史段（流帧独有网络字段作标记）就如实说两段
      口径；历史滑出窗或没拉到时，维持纯实时的原口径 —— 摘要句不许说谎。 */
  const metaText = computed(() =>
    samples.value.some((s) => s.netRxBytesSec === undefined)
      ? '历史约 30 分钟 · 实时 1 秒采样'
      : '1 秒采样 · 最近约 2 分钟'
  )

  // ── 流生命周期（照抄 startLogFollow 的节奏与守卫）──────────────────
  /** 受理期失败的文案：服务端给的结论句优先。 */
  function errMsg(e: unknown, fallback: string): string {
    const msg = (e as { message?: string })?.message
    return msg && msg.trim() !== '' ? msg : fallback
  }

  /** 流端点接入失败的结论句（状态码是排障线索，不进页面）。 */
  function streamOpenConclusion(status: number): string {
    if (status === 401) return '登录状态已失效，请重新登录后再试。'
    if (status === 403) return '没有接入该统计流的权限。'
    if (status === 404) return '该统计会话已过期，请重试。'
    if (status === 409) return '该统计会话已有连接，请稍后重试。'
    return '实时统计未能建立，请稍后重试'
  }

  async function start() {
    const seq = ++statsSeq
    phase.value = 'connecting'
    errorText.value = ''
    samples.value = []

    // 0. 历史预填（P2 · 历史半边）：先拉过去约 30 分钟（一次性 GET，快），
    //    再去受理实时流 —— 顺序是「先有形状，再接当下」。失败或为空**静默
    //    降级为纯实时**（= 原行为）：历史是回看增强不是门槛，回看拉不到不该
    //    把「现在稳不稳」的实时流一起拖下水 —— 不给错误态，重试按钮属于流。
    try {
      const resp = await fetchDockerStatsHistory(props.hostId, props.containerId)
      history = Array.isArray(resp?.samples) ? resp.samples : []
    } catch {
      history = []
    }
    if (seq !== statsSeq) return
    // 预填即上屏：受理/建流还在路上，读数与曲线先吃历史末样本（30s 采样，
    // 末样本至多 30 秒旧 —— 仍好过行快照的刷新间隔）。
    samples.value = mergeStatsHistory(history, [])

    try {
      // 1. 受理会话制指令 → 轮询到终态（终态携带 session_id = 会话已建立）。
      const accepted = await sendDockerCmd(props.hostId, {
        action: 'container:stats',
        target: props.containerId
      })
      let attempt = 0
      for (;;) {
        if (seq !== statsSeq) return
        const res = await fetchDockerCmdResult(props.hostId, accepted.ref)
        if (res.status !== 'pending') {
          if (res.status !== 'succeeded') {
            throw new Error(res.error || '实时统计未能建立，请稍后重试')
          }
          break
        }
        await new Promise((r) => setTimeout(r, pollDelay(attempt++)))
        if (attempt > 20) throw new Error('实时统计建立超时，请重试')
      }
      if (seq !== statsSeq) return

      // 2. 接入 NDJSON 样本流；首帧即当前值，故读数立刻有据。
      const controller = new AbortController()
      streamAbort = controller
      const res = await openDockerStatsStream(props.hostId, accepted.ref, controller.signal)
      if (!res.ok || !res.body) {
        throw new Error(streamOpenConclusion(res.status))
      }
      phase.value = 'live'

      const feed = createStatsFeed(MAX_STATS_SAMPLES)
      const reader = res.body.getReader()
      const chunkDecoder = new TextDecoder('utf-8')
      for (;;) {
        const { done, value } = await reader.read()
        if (done || seq !== statsSeq) break
        feed.pushRaw(chunkDecoder.decode(value, { stream: true }))
        // 两段衔接：历史段在前、流窗口在后，合并成一条 t 单调序列（同 t 丢
        // 历史留流帧、缺口连线 —— 口径见 mergeStatsHistory）。
        samples.value = mergeStatsHistory(history, feed.samples)
        if (feed.eof) break
      }
      if (seq !== statsSeq) return
      // 流自然收尾（agent 发了 eof）：最后一批字节可能还在解码器里。
      feed.pushRaw(chunkDecoder.decode())
      samples.value = mergeStatsHistory(history, feed.samples)
      streamAbort = null
      if (feed.eof) {
        phase.value = 'ended'
      } else {
        // 无 eof 的断流（网络/网关）：不是正常收尾，给错误态 + 重试。
        phase.value = 'error'
        errorText.value = '统计流已断开，请重试。'
      }
    } catch (e) {
      if (seq !== statsSeq) return
      // 主动断开（切 Tab / 关抽屉 / 换行）不是错误：不弹结论句。
      if ((e as { name?: string })?.name === 'AbortError') return
      streamAbort = null
      phase.value = 'error'
      errorText.value = errMsg(e, '实时统计连接失败，请稍后重试')
    } finally {
      if (seq === statsSeq) streamAbort = null
    }
  }

  /** 断流（重试前 / 卸载时走这里；幂等 —— 没有在飞就不扰动）。 */
  function stop() {
    const controller = streamAbort
    const inFlight = controller !== null || phase.value === 'connecting' || phase.value === 'live'
    streamAbort = null
    if (!inFlight) return
    statsSeq++ // 让在飞的建立流程与读循环失效
    if (controller) controller.abort()
  }

  function restart() {
    stop()
    void start()
  }

  onMounted(() => {
    if (props.running) void start()
  })

  // 卸载即断流：本组件由抽屉 v-if/:key 控制挂载，切 Tab / 关抽屉 / 换行都到这里。
  onBeforeUnmount(stop)
</script>

<style lang="scss" scoped>
  @use '@styles/core/breakpoints.scss' as *;

  /*
   * 序列色 = EP 语义色按「分类槽」固定指派（dataviz 六项校验两模式全过，实测值见
   * 注释）：CPU 蓝（primary，明暗同值）/ 内存 teal / 接收蓝 / 发送 amber。
   * 蓝 ↔ teal、蓝 ↔ amber 是全部邻接对（读数行序：CPU、内存 | 接收、发送），
   * 浅色 ΔE≥21.4、暗色 ΔE≥28.6；teal 与 amber 永不相邻（暗色下二者 ΔE 仅 2.9）。
   * 浅色 EP 基值明度带外且对比 <3:1，故取 -dark-2 加深步（#0FB194/#CC8B19）；
   * 暗色 EP 基值偏浅，取 light-3（暗色主题里是向背景混深的步，#4E8E2F/#A77730）。
   * 对比度 WARN 的缓解层 = 读数行的直标当前值（tooltip 只是增强）。
   */
  .wkl-stats {
    --stat-cpu: var(--el-color-primary);
    --stat-mem: var(--el-color-success-dark-2);
    --stat-rx: var(--el-color-primary);
    --stat-tx: var(--el-color-warning-dark-2);
  }

  .dark .wkl-stats {
    --stat-mem: var(--el-color-success-light-3);
    --stat-tx: var(--el-color-warning-light-3);
  }

  .wkl-stats__head {
    display: flex;
    flex-wrap: wrap;
    gap: 6px 10px;
    align-items: center;
    margin-bottom: 10px;
  }

  .wkl-stats__title {
    color: var(--el-text-color-primary);
    font-size: 14px;
    font-weight: 600;
  }

  .wkl-stats__live {
    // 「实时」状态签：主色 plain（它是状态而不是序列，不该撞序列色）。
    --el-tag-bg-color: var(--el-color-primary-light-9);
    --el-tag-border-color: var(--el-color-primary-light-5);
    --el-tag-text-color: var(--el-color-primary);
  }

  .wkl-stats__note {
    color: var(--el-text-color-secondary);
    font-size: 12px;
  }

  // 网络组的口径说明（历史缺席的如实交代）：与状态行 note 同一副次要色，
  // 折叠展开后先于读数出现 —— 缺席要有解释，而不是留一截短曲线让人猜。
  .wkl-stats__net-note {
    margin: 0 0 8px;
    color: var(--el-text-color-secondary);
    font-size: 12px;
  }

  .wkl-stats__error {
    color: var(--el-color-danger);
    font-size: 12px;
  }

  .wkl-stats__retry {
    margin-left: 2px;
  }

  // 采样口径摘要：右对齐收尾，不与状态行抢注意力。
  .wkl-stats__meta {
    margin-left: auto;
    color: var(--el-text-color-placeholder);
    font-size: 12px;
    white-space: nowrap;
  }

  .wkl-stats__off {
    margin: 0;
    color: var(--el-text-color-secondary);
    font-size: 13px;
  }

  .wkl-stats__tiles {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px;
    margin-bottom: 10px;

    &--net {
      margin-bottom: 10px;
    }
  }

  .wkl-stats__tile {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
    padding: 10px 12px;
    background: var(--el-fill-color-light);
    border-radius: 8px;
  }

  .wkl-stats__tile-head {
    display: flex;
    gap: 6px;
    align-items: center;
  }

  // 序列身份的线键（与图例/tooltip 同一记号）：色由 style 内联给，跟随 --stat-*。
  .wkl-stats__key {
    display: inline-block;
    flex: none;
    width: 12px;
    height: 2px;
    border-radius: 1px;
  }

  .wkl-stats__label {
    color: var(--el-text-color-secondary);
    font-size: 12px;
  }

  // 独立大数值用比例数字（tabular-nums 会让「121」看着松散 —— 反模式）。
  .wkl-stats__value {
    color: var(--el-text-color-primary);
    font-size: 17px;
    font-weight: 600;
    line-height: 1.4;
    word-break: break-all;
  }

  .wkl-stats__sub {
    color: var(--el-text-color-secondary);
    font-size: 12px;
  }

  .wkl-stats__charts {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 12px;
    margin-bottom: 10px;
  }

  .wkl-stats__net {
    border-top: none;
    border-bottom: none;

    // 折叠头的字号/留白与读数行同级（次要组不该比主组更响）。
    :deep(.el-collapse-item__header) {
      font-size: 13px;
      color: var(--el-text-color-regular);
    }
  }

  // 平板竖屏以下（抽屉全屏）：两列读数/曲线堆叠成单列，横向不再挤成窄缝。
  @include respond-below('tablet') {
    .wkl-stats__tiles,
    .wkl-stats__charts {
      grid-template-columns: 1fr;
    }
  }
</style>
