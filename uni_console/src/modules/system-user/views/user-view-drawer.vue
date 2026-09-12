<!--
  用户详情抽屉 — 从表格行直接渲染，无需额外请求
  操作：编辑 / 分配角色，通过事件交给列表页复用既有弹窗
-->
<template>
  <ElDrawer v-model="visible" size="420px" :title="user ? '用户详情' : ''" append-to-body>
    <template v-if="user">
      <!-- 头部：头像 + 身份 -->
      <div class="profile-head">
        <ElAvatar :src="user.avatar?.trim() || defaultAvatar" :size="56" />
        <div class="profile-id">
          <div class="profile-username">
            {{ user.username }}
            <ElTag :type="statusTagType" size="small" effect="light">
              {{ statusLabel }}
            </ElTag>
          </div>
          <div class="profile-sub">
            {{ user.realName || '未填写姓名' }}
            <span v-if="user.deptName">· {{ user.deptName }}</span>
          </div>
        </div>
      </div>

      <ElDescriptions :column="1" class="profile-desc" label-width="76px">
        <ElDescriptionsItem label="用户账号">{{ user.username }}</ElDescriptionsItem>
        <ElDescriptionsItem label="昵称">{{ user.nickname || '—' }}</ElDescriptionsItem>
        <ElDescriptionsItem label="性别">{{ genderLabel }}</ElDescriptionsItem>
        <ElDescriptionsItem label="手机号码">{{ user.phone || '—' }}</ElDescriptionsItem>
        <ElDescriptionsItem label="邮箱">{{ user.email || '—' }}</ElDescriptionsItem>
        <ElDescriptionsItem label="归属部门">{{ user.deptName || '—' }}</ElDescriptionsItem>
        <ElDescriptionsItem label="用户角色">
          <div class="flex flex-wrap gap-1.5">
            <ElTag
              v-for="role in user.roleNames ?? []"
              :key="role"
              size="small"
              effect="light"
              class="role-tag"
            >
              {{ role }}
            </ElTag>
            <span v-if="!user.roleNames?.length" class="text-g-400">—</span>
          </div>
        </ElDescriptionsItem>
        <ElDescriptionsItem label="最后登录">{{ fmtTime(user.lastLoginTime) }}</ElDescriptionsItem>
        <ElDescriptionsItem label="创建时间">{{ fmtTime(user.createdAt) }}</ElDescriptionsItem>
        <ElDescriptionsItem label="备注">{{ user.remark || '—' }}</ElDescriptionsItem>
      </ElDescriptions>
    </template>

    <template #footer>
      <div class="art-dialog-footer">
        <ArtButtonTable
          icon="ri:close-line"
          iconClass="bg-g-300/55 text-g-700"
          title="关闭"
          @click="visible = false"
        />
        <template v-if="user && user.username !== 'admin'">
          <ArtButtonTable
            icon="ri:shield-user-line"
            iconClass="bg-info/12 text-info"
            title="分配角色"
            @click="onAssign"
          />
          <ArtButtonTable
            icon="ri:edit-line"
            iconClass="bg-theme/12 text-theme"
            title="编辑"
            @click="onEdit"
          />
        </template>
      </div>
    </template>
  </ElDrawer>
</template>

<script setup lang="ts">
  import { computed, ref } from 'vue'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import defaultAvatar from '@imgs/user/avatar.webp'
  import { useDictStore } from '@/store/modules/dict'

  defineOptions({ name: 'UserViewDrawer' })

  const emit = defineEmits<{
    (e: 'edit', row: Api.System.User): void
    (e: 'assign', row: Api.System.User): void
  }>()

  const visible = ref(false)
  const user = ref<Api.System.User>()
  const dictStore = useDictStore()
  const statusLabel = computed(() => {
    const s = user.value?.status
    const label = dictStore.items['sys_normal_disable']?.find((i) => i.value === String(s))?.label
    return label ?? (s === 1 ? '正常' : '停用')
  })
  const genderLabel = computed(() => {
    const g = user.value?.gender
    if (!g) return '—'
    const label = dictStore.items['sys_user_gender']?.find((i) => i.value === String(g))?.label
    return label ?? '—'
  })
  const statusTagType = computed<'primary' | 'success' | 'warning' | 'info' | 'danger'>(() => {
    const s = user.value?.status
    const cls = dictStore.items['sys_normal_disable']?.find(
      (i) => i.value === String(s)
    )?.list_class
    const valid = ['primary', 'warning', 'info', 'success', 'danger'] as const
    return (valid as readonly string[]).includes(cls ?? '')
      ? (cls as 'primary' | 'success' | 'warning' | 'info' | 'danger')
      : s === 1
        ? 'success'
        : 'danger'
  })

  function fmtTime(v: string | null | undefined) {
    if (!v) return '—'
    return String(v).replace('T', ' ').slice(0, 19)
  }

  function open(row: Api.System.User) {
    dictStore.load('sys_normal_disable') // 预热;未加载时回退文案与字典 label 相同,视觉不变
    dictStore.load('sys_user_gender') // 预热性别字典,渲染中文标签
    user.value = row
    visible.value = true
  }

  function onEdit() {
    if (user.value) emit('edit', user.value)
  }
  function onAssign() {
    if (user.value) emit('assign', user.value)
  }

  defineExpose({ open })
</script>

<style lang="scss" scoped>
  .profile-head {
    display: flex;
    align-items: center;
    gap: 14px;
    padding: 4px 2px 18px;
    border-bottom: 1px solid var(--el-border-color-lighter);
    margin-bottom: 16px;

    .profile-id {
      min-width: 0;

      .profile-username {
        display: flex;
        align-items: center;
        gap: 8px;
        font-size: 16px;
        font-weight: 600;
        color: var(--el-text-color-primary);
      }

      .profile-sub {
        margin-top: 4px;
        font-size: 13px;
        color: var(--el-text-color-secondary);
      }
    }
  }

  .profile-desc {
    :deep(.el-descriptions__label) {
      color: var(--el-text-color-secondary);
    }
  }
</style>
