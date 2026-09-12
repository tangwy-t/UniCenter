<template>
  <div class="sql-page art-full-height overflow-y-auto">
    <div class="sql-page__inner p-4 pb-8 md:p-5">
      <!-- ============ 页头：标题 + 实时状态 + 时间范围 + 自动刷新 ============ -->
      <div class="sql-hero mb-4 flex flex-wrap items-center gap-3">
        <div class="sql-hero__icon flex-cc">
          <ArtSvgIcon icon="ri:terminal-box-line" />
        </div>
        <div class="sql-hero__text min-w-0">
          <h2 class="text-lg font-semibold text-[var(--el-text-color-primary)]">SQL 监控</h2>
          <p class="mt-0.5 flex flex-wrap items-center gap-1.5 text-xs text-g-600">
            <span
              class="live-dot inline-block h-1.5 w-1.5 rounded-full bg-success"
              :class="{ 'is-loading': loading }"
            />
            {{ loading ? '正在采集 SQL 指标…' : `最近更新 ${updatedAtText()}` }}
            <template v-if="autoRefresh">· 5s 自动刷新</template>
          </p>
        </div>

        <div class="ml-auto flex flex-wrap items-center gap-1.5">
          <!-- 时间范围分段 -->
          <div
            class="sql-range flex items-center gap-0.5"
            role="radiogroup"
            aria-label="统计时间范围"
          >
            <button
              v-for="r in RANGES"
              :key="r.key"
              type="button"
              :class="['sql-range__item', { 'is-active': rangeKey === r.key }]"
              :aria-pressed="rangeKey === r.key"
              @click="switchRange(r.key)"
            >
              {{ r.label }}
            </button>
          </div>

          <div class="relative">
            <ArtButtonTable
              :icon="'ri:timer-2-line'"
              :iconClass="autoRefresh ? 'bg-theme text-white shadow-sm' : 'bg-theme/12 text-theme'"
              :title="autoRefresh ? '关闭自动刷新 (5s)' : '开启自动刷新 (5s)'"
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
            @click="loadAll(false)"
          />
        </div>
      </div>

      <!-- ============ 空状态 ============ -->
      <div v-if="!stats && !loading" class="sql-card sql-empty flex-cc flex-col gap-3 py-16">
        <div class="sql-empty__icon flex-cc">
          <ArtSvgIcon icon="ri:database-2-line" />
        </div>
        <div class="text-sm font-medium text-[var(--el-text-color-regular)]">暂无 SQL 监控数据</div>
        <div class="text-xs text-g-600">请确认后端已通过 GORM 记录 SQL 统计，或点击刷新重试</div>
        <ArtButtonTable
          icon="ri:refresh-line"
          iconClass="bg-theme/12 text-theme"
          title="刷新"
          @click="loadAll(false)"
        />
      </div>

      <!-- ============ KPI 数据磁贴（6 项，带迷你趋势线） ============ -->
      <div
        v-loading="loading && !stats"
        class="kpi-grid grid grid-cols-2 gap-4 md:grid-cols-3 2xl:grid-cols-6"
      >
        <div v-for="k in kpis" :key="k.key" class="sql-card kpi-tile" :title="k.title">
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

      <!-- ============ 主网格：吞吐(左) + 操作分布(右) ============ -->
      <div v-if="stats" class="mt-4 grid grid-cols-1 gap-4 lg:grid-cols-5">
        <!-- 吞吐趋势 + 延迟热力条 -->
        <div class="flex min-w-0 flex-col lg:col-span-3">
          <div class="sql-card !p-4">
            <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
              <div class="flex items-center gap-2.5">
                <span class="sql-chip flex-cc" style="color: #3b82f6; background: #3b82f614">
                  <ArtSvgIcon icon="ri:line-chart-line" />
                </span>
                <div>
                  <div class="text-sm font-semibold text-[var(--el-text-color-primary)]"
                    >吞吐趋势 · QPS</div
                  >
                  <div class="sql-card__sub"
                    >最近 {{ rangeLabel }} · 每 {{ history?.step_seconds ?? '-' }}s 聚合一个桶</div
                  >
                </div>
              </div>
              <span class="sql-summary-pill tabular-nums">
                <ArtSvgIcon v-if="loading" icon="ri:loader-4-line" class="animate-spin" />
                此刻 {{ fmtNum(history?.recent_qps ?? null, 1) }} QPS
              </span>
            </div>

            <ArtLineChart
              height="230px"
              :data="qpsData"
              :x-axis-data="timeLabels"
              :colors="['#3b82f6']"
              :show-area-color="true"
              :smooth="false"
              :loading="loading && !stats"
              :live="true"
              :show-data-zoom="true"
            />
          </div>

          <!-- 延迟热力条：每个时间桶一格，按 P95 相对慢阈值着色 -->
          <div class="sql-card sql-heat mt-4 !p-4">
            <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
              <div class="flex items-center gap-2.5">
                <span class="sql-chip flex-cc" style="color: #f59e0b; background: #f59e0b14">
                  <ArtSvgIcon icon="ri:water-flash-line" />
                </span>
                <div>
                  <div class="text-sm font-semibold text-[var(--el-text-color-primary)]"
                    >延迟热力</div
                  >
                  <div class="sql-card__sub"
                    >每个色块 = 一个时间桶的 P95 耗时 · 颜色越暖越接近慢查询阈值</div
                  >
                </div>
              </div>
              <div class="sql-heat__legend">
                <span class="text-g-600">快</span>
                <i
                  v-for="c in ['#10b981', '#3b82f6', '#f59e0b', '#dc2626']"
                  :key="c"
                  class="sql-heat__swatch"
                  :style="{ background: c }"
                />
                <span class="text-g-600">慢</span>
                <b class="sql-heat__thr tabular-nums" :title="`慢查询阈值 ${thresholdMs}ms`"
                  >≥ {{ thresholdMs }}ms</b
                >
              </div>
            </div>
            <div
              v-if="heatCells.length"
              class="sql-heat__strip"
              role="img"
              aria-label="延迟热力时间条"
            >
              <span
                v-for="(cell, i) in heatCells"
                :key="i"
                class="sql-heat__cell"
                :style="{ background: cell.color }"
                :title="cell.title"
              />
            </div>
            <div v-else class="sql-panel-empty">等待采样…生成第一段热力条</div>
          </div>
        </div>

        <!-- 操作类型分布 -->
        <div class="flex min-w-0 flex-col lg:col-span-2">
          <div class="sql-card flex-1 !p-4">
            <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
              <div class="flex items-center gap-2.5">
                <span class="sql-chip flex-cc" style="color: #7c3aed; background: #7c3aed14">
                  <ArtSvgIcon icon="ri:apps-2-line" />
                </span>
                <div>
                  <div class="text-sm font-semibold text-[var(--el-text-color-primary)]"
                    >操作类型分布</div
                  >
                  <div class="sql-card__sub">累计执行次数占比（悬停查看明细）</div>
                </div>
              </div>
            </div>

            <ArtRingChart
              height="240px"
              :data="ringData"
              :colors="ringColors"
              :center-text="ringTotal ? fmtCount(ringTotal) : '—'"
              :show-legend="true"
              legend-position="right"
              :loading="loading && !stats"
              class="ml-1.5"
            />

            <div v-if="!ringData.length && stats && !loading" class="sql-panel-empty"
              >暂无执行记录，占比待采样</div
            >
          </div>
        </div>
      </div>

      <!-- ============ 次网格：延迟趋势(左) + 耗时画像(右) ============ -->
      <div v-if="stats" class="mt-4 grid grid-cols-1 gap-4 lg:grid-cols-5">
        <div class="flex min-w-0 flex-col lg:col-span-3">
          <div class="sql-card flex-1 !p-4">
            <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
              <div class="flex items-center gap-2.5">
                <span class="sql-chip flex-cc" style="color: #06b6d4; background: #06b6d414">
                  <ArtSvgIcon icon="ri:time-line" />
                </span>
                <div>
                  <div class="text-sm font-semibold text-[var(--el-text-color-primary)]"
                    >延迟趋势 · P50 / P95 / P99</div
                  >
                  <div class="sql-card__sub">最近 {{ rangeLabel }} · 耗时越高越靠上</div>
                </div>
              </div>
              <span class="sql-summary-pill tabular-nums">
                <ArtSvgIcon icon="ri:timer-line" class="mr-0.5" />
                阈值 {{ thresholdMs }}ms
              </span>
            </div>

            <ArtLineChart
              height="240px"
              :data="latencySeries"
              :x-axis-data="timeLabels"
              :colors="['#3b82f6', '#f59e0b', '#dc2626']"
              :show-legend="true"
              legend-position="bottom"
              :smooth="false"
              :loading="loading && !stats"
              :live="true"
              :show-data-zoom="true"
            />
          </div>
        </div>

        <!-- 耗时画像：分位数 + 阈值对比 -->
        <div class="flex min-w-0 flex-col lg:col-span-2">
          <div class="sql-card flex-1 !p-4">
            <div class="mb-3 flex items-center justify-between gap-2">
              <div class="flex items-center gap-2.5">
                <span class="sql-chip flex-cc" style="color: #10b981; background: #10b98114">
                  <ArtSvgIcon icon="ri:numbers-line" />
                </span>
                <div>
                  <div class="text-sm font-semibold text-[var(--el-text-color-primary)]"
                    >耗时画像</div
                  >
                  <div class="sql-card__sub">全局累计统计（含窗口内采样）</div>
                </div>
              </div>
              <span class="sql-summary-pill tabular-nums"
                >{{ fmtCount(stats.global.count) }} 次</span
              >
            </div>

            <div class="sql-cells">
              <div class="sql-cell" title="最小耗时">
                <div class="sql-cell__label">最小 Min</div>
                <div class="sql-cell__value tabular-nums"
                  >{{ fmtNum(stats.global.min_ms, 2) }}<i class="sql-cell__unit">ms</i></div
                >
              </div>
              <div class="sql-cell" title="中位数耗时（约 50% 的查询快于此值）">
                <div class="sql-cell__label">中位 P50</div>
                <div class="sql-cell__value tabular-nums"
                  >{{ fmtNum(stats.global.p50_ms, 2) }}<i class="sql-cell__unit">ms</i></div
                >
              </div>
              <div class="sql-cell" title="平均耗时">
                <div class="sql-cell__label">平均 Avg</div>
                <div class="sql-cell__value tabular-nums"
                  >{{ fmtNum(stats.global.avg_ms, 2) }}<i class="sql-cell__unit">ms</i></div
                >
              </div>
              <div class="sql-cell" title="95% 的查询快于此值">
                <div class="sql-cell__label">P95</div>
                <div class="sql-cell__value tabular-nums"
                  >{{ fmtNum(stats.global.p95_ms, 2) }}<i class="sql-cell__unit">ms</i></div
                >
              </div>
              <div class="sql-cell" title="99% 的查询快于此值">
                <div class="sql-cell__label">P99</div>
                <div class="sql-cell__value tabular-nums"
                  >{{ fmtNum(stats.global.p99_ms, 2) }}<i class="sql-cell__unit">ms</i></div
                >
              </div>
              <div class="sql-cell" title="历史最大耗时">
                <div class="sql-cell__label">最大 Max</div>
                <div class="sql-cell__value tabular-nums"
                  >{{ fmtNum(stats.global.max_ms, 2) }}<i class="sql-cell__unit">ms</i></div
                >
              </div>
            </div>

            <!-- 阈值水位：P95 相对慢查询阈值 -->
            <div class="sql-gauge-strip">
              <div class="sql-gauge-strip__head">
                <span class="text-xs text-g-600">P95 相对慢查询阈值的余量</span>
                <span class="sql-gauge-strip__text tabular-nums">
                  {{ fmtNum(stats.global.p95_ms, 2) }}ms / {{ thresholdMs }}ms
                </span>
              </div>
              <div
                class="sql-gauge-strip__bar"
                role="img"
                :aria-label="`P95 为慢查询阈值的 ${p95RatioText}`"
              >
                <span
                  class="sql-gauge-strip__fill"
                  :style="{
                    width: `${clamp01((stats.global.p95_ms / thresholdMs) * 100)}%`,
                    background: heatTone(stats.global.p95_ms, thresholdMs)
                  }"
                />
                <span class="sql-gauge-strip__mark" :style="{ left: '100%' }" title="慢查询阈值" />
              </div>
            </div>

            <div class="sql-cells sql-cells--health">
              <div class="sql-cell" title="超过慢查询阈值的 SQL 累计次数">
                <div class="sql-cell__label">慢查询</div>
                <div class="sql-cell__value tabular-nums" style="color: #f59e0b">{{
                  fmtCount(stats.global.slow_count)
                }}</div>
              </div>
              <div class="sql-cell" title="执行失败的 SQL 累计次数">
                <div class="sql-cell__label">错误</div>
                <div
                  class="sql-cell__value tabular-nums"
                  :style="{ color: stats.global.error_count > 0 ? '#dc2626' : undefined }"
                >
                  {{ fmtCount(stats.global.error_count) }}
                </div>
              </div>
              <div class="sql-cell" title="错误占比">
                <div class="sql-cell__label">错误率</div>
                <div class="sql-cell__value tabular-nums">{{ errorRateText }}</div>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- ============ 底网格：热点表(左) + 慢查询实录(右) ============ -->
      <div v-if="stats" class="mt-4 grid grid-cols-1 gap-4 lg:grid-cols-5">
        <!-- 热点表 Top 10 -->
        <div class="flex min-w-0 flex-col lg:col-span-2">
          <div class="sql-card flex-1 !p-4">
            <div class="mb-3 flex items-center justify-between gap-2">
              <div class="flex items-center gap-2.5">
                <span class="sql-chip flex-cc" style="color: #ec4899; background: #ec489914">
                  <ArtSvgIcon icon="ri:bar-chart-2-line" />
                </span>
                <div>
                  <div class="text-sm font-semibold text-[var(--el-text-color-primary)]"
                    >热点表 Top 10</div
                  >
                  <div class="sql-card__sub">按累计执行次数排名 · 长度 = 执行量占比</div>
                </div>
              </div>
              <span class="sql-summary-pill tabular-nums">{{ hotTables.length }} 张表</span>
            </div>

            <div v-if="hotTables.length" class="sql-hot">
              <div
                v-for="(row, i) in hotTables"
                :key="row.name"
                class="sql-hot__row"
                :title="`${row.name} · ${fmtCount(row.count)} 次 · 平均 ${fmtNum(row.avg, 2)}ms`"
              >
                <span class="sql-hot__rank" :class="`is-${i + 1}`">{{ i + 1 }}</span>
                <span
                  class="sql-hot__name"
                  :class="{ 'is-sys': isSysTable(row.name) }"
                  :title="row.name"
                  >{{ row.name }}</span
                >
                <span class="sql-hot__bar">
                  <span
                    class="sql-hot__fill"
                    :style="{
                      width: `${hotMax ? (row.count / hotMax) * 100 : 0}%`,
                      background: heatTone(row.avg, thresholdMs)
                    }"
                  />
                </span>
                <span class="sql-hot__count tabular-nums">{{ fmtCount(row.count) }}</span>
                <span class="sql-hot__avg tabular-nums">{{ fmtNum(row.avg, 2) }}</span>
              </div>
            </div>
            <div v-else class="sql-panel-empty">暂无按表统计，等待第一条 SQL</div>
          </div>
        </div>

        <!-- 慢查询实录 -->
        <div class="flex min-w-0 flex-col lg:col-span-3">
          <div class="sql-card sql-slowcard flex flex-1 flex-col !p-4">
            <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
              <div class="flex items-center gap-2.5">
                <span class="sql-chip flex-cc" style="color: #f59e0b; background: #f59e0b14">
                  <ArtSvgIcon icon="ri:error-warning-line" />
                </span>
                <div>
                  <div class="text-sm font-semibold text-[var(--el-text-color-primary)]"
                    >慢查询实录</div
                  >
                  <div class="sql-card__sub"
                    >按耗时降序 · 环形缓冲最近 100 条 · 点击行查看完整 SQL</div
                  >
                </div>
              </div>
              <div class="flex flex-wrap items-center gap-1">
                <button
                  v-for="op in ['ALL', ...OPS]"
                  :key="op"
                  type="button"
                  :class="[
                    'sql-filterchip',
                    { 'is-active': opFilter === (op === 'ALL' ? null : op) }
                  ]"
                  @click="opFilter = op === 'ALL' ? null : op"
                >
                  {{ op === 'ALL' ? '全部' : op }}
                </button>
                <button
                  type="button"
                  class="sql-filterchip"
                  :class="{ 'is-active is-slow': slowOnly }"
                  :title="`仅显示超过阈值 (${thresholdMs}ms) 的查询`"
                  @click="slowOnly = !slowOnly"
                >
                  慢查询≥{{ thresholdMs }}ms
                </button>
              </div>
            </div>

            <div
              v-if="visibleQueries.length"
              class="sql-slowscroll min-h-0 flex-1 overflow-y-auto pr-1"
            >
              <button
                v-for="(q, i) in visibleQueries"
                :key="`${q.timestamp}-${i}`"
                type="button"
                class="sql-slow"
                @click="openDrawer(q)"
              >
                <span class="sql-slow__time tabular-nums">{{ q.timestamp.slice(11, 19) }}</span>
                <span class="sql-slow__op" :style="opBadgeStyle(q.operation)">{{
                  q.operation
                }}</span>
                <span
                  class="sql-slow__table"
                  :class="{ 'is-sys': isSysTable(q.table) }"
                  :title="
                    q.table
                      ? `涉及表：${q.table}${isSysTable(q.table) ? '（MySQL 系统库）' : ''}`
                      : '无表名(事务/函数等)'
                  "
                >
                  {{ q.table || '—' }}
                </span>
                <span
                  class="sql-slow__dur tabular-nums"
                  :style="{ color: durTone(q.duration_ms, thresholdMs) }"
                >
                  {{ fmtNum(q.duration_ms, q.duration_ms < 10 ? 2 : 1) }}<i>ms</i>
                </span>
                <span class="sql-slow__bar">
                  <span
                    class="sql-slow__bar-fill"
                    :style="{
                      width: `${slowMaxMs ? Math.max(2, (q.duration_ms / slowMaxMs) * 100) : 0}%`,
                      background: durTone(q.duration_ms, thresholdMs)
                    }"
                  />
                </span>
                <span class="sql-slow__sql">
                  <span v-html="highlightSQL(q.sql)" />
                </span>
                <span v-if="q.is_slow" class="sql-slow__flag" title="超过慢查询阈值">慢</span>
                <span class="sql-slow__arrow">
                  <ArtSvgIcon icon="ri:arrow-right-s-line" />
                </span>
              </button>
            </div>
            <div v-else class="sql-panel-empty">
              {{
                slowOnly
                  ? '当前筛选下没有慢查询'
                  : opFilter
                    ? `暂无 ${opFilter} 类型的 SQL`
                    : '暂无执行记录，待第一条 SQL 落库'
              }}
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- ============ SQL 详情抽屉 ============ -->
    <ElDrawer
      v-model="drawerVisible"
      :size="'min(640px, 94vw)'"
      :title="drawerTitle"
      append-to-body
      class="sql-drawer"
    >
      <template v-if="drawerEntry">
        <div class="mb-4 flex flex-wrap items-center gap-2">
          <span class="sql-drawer__badge" :style="opBadgeStyle(drawerEntry.operation)">{{
            drawerEntry.operation
          }}</span>
          <span
            v-if="drawerEntry.table"
            class="sql-drawer__chip tabular-nums"
            :class="{ 'is-sys': isSysTable(drawerEntry.table) }"
          >
            表 {{ drawerEntry.table }}
          </span>
          <span class="sql-drawer__chip tabular-nums">{{ drawerEntry.timestamp }}</span>
          <span
            class="sql-drawer__dur tabular-nums"
            :style="{ color: durTone(drawerEntry.duration_ms, thresholdMs) }"
          >
            {{ fmtNum(drawerEntry.duration_ms, drawerEntry.duration_ms < 10 ? 2 : 1) }} ms
          </span>
          <span v-if="drawerEntry.is_slow" class="sql-drawer__flag">慢查询</span>
          <span v-if="drawerEntry.is_error" class="sql-drawer__flag sql-drawer__flag--err"
            >执行失败</span
          >
        </div>

        <div class="sql-drawer__meter">
          <div class="sql-drawer__meter-head">
            <span class="text-xs text-g-600">耗时相对慢查询阈值（{{ thresholdMs }}ms）</span>
            <span
              class="text-xs tabular-nums"
              :style="{ color: durTone(drawerEntry.duration_ms, thresholdMs) }"
            >
              {{ fmtNum(Math.min((drawerEntry.duration_ms / thresholdMs) * 100, 9999), 1) }}%
              {{ drawerEntry.duration_ms >= thresholdMs ? '· 已超限' : '' }}
            </span>
          </div>
          <div class="sql-drawer__meter-bar">
            <span
              class="sql-drawer__meter-fill"
              :style="{
                width: `${clamp01((drawerEntry.duration_ms / thresholdMs) * 100)}%`,
                background: durTone(drawerEntry.duration_ms, thresholdMs)
              }"
            />
            <span class="sql-drawer__meter-mark" :style="{ left: '100%' }" title="慢查询阈值" />
          </div>
        </div>

        <div class="sql-drawer__sqlhead">
          <span class="text-xs font-medium text-[var(--el-text-color-regular)]"
            >完整 SQL{{
              drawerEntry.sql.endsWith('...') ? '（服务端仅保留前 512 字符）' : ''
            }}</span
          >
          <ArtButtonTable
            icon="ri:file-copy-line"
            iconClass="bg-theme/12 text-theme"
            title="复制 SQL"
            @click="copySQL(drawerEntry.sql)"
          />
        </div>
        <pre class="sql-drawer__code"><code v-html="highlightSQL(drawerEntry.sql)" /></pre>

        <div class="sql-drawer__hint">
          <ArtSvgIcon icon="ri:information-line" />
          <span
            >定位慢查询：将 SQL 粘贴到数据库客户端执行
            <b>EXPLAIN</b>，关注索引命中与扫描行数。</span
          >
        </div>
      </template>
    </ElDrawer>
  </div>
