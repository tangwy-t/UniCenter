<template>
  <div class="server-page art-full-height overflow-y-auto">
    <div class="server-page__inner p-4 pb-8 md:p-5">
      <!-- ============ 页头：标题 + 主机身份 + 实时状态 + 自动刷新 ============ -->
      <div class="sv-hero mb-4 flex flex-wrap items-center gap-3">
        <div class="sv-hero__icon flex-cc">
          <ArtSvgIcon icon="ri:server-line" />
        </div>
        <div class="sv-hero__text min-w-0">
          <h2 class="text-lg font-semibold text-[var(--el-text-color-primary)]">服务器监控</h2>
          <p class="mt-0.5 flex flex-wrap items-center gap-1.5 text-xs text-g-600">
            <span
              class="live-dot inline-block h-1.5 w-1.5 rounded-full bg-success"
              :class="{ 'is-loading': loading }"
            />
            {{
              loading ? '正在采集指标…' : `最近更新 ${updatedAtText} · 历史 ${buckets.length} 桶`
            }}
            <template v-if="autoRefresh">· 10s 自动刷新</template>
          </p>
        </div>
        <div v-if="hostChips.length" class="ml-3 hidden items-center gap-1.5 md:flex">
          <span v-for="c in hostChips" :key="c.text" class="sv-hero-chip" :title="c.text">
            <ArtSvgIcon :icon="c.icon" />
            <span class="max-w-40 truncate">{{ c.text }}</span>
          </span>
        </div>
        <div class="ml-auto flex items-center gap-1">
          <div class="relative">
            <ArtButtonTable
              :icon="'ri:timer-2-line'"
              :iconClass="autoRefresh ? 'bg-theme text-white shadow-sm' : 'bg-theme/12 text-theme'"
              :title="autoRefresh ? '关闭自动刷新 (10s)' : '开启自动刷新 (10s)'"
              @click="autoRefresh = !autoRefresh"
            />
            <span
              v-if="autoRefresh"
              class="live-dot absolute top-0.5 right-3 h-1.5 w-1.5 rounded-full bg-success ring-2 ring-[var(--default-box-color)]"
            />
          </div>
          <ArtButtonTable
            icon="ri:refresh-line"
            iconClass="bg-theme/12 text-theme"
            title="刷新"
            @click="loadStats(false)"
          />
        </div>
      </div>

      <!-- ============ 空状态 / 加载态 ============ -->
      <div v-if="!stats && !loading" class="sv-card sv-empty flex-cc flex-col gap-3 py-16">
        <div class="sv-empty__icon flex-cc">
          <ArtSvgIcon icon="ri:server-line" />
        </div>
        <div class="text-sm font-medium text-[var(--el-text-color-regular)]">暂无监控数据</div>
        <div class="text-xs text-g-600">请检查后端服务是否正常，或点击刷新重试</div>
        <div class="flex items-center">
          <ArtButtonTable
            icon="ri:refresh-line"
            iconClass="bg-theme/12 text-theme"
            title="刷新"
            @click="loadStats(false)"
          />
        </div>
      </div>

      <!-- KPI 数据磁贴(6 项,带迷你趋势线):640px 以下 2 列 3 行;640~1536 为 3 列 2 行;≥1536(2xl)宽度充裕时单行 6 列 -->
      <div
        v-loading="loading && !stats"
        class="kpi-grid grid grid-cols-2 gap-4 md:grid-cols-3 2xl:grid-cols-6"
      >
        <div v-for="k in kpis" :key="k.label" class="sv-card kpi-tile" :title="k.title">
          <div class="kpi-tile__icon flex-cc" :style="{ '--tile': k.tile, '--tile2': k.tile2 }">
            <ArtSvgIcon :icon="k.icon" />
          </div>
          <div class="min-w-0 flex-1">
            <div class="flex items-end justify-between gap-2">
              <div class="kpi-tile__value truncate">{{ k.value }}</div>
              <svg
                v-if="k.line"
                viewBox="0 0 100 50"
                preserveAspectRatio="none"
                class="kpi-tile__spark"
                aria-hidden="true"
              >
                <polygon v-if="k.area" :points="k.area" :fill="k.color" fill-opacity="0.12" />
                <polyline
                  :points="k.line"
                  :stroke="k.color"
                  fill="none"
                  stroke-width="2"
                  vector-effect="non-scaling-stroke"
                  stroke-linejoin="round"
                  stroke-linecap="round"
                />
              </svg>
              <span v-else class="kpi-tile__spark kpi-tile__spark--ph" />
            </div>
            <div class="kpi-tile__label">{{ k.label }}</div>
            <div class="kpi-tile__sub truncate">{{ k.sub }}</div>
          </div>
        </div>
      </div>

      <!-- ============ 主网格：左(CPU/内存/趋势) + 右(主机信息/磁盘) ============ -->
      <div v-if="stats" class="mt-4 grid grid-cols-1 gap-4 lg:grid-cols-5">
        <div class="flex min-w-0 flex-col gap-4 lg:col-span-3">
          <!-- CPU：仪表环 + 每核心热力柱 + 负载均值 -->
          <div class="sv-card !p-4">
            <div class="mb-4 flex flex-wrap items-center justify-between gap-2">
              <div class="flex items-center gap-2.5">
                <span class="sv-chip flex-cc" style="color: #3b82f6; background: #3b82f614">
                  <ArtSvgIcon icon="ri:cpu-line" />
                </span>
                <div>
                  <div class="text-sm font-semibold text-[var(--el-text-color-primary)]"
                    >CPU 处理器</div
                  >
                  <div class="sv-card__sub"
                    >{{ numCPU }} 核 ·
                    {{ perCore.length ? `${perCore.length} 线程负载` : '整体使用率' }}</div
                  >
                </div>
              </div>
              <div class="flex items-center gap-1.5">
                <span v-for="l in loadChips" :key="l.key" class="sv-load-chip" :title="l.title">
                  <span class="sv-load-chip__dot" :style="{ background: l.color }" />
                  <b class="sv-load-chip__key">{{ l.key }}</b>
                  <span class="tabular-nums">{{ l.text }}</span>
                </span>
              </div>
            </div>

            <div class="flex flex-col items-center gap-5 md:flex-row">
              <div class="sv-gauge" role="img" :aria-label="`CPU 使用率 ${cpuText}`">
                <svg viewBox="0 0 120 120">
                  <circle class="sv-ring__track" cx="60" cy="60" r="52" />
                  <circle
                    class="sv-ring__bar"
                    cx="60"
                    cy="60"
                    r="52"
                    :stroke="cpuTone"
                    :stroke-dasharray="gaugeDash"
                    :stroke-dashoffset="cpuOffset"
                    transform="rotate(-90 60 60)"
                  />
                </svg>
                <div class="sv-gauge__center">
                  <div class="sv-gauge__value tabular-nums">{{ cpuText }}</div>
                  <div class="sv-gauge__label">CPU 使用率</div>
                </div>
              </div>

              <div class="w-full min-w-0 flex-1">
                <div class="sv-cores" aria-hidden="true">
                  <div
                    v-for="(p, i) in perCore"
                    :key="i"
                    class="sv-core"
                    :title="`核心 C${i} · ${Math.round(p)}%`"
                  >
                    <span class="sv-core__track">
                      <span
                        class="sv-core__fill"
                        :style="{ height: `${Math.max(p, 2)}%`, background: usageTone(p) }"
                      />
                    </span>
                    <span class="sv-core__label">C{{ i }}</span>
                  </div>
                </div>
                <div v-if="!perCore.length" class="sv-panel-empty">每核心负载暂不可用</div>
                <div class="sv-cores-note">每个纵条代表一个逻辑核心 · 悬停查看明细</div>
              </div>
            </div>
          </div>

          <!-- 内存：系统水位 + 交换分区 + Go 运行时明细 -->
          <div class="sv-card !p-4">
            <div class="mb-4 flex items-center justify-between gap-2">
              <div class="flex items-center gap-2.5">
                <span class="sv-chip flex-cc" style="color: #7c3aed; background: #7c3aed14">
                  <ArtSvgIcon icon="ri:database-2-line" />
                </span>
                <div>
                  <div class="text-sm font-semibold text-[var(--el-text-color-primary)]">内存</div>
                  <div class="sv-card__sub">系统水位与 Go 运行时明细</div>
                </div>
              </div>
              <span
                v-if="systemMem"
                class="sv-chip-label tabular-nums"
                :style="{ color: memTone, background: `${memTone}14` }"
              >
                {{ fmt1(systemMem.usedPercent) }}%
              </span>
            </div>

            <template v-if="systemMem">
              <div class="sv-water">
                <div
                  class="sv-water__bar"
                  role="img"
                  :aria-label="`系统内存使用率 ${fmt1(systemMem.usedPercent)}%`"
                >
                  <span
                    class="sv-water__fill"
                    :style="{ width: `${clamp01(systemMem.usedPercent)}%`, background: memTone }"
                  />
                </div>
                <div class="sv-water__legend">
                  <span class="text-g-600">
                    可用
                    <b class="ml-0.5 tabular-nums text-[var(--el-text-color-primary)]">{{
                      fmtMB(systemMem.availableMB)
                    }}</b>
                  </span>
                  <span class="text-g-600">
                    已用
                    <b class="ml-0.5 tabular-nums" :style="{ color: memTone }">
                      {{ fmtMB(systemMem.usedMB) }} / {{ fmtMB(systemMem.totalMB) }}
                    </b>
                  </span>
                </div>
              </div>

              <div class="sv-cells">
                <div v-for="c in runtimeCells" :key="c.label" class="sv-cell" :title="c.title">
                  <div class="sv-cell__label">{{ c.label }}</div>
                  <div class="sv-cell__value tabular-nums">{{ c.value }}</div>
                </div>
              </div>

              <div v-if="swap" class="sv-swap">
                <div class="sv-swap__head">
                  <span class="text-xs text-g-600">Swap 交换分区</span>
                  <span class="text-xs tabular-nums text-g-600">
                    {{ fmtMB(swap.usedMB) }} / {{ fmtMB(swap.totalMB) }} ·
                    {{ fmt1(swap.usedPercent) }}%
                  </span>
                </div>
                <div class="sv-swap__bar">
                  <span
                    class="sv-swap__fill"
                    :style="{
                      width: `${clamp01(swap.usedPercent)}%`,
                      background: usageTone(swap.usedPercent)
                    }"
                  />
                </div>
              </div>
            </template>
            <div v-else class="sv-panel-empty">系统内存信息暂不可用</div>
          </div>

          <!-- 历史趋势:服务端 Redis 历史,范围切换 + 指标多选 -->
          <div class="sv-card sv-trend-card !p-4">
            <div class="mb-4 flex flex-wrap items-center justify-between gap-2">
              <div class="flex items-center gap-2.5">
                <span class="sv-chip flex-cc" style="color: #06b6d4; background: #06b6d414">
                  <ArtSvgIcon icon="ri:line-chart-line" />
                </span>
                <div>
                  <div class="text-sm font-semibold text-[var(--el-text-color-primary)]"
                    >历史趋势</div
                  >
                  <div class="sv-card__sub"
                    >最近 {{ rangeLabel }} · Redis 持久化 · 刷新/重启不丢</div
                  >
                </div>
              </div>
              <div class="flex flex-wrap items-center gap-2">
                <div
                  class="sv-range flex items-center gap-0.5"
                  role="radiogroup"
                  aria-label="历史时间范围"
                >
                  <button
                    v-for="r in RANGES"
                    :key="r.key"
                    type="button"
                    :class="['sv-range__item', { 'is-active': rangeKey === r.key }]"
                    :aria-pressed="rangeKey === r.key"
                    @click="switchRange(r.key)"
                  >
                    {{ r.label }}
                  </button>
                </div>
                <span class="sv-summary-pill tabular-nums">
                  <ArtSvgIcon v-if="historyLoading" icon="ri:loader-4-line" class="animate-spin" />
                  {{ buckets.length }} 桶
                </span>
              </div>
            </div>

            <div
              class="mb-3 flex flex-wrap items-center gap-1.5"
              role="group"
              aria-label="指标选择"
            >
              <button
                v-for="m in METRICS"
                :key="m.key"
                type="button"
                :class="['sv-metric', { 'is-active': selectedMetrics.includes(m.key) }]"
                :aria-pressed="selectedMetrics.includes(m.key)"
                @click="toggleMetric(m.key)"
              >
                {{ m.label }}
              </button>
            </div>

            <ArtLineChart
              height="240px"
              :data="trendSeries"
              :x-axis-data="timeLabels"
              :colors="activeColors"
              :show-legend="true"
              legend-position="bottom"
              :smooth="false"
              :loading="historyLoading && !historyData"
              :live="true"
              :show-data-zoom="true"
            />
            <div
              v-if="!trendSeries.length && historyData && !historyLoading"
              class="sv-panel-empty"
            >
              请选择至少一个指标，或等待采样…
            </div>
          </div>
        </div>

        <!-- 右列：主机信息 + 磁盘 -->
        <div class="flex min-w-0 flex-col gap-4 lg:col-span-2">
          <div class="sv-card !p-4">
            <div class="mb-3 flex items-center justify-between gap-2">
              <div class="flex items-center gap-2.5">
                <span class="sv-chip flex-cc" style="color: #3b82f6; background: #3b82f614">
                  <ArtSvgIcon icon="ri:computer-line" />
                </span>
                <div>
                  <div class="text-sm font-semibold text-[var(--el-text-color-primary)]"
                    >主机信息</div
                  >
                  <div class="sv-card__sub">身份 · 环境 · 服务指纹</div>
                </div>
              </div>
              <span v-if="stats.host?.pid" class="sv-summary-pill tabular-nums"
                >PID {{ stats.host.pid }}</span
              >
            </div>

            <div v-for="(rows, key) in hostGroups" :key="key" class="sv-host-group">
              <div class="sv-host-group__title">{{ hostGroupTitles[key] ?? key }}</div>
              <div v-for="row in rows" :key="row.label" class="sv-info-row">
                <span class="sv-info-row__icon flex-cc">
                  <ArtSvgIcon :icon="row.icon" />
                </span>
                <span class="sv-info-row__label flex-none">{{ row.label }}</span>
                <span
                  class="sv-info-row__value"
                  :class="{ 'is-mono': row.mono, 'is-chip': row.chip }"
                  :title="row.value"
                >
                  {{ row.value }}
                </span>
              </div>
            </div>
          </div>

          <div class="sv-card flex-1 !p-4">
            <div class="mb-4 flex items-center justify-between gap-2">
              <div class="flex items-center gap-2.5">
                <span class="sv-chip flex-cc" style="color: #f59e0b; background: #f59e0b14">
                  <ArtSvgIcon icon="ri:hard-drive-2-line" />
                </span>
                <div>
                  <div class="text-sm font-semibold text-[var(--el-text-color-primary)]">磁盘</div>
                  <div class="sv-card__sub">应用工作目录所在分区</div>
                </div>
              </div>
              <span
                v-if="stats.disk.usagePercent >= 85"
                class="sv-warn"
                title="磁盘使用率已超过 85%，建议及时清理"
              >
                <ArtSvgIcon icon="ri:error-warning-line" />
                空间紧张
              </span>
            </div>

            <div class="flex flex-wrap items-center justify-center gap-5">
              <div
                class="sv-donut"
                role="img"
                :aria-label="`磁盘使用率 ${fmt1(stats.disk.usagePercent)}%`"
              >
                <svg viewBox="0 0 96 96">
                  <circle class="sv-ring__track" cx="48" cy="48" r="42" />
                  <circle
                    class="sv-ring__bar"
                    cx="48"
                    cy="48"
                    r="42"
                    :stroke="diskTone"
                    :stroke-dasharray="donutDash"
                    :stroke-dashoffset="diskOffset"
                    transform="rotate(-90 48 48)"
                  />
                </svg>
                <div class="sv-donut__center">
                  <div class="sv-donut__value tabular-nums"
                    >{{ fmt1(stats.disk.usagePercent) }}%</div
                  >
                  <div class="sv-donut__label">已用 {{ stats.disk.usedGB.toFixed(0) }} GB</div>
                </div>
              </div>
              <div class="sv-cells sv-cells--disk">
                <div class="sv-cell" title="分区总容量">
                  <div class="sv-cell__label">总容量</div>
                  <div class="sv-cell__value tabular-nums">{{ fmtGB(stats.disk.totalGB) }}</div>
                </div>
                <div class="sv-cell" title="已用空间">
                  <div class="sv-cell__label">已用</div>
                  <div class="sv-cell__value tabular-nums" :style="{ color: diskTone }">{{
                    fmtGB(stats.disk.usedGB)
                  }}</div>
                </div>
                <div class="sv-cell" title="可用空间">
                  <div class="sv-cell__label">可用</div>
                  <div class="sv-cell__value tabular-nums">{{ fmtGB(stats.disk.freeGB) }}</div>
                </div>
                <div class="sv-cell" title="可用空间占比">
                  <div class="sv-cell__label">剩余占比</div>
                  <div class="sv-cell__value tabular-nums">{{ fmt1(diskFreePercent) }}%</div>
                </div>
              </div>
            </div>

            <div class="sv-disk-path" :title="stats.disk.path">
              <ArtSvgIcon icon="ri:folder-3-line" class="flex-none" />
              <span class="min-w-0 truncate">{{ stats.disk.path || '-' }}</span>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
  import { computed, onBeforeUnmount, ref, watch } from 'vue'
  import { ElMessage } from 'element-plus'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import type { LineDataItem } from '@/types/component/chart'
  import { fetchServerHistory, fetchServerStats } from '../api'
  import type { ServerHistory, ServerHistoryPoint } from '../api'
  import { buildSparkSeries, sparkPath } from '../composables/use-spark-series'
  // 纯格式化 / 阈值配色工具(原散落在本文件,已抽出并配套单测)
  import {
    clamp01,
    fmt1,
    fmt2,
    fmtDurationText,
    fmtGB,
    fmtMB,
    loadTone,
    usageTone
  } from '../composables/use-server-format'

  defineOptions({ name: 'MonitorServer' })

  const AUTO_REFRESH_MS = 10_000

  /** 时间范围预设：窗口 + 分桶粒度（与后端 /history 参数一一对应） */
  const RANGES = [
    { key: '15m', label: '15 分钟', window: '15m', step: '3s' },
    { key: '1h', label: '1 小时', window: '1h', step: '12s' },
    { key: '6h', label: '6 小时', window: '6h', step: '60s' },
    { key: '24h', label: '24 小时', window: '24h', step: '240s' }
  ] as const

  /** 历史指标：key 对应后端桶字段，color 用于多序列着色 */
  const METRICS = [
    { key: 'cpu', label: 'CPU', color: '#3b82f6' },
    { key: 'memSys', label: '内存占用率', color: '#f59e0b' },
    { key: 'heapAlloc', label: '堆内存', color: '#10b981' },
    { key: 'sysMem', label: '进程内存', color: '#06b6d4' },
    { key: 'goroutines', label: 'Goroutines', color: '#7c3aed' },
    { key: 'gcNum', label: 'GC 次数', color: '#ec4899' },
    { key: 'gcPauseMs', label: 'GC 暂停', color: '#f97316' },
    { key: 'disk', label: '磁盘占用', color: '#94a3b8' },
    { key: 'load1', label: '1 分钟负载', color: '#14b8a6' }
  ] as const

  type MetricKey = (typeof METRICS)[number]['key']

  /** KPI 迷你趋势:由历史桶尾派生(单一数据源),语义恒定 ≈ 最近 10 分钟 */

  interface KpiTile {
    label: string
    icon: string
    tile: string
    tile2: string
    color: string
    value: string
    sub: string
    title: string
    line: string
    area: string
  }

  const loading = ref(false)
  const stats = ref<Api.Monitor.ServerStats | null>(null)
  const historyData = ref<ServerHistory | null>(null)
  const historyLoading = ref(false)
  const autoRefresh = ref(true)
  const updatedAt = ref<Date | null>(null)
  const rangeKey = ref<(typeof RANGES)[number]['key']>('15m')
  const selectedMetrics = ref<MetricKey[]>(['cpu', 'memSys'])
  let loadSeq = 0
  let historySeq = 0
  let timer: number | undefined

  // ---------- 数据加载（seq 守卫丢弃过期响应） ----------
  async function loadStats(silent = false) {
    const seq = ++loadSeq
    if (!silent) loading.value = true
    try {
      const data = await fetchServerStats()
      if (seq !== loadSeq) return
      stats.value = data
      updatedAt.value = new Date()
    } catch {
      if (seq !== loadSeq) return
      if (!silent) ElMessage.error('服务器监控数据获取失败，请稍后重试')
    } finally {
      if (seq === loadSeq) loading.value = false
    }
  }

  async function loadHistory(silent = false) {
    const seq = ++historySeq
    if (!silent) historyLoading.value = true
    try {
      const data = await fetchServerHistory({
        window: activeRange.value.window,
        step: activeRange.value.step
      })
      if (seq !== historySeq) return
      historyData.value = data
      updatedAt.value = new Date()
    } catch {
      if (seq !== historySeq) return
      if (!silent) ElMessage.error('历史趋势获取失败，请稍后重试')
    } finally {
      if (seq === historySeq) historyLoading.value = false
    }
  }

  async function loadAll(silent = false) {
    // 单一数据源:趋势图与 KPI 迷你趋势同源,迷你趋势由桶尾派生,
    // 不再对 /monitor/server/history 发起第二份(10m)请求。
    await Promise.all([loadStats(silent), loadHistory(silent)])
  }

  function switchRange(key: (typeof RANGES)[number]['key']) {
    if (rangeKey.value === key) return
    rangeKey.value = key
    loadHistory(false)
  }

  function toggleMetric(key: MetricKey) {
    const idx = selectedMetrics.value.indexOf(key)
    if (idx >= 0) {
      selectedMetrics.value.splice(idx, 1)
    } else {
      selectedMetrics.value.push(key)
    }
  }

  // ---------- 自动刷新（页面隐藏时跳过，静默失败不打扰） ----------
  watch(
    autoRefresh,
    (on) => {
      if (timer) {
        clearInterval(timer)
        timer = undefined
      }
      if (on) {
        timer = window.setInterval(() => {
          if (!document.hidden) loadAll(true)
        }, AUTO_REFRESH_MS)
      }
    },
    { immediate: true }
  )

  onBeforeUnmount(() => {
    if (timer) clearInterval(timer)
  })

  // ---------- 格式化 ----------
  // ---------- 状态阈值配色：<50 绿 / <80 蓝 / <90 琥珀 / ≥90 红 ----------
  // ---------- 迷你折线（SVG polyline，viewBox 100×50） ----------
  // ---------- 服务端历史 computed ----------
  const activeRange = computed(() => RANGES.find((r) => r.key === rangeKey.value) ?? RANGES[0])
  const rangeLabel = computed(() => activeRange.value.label)
  const buckets = computed(() => historyData.value?.buckets ?? [])

  const pad2 = (n: number) => String(n).padStart(2, '0')
  const tsOf = (v: string) => Date.parse(v.replace(' ', 'T'))

  // ---------- 固定槽位窗口(绝对时间对齐,参考 SQL 监控的 slotFrame) ----------
  // 若直接把桶数组铺到轴上,每 10s 新增几桶后数组长度变化,ArtLineChart
  // 会把长度变化视为"新图表"重播入场动画,视觉上整条线反复重画(像全量加载)。
  // 改为按 step 切成长度恒定的槽位:旧点像素位置恒定不动,新数据只出现在
  // 最右端,配合 live 模式短过渡收敛,形成真正的增量滚动效果。
  const slotCount = computed(() => {
    const h = historyData.value
    if (!h || !h.step_seconds) return 0
    const n = Math.floor(h.window_seconds / h.step_seconds)
    return Math.min(Math.max(n, 1), 400)
  })

  interface SlotFrame {
    labels: string[]
    slots: (ServerHistoryPoint | null)[]
  }

  const slotFrame = computed<SlotFrame>(() => {
    const empty: SlotFrame = { labels: [], slots: [] }
    const h = historyData.value
    const src = buckets.value
    if (!h || !h.step_seconds || !src.length) return empty

    const stepMs = h.step_seconds * 1000
    const count = slotCount.value
    const slotMap = new Map<number, ServerHistoryPoint>()
    for (const b of src) {
      slotMap.set(Math.floor(tsOf(b.timestamp) / stepMs), b)
    }
    const lastSlot = Math.floor(tsOf(src[src.length - 1].timestamp) / stepMs)

    const labels: string[] = []
    const slots: (ServerHistoryPoint | null)[] = []
    for (let s = lastSlot - count + 1; s <= lastSlot; s++) {
      const d = new Date(s * stepMs)
      labels.push(`${pad2(d.getHours())}:${pad2(d.getMinutes())}:${pad2(d.getSeconds())}`)
      slots.push(slotMap.get(s) ?? null)
    }
    return { labels, slots }
  })

  const timeLabels = computed(() => slotFrame.value.labels)

  const METRIC_MAP = Object.fromEntries(METRICS.map((m) => [m.key, m])) as unknown as Record<
    MetricKey,
    { label: string; color: string }
  >

  const activeColors = computed(() => selectedMetrics.value.map((k) => METRIC_MAP[k].color))

  const trendSeries = computed<LineDataItem[]>(() =>
    selectedMetrics.value.map((k) => ({
      name: METRIC_MAP[k].label,
      // 空槽断线(null):后端重启/停机间隙不零填充,与桶缺失语义一致。
      // 后端聚合均值已在服务端量化到 2 位(round2),这里 toFixed(2)
      // 仅是防御性兜底(旧缓存/历史数据),保证 tooltip 不溢出两位。
      data: slotFrame.value.slots.map((b) => {
        if (!b) return null
        const v = b[k]
        return typeof v === 'number' ? Number(v.toFixed(2)) : null
      }) as unknown as number[],
      lineWidth: 2
    }))
  )

  // ---------- 仪表环 / 圆环几何 ----------
  const GAUGE_R = 52
  const GAUGE_C = 2 * Math.PI * GAUGE_R
  const gaugeDash = `${GAUGE_C.toFixed(2)} ${GAUGE_C.toFixed(2)}`
  const DONUT_R = 42
  const DONUT_C = 2 * Math.PI * DONUT_R
  const donutDash = `${DONUT_C.toFixed(2)} ${DONUT_C.toFixed(2)}`

  const cpuPercent = computed(() => stats.value?.cpu.usagePercent ?? null)
  const cpuText = computed(() => {
    const p = cpuPercent.value
    return p === null ? '-' : `${Math.round(p)}%`
  })
  const cpuTone = computed(() => usageTone(cpuPercent.value))
  const cpuOffset = computed(() => (GAUGE_C * (1 - clamp01(cpuPercent.value) / 100)).toFixed(2))

  const systemMem = computed(() => stats.value?.memory.system ?? null)
  const swap = computed(() => stats.value?.memory.swap ?? null)
  const memTone = computed(() => usageTone(systemMem.value?.usedPercent ?? null))

  const diskPercent = computed(() => stats.value?.disk.usagePercent ?? null)
  const diskTone = computed(() => usageTone(diskPercent.value))
  const diskOffset = computed(() => (DONUT_C * (1 - clamp01(diskPercent.value) / 100)).toFixed(2))
  const diskFreePercent = computed(() => {
    const d = stats.value?.disk
    if (!d || !d.totalGB) return null
    return (d.freeGB / d.totalGB) * 100
  })

  // ---------- 每核心 / 负载 ----------
  const perCore = computed<number[]>(() => stats.value?.cpu.perCore ?? [])
  const numCPU = computed(() => stats.value?.cpu.numCPU ?? 0)

  const loadChips = computed(() => {
    const load = stats.value?.cpu.load ?? null
    const cores = numCPU.value
    const mk = (key: string, v: number | null | undefined) => ({
      key,
      text: fmt2(v),
      title: `${key} 分钟负载均值${typeof v === 'number' ? ` ${fmt2(v)}` : '（暂不可用）'}`,
      color: loadTone(v ?? null, cores)
    })
    return [
      mk('1m', load?.load1 ?? null),
      mk('5m', load?.load5 ?? null),
      mk('15m', load?.load15 ?? null)
    ]
  })

  // ---------- Go 运行时内存明细 ----------
  interface RuntimeCell {
    label: string
    value: string
    title: string
  }

  const runtimeCells = computed<RuntimeCell[]>(() => {
    const m = stats.value?.memory
    if (!m) return []
    const share =
      systemMem.value && systemMem.value.totalMB > 0
        ? (m.allocMB / systemMem.value.totalMB) * 100
        : null
    return [
      { label: '当前分配', value: fmtMB(m.allocMB), title: '进程当前分配的堆对象字节数 (Alloc)' },
      {
        label: '累计分配',
        value: fmtMB(m.totalAllocMB),
        title: '进程启动以来累计分配字节数 (TotalAlloc)'
      },
      { label: '系统保留', value: fmtMB(m.sysMB), title: '运行时向操作系统申请的内存 (Sys)' },
      { label: '堆已用', value: fmtMB(m.heapAllocMB), title: '堆上正在使用的字节数 (HeapAlloc)' },
      { label: '堆预留', value: fmtMB(m.heapSysMB), title: '堆向系统预取的字节数 (HeapSys)' },
      {
        label: '进程占比',
        value: share === null ? '-' : `${fmt2(share)}%`,
        title: '当前分配占物理内存比例'
      }
    ]
  })

  // ---------- 主机信息分组 ----------
  interface InfoRow {
    icon: string
    label: string
    value: string
    mono?: boolean
    chip?: boolean
  }

  const hostGroupTitles: Record<string, string> = {
    identity: '主机身份',
    runtime: '运行环境',
    service: '服务信息'
  }

  const hostGroups = computed<Record<string, InfoRow[]>>(() => {
    const host = stats.value?.host ?? null
    const srv = stats.value?.server
    return {
      identity: [
        { icon: 'ri:computer-line', label: '主机名', value: host?.hostname || '-', mono: true },
        {
          icon: 'ri:terminal-box-line',
          label: '操作系统',
          value: host?.os ? [host.os, host.platformVersion].filter(Boolean).join(' · ') : '-'
        },
        {
          icon: 'ri:terminal-line',
          label: '内核',
          value: [host?.kernelVersion, host?.kernelArch].filter(Boolean).join(' · ') || '-',
          mono: true
        },
        {
          icon: 'ri:cpu-line',
          label: '架构',
          value: host?.arch ? `${host.arch} · ${numCPU.value} 核` : `${numCPU.value} 核`
        }
      ],
      runtime: [
        {
          icon: 'ri:code-s-slash-line',
          label: 'Go 版本',
          value: host?.goVersion || '-',
          mono: true
        },
        {
          icon: 'ri:numbers-line',
          label: '进程 PID',
          value: host?.pid != null ? String(host.pid) : '-',
          mono: true
        },
        {
          icon: 'ri:time-line',
          label: '系统运行',
          value: host?.uptimeSeconds != null ? fmtDurationText(host.uptimeSeconds) : '-'
        },
        { icon: 'ri:restart-line', label: '开机时间', value: host?.bootTime || '-' }
      ],
      service: [
        { icon: 'ri:calendar-check-line', label: '服务启动', value: srv?.startTime || '-' },
        { icon: 'ri:timer-line', label: '运行时长', value: srv?.uptime || '-' },
        {
          icon: 'ri:information-line',
          label: '版本',
          value: srv?.version ? `${srv.version}` : '-',
          chip: true
        },
        {
          icon: 'ri:calendar-line',
          label: '构建时间',
          value: (srv?.buildTime || '-').slice(0, 10)
        },
        { icon: 'ri:git-commit-line', label: '提交哈希', value: srv?.commitHash || '-', mono: true }
      ]
    }
  })

  // ---------- 页头主机芯片 ----------
  const hostChips = computed(() => {
    const host = stats.value?.host
    const srv = stats.value?.server
    const chips: { icon: string; text: string }[] = []
    if (host?.hostname) chips.push({ icon: 'ri:computer-line', text: host.hostname })
    if (host?.os)
      chips.push({
        icon: 'ri:terminal-box-line',
        text: [host.os, host.platformVersion].filter(Boolean).join(' · ')
      })
    if (srv?.version) chips.push({ icon: 'ri:information-line', text: `${srv.version}` })
    return chips
  })

  // ---------- KPI 磁贴（合计 6 项，颜色取自全局主色板） ----------
  /** 从历史桶尾派生某指标的迷你趋势序列(窗口 ≈ SPARK_WINDOW_SECONDS) */
  // 'uptime' 不在趋势指标 chips 中,但 KPI 运行时长卡片的迷你趋势仍需要它
  function seriesOf(key: MetricKey | 'uptime'): number[] {
    const h = historyData.value
    if (!h || !h.step_seconds) return []
    return buildSparkSeries(h.buckets, h.step_seconds, key)
  }

  const kpis = computed<KpiTile[]>(() => {
    const cpuSeries = seriesOf('cpu')
    const memSeries = seriesOf('memSys')
    const diskSeries = seriesOf('disk')
    const gorSeries = seriesOf('goroutines')
    const gcSeries = seriesOf('gcNum')
    const upSeries = seriesOf('uptime')
    const cpuSpark = sparkPath(cpuSeries)
    const memSpark = sparkPath(memSeries)
    const diskSpark = sparkPath(diskSeries)
    const gorSpark = sparkPath(gorSeries)
    const gcSpark = sparkPath(gcSeries)
    const upSpark = sparkPath(upSeries)
    const hasStats = stats.value !== null

    return [
      {
        label: 'CPU 使用率',
        icon: 'ri:cpu-line',
        tile: '#3b82f6',
        tile2: '#60a5fa',
        color: '#3b82f6',
        value: hasStats ? cpuText.value : '-',
        sub:
          stats.value?.cpu.load?.load1 != null
            ? `负载 1m ${fmt2(stats.value.cpu.load.load1)}`
            : `${numCPU.value} 核`,
        title: 'CPU 整体使用率（含迷你趋势）',
        line: cpuSpark.line,
        area: cpuSpark.area
      },
      {
        label: '内存使用',
        icon: 'ri:database-2-line',
        tile: '#7c3aed',
        tile2: '#a78bfa',
        color: '#7c3aed',
        value: systemMem.value ? `${fmt1(systemMem.value.usedPercent)}%` : '-',
        sub: systemMem.value
          ? `已用 ${fmtGB(systemMem.value.usedMB / 1024)} / ${fmtGB(systemMem.value.totalMB / 1024)}`
          : '暂无数据',
        title: '系统物理内存使用率（含迷你趋势）',
        line: memSpark.line,
        area: memSpark.area
      },
      {
        label: '磁盘使用',
        icon: 'ri:hard-drive-2-line',
        tile: '#f59e0b',
        tile2: '#fbbf24',
        color: '#f59e0b',
        value: hasStats ? `${fmt1(stats.value!.disk.usagePercent)}%` : '-',
        sub: hasStats ? `已用 ${fmtGB(stats.value!.disk.usedGB)}` : '暂无数据',
        title: '工作目录所在分区使用率（含迷你趋势）',
        line: diskSpark.line,
        area: diskSpark.area
      },
      {
        label: 'Goroutines',
        icon: 'ri:git-branch-line',
        tile: '#10b981',
        tile2: '#34d399',
        color: '#10b981',
        value: hasStats ? String(stats.value!.goroutines.count) : '-',
        sub:
          hasStats && stats.value!.gc.numGC > 0
            ? `GC 已执行 ${stats.value!.gc.numGC} 次`
            : '活跃协程数',
        title: 'Go 协程数量（含迷你趋势）',
        line: gorSpark.line,
        area: gorSpark.area
      },
      {
        label: 'GC 暂停',
        icon: 'ri:recycle-line',
        tile: '#06b6d4',
        tile2: '#22d3ee',
        color: '#06b6d4',
        value: hasStats ? `${fmt1(stats.value!.gc.lastPauseMs)}ms` : '-',
        sub: hasStats
          ? `累计 ${fmt1(stats.value!.gc.pauseTotalMs)}ms · ${stats.value!.gc.numGC} 次`
          : '最近一次 GC 暂停',
        title: '最近一次 GC 暂停耗时（含累计趋势）',
        line: gcSpark.line,
        area: gcSpark.area
      },
      {
        label: '运行时长',
        icon: 'ri:time-line',
        tile: '#ec4899',
        tile2: '#f472b6',
        color: '#ec4899',
        value: hasStats ? stats.value!.server.uptime : '-',
        sub: hasStats
          ? `自 ${stats.value!.server.startTime.substring(5, 16)} 启动`
          : '后端进程已运行时间',
        title: '后端进程运行时长（含迷你趋势）',
        line: upSpark.line,
        area: upSpark.area
      }
    ]
  })

  // ---------- 最近更新时间 ----------
  const updatedAtText = computed(() => {
    const d = updatedAt.value
    return d ? d.toLocaleTimeString('zh-CN', { hour12: false }) : '—'
  })

  loadAll(false)
