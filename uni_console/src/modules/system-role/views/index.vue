<template>
  <div class="role-page art-full-height">
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
          <div class="toolbar-left">
            <ArtButtonTable
              v-perm="PermRoleAdd"
              type="add"
              title="新增角色"
              @click="openDialog()"
            />

            <!-- 排序有改动时出现（同菜单管理「保存排序」） -->
            <ArtButtonTable
              v-if="sortDirtyCount > 0"
              v-perm="PermRoleSort"
              icon="ri:save-2-line"
              iconClass="bg-warning/12 text-warning"
              :title="`保存排序 (${sortDirtyCount})`"
              @click="onSaveSort"
            />

            <!-- 批量操作（有勾选时出现） -->
            <template v-if="selectedRows.length">
              <ArtButtonTable
                v-perm="PermRoleDelete"
                icon="ri:delete-bin-5-line"
                iconClass="bg-danger/12 text-danger"
                :title="`批量删除 (${selectedRows.length})`"
                @click="onBatchRemove"
              />
              <ArtButtonTable
                icon="ri:close-line"
                iconClass="bg-g-300/55 text-g-700"
                title="清空选择"
                @click="clearSelection"
              />
            </template>
          </div>
        </template>
      </ArtTableHeader>

      <ArtTable
        ref="tableRef"
        :loading="loading"
        :data="data"
        :columns="columns"
        :pagination="pagination"
        :selectable="rowSelectable"
        @selection-change="onSelectionChange"
        @pagination:size-change="handleSizeChange"
        @pagination:current-change="handleCurrentChange"
      >
        <!-- 行内可编辑排序，同菜单管理 -->
        <template #sort="{ row }">
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
    </ElCard>

    <RoleDialog ref="dialog" @saved="loadList" />
    <RoleUserDrawer ref="userDrawer" />
  </div>
</template>

