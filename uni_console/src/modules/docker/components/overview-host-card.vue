<template>
  <!-- 单根（single-root 守卫在库：布局的 Transition 只支持单根）。整卡是按钮：
       卡片就是「下钻到该主机容器列表」的入口，语义上它是一个动作，不是一块内容。 -->
  <button
    type="button"
    class="dov-host dov-card"
    :title="`查看 ${label} 上的容器列表`"
    @click="emit('open', host)"
  >
    <div class="dov-host__head">
      <!-- 在线点：绿=在线；灰=离线（最后已知状态仍展示 —— 后端口径） -->
      <span class="dov-dot" :class="host.online ? 'is-success' : 'is-off'" aria-hidden="true" />
      <span class="dov-host__name truncate">{{ label }}</span>
      <span v-if="!host.online" class="dov-host__offline">离线</span>
    </div>
    <div class="dov-host__ip dov-mono truncate">{{ host.primaryIp || '—' }}</div>
    <div class="dov-host__verdict" :class="`is-${verdict.tone}`">{{ verdict.text }}</div>
    <div class="dov-host__sync" :class="syncWarn ? 'is-warn' : ''">{{ sync }}</div>
    <div class="dov-host__counts">容器 {{ host.containers }} · 镜像 {{ host.images }}</div>
  </button>
</template>

<script setup lang="ts">
  /**
   * 主机卡片：控制塔的「就绪板」一行 —— 一台主机一张卡，三条事实线各说一件事：
   *   ① 头行：谁（主机名/IP）+ 在线点；
   *   ② 结论行：Docker 能力可用吗（服务端算好的结论句，前端不重复判断）；
   *   ③ 同步行：这批数据是几点的（syncText 的三句结论，utils/host.ts 同一口径）。
   * 计数行只是补充读数。点击整卡 → 该主机的容器列表（query 约定见 host-context.ts）。
   */
  import { computed } from 'vue'
  import type { DockerHostItem } from '../api'
  import { hostLabel } from '../utils/host'
  import { hostVerdict, hostSyncText, hostSyncTone } from '../utils/overview'

  defineOptions({ name: 'DockerOverviewHostCard' })

  const props = defineProps<{ host: DockerHostItem }>()
  const emit = defineEmits<{ (e: 'open', host: DockerHostItem): void }>()

  const label = computed(() => hostLabel(props.host))
  const verdict = computed(() => hostVerdict(props.host))
  const sync = computed(() => hostSyncText(props.host))
  /** syncText 在取值期换算「现在」，computed 依赖 props 即可（时间口径由纯函数管）。 */
  const syncWarn = computed(() => hostSyncTone(props.host) === 'warning')
</script>

<style lang="scss" scoped>
  /* 令牌数值复制自 monitor-tokens（经本模块 views/overview-tokens，见其文件头注释） */
  @use '../views/overview-tokens' as t;

  @include t.rise-keyframes;

  .dov-host {
    @include t.card;
    @include t.rise;

    display: block;
    width: 100%;
    text-align: left;
    font: inherit;
    color: inherit;
    cursor: pointer;
    /* 可交互卡片（对齐 server.vue 的 .sv-card）：hover 抬起是「这块能点」的预告 */
    transition:
      transform 0.2s ease,
      box-shadow 0.2s ease,
      border-color 0.2s ease;
  }

  .dark .dov-host {
    @include t.card-dark;
  }

  .dov-host:hover {
    transform: translateY(-2px);
    box-shadow: 0 8px 18px rgba(16, 24, 40, 0.09);
    border-color: var(--el-color-primary-light-5);
  }

  .dov-host:focus-visible {
    outline: 2px solid var(--el-color-primary);
    outline-offset: 2px;
  }

  .dov-host__head {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
  }

  .dov-host__name {
    font-weight: 600;
    font-size: 14px;
    color: var(--el-text-color-primary);
  }

  .dov-host__offline {
    flex: none;
    padding: 0 6px;
    border-radius: 999px;
    background: var(--el-fill-color);
    color: var(--el-text-color-secondary);
    font-size: 11px;
    line-height: 18px;
  }

  .dov-host__ip {
    margin-top: 2px;
    font-size: 12px;
    color: var(--el-text-color-secondary);
  }

  .dov-host__verdict {
    margin-top: 8px;
    font-size: 13px;
    font-weight: 500;

    &.is-success {
      color: var(--el-color-success);
    }

    &.is-danger {
      color: var(--el-color-danger);
    }
  }

  .dov-host__sync {
    margin-top: 1px;
    font-size: 11px;
    color: var(--el-text-color-secondary);

    &.is-warn {
      color: var(--el-color-warning);
    }
  }

  .dov-host__counts {
    margin-top: 8px;
    padding-top: 8px;
    border-top: 1px solid var(--default-border);
    font-size: 12px;
    color: var(--el-text-color-regular);
    font-variant-numeric: tabular-nums;
  }

  /* 状态圆点（异常表同款，定义一份两边 @use 不了的 scoped 限制下各自声明） */
  .dov-dot {
    flex: none;
    width: 8px;
    height: 8px;
    border-radius: 50%;

    &.is-success {
      background: var(--el-color-success);
    }

    &.is-off {
      background: var(--el-color-info);
    }
  }

  .dov-mono {
    font-family: var(--el-font-family-monospace, monospace);
  }

  @include t.reduced-motion('.dov-host');
</style>