</template>

<script setup lang="ts">
  import { computed, onBeforeUnmount, ref, watch } from 'vue'
  import { ElDrawer, ElMessage } from 'element-plus'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import type { LineDataItem } from '@/types/component/chart'
  import { fetchSQLHistory, fetchSQLStats } from '../api'
  import type { SqlHistory, SqlHistoryPoint, SqlQueryEntry, SqlStats } from '../api'
  // sparkPath/SparkResult 与 server.vue 原有实现合并至此(两者在 pad=3 时恒等)
  import { sparkPath } from '../composables/use-spark-series'
  // 纯格式化 / 阈值配色工具(原散落在本文件,已抽出并配套单测)
  import {
    OP_COLORS,
    OPS,
    clamp01,
    durTone,
    fmtCount,
    fmtPct,
    heatTone,
    isSysTable,
    opBadgeStyle,
    timeOf
  } from '../composables/use-sql-format'

  defineOptions({ name: 'MonitorSql' })

  const REFRESH_MS = 5_000

  /** 时间范围预设：窗口 + 分桶粒度（与后端 /history 参数一一对应） */
  const RANGES = [
    { key: '15m', label: '15 分钟', window: '15m', step: '3s' },
    { key: '1h', label: '1 小时', window: '1h', step: '12s' },
    { key: '6h', label: '6 小时', window: '6h', step: '60s' }
  ] as const

  interface KpiTile {
    key: string
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
  const stats = ref<SqlStats | null>(null)
  const history = ref<SqlHistory | null>(null)
  const autoRefresh = ref(true)
  const rangeKey = ref<(typeof RANGES)[number]['key']>('15m')
  const updatedAt = ref<Date | null>(null)
  const opFilter = ref<string | null>(null)
  const slowOnly = ref(false)
  const drawerVisible = ref(false)
  const drawerEntry = ref<SqlQueryEntry | null>(null)

  let statsSeq = 0
  let historySeq = 0
  let timer: number | undefined

  const activeRange = computed(() => RANGES.find((r) => r.key === rangeKey.value) ?? RANGES[0])
  const rangeLabel = computed(() => activeRange.value.label)
  const thresholdMs = computed(() => stats.value?.global.slow_threshold_ms ?? 200)
  const buckets = computed(() => history.value?.buckets ?? [])

  // ---------- 数据加载（各自 seq 守卫丢弃过期响应，互不干扰） ----------
  async function loadStats(silent = false) {
    const seq = ++statsSeq
    if (!silent) loading.value = true
    try {
      const data = await fetchSQLStats({})
      if (seq !== statsSeq) return
      stats.value = data
      updatedAt.value = new Date()
    } catch {
      if (seq !== statsSeq) return
      if (!silent) ElMessage.error('SQL 监控数据获取失败，请稍后重试')
    } finally {
      if (seq === statsSeq) loading.value = false
    }
  }

  async function loadHistory(silent = false) {
    const seq = ++historySeq
    if (!silent) loading.value = true
    try {
      const data = await fetchSQLHistory({
        window: activeRange.value.window,
        step: activeRange.value.step
      })
      if (seq !== historySeq) return
      history.value = data
      updatedAt.value = new Date()
    } catch {
      if (seq !== historySeq) return
      if (!silent) ElMessage.error('SQL 时序数据获取失败，请稍后重试')
    } finally {
      if (seq === historySeq) loading.value = false
    }
  }

  async function loadAll(silent = false) {
    await Promise.all([loadStats(silent), loadHistory(silent)])
  }

  function switchRange(key: (typeof RANGES)[number]['key']) {
    if (rangeKey.value === key) return
    rangeKey.value = key
    loadHistory(false)
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
        }, REFRESH_MS)
      }
    },
    { immediate: true }
  )

  onBeforeUnmount(() => {
    if (timer) clearInterval(timer)
  })

  // ---------- 格式化 ----------
  function fmtNum(v: number | null | undefined, digits = 0): string {
    return typeof v === 'number' && Number.isFinite(v) ? v.toFixed(digits) : '-'
  }

  const errorRateText = computed(() =>
    fmtPct(stats.value?.global.error_count ?? 0, stats.value?.global.count ?? 0)
  )
  const p95RatioText = computed(() => fmtPct(stats.value?.global.p95_ms ?? 0, thresholdMs.value))

  function updatedAtText(): string {
    const d = updatedAt.value
    return d ? d.toLocaleTimeString('zh-CN', { hour12: false }) : '—'
  }

  // 状态阈值配色(heatTone/durTone/opBadgeStyle)已移至
  // composables/use-sql-format.ts,本文件只保留响应式编排。

  // ---------- KPI 磁贴（6 项，颜色取自全局主色板） ----------
  const kpis = computed<KpiTile[]>(() => {
    const series = {
      qps: buckets.value.map((b) => b.qps),
      count: buckets.value.map((b) => b.count),
      avg: buckets.value.map((b) => b.avg_ms),
      p95: buckets.value.map((b) => b.p95_ms),
      slow: buckets.value.map((b) => b.slow_count),
      err: buckets.value.map((b) => b.error_count)
    }
    const mk = (key: keyof typeof series) => sparkPath(series[key])
    const g = stats.value?.global
    const total = g?.count ?? 0

    return [
      {
        key: 'qps',
        label: '此刻 QPS',
        icon: 'ri:heart-3-line',
        tile: '#3b82f6',
        tile2: '#60a5fa',
        color: '#3b82f6',
        value: fmtNum(history.value?.recent_qps ?? null, 1),
        sub: history.value ? `最近 ${history.value.step_seconds}s 滚动` : '等待时序采样',
        title: '当前每秒执行的 SQL 数（由最近一个时间桶推算）',
        line: mk('qps').line,
        area: mk('qps').area
      },
      {
        key: 'count',
        label: '累计执行',
        icon: 'ri:numbers-line',
        tile: '#7c3aed',
        tile2: '#a78bfa',
        color: '#7c3aed',
        value: fmtCount(total),
        sub: '自服务进程启动以来',
        title: '累计执行的 SQL 总数（含迷你趋势）',
        line: mk('count').line,
        area: mk('count').area
      },
      {
        key: 'avg',
        label: '平均耗时',
        icon: 'ri:timer-line',
        tile: '#06b6d4',
        tile2: '#22d3ee',
        color: '#06b6d4',
        value: fmtNum(g?.avg_ms ?? null, g && g.avg_ms < 10 ? 2 : 1),
        sub: '全局累计平均',
        title: '全局平均 SQL 耗时（含迷你趋势）',
        line: mk('avg').line,
        area: mk('avg').area
      },
      {
        key: 'p95',
        label: 'P95 耗时',
        icon: 'ri:line-chart-line',
        tile: '#f59e0b',
        tile2: '#fbbf24',
        color: '#f59e0b',
        value: fmtNum(g?.p95_ms ?? null, g && g.p95_ms < 10 ? 2 : 1),
        sub: `阈值 ${thresholdMs.value}ms · ${p95RatioText.value}`,
        title: '95% 的查询快于此值（含迷你趋势）',
        line: mk('p95').line,
        area: mk('p95').area
      },
      {
        key: 'slow',
        label: '慢查询',
        icon: 'ri:error-warning-line',
        tile: '#ec4899',
        tile2: '#f472b6',
        color: '#ec4899',
        value: fmtCount(g?.slow_count ?? 0),
        sub: fmtPct(g?.slow_count ?? 0, total),
        title: `超过阈值 ${thresholdMs.value}ms 的累计查询数（含迷你趋势）`,
        line: mk('slow').line,
        area: mk('slow').area
      },
      {
        key: 'err',
        label: '执行错误',
        icon: 'ri:close-circle-line',
        tile: '#dc2626',
        tile2: '#f87171',
        color: '#dc2626',
        value: fmtCount(g?.error_count ?? 0),
        sub: `错误率 ${errorRateText.value}`,
        title: '执行失败的 SQL 累计次数（含迷你趋势）',
        line: mk('err').line,
        area: mk('err').area
      }
    ]
  })

  // ---------- 时序图表数据 ----------
  // 后端已在计算层把 QPS/耗时统一量化到 2 位小数(util.Round2),这里 toFixed
  // 仅作防御性兜底(旧缓存数据),保证 tooltip 不出现长尾小数,不影响图表形态。
  //
  // 固定槽位窗口：直接按"时间窗口截取"渲染时，每次新增一个桶所有旧点都会
  // 左移压缩，平滑曲线控制点整体联动重算 → 视觉上整条线都在刷新。
  // 改为按 step 切成长度恒定的槽位(绝对时间对齐)，旧点像素位置恒定不动，
  // 新数据只出现在最右端，配合 live 模式形成真正的增量滚动效果。
  const slotCount = computed(() => {
    const h = history.value
    if (!h || !h.step_seconds) return 0
    const n = Math.floor(h.window_seconds / h.step_seconds)
    return Math.min(Math.max(n, 1), 400)
  })

  interface SlotFrame {
    labels: string[]
    qps: number[]
    p50: (number | null)[]
    p95: (number | null)[]
    p99: (number | null)[]
  }

  const pad2 = (n: number) => String(n).padStart(2, '0')

  const slotFrame = computed<SlotFrame>(() => {
    const empty: SlotFrame = { labels: [], qps: [], p50: [], p95: [], p99: [] }
    const h = history.value
    const src = buckets.value
    if (!h || !h.step_seconds || !src.length) return empty

    const stepMs = h.step_seconds * 1000
    const count = slotCount.value
    const tsOf = (v: string) => Date.parse(v.replace(' ', 'T'))
    const slotMap = new Map<number, SqlHistoryPoint>()
    for (const b of src) {
      slotMap.set(Math.floor(tsOf(b.timestamp) / stepMs), b)
    }
    const lastSlot = Math.floor(tsOf(src[src.length - 1].timestamp) / stepMs)

    const frame: SlotFrame = { labels: [], qps: [], p50: [], p95: [], p99: [] }
    for (let s = lastSlot - count + 1; s <= lastSlot; s++) {
      const b = slotMap.get(s)
      const d = new Date(s * stepMs)
      frame.labels.push(`${pad2(d.getHours())}:${pad2(d.getMinutes())}:${pad2(d.getSeconds())}`)
      // 空槽：QPS 为 0(无请求即 0，符合事实)；耗时用 null 断线(无采样不误导)
      frame.qps.push(b ? Number(b.qps.toFixed(2)) : 0)
      frame.p50.push(b ? Number(b.p50_ms.toFixed(2)) : null)
      frame.p95.push(b ? Number(b.p95_ms.toFixed(2)) : null)
      frame.p99.push(b ? Number(b.p99_ms.toFixed(2)) : null)
    }
    return frame
  })

  const timeLabels = computed(() => slotFrame.value.labels)
  const qpsData = computed(() => slotFrame.value.qps)
  const latencySeries = computed<LineDataItem[]>(() => [
    // ECharts 原生支持 null 断点；这里断言一次以复用共享组件的 number[] 类型
    { name: 'P50', data: slotFrame.value.p50 as unknown as number[], lineWidth: 2 },
    { name: 'P95', data: slotFrame.value.p95 as unknown as number[], lineWidth: 2 },
    { name: 'P99', data: slotFrame.value.p99 as unknown as number[], lineWidth: 2 }
  ])

  // ---------- 延迟热力条 ----------
  const heatCells = computed(() => {
    const src = buckets.value
    const stride = Math.max(1, Math.ceil(src.length / 120))
    const cells: { color: string; title: string }[] = []
    for (let i = 0; i < src.length; i += stride) {
      const b = src[i]
      cells.push({
        color: heatTone(b.p95_ms, thresholdMs.value),
        title: `${timeOf(b.timestamp)} · P95 ${fmtNum(b.p95_ms, 2)}ms · ${fmtCount(b.count)} 次`
      })
    }
    return cells
  })

  // ---------- 操作类型环形图 ----------
  const ringData = computed(() =>
    Object.entries(stats.value?.by_operation ?? {})
      .filter(([, d]) => (d.count ?? 0) > 0)
      .map(([op, d]) => ({ name: op, value: d.count }))
  )
  const ringColors = computed(() => ringData.value.map((d) => OP_COLORS[d.name] ?? OP_COLORS.OTHER))
  const ringTotal = computed(() => stats.value?.global.count ?? 0)

  // ---------- 热点表 ----------
  interface HotRow {
    name: string
    count: number
    avg: number
  }

  const hotTables = computed<HotRow[]>(() =>
    Object.entries(stats.value?.by_table ?? {})
      .map(([name, d]) => ({ name, count: d.count ?? 0, avg: d.avg_ms ?? 0 }))
      .filter((row) => row.name !== '') // 无表名的 SQL(事务/函数调用)不进热点榜
      .sort((a, b) => b.count - a.count)
      .slice(0, 10)
  )
  const hotMax = computed(() => hotTables.value[0]?.count ?? 0)

  // ---------- 慢查询实录 ----------
  const visibleQueries = computed(() => {
    let list = stats.value?.slow_queries ?? []
    if (opFilter.value)
      list = list.filter((q) => (q.operation || 'OTHER').toUpperCase() === opFilter.value)
    if (slowOnly.value) list = list.filter((q) => q.is_slow || q.duration_ms >= thresholdMs.value)
    return list
  })
  const slowMaxMs = computed(() =>
    Math.max(...(stats.value?.slow_queries ?? []).map((q) => q.duration_ms), 1)
  )
  const drawerTitle = computed(() => {
    const q = drawerEntry.value
    if (!q) return 'SQL 详情'
    return q.table ? `${q.operation} · ${q.table}` : `${q.operation} · 详情`
  })

  function openDrawer(q: SqlQueryEntry) {
    drawerEntry.value = q
    drawerVisible.value = true
  }

  async function copySQL(sql: string) {
    try {
      await navigator.clipboard.writeText(sql)
      ElMessage.success('已复制 SQL 到剪贴板')
    } catch {
      // 降级：execCommand 方式
      const ta = document.createElement('textarea')
      ta.value = sql
      ta.style.position = 'fixed'
      ta.style.opacity = '0'
      document.body.appendChild(ta)
      ta.select()
      const ok = document.execCommand('copy')
      document.body.removeChild(ta)
      if (ok) {
        ElMessage.success('已复制 SQL 到剪贴板')
      } else {
        ElMessage.error('复制失败，请手动选择复制')
      }
    }
  }

  // ---------- SQL 关键字高亮（先转义再着色，防注入） ----------
  function highlightSQL(sql: string): string {
    const escaped = sql.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
    let out = escaped
    out = out.replace(
      /\b(SELECT|INSERT|UPDATE|DELETE|REPLACE)\b/gi,
      (m) => `<span class="sql-kw sql-kw--dml">${m}</span>`
    )
    out = out.replace(
      /\b(FROM|WHERE|JOIN|LEFT|RIGHT|INNER|OUTER|CROSS|ON|ORDER|GROUP|BY|LIMIT|OFFSET|HAVING|AND|OR|NOT|IN|BETWEEN|LIKE|AS|DISTINCT|VALUES|SET|INTO|UNION|ALL|DESC|ASC|IS|NULL|IF|WHEN|THEN|ELSE|END|CASE)\b/gi,
      (m) => `<span class="sql-kw sql-kw--clause">${m}</span>`
    )
    out = out.replace(
      /\b(COUNT|SUM|AVG|MIN|MAX|GROUP_CONCAT|NOW|CURDATE|DATE_FORMAT|IFNULL|COALESCE|CONCAT|ROUND|LOWER|UPPER|TRIM|SUBSTR|LENGTH)\b/gi,
      (m) => `<span class="sql-kw sql-kw--fn">${m}</span>`
    )
    return out
  }

  // ---------- 初次加载 ----------
  loadAll(false)
