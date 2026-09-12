<template>
  <div class="pprof-page art-full-height overflow-y-auto">
    <div class="pprof-page__inner p-4 pb-8 md:p-5">
      <!-- ============ 页头：标题 + 实时状态 + 启停开关 ============ -->
      <div class="pf-hero mb-4 flex flex-wrap items-center gap-3">
        <div class="pf-hero__icon flex-cc">
          <ArtSvgIcon icon="ri:fire-line" />
        </div>
        <div class="pf-hero__text min-w-0">
          <h2 class="text-lg font-semibold text-[var(--el-text-color-primary)]">pprof 性能分析</h2>
          <p class="mt-0.5 flex flex-wrap items-center gap-1.5 text-xs text-g-600">
            <span
              class="live-dot inline-block h-1.5 w-1.5 rounded-full"
              :class="enabled ? 'bg-success' : 'bg-[#94a3b8]'"
            />
            {{ statusSubText }}
          </p>
        </div>
        <div class="ml-auto flex flex-wrap items-center gap-2">
          <div v-if="enabled" class="relative">
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
          <button
            type="button"
            class="pf-toggle"
            :class="{ 'is-on': enabled }"
            :disabled="busy"
            @click="toggleEnabled"
          >
            <span class="pf-toggle__dot" />
            <ArtSvgIcon :icon="enabled ? 'ri:lock-unlock-line' : 'ri:lock-line'" />
            {{ enabled ? '停用采集' : '启用采集' }}
          </button>
          <ArtButtonTable
            v-if="enabled"
            icon="ri:refresh-line"
            iconClass="bg-theme/12 text-theme"
            title="刷新状态"
            @click="refreshAll(false)"
          />
        </div>
      </div>

      <!-- ============ 采集开关状态条 ============ -->
      <div class="pf-card pf-ctl flex flex-wrap items-center gap-x-5 gap-y-2 !p-4">
        <div class="flex items-center gap-3">
          <span class="pf-ctl__badge flex-cc" :class="enabled ? 'is-on' : 'is-off'">
            <ArtSvgIcon :icon="enabled ? 'ri:shield-star-line' : 'ri:shield-keyhole-line'" />
          </span>
          <div>
            <div class="text-sm font-semibold text-[var(--el-text-color-primary)]">
              数据端点
              <span :class="enabled ? 'text-success' : 'text-[#94a3b8]'">{{
                enabled ? '已开放' : '已锁定'
              }}</span>
            </div>
            <div class="text-xs text-g-600">
              {{
                enabled
                  ? '火焰图与原始数据均可正常访问'
                  : '启用后方可采样查看火焰图、热点函数与原始数据'
              }}
            </div>
          </div>
        </div>

        <div class="flex flex-wrap items-center gap-2">
          <span class="pf-ctl__chip tabular-nums" :class="{ 'is-live': enabled && autoOff > 0 }">
            <ArtSvgIcon icon="ri:timer-flash-line" />
            自动关闭剩余
            <b class="tabular-nums">{{ countdownText }}</b>
          </span>
          <span class="pf-ctl__chip tabular-nums">
            <ArtSvgIcon icon="ri:settings-3-line" />
            超时配置 {{ autoOff > 0 ? fmtDur(autoOff) : '未开启' }}
          </span>
          <span class="pf-ctl__chip pf-ctl__chip--url" :title="profileBaseUrl">
            <ArtSvgIcon icon="ri:link" />
            <span class="max-w-56 truncate font-mono">{{ profileBaseUrl }}</span>
            <span class="pf-ctl__copy flex-cc" title="复制端点地址" @click="copyEndpoint"
              >复制</span
            >
          </span>
        </div>
      </div>

      <!-- ============ KPI 数据磁贴（4 项） ============ -->
      <div class="kpi-grid mt-4 grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <div class="pf-card kpi-tile">
          <div
            class="kpi-tile__icon flex-cc"
            :style="{
              '--tile': enabled ? '#10b981' : '#94a3b8',
              '--tile2': enabled ? '#34d399' : '#cbd5e1'
            }"
          >
            <ArtSvgIcon :icon="enabled ? 'ri:lock-unlock-line' : 'ri:lock-line'" />
          </div>
          <div class="min-w-0">
            <div class="kpi-tile__value" :class="enabled ? 'text-success' : ''">{{
              enabled ? '已启用' : '已停用'
            }}</div>
            <div class="kpi-tile__label">采集状态</div>
            <div class="kpi-tile__sub">{{ enabled ? '数据端点开放中' : '端点已锁定' }}</div>
          </div>
        </div>
        <div class="pf-card kpi-tile">
          <div class="kpi-tile__icon flex-cc" style="--tile: #f97316; --tile2: #fb923c">
            <ArtSvgIcon icon="ri:timer-2-line" />
          </div>
          <div class="min-w-0">
            <div class="kpi-tile__value tabular-nums">{{ autoOff > 0 ? countdownText : '—' }}</div>
            <div class="kpi-tile__label">自动关闭倒计时</div>
            <div class="kpi-tile__sub">{{
              autoOff > 0 ? `启用后 ${fmtDur(autoOff)} 自动锁定` : '未配置自动关闭'
            }}</div>
          </div>
        </div>
        <div class="pf-card kpi-tile">
          <div class="kpi-tile__icon flex-cc" style="--tile: #3b82f6; --tile2: #60a5fa">
            <ArtSvgIcon icon="ri:stack-line" />
          </div>
          <div class="min-w-0">
            <div class="kpi-tile__value tabular-nums"
              >{{ activeSnapshotCount }} / {{ snapshotTotal }}</div
            >
            <div class="kpi-tile__label">活跃快照</div>
            <div class="kpi-tile__sub">样本数 &gt; 0 的快照 profile</div>
          </div>
        </div>
        <div class="pf-card kpi-tile">
          <div class="kpi-tile__icon flex-cc" style="--tile: #10b981; --tile2: #34d399">
            <ArtSvgIcon icon="ri:bar-chart-2-line" />
          </div>
          <div class="min-w-0">
            <div class="kpi-tile__value truncate tabular-nums">{{ flameTotalText }}</div>
            <div class="kpi-tile__label">总采样值 · {{ flameData?.sampleType ?? '-' }}</div>
            <div class="kpi-tile__sub">{{
              flameData ? `${flameData.sampleCount} 条采样` : '尚未加载火焰图'
            }}</div>
          </div>
        </div>
      </div>

      <!-- ============ 主网格：火焰图(左) + 热点函数/采集下载(右) ============ -->
      <div class="mt-4 grid grid-cols-1 gap-4 lg:grid-cols-5">
        <!-- ── 左列：profile 选择 + 火焰图 ── -->
        <div class="flex min-w-0 flex-col gap-4 lg:col-span-3">
          <!-- profile 选择器 -->
          <div class="pf-card !p-4">
            <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
              <div class="flex items-center gap-2.5">
                <span class="pf-chip flex-cc" style="color: #f97316; background: #f9731614">
                  <ArtSvgIcon icon="ri:compass-3-line" />
                </span>
                <div>
                  <div class="text-sm font-semibold text-[var(--el-text-color-primary)]"
                    >选择分析目标</div
                  >
                  <div class="pf-card__sub">快照类可立即采样解析 · 采集类按需抓取原始数据</div>
                </div>
              </div>
              <span v-if="flameTruncatedText" class="pf-summary-pill">
                <ArtSvgIcon icon="ri:error-warning-line" />
                {{ flameTruncatedText }}
              </span>
            </div>

            <div class="flex flex-wrap items-center gap-1.5">
              <button
                v-for="p in profiles"
                :key="p.name"
                type="button"
                class="pf-profile-chip"
                :class="{ 'is-active': selectedProfile === p.name, 'is-disabled': !enabled }"
                :style="chipStyle(p)"
                :title="chipTitle(p)"
                @click="selectProfile(p.name)"
              >
                <ArtSvgIcon :icon="profileIcon(p.name)" />
                <span class="font-mono font-semibold">{{ p.name }}</span>
                <span
                  v-if="p.category === 'snapshot'"
                  class="pf-profile-chip__count tabular-nums"
                  >{{ p.count }}</span
                >
                <span v-else class="pf-profile-chip__count">按需</span>
              </button>
            </div>
          </div>

          <!-- 火焰图 -->
          <div class="pf-card pf-flame-card relative !p-4">
            <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
              <div class="flex items-center gap-2.5">
                <span class="pf-chip flex-cc" style="color: #ef4444; background: #ef444414">
                  <ArtSvgIcon icon="ri:fire-line" />
                </span>
                <div>
                  <div class="text-sm font-semibold text-[var(--el-text-color-primary)]">
                    火焰图
                    <span class="ml-1 align-middle font-mono text-xs font-normal text-g-600">{{
                      profileLabel
                    }}</span>
                  </div>
                  <div class="pf-card__sub">
                    <template v-if="flameData">
                      {{ fmtValue(flameData.totalValue) }} {{ unitLabel }} ·
                      {{ flameData.sampleCount }} 条采样 · 点击帧下钻
                    </template>
                    <template v-else>按调用栈宽度呈现热点占比</template>
                  </div>
                </div>
              </div>

              <div class="flex flex-wrap items-center gap-2">
                <ElInput v-model="searchText" class="pf-search" placeholder="搜索函数" clearable>
                  <template #prefix>
                    <ArtSvgIcon icon="ri:search-line" class="text-g-500" />
                  </template>
                </ElInput>
                <ArtButtonTable
                  icon="ri:arrow-go-back-line"
                  iconClass="bg-theme/12 text-theme"
                  title="返回全图"
                  @click="resetZoom"
                />
                <ArtButtonTable
                  icon="ri:file-download-line"
                  iconClass="bg-theme/12 text-theme"
                  :title="
                    selectedProfile === 'profile' || selectedProfile === 'trace'
                      ? '采集类请到右侧按需采集'
                      : `下载 ${selectedProfile} 原始数据`
                  "
                  @click="downloadProfile"
                />
                <ArtButtonTable
                  icon="ri:refresh-line"
                  iconClass="bg-theme/12 text-theme"
                  title="重新解析"
                  @click="loadFlame(false)"
                />
              </div>
            </div>

            <!-- 缩放路径面包屑 -->
            <div
              v-if="zoomStack.length"
              class="pf-breadcrumb mb-3 flex flex-wrap items-center gap-1"
            >
              <button type="button" class="pf-breadcrumb__chip" @click="resetZoom">全部</button>
              <template v-for="(n, i) in zoomStack" :key="i">
                <ArtSvgIcon icon="ri:arrow-right-s-line" class="text-xs text-g-500" />
                <button
                  type="button"
                  class="pf-breadcrumb__chip font-mono"
                  :class="{ 'is-last': i === zoomStack.length - 1 }"
                  :title="n.name"
                  @click="zoomTo(i)"
                >
                  {{ n.name }}
                </button>
              </template>
            </div>

            <div v-loading="flameLoading" class="pf-flame-body rounded-xl">
              <!-- 火焰图 -->
              <template v-if="flameDepth">
                <div
                  class="pf-flame"
                  role="img"
                  :aria-label="`${profileLabel} 火焰图`"
                  :style="{ height: `${flameDepth * ROW_H}px` }"
                  @mouseleave="hoverKey = null"
                >
                  <div
                    v-for="f in flameFrames"
                    :key="f.key"
                    class="pf-frame"
                    :class="{
                      'is-hover': hoverKey === f.key,
                      'is-dim': isDimmed(f),
                      'is-fade': isFaded(f),
                      'is-hit': isSearchHit(f)
                    }"
                    :style="{
                      left: `${f.x * 100}%`,
                      width: `${f.w * 100}%`,
                      top: `${f.depth * ROW_H}px`,
                      background: FLAME_COLORS[f.depth % FLAME_COLORS.length]
                    }"
                    :title="frameTitle(f)"
                    @mouseenter="hoverKey = f.key"
                    @click="zoomInto(f.node)"
                  >
                    <span v-if="f.w * 100 >= 1.8" class="pf-frame__text">{{ f.node.name }}</span>
                  </div>
                </div>
                <div class="pf-flame-legend">
                  <span class="pf-flame-legend__gradient" />
                  <span class="text-[11px] text-g-600">
                    颜色随调用深度加深 · 帧宽 = 采样值占比 · 悬停高亮调用链 · 点击下钻 · 最深 48 层
                  </span>
                  <span v-if="searchHits" class="pf-flame-legend__hit">
                    <ArtSvgIcon icon="ri:search-line" />
                    命中 {{ searchHits }} 帧
                  </span>
                </div>
              </template>

              <!-- 空态：无采样 -->
              <div v-else-if="!flameLoading && flameData" class="pf-panel-empty">
                <ArtSvgIcon icon="ri:file-list-3-line" />
                <span>该 profile 当前没有采样数据</span>
                <span
                  v-if="['block', 'mutex'].includes(selectedProfile)"
                  class="pf-panel-empty__hint"
                >
                  需在服务端开启采样率：runtime.SetBlockProfileRate(1) /
                  runtime.SetMutexProfileFraction(1)
                </span>
                <span v-else class="pf-panel-empty__hint"
                  >采集端点为锁定状态或进程尚无对应事件，可稍后重新解析</span
                >
              </div>

              <!-- 空态：未加载 -->
              <div v-else-if="!flameLoading && !flameData && isCapture" class="pf-panel-empty">
                <ArtSvgIcon icon="ri:download-2-line" />
                <span>{{ selectedProfile }} 为按需采集类型，不提供在线火焰图</span>
                <span class="pf-panel-empty__hint">
                  请使用右侧「采集与下载」按需采集；原始数据可交给 go tool pprof / go tool trace
                  离线分析
                </span>
              </div>

              <div v-else-if="!flameLoading && !flameData" class="pf-panel-empty">
                <ArtSvgIcon icon="ri:fire-line" />
                <span>尚未加载火焰图</span>
                <span class="pf-panel-empty__hint"
                  >启用采集后自动解析 {{ profileLabel }}，或点击重新解析</span
                >
              </div>
            </div>

            <!-- 锁定遮罩 -->
            <div v-if="status && !enabled" class="pf-lock-mask">
              <div class="pf-lock-mask__icon flex-cc">
                <ArtSvgIcon icon="ri:lock-2-line" />
              </div>
              <div class="pf-lock-mask__title">采集已停用，火焰图不可见</div>
              <div class="pf-lock-mask__sub">启用后即可采样查看调用栈热点与函数级分布</div>
              <button type="button" class="pf-lock-mask__cta" :disabled="busy" @click="enable">
                <ArtSvgIcon icon="ri:play-circle-line" />
                立即启用
              </button>
            </div>
          </div>
        </div>

        <!-- ── 右列：热点函数 + 采集下载 + 分析提示 ── -->
        <div class="flex min-w-0 flex-col gap-4 lg:col-span-2">
          <!-- 热点函数 -->
          <div class="pf-card !p-4">
            <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
              <div class="flex items-center gap-2.5">
                <span class="pf-chip flex-cc" style="color: #ec4899; background: #ec489914">
                  <ArtSvgIcon icon="ri:line-chart-line" />
                </span>
                <div>
                  <div class="text-sm font-semibold text-[var(--el-text-color-primary)]"
                    >热点函数 Top {{ topN }}</div
                  >
                  <div class="pf-card__sub">flat 自身采样值 · 点击行定位火焰图 · 列表内滚动</div>
                </div>
              </div>
              <div
                class="pf-seg flex items-center gap-0.5"
                role="radiogroup"
                aria-label="热点函数数量"
              >
                <button
                  v-for="n in [10, 15, 25]"
                  :key="n"
                  type="button"
                  class="pf-seg__item"
                  :class="{ 'is-active': topN === n }"
                  :aria-pressed="topN === n"
                  @click="setTopN(n)"
                >
                  {{ n }}
                </button>
              </div>
            </div>

            <template v-if="topRows.length">
              <ul class="pf-top">
                <li
                  v-for="(r, i) in topRows"
                  :key="r.fn"
                  class="pf-top__row"
                  :class="{ 'is-hit': searchText && r.name.toLowerCase().includes(q) }"
                  :title="topRowTitle(r)"
                  @click="searchText = r.name"
                >
                  <span class="pf-top__rank" :class="{ 'is-top3': i < 3 }">{{ i + 1 }}</span>
                  <div class="min-w-0 flex-1">
                    <div class="flex items-baseline gap-2 overflow-hidden">
                      <span class="pf-top__name min-w-0 truncate font-mono">{{ r.name }}</span>
                      <span class="pf-top__loc truncate font-mono" style="max-width: 200px"
                        >{{ r.file }}:{{ r.line }}</span
                      >
                    </div>
                    <div class="pf-top__bars">
                      <span class="pf-top__track">
                        <span
                          class="pf-top__bar pf-top__bar--flat"
                          :style="{ width: flatPct(r) }"
                        />
                      </span>
                      <span class="pf-top__track pf-top__track--cum">
                        <span class="pf-top__bar pf-top__bar--cum" :style="{ width: cumPct(r) }" />
                      </span>
                    </div>
                  </div>
                  <div class="pf-top__value text-right tabular-nums">
                    <b>{{ fmtValue(r.flat) }}</b>
                    <span class="pf-top__pct">{{ pctText(r.flat) }}</span>
                  </div>
                </li>
              </ul>
            </template>
            <div v-else class="pf-panel-empty">
              <ArtSvgIcon icon="ri:line-chart-line" />
              <span>暂无热点函数数据</span>
            </div>
          </div>

          <!-- 采集与下载 -->
          <div class="pf-card !p-4">
            <div class="mb-3 flex items-center gap-2.5">
              <span class="pf-chip flex-cc" style="color: #0ea5e9; background: #0ea5e914">
                <ArtSvgIcon icon="ri:download-2-line" />
              </span>
              <div>
                <div class="text-sm font-semibold text-[var(--el-text-color-primary)]"
                  >采集与下载</div
                >
                <div class="pf-card__sub">原始数据可交给 go tool pprof 离线分析</div>
              </div>
            </div>

            <div class="pf-capture">
              <div class="pf-capture__row">
                <div class="pf-caption-line">
                  <div
                    class="flex items-center gap-1.5 whitespace-nowrap text-xs text-[var(--el-text-color-regular)]"
                  >
                    <ArtSvgIcon icon="ri:scan-2-line" class="shrink-0" style="color: #f97316" />
                    <b>CPU profile</b>· CPU 使用热点采样
                  </div>
                  <div class="pf-card__sub">服务端阻塞采集,期间请保持页面等待</div>
                </div>
                <div class="pf-actions">
                  <div class="pf-seg flex items-center gap-0.5">
                    <button
                      v-for="s in CPU_SECS"
                      :key="s"
                      type="button"
                      class="pf-seg__item"
                      :class="{ 'is-active': cpuSec === s }"
                      @click="cpuSec = s"
                    >
                      {{ s }}s
                    </button>
                  </div>
                  <button
                    type="button"
                    class="pf-capture__btn"
                    :class="{ 'is-busy': capturing === 'profile' }"
                    :disabled="!enabled || !!capturing"
                    @click="capture('profile')"
                  >
                    <ArtSvgIcon
                      :icon="capturing === 'profile' ? 'ri:loader-4-line' : 'ri:play-circle-line'"
                      :class="{ 'animate-spin': capturing === 'profile' }"
                    />
                    {{
                      capturing === 'profile'
                        ? `采集中 ${captureElapsed}s / ${cpuSec}s`
                        : '采集并下载'
                    }}
                  </button>
                </div>
              </div>

              <div class="pf-capture__row">
                <div class="pf-caption-line">
                  <div
                    class="flex items-center gap-1.5 whitespace-nowrap text-xs text-[var(--el-text-color-regular)]"
                  >
                    <ArtSvgIcon icon="ri:pulse-line" class="shrink-0" style="color: #14b8a6" />
                    <b>trace</b>· Go 执行跟踪
                  </div>
                  <div class="pf-card__sub">go tool trace 分析调度与延迟</div>
                </div>
                <div class="pf-actions">
                  <div class="pf-seg flex items-center gap-0.5">
                    <button
                      v-for="s in TRACE_SECS"
                      :key="s"
                      type="button"
                      class="pf-seg__item"
                      :class="{ 'is-active': traceSec === s }"
                      @click="traceSec = s"
                    >
                      {{ s }}s
                    </button>
                  </div>
                  <button
                    type="button"
                    class="pf-capture__btn"
                    :class="{ 'is-busy': capturing === 'trace' }"
                    :disabled="!enabled || !!capturing"
                    @click="capture('trace')"
                  >
                    <ArtSvgIcon
                      :icon="capturing === 'trace' ? 'ri:loader-4-line' : 'ri:play-circle-line'"
                      :class="{ 'animate-spin': capturing === 'trace' }"
                    />
                    {{
                      capturing === 'trace'
                        ? `采集中 ${captureElapsed}s / ${traceSec}s`
                        : '采集并下载'
                    }}
                  </button>
                </div>
              </div>
            </div>

            <div class="pf-download-line">
              <div class="min-w-0 flex-1 truncate">
                <b class="text-xs">当前快照</b>
                <span class="pf-download-line__name font-mono">{{ selectedProfile }}</span>
                <span class="text-[11px] text-g-600">.pb.gz（gzip 压缩的 profile.proto）</span>
              </div>
              <button
                type="button"
                class="pf-download-line__btn"
                :disabled="!enabled || !isSnapshot"
                @click="downloadProfile"
              >
                <ArtSvgIcon icon="ri:file-download-line" />
                下载当前选中项
              </button>
            </div>
          </div>

          <!-- 离线分析提示 -->
          <div class="pf-card pf-note !p-4">
            <div
              class="flex items-center gap-2 text-xs font-semibold text-[var(--el-text-color-regular)]"
            >
              <ArtSvgIcon icon="ri:terminal-box-line" style="color: #7c3aed" />
              下载后离线分析
            </div>
            <pre class="pf-note__code font-mono">