</script>

<style scoped lang="scss">
  /* 四视图共享的设计令牌(卡片 / KPI / 呼吸圆点 / 入场动画) */
  @use './monitor-tokens' as t;

  @include t.rise-keyframes;
  @include t.pulse-keyframes;

  /* ---------- 基础卡片 ---------- */
  .sv-card {
    @include t.card;
    /* 本视图特有:卡片可交互(hover 抬起),较共享令牌多一组 transition */
    transition:
      transform 0.2s ease,
      box-shadow 0.2s ease,
      border-color 0.2s ease;
    @include t.rise;
  }

  .dark .sv-card {
    @include t.card-dark;
  }

  /* ---------- 页头 ---------- */
  .sv-hero__icon {
    width: 44px;
    height: 44px;
    flex: none;
    border-radius: 14px;
    font-size: 22px;
    color: #fff;
    background: linear-gradient(135deg, #3b82f6 0%, #7c3aed 100%);
    box-shadow: 0 4px 12px rgba(59, 130, 246, 0.35);
  }

  .live-dot {
    @include t.live-dot;
  }

  .live-dot.is-loading {
    background: #f59e0b;
    animation-duration: 0.9s;
  }

  .sv-hero-chip {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    max-width: 220px;
    padding: 3px 10px;
    border-radius: 999px;
    border: 1px solid var(--default-border);
    background: var(--art-gray-100);
    color: var(--el-text-color-secondary);
    font-size: 12px;
    white-space: nowrap;
  }

  .sv-hero-chip :deep(svg) {
    flex: none;
    color: var(--el-color-primary);
  }

  /* ---------- KPI 磁贴 ---------- */
  .kpi-tile {
    display: flex;
    align-items: center;
    gap: 0.75rem;
  }

  .kpi-grid > *:nth-child(2) {
    animation-delay: 40ms;
  }
  .kpi-grid > *:nth-child(3) {
    animation-delay: 80ms;
  }
  .kpi-grid > *:nth-child(4) {
    animation-delay: 120ms;
  }
  .kpi-grid > *:nth-child(5) {
    animation-delay: 160ms;
  }
  .kpi-grid > *:nth-child(6) {
    animation-delay: 200ms;
  }

  .kpi-tile:hover {
    transform: translateY(-2px);
    box-shadow: 0 8px 18px rgba(16, 24, 40, 0.09);
    border-color: var(--el-color-primary-light-5);
  }

  .kpi-tile__icon {
    @include t.kpi-icon;
    font-size: 19px;
    color: #fff;
    background: linear-gradient(135deg, var(--tile) 0%, var(--tile2) 100%);
    box-shadow: 0 4px 10px color-mix(in srgb, var(--tile) 35%, transparent);
  }

  .kpi-tile__value {
    @include t.kpi-value;
  }

  .kpi-tile__label {
    @include t.kpi-label;
  }

  .kpi-tile__sub {
    @include t.kpi-sub;
  }

  .kpi-tile__spark {
    /* 宽度随卡片自适应:52px 基准,吃掉同行剩余空间,上下限防丑 */
    flex: 1 1 52px;
    min-width: 40px;
    max-width: 160px;
    height: 26px;
  }

  .kpi-tile__spark--ph {
    border-radius: 6px;
    background: var(--el-fill-color-light);
  }

  @media (max-width: 639px) {
    .kpi-tile__spark {
      display: none;
    }
  }

  /* ---------- 卡片头 ---------- */
  .sv-chip {
    width: 34px;
    height: 34px;
    flex: none;
    border-radius: 10px;
    font-size: 17px;
  }

  .sv-chip-label {
    padding: 3px 10px;
    border-radius: 999px;
    font-size: 12px;
    font-weight: 600;
  }

  .sv-card__sub {
    @include t.card-sub;
  }

  .sv-summary-pill {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 3px 10px;
    border-radius: 999px;
    border: 1px solid var(--default-border);
    background: var(--art-gray-100);
    color: var(--el-text-color-secondary);
    font-size: 12px;
    font-variant-numeric: tabular-nums;
  }

  .sv-load-chip {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding: 3px 9px;
    border-radius: 999px;
    border: 1px solid var(--default-border);
    background: var(--art-gray-100);
    color: var(--el-text-color-secondary);
    font-size: 11px;
    white-space: nowrap;
  }

  .sv-load-chip__dot {
    width: 6px;
    height: 6px;
    border-radius: 999px;
    flex: none;
  }

  .sv-load-chip__key {
    color: var(--el-text-color-regular);
    font-variant-numeric: tabular-nums;
  }

  /* ---------- 仪表环 / 圆环 ---------- */
  .sv-gauge,
  .sv-donut {
    position: relative;
    width: 128px;
    height: 128px;
    flex: none;
  }

  .sv-ring__track {
    fill: none;
    stroke: var(--el-fill-color-light);
    stroke-width: 11;
  }

  .sv-ring__bar {
    fill: none;
    stroke-width: 11;
    stroke-linecap: round;
    transition:
      stroke-dashoffset 0.45s ease,
      stroke 0.3s ease;
  }

  .sv-gauge__center,
  .sv-donut__center {
    position: absolute;
    inset: 0;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 2px;
  }

  .sv-gauge__value,
  .sv-donut__value {
    font-size: 24px;
    font-weight: 650;
    color: var(--el-text-color-primary);
    font-variant-numeric: tabular-nums;
  }

  .sv-gauge__label,
  .sv-donut__label {
    font-size: 11px;
    color: var(--el-text-color-secondary);
  }

  .sv-donut .sv-ring__track {
    stroke-width: 12;
  }

  .sv-donut .sv-ring__bar {
    stroke-width: 12;
  }

  .sv-donut__value {
    font-size: 20px;
  }

  /* ---------- 每核心热力柱 ---------- */
  .sv-cores {
    display: grid;
    grid-template-columns: repeat(8, minmax(0, 1fr));
    gap: 10px 8px;
  }

  .sv-core {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 4px;
  }

  .sv-core__track {
    position: relative;
    display: block;
    width: 100%;
    height: 76px;
    border-radius: 7px;
    background: var(--el-fill-color-light);
    overflow: hidden;
  }

  .sv-core__fill {
    position: absolute;
    left: 0;
    right: 0;
    bottom: 0;
    border-radius: 7px 7px 0 0;
    transition: height 0.45s ease;
  }

  .sv-core__label {
    font-size: 10px;
    color: var(--el-text-color-secondary);
    font-variant-numeric: tabular-nums;
  }

  .sv-cores-note {
    margin-top: 8px;
    font-size: 11px;
    color: var(--el-text-color-secondary);
  }

  /* ---------- 内存水位 ---------- */
  .sv-water__bar {
    position: relative;
    height: 10px;
    border-radius: 999px;
    background: var(--el-fill-color-light);
    overflow: hidden;
  }

  .sv-water__fill {
    position: absolute;
    left: 0;
    top: 0;
    bottom: 0;
    border-radius: 999px;
    transition: width 0.45s ease;
  }

  .sv-water__legend {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-top: 8px;
    font-size: 12px;
  }

  .sv-cells {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 8px;
    margin-top: 14px;
  }

  @media (min-width: 640px) {
    .sv-cells {
      grid-template-columns: repeat(6, minmax(0, 1fr));
    }
  }

  .sv-cell {
    padding: 8px 10px;
    border-radius: 10px;
    background: var(--art-gray-100);
  }

  .sv-cell__label {
    font-size: 11px;
    color: var(--el-text-color-secondary);
  }

  .sv-cell__value {
    margin-top: 2px;
    font-size: 13px;
    font-weight: 600;
    color: var(--el-text-color-primary);
    font-variant-numeric: tabular-nums;
  }

  .sv-swap {
    margin-top: 14px;
  }

  .sv-swap__head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 6px;
  }

  .sv-swap__bar {
    position: relative;
    height: 6px;
    border-radius: 999px;
    background: var(--el-fill-color-light);
    overflow: hidden;
  }

  .sv-swap__fill {
    position: absolute;
    left: 0;
    top: 0;
    bottom: 0;
    border-radius: 999px;
    transition: width 0.45s ease;
  }

  /* ---------- 历史趋势:范围分段 + 指标 chips ---------- */
  .sv-range {
    padding: 2px;
    border-radius: 10px;
    border: 1px solid var(--default-border);
    background: var(--art-gray-100);
  }

  .sv-range__item {
    padding: 4px 10px;
    border-radius: 8px;
    border: none;
    background: transparent;
    color: var(--el-text-color-secondary);
    font-size: 12px;
    line-height: 20px;
    cursor: pointer;
    transition:
      background 0.2s ease,
      color 0.2s ease;
  }

  .sv-range__item:hover {
    color: var(--el-text-color-primary);
  }

  .sv-range__item.is-active {
    background: var(--default-box-color);
    color: var(--el-color-primary);
    font-weight: 600;
    box-shadow: 0 1px 2px rgba(16, 24, 40, 0.08);
  }

  .sv-metric {
    padding: 3px 10px;
    border-radius: 9999px;
    border: 1px solid var(--default-border);
    background: var(--art-gray-100);
    color: var(--el-text-color-secondary);
    font-size: 12px;
    line-height: 20px;
    cursor: pointer;
    transition:
      background 0.2s ease,
      color 0.2s ease,
      border-color 0.2s ease;
  }

  .sv-metric:hover {
    color: var(--el-text-color-primary);
  }

  .sv-metric.is-active {
    background: var(--default-box-color);
    color: var(--el-color-primary);
    border-color: var(--el-color-primary);
    font-weight: 600;
  }

  /* ---------- 主机信息 ---------- */
  .sv-host-group + .sv-host-group {
    margin-top: 12px;
  }

  .sv-host-group__title {
    margin-bottom: 4px;
    font-size: 11px;
    font-weight: 600;
    letter-spacing: 0.06em;
    color: var(--el-text-color-secondary);
  }

  .sv-info-row {
    display: flex;
    align-items: center;
    gap: 9px;
    padding: 5px 4px;
    border-radius: 8px;
    transition: background 0.15s ease;
  }

  .sv-info-row:hover {
    background: var(--art-hover-color);
  }

  .sv-info-row__icon {
    width: 26px;
    height: 26px;
    flex: none;
    border-radius: 8px;
    font-size: 14px;
    color: var(--el-color-primary);
    background: var(--el-color-primary-light-9);
  }

  .dark .sv-info-row__icon {
    color: #8ab4ff;
    background: rgba(59, 130, 246, 0.16);
  }

  .sv-info-row__label {
    width: 58px;
    font-size: 12px;
    color: var(--el-text-color-secondary);
  }

  .sv-info-row__value {
    min-width: 0;
    flex: 1;
    text-align: right;
    font-size: 12px;
    color: var(--el-text-color-primary);
    font-variant-numeric: tabular-nums;
  }

  .sv-info-row__value.is-mono {
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, 'Fira Code', monospace;
    font-size: 11.5px;
  }

  .sv-info-row__value.is-chip {
    color: var(--el-color-primary);
    font-weight: 600;
  }

  /* ---------- 磁盘 ---------- */
  .sv-cells--disk {
    display: grid !important;
    grid-template-columns: repeat(2, minmax(120px, 1fr));
  }

  .sv-disk-path {
    display: flex;
    align-items: center;
    gap: 7px;
    margin-top: 16px;
    padding: 8px 12px;
    border-radius: 10px;
    background: var(--art-gray-100);
    color: var(--el-text-color-secondary);
    font-size: 11.5px;
  }

  .sv-disk-path :deep(svg) {
    color: #f59e0b;
  }

  .sv-disk-path span {
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, 'Fira Code', monospace;
  }

  .sv-warn {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 3px 10px;
    border-radius: 999px;
    border: 1px solid rgba(220, 38, 38, 0.35);
    background: rgba(220, 38, 38, 0.1);
    color: #dc2626;
    font-size: 11.5px;
    white-space: nowrap;
  }

  .dark .sv-warn {
    color: #fca5a5;
  }

  /* ---------- 空状态 ---------- */
  .sv-empty__icon {
    width: 64px;
    height: 64px;
    border-radius: 20px;
    font-size: 30px;
    color: var(--el-color-primary);
    background: rgba(59, 130, 246, 0.12);
  }

  .sv-panel-empty {
    padding: 10px;
    border-radius: 10px;
    border: 1px dashed var(--default-border-dashed);
    color: var(--el-text-color-secondary);
    font-size: 12px;
  }

  /* ---------- 动效降级 ---------- */
  /* 动画停用收敛到共享 mixin(其余视图曾各自重写同一媒体查询) */
  @include t.reduced-motion('.sv-card', '.live-dot');
  @media (prefers-reduced-motion: reduce) {
    .sv-core__fill,
    .sv-water__fill,
    .sv-swap__fill,
    .sv-ring__bar {
      transition: none;
    }
  }
</style>
