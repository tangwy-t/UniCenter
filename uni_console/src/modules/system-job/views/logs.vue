<template>
  <div class="job-log-page art-full-height">
    <div class="job-log-title">
      任务日志<span v-if="jobName"> - {{ jobName }}</span>
    </div>

    <ArtSearchBar
      v-show="showSearchBar"
      v-model="searchForm"
      :items="searchItems"
      @search="handleSearch"
      @reset="handleReset"
    />

    <ElCard class="art-table-card" shadow="never">
      <ArtTableHeader
        v-model:showSearchBar="showSearchBar"
        :loading="loading"
        layout="refresh,size,fullscreen,settings"
        @refresh="loadList"
      >
        <template #left>
          <ArtButtonTable
            icon="ri:arrow-left-line"
            iconClass="bg-g-300/55 text-g-700"
            title="返回"
            @click="back"
          />
          <ArtButtonTable
            v-perm="PermJobLogDelete"
            icon="ri:delete-bin-5-line"
            iconClass="bg-danger/12 text-danger"
            title="清理日志"
            @click="cleanupVisible = true"
          />
        </template>
      </ArtTableHeader>

      <ArtTable
        :loading="loading"
        :data="list"
        :columns="columns"
        :pagination="pagination"
        @pagination:size-change="handleSizeChange"
        @pagination:current-change="handleCurrentChange"
      >
        <template #error-info="{ row }">
          <span v-if="row.status === 2" class="error-brief">
            {{ row.errorMsg || '执行失败' }}
          </span>
          <span v-else class="text-g-500">—</span>
        </template>
      </ArtTable>
    </ElCard>

    <!-- 执行详情 -->
    <el-dialog v-model="detailVisible" title="执行详情" width="560px">
      <div v-if="detail" class="detail-box">
        <div class="detail-grid">
          <div class="detail-item">
            <span class="detail-label">任务</span>
            <span class="detail-value">{{ detail.jobName }}</span>
          </div>
          <div class="detail-item">
            <span class="detail-label">调用目标</span>
            <span class="detail-value font-mono">{{ detail.invokeTarget }}</span>
          </div>
          <div class="detail-item">
            <span class="detail-label">触发方式</span>
            <span class="detail-value">{{ triggerDict.labelOf(detail.triggerType) }}</span>
          </div>
          <div class="detail-item">
            <span class="detail-label">开始时间</span>
            <span class="detail-value">{{ detail.startTime }}</span>
          </div>
          <div class="detail-item">
            <span class="detail-label">结束时间</span>
            <span class="detail-value">{{ detail.endTime || '—' }}</span>
          </div>
          <div class="detail-item">
            <span class="detail-label">耗时</span>
            <span class="detail-value">{{ formatCost(detail.costTime) }}</span>
          </div>
          <div class="detail-item">
            <span class="detail-label">状态</span>
            <span class="detail-value" :class="statusClass(detail.status)">
              {{ statusDict.labelOf(detail.status) }}
            </span>
          </div>
        </div>
        <div v-if="detail.errorMsg" class="detail-error">
          <div class="detail-error-head">
            <span>错误信息</span>
            <ArtButtonTable
              icon="ri:file-copy-line"
              iconClass="bg-g-300/55 text-g-700"
              title="复制"
              @click="copyText(detail.errorMsg)"
            />
          </div>
          <pre class="detail-error-body font-mono">{{ detail.errorMsg }}</pre>
        </div>
      </div>
    </el-dialog>

    <!-- 清理日志 -->
    <el-dialog v-model="cleanupVisible" title="清理任务日志" width="460px">
      <div class="cleanup-box">
        <el-radio-group v-model="cleanupRange" class="cleanup-radios">
          <el-radio value="7">清理 7 天前</el-radio>
          <el-radio value="30">清理 30 天前</el-radio>
          <el-radio value="all">清理全部</el-radio>
          <el-radio value="custom">自定义日期之前</el-radio>
        </el-radio-group>
        <el-date-picker
          v-if="cleanupRange === 'custom'"
          v-model="cleanupDate"
          type="date"
          value-format="YYYY-MM-DD"
          placeholder="选择日期"
          :clearable="false"
          class="cleanup-date"
        />
        <div class="cleanup-hint">
          <ArtSvgIcon icon="ri:information-line" />
          仅清理当前任务在此时间点之前的执行日志，操作不可恢复
        </div>
      </div>
      <template #footer>
        <div class="art-dialog-footer">
          <ArtButtonTable
            icon="ri:close-line"
            iconClass="bg-g-300/55 text-g-700"
            title="取消"
            @click="cleanupVisible = false"
          />
          <ArtButtonTable
            icon="ri:delete-bin-5-line"
            iconClass="bg-danger/12 text-danger"
            title="确认清理"
            @click="onCleanup"
          />
        </div>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
  import { PermJobLogDelete } from '@/enums/permission'
  import { reactive, ref, h } from 'vue'
  import { useRoute, useRouter } from 'vue-router'
  import { ElMessage } from 'element-plus'
  import { useTableColumns } from '@/hooks/core/useTableColumns'
  import { useAuth } from '@/hooks/core/useAuth'
  import { useDict } from '@/hooks/core/useDict'
  import { useDictStore } from '@/store/modules/dict'
  import { operationColumn } from '@/components/core/tables/operation-column'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtSvgIcon from '@/components/core/base/art-svg-icon/index.vue'
  import { fetchJob, fetchJobLogs, deleteJobLogs } from '../api'
  import { useJobTargets } from '../composables/useJobTargets'

  defineOptions({ name: 'SystemJobLogs' })

  const { hasAuth } = useAuth()
  const route = useRoute()
  const router = useRouter()

  const jobId = (route.params.jobId as string) || ''
  const jobName = ref('')
  const loading = ref(false)
  const list = ref<Api.Job.JobLog[]>([])
  const pagination = reactive({ current: 1, size: 10, total: 0 })
  const showSearchBar = ref(false)

  const statusDict = useDict('sys_job_log_status', { numeric: true })
  const triggerDict = useDict('sys_job_log_trigger', { numeric: true })
  const dictStore = useDictStore()
  const { ensure: ensureTargets, targets } = useJobTargets()

  const searchForm = ref<{ status?: number; timeRange?: [string, string] }>({})
  const searchItems = computed(() => [
    {
      key: 'status',
      label: '状态',
      type: 'select',
      placeholder: '全部',
      clearable: true,
      props: { options: statusDict.options.value }
    },
    {
      key: 'timeRange',
      label: '时间范围',
      type: 'daterange',
      placeholder: '开始日期 ~ 结束日期',
      valueFormat: 'YYYY-MM-DD',
      clearable: true
    }
  ])

  /** 详情抽屉 */
  const detailVisible = ref(false)
  const detail = ref<Api.Job.JobLog | null>(null)

  /** 清理日志 */
  const cleanupVisible = ref(false)
  const cleaning = ref(false)
  const cleanupRange = ref<string>('30')
  const cleanupDate = ref('')

  /** 存在"执行中"记录时每 15s 自动刷新，全部终态后停止 */
  let pollTimer: ReturnType<typeof setInterval> | null = null

  async function loadList() {
    if (!jobId) return
    loading.value = true
    try {
      const [res] = await Promise.all([
        fetchJobLogs(buildQuery()),
        statusDict.ensure(),
        triggerDict.ensure(),
        ensureTargets()
      ])
      list.value = res.list
      pagination.total = res.total
      schedulePolling()
    } finally {
      loading.value = false
    }
  }

  function buildQuery(): Api.Job.LogQuery {
    const [start, end] = searchForm.value.timeRange ?? []
    return {
      page: pagination.current,
      pageSize: pagination.size,
      jobId,
      ...(searchForm.value.status !== undefined && searchForm.value.status !== null
        ? { status: searchForm.value.status }
        : {}),
      ...(start ? { startTime: start } : {}),
      ...(end ? { endTime: end } : {})
    }
  }

  /** 加载任务名称用于标题展示（任务可能已删除，失败时标题降级为空） */
  async function loadJobName() {
    if (!jobId) return
    try {
      const job = await fetchJob(jobId)
      jobName.value = job.name
    } catch {
      /* 任务已删除等场景：仅影响标题，不影响日志列表 */
    }
  }

  /** 状态文字颜色:跟随 sys_job_log_status 字典 list_class(0 warning/1 success/2 danger) */
  function statusClass(status: number) {
    const cls = dictStore.items['sys_job_log_status']?.find(
      (i) => i.value === String(status)
    )?.list_class
    const map: Record<string, string> = {
      primary: 'text-theme',
      success: 'text-success',
      danger: 'text-danger',
      warning: 'text-warning',
      info: 'text-info'
    }
    return map[cls ?? ''] ?? 'text-warning'
  }

  function back() {
    router.push('/system/job')
  }

  function formatCost(ms: number) {
    if (ms == null || Number.isNaN(ms)) return '—'
    if (ms < 1000) return `${ms} ms`
    return `${(ms / 1000).toFixed(1)} s`
  }

  async function copyText(text: string) {
    try {
      await navigator.clipboard.writeText(text)
    } catch {
      const ta = document.createElement('textarea')
      ta.value = text
      ta.style.position = 'fixed'
      ta.style.opacity = '0'
      document.body.appendChild(ta)
      ta.select()
      document.execCommand('copy')
      document.body.removeChild(ta)
    }
    ElMessage.success('已复制')
  }

  /** 详情弹窗 */
  function openDetail(row: Api.Job.JobLog) {
    detail.value = row
    detailVisible.value = true
  }

  /** 清理确认与提交 */
  async function onCleanup() {
    if (cleaning.value) return
    let before: Date
    if (cleanupRange.value === 'all') {
      before = new Date()
    } else if (cleanupRange.value === 'custom') {
      if (!cleanupDate.value) {
        ElMessage.warning('请选择日期')
        return
      }
      before = new Date(`${cleanupDate.value}T00:00:00`)
    } else {
      before = new Date(Date.now() - Number(cleanupRange.value) * 86_400_000)
    }
    // 弹窗本身即确认界面(含"清理全部/日期范围"选项与不可恢复提示),
    // 不再叠加第二层 ElMessageBox 确认。
    cleaning.value = true
    try {
      await deleteJobLogs(before.toISOString())
      ElMessage.success('清理成功')
      cleanupVisible.value = false
      await loadList()
    } finally {
      cleaning.value = false
    }
  }

  /** 轮询调度：有执行中记录 → 15s；visibilitychange 时暂停 */
  function schedulePolling() {
    stopPolling()
    const hasRunning = list.value.some((row) => row.status === 0)
    if (!hasRunning || document.hidden) return
    pollTimer = setInterval(loadList, 15_000)
  }

  function stopPolling() {
    if (pollTimer) {
      clearInterval(pollTimer)
      pollTimer = null
    }
  }

  function onVisibilityChange() {
    if (document.hidden) stopPolling()
    else {
      loadList()
    }
  }

  const { columns } = useTableColumns<Api.Job.JobLog>(() => {
    const operationColumnConfig = operationColumn<Api.Job.JobLog>({
      count: hasAuth('system:job:log:list') ? 1 : 0,
      formatter: (row) =>
        h('div', { class: 'flex items-center' }, [
          h(ArtButtonTable, {
            type: 'view',
            title: '详情',
            auth: 'system:job:log:list',
            onClick: () => openDetail(row)
          })
        ])
    })

    return [
      { type: 'index', width: 55, label: '序号' },
      { prop: 'jobName', label: '任务名称', minWidth: 120, showOverflowTooltip: true },
      {
        prop: 'invokeTarget',
        label: '调用目标',
        minWidth: 150,
        formatter: (row) => {
          const displayName = targets.value.find((t) => t.target === row.invokeTarget)?.displayName
          return h('div', { class: 'target-cell' }, [
            h('div', { class: 'target-name-row' }, [
              h('span', { class: 'target-name' }, displayName || row.invokeTarget)
            ]),
            displayName ? h('div', { class: 'target-sub font-mono' }, row.invokeTarget) : null
          ])
        }
      },
      {
        prop: 'triggerType',
        label: '触发方式',
        width: 95,
        align: 'center',
        formatter: (row) => triggerDict.render(row.triggerType)
      },
      { prop: 'startTime', label: '开始时间', width: 165 },
      {
        prop: 'costTime',
        label: '耗时',
        width: 90,
        formatter: (row) =>
          h('span', { class: row.costTime > 10_000 ? 'text-danger' : '' }, formatCost(row.costTime))
      },
      {
        prop: 'status',
        label: '状态',
        width: 90,
        align: 'center',
        formatter: (row) => statusDict.render(row.status)
      },
      {
        prop: 'errorMsg',
        label: '错误信息',
        minWidth: 140,
        useSlot: true,
        slotName: 'error-info',
        showOverflowTooltip: true
      },
      ...(operationColumnConfig ? [operationColumnConfig] : [])
    ]
  })

  function handleSizeChange(size: number) {
    pagination.size = size
    pagination.current = 1
    loadList()
  }
  function handleCurrentChange(current: number) {
    pagination.current = current
    loadList()
  }
  function handleSearch() {
    pagination.current = 1
    loadList()
  }
  function handleReset() {
    pagination.current = 1
    loadList()
  }

  onMounted(() => {
    document.addEventListener('visibilitychange', onVisibilityChange)
    loadList()
    loadJobName()
  })

  onUnmounted(() => {
    stopPolling()
    document.removeEventListener('visibilitychange', onVisibilityChange)
  })
