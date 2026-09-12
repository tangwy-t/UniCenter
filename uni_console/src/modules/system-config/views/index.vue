<template>
  <div class="config-page art-full-height">
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
            v-perm="PermConfigAdd"
            type="add"
            title="新增参数"
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

    <ConfigDialog ref="dialog" @saved="loadList" />
  </div>
</template>

<script setup lang="ts">
  import { PermConfigAdd } from '@/enums/permission'
  import { reactive, ref, h } from 'vue'
  import { ElMessage, ElMessageBox } from 'element-plus'
  import { useTableColumns } from '@/hooks/core/useTableColumns'
  import { useAuth } from '@/hooks/core/useAuth'
  import { useDict } from '@/hooks/core/useDict'
  import { operationColumn } from '@/components/core/tables/operation-column'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import { fetchConfigs, removeConfig } from '../api'
  import ConfigDialog from './config-dialog.vue'

  defineOptions({ name: 'SystemConfig' })

  const { hasAuth } = useAuth()
  const searchForm = ref<{ configKey?: string }>({})
  const showSearchBar = ref(false)
  const searchItems = [
    {
      key: 'configKey',
      label: '参数键',
      type: 'input',
      placeholder: '请输入参数键',
      clearable: true
    }
  ]

  const loading = ref(false)
  const data = ref<Api.Config.Config[]>([])
  const pagination = reactive({ current: 1, size: 10, total: 0 })
  const dialog = ref<InstanceType<typeof ConfigDialog>>()

  const statusDict = useDict('sys_config_status', { numeric: true })
  const typeLabels: Record<string, string> = { S: '字符串', N: '数值', B: '布尔', J: 'JSON' }

  async function loadList() {
    loading.value = true
    try {
      const [res] = await Promise.all([
        fetchConfigs({
          page: pagination.current,
          pageSize: pagination.size,
          ...(searchForm.value.configKey ? { configKey: searchForm.value.configKey } : {})
        }),
        statusDict.ensure()
      ])
      data.value = res.list
      pagination.total = res.total
    } finally {
      loading.value = false
    }
  }

  function openDialog(row?: Api.Config.Config) {
    dialog.value?.open(row)
  }

  async function onRemove(row: Api.Config.Config) {
    await ElMessageBox.confirm(`确认删除参数「${row.configKey}」吗？`, '删除确认', {
      type: 'warning',
      confirmButtonText: '确定',
      cancelButtonText: '取消'
    })
    await removeConfig(row.id)
    ElMessage.success('已删除')
    await loadList()
  }

  const { columns, columnChecks } = useTableColumns<Api.Config.Config>(() => {
    const operationColumnConfig = operationColumn<Api.Config.Config>({
      count: (hasAuth('system:config:edit') ? 1 : 0) + (hasAuth('system:config:delete') ? 1 : 0),
      formatter: (row) =>
        h('div', { class: 'flex items-center' }, [
          h(ArtButtonTable, {
            type: 'edit',
            title: '编辑',
            auth: 'system:config:edit',
            onClick: () => openDialog(row)
          }),
          h(ArtButtonTable, {
            type: 'delete',
            title: '删除',
            auth: 'system:config:delete',
            onClick: () => onRemove(row)
          })
        ])
    })

    return [
      { type: 'index', width: 60, label: '序号' },
      { prop: 'name', label: '参数名称', minWidth: 140 },
      { prop: 'configKey', label: '参数键', minWidth: 180 },
      { prop: 'configValue', label: '参数值', minWidth: 140, showOverflowTooltip: true },
      {
        prop: 'configType',
        label: '类型',
        width: 90,
        formatter: (row) => typeLabels[row.configType] ?? row.configType
      },
      {
        prop: 'status',
        label: '状态',
        width: 90,
        formatter: (row) => statusDict.render(row.status)
      },
      { prop: 'remark', label: '备注', minWidth: 140, showOverflowTooltip: true },
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