go tool pprof -http=:8080 &lt;server 二进制&gt; {{ fileNameExample }}</pre>
            <div class="pf-note__hint">
              火焰图 / 热点函数为在线速览;CPU、trace 与各快照均可下载原始数据,用官方 pprof
              工具做深度分析。
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
  import FileSaver from 'file-saver'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import {
    disablePprof,
    downloadPprofRaw,
    enablePprof,
    fetchPprofFlame,
    fetchPprofStatus,
    type PprofFlameData,
    type PprofFlameNode,
    type PprofProfileEntry,
    type PprofStatus,
    type PprofTopFunc
  } from '../api'
  // 纯格式化 / 采样类型元数据(原散落在本文件,已抽出并配套单测)
  import {
    fmtClock,
    fmtDur,
    fmtTime,
    fmtValue,
    profileIcon,
    profileMetaOf,
    stamp
  } from '../composables/use-pprof-format'

  defineOptions({ name: 'MonitorPprof' })

  const PREFIX = import.meta.env.VITE_API_PREFIX

  // ---------- 常量 ----------
  const ROW_H = 22
  const CPU_SECS = [10, 30]
  const TRACE_SECS = [5, 10]
  const FLAME_COLORS = [
    '#fbbf24',
    '#f59e0b',
    '#fb923c',
    '#f87171',
    '#ef4444',
    '#ec4899',
    '#c084fc',
    '#a855f7',
    '#8b5cf6',
    '#6366f1',
    '#3b82f6',
    '#0ea5e9'
  ]

  // ---------- profile 元信息（UI 层独立维护，与后端 meta 对齐） ----------

  // ---------- 状态 ----------
  const status = ref<PprofStatus | null>(null)
  const busy = ref(false)
  const updatedAt = ref<Date | null>(null)

  const selectedProfile = ref('heap')
  const flameData = ref<PprofFlameData | null>(null)
  const flameLoading = ref(false)
  const autoRefresh = ref(false)
  const topN = ref(15)

  const searchText = ref('')
  const zoomStack = ref<PprofFlameNode[]>([])
  const hoverKey = ref<number | null>(null)

  const capturing = ref<'profile' | 'trace' | null>(null)
  const captureElapsed = ref(0)
  const cpuSec = ref(10)
  const traceSec = ref(5)

  let statusTimer: number | undefined
  let flameTimer: number | undefined
  let captureTimer: number | undefined
  let autoOffDeadline = 0 // 本地倒计时截止时刻(ms)
  const tickNow = ref(Date.now())
  let flameSeq = 0

  // ---------- 派生状态 ----------
  const enabled = computed(() => status.value?.enabled ?? false)
  const autoOff = computed(() => status.value?.autoOffSeconds ?? 0)
  const profiles = computed<PprofProfileEntry[]>(() => status.value?.profiles ?? [])
  const snapshotTotal = computed(
    () => profiles.value.filter((p) => p.category === 'snapshot').length
  )
  const activeSnapshotCount = computed(
    () => profiles.value.filter((p) => p.category === 'snapshot' && p.count > 0).length
  )
  const isSnapshot = computed(() => {
    const p = profiles.value.find((x) => x.name === selectedProfile.value)
    return p?.category === 'snapshot'
  })
  const isCapture = computed(() => {
    const p = profiles.value.find((x) => x.name === selectedProfile.value)
    return p?.category === 'capture'
  })
  const profileLabel = computed(
    () =>
      profiles.value.find((p) => p.name === selectedProfile.value)?.description ??
      selectedProfile.value
  )
  const unitLabel = computed(() =>
    flameData.value?.unit === 'B' ? '内存' : flameData.value?.unit || ''
  )
  const flameTotalText = computed(() => {
    const d = flameData.value
    if (!d) return '-'
    return `${fmtValue(d.totalValue)}${d.unit === 'B' ? 'B' : d.unit === '次' ? ' 次' : ''}`
  })
  const flameTruncatedText = computed(() =>
    flameData.value?.truncated ? '深树已按显示上限裁切' : ''
  )
  const q = computed(() => searchText.value.trim().toLowerCase())
  const profileBaseUrl = `${PREFIX}/monitor/debug/pprof/`
  const fileNameExample = computed(() => `pprof_${selectedProfile.value}_${stamp()}.pb.gz`)

  const countdownText = computed(() => {
    if (!enabled.value || autoOff.value <= 0) return '—'
    if (!autoOffDeadline) return '排程中'
    // 剩余秒数恒 ≤ autoOffSeconds：clamp 防止首帧 tickNow 未就绪时出现天文数字
    const left = Math.min(
      Math.max(0, Math.ceil((autoOffDeadline - tickNow.value) / 1000)),
      autoOff.value
    )
    return fmtClock(left)
  })

  const statusSubText = computed(() => {
    if (!status.value) return '正在同步状态…'
    if (!enabled.value) return '采集已停用 · 端点锁定'
    const parts = [`已启用`]
    if (autoOff.value > 0) parts.push(`自动关闭剩余 ${countdownText.value}`)
    if (updatedAt.value) parts.push(`同步于 ${fmtTime(updatedAt.value)}`)
    return parts.join(' · ')
  })

  // ---------- 火焰图布局 ----------
  interface PpfFrame {
    key: number
    node: PprofFlameNode
    depth: number
    x: number
    w: number
    ancestors: Set<number>
  }

  const flameFrames = computed<PpfFrame[]>(() => {
    let keySeq = 0
    const root = zoomStack.value.length
      ? zoomStack.value[zoomStack.value.length - 1]
      : flameData.value?.flame
    if (!root || !root.value) return []
    const rows: PpfFrame[] = []
    const walk = (node: PprofFlameNode, depth: number, x: number, ancestorKeys: Set<number>) => {
      if (!flameData.value) return
      const w = node.value / flameData.value.totalValue
      const key = keySeq++
      const f: PpfFrame = { key, node, depth, x, w, ancestors: ancestorKeys }
      rows.push(f)
      if (node.children?.length) {
        const nextAncestors = new Set(ancestorKeys)
        nextAncestors.add(key)
        let cursor = x
        for (const c of node.children) {
          // 后端火焰树 children 可为 null 元素([]*FlameNode),跳过空子节点
          if (!c) continue
          walk(c, depth + 1, cursor, nextAncestors)
          cursor += c.value / flameData.value.totalValue
        }
      }
    }
    walk(root, 0, 0, new Set<number>())
    return rows
  })

  const flameDepth = computed(() => {
    let max = 0
    for (const f of flameFrames.value) max = Math.max(max, f.depth + 1)
    return max
  })

  const searchHits = computed(() =>
    q.value
      ? flameFrames.value.filter((f) => f.node.name.toLowerCase().includes(q.value)).length
      : 0
  )

  function isDimmed(f: PpfFrame): boolean {
    if (hoverKey.value == null) return false
    if (hoverKey.value === f.key) return false
    return !f.ancestors.has(hoverKey.value)
  }

  function isFaded(f: PpfFrame): boolean {
    return !!q.value && !f.node.name.toLowerCase().includes(q.value)
  }

  function isSearchHit(f: PpfFrame): boolean {
    return !!q.value && f.node.name.toLowerCase().includes(q.value)
  }

  function frameTitle(f: PpfFrame): string {
    const pct = (f.w * 100).toFixed(2)
    return `${f.node.name}\n${fmtValue(f.node.value)} · ${pct}%${flameData.value?.unit === 'B' ? ' · ' + fmtValue(f.node.value) : ''}`
  }

  function zoomInto(node: PprofFlameNode) {
    const cur = zoomStack.value.length
      ? zoomStack.value[zoomStack.value.length - 1]
      : flameData.value?.flame
    if (cur === node) return
    zoomStack.value.push(node)
  }

  function resetZoom() {
    zoomStack.value = []
  }

  function zoomTo(index: number) {
    zoomStack.value = zoomStack.value.slice(0, index + 1)
  }

  // ---------- Top 热点函数 ----------
  const topRows = computed<PprofTopFunc[]>(() => flameData.value?.top ?? [])

  const maxFlat = computed(() => {
    let m = 1
    for (const r of topRows.value) m = Math.max(m, r.flat)
    return m
  })
  const maxCum = computed(() => {
    let m = 1
    for (const r of topRows.value) m = Math.max(m, r.cum)
    return m
  })

  function flatPct(r: PprofTopFunc): string {
    return `${((r.flat / maxFlat.value) * 100).toFixed(1)}%`
  }

  function cumPct(r: PprofTopFunc): string {
    return `${((r.cum / maxCum.value) * 100).toFixed(1)}%`
  }

  function pctText(v: number): string {
    const d = flameData.value
    if (!d || !d.totalValue) return ''
    return `${((v / d.totalValue) * 100).toFixed(1)}%`
  }

  function topRowTitle(r: PprofTopFunc): string {
    return `${r.fn}\n${r.file}:${r.line}\nflat ${fmtValue(r.flat)} · cum ${fmtValue(r.cum)} · 点击定位火焰图`
  }

  // ---------- 格式化 ----------

  // ---------- profile 选择器 ----------

  function chipStyle(p: PprofProfileEntry): Record<string, string> {
    const { color } = profileMetaOf(p.name)
    return selectedProfile.value === p.name
      ? { color, background: `${color}14`, borderColor: `${color}55` }
      : {}
  }

  function chipTitle(p: PprofProfileEntry): string {
    const extra = p.category === 'snapshot' ? ` · ${p.count} 条采样` : ' · 按需采集'
    return `${p.name}${extra}：${p.description}`
  }

  function selectProfile(name: string) {
    if (!enabled.value && name !== selectedProfile.value) return
    if (selectedProfile.value === name) return
    selectedProfile.value = name
    zoomStack.value = []
    searchText.value = ''
    loadFlame(true)
  }

  // ---------- 数据加载 ----------
  async function loadStatus(): Promise<void> {
    try {
      const s = await fetchPprofStatus()
      if (s.enabled && s.remainingSeconds > 0) {
        autoOffDeadline = Date.now() + s.remainingSeconds * 1000
      } else {
        autoOffDeadline = 0
      }
      tickNow.value = Date.now() // 与 deadline 同步,避免首帧按旧时钟计算出现天文数字
      status.value = s
      updatedAt.value = new Date()
    } catch {
      /* 静默:状态轮询失败不打扰,下次轮询自愈 */
    }
  }

  async function loadFlame(silent = false): Promise<void> {
    // 采集类(profile/trace)无快照数据,不请求火焰图接口,直接进入引导空态
    if (isCapture.value) {
      flameData.value = null
      zoomStack.value = []
      flameLoading.value = false
      return
    }
    const seq = ++flameSeq
    if (!silent) flameLoading.value = true
    try {
      const data = await fetchPprofFlame(selectedProfile.value, topN.value)
      if (seq !== flameSeq) return
      flameData.value = data
      if (zoomStack.value.length) zoomStack.value = []
    } catch {
      if (seq !== flameSeq) return
      if (!silent) ElMessage.error(`火焰图解析失败，请确认已启用采集`)
    } finally {
      if (seq === flameSeq) flameLoading.value = false
    }
  }

  async function refreshAll(silent = false): Promise<void> {
    await Promise.all([loadStatus(), loadFlame(true)])
    if (!silent) ElMessage.success('状态已刷新')
  }

  function setTopN(n: number) {
    if (topN.value === n) return
    topN.value = n
    loadFlame(true)
  }

  // ---------- 启停控制 ----------
  async function toggleEnabled(): Promise<void> {
    if (!status.value) return
    if (status.value.enabled) {
      await disable()
    } else {
      await enable()
    }
  }

  async function enable(): Promise<void> {
    if (busy.value) return
    busy.value = true
    try {
      await enablePprof()
      autoOffDeadline = 0
      await loadStatus()
      ElMessage.success(
        autoOff.value > 0 ? `pprof 已启用 · ${fmtDur(autoOff.value)} 后自动停用` : 'pprof 已启用'
      )
      loadFlame(true)
    } catch {
      // 错误已由 http 层统一提示
    } finally {
      busy.value = false
    }
  }

  async function disable(): Promise<void> {
    if (busy.value) return
    busy.value = true
    try {
      await disablePprof()
      autoOffDeadline = 0
      await loadStatus()
      ElMessage.success('pprof 已停用，数据端点已锁定')
    } catch {
      // 错误已由 http 层统一提示
    } finally {
      busy.value = false
    }
  }

  // ---------- 下载 ----------
  async function downloadProfile(): Promise<void> {
    const name = selectedProfile.value
    if (name === 'profile' || name === 'trace') {
      ElMessage.warning('CPU profile / trace 需在右侧「采集与下载」按需采集')
      return
    }
    await doDownload(name, {})
  }

  async function doDownload(name: string, params: Record<string, string | number>): Promise<void> {
    try {
      const blob = await downloadPprofRaw(name, params)
      const ext = name === 'trace' ? '.trace' : '.pb.gz'
      FileSaver.saveAs(blob, `pprof_${name}_${stamp()}${ext}`)
      ElMessage.success(`已下载 ${name} 原始数据`)
    } catch {
      // 错误已由 http 层统一提示
    }
  }

  async function capture(kind: 'profile' | 'trace'): Promise<void> {
    if (!enabled.value || capturing.value) return
    const sec = kind === 'profile' ? cpuSec.value : traceSec.value
    capturing.value = kind
    captureElapsed.value = 0
    captureTimer = window.setInterval(() => {
      captureElapsed.value += 1
    }, 1000)
    try {
      await doDownload(kind === 'profile' ? 'profile' : 'trace', { seconds: sec })
    } finally {
      if (captureTimer) clearInterval(captureTimer)
      captureTimer = undefined
      capturing.value = null
    }
  }

  function copyEndpoint() {
    const url = `${window.location.origin}${profileBaseUrl}`
    try {
      navigator.clipboard.writeText(url).then(() => ElMessage.success('已复制端点地址'))
    } catch {
      ElMessage.error('复制失败，请手动复制')
    }
  }

  // ---------- 轮询与生命周期 ----------
  watch(
    [autoRefresh, enabled],
    ([on, en]) => {
      if (flameTimer) {
        clearInterval(flameTimer)
        flameTimer = undefined
      }
      if (on && en) {
        flameTimer = window.setInterval(() => {
          if (!document.hidden) loadFlame(true)
        }, 10_000)
      }
    },
    { immediate: true }
  )

  watch(enabled, (en, prev) => {
    if (en && prev === false) {
      loadFlame(false)
    }
  })

  onBeforeUnmount(() => {
    if (statusTimer) clearInterval(statusTimer)
    if (flameTimer) clearInterval(flameTimer)
    if (captureTimer) clearInterval(captureTimer)
  })

  // 状态轮询:1s 驱动倒计时与开关联动;隐藏时暂停
  statusTimer = window.setInterval(() => {
    if (document.hidden) return
    tickNow.value = Date.now()
    loadStatus()
  }, 1000)

  // 首屏加载
  loadStatus().then(() => {
    if (enabled.value) loadFlame(false)
  })
