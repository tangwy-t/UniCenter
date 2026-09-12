<template>
  <div class="job-page art-full-height">
    <!-- 调度器健康条（30s 轮询，失败静默降级） -->
    <JobHealthStrip />

    <ArtSearchBar
      v-show="showSearchBar"
      v-model="searchForm"
      :items="searchItems"
      @search="handleSearch"
      @reset="handleReset"
    />

    <ElCard class="art-table-card" shadow="never">
      <ArtTableHeader
        v-model:columns="columnChecks"
        v-model:showSearchBar="showSearchBar"
        :loading="loading"
        @refresh="loadList"
      >
        <template #left>
          <ArtButtonTable v-perm="PermJobAdd" type="add" title="新增任务" @click="openDialog()" />
        </template>
      </ArtTableHeader>

      <ArtTable
        :loading="loading"
        :data="data"
        :columns="columns"
        :pagination="pagination"
        @pagination:size-change="handleSizeChange"
        @pagination:current-change="handleCurrentChange"
      >
        <!-- 状态：行内开关（唯一启停入口） -->
        <template #status="{ row }">
          <div class="status-cell">
            <ElSwitch
              :model-value="row.status === 1"
              :loading="togglingId === String(row.id)"
              :disabled="!canToggle(row) || togglingId !== null"
              :aria-label="`${statusDict.labelOf(row.status)}，点击${row.status === 1 ? '暂停' : '恢复'}任务 ${row.name}`"
              @update:model-value="(v: boolean | string | number) => onToggleStatus(v, row)"
            />
            <span :class="['status-text', row.status === 1 ? 'text-success' : 'text-g-500']">
              {{ statusDict.labelOf(row.status) }}
            </span>
          </div>
        </template>
      </ArtTable>
    </ElCard>

    <JobDialog ref="dialog" @saved="loadList" />
  </div>
</template>