<script setup lang="ts">
  import { PermRoleAdd, PermRoleDelete, PermRoleSort } from '@/enums/permission'
  import { computed, h, reactive, ref } from 'vue'
  import { ElMessage, ElMessageBox, ElSwitch, ElTag } from 'element-plus'
  import { useTableColumns } from '@/hooks/core/useTableColumns'
  import { useDict } from '@/hooks/core/useDict'
  import { useAuth } from '@/hooks/core/useAuth'
  import { operationColumn } from '@/components/core/tables/operation-column'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtSvgIcon from '@/components/core/base/art-svg-icon/index.vue'
  import { fetchRoles, removeRole, updateRoleStatus, updateRoleSort } from '../api'
  import RoleDialog from './role-dialog.vue'
  import RoleUserDrawer from './role-user-drawer.vue'

  defineOptions({ name: 'SystemRole' })

  const { hasAuth } = useAuth()

  /* ── 搜索栏 ─────────────────────────────────────── */
  const statusDict = useDict('sys_role_status', { numeric: true })

  const searchForm = ref<{ name?: string; code?: string; status?: number | string }>({})
  const showSearchBar = ref(false)
  const searchItems = computed(() => [
    {
      key: 'name',
      label: '角色名称',
      type: 'input',
      placeholder: '请输入角色名称',
      clearable: true,
      span: 6
    },
    {
      key: 'code',
      label: '角色编码',
      type: 'input',
      placeholder: '请输入角色编码',
      clearable: true,
      span: 6
    },
    {
      key: 'status',
      label: '状态',
      type: 'select',
      placeholder: '全部状态',
      clearable: true,
      span: 6,
      options: statusDict.options.value
    }
  ])

  /* ── 列表数据 ───────────────────────────────────── */
  const loading = ref(false)
  const data = ref<Api.System.Role[]>([])
  const pagination = reactive({ current: 1, size: 10, total: 0 })
  const dialog = ref<InstanceType<typeof RoleDialog>>()
  const userDrawer = ref<InstanceType<typeof RoleUserDrawer>>()
  const tableRef = ref<{ elTableRef?: { clearSelection: () => void } }>()
  const selectedRows = ref<Api.System.Role[]>([])
  const batchRemoving = ref(false)
  const togglingId = ref<string | null>(null)

  const isAdminRole = (row: Api.System.Role) => row.code === 'admin'
  const rowSelectable = (row: Api.System.Role) => !isAdminRole(row)

  async function loadList() {
    loading.value = true
    try {
      const [res] = await Promise.all([
        fetchRoles({
          page: pagination.current,
          pageSize: pagination.size,
          ...(searchForm.value.name ? { name: searchForm.value.name } : {}),
          ...(searchForm.value.code ? { code: searchForm.value.code } : {}),
          ...(searchForm.value.status !== undefined &&
          searchForm.value.status !== null &&
          searchForm.value.status !== ''
            ? { status: Number(searchForm.value.status) }
            : {})
        }),
        statusDict.ensure()
      ])
      data.value = res.list
      pagination.total = res.total
      resetSortDirty()
    } finally {
      loading.value = false
    }
  }

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

  /* ── 弹窗 / 分配用户 ─────────────────────────────── */
  function openDialog(row?: Api.System.Role) {
    dialog.value?.open(row)
  }
  function openUserDrawer(row: Api.System.Role) {
    userDrawer.value?.open(row)
  }

  /* ── 行内排序编辑 + 批量保存（同菜单管理） ───────── */
  const sortDirty = new Set<string>()
  const sortDirtyCount = ref(0)

  function onRowSortChange(row: Api.System.Role) {
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
    const changed = data.value.filter((n) => sortDirty.has(n.id))
    try {
      await updateRoleSort(changed.map((n) => ({ id: n.id, sort: n.sort })))
    } catch {
      return
    }
    ElMessage.success('排序保存成功')
    resetSortDirty()
    await loadList()
  }

  /* ── 删除 ───────────────────────────────────────── */
  async function onRemove(row: Api.System.Role) {
    await ElMessageBox.confirm(`确认删除角色「${row.name}」吗？删除后不可恢复。`, '删除确认', {
      type: 'warning',
      confirmButtonText: '确定',
      cancelButtonText: '取消'
    })
    await removeRole(row.id)
    ElMessage.success('已删除')
    if (data.value.length === 1 && pagination.current > 1) pagination.current -= 1
    await loadList()
  }

  async function onBatchRemove() {
    const list = selectedRows.value.filter((r) => !isAdminRole(r))
    if (!list.length) return
    await ElMessageBox.confirm(`确认删除选中的 ${list.length} 个角色吗？`, '批量删除确认', {
      type: 'warning',
      confirmButtonText: '确定',
      cancelButtonText: '取消'
    })
    batchRemoving.value = true
    try {
      await Promise.all(list.map((r) => removeRole(r.id)))
      ElMessage.success(`已删除 ${list.length} 个角色`)
      clearSelection()
      if (data.value.length === list.length && pagination.current > 1) pagination.current -= 1
      await loadList()
    } finally {
      batchRemoving.value = false
    }
  }

  function onSelectionChange(rows: Api.System.Role[]) {
    selectedRows.value = rows
  }
  function clearSelection() {
    selectedRows.value = []
    tableRef.value?.elTableRef?.clearSelection()
  }

  /* ── 状态开关 ───────────────────────────────────── */
  async function onToggleStatus(row: Api.System.Role, next: boolean) {
    const act = next ? '启用' : '停用'
    try {
      await ElMessageBox.confirm(`确认要「${act}」角色「${row.name}」吗？`, '提示', {
        type: 'warning'
      })
    } catch {
      return // 用户取消
    }
    togglingId.value = row.id
    try {
      await updateRoleStatus(row.id, next ? 1 : 0)
      row.status = next ? 1 : 0
      ElMessage.success(`已${act}`)
    } finally {
      togglingId.value = null
    }
  }

  /* ── 渲染工具 ───────────────────────────────────── */
  // 角色图标:按编码 hash 取配色,内置 admin 用皇冠
  const ROLE_ICON_STYLES = [
    { icon: 'ri:shield-star-line', cls: 'bg-theme/10 text-theme' },
    { icon: 'ri:shield-keyhole-line', cls: 'bg-success/10 text-success' },
    { icon: 'ri:shield-user-line', cls: 'bg-info/10 text-info' },
    { icon: 'ri:shield-check-line', cls: 'bg-warning/10 text-warning' }
  ]
  function roleStyleOf(code: string) {
    let hash = 0
    for (const ch of code) hash = (hash * 31 + ch.charCodeAt(0)) % 100000
    return ROLE_ICON_STYLES[hash % ROLE_ICON_STYLES.length]
  }

  function renderRoleCell(row: Api.System.Role) {
    const admin = isAdminRole(row)
    const style = admin
      ? { icon: 'ri:vip-crown-2-line', cls: 'bg-warning/12 text-warning' }
      : roleStyleOf(row.code)
    return h('div', { class: 'role-cell' }, [
      h('div', { class: `role-cell-icon ${style.cls}` }, [h(ArtSvgIcon, { icon: style.icon })]),
      h('div', { class: 'role-cell-text' }, [
        h('div', { class: 'role-cell-name' }, [
          row.name,
          admin
            ? h(
                ElTag,
                { size: 'small', type: 'warning', effect: 'light', class: 'role-admin-tag' },
                () => '内置'
              )
            : null
        ]),
        h('div', { class: 'role-cell-code' }, row.code)
      ])
    ])
  }

  const DATA_SCOPE_META: Record<number, { label: string; tag: string }> = {
    1: { label: '全部', tag: 'primary' },
    2: { label: '自定', tag: 'warning' },
    3: { label: '本部门', tag: 'success' },
    4: { label: '本部门及以下', tag: 'success' },
    5: { label: '仅本人', tag: 'info' }
  }
  function renderDataScope(row: Api.System.Role) {
    const meta = DATA_SCOPE_META[row.dataScope]
    if (!meta) return h('span', { class: 'text-g-400' }, '—')
    return h(ElTag, { size: 'small', effect: 'light', type: meta.tag as any }, () => meta.label)
  }

  function renderStatus(row: Api.System.Role) {
    const enabled = row.status === 1
    const busy = togglingId.value === row.id
    const admin = isAdminRole(row)
    if (admin) return h(ElTag, { size: 'small', type: 'info', effect: 'plain' }, () => '内置')
    // 无状态切换权限时仅展示状态文本，不渲染可切换的开关。
    const canToggleStatus = hasAuth('system:role:status')
    return h('div', { class: 'flex items-center gap-1.5' }, [
      canToggleStatus
        ? h(ElSwitch, {
            modelValue: enabled,
            size: 'small',
            loading: busy,
            'onUpdate:modelValue': (v: boolean | string | number) => onToggleStatus(row, Boolean(v))
          })
        : null,
      h(
        'span',
        { class: enabled ? 'status-text on' : 'status-text off' },
        statusDict.labelOf(row.status) || (enabled ? '正常' : '停用')
      )
    ])
  }

  function fmtTime(v?: string | null) {
    if (!v) return '—'
    return String(v).replace('T', ' ').slice(0, 16)
  }

  function renderOperation(row: Api.System.Role) {
    if (isAdminRole(row)) {
      return h(ElTag, { size: 'small', type: 'info', effect: 'plain' }, () => '内置角色')
    }
    return h('div', { class: 'flex items-center' }, [
      h(ArtButtonTable, {
        type: 'edit',
        title: '编辑',
        auth: 'system:role:edit',
        onClick: () => openDialog(row)
      }),
      h(ArtButtonTable, {
        icon: 'ri:user-add-line',
        iconClass: 'bg-secondary/12 text-secondary',
        title: '分配用户',
        auth: 'system:role:assign',
        onClick: () => openUserDrawer(row)
      }),
      h(ArtButtonTable, {
        type: 'delete',
        title: '删除',
        auth: 'system:role:delete',
        onClick: () => onRemove(row)
      })
    ])
  }

  /* ── 表格列 ─────────────────────────────────────── */
  const { columns, columnChecks } = useTableColumns<Api.System.Role>(() => {
    const operationColumnConfig = operationColumn<Api.System.Role>({
      count:
        (hasAuth('system:role:edit') ? 1 : 0) +
        (hasAuth('system:role:assign') ? 1 : 0) +
        (hasAuth('system:role:delete') ? 1 : 0),
      formatter: (row) => renderOperation(row)
    })

    return [
      { type: 'selection', width: 46 },
      { type: 'index', width: 60, label: '序号' },
      { prop: 'name', label: '角色', minWidth: 190, formatter: (row) => renderRoleCell(row) },
      {
        prop: 'dataScope',
        label: '数据权限',
        width: 110,
        formatter: (row) => renderDataScope(row)
      },
      { prop: 'sort', label: '排序', width: 150, align: 'center', useSlot: true },
      { prop: 'status', label: '状态', width: 104, formatter: (row) => renderStatus(row) },
      {
        prop: 'createdAt',
        label: '创建时间',
        width: 150,
        formatter: (row) => fmtTime(row.createdAt)
      },
      ...(operationColumnConfig ? [operationColumnConfig] : [])
    ]
  })

  loadList()
</script>

<style lang="scss" scoped>
  .role-page {
    .toolbar-left {
      display: flex;
      align-items: center;
      gap: 8px;
      flex-wrap: wrap;
    }
  }

  :deep(.role-cell) {
    display: flex;
    align-items: center;
    gap: 10px;

    .role-cell-icon {
      display: flex;
      align-items: center;
      justify-content: center;
      width: 32px;
      height: 32px;
      border-radius: 8px;
      font-size: 16px;
      flex-shrink: 0;
    }

    .role-cell-text {
      min-width: 0;

      .role-cell-name {
        display: flex;
        align-items: center;
        gap: 6px;
        font-weight: 500;
        color: var(--g-900, var(--el-text-color-primary));
        line-height: 1.35;
      }

      .role-cell-code {
        font-size: 12px;
        color: var(--g-400, var(--el-text-color-secondary));
        line-height: 1.35;
      }
    }

    .role-admin-tag {
      margin-left: 2px;
      transform: scale(0.92);
      transform-origin: left center;
    }
  }

  .status-text {
    font-size: 12px;

    &.on {
      color: var(--el-color-success);
    }

    &.off {
      color: var(--art-secondary);
    }
  }
</style>