</script>

<style scoped lang="scss">
  /* 四视图共享的设计令牌(卡片 / KPI / 呼吸圆点 / 入场动画) */
  @use './monitor-tokens' as t;

  @include t.rise-keyframes;
  @include t.pulse-keyframes;

  /* ---------- 基础卡片 ---------- */
  .pf-card {
    @include t.card;
    /* 本视图卡片不可交互,故无 hover 抬起所需的 transition */
    @include t.rise;
  }

  .dark .pf-card {
    @include t.card-dark;
  }

  /* ---------- 页头 ---------- */
  .pf-hero__icon {
    width: 44px;
    height: 44px;
    flex: none;
    border-radius: 14px;
    font-size: 22px;
    color: #fff;
    background: linear-gradient(135deg, #f97316 0%, #ec4899 100%);
    box-shadow: 0 4px 12px rgba(249, 115, 22, 0.35);
  }

  .live-dot {
    @include t.live-dot;
  }

  .pf-toggle {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    height: 36px;
    padding: 0 16px;
    border-radius: 10px;
    border: 1px solid rgba(16, 185, 129, 0.4);
    background: rgba(16, 185, 129, 0.1);
    color: #059669;
    font-size: 13px;
    font-weight: 600;
    cursor: pointer;
    transition: all 0.2s ease;
  }

  .pf-toggle:hover {
    background: rgba(16, 185, 129, 0.18);
    border-color: rgba(16, 185, 129, 0.7);
  }

  .pf-toggle.is-on {
    border-color: rgba(220, 38, 38, 0.4);
    background: rgba(220, 38, 38, 0.08);
    color: #dc2626;
  }

  .pf-toggle.is-on:hover {
    background: rgba(220, 38, 38, 0.14);
    border-color: rgba(220, 38, 38, 0.7);
  }

  .dark .pf-toggle {
    color: #34d399;
  }
  .dark .pf-toggle.is-on {
    color: #f87171;
  }

  .pf-toggle__dot {
    width: 7px;
    height: 7px;
    border-radius: 999px;
    background: currentColor;
    box-shadow: 0 0 0 0 rgba(16, 185, 129, 0.5);
    /* 复用共享呼吸动画(原 pf-pulse 已随本次收敛删除) */
    @include t.live-dot;
  }

  .pf-toggle.is-on .pf-toggle__dot {
    box-shadow: 0 0 0 0 rgba(220, 38, 38, 0.5);
  }

  /* ---------- 控制条 ---------- */
  .pf-ctl__badge {
    width: 40px;
    height: 40px;
    flex: none;
    border-radius: 12px;
    font-size: 19px;
  }

  .pf-ctl__badge.is-on {
    color: #059669;
    background: rgba(16, 185, 129, 0.12);
  }

  .pf-ctl__badge.is-off {
    color: #94a3b8;
    background: var(--art-gray-100);
  }

  .pf-ctl__chip {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding: 5px 11px;
    border-radius: 999px;
    border: 1px solid var(--default-border);
    background: var(--art-gray-100);
    color: var(--el-text-color-secondary);
    font-size: 12px;
    white-space: nowrap;
  }

  .pf-ctl__chip b {
    color: var(--el-text-color-primary);
    font-variant-numeric: tabular-nums;
  }

  .pf-ctl__chip.is-live {
    border-color: rgba(249, 115, 22, 0.45);
    background: rgba(249, 115, 22, 0.1);
    color: #ea580c;
  }

  .pf-ctl__chip.is-live b {
    color: #ea580c;
    font-size: 13px;
  }

  .pf-ctl__chip--url {
    max-width: 360px;
  }

  .pf-ctl__chip--url :deep(svg) {
    flex: none;
  }

  .pf-ctl__copy {
    margin-left: 2px;
    padding: 1px 8px;
    border-radius: 999px;
    background: var(--el-color-primary-light-9);
    color: var(--el-color-primary);
    cursor: pointer;
    transition: background 0.15s ease;
  }

  .pf-ctl__copy:hover {
    background: var(--el-color-primary-light-8);
  }

  /* ---------- KPI 磁贴 ---------- */
  .kpi-tile {
    display: flex;
    align-items: center;
    gap: 0.75rem;
  }

  .kpi-tile:hover {
    transform: translateY(-2px);
    box-shadow: 0 8px 18px rgba(16, 24, 40, 0.09);
    border-color: var(--el-color-primary-light-5);
    transition: all 0.2s ease;
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

  /* ---------- 卡片头 ---------- */
  .pf-chip {
    width: 34px;
    height: 34px;
    flex: none;
    border-radius: 10px;
    font-size: 17px;
  }

  .pf-card__sub {
    @include t.card-sub;
  }

  .pf-summary-pill {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 3px 10px;
    border-radius: 999px;
    border: 1px solid rgba(234, 179, 8, 0.4);
    background: rgba(234, 179, 8, 0.1);
    color: #b45309;
    font-size: 11.5px;
  }

  /* ---------- profile 选择 chips ---------- */
  .pf-profile-chip {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 5px 12px;
    border-radius: 999px;
    border: 1px solid var(--default-border);
    background: var(--art-gray-100);
    color: var(--el-text-color-regular);
    font-size: 12px;
    line-height: 18px;
    cursor: pointer;
    transition:
      background 0.15s ease,
      border-color 0.15s ease,
      color 0.15s ease;
  }

  .pf-profile-chip:hover {
    color: var(--el-text-color-primary);
    border-color: var(--el-color-primary-light-5);
  }

  .pf-profile-chip.is-active {
    font-weight: 600;
  }

  .pf-profile-chip.is-disabled {
    opacity: 0.55;
    cursor: not-allowed;
  }

  .pf-profile-chip__count {
    padding: 0 6px;
    border-radius: 999px;
    background: var(--default-box-color);
    font-size: 11px;
    font-variant-numeric: tabular-nums;
  }

  /* ---------- 搜索 / 分段 ---------- */
  .pf-search {
    width: 168px;
  }

  .pf-search :deep(.el-input__wrapper) {
    border-radius: 999px;
  }

  .pf-seg {
    padding: 2px;
    border-radius: 10px;
    border: 1px solid var(--default-border);
    background: var(--art-gray-100);
  }

  .pf-seg__item {
    padding: 3px 10px;
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

  .pf-seg__item:hover {
    color: var(--el-text-color-primary);
  }

  .pf-seg__item.is-active {
    background: var(--default-box-color);
    color: var(--el-color-primary);
    font-weight: 600;
    box-shadow: 0 1px 2px rgba(16, 24, 40, 0.08);
  }

  /* ---------- 面包屑 ---------- */
  .pf-breadcrumb__chip {
    max-width: 260px;
    padding: 3px 10px;
    border-radius: 999px;
    border: 1px solid var(--default-border);
    background: var(--art-gray-100);
    color: var(--el-text-color-secondary);
    font-size: 11.5px;
    cursor: pointer;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .pf-breadcrumb__chip:hover {
    color: var(--el-text-color-primary);
    border-color: var(--el-color-primary-light-5);
  }

  .pf-breadcrumb__chip.is-last {
    color: var(--el-color-primary);
    border-color: var(--el-color-primary-light-5);
    background: var(--el-color-primary-light-9);
    font-weight: 600;
  }

  /* ---------- 火焰图 ---------- */
  .pf-flame-card {
    overflow: visible;
  }

  .pf-flame-body {
    position: relative;
    min-height: 88px;
    max-height: 560px;
    overflow: auto;
    overflow-x: hidden;
    scrollbar-width: thin;
  }

  .pf-flame-body::-webkit-scrollbar {
    width: 8px;
    height: 8px;
  }

  .pf-flame-body::-webkit-scrollbar-thumb {
    border-radius: 999px;
    background: var(--default-border);
  }

  .pf-flame {
    position: relative;
    width: 100%;
  }

  .pf-frame {
    position: absolute;
    box-sizing: border-box;
    height: 21px;
    padding: 0;
    border-radius: 3px;
    cursor: pointer;
    overflow: hidden;
    transition:
      opacity 0.15s ease,
      filter 0.15s ease;
    box-shadow: inset 0 0 0 0.5px rgba(255, 255, 255, 0.35);
  }

  .pf-frame.is-dim {
    opacity: 0.32;
  }

  .pf-frame.is-fade {
    opacity: 0.1;
  }

  .pf-frame.is-hover {
    opacity: 1 !important;
    filter: brightness(1.18);
    box-shadow: inset 0 0 0 1.5px rgba(255, 255, 255, 0.9);
    z-index: 2;
  }

  .pf-frame.is-hit {
    box-shadow: inset 0 0 0 1.5px #06b6d4;
  }

  .pf-frame__text {
    display: block;
    width: 100%;
    font-size: 11px;
    line-height: 21px;
    color: #fff;
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, 'Fira Code', monospace;
    text-indent: 3px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    text-shadow: 0 1px 1px rgba(0, 0, 0, 0.3);
    user-select: none;
    pointer-events: none;
  }

  .pf-flame-legend {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: 8px;
    padding-top: 8px;
    border-top: 1px dashed var(--default-border);
  }

  .pf-flame-legend__gradient {
    width: 96px;
    height: 8px;
    flex: none;
    border-radius: 999px;
    background: linear-gradient(
      90deg,
      #fbbf24,
      #f59e0b,
      #fb923c,
      #f87171,
      #ef4444,
      #ec4899,
      #a855f7,
      #6366f1
    );
  }

  .pf-flame-legend__hit {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    margin-left: auto;
    padding: 2px 9px;
    border-radius: 999px;
    background: rgba(6, 182, 212, 0.12);
    color: #0891b2;
    font-size: 11.5px;
  }

  /* ---------- 空态 / 锁定遮罩 ---------- */
  .pf-panel-empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 6px;
    min-height: 120px;
    padding: 20px;
    color: var(--el-text-color-secondary);
    font-size: 12px;
  }

  .pf-panel-empty > :deep(svg) {
    font-size: 26px;
    color: var(--el-color-primary-light-5);
  }

  .pf-panel-empty__hint {
    max-width: 420px;
    text-align: center;
    font-size: 11px;
    color: var(--el-text-color-secondary);
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, 'Fira Code', monospace;
  }

  .pf-lock-mask {
    position: absolute;
    inset: 0;
    z-index: 5;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 8px;
    border-radius: 14px;
    background: color-mix(in srgb, var(--default-box-color) 82%, transparent);
    backdrop-filter: blur(2px);
  }

  .pf-lock-mask__icon {
    width: 56px;
    height: 56px;
    border-radius: 18px;
    font-size: 26px;
    color: #64748b;
    background: var(--el-fill-color-light);
  }

  .pf-lock-mask__title {
    font-size: 14px;
    font-weight: 600;
    color: var(--el-text-color-primary);
  }

  .pf-lock-mask__sub {
    font-size: 12px;
    color: var(--el-text-color-secondary);
  }

  .pf-lock-mask__cta {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    margin-top: 4px;
    height: 34px;
    padding: 0 18px;
    border-radius: 10px;
    border: none;
    background: var(--el-color-primary);
    color: #fff;
    font-size: 13px;
    font-weight: 600;
    cursor: pointer;
    transition: background 0.2s ease;
  }

  .pf-lock-mask__cta:hover {
    background: var(--el-color-primary-light-3);
  }

  /* ---------- Top 热点函数 ---------- */
  .pf-top {
    display: flex;
    flex-direction: column;
    gap: 2px;
    max-height: 560px;
    overflow-y: auto;
    padding-right: 4px;
    scrollbar-width: thin;
  }

  .pf-top::-webkit-scrollbar {
    width: 6px;
  }

  .pf-top::-webkit-scrollbar-thumb {
    border-radius: 999px;
    background: var(--default-border);
  }

  .pf-top::-webkit-scrollbar-track {
    background: transparent;
  }

  .pf-top__row {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 7px 8px;
    border-radius: 10px;
    cursor: pointer;
    transition: background 0.15s ease;
  }

  .pf-top__row:hover {
    background: var(--art-hover-color);
  }

  .pf-top__row.is-hit {
    background: rgba(6, 182, 212, 0.08);
  }

  .pf-top__rank {
    width: 22px;
    height: 22px;
    flex: none;
    display: flex;
    align-items: center;
    justify-content: center;
    border-radius: 7px;
    background: var(--el-fill-color-light);
    color: var(--el-text-color-secondary);
    font-size: 11px;
    font-weight: 600;
    font-variant-numeric: tabular-nums;
  }

  .pf-top__rank.is-top3 {
    color: #fff;
    background: var(--el-color-primary);
  }

  .pf-top__name {
    font-size: 12px;
    color: var(--el-text-color-primary);
    font-weight: 600;
  }

  .pf-top__loc {
    font-size: 10.5px;
    color: var(--el-text-color-secondary);
  }

  .pf-top__bars {
    display: flex;
    flex-direction: column;
    gap: 2px;
    margin-top: 4px;
  }

  .pf-top__track {
    display: block;
    height: 4px;
    border-radius: 999px;
    background: var(--el-fill-color-light);
    overflow: hidden;
  }

  .pf-top__bar {
    display: block;
    height: 100%;
    border-radius: 999px;
    transition: width 0.3s ease;
  }

  .pf-top__bar--flat {
    background: linear-gradient(90deg, #ec4899, #f97316);
  }

  .pf-top__bar--cum {
    background: linear-gradient(90deg, #6366f1, #3b82f6);
  }

  .pf-top__value {
    flex: none;
    width: 72px;
    font-size: 12px;
    color: var(--el-text-color-primary);
  }

  .pf-top__value b {
    font-weight: 650;
  }

  .pf-top__pct {
    display: block;
    margin-top: 1px;
    font-size: 10.5px;
    color: var(--el-text-color-secondary);
    font-variant-numeric: tabular-nums;
  }

  /* ---------- 采集与下载 ---------- */
  .pf-capture {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .pf-capture__row {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 10px;
    border-radius: 12px;
    background: var(--art-gray-100);
  }

  .pf-actions {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-wrap: wrap;
  }

  .pf-capture__btn {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    height: 30px;
    padding: 0 13px;
    border-radius: 9px;
    border: 1px solid var(--el-color-primary-light-5);
    background: var(--el-color-primary-light-9);
    color: var(--el-color-primary);
    font-size: 12px;
    font-weight: 600;
    cursor: pointer;
    transition: all 0.2s ease;
  }

  .pf-capture__btn:hover:not(:disabled) {
    background: var(--el-color-primary-light-7);
  }

  .pf-capture__btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .pf-capture__btn.is-busy {
    border-color: rgba(249, 115, 22, 0.5);
    background: rgba(249, 115, 22, 0.12);
    color: #ea580c;
  }

  .pf-download-line {
    display: flex;
    align-items: center;
    gap: 10px;
    margin-top: 12px;
    padding-top: 12px;
    border-top: 1px dashed var(--default-border);
  }

  .pf-download-line__name {
    color: var(--el-color-primary);
    padding: 0 4px;
  }

  .pf-download-line__btn {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    flex: none;
    height: 30px;
    padding: 0 13px;
    border-radius: 9px;
    border: 1px solid var(--default-border);
    background: var(--default-box-color);
    color: var(--el-text-color-regular);
    font-size: 12px;
    cursor: pointer;
    transition: all 0.2s ease;
  }

  .pf-download-line__btn:hover:not(:disabled) {
    color: var(--el-color-primary);
    border-color: var(--el-color-primary-light-5);
  }

  .pf-download-line__btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  /* ---------- 离线分析提示 ---------- */
  .pf-note__code {
    margin: 8px 0 0;
    padding: 10px 12px;
    border-radius: 10px;
    background: var(--art-gray-100);
    color: var(--el-text-color-primary);
    font-size: 11.5px;
    line-height: 1.6;
    overflow-x: auto;
    white-space: pre-wrap;
    word-break: break-all;
  }

  .pf-note__hint {
    margin-top: 8px;
    font-size: 11px;
    line-height: 1.6;
    color: var(--el-text-color-secondary);
  }

  /* 动画停用收敛到共享 mixin(其余视图曾各自重写同一媒体查询) */
  @include t.reduced-motion('.pf-card', '.live-dot', '.pf-toggle__dot');
  @media (prefers-reduced-motion: reduce) {
    .pf-toggle,
    .pf-ctl__copy,
    .kpi-tile,
    .pf-profile-chip,
    .pf-seg__item,
    .pf-frame,
    .pf-lock-mask__cta,
    .pf-top__row,
    .pf-top__bar,
    .pf-capture__btn,
    .pf-download-line__btn,
    .pf-note__hint {
      transition: none;
    }
  }
</style>
