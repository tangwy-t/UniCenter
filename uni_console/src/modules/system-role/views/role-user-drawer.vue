<template>
  <ElDrawer v-model="visible" :title="`分配用户 - ${roleName}`" size="600px" append-to-body>
    <div class="drawer-body">
      <div class="drawer-toolbar">
        <ElInput
          v-model="keyword"
          placeholder="搜索用户名"
          clearable
          class="keyword-input"
          @keyup.enter="handleSearch"
          @clear="handleSearch"
        />
        <ArtButtonTable
          icon="ri:search-line"
          iconClass="bg-theme/12 text-theme"
          title="搜索"
          @click="handleSearch"
        />
        <div class="flex-1" />
      </div>

      <ElTable v-loading="loading" :data="users" class="user-table">
        <ElTableColumn label="用户" min-width="150">
          <template #default="{ row }">
            <div class="user-cell">
              <ElAvatar :size="30" :src="row.avatar?.trim() || defaultAvatar" />
              <div class="user-cell-text">
                <div class="user-name">{{ row.username }}</div>
                <div class="user-sub">{{ row.realName || '—' }}</div>
              </div>
            </div>
          </template>
        </ElTableColumn>
        <ElTableColumn label="部门" min-width="110">
          <template #default="{ row }">{{ row.deptName || '—' }}</template>
        </ElTableColumn>
        <ElTableColumn label="手机" min-width="110">
          <template #default="{ row }">{{ row.phone || '—' }}</template>
        </ElTableColumn>
        <ElTableColumn label="状态" width="80" align="center">
          <template #default="{ row }">
            <ElTag :type="statusTagType(row.status)" size="small" effect="light">
              {{ statusLabel(row.status) }}
            </ElTag>
          </template>
        </ElTableColumn>
        <ElTableColumn label="操作" width="76" align="center" fixed="right">
          <template #default="{ row }">
            <ArtButtonTable
              v-perm="PermRoleAssign"
              type="delete"
              title="移除"
              @click="onRemove(row)"
            />
          </template>
        </ElTableColumn>
      </ElTable>

      <div class="drawer-pagination">
        <ElPagination
          v-show="total > 0"
          v-model:current-page="pagination.current"
          v-model:page-size="pagination.size"
          :total="total"
          :page-sizes="[10, 20, 50]"
          layout="total, sizes, prev, pager, next"
          @size-change="loadUsers"
          @current-change="loadUsers"
        />
      </div>
    </div>

    <template #footer>
      <div class="art-dialog-footer">
        <ArtButtonTable
          icon="ri:close-line"
          iconClass="bg-g-300/55 text-g-700"
          title="关闭"
          @click="visible = false"
        />
        <ArtButtonTable
          v-perm="PermRoleAssign"
          icon="ri:user-add-line"
          iconClass="bg-theme/12 text-theme"
          title="添加用户"
          @click="openAddDialog"
        />
      </div>
    </template>

    <!-- 添加用户对话框 -->
    <el-dialog
      v-model="addVisible"
      title="添加用户"
      width="480px"
      append-to-body
      :close-on-click-modal="false"
    >
      <div class="add-body">
        <ElSelect
          v-model="selectedIds"
          multiple
          filterable
          remote
          reserve-keyword
          :remote-method="searchCandidates"
          :loading="candidateLoading"
          placeholder="输入用户名搜索并选择"
          class="candidate-select"
        >
          <ElOption
            v-for="u in candidates"
            :key="u.id"
            :label="`${u.username}（${u.realName || '未设姓名'}）`"
            :value="u.id"
          />
        </ElSelect>
        <div class="add-tip">
          <ArtSvgIcon icon="ri:information-line" class="tip-icon" />
          选择后点击「确定」，将用户分配为「{{ roleName }}」角色成员
        </div>
      </div>
      <template #footer>
        <div class="art-dialog-footer">
          <ArtButtonTable
            icon="ri:close-line"
            iconClass="bg-g-300/55 text-g-700"
            title="取消"
            @click="addVisible = false"
          />
          <ArtButtonTable
            icon="ri:check-line"
            iconClass="bg-theme/12 text-theme"
            title="确定"
            @click="onAdd"
          />
        </div>
      </template>
    </el-dialog>
  </ElDrawer>
</template>

