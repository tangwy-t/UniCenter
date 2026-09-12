<template>
  <div class="user-page art-full-height">
    <div class="user-layout">
      <!-- 部门树侧栏（桌面端） -->
      <ElCard v-if="!isMobile" class="dept-panel" shadow="never">
        <DeptTreePanel
          :data="deptTree"
          :loading="deptLoading"
          @select="onDeptSelect"
          @refresh="loadDeptTree"
        />
      </ElCard>

      <div class="main-col">
        <!-- 搜索栏 -->
        <ArtSearchBar
          v-show="showSearchBar"
          v-model="searchForm"
          :items="searchItems"
          :button-left-limit="2"
          @search="handleSearch"
          @reset="handleReset"
        />

        <!-- 表格卡片 -->
        <ElCard class="art-table-card table-card" shadow="never">
          <ArtTableHeader
            v-model:columns="columnChecks"
            v-model:showSearchBar="showSearchBar"
            :loading="loading"
            @refresh="loadList"
          >
            <template #left>
              <div class="toolbar-left">
                <ArtButtonTable
                  v-perm="PermUserAdd"
                  type="add"
                  title="新增用户"
                  @click="openDialog()"
                />

                <!-- 移动端：部门树入口 -->
                <ArtButtonTable
                  v-if="isMobile"
                  icon="ri:folder-user-line"
                  iconClass="bg-g-300/55 text-g-700"
                  title="部门"
                  @click="drawerVisible = true"
                />

                <!-- 批量操作（有勾选时出现） -->
                <template v-if="selectedRows.length">
                  <ArtButtonTable
                    v-perm="PermUserDelete"
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

                <!-- 部门过滤条件提示 -->
                <div v-else-if="currentDept" class="dept-chip">
                  <ArtSvgIcon icon="ri:folder-user-line" class="text-[13px]" />
                  <span>{{ currentDept.name }}</span>
                  <span class="dept-chip-clear" title="清除部门过滤" @click="clearDeptFilter"
                    >×</span
                  >
                </div>
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
          />
        </ElCard>
      </div>
    </div>

    <!-- 移动端部门树抽屉 -->
    <ElDrawer v-model="drawerVisible" title="组织机构" size="300px" append-to-body>
      <div class="drawer-tree-wrap">
        <DeptTreePanel
          :data="deptTree"
          :loading="deptLoading"
          @select="onDeptSelect"
          @refresh="loadDeptTree"
        />
      </div>
    </ElDrawer>

    <UserDialog ref="dialog" @saved="loadList" />
    <RoleDialog ref="roleDialog" @saved="loadList" />
    <UserViewDrawer ref="viewDrawer" @edit="openDialog" @assign="openRoleDialog" />
  </div>
</template>

