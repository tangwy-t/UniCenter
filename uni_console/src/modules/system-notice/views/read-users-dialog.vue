<template>
  <el-dialog
    v-model="visible"
    :title="`「${noticeTitle}」已读用户`"
    width="760px"
    top="6vh"
    class="read-users-dialog"
    @closed="handleClose"
  >
    <!-- 搜索 + 统计 -->
    <div class="read-users-toolbar">
      <el-input
        v-model="queryForm.searchValue"
        placeholder="登录名称 / 用户姓名"
        clearable
        style="width: 240px"
        @keyup.enter="handleSearch"
        @clear="handleSearch"
      />
      <ArtButtonTable
        icon="ri:refresh-line"
        iconClass="bg-g-300/55 text-g-700"
        title="重置"
        @click="handleReset"
      />
      <ArtButtonTable
        icon="ri:search-line"
        iconClass="bg-theme/12 text-theme"
        title="搜索"
        @click="handleSearch"
      />
      <span class="read-stat">
        共 <strong>{{ total }}</strong> 人已读
      </span>
    </div>

    <!-- 列表 -->
    <el-table v-loading="loading" :data="userList" stripe class="read-users-table">
      <el-table-column type="index" label="序号" width="60" align="center" />
      <el-table-column prop="username" label="登录名称" align="center" show-overflow-tooltip />
      <el-table-column prop="realName" label="用户姓名" align="center" show-overflow-tooltip>
        <template #default="{ row }">
          <span>{{ row.realName || '—' }}</span>
        </template>
      </el-table-column>
      <el-table-column prop="deptName" label="所属部门" align="center" show-overflow-tooltip>
        <template #default="{ row }">
          <span>{{ row.deptName || '—' }}</span>
        </template>
      </el-table-column>
      <el-table-column prop="phone" label="手机号码" width="130" align="center">
        <template #default="{ row }">
          <span>{{ row.phone || '—' }}</span>
        </template>
      </el-table-column>
      <el-table-column prop="readTime" label="阅读时间" width="170" align="center">
        <template #default="{ row }">
          <span>{{ row.readTime || '—' }}</span>
        </template>
      </el-table-column>
      <template #empty>
        <el-empty description="暂无已读记录" :image-size="90" />
      </template>
    </el-table>

    <!-- 分页 -->
    <div v-if="total > 0" class="read-users-pagination">
      <el-pagination
        v-model:current-page="pagination.current"
        v-model:page-size="pagination.size"
        :total="total"
        :page-sizes="[10, 20, 30, 50]"
        background
        small
        layout="total, prev, pager, next, sizes"
        @size-change="loadList(1)"
        @current-change="loadList()"
      />
    </div>

    <template #footer>
      <div class="art-dialog-footer">
        <ArtButtonTable
          icon="ri:close-line"
          iconClass="bg-g-300/55 text-g-700"
          title="关闭"
          @click="visible = false"
        />
      </div>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
  import { ref, reactive } from 'vue'
  import { fetchNoticeReadUsers } from '../api'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'

  defineOptions({ name: 'NoticeReadUsers' })

  const visible = ref(false)
  const loading = ref(false)
  const noticeTitle = ref('')
  const total = ref(0)
  const userList = ref<Api.Notice.ReadUser[]>([])

  const queryForm = ref<{ searchValue?: string }>({})
  const pagination = reactive({ current: 1, size: 10 })
  let noticeId = ''

  async function loadList(page = pagination.current) {
    pagination.current = page
    loading.value = true
    try {
      const res = await fetchNoticeReadUsers(noticeId, {
        page: pagination.current,
        pageSize: pagination.size,
        ...(queryForm.value.searchValue ? { searchValue: queryForm.value.searchValue } : {})
      })
      userList.value = res.list
      total.value = res.total
    } finally {
      loading.value = false
    }
  }

  function handleSearch() {
    loadList(1)
  }

  function handleReset() {
    queryForm.value.searchValue = undefined
    loadList(1)
  }

  function open(row: Api.Notice.Notice) {
    noticeId = row.id
    noticeTitle.value = row.title
    queryForm.value.searchValue = undefined
    pagination.current = 1
    visible.value = true
    loadList(1)
  }

  function handleClose() {
    userList.value = []
    total.value = 0
    queryForm.value.searchValue = undefined
  }

  defineExpose({ open })
</script>

<style lang="scss" scoped>
  .read-users-toolbar {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-bottom: 10px;

    .read-stat {
      margin-left: auto;
      font-size: 13px;
      color: var(--el-text-color-secondary);

      strong {
        color: var(--el-color-primary);
        font-size: 15px;
        margin: 0 2px;
      }
    }
  }

  .read-users-table {
    width: 100%;
  }

  .read-users-pagination {
    display: flex;
    justify-content: flex-end;
    padding-top: 10px;
  }
</style>
