<!-- 403页面 -->
<template>
  <ArtException
    :data="{
      title: '403',
      desc,
      btnText: '返回首页',
      imgUrl
    }"
  />
</template>

<script setup lang="ts">
  import { computed } from 'vue'
  import { useRoute } from 'vue-router'
  import imgUrl from '@imgs/svg/403.svg'
  defineOptions({ name: 'Exception403' })

  const route = useRoute()

  // 展示用户尝试访问的地址:路由守卫在无权限跳转时带上 from。
  // 无该参数(例如直接输入 /403)时退回通用文案。
  const desc = computed(() => {
    const from = route.query.from
    if (typeof from === 'string' && from) {
      return `抱歉，您无权访问 ${from}`
    }
    return '抱歉，您无权访问该页面'
  })
</script>
