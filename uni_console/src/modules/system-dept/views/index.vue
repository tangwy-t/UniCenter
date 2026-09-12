<template>
  <div class="dept-page art-full-height">
    <!-- 搜索栏 -->
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
        layout="refresh,size,fullscreen,settings"
        @refresh="loadList"
      >
        <template #left>
          <ArtButtonTable v-perm="PermDeptAdd" type="add" title="新增部门" @click="openDialog()" />
          <!-- 排序有改动时出现(样式与角色管理一致) -->
          <ArtButtonTable
            v-if="sortDirtyCount > 0"
            v-perm="PermDeptSort"
            icon="ri:save-2-line"
            iconClass="bg-warning/12 text-warning"
            :title="`保存排序 (${sortDirtyCount})`"
            @click="onSaveSort"
          />
          <ArtButtonTable
            icon="ri:arrow-up-down-line"
            iconClass="bg-info/12 text-info"
            :title="expanded ? '折叠' : '展开'"
            @click="toggleExpandAll"
          />
        </template>
      </ArtTableHeader>

      <ArtTable
        :loading="loading"
        :data="data"
        :columns="columns"
        row-key="id"
        :tree-props="{ children: 'children' }"
        :expand-row-keys="expandedKeys"
        :indent="22"
        @expand-change="onExpandChange"
      >
        <template v-if="sortColReady" #sort="{ row }">
          <el-input-number
            v-model="row.sort"
            :min="0"
            :max="9999"
            controls-position="right"
            style="width: 96px"
            @change="onRowSortChange(row)"
          />
        </template>
      </ArtTable>

      <DeptDialog ref="dialog" @saved="loadList" />
    </ElCard>
  </div>
</template>

<script setup lang="ts">
  import { PermDeptAdd, PermDeptSort } from '@/enums/permission'
  import { computed, ref, h } from 'vue'
  import { ElMessage, ElMessageBox } from 'element-plus'
  import { useTableColumns } from '@/hooks/core/useTableColumns'
  import { useAuth } from '@/hooks/core/useAuth'
  import { useDict } from '@/hooks/core/useDict'
  import { useTreeExpand } from '@/hooks/core/useTreeExpand'
  import { operationColumn } from '@/components/core/tables/operation-column'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import { fetchDepts, removeDept, updateDeptSort } from '../api'
  import DeptDialog from './dept-dialog.vue'

  defineOptions({ name: 'SystemDept' })

  const { hasAuth } = useAuth()
  const loading = ref(false)
  const data = ref<Api.System.Dept[]>([])
  const dialog = ref<InstanceType<typeof DeptDialog>>()
  const sortColReady = ref(true)

  const statusDict = useDict('sys_dept_status', { numeric: true })

  // 树形展开状态（受控 + 记忆折叠，见 useTreeExpand）。
  const { expandedKeys, expanded, initExpanded, onExpandChange, toggleExpandAll } =
    useTreeExpand(data)

  // 搜索表单
  const searchForm = ref<{ name?: string; status?: number }>({})
  const showSearchBar = ref(false)

  const searchItems = computed(() => [
    {
      key: 'name',
      label: '部门名称',
      type: 'input',
      placeholder: '请输入部门名称',
      clearable: true
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

  async function loadList() {
    loading.value = true
    try {
      const [list] = await Promise.all([
        fetchDepts({
          ...(searchForm.value.name ? { name: searchForm.value.name } : {}),
          ...(searchForm.value.status !== undefined && searchForm.value.status !== null
            ? { status: searchForm.value.status }
            : {})
        }),
        statusDict.ensure()
      ])
      data.value = list
      // 首次加载：默认全展开（由 useTreeExpand 记录）；此后保持用户手动折叠状态。
      initExpanded()
      resetSortDirty()
    } finally {
      loading.value = false
    }
  }

  function handleSearch() {
    loadList()
  }

  function handleReset() {
    loadList()
  }

  function openDialog(row?: Api.System.Dept, parentId?: string) {
    dialog.value?.open(row, parentId)
  }

  function hasChildren(row: Api.System.Dept) {
    return !!row.children?.length
  }

  async function onRemove(row: Api.System.Dept) {
    await ElMessageBox.confirm(`确认删除部门「${row.name}」吗？`, '删除确认', {
      type: 'warning',
      confirmButtonText: '确定',
      cancelButtonText: '取消'
    })
    await removeDept(row.id)
    ElMessage.success('已删除')
    await loadList()
  }

  // —— 行内排序编辑 + 批量保存(有改动时出现,计数逻辑同角色管理) ——
  const sortDirty = new Set<string>()
  const sortDirtyCount = ref(0)

  function onRowSortChange(row: Api.System.Dept) {
    if (row.sort === null || row.sort === undefined) row.sort = 0
    sortDirty.add(row.id)
    sortDirtyCount.value = sortDirty.size
  }

  function resetSortDirty() {
    sortDirty.clear()
    sortDirtyCount.value = 0
  }

  async function onSaveSort() {
    if (sortDirty.size === 0) {
      ElMessage.warning('未检测到排序修改')
      return
    }
    const flat: Api.System.Dept[] = []
    const collect = (list: Api.System.Dept[]) =>
      list.forEach((n) => {
        flat.push(n)
        if (n.children?.length) collect(n.children)
      })
    collect(data.value)

    const changed = flat.filter((n) => sortDirty.has(n.id))
    try {
      await updateDeptSort(changed.map((n) => ({ id: n.id, sort: n.sort })))
    } catch {
      return
    }
    ElMessage.success('排序保存成功')
    resetSortDirty()
    await loadList()
  }

  const { columns, columnChecks } = useTableColumns<Api.System.Dept>(() => {
    const operationColumnConfig = operationColumn<Api.System.Dept>({
      count: (hasAuth('system:dept:edit') ? 1 : 0) + (hasAuth('system:dept:add') ? 1 : 0) + 1,
      formatter: (row) =>
        h('div', { class: 'flex items-center' }, [
          h(ArtButtonTable, {
            type: 'edit',
            title: '修改',
            auth: 'system:dept:edit',
            onClick: () => openDialog(row)
          }),
          h(ArtButtonTable, {
            icon: 'ri:add-fill',
            iconClass: 'bg-theme/12 text-theme',
            title: '新增',
            auth: 'system:dept:add',
            onClick: () => openDialog(undefined, row.id)
          }),
          hasChildren(row)
            ? h(ArtButtonTable, {
                icon: 'ri:delete-bin-5-line',
                iconClass: 'bg-g-300/55 text-g-700',
                style: { opacity: 0.5, cursor: 'not-allowed' },
                title: '请先删除子部门'
              })
            : h(ArtButtonTable, {
                type: 'delete',
                title: '删除',
                auth: 'system:dept:delete',
                onClick: () => onRemove(row)
              })
        ])
    })

    return [
      {
        prop: 'name',
        label: '部门名称',
        minWidth: 220,
        showOverflowTooltip: true
      },
      {
        prop: 'sort',
        label: '排序',
        width: 150,
        align: 'center',
        useSlot: true,
        slotName: 'sort'
      },
      { prop: 'leader', label: '负责人', width: 110 },
      { prop: 'phone', label: '联系电话', width: 140 },
      { prop: 'email', label: '邮箱', minWidth: 160 },
      {
        prop: 'status',
        label: '状态',
        width: 80,
        align: 'center',
        formatter: (row) => statusDict.render(row.status)
      },
      ...(operationColumnConfig ? [operationColumnConfig] : [])
    ]
  })

  loadList()
</script>
