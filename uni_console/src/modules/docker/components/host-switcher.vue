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

<style lang="scss" scoped>
  // 主机条本体（「主机」标签 + 下拉 + 调用方塞进来的刷新按钮/同步文案）：
  // 一行居中放下，窄屏换行而不是互相挤压。
  .docker-host {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;
  }

  // 字段标签：次要文字色，不与下拉里的主机名抢视觉。
  .docker-host__label {
    color: var(--el-text-color-secondary);
    font-size: 13px;
  }

  // ElSelect 默认 width:100%（--el-select-width），放进 flex 行会吞掉整行：
  // 固定一个稳定宽度；min-width:0 允许极窄屏下继续收缩（下拉内部自己省略号）。
  .docker-host__select {
    width: 220px;
    min-width: 0;
  }

  // 下拉项一行：圆点 + 主机名 + 在线/离线副文案。
  .docker-host__opt {
    display: flex;
    gap: 8px;
    align-items: center;
  }

  // 状态圆点：主机 Docker 可用性唯一的视觉判据（绿=可用 / 红=不可用）。
  // 固定 8px + flex-shrink:0：换行/挤压下不变形、不被吞。
  .docker-host__dot {
    display: inline-block;
    width: 8px;
    height: 8px;
    flex-shrink: 0;
    border-radius: 50%;

    &.is-ok {
      background: var(--el-color-success);
    }

    &.is-bad {
      background: var(--el-color-danger);
    }
  }

  // 在线/离线副文案：小号次要色，推到下拉项右端与主机名分开。
  .docker-host__sub {
    margin-left: auto;
    color: var(--el-text-color-secondary);
    font-size: 12px;
  }
</style>
