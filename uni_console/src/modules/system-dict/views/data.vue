<template>
  <div class="dict-data-page art-full-height">
    <div class="dict-data-title"
      >字典数据<span v-if="typeName"> - {{ typeName }}</span></div
    >

    <ElCard class="art-table-card" shadow="never">
      <ArtTableHeader
        :loading="loading"
        layout="refresh,size,fullscreen,settings"
        @refresh="loadAll"
      >
        <template #left>
          <ArtButtonTable
            icon="ri:arrow-left-line"
            iconClass="bg-g-300/55 text-g-700"
            title="返回"
            @click="back"
          />
          <ArtButtonTable
            v-perm="PermDictDataAdd"
            type="add"
            title="新增数据"
            @click="openDataDialog()"
          />
        </template>
      </ArtTableHeader>

      <ArtTable :loading="loading" :data="dataList" :columns="columns" />
    </ElCard>

    <DataDialog ref="dataDialog" :type-id="typeId" @saved="loadAll" />
  </div>
</template>

<script setup lang="ts">
  import { PermDictDataAdd } from '@/enums/permission'
  import { ref, h } from 'vue'
  import { useRoute, useRouter } from 'vue-router'
  import { ElTag, ElMessage, ElMessageBox } from 'element-plus'
  import { useTableColumns } from '@/hooks/core/useTableColumns'
  import { useAuth } from '@/hooks/core/useAuth'
  import { operationColumn } from '@/components/core/tables/operation-column'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import { fetchDictType, fetchDictData, removeDictData } from '../api'
  import { useDictStatus } from '../useDictStatus'
  import DataDialog from './data-dialog.vue'

  defineOptions({ name: 'SystemDictData' })

  const { hasAuth } = useAuth()
  const route = useRoute()
  const router = useRouter()

  const typeId = (route.params.typeId as string) || ''
  const typeName = ref('')
  const loading = ref(false)
  const dataList = ref<Api.Dict.DictData[]>([])
  const dataDialog = ref<InstanceType<typeof DataDialog>>()

  const dictStatus = useDictStatus()

  // 把 listClass 转为 el-tag 的 type（用于「标签」列）；未配置样式时回落到 info 中性色。
  // 后端 listClass 契约类型为 string(生成契约),这里白名单收敛到 el-tag 支持的色板。
  const tagType = (c: string): 'primary' | 'success' | 'info' | 'warning' | 'danger' =>
    c === 'primary' || c === 'success' || c === 'warning' || c === 'danger' ? c : 'info'

  async function loadAll() {
    if (!typeId) return
    loading.value = true
    try {
      const [type, list] = await Promise.all([
        fetchDictType(typeId),
        fetchDictData(typeId),
        dictStatus.ensure()
      ])
      typeName.value = type.name
      dataList.value = list
    } finally {
      loading.value = false
    }
  }

  function back() {
    router.push('/system/dict')
  }

  function openDataDialog(row?: Api.Dict.DictData) {
    dataDialog.value?.open(row)
  }

  async function onRemoveData(row: Api.Dict.DictData) {
    await ElMessageBox.confirm(`确认删除字典数据「${row.label}」吗？`, '删除确认', {
      type: 'warning',
      confirmButtonText: '确定',
      cancelButtonText: '取消'
    })
    await removeDictData(typeId, row.id)
    ElMessage.success('已删除')
    await loadAll()
  }

  const { columns } = useTableColumns<Api.Dict.DictData>(() => {
    const operationColumnConfig = operationColumn<Api.Dict.DictData>({
      count:
        (hasAuth('system:dict:data:edit') ? 1 : 0) + (hasAuth('system:dict:data:delete') ? 1 : 0),
      formatter: (row) =>
        h('div', { class: 'flex items-center' }, [
          h(ArtButtonTable, {
            type: 'edit',
            title: '编辑',
            auth: 'system:dict:data:edit',
            onClick: () => openDataDialog(row)
          }),
          h(ArtButtonTable, {
            type: 'delete',
            title: '删除',
            auth: 'system:dict:data:delete',
            onClick: () => onRemoveData(row)
          })
        ])
    })

    return [
      { type: 'index', width: 60, label: '序号' },
      {
        prop: 'label',
        label: '标签',
        minWidth: 130,
        formatter: (row) =>
          h(ElTag, { type: tagType(row.listClass), effect: 'light' }, () => row.label)
      },
      { prop: 'value', label: '值', minWidth: 120 },
      { prop: 'sort', label: '排序', width: 80 },
      {
        prop: 'status',
        label: '状态',
        width: 90,
        formatter: (row) => dictStatus.render(row.status)
      },
      { prop: 'remark', label: '备注', minWidth: 160, showOverflowTooltip: true },
      ...(operationColumnConfig ? [operationColumnConfig] : [])
    ]
  })

  loadAll()
</script>

<style scoped>
  .dict-data-title {
    margin-bottom: 12px;
    font-size: 16px;
    font-weight: 600;
    color: var(--text-color, var(--art-gray-900));
  }
</style>