<script setup lang="ts">
  import { PermJobAdd } from '@/enums/permission'
  import { reactive, ref, h, computed } from 'vue'
  import { ElMessage, ElMessageBox, ElSwitch, ElTooltip } from 'element-plus'
  import { useTableColumns } from '@/hooks/core/useTableColumns'
  import { useAuth } from '@/hooks/core/useAuth'
  import { useDict } from '@/hooks/core/useDict'
  import { useUserStore } from '@/store/modules/user'
  import { useRouter } from 'vue-router'
  import { operationColumn } from '@/components/core/tables/operation-column'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtButtonMore from '@/components/core/forms/art-button-more/index.vue'
  import ArtSvgIcon from '@/components/core/base/art-svg-icon/index.vue'
  import JobHealthStrip from '../components/job-health-strip.vue'
  import { fetchJobs, removeJob, resumeJob, pauseJob, runJob } from '../api'
  import { useJobTargets } from '../composables/useJobTargets'
  import { describeCron, formatRelative, formatShortTime } from '../utils/cron'
  import JobDialog from './job-dialog.vue'

  defineOptions({ name: 'SystemJob' })

  const { hasAuth } = useAuth()
  const searchForm = ref<Api.Job.Query>({})
  const showSearchBar = ref(false)
  const loading = ref(false)
  const data = ref<Api.Job.Job[]>([])
  const pagination = reactive({ current: 1, size: 10, total: 0 })
  const dialog = ref<InstanceType<typeof JobDialog>>()
  const router = useRouter()

  const statusDict = useDict('sys_job_status', { numeric: true })
  const concurrentDict = useDict('sys_job_concurrent', { numeric: true })
  const { ensure: ensureTargets, targets } = useJobTargets()

  /** 行内开关按目标动作校验权限：暂停需 pause 权限，恢复需 execute 权限（与旧版行为一致） */
  const userPerms = computed(() => useUserStore().info?.permissions ?? [])
  function canToggle(row: Api.Job.Job) {
    const perm = row.status === 1 ? 'system:job:pause' : 'system:job:execute'
    return userPerms.value.includes(perm)
  }

  /** 搜索项：新增状态筛选（字典选项） */
  const searchItems = computed(() => [
    {
      key: 'name',
      label: '任务名称',
      type: 'input',
      placeholder: '请输入任务名称',
      clearable: true
    },
    {
      key: 'jobGroup',
      label: '任务组',
      type: 'input',
      placeholder: '请输入任务组',
      clearable: true
    },
    {
      key: 'status',
      label: '状态',
      type: 'select',
      placeholder: '全部',
      clearable: true,
      props: { options: statusDict.options.value }
    }
  ])

  const togglingId = ref<string | null>(null)
  const runningOnceId = ref<string | null>(null)

  async function loadList() {
    loading.value = true
    try {
      const [res] = await Promise.all([
        fetchJobs({
          page: pagination.current,
          pageSize: pagination.size,
          ...(searchForm.value.name ? { name: searchForm.value.name } : {}),
          ...(searchForm.value.jobGroup ? { jobGroup: searchForm.value.jobGroup } : {}),
          ...(searchForm.value.status !== undefined && searchForm.value.status !== null
            ? { status: searchForm.value.status }
            : {})
        }),
        statusDict.ensure(),
        concurrentDict.ensure(),
        ensureTargets()
      ])
      data.value = res.list
      pagination.total = res.total
    } finally {
      loading.value = false
    }
  }

  function openDialog(row?: Api.Job.Job) {
    dialog.value?.open(row)
  }
  function openLog(row: Api.Job.Job) {
    router.push(`/system/job/${row.id}/logs`)
  }

  async function onRemove(row: Api.Job.Job) {
    await ElMessageBox.confirm(
      `确认删除任务「${row.name}」吗？删除后其执行日志仍保留。`,
      '删除确认',
      {
        type: 'warning',
        confirmButtonText: '确定',
        cancelButtonText: '取消'
      }
    )
    await removeJob(row.id)
    ElMessage.success('已删除')
    await loadList()
  }

  /** 行内开关切换启停（失败由全局 toast 提示，状态不变） */
  async function onToggleStatus(next: boolean | string | number, row: Api.Job.Job) {
    if (togglingId.value) return
    const enable = next === true || next === 1 || next === '1'
    const action = enable ? '恢复' : '暂停'
    await ElMessageBox.confirm(`确认${action}任务「${row.name}」吗？`, `${action}确认`, {
      type: 'warning',
      confirmButtonText: '确定',
      cancelButtonText: '取消'
    })
    togglingId.value = String(row.id)
    try {
      if (enable) await resumeJob(row.id)
      else await pauseJob(row.id)
      row.status = enable ? 1 : 0
      ElMessage.success(`已${action}「${row.name}」`)
    } finally {
      togglingId.value = null
      await loadList()
    }
  }

  /** 手动执行一次：后端为异步触发，提示语义修正为"已触发" */
  async function onRun(row: Api.Job.Job) {
    if (runningOnceId.value) return
    runningOnceId.value = String(row.id)
    try {
      await runJob(row.id)
      ElMessage.success(`已触发「${row.name}」，可在任务日志中查看执行结果`)
    } finally {
      runningOnceId.value = null
    }
  }

  async function onMore(item: { key: string | number }, row: Api.Job.Job) {
    const key = String(item.key)
    if (key === 'delete') await onRemove(row)
  }

  const { columns, columnChecks } = useTableColumns<Api.Job.Job>(() => {
    const operationColumnConfig = operationColumn<Api.Job.Job>({
      count:
        (hasAuth('system:job:edit') ? 1 : 0) +
        (hasAuth('system:job:once') ? 1 : 0) +
        (hasAuth('system:job:log:list') ? 1 : 0) +
        (hasAuth('system:job:delete') ? 1 : 0),
      formatter: (row) =>
        h('div', { class: 'flex items-center' }, [
          h(ArtButtonTable, {
            type: 'edit',
            title: '编辑',
            auth: 'system:job:edit',
            onClick: () => openDialog(row)
          }),
          h(ArtButtonTable, {
            icon: 'ri:play-circle-line',
            iconClass:
              runningOnceId.value === String(row.id)
                ? 'bg-g-300/55 text-g-700'
                : 'bg-info/12 text-info',
            title: runningOnceId.value === String(row.id) ? '执行中…' : '执行一次',
            auth: 'system:job:once',
            onClick: () => onRun(row)
          }),
          h(ArtButtonTable, {
            icon: 'ri:file-list-3-line',
            iconClass: 'bg-g-300/55 text-g-700',
            title: '日志',
            auth: 'system:job:log:list',
            onClick: () => openLog(row)
          }),
          h(ArtButtonMore, {
            list: [
              {
                key: 'delete',
                label: '删除',
                icon: 'ri:delete-bin-5-line',
                color: 'var(--art-danger)',
                auth: 'system:job:delete'
              }
            ],
            onClick: (item: { key: string | number }) => onMore(item, row)
          })
        ])
    })

    return [
      { type: 'index', width: 60, label: '序号' },
      { prop: 'name', label: '任务名称', minWidth: 140, showOverflowTooltip: true },
      { prop: 'jobGroup', label: '任务组', width: 100 },
      {
        prop: 'invokeTarget',
        label: '调用目标',
        minWidth: 160,
        formatter: (row) => {
          const displayName = targets.value.find((t) => t.target === row.invokeTarget)?.displayName
          const registered = targets.value.some((t) => t.target === row.invokeTarget)
          const warn = targets.value.length > 0 && !registered
          return h('div', { class: 'target-cell' }, [
            h('div', { class: 'target-name-row' }, [
              h('span', { class: 'target-name' }, displayName || row.invokeTarget),
              warn
                ? h(ArtSvgIcon, {
                    icon: 'ri:error-warning-line',
                    class: 'text-warning',
                    title: '该目标未在注册表中'
                  })
                : null
            ]),
            displayName ? h('div', { class: 'target-sub font-mono' }, row.invokeTarget) : null
          ])
        }
      },
      {
        prop: 'cronExpression',
        label: '执行周期',
        minWidth: 190,
        formatter: (row) => {
          const desc = describeCron(row.cronExpression) || '自定义表达式'
          return h('div', { class: 'cron-cell' }, [
            h('div', { class: 'cron-desc' }, desc),
            h(
              'div',
              {
                class: 'cron-raw font-mono',
                title: `原始表达式：${row.cronExpression}`
              },
              row.cronExpression
            )
          ])
        }
      },
      {
        prop: 'concurrent',
        label: '并发',
        width: 80,
        align: 'center',
        formatter: (row) => concurrentDict.render(row.concurrent)
      },
      { prop: 'status', label: '状态', width: 140, useSlot: true, slotName: 'status' },
      {
        prop: 'nextRunTime',
        label: '下次执行',
        width: 175,
        formatter: (row) => {
          const next = row.nextRunTime
          if (!next || row.status !== 1) {
            return h('span', { class: 'text-g-500' }, '—')
          }
          const relative = formatRelative(next)
          return h(
            ElTooltip,
            { content: relative, placement: 'top', disabled: !relative },
            {
              default: () =>
                h('span', { class: 'next-run' }, formatShortTime(new Date(next.replace(' ', 'T'))))
            }
          )
        }
      },
      ...(operationColumnConfig ? [operationColumnConfig] : [])
    ]
  })

  function handleSearch() {
    pagination.current = 1
    loadList()
  }
  function handleReset() {
    pagination.current = 1
    loadList()
  }
  function handleSizeChange(size: number) {
    pagination.size = size
    pagination.current = 1
    loadList()
  }
  function handleCurrentChange(current: number) {
    pagination.current = current
    loadList()
  }

  loadList()
</script>

<style scoped>
  .status-cell {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: nowrap;
  }

  .status-text {
    flex: none;
    font-size: 13px;
    font-weight: 500;
    white-space: nowrap;
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

  .cron-cell {
    display: flex;
    flex-direction: column;
    line-height: 1.5;
  }

  .cron-desc {
    font-size: 13px;
    color: var(--art-gray-900, #323251);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .dark .cron-desc {
    color: var(--art-gray-900, #e3e3e8);
  }

  .cron-raw {
    font-size: 11px;
    color: var(--color-g-500, #949eb7);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .next-run {
    font-feature-settings: 'tnum';
  }
</style>
