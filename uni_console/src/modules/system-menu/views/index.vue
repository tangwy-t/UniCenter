<template>
  <div class="menu-page art-full-height">
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
          <ArtButtonTable v-perm="PermMenuAdd" type="add" title="新增菜单" @click="openDialog()" />
          <!-- 排序有改动时出现(样式与角色管理一致) -->
          <ArtButtonTable
            v-if="sortDirtyCount > 0"
            v-perm="PermMenuSort"
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

      <MenuDialog ref="dialog" @saved="loadList" />
    </ElCard>
  </div>
</template>

<script setup lang="ts">
  import { PermMenuAdd, PermMenuSort } from '@/enums/permission'
  import { computed, ref, h } from 'vue'
  import { ElMessage, ElMessageBox, ElTag } from 'element-plus'
  import { useTableColumns } from '@/hooks/core/useTableColumns'
  import { useAuth } from '@/hooks/core/useAuth'
  import { useDict } from '@/hooks/core/useDict'
  import { useTreeExpand } from '@/hooks/core/useTreeExpand'
  import ArtSvgIcon from '@/components/core/base/art-svg-icon/index.vue'
  import { operationColumn } from '@/components/core/tables/operation-column'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import { fetchMenus, removeMenu, updateMenuSort } from '../api'
  import { MENU_TYPE_META, MENU_TYPE_OPTIONS } from '../constants'
  import MenuDialog from './menu-dialog.vue'

  defineOptions({ name: 'SystemMenu' })

  const { hasAuth } = useAuth()
  const loading = ref(false)
  const data = ref<Api.System.Menu[]>([])
  const dialog = ref<InstanceType<typeof MenuDialog>>()
  const sortColReady = ref(true)

  const statusDict = useDict('sys_menu_status', { numeric: true })
  const visibleDict = useDict('sys_show_hide', { numeric: true })

  // 类型可视化：目录=primary、菜单=success、按钮=warning（对齐 RuoYi）。
  const typeMeta = MENU_TYPE_META

  // 树形展开状态（受控 + 记忆折叠，见 useTreeExpand）。
  const { expandedKeys, expanded, initExpanded, onExpandChange, toggleExpandAll } =
    useTreeExpand(data)

  // 搜索表单
  const searchForm = ref<{ name?: string; status?: number; type?: string }>({})
  const showSearchBar = ref(false)

  const searchItems = computed(() => [
    {
      key: 'name',
      label: '菜单名称',
      type: 'input',
      placeholder: '请输入菜单名称',
      clearable: true
    },
    {
      key: 'type',
      label: '菜单类型',
      type: 'select',
      placeholder: '请选择类型',
      clearable: true,
      options: MENU_TYPE_OPTIONS
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
        fetchMenus({
          ...(searchForm.value.name ? { name: searchForm.value.name } : {}),
          ...(searchForm.value.type
            ? { type: searchForm.value.type as 'dir' | 'menu' | 'btn' }
            : {}),
          ...(searchForm.value.status !== undefined && searchForm.value.status !== null
            ? { status: searchForm.value.status }
            : {})
        }),
        statusDict.ensure(),
        visibleDict.ensure()
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

  function openDialog(row?: Api.System.Menu, parentId?: string) {
    dialog.value?.open(row, parentId)
  }

  function hasChildren(row: Api.System.Menu) {
    return !!row.children?.length
  }

  async function onRemove(row: Api.System.Menu) {
    if (hasChildren(row)) {
      ElMessage.warning('该菜单下存在子节点，请先删除其子菜单')
      return
    }
    await ElMessageBox.confirm(`确认删除菜单「${row.name}」吗？`, '删除确认', {
      type: 'warning',
      confirmButtonText: '确定',
      cancelButtonText: '取消'
    })
    await removeMenu(row.id)
    ElMessage.success('已删除')
    await loadList()
  }

  // —— 行内排序编辑 + 批量保存(有改动时出现,计数逻辑同角色管理) ——
  const sortDirty = new Set<string>()
  const sortDirtyCount = ref(0)

  function onRowSortChange(row: Api.System.Menu) {
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
    const flat: Api.System.Menu[] = []
    const collect = (list: Api.System.Menu[]) =>
      list.forEach((n) => {
        flat.push(n)
        if (n.children?.length) collect(n.children)
      })
    collect(data.value)

    const changed = flat.filter((n) => sortDirty.has(n.id))
    try {
      await updateMenuSort(changed.map((n) => ({ id: n.id, sort: n.sort })))
    } catch {
      return
    }
    ElMessage.success('排序保存成功')
    resetSortDirty()
    await loadList()
  }

  const { columns, columnChecks } = useTableColumns<Api.System.Menu>(() => {
    const operationColumnConfig = operationColumn<Api.System.Menu>({
      count: (hasAuth('system:menu:edit') ? 1 : 0) + (hasAuth('system:menu:add') ? 1 : 0) + 1,
      formatter: (row) =>
        h('div', { class: 'flex items-center' }, [
          h(ArtButtonTable, {
            type: 'edit',
            title: '修改',
            auth: 'system:menu:edit',
            onClick: () => openDialog(row)
          }),
          h(ArtButtonTable, {
            icon: 'ri:add-fill',
            iconClass: 'bg-theme/12 text-theme',
            title: '新增',
            auth: 'system:menu:add',
            onClick: () => openDialog(undefined, row.id)
          }),
          hasChildren(row)
            ? h(ArtButtonTable, {
                icon: 'ri:delete-bin-5-line',
                iconClass: 'bg-g-300/55 text-g-700',
                style: { opacity: 0.5, cursor: 'not-allowed' },
                title: '请先删除子菜单'
              })
            : h(ArtButtonTable, {
                type: 'delete',
                title: '删除',
                auth: 'system:menu:delete',
                onClick: () => onRemove(row)
              })
        ])
    })

    return [
      {
        prop: 'name',
        label: '菜单名称',
        minWidth: 220,
        showOverflowTooltip: true,
        formatter: (row) =>
          h(
            'div',
            {
              class: 'menu-name-cell',
              // 必须保持行内级（inline-flex 而非 flex），否则该 div 是块级元素，
              // 会被挤到展开箭头（行内元素）的下一行。
              style: {
                display: 'inline-flex',
                alignItems: 'center',
                verticalAlign: 'middle',
                maxWidth: '100%'
              }
            },
            [
              row.icon
                ? h(ArtSvgIcon, {
                    icon: row.icon,
                    class: 'menu-name-icon',
                    style: { flexShrink: 0 }
                  })
                : null,
              h('span', { class: 'ml5' }, row.name)
            ]
          )
      },
      {
        prop: 'type',
        label: '类型',
        width: 90,
        align: 'center',
        formatter: (row) => {
          const m = typeMeta[row.type] ?? { label: row.type, tag: 'info' as any }
          return h(ElTag, { type: m.tag, size: 'small' }, () => m.label)
        }
      },
      {
        prop: 'sort',
        label: '排序',
        width: 150,
        align: 'center',
        useSlot: true,
        slotName: 'sort'
      },
      { prop: 'perms', label: '权限标识', minWidth: 160, showOverflowTooltip: true },
      { prop: 'component', label: '组件路径', minWidth: 140, showOverflowTooltip: true },
      {
        prop: 'visible',
        label: '显示',
        width: 80,
        align: 'center',
        formatter: (row) => visibleDict.render(row.visible)
      },
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

<!-- 说明：.menu-name-cell 由 h() formatter 动态创建，并在子组件 el-table 内渲染，
     scoped 样式不会附加到该节点上——这正是此前 display:inline-flex 写在 <style scoped>
     中却始终未生效，图标/名称被挤到展开箭头下一行的根本原因。
     因此这里改用全局选择器（非 scoped）确保始终命中；类名在该页面内唯一，无副作用。 -->
<style>
  .el-table .cell .menu-name-cell {
    /* 必须保持行内级（inline-flex 而非 flex）：
       否则该 div 是块级元素，会被挤到展开箭头（行内元素）的下一行 */
    display: inline-flex;
    align-items: center;
    vertical-align: middle;
    max-width: 100%;
  }

  /* 树形展开箭头默认是 baseline 对齐的 inline-block，会相对内容略偏高；
     与内容统一用 vertical-align: middle，使其与图标/名称在同一水平线上。 */
  .el-table .cell .el-table__expand-icon {
    vertical-align: middle;
  }

  .el-table .cell .menu-name-cell .menu-name-icon {
    font-size: 16px;
    color: var(--el-text-color-regular);
    flex-shrink: 0;
  }

  .el-table .cell .menu-name-cell .ml5 {
    margin-left: 5px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
</style>
