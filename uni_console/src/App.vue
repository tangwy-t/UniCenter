<template>
  <ElConfigProvider
    size="default"
    :locale="zh"
    :z-index="3000"
    :card="{
      shadow: 'never'
    }"
  >
    <RouterView></RouterView>
  </ElConfigProvider>
</template>

<script setup lang="ts">
  import zh from 'element-plus/es/locale/lang/zh-cn'
  import { toggleTransition } from './utils/ui/animation'
  import { checkStorageCompatibility } from './utils/storage'
  import { initializeTheme } from './hooks/core/useTheme'
  import { useSocketStore } from './store/modules/socket'

  onBeforeMount(() => {
    toggleTransition(true)
    initializeTheme()
  })

  onMounted(() => {
    checkStorageCompatibility()
    toggleTransition(false)
    // 已登录（persist 恢复 token）时恢复 WebSocket 连接
    useSocketStore().ensureConnected()
  })
</script>
