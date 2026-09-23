<template>
  <div class="device-overview art-full-height overflow-y-auto">
    <div class="device-overview__inner p-4 pb-8 md:p-5">
    <!-- ============ 页头：标题 + 实时状态 + 自动刷新（对齐服务监控 .sv-hero）============ -->
    <div class="do-hero mb-4 flex flex-wrap items-center gap-3">
      <div class="do-hero__icon flex-cc">
        <ArtSvgIcon icon="ri:dashboard-3-line" />
      </div>
      <div class="min-w-0">
        <h2 class="text-lg font-semibold text-[var(--el-text-color-primary)]">设备监控总览</h2>
        <p class="mt-0.5 flex flex-wrap items-center gap-1.5 text-xs text-g-600">
          <span
            class="live-dot inline-block h-1.5 w-1.5 rounded-full bg-success"
            :class="{ 'is-loading': loading }"
          />
          <span>{{ overviewSubtitle }}</span>
        </p>
      </div>
      <div class="ml-auto flex items-center gap-1">
        <ArtButtonTable
          :icon="'ri:timer-2-line'"
          :iconClass="autoRefresh ? 'bg-theme text-white shadow-sm' : 'bg-theme/12 text-theme'"
          :title="autoRefresh ? `关闭自动刷新（每 ${autoIntervalSec} 秒）` : '开启自动刷新'"
          @click="autoRefresh = !autoRefresh"
        />
        <ArtButtonTable
          icon="ri:refresh-line"
          iconClass="bg-theme/12 text-theme"
          title="刷新"
          @click="reload"
        />
      </div>
    </div>

    <!-- ============ 全局过滤条 ============ -->
    <!-- 「所有设备 × 各类指标」的总览页里，过滤条件是**页面级**的：
         它同时作用于概览条、快照表以及**每一张**图。故它必须常驻在页面顶部
         （而不是像列表页那样可折叠），并且带显式的「当前生效的筛选」摘要 ——
         否则用户看到一张只有 2 条线的图时，无法判断是「只有 2 台设备」
         还是「筛选把其它设备滤掉了」。 -->
    <div class="do-card do-filter">
      <div class="do-filter__row">
        <div class="do-filter__fields">
          <ElInput
            v-model="filters.hostname"
            class="do-filter__host"
            placeholder="主机名（模糊匹配）"
            clearable
            @keyup.enter="reload"
          />
          <ElSelect v-model="filters.status" class="do-filter__status" placeholder="全部状态" clearable>
            <ElOption label="启用" :value="1" />
            <ElOption label="停用" :value="0" />
          </ElSelect>
          <ElSelect v-model="filters.online" class="do-filter__status" placeholder="在线状态" clearable>
            <ElOption label="在线" :value="true" />
            <ElOption label="离线" :value="false" />
          </ElSelect>
        </div>

        <div class="do-filter__actions">
          <ElButton type="primary" :loading="loading" @click="reload">查询</ElButton>
          <ElButton :disabled="loading" @click="resetFilters">重置</ElButton>
        </div>
      </div>

      <div class="do-filter__row do-filter__row--ranges">
        <!-- 时间窗：与详情页**同一套**档位（后端同一个选档函数），
             故这里选 90 天与在详情页选 90 天得到的是同一批桶。 -->
        <ElRadioGroup v-model="rangeSeconds" size="small" @change="reload">
          <ElRadioButton v-for="r in rangeChoices" :key="r.seconds" :value="r.seconds">
            {{ r.label }}
          </ElRadioButton>
        </ElRadioGroup>

        <div class="do-filter__auto">
          <!-- 自动刷新：总览页的核心使用方式是「盯一眼」，
               手动刷新会让用户看到过时数据而不自知。 -->
          <ElSwitch v-model="autoRefresh" size="small" />
          <span class="do-filter__auto-label">自动刷新</span>
          <ElSelect v-model="autoIntervalSec" size="small" class="do-filter__interval" :disabled="!autoRefresh">
            <ElOption v-for="s in AUTO_INTERVALS" :key="s" :label="`每 ${s} 秒`" :value="s" />
          </ElSelect>
          <ElButton size="small" :loading="loading" @click="reload">
            <ArtSvgIcon icon="ri:refresh-line" />
          </ElButton>
        </div>
      </div>

      <!-- 生效条件摘要 + 数据口径说明：把「当前在看什么」写清楚。 -->
      <div class="do-filter__summary">
        <span class="do-pill">
          <ArtSvgIcon icon="ri:filter-3-line" />
          <span>{{ effectiveSummary }}</span>
        </span>
        <ElTag v-if="meta?.downsampled" size="small" type="warning" effect="light">
          已抽样（桶宽 {{ resolutionText }}）
        </ElTag>
        <ElTag v-if="meta?.truncated" size="small" type="warning" effect="light">
          仅显示前 {{ meta.max_devices }} 台
        </ElTag>
        <ElTooltip v-if="meta" placement="top">
          <template #content>
            <div class="do-tip">
              <div>时间窗 {{ formatDurationText(meta.range_seconds) }}（range={{ meta.range_seconds }}）</div>
              <div>桶宽 {{ resolutionText }}（resolution_seconds={{ meta.resolution_seconds }}）</div>
              <div>数据源 {{ meta.source }}（redis=热层 / db=历史表）</div>
              <div>可用指标列 {{ meta.available_metrics.length }} 列</div>
              <div>设备上限 {{ meta.max_devices }} 台，截断时如实标注</div>
            </div>
          </template>
          <ArtSvgIcon class="do-filter__info" icon="ri:information-line" />
        </ElTooltip>
      </div>
    </div>

    <!-- ============ 加载中 ============ -->
    <div v-if="state === 'loading'" class="do-card do-empty flex-cc flex-col gap-3 py-16">
      <div class="do-empty__icon flex-cc">
        <ArtSvgIcon icon="ri:loader-4-line" class="do-empty__spin" />
      </div>
      <div class="text-sm font-medium text-[var(--el-text-color-regular)]">正在加载设备指标…</div>
      <div class="text-xs text-g-600">首次加载需聚合全部设备的时间序列，请稍候</div>
    </div>

    <!-- ============ 错误 ============ -->
    <div v-else-if="state === 'error'" class="do-card do-state">
      <ElResult :icon="errorInfo?.retryable ? 'warning' : 'error'" :title="errorInfo?.title" :sub-title="errorInfo?.hint">
        <template #extra>
          <ElButton v-if="errorInfo?.retryable" type="primary" @click="reload">重试</ElButton>
          <ElButton @click="resetFilters">重置筛选</ElButton>
          <ElButton v-if="errorInfo?.raw" text type="info" @click="showRawError = !showRawError">
            {{ showRawError ? '收起详情' : '查看详情' }}
          </ElButton>
        </template>
      </ElResult>
      <!--
        技术细节**不能**写在 ElResult 内部：ElResult 只渲染
        icon/title/sub-title/extra 四个具名插槽，**没有 default 插槽**
        （见 element-plus/es/components/result/src/result2.mjs）。
        写在里面会被静默丢弃 —— 按钮能点、标题会变「收起详情」，
        但内容永远不出现。故放在 ElResult 之后的兄弟节点。
      -->
      <div v-if="showRawError && errorInfo?.raw" class="do-state__raw">{{ errorInfo.raw }}</div>
    </div>

    <!-- ============ 空态 ============ -->
    <div v-else-if="state === 'empty'" class="do-card do-state">
      <!--
        注意插槽名：ElEmpty 渲染 description/image/**default** 三个插槽
        （见 element-plus/es/components/empty/src/empty2.mjs），
        额外的操作按钮要塞进 **default**，没有 `#extra` 插槽。
        写成 `#extra` 会被静默丢弃 —— 空态下就再也无法「一键清除筛选」，
        而空态恰恰是用户最需要这个按钮的时刻。
      -->
      <ElEmpty :description="emptyDescription">
        <ElButton v-if="hasActiveFilter" @click="resetFilters">清除筛选条件</ElButton>
      </ElEmpty>
    </div>

    <!-- ============ 正常 ============ -->
    <template v-else>
      <!-- 页面级概览：设备总数 / 在线 / 离线 / 陈旧 / 停用 -->
      <!-- 页面级概览磁贴：对齐服务监控 .kpi-tile（图标方块 + 数值 + 标签 + 说明） -->
      <div class="do-stats kpi-grid grid grid-cols-2 gap-4 md:grid-cols-3 2xl:grid-cols-5">
        <div
          v-for="s in stats"
          :key="s.key"
          class="do-card do-stats__card kpi-tile"
          :title="s.hint"
        >
          <div class="kpi-tile__icon flex-cc" :style="{ '--tile': s.tile, '--tile2': s.tile2 }">
            <ArtSvgIcon :icon="s.icon" />
          </div>
          <div class="min-w-0 flex-1">
            <div class="kpi-tile__value truncate" :class="{ 'is-alert': s.alert }">{{ s.value }}</div>
            <div class="kpi-tile__label">{{ s.label }}</div>
            <div class="kpi-tile__sub truncate">{{ s.hint ?? '&nbsp;' }}</div>
          </div>
        </div>
      </div>

      <!-- 设备快照表：每台设备一行，列出关键水位指标。
           它承担「横向对比」的精确读数职责 —— 图表看趋势，表格看当前值。 -->
      <div class="do-card do-snapshot">
        <div class="do-section__head">
          <span class="do-section__title">设备实时快照</span>
          <span class="do-section__sub">{{ snapshotSummary }}</span>
          <div class="do-section__spacer"></div>
          <ElButton size="small" text @click="clearSelection" v-if="selectedDeviceIds.length">
            清除选择（已选 {{ selectedDeviceIds.length }} 台）
          </ElButton>
        </div>

        <ElTable :data="snapshotRows" size="small" max-height="20rem">
          <ElTableColumn label="设备" min-width="180" fixed>
            <template #default="{ row }">
              <div class="do-dev">
                <ElCheckbox
                  :model-value="selectedDeviceIds.includes(row.id)"
                  @change="toggleDevice(row.id)"
                />
                <ArtSvgIcon :icon="deviceIcon(row.os, row.platform)" class="do-dev__icon" />
                <div class="do-dev__text">
                  <div class="do-dev__name">{{ row.hostname || row.id }}</div>
                  <div class="do-dev__meta">{{ row.id }}</div>
                </div>
              </div>
            </template>
          </ElTableColumn>

          <ElTableColumn label="状态" width="120">
            <template #default="{ row }">
              <ElTag :type="row.online ? 'success' : 'danger'" size="small" effect="light">
                {{ row.online ? '在线' : '离线' }}
              </ElTag>
              <ElTag v-if="row.stale" type="warning" size="small" effect="light">陈旧</ElTag>
              <ElTag v-if="row.status === 0" type="info" size="small" effect="plain">停用</ElTag>
            </template>
          </ElTableColumn>

          <ElTableColumn label="水位时间" width="110">
            <template #default="{ row }">
              <span :title="row.watermarkAt ? formatUnixSeconds(row.watermarkAt) : ''">
                {{ formatRelative(row.watermarkAt) }}
              </span>
            </template>
          </ElTableColumn>

          <!-- 关键水位列：CPU / 内存 / 磁盘 使用率带进度条（一眼看紧张度），
               其余为数值。缺值一律「—」，绝不为 0。 -->
          <ElTableColumn v-for="c in SNAPSHOT_COLUMNS" :key="c.column" :label="c.label" width="150">
            <template #default="{ row }">
              <div v-if="c.bar" class="do-usage">
                <ElProgress
                  :percentage="clampPercent(row.watermark?.[c.column])"
                  :stroke-width="6"
                  :show-text="false"
                  :color="usageColor(row.watermark?.[c.column])"
                />
                <span class="do-usage__text">{{ formatMetric(c.column, row.watermark?.[c.column]) }}</span>
              </div>
              <span v-else>{{ formatMetric(c.column, row.watermark?.[c.column]) }}</span>
            </template>
          </ElTableColumn>

          <ElTableColumn label="提示" min-width="180">
            <template #default="{ row }">
              <span v-if="deviceIssueText(row, meta?.summary.offline_threshold_sec)" class="do-issue">
                {{ deviceIssueText(row, meta?.summary.offline_threshold_sec) }}
              </span>
              <span v-else class="do-ok">正常</span>
            </template>
          </ElTableColumn>

          <ElTableColumn label="操作" width="100" fixed="right">
            <template #default="{ row }">
              <ElButton link type="primary" size="small" @click="gotoDetail(row.id)">详情下钻</ElButton>
            </template>
          </ElTableColumn>
        </ElTable>
      </div>

      <!-- ============ 各指标类别的图表块 ============ -->
      <!-- 这是本页的主体：每张图回答一个独立的问题（见 CHART_BLOCKS 的 purpose）。
           栅格：宽图占满一行，窄图两列并排；窄屏一律单列。 -->
      <div v-if="chartBlocks.length" class="do-charts">
        <div
          v-for="chart in chartBlocks"
          :key="chart.key"
          class="do-charts__item"
          :class="`do-charts__item--${chart.span}`"
        >
          <OverviewChartCard
            :chart="chart"
            :axis="axis"
            :range-seconds="meta?.range_seconds ?? rangeSeconds"
            :resolution-seconds="meta?.resolution_seconds ?? 0"
            :highlighted-device-ids="selectedDeviceIds"
          />
        </div>
      </div>

      <!-- 有设备但画不出任何曲线：显式说明，而不是一屏空白 -->
      <div v-else class="do-card do-state">
        <!-- 同样用 ElEmpty 的 default 插槽（它没有 #extra）。 -->
        <ElEmpty description="所选设备在该时间窗内没有任何指标数据">
          <div class="do-state__hint">
            常见原因：设备刚注册尚未上报、所选时间窗内 agent 未提交指标，
            或筛选条件过滤掉了所有设备。
          </div>
          <ElButton type="primary" @click="reload">重新加载</ElButton>
        </ElEmpty>
      </div>
    </template>
    </div>
  </div>
</template>

<script setup lang="ts">
  import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
  import { useRouter } from 'vue-router'

  import { fetchDeviceOverview, type DeviceOverviewQuery } from '../api'
  import OverviewChartCard from '../components/overview-chart-card.vue'
  import { classifyDeviceError, type DeviceErrorInfo } from '../utils/error'
  import {
    clampPercent,
    deviceIcon,
    formatRelative,
    formatUnixSeconds,
    usageTone
  } from '../utils/display'
  import { DEFAULT_RANGE_SECONDS, formatDurationText, rangeOptions } from '../utils/metrics'
  import {
    abnormalDeviceCount,
    buildOverviewCharts,
    buildOverviewStats,
    COL,
    deviceIssueText,
    formatMetric,
    overviewState
  } from '../utils/overview'

  defineOptions({ name: 'DeviceOverview' })

  const router = useRouter()

  // ── 响应与状态 ──────────────────────────────────────────
  const meta = ref<Api.Device.DeviceOverviewResp | null>(null)
  const loading = ref(false)
  // hasError 与 errorInfo 分开：DeviceErrorKind 描述的是**错误类型**，
  // 没有「无错误」这一档；把「无错误」塞进 kind 会让类型失去意义。
  const hasError = ref(false)
  const errorInfo = ref<DeviceErrorInfo | null>(null)
  const showRawError = ref(false)

  // ── 全局过滤（一次请求内所有设备共用）────────────────────
  const filters = reactive<{ hostname: string; status?: number; online?: boolean }>({
    hostname: '',
    status: undefined,
    online: undefined
  })
  const rangeSeconds = ref(DEFAULT_RANGE_SECONDS)

  /** 时间窗档位复用详情页的 rangeOptions（同一套后端选档口径）。 */
  const rangeChoices = computed(() => rangeOptions(false).map((r) => ({ label: r.label, seconds: r.seconds })))

  // ── 自动刷新 ────────────────────────────────────────────
  const AUTO_INTERVALS = [10, 30, 60, 300]
  const autoRefresh = ref(false)
  const autoIntervalSec = ref(30)
  let timer: ReturnType<typeof setInterval> | null = null

  // ── 设备勾选（图表对比联动）──────────────────────────────
  // 空数组 = 不筛选（全部同等显示）。这与「把所有设备都勾上」语义不同：
  // 后者会在设备集合变化后失效（新设备不在勾选里而被淡出）。
  const selectedDeviceIds = ref<string[]>([])

  // ── 数据派生 ────────────────────────────────────────────
  const axis = computed(() => meta.value?.axis ?? [])

  const devices = computed<Api.Device.DeviceOverviewItem[]>(() => meta.value?.devices ?? [])

  const snapshotRows = computed(() =>
    devices.value.map((d) => ({
      id: d.id,
      hostname: d.hostname,
      platform: d.platform,
      os: d.os,
      online: d.online,
      stale: d.stale,
      status: d.status,
      watermarkAt: d.watermarkAt,
      watermark: d.watermark,
      error: d.error
    }))
  )

  const stats = computed(() => {
    const s = meta.value?.summary
    if (!s) return []
    return buildOverviewStats(s)
  })

  const abnormalCount = computed(() => abnormalDeviceCount(devices.value))

  /**
   * 页头副标题：与「服务监控」同一句式（`最近更新 X · 历史 N 桶`）。
   *
   * 为什么要有它：页头原本只有一个标题，用户无法判断"这屏数据是几点的"。
   * 服务监控把「最近更新 / 历史桶数 / 自动刷新」都写在标题下方一行，
   * 这里对齐同一信息密度 —— 但**不编造**：后端没给更新时间就不写，
   * 而不是显示"刚刚"这种无法验证的说法。
   */
  const overviewSubtitle = computed(() => {
    const m = meta.value
    if (!m) return loading.value ? '正在加载…' : '暂无数据'
    const parts: string[] = []
    parts.push(`共 ${m.summary.total} 台设备 · 在线 ${m.summary.online} / 离线 ${m.summary.offline}`)
    parts.push(`时间窗 ${formatDurationText(m.range_seconds)} · ${axis.value.length} 桶`)
    if (autoRefresh.value) parts.push(`每 ${autoIntervalSec} 秒自动刷新`)
    return parts.join(' · ')
  })

  const chartBlocks = computed(() => {
    const m = meta.value
    if (!m) return []
    return buildOverviewCharts(m.axis ?? [], m.available_metrics ?? [], m.devices ?? [])
  })

  /**
   * 页面状态。
   *
   * 三态优先级由 overviewState 定义（loading > error > empty > ready）。
   * 「设备台数」只有在**已成功拿到响应**时才可信：请求失败时 devices 是空数组，
   * 若直接把它当「0 台设备」就会渲染空态 —— 那正是「失败伪装成没有设备」。
   * 故这里显式传 -1 给未就绪的情形，让 empty 分支不可达。
   */
  const deviceCountForState = computed(() => (meta.value === null ? -1 : devices.value.length))

  const state = computed(() =>
    overviewState(hasError.value, loading.value && meta.value === null, Math.max(deviceCountForState.value, 0))
  )

  const hasActiveFilter = computed(
    () => !!filters.hostname || filters.status !== undefined || filters.online !== undefined
  )

  const resolutionText = computed(() => {
    const r = meta.value?.resolution_seconds ?? 0
    if (r <= 0) return '未知'
    if (r < 60) return `${r} 秒`
    if (r < 3600) return `${r / 60} 分钟`
    return `${r / 3600} 小时`
  })

  /** 生效条件摘要：把「现在看的是哪一批设备」用一句话讲清。 */
  const effectiveSummary = computed(() => {
    const m = meta.value
    if (!m) return '加载中…'
    const parts: string[] = []
    parts.push(`共 ${m.summary.total} 台设备`)
    parts.push(`在线 ${m.summary.online} / 离线 ${m.summary.offline}`)
    if (m.summary.stale > 0) parts.push(`数据陈旧 ${m.summary.stale}`)
    if (abnormalCount.value > 0) parts.push(`异常 ${abnormalCount.value}`)
    const conds: string[] = []
    if (filters.hostname) conds.push(`主机名含「${filters.hostname}」`)
    if (filters.status !== undefined) conds.push(filters.status === 1 ? '仅启用' : '仅停用')
    if (filters.online !== undefined) conds.push(filters.online ? '仅在线' : '仅离线')
    if (conds.length) parts.push(`筛选：${conds.join('、')}`)
    return parts.join(' · ')
  })

  const snapshotSummary = computed(() => {
    const total = devices.value.length
    const withData = devices.value.filter((d) => d.watermark && Object.keys(d.watermark).length > 0).length
    if (total === 0) return ''
    const bits = [`${total} 台`, `其中 ${withData} 台有指标快照`]
    if (selectedDeviceIds.value.length > 0) bits.push(`已选 ${selectedDeviceIds.value.length} 台（图表已同步高亮）`)
    return bits.join(' · ')
  })

  const emptyDescription = computed(() => {
    if (hasActiveFilter.value) return '当前筛选条件下没有匹配的设备'
    const m = meta.value
    if (m && m.summary.total === 0) return '系统中还没有注册任何设备'
    return '暂无数据'
  })

  // 快照表固定列：CPU / 内存 / 磁盘使用率带进度条（0–100 有天然边界），
  // 其余为数值列。列名与后端水位字段一一对应（见 agentmetrics.LatestToWatermark）。
  const SNAPSHOT_COLUMNS = [
    { column: COL.cpu, label: 'CPU', bar: true },
    { column: COL.memPercent, label: '内存', bar: true },
    { column: COL.diskUsedPercent, label: '磁盘使用率', bar: true },
    { column: COL.load1, label: '负载 1m', bar: false },
    { column: COL.memUsedMB, label: '内存已用', bar: false },
    { column: COL.diskUsedGB, label: '磁盘已用', bar: false },
    { column: COL.nicRxBytesSec, label: '接收速率', bar: false },
    { column: COL.nicTxBytesSec, label: '发送速率', bar: false },
    { column: COL.maxTemperatureC, label: '最高温度', bar: false }
  ]

  // 水位着色直接复用详情页的 usageTone（返回 CSS 变量字符串）——
  // 避免两页对「多少算紧张」给出不同颜色。
  const usageColor = (v?: number | null) => usageTone(v)

  // ── 加载 ────────────────────────────────────────────────
  const buildQuery = (): DeviceOverviewQuery => {
    const q: DeviceOverviewQuery = { range: rangeSeconds.value }
    // 必须显式要**全部可用列**（`*`）。后端不带 metrics 时只回默认列集
    // （约 7 列：cpu / load1 / mem / disk / 网卡 / 温度），而 CHART_BLOCKS
    // 里的「系统负载（load5/load15）」「TCP 连接状态」「进程与运行时长」
    // 「磁盘 IO」「交换分区」「CPU iowait」等图块依赖的列**不在默认集内** ——
    // 不传 `*` 时这些图块会永远因「拿不到列」被静默隐藏，且不报任何错
    // （最隐蔽的一类缺陷：功能看似设计好了，实则从未生效）。
    //
    // 传 `*` 的代价可接受：实测 2 台设备 × 24 列 × 24h 窗口约 75 KB，
    // 远小于把同样数据拆成 N 次详情页请求（这正是本页要消除的开销）。
    // `*` 由后端解析为该档位的全部可用列（见 resolveTrendColumns），
    // 只有非法列名才 400，`*` 本身永远合法。
    q.metrics = '*'
    if (filters.hostname.trim()) q.hostname = filters.hostname.trim()
    if (filters.status !== undefined) q.status = filters.status
    if (filters.online !== undefined) q.online = filters.online
    // 勾选设备时下发白名单：让**后端**只取这几台（而不是前端本地过滤）——
    // 后者会把未勾选设备的趋势数据也白拉一遍，而趋势正是最贵的那部分。
    if (selectedDeviceIds.value.length > 0) q.ids = selectedDeviceIds.value.join(',')
    return q
  }

  /**
   * 加载数据。
   *
   * @param silent 自动刷新时为 true：不显示 loading 骨架。
   *   强制要求：自动刷新若每次都把页面切成加载态，用户会在图表反复重建中
   *   无法读数（每次刷新都闪一下），自动刷新反而变得不可用。
   */
  const load = async (silent = false) => {
    if (!silent) loading.value = true
    try {
      const resp = await fetchDeviceOverview(buildQuery())
      meta.value = resp as unknown as Api.Device.DeviceOverviewResp
      hasError.value = false
      errorInfo.value = null
      showRawError.value = false
      // 设备集合变化后清理失效的勾选（否则「已选 2 台」里可能有已不存在的设备，
      // 下发 ids 会被后端当成有效 ID 而在结果里缺席，造成难以解释的空图）。
      const alive = new Set((meta.value.devices ?? []).map((d) => d.id))
      selectedDeviceIds.value = selectedDeviceIds.value.filter((id) => alive.has(id))
    } catch (e) {
      errorInfo.value = classifyDeviceError(e)
      hasError.value = true
      // 失败时**保留**上一次的 meta 吗？不保留 —— 保留会让页面同时显示
      // 「错误横幅」与「旧图表」，用户无法判断看到的是新数据还是旧数据。
      // 宁可显示明确的错误页，也不给出可能过期的数字。
      meta.value = null
    } finally {
      loading.value = false
    }
  }

  const reload = () => {
    // 手动查询会重置自动刷新计时器：否则用户刚点完「查询」，
    // 1 秒后自动刷新又打一次，白费一次昂贵的全量聚合。
    restartTimer()
    return load()
  }

  const resetFilters = () => {
    filters.hostname = ''
    filters.status = undefined
    filters.online = undefined
    rangeSeconds.value = DEFAULT_RANGE_SECONDS
    selectedDeviceIds.value = []
    return reload()
  }

  const toggleDevice = (id: string) => {
    const i = selectedDeviceIds.value.indexOf(id)
    if (i >= 0) {
      selectedDeviceIds.value = selectedDeviceIds.value.filter((x) => x !== id)
    } else {
      selectedDeviceIds.value = [...selectedDeviceIds.value, id]
    }
    // 勾选变化必须重新取数：ids 会影响**后端**取哪些设备（见 buildQuery）。
    // 取消勾选同样要请求 —— 否则那台设备的数据不会回到图上。
    return load()
  }

  const clearSelection = () => {
    selectedDeviceIds.value = []
    return load()
  }

  const gotoDetail = (id: string) => {
    router.push({ name: 'DeviceDetail', params: { id } })
  }

  // ── 自动刷新计时器 ──────────────────────────────────────
  const stopTimer = () => {
    if (timer !== null) {
      clearInterval(timer)
      timer = null
    }
  }

  const restartTimer = () => {
    stopTimer()
    if (autoRefresh.value) {
      timer = setInterval(() => load(true), autoIntervalSec.value * 1000)
    }
  }

  watch([autoRefresh, autoIntervalSec], restartTimer)

  onMounted(() => {
    load()
    restartTimer()
  })

  // 组件卸载必须停表：否则切走后仍在轮询，是典型的资源泄漏
  // （总览页的请求又是最贵的全量聚合，泄漏代价尤其高）。
  onBeforeUnmount(stopTimer)

  // 页面重新可见时补一次刷新：后台标签页里 setInterval 会被浏览器限流，
  // 用户切回来看到的可能是几分钟前的数据。
  const onVisible = () => {
    if (document.visibilityState === 'visible' && autoRefresh.value) load(true)
  }
  onMounted(() => document.addEventListener('visibilitychange', onVisible))
  onBeforeUnmount(() => document.removeEventListener('visibilitychange', onVisible))
