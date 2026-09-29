<template>
  <div class="login-page art-full-height">
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
  import { reactive, ref, computed } from 'vue'
  import { useRoute } from 'vue-router'
  import { useTableColumns } from '@/hooks/core/useTableColumns'
  import { useDict } from '@/hooks/core/useDict'
  import { fetchLoginLogs } from '../api'

  defineOptions({ name: 'LogLogin' })

  const route = useRoute()

  // 支持从 URL query 初始化过滤条件(如个人中心「全部日志」跳转携带 username):
  // 命中时自动展开搜索栏,让生效中的过滤条件可见、可编辑。
  const queryUsername = typeof route.query.username === 'string' ? route.query.username.trim() : ''

  const searchForm = ref<{ username?: string; ip?: string; code?: number | string }>(
    queryUsername ? { username: queryUsername } : {}
  )
  const showSearchBar = ref(!!queryUsername)

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
      key: 'ip',
      label: 'IP',
      type: 'input',
      placeholder: '请输入IP',
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
  const data = ref<Api.Log.LoginLog[]>([])
  const pagination = reactive({ current: 1, size: 10, total: 0 })

  async function loadList() {
    loading.value = true
    try {
      const [res] = await Promise.all([
        fetchLoginLogs({
          page: pagination.current,
          pageSize: pagination.size,
          ...(searchForm.value.username ? { username: searchForm.value.username } : {}),
          ...(searchForm.value.ip ? { ip: searchForm.value.ip } : {}),
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

  // 列优先级：手机（<768）只保留"谁 / 结果 / 时间"；平板加序号与 IP；细节列（地点/浏览器/系统/提示消息）桌面才展示
  const { columns, columnChecks } = useTableColumns<Api.Log.LoginLog>(() => [
    { type: 'index', width: 60, label: '序号', hideBelow: 'tablet' },
    { prop: 'username', label: '用户名称', minWidth: 110 },
    { prop: 'ip', label: 'IP', minWidth: 140, hideBelow: 'tablet' },
    { prop: 'location', label: '地点', minWidth: 140, hideBelow: 'desktop' },
    { prop: 'browser', label: '浏览器', minWidth: 140, hideBelow: 'desktop' },
    { prop: 'os', label: '系统', minWidth: 140, hideBelow: 'desktop' },
    {
      prop: 'code',
      label: '状态',
      minWidth: 100,
      formatter: (row) => resultCodeDict.render(row.code)
    },
    {
      prop: 'msg',
      label: '提示消息',
      minWidth: 160,
      showOverflowTooltip: true,
      hideBelow: 'desktop'
    },
    { prop: 'loginTime', label: '登录时间', minWidth: 150 }
  ])

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
