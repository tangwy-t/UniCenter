<template>
  <div class="device-upgrade-task art-full-height overflow-y-auto">
    <div class="ut__inner p-4 pb-8 md:p-5">
      <!-- ============ 页头 ============ -->
      <div class="do-hero mb-4 flex flex-wrap items-center gap-3">
        <div class="do-hero__icon flex-cc">
          <ArtSvgIcon :icon="pageIcon" />
        </div>
        <div class="min-w-0">
          <h2 class="text-lg font-semibold text-[var(--el-text-color-primary)]">升级任务</h2>
          <p class="mt-0.5 text-xs text-g-600">{{ subtitle }}</p>
        </div>
        <div class="ml-auto flex items-center gap-1">
          <ArtButtonTable
            icon="ri:refresh-line"
            iconClass="bg-theme/12 text-theme"
            title="刷新"
            @click="reloadAll"
          />
        </div>
      </div>

      <div class="ut-grid">
        <!-- ============ 左：任务列表 ============ -->
        <div class="do-card ut-list">
          <div class="ut-list__head">
            <span class="ut-list__title">任务列表</span>
            <span class="ut-list__hint">每 {{ listIntervalSec }} 秒自动刷新</span>
          </div>
          <ElTable
            v-loading="listLoading"
            :data="tasks"
            size="small"
            highlight-current-row
            :current-row-key="selectedId"
            row-key="id"
            max-height="34rem"
            @current-change="onSelectTask"
          >
            <ElTableColumn label="任务" width="92">
              <template #default="{ row }">
                <span class="ut-mono">#{{ row.id.slice(-6) }}</span>
              </template>
            </ElTableColumn>
            <ElTableColumn label="目标版本" width="96">
              <template #default="{ row }">{{ row.targetVersion }}</template>
            </ElTableColumn>
            <ElTableColumn label="来源" width="76">
              <template #default="{ row }">{{ sourceText(row.source) }}</template>
            </ElTableColumn>
            <ElTableColumn label="进度" min-width="220">
              <template #default="{ row }">
                <span class="ut-counts">{{ taskCountsText(row.counts) }}</span>
                <span v-if="isSettled(row)" class="ut-done">已收口</span>
              </template>
            </ElTableColumn>
            <ElTableColumn label="发起人" width="90">
              <template #default="{ row }">{{ row.actor || '—' }}</template>
            </ElTableColumn>
            <ElTableColumn label="时间" width="128">
              <template #default="{ row }">{{ formatUnixTime(row.createdAt) }}</template>
            </ElTableColumn>
          </ElTable>
          <ElPagination
            v-if="taskTotal > pageSize"
            class="ut-list__pager"
            layout="prev, pager, next"
            small
            :total="taskTotal"
            :page-size="pageSize"
            :current-page="page"
            @current-change="onPageChange"
          />
        </div>

        <!-- ============ 右：任务详情（逐台进度） ============ -->
        <div class="do-card ut-detail">
          <template v-if="detail">
            <div class="ut-detail__head">
              <div class="min-w-0">
                <div class="ut-detail__title">
                  升级到 {{ detail.task.targetVersion }}
                  <span class="ut-detail__meta">
                    {{ sourceText(detail.task.source) }} · {{ detail.task.actor || '—' }} ·
                    {{ formatUnixTime(detail.task.createdAt) }}
                  </span>
                </div>
                <div class="ut-detail__counts">{{ taskCountsText(detail.task.counts) }}</div>
              </div>
              <div class="ut-detail__actions">
                <ElRadioGroup v-model="filter" size="small" @change="loadDetail">
                  <ElRadioButton value="">全部</ElRadioButton>
                  <ElRadioButton value="active">进行中</ElRadioButton>
                  <ElRadioButton value="failed">失败</ElRadioButton>
                </ElRadioGroup>
                <ElButton
                  v-if="failedDeviceIds.length > 0 && canUpgrade"
                  size="small"
                  type="primary"
                  plain
                  @click="retryFailed"
                >
                  重试失败项（{{ failedDeviceIds.length }}）
                </ElButton>
              </div>
            </div>

            <ElTable :data="detail.list" size="small" max-height="30rem">
              <ElTableColumn label="设备" min-width="170">
                <template #default="{ row }">
                  <template v-if="row.deviceDeleted">
                    <span class="ut-muted">设备已删除</span>
                  </template>
                  <template v-else>
                    <div>{{ row.hostname || '—' }}</div>
                    <div class="ut-muted">{{ row.primaryIp || '—' }}</div>
                  </template>
                </template>
              </ElTableColumn>
              <ElTableColumn label="版本" width="128">
                <template #default="{ row }">
                  <span class="ut-mono">{{ row.fromVersion || '—' }} → {{ row.toVersion }}</span>
                </template>
              </ElTableColumn>
              <ElTableColumn label="进度" min-width="260">
                <template #default="{ row }">
                  <!-- 阶段轨：唯一有天然刻度的只有下载（真实字节百分比），
                       其余阶段用位置表达「卡在哪一步」——不造连续假进度。 -->
                  <div
                    class="ut-rail"
                    :class="{ 'is-failed': stage(row).failed, 'is-rolled': stage(row).rolledBack }"
                  >
                    <template v-for="(key, i) in stage(row).keys" :key="key">
                      <span
                        class="ut-rail__node"
                        :class="{
                          'is-done': i < stage(row).index || stage(row).succeeded,
                          'is-now': i === stage(row).index,
                          'is-bad':
                            (stage(row).failed || stage(row).rolledBack) &&
                            i === Math.max(stage(row).index, 0)
                        }"
                      />
                      <span class="ut-rail__seg" :class="{ 'is-done': i < stage(row).index }" />
                      <span class="ut-rail__label">{{ stageLabel(key) }}</span>
                    </template>
                  </div>
                  <div class="ut-desc">{{ stage(row).description }}</div>
                  <!-- 阶段轨的文字形式：读屏与色弱用户不能只靠图形定位。 -->
                  <div v-if="stageText(stage(row))" class="ut-rail__a11y">{{
                    stageText(stage(row))
                  }}</div>
                </template>
              </ElTableColumn>
              <ElTableColumn label="耗时" width="86">
                <template #default="{ row }">{{ elapsedText(row) }}</template>
              </ElTableColumn>
              <ElTableColumn label="结果" width="96">
                <template #default="{ row }">
                  <ElTag :type="resultTone(row)" size="small" effect="light">{{
                    resultLabel(row)
                  }}</ElTag>
                </template>
              </ElTableColumn>
            </ElTable>
            <div v-if="detail.list.length === 0" class="ut-empty">
              <span>{{ filter === '' ? '这个任务没有明细' : '当前筛选下没有设备' }}</span>
            </div>
          </template>
          <div v-else-if="!listLoading" class="ut-empty ut-empty--lg">
            <span>左侧选一个任务查看逐台进度</span>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
  import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
  import { useRoute, useRouter } from 'vue-router'
  import { ElMessage, ElTag } from 'element-plus'

  import { PermDeviceUpgrade } from '@/enums/permission'
  import { useAuth } from '@/hooks/core/useAuth'
  import { usePageIcon } from '@/hooks/core/usePageIcon'
  import {
    fetchAgentUpgradeTaskDetail,
    fetchAgentUpgradeTasks,
    type UpgradeTaskQuery
  } from '../api'
  import {
    formatUnixTime,
    sourceText,
    stageLabel,
    stageText,
    stageView,
    taskCountsText
  } from '../utils/upgrade'

  defineOptions({ name: 'DeviceUpgradeTask' })

  // 页头图标与侧边栏/页签同源（取菜单图标，改「菜单管理」即同步；见 usePageIcon）
  const pageIcon = usePageIcon('ri:list-check-2')

  const route = useRoute()
  const router = useRouter()
  const { hasAuth } = useAuth()
  const canUpgrade = computed(() => hasAuth(PermDeviceUpgrade))

  type TaskItem = Api.Device.AgentUpgradeTaskItem
  type AttemptItem = Api.Device.AgentUpgradeAttemptItem

  const tasks = ref<TaskItem[]>([])
  const taskTotal = ref(0)
  const page = ref(1)
  const pageSize = 15
  const listLoading = ref(false)
  const detail = ref<Api.Device.AgentUpgradeTaskDetailResp | null>(null)
  const selectedId = ref<string>('')
  const filter = ref<'' | 'active' | 'failed'>('')

  const listIntervalSec = 5
  const detailIntervalSec = 2
  let listTimer: ReturnType<typeof setInterval> | null = null
  let detailTimer: ReturnType<typeof setInterval> | null = null

  const subtitle = computed(() =>
    tasks.value.length > 0
      ? `共 ${taskTotal.value} 个任务 · 详情每 ${detailIntervalSec} 秒刷新`
      : `每 ${listIntervalSec} 秒自动刷新`
  )

  /** 任务是否已收口（后端在没有进行中且没有等待时写 finishedAt）。 */
  function isSettled(row: TaskItem): boolean {
    return !!row.finishedAt
  }

  function stage(row: AttemptItem) {
    return stageView(row.state, row.progress, row.reasonCode)
  }

  function resultLabel(row: AttemptItem): string {
    const v = stage(row)
    if (v.succeeded) return '已达成'
    if (v.rolledBack) return '已回滚'
    if (v.failed) return '失败'
    if (row.state === 'superseded') return '已取代'
    return '进行中'
  }

  function resultTone(row: AttemptItem): 'success' | 'danger' | 'warning' | 'info' {
    const v = stage(row)
    if (v.succeeded) return 'success'
    if (v.rolledBack || v.failed) return 'danger'
    if (row.state === 'superseded') return 'info'
    return 'warning'
  }

  /** 耗时：从开工到终结（未终结则到最近一次上报）。 */
  function elapsedText(row: AttemptItem): string {
    const start = row.startedAt ?? row.createdAt
    const end = row.finishedAt ?? row.lastReportAt ?? null
    if (!start) return '—'
    if (!end) return '进行中'
    const sec = Math.max(0, end - start)
    if (sec < 60) return `${sec} 秒`
    return `${Math.floor(sec / 60)} 分 ${sec % 60} 秒`
  }

  async function loadList(keepSelection = true) {
    listLoading.value = true
    try {
      const q: UpgradeTaskQuery = { page: page.value, pageSize }
      const res = await fetchAgentUpgradeTasks(q)
      tasks.value = res.list ?? []
      taskTotal.value = res.total ?? 0
      // URL 里的 id 优先（深链：刷新/分享后仍停在同一个任务）。
      const wanted = selectedId.value || (route.query.id as string) || ''
      const exists = tasks.value.some((t) => t.id === wanted)
      if (!exists && tasks.value.length > 0 && !keepSelection) {
        selectedId.value = tasks.value[0].id
      } else if (exists) {
        selectedId.value = wanted
      }
      if (!selectedId.value && tasks.value.length > 0) selectedId.value = tasks.value[0].id
    } catch (e) {
      ElMessage.error(errText(e, '任务列表加载失败'))
    } finally {
      listLoading.value = false
    }
  }

  async function loadDetail() {
    if (!selectedId.value) {
      detail.value = null
      return
    }
    try {
      detail.value = await fetchAgentUpgradeTaskDetail(selectedId.value, { filter: filter.value })
    } catch (e) {
      // 详情轮询失败只提示一次即可：静默重试由下一轮承担（不打扰正在看的人）。
      ElMessage.error(errText(e, '任务详情加载失败'))
    }
  }

  function onSelectTask(row: TaskItem | null) {
    if (!row || row.id === selectedId.value) return
    selectedId.value = row.id
    // 深链：把当前任务写进 URL（刷新/分享后仍停在这里）。
    void router.replace({ query: { ...route.query, id: row.id } })
    filter.value = ''
    void loadDetail()
  }

  function onPageChange(p: number) {
    page.value = p
    void loadList(false)
  }

  function reloadAll() {
    void loadList()
    void loadDetail()
  }

  /** 失败/回滚/超时的设备 ID（「重试失败项」用它填进批量下发对话框）。 */
  const failedDeviceIds = computed(() => {
    if (!detail.value) return [] as string[]
    return detail.value.list
      .filter((r) => ['failed', 'rolled_back', 'timeout'].includes(r.state))
      .map((r) => r.deviceId)
  })

  /**
   * 重试失败项 = 回到设备列表页并用既有的批量下发流程重新下发。
   *
   * 刻意**不新增后端重试端点**：重试的语义就是「再下发一次到同一个版本」，
   * 复用批量下发既有的校验（产物存在、平台匹配、影响面确认）比另开一条路径更安全
   * —— 另开一条就会在「跳过哪几类」上与主路径分叉。
   */
  function retryFailed() {
    if (!detail.value) return
    void router.push({
      name: 'DeviceList',
      query: {
        retryIds: failedDeviceIds.value.join(','),
        retryVersion: detail.value.task.targetVersion
      }
    })
  }

  function errText(e: unknown, fallback: string): string {
    const msg = (e as { message?: string })?.message
    return msg && msg.trim() !== '' ? msg : fallback
  }

  onMounted(async () => {
    await loadList()
    await loadDetail()
    // 列表 5 秒、详情 2 秒（升级是低频秒级操作，2 秒延迟不可感知；
    // 引入 WS 推送的复杂度与收益不成比例——见设计 §6）。
    listTimer = setInterval(() => void loadList(), listIntervalSec * 1000)
    detailTimer = setInterval(() => void loadDetail(), detailIntervalSec * 1000)
  })

  // 离开页面即停：轮询不该在后台继续烧请求。
  onBeforeUnmount(() => {
    if (listTimer) clearInterval(listTimer)
    if (detailTimer) clearInterval(detailTimer)
  })