<script setup lang="ts">
  import { PermUserAdd, PermUserDelete } from '@/enums/permission'
  import { computed, h, reactive, ref } from 'vue'
  import { ElSwitch, ElTag, ElTooltip, ElMessage, ElMessageBox } from 'element-plus'
  import { useWindowSize } from '@vueuse/core'
  import { useTableColumns } from '@/hooks/core/useTableColumns'
  import { useAuth } from '@/hooks/core/useAuth'
  import { useDict } from '@/hooks/core/useDict'
  import { resolveAvatar } from '@/utils/avatar'
  import { operationColumn } from '@/components/core/tables/operation-column'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtButtonMore from '@/components/core/forms/art-button-more/index.vue'
  import ArtSvgIcon from '@/components/core/base/art-svg-icon/index.vue'
  import {
    fetchUsers,
    removeUser,
    enableUser,
    disableUser,
    resetUserPassword,
    unlockUser,
    fetchDepts
  } from '../api'
  import UserDialog from './user-dialog.vue'
  import RoleDialog from './role-dialog.vue'
  import UserViewDrawer from './user-view-drawer.vue'
  import DeptTreePanel from './dept-tree-panel.vue'

  defineOptions({ name: 'SystemUser' })

  const { width } = useWindowSize()
  const isMobile = computed(() => width.value < 1024)
  const { hasAuth } = useAuth()

  /* ── 部门树 ─────────────────────────────────────── */
  const deptTree = ref<Api.System.Dept[]>([])
  const deptLoading = ref(false)
  const currentDept = ref<Api.System.Dept | null>(null)
  const drawerVisible = ref(false)

  async function loadDeptTree() {
    deptLoading.value = true
    try {
      deptTree.value = await fetchDepts()
    } finally {
      deptLoading.value = false
    }
  }

  function onDeptSelect(dept: Api.System.Dept | null) {
    currentDept.value = dept
    // 移动端从抽屉选择后自动收起
    if (isMobile.value) drawerVisible.value = false
    pagination.current = 1
    loadList()
  }

  function clearDeptFilter() {
    currentDept.value = null
    pagination.current = 1
    loadList()
  }

  /* ── 搜索栏 ─────────────────────────────────────── */
  const statusDict = useDict('sys_normal_disable', { numeric: true })
  const genderDict = useDict('sys_user_gender')

  const searchForm = ref<{
    username?: string
    phone?: string
    status?: number | string
  }>({})

  const searchItems = computed(() => [
    {
      key: 'username',
      label: '用户账号',
      type: 'input',
      placeholder: '请输入用户账号',
      clearable: true,
      span: 6
    },
    {
      key: 'phone',
      label: '手机号码',
      type: 'input',
      placeholder: '请输入手机号码',
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
  const data = ref<Api.System.User[]>([])
  const pagination = reactive({ current: 1, size: 10, total: 0 })
  const showSearchBar = ref(false)
  const selectedRows = ref<Api.System.User[]>([])
  const batchRemoving = ref(false)
  const togglingId = ref<string | null>(null)

  const dialog = ref<InstanceType<typeof UserDialog>>()
  const roleDialog = ref<InstanceType<typeof RoleDialog>>()
  const viewDrawer = ref<InstanceType<typeof UserViewDrawer>>()
  const tableRef = ref<{ elTableRef?: { clearSelection: () => void } }>()

  const isAdmin = (row: Api.System.User) => row.username === 'admin'
  const rowSelectable = (row: Api.System.User) => !isAdmin(row)

  async function loadList() {
    loading.value = true
    try {
      const [res] = await Promise.all([
        fetchUsers({
          page: pagination.current,
          pageSize: pagination.size,
          ...(searchForm.value.username ? { username: searchForm.value.username } : {}),
          ...(searchForm.value.phone ? { phone: searchForm.value.phone } : {}),
          ...(searchForm.value.status !== undefined &&
          searchForm.value.status !== null &&
          searchForm.value.status !== ''
            ? { status: Number(searchForm.value.status) }
            : {}),
          ...(currentDept.value ? { deptId: currentDept.value.id } : {})
        }),
        statusDict.ensure(),
        genderDict.ensure()
      ])
      data.value = res.list
      pagination.total = res.total
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

  function onSelectionChange(rows: Api.System.User[]) {
    selectedRows.value = rows
  }

  function clearSelection() {
    selectedRows.value = []
    tableRef.value?.elTableRef?.clearSelection()
  }

  /* ── 弹窗 ───────────────────────────────────────── */
  function openDialog(row?: Api.System.User) {
    dialog.value?.open(row)
  }
  function openRoleDialog(row: Api.System.User) {
    roleDialog.value?.open(row)
  }
  function openDetail(row: Api.System.User) {
    viewDrawer.value?.open(row)
  }

  /* ── 行操作 ─────────────────────────────────────── */
  async function onRemove(row: Api.System.User) {
    await ElMessageBox.confirm(`确认删除用户「${row.username}」吗？`, '删除确认', {
      type: 'warning',
      confirmButtonText: '确定',
      cancelButtonText: '取消'
    })
    await removeUser(row.id)
    ElMessage.success('已删除')
    await loadList()
  }

  async function onBatchRemove() {
    const list = selectedRows.value.filter((r) => !isAdmin(r))
    if (!list.length) {
      ElMessage.warning('请先勾选要删除的用户')
      return
    }
    await ElMessageBox.confirm(
      `确认删除选中的 ${list.length} 个用户吗？删除后不可恢复。`,
      '批量删除',
      {
        type: 'warning',
        confirmButtonText: '确定',
        cancelButtonText: '取消'
      }
    )
    batchRemoving.value = true
    try {
      await Promise.all(list.map((r) => removeUser(r.id)))
      ElMessage.success(`已删除 ${list.length} 个用户`)
      clearSelection()
      // 删除后若当前页已空，回退一页
      if (data.value.length === list.length && pagination.current > 1) pagination.current -= 1
      await loadList()
    } finally {
      batchRemoving.value = false
    }
  }

  async function onToggleStatus(row: Api.System.User, next: boolean) {
    const act = next ? '启用' : '停用'
    try {
      await ElMessageBox.confirm(`确认要「${act}」用户「${row.username}」吗？`, '提示', {
        type: 'warning'
      })
    } catch {
      return // 用户取消
    }
    togglingId.value = row.id
    try {
      if (next) await enableUser(row.id)
      else await disableUser(row.id)
      row.status = next ? 1 : 0
      ElMessage.success(`已${act}`)
    } finally {
      togglingId.value = null
    }
  }

  async function onResetPassword(row: Api.System.User) {
    const { value } = await ElMessageBox.prompt(`为用户「${row.username}」设置新密码`, '重置密码', {
      inputPattern: /^.{6,64}$/,
      inputErrorMessage: '密码长度 6-64 位'
    })
    await resetUserPassword(row.id, value)
    ElMessage.success('密码已重置')
  }

  async function onUnlock(row: Api.System.User) {
    await unlockUser(row.id)
    ElMessage.success('已解锁')
    await loadList()
  }

  async function onMore(item: { key: string | number }, row: Api.System.User) {
    const key = String(item.key)
    if (key === 'detail') openDetail(row)
    else if (key === 'reset') await onResetPassword(row)
    else if (key === 'unlock') await onUnlock(row)
    else if (key === 'toggle') await onToggleStatus(row, row.status !== 1)
    else if (key === 'delete') await onRemove(row)
  }

  /* ── 渲染工具 ───────────────────────────────────── */
  function renderAvatar(row: Api.System.User) {
    return h('img', {
      src: resolveAvatar(row.avatar),
      alt: row.username,
      class: 'w-[34px] h-[34px] rounded-full object-cover align-middle',
      onError: (e: Event) => {
        ;(e.target as HTMLImageElement).src = resolveAvatar()
      }
    })
  }

  function fmtTime(v?: string | null) {
    if (!v) return '—'
    return String(v).replace('T', ' ').slice(0, 16)
  }

  function renderRoleTags(row: Api.System.User) {
    const names = row.roleNames ?? []
    if (!names.length) {
      // 无角色用户醒目标记：警示标签，提醒运营该用户尚未分配任何角色。
      return h(
        ElTag,
        { size: 'small', type: 'warning', effect: 'plain', class: 'role-tag role-tag-empty' },
        () => '未分配角色'
      )
    }
    const shown = names.slice(0, 2)
    const rest = names.length - shown.length
    const tags = shown.map((n) =>
      h(ElTag, { size: 'small', effect: 'light', class: 'role-tag' }, () => n)
    )
    if (rest > 0) {
      tags.push(
        h(
          ElTooltip,
          { content: names.join('、'), placement: 'top' },
          { default: () => h(ElTag, { size: 'small', effect: 'plain' }, () => `+${rest}`) }
        )
      )
    }
    return h('div', { class: 'flex flex-wrap gap-1' }, tags)
  }

  function renderStatus(row: Api.System.User) {
    const enabled = row.status === 1
    const busy = togglingId.value === row.id
    return h('div', { class: 'flex items-center gap-1.5' }, [
      h(ElSwitch, {
        modelValue: enabled,
        size: 'small',
        loading: busy,
        disabled: isAdmin(row),
        'onUpdate:modelValue': (v: boolean | string | number) => onToggleStatus(row, Boolean(v))
      }),
      h(
        'span',
        { class: enabled ? 'status-text on' : 'status-text off' },
        statusDict.labelOf(row.status) || (enabled ? '正常' : '停用')
      )
    ])
  }

  function renderOperation(row: Api.System.User) {
    // 内置管理员仅可查看，不可修改（与 RuoYi 行为一致）
    if (isAdmin(row)) {
      return h(ElTag, { size: 'small', type: 'info', effect: 'plain' }, () => '内置管理员')
    }
    return h('div', { class: 'flex items-center' }, [
      h(ArtButtonTable, {
        type: 'edit',
        title: '编辑',
        auth: 'system:user:edit',
        onClick: () => openDialog(row)
      }),
      h(ArtButtonTable, {
        icon: 'ri:shield-user-line',
        iconClass: 'bg-info/12 text-info',
        title: '分配角色',
        auth: 'system:user:assign',
        onClick: () => openRoleDialog(row)
      }),
      h(ArtButtonMore, {
        list: [
          {
            key: 'detail',
            label: '查看详情',
            icon: 'ri:file-user-line',
            auth: 'system:user:query'
          },
          { key: 'reset', label: '重置密码', icon: 'ri:key-2-line', auth: 'system:user:reset' },
          { key: 'unlock', label: '解锁', icon: 'ri:lock-unlock-line', auth: 'system:user:unlock' },
          row.status === 1
            ? {
                key: 'toggle',
                label: '停用',
                icon: 'ri:shut-down-line',
                color: '#e6a23c',
                auth: 'system:user:disable'
              }
            : { key: 'toggle', label: '启用', icon: 'ri:switch-line', auth: 'system:user:enable' },
          {
            key: 'delete',
            label: '删除',
            icon: 'ri:delete-bin-4-line',
            color: 'var(--art-danger)',
            auth: 'system:user:delete'
          }
        ],
        onClick: (item: { key: string | number }) => onMore(item, row)
      })
    ])
  }

  /* ── 表格列 ─────────────────────────────────────── */
  const { columns, columnChecks } = useTableColumns<Api.System.User>(() => {
    const operationColumnConfig = operationColumn<Api.System.User>({
      count:
        (hasAuth('system:user:edit') ? 1 : 0) +
        (hasAuth('system:user:assign') ? 1 : 0) +
        ([
          'system:user:query',
          'system:user:reset',
          'system:user:unlock',
          'system:user:disable',
          'system:user:enable',
          'system:user:delete'
        ].some((p) => hasAuth(p))
          ? 1
          : 0),
      formatter: (row) => renderOperation(row)
    })

    return [
      { type: 'selection', width: 46 },
      {
        prop: 'avatar',
        label: '头像',
        width: 72,
        align: 'center',
        formatter: (row) => renderAvatar(row)
      },
      {
        prop: 'username',
        label: '账号',
        minWidth: 120,
        formatter: (row) =>
          h(
            'a',
            { class: 'cell-username', title: '查看用户详情', onClick: () => openDetail(row) },
            row.username
          )
      },
      { prop: 'realName', label: '姓名', width: 100, formatter: (row) => row.realName || '—' },
      { prop: 'nickname', label: '昵称', width: 120, formatter: (row) => row.nickname || '—' },
      { prop: 'deptName', label: '部门', width: 130, formatter: (row) => row.deptName || '—' },
      { prop: 'phone', label: '手机', width: 120, formatter: (row) => row.phone || '—' },
      {
        prop: 'email',
        label: '邮箱',
        minWidth: 150,
        showOverflowTooltip: true,
        visible: false,
        formatter: (row) => row.email || '—'
      },
      { prop: 'roleNames', label: '角色', minWidth: 150, formatter: (row) => renderRoleTags(row) },
      { prop: 'gender', label: '性别', width: 80, formatter: (row) => genderDict.labelOf(row.gender) },
      { prop: 'status', label: '状态', width: 96, formatter: (row) => renderStatus(row) },
      {
        prop: 'lastLoginTime',
        label: '最后登录',
        width: 140,
        formatter: (row) => fmtTime(row.lastLoginTime)
      },
      {
        prop: 'createdAt',
        label: '创建时间',
        width: 140,
        formatter: (row) => fmtTime(row.createdAt)
      },
      ...(operationColumnConfig ? [operationColumnConfig] : [])
    ]
  })

  loadDeptTree()
  loadList()
</script>

<style lang="scss" scoped>
  .user-layout {
    display: flex;
    align-items: stretch;
    gap: 16px;
    height: 100%;
  }

  .dept-panel {
    width: 252px;
    flex-shrink: 0;
    height: 100%;

    :deep(.el-card__body) {
      height: 100%;
      padding: 16px 12px;
    }
  }

  .main-col {
    display: flex;
    flex-direction: column;
    gap: 16px;
    flex: 1;
    min-width: 0;
    height: 100%;
  }

  .table-card {
    // 复用全局 art-table-card 布局；min-height 允许在 flex 列中收缩，
    // margin-top 由 user-layout 的 gap 控制，抵消全局的 12px
    min-height: 0;
    margin-top: 0 !important;
  }

  .toolbar-left {
    display: flex;
    align-items: center;
    justify-content: flex-start;
    gap: 8px;
    flex-wrap: wrap;
  }

  .dept-chip {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding: 4px 8px;
    border-radius: 6px;
    font-size: 12px;
    color: var(--el-color-primary);
    background: var(--el-color-primary-light-9);
    border: 1px solid var(--el-color-primary-light-7);
    max-width: 220px;

    span {
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .dept-chip-clear {
      cursor: pointer;
      font-weight: 700;
      line-height: 1;
      padding: 0 2px;

      &:hover {
        color: var(--el-color-primary-dark-2);
      }
    }
  }

  .drawer-tree-wrap {
    height: 100%;
  }

  /* ── 单元格渲染 ─────────────────────────────────── */
  .cell-username {
    font-size: 13.5px;
    font-weight: 500;
    color: var(--el-text-color-primary);
    cursor: pointer;

    &:hover {
      color: var(--el-color-primary);
    }
  }

  :deep(.role-tag.el-tag) {
    margin: 0;
  }

  .status-text {
    font-size: 12px;

    &.on {
      color: var(--el-color-success);
    }

    &.off {
      color: var(--el-text-color-secondary);
    }
  }

  /* ── 响应式 ─────────────────────────────────────── */
  @media screen and (max-width: 1023.98px) {
    .user-layout {
      gap: 0;
    }

    .main-col {
      gap: 12px;
    }
  }
</style>