</script>

<style scoped lang="scss">
  /* 四视图共享的设计令牌(卡片 / KPI / 呼吸圆点 / 入场动画) */
  @use './monitor-tokens' as t;

  @include t.rise-keyframes;
  @include t.pulse-keyframes;

  /* ---------- 基础卡片 ---------- */
  .sql-card {
    @include t.card;
    /* 本视图特有:卡片可交互(hover 抬起),较共享令牌多一组 transition */
    transition:
      transform 0.2s ease,
      box-shadow 0.2s ease,
      border-color 0.2s ease;
    @include t.rise;
  }

  .dark .sql-card {
    @include t.card-dark;
  }

  /* ---------- 页头 ---------- */
  .sql-hero__icon {
    width: 44px;
    height: 44px;
    flex: none;
    border-radius: 14px;
    font-size: 22px;
    color: #fff;
    background: linear-gradient(135deg, #3b82f6 0%, #06b6d4 100%);
    box-shadow: 0 4px 12px rgba(59, 130, 246, 0.35);
  }

  .live-dot {
    @include t.live-dot;
  }

  .live-dot.is-loading {
    background: #f59e0b;
    animation-duration: 0.9s;
  }

  /* 时间范围分段 */
  .sql-range {
    padding: 2px;
    border-radius: 10px;
    border: 1px solid var(--default-border);
    background: var(--art-gray-100);
  }

  .sql-range__item {
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

  .sql-range__item:hover {
    color: var(--el-text-color-primary);
  }

  .sql-range__item.is-active {
    background: var(--default-box-color);
    color: var(--el-color-primary);
    font-weight: 600;
    box-shadow: 0 1px 2px rgba(16, 24, 40, 0.08);
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
  .sql-chip {
    width: 34px;
    height: 34px;
    flex: none;
    border-radius: 10px;
    font-size: 17px;
  }

  .sql-card__sub {
    @include t.card-sub;
  }

  .sql-summary-pill {
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

  .sql-summary-pill :deep(svg) {
    width: 13px;
    height: 13px;
  }

  /* ---------- 延迟热力条 ---------- */
  .sql-heat__legend {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    font-size: 11px;
  }

  .sql-heat__swatch {
    width: 12px;
    height: 8px;
    border-radius: 3px;
  }

  .sql-heat__thr {
    padding: 2px 8px;
    border-radius: 999px;
    background: rgba(220, 38, 38, 0.1);
    color: #dc2626;
    font-size: 11px;
    font-weight: 600;
  }

  .dark .sql-heat__thr {
    color: #fca5a5;
    background: rgba(220, 38, 38, 0.16);
  }

  .sql-heat__strip {
    display: flex;
    gap: 2px;
    padding: 12px;
    border-radius: 10px;
    background: var(--art-gray-100);
    overflow-x: auto;
  }

  .sql-heat__cell {
    flex: 1 1 0;
    min-width: 2px;
    height: 14px;
    border-radius: 2px;
    transition: transform 0.15s ease;
  }

  .sql-heat__cell:hover {
    transform: scaleY(1.7);
  }

  /* ---------- 耗时画像 ---------- */
  .sql-cells {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 8px;
  }

  .sql-cell {
    padding: 8px 10px;
    border-radius: 10px;
    background: var(--art-gray-100);
  }

  .sql-cell__label {
    font-size: 11px;
    color: var(--el-text-color-secondary);
  }

  .sql-cell__value {
    margin-top: 2px;
    font-size: 13px;
    font-weight: 600;
    color: var(--el-text-color-primary);
    font-variant-numeric: tabular-nums;
  }

  .sql-cell__unit {
    margin-left: 2px;
    font-size: 10px;
    font-style: normal;
    font-weight: 400;
    color: var(--el-text-color-secondary);
  }

  .sql-cells--health {
    margin-top: 8px;
  }

  /* P95 阈值水位 */
  .sql-gauge-strip {
    margin-top: 10px;
  }

  .sql-gauge-strip__head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    margin-bottom: 6px;
  }

  .sql-gauge-strip__text {
    font-size: 11px;
    color: var(--el-text-color-secondary);
  }

  .sql-gauge-strip__bar {
    position: relative;
    height: 8px;
    border-radius: 999px;
    background: var(--el-fill-color-light);
    overflow: visible;
  }

  .sql-gauge-strip__fill {
    position: absolute;
    inset: 0 auto 0 0;
    border-radius: 999px;
    transition: width 0.45s ease;
  }

  .sql-gauge-strip__mark {
    position: absolute;
    top: -3px;
    bottom: -3px;
    width: 2px;
    border-radius: 2px;
    background: #dc2626;
    opacity: 0.85;
  }

  /* ---------- 热点表 ---------- */
  .sql-hot__row {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 5px 4px;
    border-radius: 8px;
    transition: background 0.15s ease;
  }

  .sql-hot__row:hover {
    background: var(--art-hover-color);
  }

  .sql-hot__rank {
    width: 20px;
    height: 20px;
    flex: none;
    border-radius: 6px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    font-size: 11px;
    font-weight: 700;
    color: var(--el-text-color-secondary);
    background: var(--el-fill-color-light);
    font-variant-numeric: tabular-nums;
  }

  .sql-hot__rank.is-1 {
    color: #fff;
    background: linear-gradient(135deg, #f59e0b, #fbbf24);
  }

  .sql-hot__rank.is-2 {
    color: #fff;
    background: linear-gradient(135deg, #94a3b8, #cbd5e1);
  }

  .sql-hot__rank.is-3 {
    color: #fff;
    background: linear-gradient(135deg, #d97706, #f59e0b);
  }

  .sql-hot__name {
    width: 118px;
    flex: none;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 12px;
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, 'Fira Code', monospace;
    color: var(--el-text-color-primary);
  }

  .sql-hot__name.is-sys {
    color: var(--el-text-color-placeholder);
  }

  .sql-hot__bar {
    position: relative;
    flex: 1;
    height: 6px;
    border-radius: 999px;
    background: var(--el-fill-color-light);
    overflow: hidden;
  }

  .sql-hot__fill {
    position: absolute;
    inset: 0 auto 0 0;
    border-radius: 999px;
    transition: width 0.45s ease;
  }

  .sql-hot__count {
    width: 64px;
    flex: none;
    text-align: right;
    font-size: 12px;
    font-weight: 600;
    color: var(--el-text-color-primary);
  }

  .sql-hot__avg {
    width: 66px;
    flex: none;
    text-align: right;
    font-size: 11px;
    color: var(--el-text-color-secondary);
  }

  /* ---------- 慢查询实录 ---------- */
  .sql-slowcard {
    display: flex;
    flex-direction: column;
  }

  .sql-filterchip {
    padding: 3px 10px;
    border-radius: 999px;
    border: 1px solid var(--default-border);
    background: transparent;
    color: var(--el-text-color-secondary);
    font-size: 11px;
    line-height: 18px;
    cursor: pointer;
    transition:
      background 0.2s ease,
      color 0.2s ease,
      border-color 0.2s ease;
  }

  .sql-filterchip:hover {
    color: var(--el-text-color-primary);
    border-color: var(--el-color-primary-light-5);
  }

  .sql-filterchip.is-active {
    border-color: var(--el-color-primary);
    background: var(--el-color-primary-light-9);
    color: var(--el-color-primary);
    font-weight: 600;
  }

  .dark .sql-filterchip.is-active {
    background: rgba(59, 130, 246, 0.16);
  }

  .sql-filterchip.is-slow {
    color: #dc2626;
    border-color: rgba(220, 38, 38, 0.35);
  }

  .dark .sql-filterchip.is-slow {
    color: #fca5a5;
  }

  .sql-slowscroll {
    scrollbar-width: thin;
    max-height: 512px;
  }

  .sql-slow {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 7px 6px;
    border: none;
    border-radius: 10px;
    background: transparent;
    text-align: left;
    cursor: pointer;
    transition: background 0.15s ease;
  }

  .sql-slow:hover {
    background: var(--art-hover-color);
  }

  .sql-slow + .sql-slow {
    margin-top: 2px;
  }

  .sql-slow__time {
    flex: none;
    font-size: 11px;
    color: var(--el-text-color-secondary);
  }

  .sql-slow__op {
    flex: none;
    padding: 1px 7px;
    border-radius: 6px;
    border: 1px solid;
    font-size: 10.5px;
    font-weight: 700;
    letter-spacing: 0.04em;
  }

  .sql-slow__table {
    flex: none;
    max-width: 110px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    padding: 1px 7px;
    border-radius: 6px;
    background: var(--art-gray-100);
    color: var(--el-text-color-secondary);
    font-size: 10.5px;
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, 'Fira Code', monospace;
  }

  .sql-slow__table.is-sys {
    border: 1px dashed var(--default-border);
    color: var(--el-text-color-placeholder);
  }

  .sql-slow__dur {
    flex: none;
    width: 62px;
    text-align: right;
    font-size: 12.5px;
    font-weight: 650;
  }

  .sql-slow__dur i {
    margin-left: 1px;
    font-size: 10px;
    font-style: normal;
    font-weight: 400;
  }

  .sql-slow__bar {
    position: relative;
    width: 56px;
    flex: none;
    height: 5px;
    border-radius: 999px;
    background: var(--el-fill-color-light);
    overflow: hidden;
  }

  .sql-slow__bar-fill {
    position: absolute;
    inset: 0 auto 0 0;
    border-radius: 999px;
    transition: width 0.45s ease;
  }

  .sql-slow__sql {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    font-size: 11.5px;
    line-height: 1.5;
    white-space: nowrap;
    text-overflow: ellipsis;
    color: var(--el-text-color-regular);
  }

  .sql-slow__sql :deep(.sql-kw) {
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, 'Fira Code', monospace;
  }

  .sql-slow__sql :deep(.sql-kw--dml) {
    color: #7c3aed;
  }

  .sql-slow__sql :deep(.sql-kw--clause) {
    color: #0ea5e9;
  }

  .sql-slow__sql :deep(.sql-kw--fn) {
    color: #d97706;
  }

  .dark .sql-slow__sql :deep(.sql-kw--dml) {
    color: #a78bfa;
  }

  .dark .sql-slow__sql :deep(.sql-kw--clause) {
    color: #38bdf8;
  }

  .dark .sql-slow__sql :deep(.sql-kw--fn) {
    color: #fbbf24;
  }

  .sql-slow__flag {
    flex: none;
    padding: 1px 6px;
    border-radius: 6px;
    background: rgba(220, 38, 38, 0.12);
    color: #dc2626;
    font-size: 10px;
    font-weight: 700;
  }

  .dark .sql-slow__flag {
    color: #fca5a5;
    background: rgba(220, 38, 38, 0.18);
  }

  .sql-slow__arrow {
    flex: none;
    font-size: 13px;
    color: var(--el-text-color-secondary);
    opacity: 0;
    transition: opacity 0.15s ease;
  }

  .sql-slow:hover .sql-slow__arrow {
    opacity: 1;
  }

  /* ---------- 抽屉 ---------- */
  .sql-drawer__badge {
    padding: 2px 10px;
    border-radius: 8px;
    border: 1px solid;
    font-size: 12px;
    font-weight: 700;
    letter-spacing: 0.05em;
  }

  .sql-drawer__chip {
    padding: 2px 10px;
    border-radius: 999px;
    border: 1px solid var(--default-border);
    background: var(--art-gray-100);
    color: var(--el-text-color-secondary);
    font-size: 12px;
  }

  .sql-drawer__chip.is-sys {
    border-style: dashed;
    color: var(--el-text-color-placeholder);
  }

  .sql-drawer__dur {
    font-size: 18px;
    font-weight: 700;
  }

  .sql-drawer__flag {
    padding: 2px 10px;
    border-radius: 999px;
    background: rgba(220, 38, 38, 0.12);
    color: #dc2626;
    font-size: 12px;
    font-weight: 600;
  }

  .dark .sql-drawer__flag {
    color: #fca5a5;
  }

  .sql-drawer__flag--err {
    background: rgba(220, 38, 38, 0.16);
  }

  .sql-drawer__meter {
    margin-bottom: 14px;
    padding: 10px 12px;
    border-radius: 10px;
    background: var(--art-gray-100);
  }

  .sql-drawer__meter-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    margin-bottom: 7px;
  }

  .sql-drawer__meter-bar {
    position: relative;
    height: 8px;
    border-radius: 999px;
    background: var(--el-fill-color-light);
  }

  .sql-drawer__meter-fill {
    position: absolute;
    inset: 0 auto 0 0;
    border-radius: 999px;
    transition: width 0.45s ease;
  }

  .sql-drawer__meter-mark {
    position: absolute;
    top: -3px;
    bottom: -3px;
    width: 2px;
    border-radius: 2px;
    background: #dc2626;
  }

  .sql-drawer__sqlhead {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    margin: 14px 0 8px;
  }

  .sql-drawer__code {
    margin: 0;
    padding: 14px;
    border-radius: 12px;
    border: 1px solid var(--default-border);
    background: var(--art-gray-100);
    color: var(--el-text-color-primary);
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, 'Fira Code', monospace;
    font-size: 12.5px;
    line-height: 1.7;
    white-space: pre-wrap;
    word-break: break-all;
  }

  .sql-drawer__code :deep(.sql-kw--dml) {
    color: #7c3aed;
    font-weight: 600;
  }

  .sql-drawer__code :deep(.sql-kw--clause) {
    color: #0ea5e9;
  }

  .sql-drawer__code :deep(.sql-kw--fn) {
    color: #d97706;
  }

  .dark .sql-drawer__code :deep(.sql-kw--dml) {
    color: #a78bfa;
  }

  .dark .sql-drawer__code :deep(.sql-kw--clause) {
    color: #38bdf8;
  }

  .dark .sql-drawer__code :deep(.sql-kw--fn) {
    color: #fbbf24;
  }

  .sql-drawer__hint {
    display: flex;
    align-items: flex-start;
    gap: 7px;
    margin-top: 12px;
    padding: 9px 12px;
    border-radius: 10px;
    background: var(--el-color-primary-light-9);
    color: var(--el-text-color-secondary);
    font-size: 12px;
    line-height: 1.6;
  }

  .sql-drawer__hint :deep(svg) {
    margin-top: 1px;
    flex: none;
    color: var(--el-color-primary);
  }

  /* ---------- 空状态 / 占位 ---------- */
  .sql-empty__icon {
    width: 64px;
    height: 64px;
    border-radius: 20px;
    font-size: 30px;
    color: var(--el-color-primary);
    background: rgba(59, 130, 246, 0.12);
  }

  .sql-panel-empty {
    padding: 12px;
    border-radius: 10px;
    border: 1px dashed var(--default-border-dashed);
    color: var(--el-text-color-secondary);
    font-size: 12px;
    text-align: center;
  }

  /* ---------- 键盘可达性 ---------- */
  .sql-range__item:focus-visible,
  .sql-filterchip:focus-visible,
  .sql-slow:focus-visible {
    outline: 2px solid var(--el-color-primary);
    outline-offset: 2px;
    border-radius: 8px;
  }

  /* ---------- 动效降级 ---------- */
  /* 动画停用收敛到共享 mixin(其余视图曾各自重写同一媒体查询) */
  @include t.reduced-motion('.sql-card', '.live-dot');
  @media (prefers-reduced-motion: reduce) {
    .sql-gauge-strip__fill,
    .sql-hot__fill,
    .sql-slow__bar-fill,
    .sql-drawer__meter-fill,
    .sql-heat__cell {
      transition: none;
    }
  }
</style>