</script>

<style lang="scss" scoped>
  /* 设备模块共享设计令牌（数值与服务监控 monitor-tokens 一致，见该文件注释） */
  @use './device-tokens' as t;

  @include t.rise-keyframes;
  @include t.pulse-keyframes;

  /* ---------- 骨架：对齐服务监控 .server-page__inner 的分栏与留白 ---------- */
  .device-overview__inner {
    display: flex;
    flex-direction: column;
    gap: 16px;
  }

  /* ---------- 基础卡片 ---------- */
  .do-card {
    @include t.card;
    @include t.rise;
  }

  .dark .do-card {
    @include t.card-dark;
  }

  /* ---------- 页头（对齐 .sv-hero） ---------- */
  .do-hero__icon {
    @include t.hero-icon;
  }

  .live-dot {
    @include t.live-dot;
  }

  .live-dot.is-loading {
    background: #f59e0b;
    animation-duration: 0.9s;
  }

  /* ---------- 过滤条 ---------- */
  .do-filter {
    &__row {
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      justify-content: space-between;
      gap: 8px;

      &--ranges {
        margin-top: 12px;
      }
    }

    &__fields {
      display: flex;
      flex-wrap: wrap;
      gap: 8px;
    }

    &__host {
      width: 220px;
    }

    &__status {
      width: 140px;
    }

    &__actions {
      display: flex;
      gap: 8px;
    }

    &__auto {
      display: flex;
      align-items: center;
      gap: 6px;
    }

    &__auto-label {
      font-size: 13px;
      color: var(--el-text-color-regular);
    }

    &__interval {
      width: 110px;
    }

    &__summary {
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      gap: 8px;
      margin-top: 12px;
      padding-top: 12px;
      border-top: 1px solid var(--default-border);
      font-size: 12px;
      color: var(--el-text-color-secondary);
    }

    &__info {
      cursor: help;
      color: var(--el-text-color-placeholder);
    }
  }

  /* 生效条件摘要用胶囊标签（对齐 .sv-summary-pill） */
  .do-pill {
    @include t.pill;
    font-variant-numeric: tabular-nums;
  }

  .do-tip {
    line-height: 1.7;
  }

  /* ---------- 三态（加载 / 错误 / 空） ---------- */
  .do-state {
    &__raw {
      margin-top: 12px;
      max-width: 640px;
      padding: 8px;
      border-radius: 10px;
      background: var(--el-fill-color-light);
      color: var(--el-text-color-regular);
      font-family: var(--el-font-family-monospace, monospace);
      font-size: 12px;
      text-align: left;
      white-space: pre-wrap;
      word-break: break-all;
    }

    &__hint {
      margin-bottom: 12px;
      color: var(--el-text-color-secondary);
      font-size: 12px;
      line-height: 1.7;
    }
  }

  /* 空/加载态图标方块（对齐 .sv-empty__icon） */
  .do-empty {
    &__icon {
      width: 64px;
      height: 64px;
      border-radius: 20px;
      font-size: 30px;
      color: var(--el-color-primary);
      background: rgba(59, 130, 246, 0.12);
    }

    &__spin {
      animation: do-spin 1.1s linear infinite;
    }
  }

  @keyframes do-spin {
    to {
      transform: rotate(360deg);
    }
  }

  /* ---------- KPI 磁贴（对齐 .kpi-tile） ---------- */
  .kpi-tile {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    transition:
      transform 0.2s ease,
      box-shadow 0.2s ease,
      border-color 0.2s ease;
  }

  /* 入场错峰：序号越大越晚，与服务监控 KPI 栅格同一节奏 */
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

    /* 告警态：数值转红。服务监控的 KPI 不带告警色（它没有"异常计数"这类指标），
       故这一条是设备模块的**领域补充**，但只借用既有语义色，不新增调色板。 */
    &.is-alert {
      color: var(--el-color-danger);
    }
  }

  .kpi-tile__label {
    @include t.kpi-label;
  }

  .kpi-tile__sub {
    @include t.kpi-sub;
  }

  /* ---------- 区块标题 ---------- */
  .do-section {
    &__head {
      display: flex;
      align-items: center;
      gap: 8px;
      margin-bottom: 12px;
    }

    &__title {
      font-size: 14px;
      font-weight: 600;
      color: var(--el-text-color-primary);
    }

    &__sub {
      color: var(--el-text-color-secondary);
      font-size: 11px;
    }

    &__spacer {
      flex: 1;
    }
  }

  /* ---------- 设备单元格 ---------- */
  .do-dev {
    display: flex;
    align-items: center;
    gap: 6px;

    &__icon {
      font-size: 16px;
      color: var(--el-text-color-regular);
    }

    &__text {
      min-width: 0;
    }

    &__name {
      font-weight: 500;
    }

    &__meta {
      color: var(--el-text-color-placeholder);
      font-size: 11px;
      font-variant-numeric: tabular-nums;
    }
  }

  .do-usage {
    display: flex;
    align-items: center;
    gap: 8px;

    :deep(.el-progress) {
      flex: 1;
      min-width: 40px;
    }

    &__text {
      min-width: 56px;
      text-align: right;
      font-variant-numeric: tabular-nums;
    }
  }

  .do-issue {
    color: var(--el-color-warning);
    font-size: 12px;
  }

  .do-ok {
    color: var(--el-text-color-placeholder);
    font-size: 12px;
  }

  /* ---------- 图表栅格 ---------- */
  .do-charts {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 16px;

    &__item {
      min-width: 0;

      &--full {
        grid-column: span 2;
      }
    }
  }

  /* 窄屏：一律单列。图表在窄屏并排会窄到无法读数。 */
  @media (max-width: 1200px) {
    .do-charts {
      grid-template-columns: minmax(0, 1fr);

      &__item--full {
        grid-column: span 1;
      }
    }
  }

  @media (max-width: 768px) {
    .do-filter__host,
    .do-filter__status {
      width: 100%;
    }
  }

  /* 动效降级：与服务监控同一收敛点 */
  @include t.reduced-motion('.do-card', '.kpi-tile', '.live-dot', '.do-empty__spin');
</style>