</script>

<style lang="scss" scoped>
  @use '../views/device-tokens' as t;

  @include t.rise-keyframes;
  @include t.pulse-keyframes;

  .do-hero {
    &__icon {
      @include t.hero-icon;
      width: 40px;
      height: 40px;
      font-size: 20px;
      border-radius: 12px;
    }
  }

  .do-card {
    @include t.card;
    @include t.rise;
  }

  .dark .do-card {
    @include t.card-dark;
  }

  .ut-grid {
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    gap: 16px;

    @media (min-width: 1280px) {
      grid-template-columns: 26rem minmax(0, 1fr);
    }
  }

  .ut-list,
  .ut-detail {
    min-width: 0;
  }

  .ut-list__head {
    display: flex;
    align-items: baseline;
    gap: 8px;
    margin-bottom: 8px;
  }

  .ut-list__title {
    font-size: 14px;
    font-weight: 600;
    color: var(--el-text-color-primary);
  }

  .ut-list__hint {
    font-size: 11px;
    color: var(--el-text-color-secondary);
  }

  .ut-list__pager {
    margin-top: 8px;
    justify-content: flex-end;
  }

  .ut-detail__head {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 10px;
  }

  .ut-detail__title {
    font-size: 14px;
    font-weight: 600;
    color: var(--el-text-color-primary);
  }

  .ut-detail__meta {
    margin-left: 6px;
    font-size: 12px;
    font-weight: 400;
    color: var(--el-text-color-secondary);
  }

  .ut-detail__counts {
    margin-top: 2px;
    font-size: 12px;
    color: var(--el-text-color-regular);
  }

  .ut-detail__actions {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .ut-mono {
    font-variant-numeric: tabular-nums;
    font-family: var(--art-font-family-mono, monospace);
    font-size: 12px;
  }

  .ut-muted {
    font-size: 11px;
    color: var(--el-text-color-secondary);
  }

  .ut-counts {
    color: var(--el-text-color-regular);
  }

  .ut-done {
    margin-left: 6px;
    font-size: 11px;
    color: var(--el-color-success);
  }

  /* ── 阶段轨（本页的签名元素）─────────────────────────────────────
     四个节点 + 连接段：位置表达「卡在哪一步」，比一根连续百分比条诚实
     （校验/替换/重启没有天然刻度）。活跃节点脉动，reduced-motion 下静止。 */
  .ut-rail {
    display: flex;
    align-items: center;
    gap: 4px;
    font-size: 11px;
    color: var(--el-text-color-secondary);

    &__node {
      width: 8px;
      height: 8px;
      border-radius: 50%;
      border: 1px solid var(--el-border-color);
      background: transparent;
      flex: none;

      &.is-done {
        border-color: var(--el-color-success);
        background: var(--el-color-success);
      }

      &.is-now {
        border-color: var(--el-color-primary);
        background: var(--el-color-primary);
        animation: dev-pulse 1.6s ease-in-out infinite;
      }

      &.is-bad {
        border-color: var(--el-color-danger);
        background: var(--el-color-danger);
      }
    }

    &__seg {
      width: 16px;
      height: 1px;
      background: var(--el-border-color);
      flex: none;

      &.is-done {
        background: var(--el-color-success);
      }
    }

    &__label {
      margin-right: 4px;
      white-space: nowrap;
    }

    &__a11y {
      /* 视觉上不占位（阶段轨已表达），但读屏能读到「第 2/4 步：校验」。 */
      position: absolute;
      width: 1px;
      height: 1px;
      overflow: hidden;
      clip: rect(0 0 0 0);
      white-space: nowrap;
    }
  }

  .ut-desc {
    margin-top: 2px;
    font-size: 12px;
    color: var(--el-text-color-regular);
    overflow-wrap: anywhere;
  }

  .ut-empty {
    padding: 12px 0;
    text-align: center;
    font-size: 12px;
    color: var(--el-text-color-secondary);

    &--lg {
      padding: 48px 0;
      font-size: 13px;
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .ut-rail__node.is-now {
      animation: none;
    }
  }
</style>
