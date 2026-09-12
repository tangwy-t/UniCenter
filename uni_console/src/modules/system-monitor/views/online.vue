<template>
  <div class="online-page art-full-height">
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
      />

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
  import { reactive, ref, computed, h } from 'vue'
  import { useTableColumns } from '@/hooks/core/useTableColumns'
  import { useAuth } from '@/hooks/core/useAuth'
  import { operationColumn } from '@/components/core/tables/operation-column'
  import { ElMessage, ElMessageBox } from 'element-plus'
  import { PermOnlineKick } from '@/enums/permission'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import { fetchOnlineUsers, kickOnlineSession, type OnlineSession } from '../api/online'

  defineOptions({ name: 'MonitorOnline' })

  const { hasAuth } = useAuth()

  const searchForm = ref<{ keyword?: string }>({})
  const showSearchBar = ref(false)

  const searchItems = computed(() => [
    {
      key: 'keyword',
      label: '关键字',
      type: 'input',
      placeholder: '用户名/姓名/IP',
      clearable: true
    }
  ])

  const loading = ref(false)
  const data = ref<OnlineSession[]>([])
  const pagination = reactive({ current: 1, size: 10, total: 0 })

  async function loadList() {
    loading.value = true
    try {
      const res = await fetchOnlineUsers({
        page: pagination.current,
        pageSize: pagination.size,
        ...(searchForm.value.keyword ? { keyword: searchForm.value.keyword } : {})
      })
      data.value = res.list
      pagination.total = res.total
    } finally {
      loading.value = false
    }
  }

  const { columns, columnChecks } = useTableColumns<OnlineSession>(() => {
    const operationColumnConfig = operationColumn<OnlineSession>({
      count: hasAuth(PermOnlineKick) ? 1 : 0,
      formatter: (row) => renderOperation(row)
    })
    return [
      { type: 'index', width: 60, label: '序号' },
      { prop: 'username', label: '用户名称', width: 140 },
      { prop: 'realName', label: '姓名', width: 120 },
      { prop: 'deptName', label: '所属部门', minWidth: 140 },
      { prop: 'ip', label: '登录地址', minWidth: 140 },
      {
        prop: 'browser',
        label: '设备',
        minWidth: 180,
        formatter: (row) => (row.os && row.os !== 'Unknown' ? `${row.browser} · ${row.os}` : row.browser)
      },
      { prop: 'loginAt', label: '最后登录时间', width: 180 },
      { prop: 'expireAt', label: '过期时间', width: 180 },
      { prop: 'tokenCount', label: '会话数', width: 90 },
      ...(operationColumnConfig ? [operationColumnConfig] : [])
    ]
  })

  function renderOperation(row: OnlineSession) {
    return h(ArtButtonTable, {
      icon: 'ri:logout-circle-r-line',
      iconClass: 'bg-danger/12 text-danger',
      title: '强制下线',
      auth: PermOnlineKick,
      onClick: () => handleKick(row)
    })
  }

  async function handleKick(row: OnlineSession) {
    try {
      await ElMessageBox.confirm(
        `确认强制下线「${row.username || row.realName}」在 ${row.ip} 的会话吗？该会话将立即失效且无法自动续期；若同一账号在其他设备登录，其刷新令牌也将被注销。`,
        '强制下线',
        { type: 'warning', confirmButtonText: '强制下线', cancelButtonText: '取消' }
      )
    } catch {
      return
    }
    try {
      await kickOnlineSession({ uid: row.userId })
      ElMessage.success('已强制下线')
      loadList()
    } catch {
      // 错误提示由 http 拦截器统一处理
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

  loadList()
</script>