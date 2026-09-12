<template>
  <div class="dashboard">
    <el-card shadow="never">
      <div class="welcome">
        <h2>你好，{{ userStore.info?.realName || userStore.info?.username }}</h2>
        <p>欢迎使用后台管理系统。</p>
      </div>
    </el-card>
  </div>
</template>

<script setup lang="ts">
  import { useUserStore } from '@/store/modules/user'

  const userStore = useUserStore()
</script>

<style scoped>
  .welcome h2 {
    margin: 0 0 8px;
  }
</style>