<script setup lang="ts">
  import { PermRoleAssign } from '@/enums/permission'
  import { reactive, ref } from 'vue'
  import { ElMessage, ElMessageBox } from 'element-plus'
  import defaultAvatar from '@imgs/user/avatar.webp'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtSvgIcon from '@/components/core/base/art-svg-icon/index.vue'
  import { fetchUsers } from '@/modules/system-user/api'
  import { useDictStore } from '@/store/modules/dict'
  import { fetchRoleUsers, addRoleUsers, removeRoleUsers } from '../api'

  defineOptions({ name: 'RoleUserDrawer' })

  const visible = ref(false)
  const roleId = ref('')
  const roleName = ref('')

  const dictStore = useDictStore()
  const statusLabel = (s?: number) => {
    const label = dictStore.items['sys_normal_disable']?.find((i) => i.value === String(s))?.label
    return label ?? (s === 1 ? '正常' : '停用')
  }
  const statusTagType = (s?: number): 'primary' | 'success' | 'warning' | 'info' | 'danger' => {
    const cls = dictStore.items['sys_normal_disable']?.find(
      (i) => i.value === String(s)
    )?.list_class
    const valid = ['primary', 'warning', 'info', 'success', 'danger'] as const
    return (valid as readonly string[]).includes(cls ?? '')
      ? (cls as 'primary' | 'success' | 'warning' | 'info' | 'danger')
      : s === 1
        ? 'success'
        : 'danger'
  }

  const keyword = ref('')
  const loading = ref(false)
  const users = ref<Api.System.User[]>([])
  const pagination = reactive({ current: 1, size: 10 })
  const total = ref(0)

  async function loadUsers() {
    if (!roleId.value) return
    loading.value = true
    try {
      const res = await fetchRoleUsers(roleId.value, {
        page: pagination.current,
        pageSize: pagination.size,
        ...(keyword.value.trim() ? { username: keyword.value.trim() } : {})
      })
      users.value = res.list
      total.value = res.total
    } finally {
      loading.value = false
    }
  }

  function handleSearch() {
    pagination.current = 1
    loadUsers()
  }

  async function open(row: Api.System.Role) {
    roleId.value = row.id
    roleName.value = row.name
    keyword.value = ''
    pagination.current = 1
    visible.value = true
    dictStore.load('sys_normal_disable') // 预热
    await loadUsers()
  }

  async function onRemove(row: Api.System.User) {
    await ElMessageBox.confirm(
      `确认将用户「${row.username}」移出角色「${roleName.value}」吗？`,
      '移除确认',
      {
        type: 'warning',
        confirmButtonText: '确定',
        cancelButtonText: '取消'
      }
    )
    await removeRoleUsers(roleId.value, [row.id])
    ElMessage.success('已移除')
    if (users.value.length === 1 && pagination.current > 1) pagination.current -= 1
    await loadUsers()
  }

  /* ── 添加用户 ───────────────────────────────────── */
  const addVisible = ref(false)
  const adding = ref(false)
  const candidateLoading = ref(false)
  const candidates = ref<Api.System.User[]>([])
  const selectedIds = ref<string[]>([])

  async function searchCandidates(q: string) {
    candidateLoading.value = true
    try {
      const res = await fetchUsers({ page: 1, pageSize: 20, username: q || undefined })
      candidates.value = res.list
    } finally {
      candidateLoading.value = false
    }
  }

  function openAddDialog() {
    selectedIds.value = []
    candidates.value = []
    addVisible.value = true
    searchCandidates('')
  }

  async function onAdd() {
    if (!selectedIds.value.length) {
      ElMessage.warning('请先选择用户')
      return
    }
    adding.value = true
    try {
      await addRoleUsers(roleId.value, selectedIds.value)
      ElMessage.success('分配成功')
      addVisible.value = false
      await loadUsers()
    } finally {
      adding.value = false
    }
  }

  defineExpose({ open })
</script>

<style scoped>
  .drawer-body {
    display: flex;
    flex-direction: column;
    height: 100%;
  }

  .drawer-toolbar {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-bottom: 12px;

    .keyword-input {
      width: 200px;
    }
  }

  .user-table {
    flex: 1;
  }

  :deep(.user-cell) {
    display: flex;
    align-items: center;
    gap: 8px;

    .user-cell-text {
      min-width: 0;

      .user-name {
        font-weight: 500;
        line-height: 1.35;
      }

      .user-sub {
        font-size: 12px;
        color: var(--el-text-color-secondary);
        line-height: 1.35;
      }
    }
  }

  .drawer-pagination {
    display: flex;
    justify-content: flex-end;
    margin-top: 12px;
  }

  .add-body {
    .candidate-select {
      width: 100%;
    }

    .add-tip {
      display: flex;
      align-items: center;
      gap: 4px;
      margin-top: 12px;
      font-size: 12px;
      color: var(--el-text-color-secondary);

      .tip-icon {
        font-size: 14px;
      }
    }
  }
</style>
