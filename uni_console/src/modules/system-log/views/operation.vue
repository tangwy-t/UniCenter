<template>
  <div class="operation-page art-full-height">
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
            v-perm="PermLogOperationDelete"
            type="delete"
            title="清空日志"
            @click="onClear"
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
  </div>
</template>

<script setup lang="ts">
  import { PermLogOperationDelete } from '@/enums/permission'
  import { reactive, ref, h, computed } from 'vue'
  import { ElMessage, ElMessageBox, ElTag } from 'element-plus'
  import { useTableColumns } from '@/hooks/core/useTableColumns'
  import { useAuth } from '@/hooks/core/useAuth'
  import { useDict } from '@/hooks/core/useDict'
  import { operationColumn } from '@/components/core/tables/operation-column'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import { fetchOperationLogs, clearOperationLogs } from '../api'

  defineOptions({ name: 'LogOperation' })

  const { hasAuth } = useAuth()
  const searchForm = ref<{ username?: string; module?: string; code?: number | string }>({})
  const showSearchBar = ref(false)

  const resultCodeDict = useDict('sys_opt_result_code', { numeric: true })

  const searchItems = computed(() => [
    {
      key: 'username',
      label: '用户名',
      type: 'input',
      placeholder: '请输入用户名',
      clearable: true
    },
    {
      key: 'module',
      label: '模块',
      type: 'input',
      placeholder: '请输入模块',
      clearable: true
    },
    {
      key: 'code',
      label: '结果码',
      type: 'select',
      placeholder: '全部结果',
      clearable: true,
      options: resultCodeDict.options.value
    }
  ])

  const loading = ref(false)
  const data = ref<Api.Log.OperationLog[]>([])
  const pagination = reactive({ current: 1, size: 10, total: 0 })

  /** 请求方式 → 标签色(REST 惯例:GET 蓝 / POST 绿 / PUT 橙 / DELETE 红,与 Swagger 等工具一致) */
  const METHOD_TAG_TYPE: Record<string, 'primary' | 'success' | 'warning' | 'danger'> = {
    GET: 'primary',
    POST: 'success',
    PUT: 'warning',
    DELETE: 'danger'
  }

  async function loadList() {
    loading.value = true
    try {
      const [res] = await Promise.all([
        fetchOperationLogs({
          page: pagination.current,
          pageSize: pagination.size,
          ...(searchForm.value.username ? { username: searchForm.value.username } : {}),
          ...(searchForm.value.module ? { module: searchForm.value.module } : {}),
          ...(searchForm.value.code !== undefined &&
          searchForm.value.code !== null &&
          searchForm.value.code !== ''
            ? { code: Number(searchForm.value.code) }
            : {})
        }),
        resultCodeDict.ensure()
      ])
      data.value = res.list
      pagination.total = res.total
    } finally {
      loading.value = false
    }
  }

  function detailField(label: string, value: string) {
    return h('div', { style: { marginBottom: '12px', width: '100%' } }, [
      h('div', { style: { fontWeight: '600', marginBottom: '4px' } }, label),
      h(
        'pre',
        {
          style: {
            margin: 0,
            padding: '8px 10px',
            background: 'var(--el-fill-color-light)',
            borderRadius: '4px',
            whiteSpace: 'pre-wrap',
            wordBreak: 'break-all',
            maxHeight: '160px',
            overflow: 'auto',
            fontSize: '12px',
            lineHeight: 1.6,
            width: '100%'
          }
        },
        value || '-'
      )
    ])
  }

  function viewDetail(row: Api.Log.OperationLog) {
    ElMessageBox.alert(
      h('div', { style: { textAlign: 'left', width: '100%' } }, [
        detailField('结果码', String(row.code)),
        detailField('请求参数', row.requestParams),
        detailField('返回结果', row.responseResult),
        detailField('错误信息', row.errorMsg)
      ]),
      '操作详情',
      {
        confirmButtonText: '关闭',
        // 固定弹窗宽度:信息少/多时弹窗与数据展示框都不随内容收缩
        customStyle: { width: '624px', maxWidth: 'calc(100vw - 48px)' },
        // 配合下面非 scoped 样式:撑开消息区 flex item,使 pre 数据框宽度固定
        customClass: 'op-detail-msgbox'
      }
    )
  }

  async function onClear() {
    try {
      const { value } = await ElMessageBox.prompt(
        '请输入截止日期，将清空该日期之前的所有操作日志',
        '清空日志',
        {
          confirmButtonText: '确定',
          cancelButtonText: '取消',
          inputPattern: /^\d{4}-\d{2}-\d{2}$/,
          inputErrorMessage: '请输入正确的日期格式（YYYY-MM-DD）',
          type: 'warning'
        }
      )
      await clearOperationLogs(value)
      ElMessage.success('已清空')
      await loadList()
    } catch {
      // 用户取消或校验未通过，忽略
    }
  }

  const { columns, columnChecks } = useTableColumns<Api.Log.OperationLog>(() => {
    const operationColumnConfig = operationColumn<Api.Log.OperationLog>({
      count: hasAuth('system:log:operation:list') ? 1 : 0,
      formatter: (row) =>
        h('div', { class: 'flex items-center' }, [
          h(ArtButtonTable, {
            type: 'view',
            title: '查看详情',
            auth: 'system:log:operation:list',
            onClick: () => viewDetail(row)
          })
        ])
    })

    return [
      { type: 'index', width: 60, label: '序号' },
      {
        prop: 'module',
        label: '系统模块',
        minWidth: 120,
        // 模块是分类信息:统一中性标签,不与状态列的语义色抢注意力
        formatter: (row) =>
          row.module ? h(ElTag, { type: 'info', effect: 'light' }, () => row.module) : '—'
      },
      { prop: 'operationType', label: '操作类型', minWidth: 120, showOverflowTooltip: true },
      { prop: 'username', label: '操作人员', width: 120 },
      { prop: 'ip', label: 'IP', width: 140 },
      {
        prop: 'requestMethod',
        label: '请求方式',
        width: 100,
        formatter: (row) => {
          const method = (row.requestMethod ?? '').toUpperCase()
          if (!method) return '—'
          return h(
            ElTag,
            { type: METHOD_TAG_TYPE[method] ?? 'info', effect: 'light' },
            () => method
          )
        }
      },
      { prop: 'requestUrl', label: '请求地址', minWidth: 180, showOverflowTooltip: true },
      {
        prop: 'code',
        label: '状态',
        width: 130,
        formatter: (row) => resultCodeDict.render(row.code)
      },
      {
        prop: 'costTime',
        label: '耗时',
        width: 100,
        formatter: (row) => `${row.costTime}ms`
      },
      { prop: 'operTime', label: '操作时间', width: 180 },
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

<style lang="scss">
  /* 操作详情弹窗:ElMessageBox 的消息区是 flex 容器(.el-message-box__container)的
     flex item(默认 flex:0 1 auto + min-width:0),宽度按内容收缩 —— 内容短的字段
     会让 pre 数据框变窄。flex:1 撑满容器,使三个 pre 框宽度固定、不随内容变化。
     注意:ElMessageBox 挂载在 body 上,scoped 样式无法命中,故不用 scoped。 */
  .op-detail-msgbox {
    .el-message-box__message {
      flex: 1 1 auto;
      width: 100%;
    }
  }
</style>
