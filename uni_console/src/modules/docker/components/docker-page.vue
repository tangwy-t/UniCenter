<template>
  <div class="docker-page art-full-height">
    <!-- 主机条：切换器 + 同步状态 + 刷新。陈旧时状态文字变琥珀色（唯一一处颜色即结论）。 -->
    <div class="docker-page__bar">
      <HostSwitcher>
        <ElButton :loading="loading" size="small" @click="$emit('refresh')">刷新</ElButton>
        <span class="docker-page__sync" :class="staleClass(stale)">
          {{ syncText(stale, ageSeconds, neverReported) }}
        </span>
        <span v-if="ctx.host && !ctx.host.online" class="docker-page__offline">
          Agent 离线，数据为最后已知状态
        </span>
      </HostSwitcher>
    </div>

    <!-- 主机自报 Docker 不可用：整页只给结论与原因，不渲染任何资源表格。 -->
    <ElEmpty
      v-if="ctx.host && !ctx.host.dockerOk"
      :description="ctx.host.error || '该主机 Docker 不可用'"
    >
      <ElButton size="small" @click="$emit('refresh')">重新检测</ElButton>
    </ElEmpty>
    <template v-else>
      <slot name="search" />
      <ElCard class="art-table-card" shadow="never">
        <slot name="table" />
        <slot name="footer" />
      </ElCard>
    </template>
  </div>
</template>

<script setup lang="ts">
  import { ElButton, ElCard, ElEmpty } from 'element-plus'
  import HostSwitcher from './host-switcher.vue'
  import { staleClass, syncText } from '../utils/host'
  import { useDockerHost } from '../utils/host-context'

  withDefaults(
    defineProps<{
      loading?: boolean
      stale?: boolean
      ageSeconds?: number
      neverReported?: boolean
    }>(),
    { loading: false, stale: false, ageSeconds: 0, neverReported: false }
  )
  defineEmits<{ refresh: [] }>()

  // 解构会丢掉 getter 的响应性（host 只在此刻求值一次，主机清单到达后永远看不到），
  // 故保留 ctx 对象在模板里按 `ctx.host` 访问 —— 首次加载 host 为 undefined 时
  // 由模板的 `v-if="ctx.host && …"` 守卫。
  const ctx = useDockerHost()
</script>
