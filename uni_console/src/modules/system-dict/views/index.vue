<template>
  <div class="dict-type-page art-full-height">
    <!-- 搜索栏 -->
    <ArtSearchBar
      v-show="showSearchBar"
      v-model="searchForm"
      :items="searchItems"
      @search="handleSearch"
      @reset="handleReset"
    />

    <ElCard class="art-table-card" shadow="never">
      <!-- 表格头部 -->
      <ArtTableHeader
        v-model:columns="columnChecks"
        v-model:showSearchBar="showSearchBar"
        :loading="loading"
        @refresh="loadTypes"
      >
        <template #left>
          <ArtButtonTable
            v-perm="PermDictTypeAdd"
            type="add"
            title="新增字典"
            @click="openTypeDialog()"
          />
        </template>
      </ArtTableHeader>

      <!-- 表格 -->
      <ArtTable
        :loading="loading"
        :data="data"
        :columns="columns"
        :pagination="pagination"
        @pagination:size-change="handleSizeChange"
        @pagination:current-change="handleCurrentChange"
      />
    </ElCard>

    <!-- 类型编辑弹窗 -->
    <TypeDialog ref="typeDialog" @saved="loadTypes" />
  </div>
</template>

<script setup lang="ts">
  import { PermDictTypeAdd } from '@/enums/permission'
  import { reactive, ref, h } from 'vue'
  import { useRouter } from 'vue-router'
  import { ElMessage, ElMessageBox } from 'element-plus'
  import { useTableColumns } from '@/hooks/core/useTableColumns'
  import { useAuth } from '@/hooks/core/useAuth'
  import { operationColumn } from '@/components/core/tables/operation-column'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import { fetchDictTypes, removeDictType } from '../api'
  import { useDictStatus } from '../useDictStatus'
  import TypeDialog from './type-dialog.vue'

  defineOptions({ name: 'SystemDict' })

  const { hasAuth } = useAuth()
  const router = useRouter()

  // 搜索表单
  const searchForm = ref<{ code?: string; name?: string }>({})
  const showSearchBar = ref(false)
  const searchItems = [
    {
      key: 'name',
      label: '字典名称',
      type: 'input',
      placeholder: '请输入字典名称',
      clearable: true
    },
    {
      key: 'code',
      label: '字典编码',
      type: 'input',
      placeholder: '请输入字典编码',
      clearable: true
    }
  ]

  // 数据与分页
  const loading = ref(false)
  const data = ref<Api.Dict.DictType[]>([])
  const pagination = reactive({ current: 1, size: 10, total: 0 })
  const typeDialog = ref<InstanceType<typeof TypeDialog>>()

  const dictStatus = useDictStatus()

  async function loadTypes() {
    loading.value = true
    try {
      const [res] = await Promise.all([
        fetchDictTypes({
          page: pagination.current,
          pageSize: pagination.size,
          ...(searchForm.value.name ? { name: searchForm.value.name } : {}),
          ...(searchForm.value.code ? { code: searchForm.value.code } : {})
        }),
        dictStatus.ensure()
      ])
      data.value = res.list
      pagination.total = res.total
    } finally {
      loading.value = false
    }
  }

  // 跳转到字典数据页
  function goData(row: Api.Dict.DictType) {
    router.push(`/system/dict/${row.id}`)
  }

  function openTypeDialog(row?: Api.Dict.DictType) {
    typeDialog.value?.open(row)
  }

  async function onRemoveType(row: Api.Dict.DictType) {
    await ElMessageBox.confirm(`确认删除字典类型「${row.name}」及其全部字典数据吗？`, '删除确认', {
      type: 'warning',
      confirmButtonText: '确定',
      cancelButtonText: '取消'
    })
    await removeDictType(row.id)
    ElMessage.success('已删除')
    // 删除后若当前页为空，回退一页
    if (data.value.length === 1 && pagination.current > 1) pagination.current -= 1
    await loadTypes()
  }

  // 列配置（操作列使用图标按钮）
  const { columns, columnChecks } = useTableColumns<Api.Dict.DictType>(() => {
    const operationColumnConfig = operationColumn<Api.Dict.DictType>({
      count:
        (hasAuth('system:dict:data:list') ? 1 : 0) +
        (hasAuth('system:dict:type:edit') ? 1 : 0) +
        (hasAuth('system:dict:type:delete') ? 1 : 0),
      formatter: (row) =>
        h('div', { class: 'flex items-center' }, [
          h(ArtButtonTable, {
            icon: 'ri:list-unordered',
            iconClass: 'bg-info/12 text-info',
            title: '字典数据',
            auth: 'system:dict:data:list',
            onClick: () => goData(row)
          }),
          h(ArtButtonTable, {
            type: 'edit',
            title: '编辑',
            auth: 'system:dict:type:edit',
            onClick: () => openTypeDialog(row)
          }),
          h(ArtButtonTable, {
            type: 'delete',
            title: '删除',
            auth: 'system:dict:type:delete',
            onClick: () => onRemoveType(row)
          })
        ])
    })

    return [
      { type: 'index', width: 60, label: '序号' },
      { prop: 'name', label: '字典名称', minWidth: 140 },
      { prop: 'code', label: '字典编码', minWidth: 160 },
      {
        prop: 'status',
        label: '状态',
        width: 90,
        formatter: (row) => dictStatus.render(row.status)
      },
      { prop: 'remark', label: '备注', minWidth: 160, showOverflowTooltip: true },
      { prop: 'createdAt', label: '创建时间', width: 180 },
      ...(operationColumnConfig ? [operationColumnConfig] : [])
    ]
  })

  function handleSearch() {
    pagination.current = 1
    loadTypes()
  }

  function handleReset() {
    pagination.current = 1
    loadTypes()
  }

  function handleSizeChange(size: number) {
    pagination.size = size
    pagination.current = 1
    loadTypes()
  }

  function handleCurrentChange(current: number) {
    pagination.current = current
    loadTypes()
  }

  loadTypes()
</script>
