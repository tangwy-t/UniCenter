<template>
  <div class="cache-page art-full-height overflow-y-auto">
    <div class="cache-page__inner p-4 pb-8 md:p-5">
      <!-- ============ 页头：标题 + 实时状态 + 自动刷新 ============ -->
      <div class="cache-hero mb-4 flex flex-wrap items-center gap-3">
        <div class="cache-hero__icon flex-cc">
          <ArtSvgIcon icon="ri:database-2-line" />
        </div>
        <div class="cache-hero__text min-w-0">
          <h2 class="text-lg font-semibold text-[var(--el-text-color-primary)]">缓存管理</h2>
          <p class="mt-0.5 flex items-center gap-1.5 text-xs text-g-600">
            <span
              class="live-dot inline-block h-1.5 w-1.5 rounded-full bg-success"
              :class="{ 'is-loading': loading }"
            />
            {{ loading || scanning ? '正在同步键空间…' : `已同步 · ${updatedAtText}` }}
            <template v-if="autoRefresh">· 10s 自动刷新</template>
          </p>
        </div>
        <div class="ml-auto flex items-center gap-1">
          <div class="relative">
            <ArtButtonTable
              :icon="'ri:timer-2-line'"
              :iconClass="autoRefresh ? 'bg-theme text-white shadow-sm' : 'bg-theme/12 text-theme'"
              :title="autoRefresh ? '关闭自动刷新 (10s)' : '开启自动刷新 (10s)'"
              @click="toggleAutoRefresh"
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
            @click="loadKeys"
          />
        </div>
      </div>

      <!-- ============ KPI 数据磁贴 ============ -->
      <div class="kpi-grid grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <div class="cache-card kpi-tile">
          <div class="kpi-tile__icon flex-cc" style="--tile: #3b82f6; --tile2: #60a5fa">
            <ArtSvgIcon icon="ri:key-2-line" />
          </div>
          <div class="min-w-0">
            <div class="kpi-tile__value">{{ totalCount }}</div>
            <div class="kpi-tile__label">键总数</div>
            <div class="kpi-tile__sub">{{
              truncated ? '已达展示上限，建议前缀过滤' : '当前过滤条件下已加载'
            }}</div>
          </div>
        </div>
        <div class="cache-card kpi-tile">
          <div class="kpi-tile__icon flex-cc" style="--tile: #7c3aed; --tile2: #a78bfa">
            <ArtSvgIcon icon="ri:apps-2-line" />
          </div>
          <div class="min-w-0">
            <div class="kpi-tile__value">{{ groups.length }}</div>
            <div class="kpi-tile__label">值类型</div>
            <div class="kpi-tile__sub truncate">{{ topTypesText || '暂无数据' }}</div>
          </div>
        </div>
        <div class="cache-card kpi-tile">
          <div class="kpi-tile__icon flex-cc" style="--tile: #10b981; --tile2: #34d399">
            <ArtSvgIcon icon="ri:bar-chart-2-line" />
          </div>
          <div class="min-w-0">
            <div class="kpi-tile__value truncate" :title="topGroup?.label ?? '-'">
              {{ topGroup?.label ?? '-' }}
            </div>
            <div class="kpi-tile__label">最大分组</div>
            <div class="kpi-tile__sub">{{ topGroup ? `占比 ${topGroupShare}%` : '暂无数据' }}</div>
          </div>
        </div>
        <div class="cache-card kpi-tile">
          <div class="kpi-tile__icon flex-cc" style="--tile: #f59e0b; --tile2: #fbbf24">
            <ArtSvgIcon icon="ri:price-tag-line" />
          </div>
          <div class="min-w-0">
            <div class="kpi-tile__value">{{ namespaces.length }}</div>
            <div class="kpi-tile__label">命名空间</div>
            <div class="kpi-tile__sub">按 key 前缀归纳（含根）</div>
          </div>
        </div>
      </div>

      <!-- ============ 查询 / 删除工具栏 ============ -->
      <div class="cache-card mt-4 flex flex-wrap items-center gap-3 !p-3.5">
        <ElInput
          v-model="prefix"
          class="cache-prefix-input"
          placeholder="按 key 前缀过滤（留空查看全部）"
          clearable
          @keyup.enter="handleSearch"
        >
          <template #prefix>
            <ArtSvgIcon icon="ri:search-line" class="text-g-500" />
          </template>
        </ElInput>
        <div class="flex items-center">
          <ArtButtonTable
            icon="ri:search-line"
            iconClass="bg-theme/12 text-theme"
            title="查询"
            @click="handleSearch"
          />
          <ArtButtonTable
            icon="ri:delete-bin-5-line"
            iconClass="bg-danger/12 text-danger"
            title="按前缀批量删除"
            @click="deleteByPrefix"
          />
        </div>
        <div class="ml-auto flex items-center gap-2 text-xs text-g-600">
          <span v-if="truncated" class="cache-limit-chip">
            <ArtSvgIcon icon="ri:error-warning-line" />
            已加载展示上限，建议使用前缀过滤
          </span>
          <span class="cache-summary-pill">
            <ArtSvgIcon v-if="scanning" icon="ri:loader-4-line" class="animate-spin" />
            <template v-if="scanning">扫描中 · 已发现 {{ scanned }}</template>
            <template v-else>{{ totalCount }} 个 key</template>
          </span>
        </div>
      </div>

      <!-- ============ 主内容：键空间浏览 + 类型分布 / 命名空间 ============ -->
      <div class="mt-4 grid grid-cols-1 gap-4 lg:grid-cols-5">
        <!-- 左：键空间浏览器 -->
        <div
          class="cache-card cache-explorer-card flex min-h-100 flex-col !p-0 lg:col-span-3"
          :style="{ '--explorer-max': explorerMax }"
        >
          <div class="flex flex-wrap items-center gap-2 border-b-[var(--default-border)] px-4 py-3">
            <ArtSvgIcon icon="ri:list-check-3" class="text-lg text-theme" />
            <span class="text-sm font-semibold text-[var(--el-text-color-primary)]"
              >键空间浏览器</span
            >
            <div class="ml-auto flex flex-wrap items-center gap-1.5">
              <button
                class="cache-pill"
                :class="{ 'is-active': activeType === 'all' }"
                :style="
                  activeType === 'all'
                    ? { color: '#3b82f6', background: '#3b82f614', borderColor: '#3b82f655' }
                    : undefined
                "
                @click="activeType = 'all'"
              >
                全部 · {{ totalCount }}
              </button>
              <button
                v-for="g in groups"
                :key="g.type"
                class="cache-pill"
                :class="{ 'is-active': activeType === g.type }"
                :style="
                  activeType === g.type
                    ? { color: g.color, background: `${g.color}14`, borderColor: `${g.color}55` }
                    : undefined
                "
                @click="activeType = g.type"
              >
                <span class="cache-pill__dot" :style="{ background: g.color }" />
                {{ g.label }} · {{ g.keys.length }}
              </button>
            </div>
          </div>

          <div v-loading="loading" class="cache-explorer flex-1 overflow-y-auto min-h-0">
            <template v-if="visibleSections.length">
              <section v-for="g in visibleSections" :key="g.type" class="cache-section">
                <header
                  class="cache-section__head flex-cb"
                  :class="{ 'is-collapsed': collapsed.has(g.type) }"
                  @click="toggleSection(g.type)"
                >
                  <div class="flex items-center gap-2 min-w-0">
                    <span
                      class="cache-section__icon flex-cc"
                      :style="{ color: g.color, background: `${g.color}14` }"
                    >
                      <ArtSvgIcon :icon="g.icon" />
                    </span>
                    <span class="text-sm font-medium text-[var(--el-text-color-primary)]">{{
                      g.label
                    }}</span>
                    <span class="cache-section__count">{{ g.keys.length }}</span>
                  </div>
                  <div class="flex items-center gap-3">
                    <span class="cache-section__share" :style="{ color: g.color }"
                      >{{ shareOf(g) }}%</span
                    >
                    <ArtSvgIcon
                      icon="ri:arrow-down-s-line"
                      class="text-xs text-g-600 transition-transform duration-200"
                      :class="{ 'rotate-180': collapsed.has(g.type) }"
                    />
                  </div>
                </header>
                <div class="cache-collapse" :class="{ 'is-collapsed': collapsed.has(g.type) }">
                  <div class="cache-collapse__inner">
                    <div
                      v-for="k in g.keys"
                      :key="k"
                      class="cache-key-row group"
                      :title="k"
                      @click="showValue(k, g.type)"
                    >
                      <span class="cache-key-row__bar" :style="{ background: g.color }" />
                      <span class="cache-key-row__text min-w-0 truncate">{{ k }}</span>
                      <span class="cache-key-row__actions">
                        <span
                          class="cache-key-row__action flex-cc"
                          style="background: #3b82f614; color: #3b82f6"
                          title="查看值"
                          @click.stop="showValue(k, g.type)"
                        >
                          <ArtSvgIcon icon="ri:eye-line" />
                        </span>
                        <span
                          class="cache-key-row__action flex-cc"
                          style="background: #0ea5e914; color: #0ea5e9"
                          title="复制 key"
                          @click.stop="copyKey(k)"
                        >
                          <ArtSvgIcon icon="ri:file-copy-line" />
                        </span>
                      </span>
                    </div>
                  </div>
                </div>
              </section>
            </template>

            <div v-else-if="!loading" class="cache-empty flex-cc flex-col gap-3 py-14">
              <div class="cache-empty__icon flex-cc">
                <ArtSvgIcon icon="ri:file-list-3-line" />
              </div>
              <div class="text-sm font-medium text-[var(--el-text-color-regular)]">暂无缓存键</div>
              <div class="text-xs text-g-600">{{
                prefix ? '试试更换前缀或清空过滤条件' : 'Redis 键空间为空，可点击右上角刷新'
              }}</div>
              <div class="flex items-center">
                <ArtButtonTable
                  icon="ri:refresh-line"
                  iconClass="bg-theme/12 text-theme"
                  title="刷新"
                  @click="loadKeys"
                />
              </div>
            </div>
          </div>
        </div>

        <!-- 右：类型分布 + 命名空间 -->
        <div ref="rightColRef" class="flex flex-col gap-4 lg:col-span-2">
          <div class="cache-card !p-4">
            <div class="flex-cb mb-3">
              <span class="text-sm font-semibold text-[var(--el-text-color-primary)]"
                >类型分布</span
              >
              <span class="text-xs text-g-600">{{ totalCount }} 个 key · 100%</span>
            </div>

            <template v-if="distribution.length">
              <div
                class="cache-distribution-bar flex h-2.5 w-full overflow-hidden rounded-full"
                role="img"
                aria-label="缓存键类型分布"
              >
                <span
                  v-for="seg in distribution"
                  :key="seg.type"
                  class="h-full transition-all duration-300"
                  :style="{ width: `${seg.percent}%`, background: seg.color }"
                  :title="`${seg.label} ${seg.count} 个 (${seg.percent}%)`"
                />
              </div>
              <ul class="mt-4 space-y-1">
                <li
                  v-for="seg in distribution"
                  :key="seg.type"
                  class="cache-legend-row c-p"
                  :class="{ 'is-active': activeType === seg.type }"
                  @click="activeType === seg.type ? (activeType = 'all') : (activeType = seg.type)"
                >
                  <span class="cache-legend-row__dot" :style="{ background: seg.color }" />
                  <span class="min-w-0 truncate text-xs text-[var(--el-text-color-regular)]">{{
                    seg.label
                  }}</span>
                  <span class="ml-auto text-xs font-medium text-[var(--el-text-color-primary)]">{{
                    seg.count
                  }}</span>
                  <span class="w-12 text-right text-xs text-g-600 tabular-nums"
                    >{{ seg.percent }}%</span
                  >
                </li>
              </ul>
            </template>
            <div v-else class="flex-cc py-6 text-xs text-g-600">暂无类型分布数据</div>
          </div>

          <div class="cache-card flex-1 !p-4">
            <div class="flex-cb mb-3">
              <span class="text-sm font-semibold text-[var(--el-text-color-primary)]"
                >命名空间 Top {{ nsTop.length }}</span
              >
            </div>
            <template v-if="nsTop.length">
              <ul class="space-y-3">
                <li v-for="(ns, i) in nsTop" :key="ns.name">
                  <div class="flex-cb mb-1.5">
                    <span
                      class="flex min-w-0 items-center gap-1.5 text-xs text-[var(--el-text-color-regular)]"
                    >
                      <span
                        class="cache-ns-dot inline-block h-2 w-2 rounded-full"
                        :style="{ background: NS_COLORS[i % NS_COLORS.length] }"
                      />
                      <span class="truncate" :title="ns.name">{{ ns.name }}</span>
                      <span class="text-g-500">({{ ns.count }})</span>
                    </span>
                    <span class="text-xs text-g-600 tabular-nums"
                      >{{ Math.round((ns.count / nsMax) * 100) }}%</span
                    >
                  </div>
                  <div
                    class="h-1.5 w-full overflow-hidden rounded-full bg-[var(--el-fill-color-light)]"
                  >
                    <div
                      class="h-full rounded-full transition-all duration-300"
                      :style="{
                        width: `${(ns.count / nsMax) * 100}%`,
                        background: NS_COLORS[i % NS_COLORS.length]
                      }"
                    />
                  </div>
                </li>
              </ul>
            </template>
            <div v-else class="flex-cc py-6 text-xs text-g-600">暂无命名空间数据</div>
          </div>
        </div>
      </div>
    </div>

    <!-- ============ 缓存值详情弹窗 ============ -->
    <ElDialog
      v-model="valueVisible"
      width="min(680px, 92vw)"
      :show-close="true"
      class="cache-value-dialog"
    >
      <template #header>
        <div class="flex min-w-0 items-center gap-3 pr-8">
          <span
            class="cache-type-chip flex items-center gap-1.5"
            :style="{ color: valueMeta.color, background: `${valueMeta.color}14` }"
          >
            <ArtSvgIcon :icon="valueMeta.icon" />
            {{ valueMeta.label }}
          </span>
          <span
            class="min-w-0 truncate text-sm text-[var(--el-text-color-primary)]"
            :title="pager.page?.key"
          >
            {{ pager.page?.key }}
          </span>
        </div>
      </template>

      <div class="cache-value">
        <div class="cache-value__meta flex flex-wrap items-center gap-2">
          <span
            class="cache-value__chip flex items-center gap-1"
            :style="{ color: ttlMeta.color, background: `${ttlMeta.color}14` }"
          >
            <ArtSvgIcon icon="ri:time-line" /> TTL {{ ttlMeta.text }}
          </span>
          <span class="cache-value__chip flex items-center gap-1">
            <ArtSvgIcon icon="ri:file-reduce-line" />
            {{ countLabel }}
          </span>
          <span class="flex items-center gap-1.5 ml-auto">
            <ArtButtonTable
              icon="ri:file-copy-line"
              iconClass="bg-theme/12 text-theme"
              title="复制值"
              @click="copyValue"
            />
          </span>
        </div>
        <!-- hash：字段表格（field | value 双列），游标翻页 -->
        <template v-if="valueType === 'hash'">
          <div v-loading="pager.loading" class="cache-pageable-panel">
            <ul v-if="hashEntries.length" class="cache-hash">
              <li v-for="e in hashEntries" :key="e.field" class="cache-hash__row group">
                <span class="cache-hash__field cache-cell-text" :title="summarize(e.field)">{{
                  truncateText(e.field)
                }}</span>
                <span class="cache-hash__cell">
                  <span class="cache-hash__val cache-cell-text" :title="summarize(e.value)">{{
                    truncateText(e.value)
                  }}</span>
                  <span class="cache-entry-copy flex-cc" title="复制该字段" @click="copyEntry(e)">
                    <ArtSvgIcon icon="ri:file-copy-line" />
                  </span>
                </span>
              </li>
            </ul>
            <div v-else-if="!pager.loading" class="cache-panel-empty">空哈希 — 无任何字段</div>
          </div>
        </template>

        <!-- list：带序号的行，页码分页 -->
        <template v-else-if="valueType === 'list'">
          <div v-loading="pager.loading" class="cache-pageable-panel">
            <ol v-if="listItems.length" class="cache-list">
              <li v-for="(item, idx) in listItems" :key="idx" class="cache-list__row group">
                <span class="cache-list__index">{{ (pager.page?.start ?? 0) + idx }}</span>
                <span class="cache-list__value cache-cell-text" :title="summarize(item)">{{
                  truncateText(item)
                }}</span>
                <span
                  class="cache-entry-copy flex-cc"
                  title="复制该元素"
                  @click="copyText(item, '元素')"
                >
                  <ArtSvgIcon icon="ri:file-copy-line" />
                </span>
              </li>
            </ol>
            <div v-else-if="!pager.loading" class="cache-panel-empty">空列表 — 无任何元素</div>
          </div>
        </template>

        <!-- set：成员标签云，游标翻页 -->
        <template v-else-if="valueType === 'set'">
          <div v-loading="pager.loading" class="cache-pageable-panel">
            <div v-if="setItems.length" class="cache-set">
              <span
                v-for="m in setItems"
                :key="m"
                class="cache-set__chip c-p"
                :title="`${summarize(m)}（点击复制）`"
                @click="copyText(m, '成员')"
              >
                <span class="cache-set__dot" />
                <span class="cache-set__text">{{ truncateText(m) }}</span>
              </span>
            </div>
            <div v-else-if="!pager.loading" class="cache-panel-empty">空集合 — 无任何成员</div>
          </div>
        </template>

        <!-- zset：成员 + 分值徽章，页码分页 -->
        <template v-else-if="valueType === 'zset'">
          <div v-loading="pager.loading" class="cache-pageable-panel">
            <ol v-if="zsetItems.length" class="cache-zset">
              <li v-for="(z, idx) in zsetItems" :key="idx" class="cache-zset__row group">
                <span class="cache-zset__rank">{{ (pager.page?.start ?? 0) + idx + 1 }}</span>
                <span class="cache-zset__member cache-cell-text" :title="summarize(z.member)">{{
                  truncateText(z.member)
                }}</span>
                <span class="cache-zset__score" :title="`分值 ${z.score}`">{{
                  formatScore(z.score)
                }}</span>
                <span
                  class="cache-entry-copy flex-cc"
                  title="复制成员"
                  @click="copyText(z.member, '成员')"
                >
                  <ArtSvgIcon icon="ri:file-copy-line" />
                </span>
              </li>
            </ol>
            <div v-else-if="!pager.loading" class="cache-panel-empty">空有序集合 — 无任何成员</div>
          </div>
        </template>

        <!-- stream：后端暂未返回内容 -->
        <template v-else-if="valueType === 'stream'">
          <div class="cache-panel-note">
            <ArtSvgIcon icon="ri:error-warning-line" />
            <div>
              <div class="cache-panel-note__title">Stream 类型暂不支持在线查看</div>
              <div class="cache-panel-note__sub"
                >请使用 redis-cli 的 XRANGE / XREAD 命令查看流内容</div
              >
            </div>
          </div>
        </template>

        <!-- string 与其他：截断提示 + JSON 自动美化/语法着色 -->
        <template v-else>
          <div v-loading="pager.loading" class="cache-pageable-panel">
            <div v-if="pager.page?.truncated" class="cache-string-banner">
              <ArtSvgIcon icon="ri:error-warning-line" />
              <div class="cache-string-banner__body">
                <div class="cache-string-banner__title">
                  该值是 {{ formatBytes(pager.page?.total ?? 0) }} 的大字符串，已显示前
                  {{ formatBytes(displayBytes) }}
                </div>
                <button
                  class="cache-string-more"
                  :disabled="!pager.canShowStringMore"
                  @click="pager.showStringMore"
                >
                  {{ pager.atStringCap ? '剩余内容请用 redis-cli 查看' : '显示更多' }}
                </button>
              </div>
            </div>
            <pre v-if="displayStr.length" class="cache-code">
              <template v-if="strHighlightable"><span v-for="(t, i) in strTokens" :key="i" :class="t.cls">{{ t.text }}</span></template>
              <template v-else>{{ displayStr }}</template>
            </pre>
            <div v-else-if="!pager.loading" class="cache-panel-empty">空值</div>
          </div>
        </template>

        <!-- list/zset 页码分页页脚 -->
        <footer v-if="indexFooterVisible" class="cache-dialog-footer">
          <ElPagination
            background
            small
            layout="total, sizes, prev, pager, next"
            :page-sizes="PAGE_SIZE_OPTIONS"
            :total="pager.page?.total ?? 0"
            :current-page="pager.currentPage"
            :page-size="pager.pageSize"
            @size-change="pager.changePageSize"
            @current-change="pager.gotoPage"
          />
        </footer>

        <!-- set/hash 游标翻页页脚 -->
        <footer v-if="cursorFooterVisible" class="cache-dialog-footer">
          <div class="cache-page-size flex items-center gap-1.5">
            <button
              v-for="s in PAGE_SIZE_OPTIONS"
              :key="s"
              class="cache-size-btn"
              :class="{ 'is-active': pager.pageSize === s }"
              @click="pager.changePageSize(s)"
            >
              {{ s }}
            </button>
          </div>
          <span class="cache-cursor-pos">{{ cursorPosText }}</span>
          <span class="inline-block" :class="{ 'cache-disabled': !pager.hasPrev }">
            <ArtButtonTable
              icon="ri:arrow-left-line"
              iconClass="bg-theme/12 text-theme"
              title="上一页"
              @click="pager.cursorPrev()"
            />
          </span>
          <span class="inline-block" :class="{ 'cache-disabled': !pager.hasNext }">
            <ArtButtonTable
              icon="ri:arrow-right-line"
              iconClass="bg-theme/12 text-theme"
              title="下一页"
              @click="pager.cursorNext()"
            />
          </span>
        </footer>
      </div>
    </ElDialog>
  </div>
