<template>
  <!-- 单根（single-root 守卫扫全模块 .vue）。内存列的小 meter：轨道 + 填充 + 「已用 / 上限」。
       形制照 components/overview-disk-panel.vue 的轨道+fill（4px 端圆角、轨道恒可见），
       但**不 import device 模块组件**（跨模块耦合）—— 本地一份最小实现。 -->
  <span class="wkl-meter">
    <span class="wkl-meter__track" aria-hidden="true">
      <span class="wkl-meter__fill" :style="{ width: `${pct}%` }"></span>
    </span>
    <span class="wkl-meter__text">{{ text }}</span>
  </span>
</template>

<script setup lang="ts">
  /**
   * 内存用量 meter（Wave 3a）：容器内存列从「一段会折行/被截断的组合文案」换成
   * 「轨道 + 数值」。填充宽度 = 已用/上限（`pct`），文本 = 「已用 / 上限」（单位 MB
   * 进列头）。缺值不经过本组件 —— 列在无用量时直接渲染「—」（不画空条，见
   * workload-table 的 memUsage 判据）。
   *
   * 色值全走 EP 变量零写死（与 overview-tokens 的立规一致）；明暗主题各自成立。
   */
  defineOptions({ name: 'DockerMemMeter' })

  withDefaults(defineProps<{ pct?: number; text?: string }>(), { pct: 0, text: '' })
</script>

<style lang="scss" scoped>
  .wkl-meter {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    min-width: 0;
    // 靠右（列 align:right 的语义）：数值贴右缘，轨道在它左侧。
    justify-content: flex-end;
  }

  // 轨道 = 填充色同 ramp 更浅一档（meter 纪律）：未填充部分恒可见 —— 0% 也画得出
  // 一条空轨道，与「没有这条」区分开。色值与填充同源一档，明暗各自成立。
  // Wave 3c：基准宽 56px 但**可收缩**（flex-basis 56 / min 24）—— 单元格可用宽不够时
  // 先压轨道、绝不压文本，保证「已用 / 上限」的分母无论在哪种列宽/视口都完整可读
  // （真机截图里正是分母被截成了「9946.2…」）。
  .wkl-meter__track {
    position: relative;
    display: block;
    flex: 0 1 56px;
    min-width: 24px;
    height: 8px;
    border-radius: 4px;
    background: color-mix(in srgb, var(--el-color-primary) 14%, transparent);
  }

  .wkl-meter__fill {
    position: absolute;
    inset: 0 auto 0 0;
    border-radius: 4px;
    background: var(--el-color-primary);
  }

  // 文本不收缩（flex 0 0 auto）：分母是这列的主事实，轨道可让位、它不让位。
  .wkl-meter__text {
    flex: 0 0 auto;
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
  }
</style>