</script>

<style scoped>
  .job-log-title {
    margin-bottom: 12px;
    font-size: 16px;
    font-weight: 600;
    color: var(--art-gray-900, #323251);
  }

  .dark .job-log-title {
    color: var(--art-gray-900, #e3e3e8);
  }

  .error-brief {
    display: inline-block;
    max-width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--color-danger, #dc2626);
  }

  .target-cell {
    display: flex;
    flex-direction: column;
    justify-content: center;
    line-height: 1.45;
    min-width: 0;
  }

  .target-name-row {
    display: flex;
    align-items: center;
    gap: 4px;
    min-width: 0;
  }

  .target-name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 13px;
    color: var(--art-gray-900, #323251);
  }

  .dark .target-name {
    color: var(--art-gray-900, #e3e3e8);
  }

  .target-sub {
    font-size: 11px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    color: var(--color-g-500, #949eb7);
  }

  .detail-grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px 16px;
    margin-bottom: 14px;
  }

  .detail-item {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
  }

  .detail-label {
    font-size: 12px;
    color: var(--color-g-500, #949eb7);
  }

  .detail-value {
    font-size: 13px;
    color: var(--art-gray-900, #323251);
    word-break: break-all;
  }

  .dark .detail-value {
    color: var(--art-gray-900, #e3e3e8);
  }

  .detail-error {
    border: 1px solid var(--default-border, #e2e8ee);
    border-radius: 8px;
    overflow: hidden;
  }

  .detail-error-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 6px 10px;
    background: var(--art-gray-200, #f2f4f5);
    font-size: 13px;
    font-weight: 600;
    color: var(--color-danger, #dc2626);
  }

  .dark .detail-error-head {
    background: var(--art-gray-200, #17171c);
  }

  .detail-error-body {
    margin: 0;
    padding: 10px;
    max-height: 240px;
    overflow: auto;
    font-size: 12px;
    line-height: 1.6;
    color: var(--color-danger, #dc2626);
    white-space: pre-wrap;
    word-break: break-all;
  }

  .cleanup-box {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }

  .cleanup-radios {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 4px;
  }

  .cleanup-date {
    width: 200px;
    margin-left: 24px;
  }

  .cleanup-hint {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 12px;
    color: var(--color-g-500, #949eb7);
  }
</style>