</template>

<script setup lang="ts">
  import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue'
  import { useResizeObserver } from '@vueuse/core'
  import { ElMessage, ElMessageBox } from 'element-plus'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import {
    useCacheValuePaging,
    truncateText,
    summarize,
    formatBytes,
    HIGHLIGHT_MAX_BYTES,
    PAGE_SIZE_OPTIONS,
    type HashEntry,
    type ZSetEntry
  } from '../composables/use-cache-value-paging'
  import { deleteCacheKeys, fetchCacheKeys, fetchCacheValuePage, type CacheKeyInfo } from '../api'
  // 纯格式化工具已抽到 composables 并配套单测(views/cache.vue 原本 1900+ 行)
  import {
    formatScore,
    formatValue,
    highlightJson,
    toPattern
  } from '../composables/use-cache-format'

  defineOptions({ name: 'MonitorCache' })

  /** Redis 值类型 → 视觉身份（图标 / 颜色 / 文案），UI 层独立维护 */
  interface TypeMeta {
    label: string
    icon: string
    color: string
  }

  const TYPE_META: Record<string, TypeMeta> = {
    string: { label: '字符串', icon: 'ri:t-box-line', color: '#3b82f6' },
    hash: { label: '哈希表', icon: 'ri:function-line', color: '#7c3aed' },
    list: { label: '列表', icon: 'ri:list-check-3', color: '#10b981' },
    set: { label: '集合', icon: 'ri:apps-2-line', color: '#f59e0b' },
    zset: { label: '有序集合', icon: 'ri:bar-chart-2-line', color: '#ec4899' },
    stream: { label: '流', icon: 'ri:water-flash-line', color: '#06b6d4' },
    none: { label: '已过期', icon: 'ri:time-line', color: '#94a3b8' },
    unknown: { label: '其他类型', icon: 'ri:more-2-fill', color: '#64748b' }
  }

  const NS_COLORS = ['#3b82f6', '#7c3aed', '#10b981', '#f59e0b', '#ec4899', '#06b6d4']

  interface KeyGroup {
    type: string
    label: string
    icon: string
    color: string
    keys: string[]
  }

  const prefix = ref('')
  const loading = ref(false)
  const scanning = ref(false)
  const scanned = ref(0)
  const truncated = ref(false)
  const keys = ref<CacheKeyInfo[]>([])
  const activeType = ref<string>('all')
  const collapsed = ref<Set<string>>(new Set())
  const autoRefresh = ref(false)
  const updatedAt = ref<Date | null>(null)
  const valueVisible = ref(false)
  /** 值详情分页状态机：page 为当前页快照（含 key/type/ttl 元信息）。
   *  reactive 解包内层 ref：脚本/模板一律不带 .value 访问（pager.page 即当前页）。 */
  const pager = reactive(useCacheValuePaging(fetchCacheValuePage))
  let autoTimer: number | undefined
  /** 并发扫描序列号：新查询取代旧扫描时丢弃过期结果 */
  let loadSeq = 0

  /** 单页扫描量（后端上限 100）；展示上限与最大扫描轮数 */
  const SCAN_COUNT = 100
  const MAX_DISPLAY_KEYS = 1000
  const MAX_SCAN_ROUNDS = 30

  /** 左列高度精确跟随右列：实测右列高度作为左列 max-height，保证两列底部持平 */
  const rightColRef = ref<HTMLElement | null>(null)
  const explorerMax = ref<string>('none')
  useResizeObserver(rightColRef, (entries) => {
    const h = entries[0]?.contentRect.height ?? 0
    explorerMax.value = h > 0 ? `${Math.round(h)}px` : 'none'
  })

  const totalCount = computed(() => keys.value.length)
  const updatedAtText = computed(() =>
    updatedAt.value ? updatedAt.value.toLocaleTimeString('zh-CN', { hour12: false }) : '尚未同步'
  )

  /** 按 Redis 值类型分组，数量降序 */
  const groups = computed<KeyGroup[]>(() => {
    const map = new Map<string, string[]>()
    for (const item of keys.value) {
      const t = (item.type || 'unknown').toLowerCase()
      if (!map.has(t)) map.set(t, [])
      map.get(t)!.push(item.key)
    }
    return Array.from(map.entries())
      .map(([type, list]) => ({ type, ...typeMetaOf(type), keys: list }))
      .sort((a, b) => b.keys.length - a.keys.length)
  })

  const topGroup = computed(() => groups.value[0] ?? null)
  const topGroupShare = computed(() =>
    topGroup.value && totalCount.value
      ? Math.round((topGroup.value.keys.length / totalCount.value) * 100)
      : 0
  )
  const topTypesText = computed(() =>
    groups.value
      .slice(0, 3)
      .map((g) => g.label)
      .join(' · ')
  )
  const visibleSections = computed(() =>
    activeType.value === 'all'
      ? groups.value
      : groups.value.filter((g) => g.type === activeType.value)
  )
  function shareOf(g: KeyGroup): number {
    return totalCount.value ? Math.round((g.keys.length / totalCount.value) * 100) : 0
  }

  /** 类型分布（100% 堆叠条 + 图例） */
  const distribution = computed(() =>
    groups.value.map((g) => ({
      type: g.type,
      label: g.label,
      color: g.color,
      count: g.keys.length,
      percent: totalCount.value ? Math.round((g.keys.length / totalCount.value) * 100) : 0
    }))
  )

  /** 命名空间：key 中第一个 `:` 之前的前缀 */
  interface NsStat {
    name: string
    count: number
  }
  const namespaces = computed<NsStat[]>(() => {
    const map = new Map<string, number>()
    for (const item of keys.value) {
      const idx = item.key.indexOf(':')
      const ns = idx > 0 ? item.key.slice(0, idx) : '(根)'
      map.set(ns, (map.get(ns) ?? 0) + 1)
    }
    return Array.from(map.entries())
      .map(([name, count]) => ({ name, count }))
      .sort((a, b) => b.count - a.count)
  })
  const nsTop = computed(() => namespaces.value.slice(0, 6))
  const nsMax = computed(() => Math.max(...nsTop.value.map((n) => n.count), 1))

  function typeMetaOf(type: string): TypeMeta {
    return TYPE_META[type] ?? TYPE_META.unknown!
  }
  const valueMeta = computed(() => typeMetaOf((pager.page?.type || 'unknown').toLowerCase()))

  /** TTL 徽标：-2 已过期 / -1 不过期 / Ns */
  const ttlMeta = computed(() => {
    const ttl = pager.page?.ttl
    if (ttl === null || ttl === undefined) return { text: '-', color: '#94a3b8' }
    if (ttl === -2) return { text: '已过期', color: '#dc2626' }
    if (ttl === -1) return { text: '不过期', color: '#10b981' }
    return { text: `${ttl}s`, color: ttl < 60 ? '#f59e0b' : '#3b82f6' }
  })

  const valueType = computed(() => (pager.page?.type || '').toLowerCase())

  /** hash 条目：value 为 [{field, value}] 数组（后端分页形状） */
  const hashEntries = computed<HashEntry[]>(() => {
    const v = pager.page?.value
    if (valueType.value !== 'hash' || !Array.isArray(v)) return []
    return v.filter(
      (e): e is HashEntry =>
        !!e &&
        typeof (e as HashEntry).field === 'string' &&
        typeof (e as HashEntry).value === 'string'
    )
  })

  /** list 元素（后端已按页返回字符串数组；逐条 is 校验与 hash 信任水平一致） */
  const listItems = computed<string[]>(() => {
    const v = pager.page?.value
    if (valueType.value !== 'list' || !Array.isArray(v)) return []
    return v.filter((x): x is string => typeof x === 'string')
  })

  /** set 成员 */
  /** set 成员：SCAN 顺序无意义，按自然序展示（member-2 在 member-10 之前），页面更整齐 */
  const naturalCmp = new Intl.Collator('zh', { numeric: true }).compare
  const setItems = computed<string[]>(() => {
    const v = pager.page?.value
    if (valueType.value !== 'set' || !Array.isArray(v)) return []
    return v.filter((x): x is string => typeof x === 'string').sort(naturalCmp)
  })

  /** zset 成员 + 分值（逐条 is 校验与 hash 信任水平一致） */
  const zsetItems = computed<ZSetEntry[]>(() => {
    const v = pager.page?.value
    if (valueType.value !== 'zset' || !Array.isArray(v)) return []
    return v.filter(
      (z): z is ZSetEntry =>
        !!z &&
        typeof (z as ZSetEntry).member === 'string' &&
        typeof (z as ZSetEntry).score === 'number'
    )
  })

  /** string 值：JSON 文本自动美化缩进，其余原样 */
  const displayStr = computed(() => {
    if (valueType.value !== 'string') return ''
    const v = pager.page?.value
    if (typeof v !== 'string' || !v.trim()) return ''
    const t = v.trim()
    if ((t.startsWith('{') && t.endsWith('}')) || (t.startsWith('[') && t.endsWith(']'))) {
      try {
        return JSON.stringify(JSON.parse(t), null, 2)
      } catch {
        return v
      }
    }
    return v
  })

  /** 轻量 JSON 语法着色分词：key/string/number/boolean/punctuation */
  /** 语法高亮仅在 ≤64KB 时启用，超过渲染纯文本避免分词卡顿 */
  const strHighlightable = computed(() => {
    const s = displayStr.value
    if (!s) return false
    try {
      return new TextEncoder().encode(s).byteLength <= HIGHLIGHT_MAX_BYTES
    } catch {
      return false
    }
  })
  const strTokens = computed(() => (strHighlightable.value ? highlightJson(displayStr.value) : []))

  /** 元信息徽章：按类型展示总量/字节数与当前页范围 */
  const countLabel = computed(() => {
    const p = pager.page
    if (!p) return '-'
    const t = (p.type || '').toLowerCase()
    if (t === 'string') {
      if (!p.total) return '0 B'
      // 口径与提示条 displayBytes 一致：原始返回 value 的字节数（不做 JSON 美化口径）
      return p.truncated
        ? `${formatBytes(p.total)} · 已显示前 ${formatBytes(displayBytes.value)}`
        : `${formatBytes(displayBytes.value)}`
    }
    const len = Array.isArray(p.value) ? p.value.length : 0
    if (!p.total) return '空'
    if (t === 'set' || t === 'hash') {
      const word = t === 'set' ? '成员' : '字段'
      return `共 ${p.total.toLocaleString()} ${word} · 本页 ${pager.cursorStart + 1}~${pager.cursorStart + len}`
    }
    if (t === 'list' || t === 'zset') {
      const word = t === 'list' ? '元素' : '成员'
      return `共 ${p.total.toLocaleString()} ${word} · 本页 ${p.start + 1}~${p.start + len}`
    }
    return p.total.toLocaleString()
  })

  /** set/hash 游标位置文案 */
  const cursorPosText = computed(() => {
    const p = pager.page
    if (!p) return ''
    const start = pager.cursorStart
    const len = Array.isArray(p.value) ? p.value.length : 0
    return `${start + 1}~${start + len} / 共 ${p.total.toLocaleString()} 条`
  })

  /** list/zset 页码分页页脚可见性 */
  const indexFooterVisible = computed(
    () => (valueType.value === 'list' || valueType.value === 'zset') && (pager.page?.total ?? 0) > 0
  )
  /** set/hash 游标翻页页脚可见性（复用 composable 的类型分类，避免视图重复开关） */
  const cursorFooterVisible = computed(() => pager.isCursorType && (pager.page?.total ?? 0) > 0)

  /** string 截断提示条数据（已显示字节数：原始返回 value，与徽章同一口径） */
  const displayBytes = computed(() => {
    const v = pager.page?.value
    if (typeof v !== 'string') return 0
    try {
      return new TextEncoder().encode(v).byteLength
    } catch {
      return 0
    }
  })

  function toggleSection(type: string) {
    const next = new Set(collapsed.value)
    if (next.has(type)) next.delete(type)
    else next.add(type)
    collapsed.value = next
  }

  function toggleAutoRefresh() {
    autoRefresh.value = !autoRefresh.value
  }

  watch(autoRefresh, (on) => {
    if (autoTimer) {
      window.clearInterval(autoTimer)
      autoTimer = undefined
    }
    if (on) {
      autoTimer = window.setInterval(() => loadKeys(true), 10_000)
    }
  })
  onBeforeUnmount(() => {
    if (autoTimer) window.clearInterval(autoTimer)
  })

  /** 无 * 时自动追加 *，转成 Redis 通配模式 */
  function handleSearch() {
    loadKeys()
  }

  /** Redis SCAN 游标分页：沿 cursor 循环扫描直至扫完（cursor=0），
   * 超过 MAX_DISPLAY_KEYS 时提前截断并提示前缀过滤 */
  async function loadKeys(silent = false) {
    if (silent && scanning.value) return
    const seq = ++loadSeq
    if (!silent) loading.value = true
    scanning.value = true
    scanned.value = 0
    truncated.value = false
    try {
      const collected: CacheKeyInfo[] = []
      let cursor = 0
      let rounds = 0
      while (rounds < MAX_SCAN_ROUNDS) {
        const res = await fetchCacheKeys({
          prefix: toPattern(prefix.value),
          cursor,
          count: SCAN_COUNT
        })
        if (seq !== loadSeq) return // 已被更新的查询取代
        collected.push(...(res.keys ?? []))
        scanned.value = collected.length
        rounds++
        const next = Number(res.cursor ?? 0)
        if (next <= 0 || collected.length >= MAX_DISPLAY_KEYS) {
          truncated.value = next > 0
          break
        }
        cursor = next
      }
      // 保险丝触发（SCAN 反复返回非零游标）：同样视为截断
      if (rounds >= MAX_SCAN_ROUNDS && collected.length < MAX_DISPLAY_KEYS) {
        truncated.value = true
      }
      keys.value = collected
      updatedAt.value = new Date()
    } finally {
      if (seq === loadSeq) {
        scanning.value = false
        if (!silent) loading.value = false
      }
    }
  }

  async function showValue(key: string, type: string) {
    try {
      // 按类型取第一页后打开弹窗：header 的 key/type/ttl 由 pager.page 提供
      await pager.open(key, type)
      valueVisible.value = true
    } catch {
      // 错误已由 http 层统一提示
    }
  }

  async function copyText(text: string, label: string) {
    try {
      await navigator.clipboard.writeText(text)
      ElMessage.success(`已复制${label}`)
    } catch {
      ElMessage.error('复制失败，请手动复制')
    }
  }

  function copyKey(key: string) {
    copyText(key, ' key')
  }

  function copyValue() {
    const p = pager.page
    if (!p) return
    // string：复制显示内容，截断时在提示中说明
    if (valueType.value === 'string') {
      if (!displayStr.value) {
        ElMessage.warning('空值无可复制')
        return
      }
      copyText(displayStr.value, p.truncated ? '缓存值（已截断的显示内容）' : '缓存值')
      return
    }
    // 集合类型：复制当前已加载页
    // set 无原生顺序，复制内容与展示顺序一致（自然序）
    const arr = valueType.value === 'set' ? setItems.value : Array.isArray(p.value) ? p.value : []
    if (!arr.length) {
      ElMessage.warning('当前页为空，无可复制')
      return
    }
    copyText(formatValue(arr), `缓存值（当前页 ${arr.length} 条）`)
  }

  function copyEntry(entry: HashEntry) {
    copyText(`${entry.field}: ${entry.value}`, '该字段')
  }

  async function deleteByPrefix() {
    if (!prefix.value.trim()) {
      ElMessage.warning('删除前请输入 key 前缀')
      return
    }
    try {
      await ElMessageBox.confirm(
        `确认删除前缀「${prefix.value.trim()}」匹配的缓存键吗？（最多 1000 个）`,
        '删除确认',
        { type: 'warning', confirmButtonText: '确定', cancelButtonText: '取消' }
      )
    } catch {
      // 用户取消
      return
    }
    const res = await deleteCacheKeys({ prefix: toPattern(prefix.value), maxCount: 1000 })
    ElMessage.success(`已删除 ${res.deleted} 个缓存键`)
    // 清空输入框后按空前缀重新查询（查看全部），避免停留在已删除前缀的过滤视图
    prefix.value = ''
    await loadKeys()
  }

  loadKeys()
