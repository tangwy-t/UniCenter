<template>
  <div class="notice-page art-full-height">
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
          <ArtButtonTable
            v-perm="PermNoticeAdd"
            type="add"
            title="新增通知"
            @click="openDialog()"
          />
        </template>
      </ArtTableHeader>

      <ArtTable
        :loading="loading"
        :data="data"
        :columns="columns"
        :pagination="pagination"
        @pagination:size-change="handleSizeChange"
        @pagination:current-change="handleCurrentChange"
      />
    </ElCard>

    <NoticeDialog ref="dialog" @saved="loadList" />
    <NoticeDetail ref="detailRef" />
    <NoticeReadUsers ref="readUsersRef" />
  </div>
</template>

<script setup lang="ts">
  import { PermNoticeAdd } from '@/enums/permission'
  import { reactive, ref, h, computed } from 'vue'
  import { ElMessage, ElMessageBox } from 'element-plus'
  import { useTableColumns } from '@/hooks/core/useTableColumns'
  import { useDict } from '@/hooks/core/useDict'
  import { useAuth } from '@/hooks/core/useAuth'
  import { operationColumn } from '@/components/core/tables/operation-column'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtButtonMore from '@/components/core/forms/art-button-more/index.vue'
  import { fetchNotices, removeNotice, publishNotice, revokeNotice } from '../api'
  import NoticeDialog from './notice-dialog.vue'
  import NoticeDetail from '@/components/core/others/art-notice-detail/index.vue'
  import NoticeReadUsers from './read-users-dialog.vue'

  defineOptions({ name: 'SystemNotice' })

  const noticeTypeDict = useDict('sys_notice_type', { numeric: true })
  const statusDict = useDict('sys_notice_status', { numeric: true })
  const priorityDict = useDict('sys_notice_priority', { numeric: true })
  const targetTypeDict = useDict('sys_notice_publish_type', { numeric: true })
  const readStatusDict = useDict('sys_notice_read_status', { numeric: true })
  const { hasAuth } = useAuth()

  const searchForm = ref<{ title?: string; noticeType?: number; status?: number }>({})
  const showSearchBar = ref(false)
  const searchItems = computed(() => [
    {
      key: 'title',
      label: '公告标题',
      type: 'input',
      placeholder: '请输入公告标题',
      clearable: true
    },
    {
      key: 'noticeType',
      label: '类型',
      type: 'select',
      placeholder: '请选择类型',
      clearable: true,
      options: noticeTypeDict.options.value
    },
    {
      key: 'status',
      label: '状态',
      type: 'select',
      placeholder: '请选择状态',
      clearable: true,
      options: statusDict.options.value
    }
  ])

  const loading = ref(false)
  const data = ref<Api.Notice.Notice[]>([])
  const pagination = reactive({ current: 1, size: 10, total: 0 })
  const dialog = ref<InstanceType<typeof NoticeDialog>>()
  const detailRef = ref<InstanceType<typeof NoticeDetail>>()
  const readUsersRef = ref<InstanceType<typeof NoticeReadUsers>>()

  async function loadList() {
    loading.value = true
    try {
      const [res] = await Promise.all([
        fetchNotices({
          page: pagination.current,
          pageSize: pagination.size,
          ...(searchForm.value.title ? { title: searchForm.value.title } : {}),
          ...(searchForm.value.noticeType !== undefined
            ? { noticeType: searchForm.value.noticeType }
            : {}),
          ...(searchForm.value.status !== undefined ? { status: searchForm.value.status } : {})
        }),
        noticeTypeDict.ensure(),
        statusDict.ensure(),
        priorityDict.ensure(),
        targetTypeDict.ensure(),
        readStatusDict.ensure()
      ])
      data.value = res.list
      pagination.total = res.total
    } finally {
      loading.value = false
    }
  }

  function openDialog(row?: Api.Notice.Notice) {
    dialog.value?.open(row)
  }

  function openDetail(row: Api.Notice.Notice) {
    detailRef.value?.open(row)
  }

  function openReadUsers(row: Api.Notice.Notice) {
    readUsersRef.value?.open(row)
  }

  async function onRemove(row: Api.Notice.Notice) {
    await ElMessageBox.confirm(`确认删除通知「${row.title}」吗？`, '删除确认', {
      type: 'warning',
      confirmButtonText: '确定',
      cancelButtonText: '取消'
    })
    await removeNotice(row.id)
    ElMessage.success('已删除')
    await loadList()
  }

  async function onTogglePublish(row: Api.Notice.Notice) {
    const publishing = row.status !== 1
    await ElMessageBox.confirm(
      publishing ? `确认发布「${row.title}」吗？` : `确认撤回「${row.title}」吗？`,
      publishing ? '发布确认' : '撤回确认',
      {
        type: 'warning',
        confirmButtonText: '确定',
        cancelButtonText: '取消'
      }
    )
    if (publishing) await publishNotice(row.id)
    else await revokeNotice(row.id)
    ElMessage.success(publishing ? '已发布' : '已撤回')
    await loadList()
  }

  async function onMore(item: { key: string | number }, row: Api.Notice.Notice) {
    const key = String(item.key)
    if (key === 'detail') openDetail(row)
    else if (key === 'readUsers') openReadUsers(row)
    else if (key === 'toggle') await onTogglePublish(row)
    else if (key === 'delete') await onRemove(row)
  }

  const { columns, columnChecks } = useTableColumns<Api.Notice.Notice>(() => {
    const operationColumnConfig = operationColumn<Api.Notice.Notice>({
      count:
        (hasAuth('system:notice:edit') ? 1 : 0) +
        (hasAuth('system:notice:query') ||
        hasAuth('system:notice:publish') ||
        hasAuth('system:notice:delete')
          ? 1
          : 0),
      formatter: (row) =>
        h('div', { class: 'flex items-center' }, [
          hasAuth('system:notice:edit')
            ? h(ArtButtonTable, { type: 'edit', title: '编辑', onClick: () => openDialog(row) })
            : null,
          h(ArtButtonMore, {
            list: [
              {
                key: 'detail',
                label: '查看详情',
                icon: 'ri:eye-line',
                auth: 'system:notice:query'
              },
              {
                key: 'readUsers',
                label: '阅读用户',
                icon: 'ri:user-line',
                auth: 'system:notice:query'
              },
              row.status === 1
                ? {
                    key: 'toggle',
                    label: '撤回',
                    icon: 'ri:file-reduce-line',
                    color: '#e6a23c',
                    auth: 'system:notice:publish'
                  }
                : {
                    key: 'toggle',
                    label: '发布',
                    icon: 'ri:send-plane-2-line',
                    auth: 'system:notice:publish'
                  },
              {
                key: 'delete',
                label: '删除',
                icon: 'ri:delete-bin-5-line',
                color: 'var(--art-danger)',
                auth: 'system:notice:delete'
              }
            ],
            onClick: (item: { key: string | number }) => onMore(item, row)
          })
        ])
    })

    return [
      { type: 'index', width: 60, label: '序号' },
      {
        prop: 'title',
        label: '公告标题',
        minWidth: 220,
        showOverflowTooltip: true,
        formatter: (row) =>
          h('a', { class: 'notice-title-link', onClick: () => openDetail(row) }, row.title)
      },
      {
        prop: 'noticeType',
        label: '公告类型',
        width: 100,
        formatter: (row) => noticeTypeDict.render(row.noticeType)
      },
      {
        prop: 'status',
        label: '状态',
        width: 100,
        formatter: (row) => statusDict.render(row.status)
      },
      {
        prop: 'readStatus',
        label: '阅读状态',
        width: 100,
        formatter: (row) => (row.readStatus == null ? '—' : readStatusDict.render(row.readStatus))
      },
      {
        prop: 'priority',
        label: '优先级',
        width: 100,
        formatter: (row) => priorityDict.render(row.priority)
      },
      {
        prop: 'targetType',
        label: '接收范围',
        width: 180,
        showOverflowTooltip: true,
        formatter: (row) => {
          const label = targetTypeDict.labelOf(row.targetType)
          const extra = row.targetDesc && row.targetDesc !== label ? row.targetDesc : ''
          return extra
            ? h('div', { class: 'flex items-center gap-1.5' }, [
                targetTypeDict.render(row.targetType),
                h('span', { class: 'text-xs text-g-500' }, extra)
              ])
            : targetTypeDict.render(row.targetType)
        }
      },
      { prop: 'createBy', label: '创建者', width: 110, showOverflowTooltip: true },
      { prop: 'publishTime', label: '发布时间', width: 170 },
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

<style lang="scss" scoped>
  /* 公告标题链接:Ruoyi 风格 link-type,点击打开详情 */
  .notice-title-link {
    color: var(--el-color-primary);
    cursor: pointer;
    white-space: nowrap;
    transition: opacity 0.2s ease;

    &:hover {
      opacity: 0.75;
      text-decoration: underline;
      text-underline-offset: 3px;
    }
  }
</style>
