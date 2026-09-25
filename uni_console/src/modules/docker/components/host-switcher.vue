<template>
  <div class="docker-host">
    <span class="docker-host__label">主机</span>
    <ElSelect
      :model-value="ctx.hostId"
      class="docker-host__select"
      placeholder="选择主机"
      :loading="ctx.loading"
      @change="ctx.selectHost"
    >
      <ElOption v-for="h in ctx.hosts" :key="h.id" :label="hostLabel(h)" :value="h.id">
        <span class="docker-host__opt">
          <span class="docker-host__dot" :class="h.dockerOk ? 'is-ok' : 'is-bad'" />
          {{ hostLabel(h) }}
          <span class="docker-host__sub">{{ h.online ? '在线' : '离线' }}</span>
        </span>
      </ElOption>
    </ElSelect>
    <slot />
  </div>
</template>

<script setup lang="ts">
  import { ElOption, ElSelect } from 'element-plus'
  import { hostLabel } from '../utils/host'
  import { useDockerHost } from '../utils/host-context'

  const ctx = useDockerHost()
</script>