</script>

<style scoped lang="scss">
  /* 四视图共享的设计令牌(卡片 / KPI / 呼吸圆点 / 入场动画) */
  @use './monitor-tokens' as t;

  @include t.rise-keyframes;
  @include t.pulse-keyframes;
  /* 动画停用收敛到共享 mixin(此前四视图各自重写同一媒体查询) */
  @include t.reduced-motion('.cache-card', '.live-dot');

  /* ---------- 基础卡片 ---------- */
  .cache-card {
    @include t.card;
    /* 本视图特有:卡片可交互(hover 抬起),较共享令牌多一组 transition */
    transition:
      transform 0.2s ease,
      box-shadow 0.2s ease,
      border-color 0.2s ease;
    @include t.rise;
  }

  .dark .cache-card {
    @include t.card-dark;
  }

  /* ---------- 页头 ---------- */
  .cache-hero__icon {
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

  /* ---------- KPI 磁贴 ---------- */
  .kpi-tile {
    display: flex;
    align-items: center;
    gap: 0.875rem;
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

  .kpi-tile:hover {
    transform: translateY(-2px);
    box-shadow: 0 8px 18px rgba(16, 24, 40, 0.09);
    border-color: var(--el-color-primary-light-5);
  }

  .kpi-tile__icon {
    @include t.kpi-icon;
    /* 本视图 KPI 磁贴较大:图标与字号按 44px 基准收敛(见下方 kpi-value) */
    width: 44px;
    height: 44px;
    font-size: 20px;
    color: #fff;
    background: linear-gradient(135deg, var(--tile) 0%, var(--tile2) 100%);
    box-shadow: 0 4px 10px color-mix(in srgb, var(--tile) 35%, transparent);
  }

  .kpi-tile__value {
    @include t.kpi-value;
    /* 与 44px 图标配套:本视图为 22px(共享令牌默认 20px) */
    font-size: 22px;
  }

  .kpi-tile__label {
    @include t.kpi-label;
  }

  .kpi-tile__sub {
    @include t.kpi-sub;
  }

  /* ---------- 工具栏 ---------- */
  .cache-prefix-input {
    width: min(340px, 100%);
  }

  .cache-summary-pill {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 3px 10px;
    border-radius: 999px;
    border: 1px solid var(--default-border);
    background: var(--art-gray-100);
    color: var(--el-text-color-secondary);
    font-variant-numeric: tabular-nums;
  }

  .cache-limit-chip {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 3px 10px;
    border-radius: 999px;
    border: 1px solid rgba(245, 158, 11, 0.35);
    background: rgba(245, 158, 11, 0.12);
    color: #b45309;
    white-space: nowrap;
  }

  .dark .cache-limit-chip {
    color: #fbbf24;
  }

  /* ---------- 类型筛选 pill ---------- */
  .cache-pill {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 3px 10px;
    border-radius: 999px;
    border: 1px solid var(--default-border);
    background: transparent;
    color: var(--el-text-color-regular);
    font-size: 12px;
    cursor: pointer;
    transition:
      color 0.15s ease,
      background 0.15s ease,
      border-color 0.15s ease,
      transform 0.15s ease;
  }

  .cache-pill:focus-visible {
    outline: 2px solid var(--el-color-primary);
    outline-offset: 1px;
  }

  .cache-pill:hover {
    transform: translateY(-1px);
    border-color: var(--el-color-primary-light-5);
  }

  .cache-pill__dot {
    width: 7px;
    height: 7px;
    border-radius: 999px;
  }

  /* ---------- 键空间浏览 ---------- */
  /* 桌面端：左列高度 = 右列实测高度（两列底部持平，列表内部滚动） */
  @media (min-width: 1024px) {
    .cache-explorer-card {
      max-height: var(--explorer-max, none);
    }
  }

  .cache-explorer {
    padding: 0 0.75rem 0.75rem;
  }

  .cache-section:not(:last-child) {
    margin-bottom: 0.375rem;
  }

  .cache-section__head {
    padding: 9px 10px;
    border-radius: 10px;
    cursor: pointer;
    transition: background 0.15s ease;
  }

  .cache-section__head:hover {
    background: var(--art-hover-color);
  }

  .cache-section__head.is-collapsed .cache-section__icon {
    opacity: 0.55;
  }

  .cache-section__icon {
    width: 26px;
    height: 26px;
    flex: none;
    border-radius: 8px;
    font-size: 14px;
  }

  .cache-section__count {
    padding: 1px 8px;
    border-radius: 999px;
    font-size: 11px;
    background: var(--el-fill-color-light);
    color: var(--el-text-color-secondary);
    font-variant-numeric: tabular-nums;
  }

  .cache-section__share {
    font-size: 11px;
    font-weight: 600;
    font-variant-numeric: tabular-nums;
  }

  .cache-collapse {
    display: grid;
    grid-template-rows: 1fr;
    transition: grid-template-rows 0.2s ease;
  }

  .cache-collapse.is-collapsed {
    grid-template-rows: 0fr;
  }

  .cache-collapse__inner {
    overflow: hidden;
    min-height: 0;
  }

  .cache-key-row {
    position: relative;
    display: flex;
    align-items: center;
    gap: 10px;
    margin: 1px 10px 1px 36px;
    padding: 6px 8px 6px 10px;
    border-radius: 8px;
    overflow: hidden;
    cursor: pointer;
    transition: background 0.15s ease;
  }

  .cache-key-row:hover {
    background: var(--art-hover-color);
  }

  .cache-key-row__bar {
    position: absolute;
    left: 0;
    top: 50%;
    transform: translateY(-50%);
    width: 3px;
    height: 55%;
    border-radius: 2px;
  }

  .cache-key-row__text {
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, 'Fira Code', monospace;
    font-size: 12.5px;
    color: var(--el-text-color-primary);
  }

  .cache-key-row__actions {
    display: flex;
    align-items: center;
    gap: 6px;
    flex: none;
    margin-left: auto;
    opacity: 0;
    transform: translateX(4px);
    transition:
      opacity 0.15s ease,
      transform 0.15s ease;
  }

  .cache-key-row:hover .cache-key-row__actions,
  .cache-key-row:focus-within .cache-key-row__actions {
    opacity: 1;
    transform: translateX(0);
  }

  .cache-key-row__action {
    width: 24px;
    height: 24px;
    border-radius: 7px;
    font-size: 13px;
    transition: transform 0.15s ease;
  }

  .cache-key-row__action:hover {
    transform: scale(1.12);
  }

  @media (hover: none) {
    .cache-key-row__actions {
      opacity: 1;
      transform: none;
    }
  }

  /* ---------- 空状态 ---------- */
  .cache-empty__icon {
    width: 64px;
    height: 64px;
    border-radius: 20px;
    font-size: 30px;
    color: var(--el-color-primary);
    background: rgba(59, 130, 246, 0.12);
  }

  /* ---------- 类型分布 ---------- */
  .cache-distribution-bar {
    background: var(--el-fill-color-light);
  }

  .cache-legend-row {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 5px 8px;
    border-radius: 8px;
    transition: background 0.15s ease;
  }

  .cache-legend-row:hover {
    background: var(--art-hover-color);
  }

  .cache-legend-row.is-active {
    background: var(--art-active-color);
  }

  .cache-legend-row__dot {
    width: 9px;
    height: 9px;
    border-radius: 3px;
    flex: none;
  }

  .cache-ns-dot {
    flex: none;
  }

  /* ---------- 值详情弹窗 ---------- */
  .cache-type-chip {
    padding: 3px 10px;
    border-radius: 999px;
    font-size: 12px;
    font-weight: 500;
    flex: none;
  }

  .cache-value__meta {
    margin-bottom: 12px;
  }

  .cache-value__chip {
    padding: 3px 10px;
    border-radius: 999px;
    font-size: 12px;
    border: 1px solid var(--default-border);
    color: var(--el-text-color-regular);
  }

  /* ---------- 值详情弹窗：分类型展示 ---------- */

  /* 通用：面板容器 / 空态 */
  .cache-panel-empty {
    padding: 26px 16px;
    border: 1px dashed var(--default-border-dashed);
    border-radius: 10px;
    font-size: 12px;
    text-align: center;
    color: var(--el-text-color-secondary);
  }

  .cache-hash,
  .cache-list,
  .cache-zset {
    margin: 0;
    padding: 0;
    list-style: none;
    max-height: 40vh;
    overflow: auto;
    border: 1px solid var(--default-border);
    border-radius: 10px;
    background: var(--el-fill-color-light);
  }

  .cache-hash__row,
  .cache-list__row,
  .cache-zset__row {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 12px;
    border-bottom: 1px solid var(--default-border);
    transition: background 0.15s ease;
  }

  .cache-hash__row:last-child,
  .cache-list__row:last-child,
  .cache-zset__row:last-child {
    border-bottom: none;
  }

  .cache-hash__row:hover,
  .cache-list__row:hover,
  .cache-zset__row:hover {
    background: var(--art-hover-color);
  }

  /* 行尾复制按钮（通用） */
  .cache-entry-copy {
    flex: none;
    margin-left: auto;
    width: 26px;
    height: 26px;
    border-radius: 7px;
    font-size: 13px;
    color: var(--el-text-color-secondary);
    background: var(--art-gray-200);
    cursor: pointer;
    opacity: 0;
    transform: translateX(4px);
    transition:
      opacity 0.15s ease,
      transform 0.15s ease,
      color 0.15s ease,
      background 0.15s ease;
  }

  .group:hover .cache-entry-copy {
    opacity: 1;
    transform: translateX(0);
  }

  .cache-entry-copy:hover {
    color: #fff;
    background: var(--el-color-primary);
  }

  @media (hover: none) {
    .cache-entry-copy {
      opacity: 1;
      transform: none;
    }
  }

  /* string：JSON 美化 + 轻量语法着色 */
  .cache-code {
    margin: 0;
    padding: 14px;
    max-height: 40vh;
    overflow: auto;
    white-space: pre-wrap;
    word-break: break-all;
    background: var(--el-fill-color-light);
    border: 1px solid var(--default-border);
    border-left: 3px solid #3b82f6;
    border-radius: 10px;
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, 'Fira Code', monospace;
    font-size: 12.5px;
    line-height: 1.7;
    color: var(--el-text-color-primary);
  }

  .tok-key {
    color: #7c3aed;
  }
  .tok-str {
    color: #059669;
  }
  .tok-num {
    color: #d97706;
  }
  .tok-kw {
    color: #0891b2;
  }
  .tok-punct {
    color: #64748b;
  }
  .tok-plain {
    color: var(--el-text-color-primary);
  }

  .dark .tok-key {
    color: #a78bfa;
  }
  .dark .tok-str {
    color: #34d399;
  }
  .dark .tok-num {
    color: #fbbf24;
  }
  .dark .tok-kw {
    color: #22d3ee;
  }

  /* hash：双列表格（field | value） */
  .cache-hash__row {
    display: grid;
    grid-template-columns: minmax(96px, 32%) 1fr;
    gap: 0;
    padding: 0;
  }

  .cache-hash__field {
    display: flex;
    align-items: center;
    padding: 9px 12px;
    border-right: 1px solid var(--default-border);
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, 'Fira Code', monospace;
    font-size: 12.5px;
    font-weight: 600;
    color: #7c3aed;
    word-break: break-all;
  }

  .dark .cache-hash__field {
    color: #a78bfa;
  }

  .cache-hash__cell {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
    padding: 9px 10px 9px 12px;
  }

  .cache-hash__val {
    flex: 1;
    min-width: 0;
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, 'Fira Code', monospace;
    font-size: 12.5px;
    word-break: break-all;
    color: var(--el-text-color-primary);
  }

  /* list：序号徽章行 */
  .cache-list__index {
    flex: none;
    min-width: 30px;
    height: 20px;
    padding: 0 7px;
    border-radius: 6px;
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, 'Fira Code', monospace;
    font-size: 11px;
    line-height: 20px;
    text-align: center;
    color: #059669;
    background: rgba(16, 185, 129, 0.1);
    font-variant-numeric: tabular-nums;
  }

  .dark .cache-list__index {
    color: #34d399;
  }

  .cache-list__value {
    flex: 1;
    min-width: 0;
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, 'Fira Code', monospace;
    font-size: 12.5px;
    word-break: break-all;
    color: var(--el-text-color-primary);
  }

  /* set：成员标签云（等宽多列栅格，成员自然序展示） */
  .cache-set {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
    gap: 8px;
    max-height: 40vh;
    overflow: auto;
    padding: 12px;
    border: 1px solid var(--default-border);
    border-radius: 10px;
    background: var(--el-fill-color-light);
  }

  .cache-set__chip {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    width: 100%;
    min-width: 0;
    padding: 4px 12px;
    border-radius: 999px;
    border: 1px solid rgba(245, 158, 11, 0.35);
    background: rgba(245, 158, 11, 0.1);
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, 'Fira Code', monospace;
    font-size: 12.5px;
    color: var(--el-text-color-primary);
    transition:
      transform 0.15s ease,
      box-shadow 0.15s ease;
  }

  .cache-set__text {
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }

  .cache-set__chip:hover {
    transform: translateY(-1px);
    box-shadow: 0 2px 8px rgba(245, 158, 11, 0.28);
  }

  .cache-set__dot {
    width: 6px;
    height: 6px;
    border-radius: 999px;
    background: #f59e0b;
    flex: none;
  }

  /* zset：排名 + 成员 + 分值徽章 */
  .cache-zset__rank {
    flex: none;
    min-width: 24px;
    height: 20px;
    padding: 0 6px;
    border-radius: 6px;
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, 'Fira Code', monospace;
    font-size: 11px;
    line-height: 20px;
    text-align: center;
    color: #db2777;
    background: rgba(236, 72, 153, 0.12);
    font-variant-numeric: tabular-nums;
  }

  .dark .cache-zset__rank {
    color: #f472b6;
  }

  .cache-zset__member {
    flex: 1;
    min-width: 0;
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, 'Fira Code', monospace;
    font-size: 12.5px;
    word-break: break-all;
    color: var(--el-text-color-primary);
  }

  .cache-zset__score {
    flex: none;
    padding: 1px 9px;
    border-radius: 999px;
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, 'Fira Code', monospace;
    font-size: 11.5px;
    font-weight: 600;
    color: #db2777;
    background: rgba(236, 72, 153, 0.12);
    font-variant-numeric: tabular-nums;
  }

  .dark .cache-zset__score {
    color: #f472b6;
  }

  /* stream：暂不支持提示 */
  .cache-panel-note {
    display: flex;
    align-items: flex-start;
    gap: 12px;
    padding: 16px;
    border-radius: 10px;
    border: 1px solid rgba(6, 182, 212, 0.3);
    background: rgba(6, 182, 212, 0.08);
    font-size: 20px;
    color: #0891b2;
  }

  .dark .cache-panel-note {
    color: #22d3ee;
  }

  .cache-panel-note__title {
    font-size: 13px;
    font-weight: 600;
    color: var(--el-text-color-primary);
  }

  .cache-panel-note__sub {
    margin-top: 2px;
    font-size: 12px;
    color: var(--el-text-color-secondary);
  }

  /* ---------- 分页页脚与超大值防护 ---------- */
  .cache-pageable-panel {
    position: relative;
    min-height: 64px;
  }

  .cache-dialog-footer {
    display: flex;
    align-items: center;
    justify-content: flex-end;
    gap: 10px;
    margin-top: 12px;
    padding-top: 10px;
    border-top: 1px solid var(--default-border);

    .el-pagination {
      margin: 0;
    }
  }

  .cache-page-size {
    margin-right: auto;
  }

  /* 页大小按钮：独立类名（与键类型筛选 .cache-pill 解耦），base 视觉同 .cache-pill */
  .cache-size-btn {
    display: inline-flex;
    align-items: center;
    padding: 3px 10px;
    border-radius: 999px;
    border: 1px solid var(--default-border);
    background: transparent;
    color: var(--el-text-color-regular);
    font-size: 12px;
    font-variant-numeric: tabular-nums;
    cursor: pointer;
    transition:
      color 0.15s ease,
      border-color 0.15s ease,
      background 0.15s ease;
  }

  .cache-size-btn:hover {
    border-color: var(--el-color-primary-light-5);
    color: var(--el-color-primary);
  }

  .cache-size-btn:focus-visible {
    outline: 2px solid var(--el-color-primary);
    outline-offset: 1px;
  }

  .cache-size-btn.is-active {
    color: var(--el-color-primary);
    background: var(--el-color-primary-light-9);
    border-color: var(--el-color-primary-light-5);
  }

  .cache-cursor-pos {
    font-size: 12px;
    color: var(--el-text-color-secondary);
    font-variant-numeric: tabular-nums;
  }

  .cache-disabled {
    opacity: 0.4;
    pointer-events: none;
  }

  /* 行内文本截断：两行 ellipsis；展示层已被 truncateText 截到 300 字符 */
  .cache-cell-text {
    display: -webkit-box;
    -webkit-box-orient: vertical;
    -webkit-line-clamp: 2;
    overflow: hidden;
    white-space: normal;
    word-break: break-all;
  }

  /* string 大值截断提示条 */
  .cache-string-banner {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    margin-bottom: 12px;
    padding: 12px 14px;
    border-radius: 10px;
    border: 1px solid rgba(245, 158, 11, 0.35);
    background: rgba(245, 158, 11, 0.1);
    font-size: 20px;
    color: #b45309;
  }

  .dark .cache-string-banner {
    color: #fbbf24;
  }

  .cache-string-banner__body {
    font-size: 12px;
    color: var(--el-text-color-regular);
  }

  .cache-string-banner__title {
    margin-bottom: 6px;
  }

  .cache-string-more {
    padding: 3px 12px;
    border-radius: 999px;
    border: 1px solid rgba(245, 158, 11, 0.4);
    background: transparent;
    font-size: 12px;
    color: #b45309;
    cursor: pointer;
    transition: opacity 0.15s ease;
  }

  .dark .cache-string-more {
    color: #fbbf24;
  }

  .cache-string-more:disabled {
    opacity: 0.55;
    cursor: not-allowed;
  }
</style>
