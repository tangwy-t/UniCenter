<template>
  <el-alert
    v-if="visible"
    type="warning"
    show-icon
    :closable="true"
    class="password-reminder"
    title="已为您生成初始/临时密码，建议尽快修改"
    @close="dismiss"
  >
    <el-button link type="primary" @click="goChange">去修改</el-button>
  </el-alert>
</template>

<script setup lang="ts">
  import { computed, ref } from 'vue'
  import { useRouter } from 'vue-router'
  import { useUserStore } from '@/store/modules/user'
  import { StorageConfig } from '@/utils/storage'

  defineOptions({ name: 'ArtPasswordReminder' })

  const router = useRouter()
  const userStore = useUserStore()

  const dismissed = ref(sessionStorage.getItem(StorageConfig.PASSWORD_REMINDER_DISMISSED_KEY) === '1')
  const mustChange = computed(() => !!userStore.info.mustChangePassword)
  const visible = computed(() => mustChange.value && !dismissed.value)

  function dismiss(): void {
    sessionStorage.setItem(StorageConfig.PASSWORD_REMINDER_DISMISSED_KEY, '1')
    dismissed.value = true
  }

  function goChange(): void {
    router.push('/system/user-center')
  }
</script>

<style scoped>
  .password-reminder {
    margin-bottom: 12px;
  }
</style>