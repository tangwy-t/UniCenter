<template>
  <!-- 单根（single-root 守卫在库：布局的 Transition 只支持单根）。整块磁贴是按钮：
       它是「下钻到对应列表页」的入口，语义上是一个动作。 -->
  <button
    type="button"
    class="dov-card dov-kpi kpi-tile"
    :style="{ '--tile': tile.tile }"
    :title="tile.title"
    @click="emit('select', tile)"
  >
    <div class="kpi-tile__icon flex-cc">
      <ArtSvgIcon :icon="tile.icon" />
    </div>
    <div class="min-w-0 flex-1">
      <div class="kpi-tile__value truncate">{{ tile.value }}</div>
      <div class="kpi-tile__label">{{ tile.label }}</div>
      <div class="kpi-tile__sub truncate">
        <span
          v-for="(s, i) in tile.sub"
          :key="i"
          class="kpi-tile__seg"
          :class="s.tone ? `is-${s.tone}` : ''"
          >{{ s.text }}</span
        >
      </div>
    </div>
  </button>
</template>

<script setup lang="ts">
  /**
   * KPI 磁贴：舰队账目的一块（主数值 + 分项），数据形状由 utils/overview 的
   * buildOverviewKpis 给出（分项语义色在那边定义 —— 颜色恒与「需要注意」绑定）。
   * 纯展示件：点击只 emit，导航（router.push / 锚点滚动）在页面层收口。
   */
  import type { OverviewKpiTile } from '../utils/overview'

  defineOptions({ name: 'DockerOverviewKpiTile' })

  defineProps<{ tile: OverviewKpiTile }>()
  const emit = defineEmits<{ (e: 'select', tile: OverviewKpiTile): void }>()
</script>

<style lang="scss" scoped>
  /* 令牌数值复制自 monitor-tokens（经本模块 views/overview-tokens，见其文件头注释） */
  @use '../views/overview-tokens' as t;

  @include t.rise-keyframes;

  .dov-kpi {
    @include t.card;
    @include t.rise;

    display: flex;
    align-items: center;
    gap: 0.75rem;
    width: 100%;
    text-align: left;
    font: inherit;
    color: inherit;
    cursor: pointer;
    /* 可交互磁贴（对齐 server.vue 的 .sv-card）：hover 抬起是「这块能点」的预告 */
    transition:
      transform 0.2s ease,
      box-shadow 0.2s ease,
      border-color 0.2s ease;
  }

  .dark .dov-kpi {
    @include t.card-dark;
  }

  .dov-kpi:hover {
    transform: translateY(-2px);
    box-shadow: 0 8px 18px rgba(16, 24, 40, 0.09);
    border-color: var(--el-color-primary-light-5);
  }

  .dov-kpi:focus-visible {
    outline: 2px solid var(--el-color-primary);
    outline-offset: 2px;
  }

  .kpi-tile__icon {
    @include t.kpi-icon;
    font-size: 19px;
    color: var(--el-color-white);
    /* 渐变向 var(--el-bg-color) 收（明色提亮、暗色压暗）—— 磁贴基色走 EP 变量零写死 */
    background: linear-gradient(
      135deg,
      color-mix(in srgb, var(--tile) 78%, var(--el-bg-color)) 0%,
      var(--tile) 100%
    );
    box-shadow: 0 4px 10px color-mix(in srgb, var(--tile) 35%, transparent);
  }

  .kpi-tile__value {
    @include t.kpi-value;
  }

  .kpi-tile__label {
    @include t.kpi-label;
  }

  .kpi-tile__sub {
    @include t.kpi-sub;
    /* 没有分项的网络磁贴也占住这一行：六块等高，栅格不跳 */
    min-height: 15px;
    line-height: 15px;
  }

  .kpi-tile__seg + .kpi-tile__seg::before {
    content: '·';
    margin: 0 4px;
    color: var(--el-text-color-placeholder);
  }

  /* 分项语义色（EP 既有色，不新增调色板）：哪些数字上色由 utils/overview 决定 */
  .kpi-tile__seg.is-success {
    color: var(--el-color-success);
  }

  .kpi-tile__seg.is-warning {
    color: var(--el-color-warning);
  }

  .kpi-tile__seg.is-danger {
    color: var(--el-color-danger);
  }

  @include t.reduced-motion('.dov-kpi');
</style>
