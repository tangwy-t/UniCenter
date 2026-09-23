<template>
  <!--
    列表页的「水位单元格」：细进度条 + 数值。

    为什么是「条 + 数字」而不是只留条或只留数字：
    - 只留数字（改动前）：三列都是 `35.8%` 这样的文本，要横向比较三台设备/三个维度
      的高低，只能逐个读数字并心算。
    - 只留条：能比高低，但读不出确切值；水位排障时「到底 89 还是 91」决定了
      要不要立刻处理，只看长度是看不出来的。
    两者并存则各取所长：条负责「一眼比高低」，数字负责「精确值」。

    为什么不做成饼图/仪表盘等图形：单元格宽度只有 ~140px，任何图形在这个尺寸下
    都会退化成一团色块，可读性反而不如一条直线；表格里同一列重复几十次时，
    图形的装饰成本也会累积成噪声。故这里用最朴素的条形。

    列宽**保持 120px 不变**（刻意的）：扣掉单元格左右各 12px 内边距后可用 96px，
    正好分给「条」48px + 间距 6px + 「数值」42px。实测该表在李 13 列下本就
    横向溢出（需 1664px、可视 1287px），三列若各加到 140px 会让「磁盘使用率」
    的右对齐数值正好落在 1600px 视口之外 —— 用户只看得到半根条、看不到数字。
    故这里靠压缩字号（12px + tabular-nums）换空间，而不是加宽列。
  -->
  <div class="wb">
    <template v-if="hasValue">
      <!-- 条是纯装饰，真实值由右侧文本承载，故对读屏隐藏。 -->
      <div class="wb__track" aria-hidden="true">
        <div class="wb__fill" :style="{ width: `${percent}%`, background: tone }" />
      </div>
      <span class="wb__value" :style="{ color: tone }">{{ text }}</span>
    </template>

    <!--
      缺值**不画条**：一条空的灰轨道会被读成「使用率 0%」，而 0% 与
      「这台设备还没上报过水位」在容量判断上含义完全相反 —— 前者说明机器很闲，
      后者说明这台机器根本不在采集。后端对缺值用 omitempty（字段直接不出现），
      前端就照实显示「—」。
    -->
    <span v-else class="wb__empty" title="该设备尚未上报水位数据">—</span>
  </div>
</template>

<script setup lang="ts">
  import { computed } from 'vue'
  import { clampPercent, formatPercent, usageTone } from '../utils/display'

  const props = defineProps<{
    /** 水位百分比；缺值（null/undefined/NaN）显示「—」。 */
    value?: number | null
  }>()

  const hasValue = computed(() => typeof props.value === 'number' && Number.isFinite(props.value))
  /** 条长只表达「有多满」，缺值不会走到这里（v-else 分支另有渲染）。 */
  const percent = computed(() => clampPercent(props.value))
  const text = computed(() => formatPercent(props.value))
  /** 与详情页健康卡、监控页同一套阈值配色，避免同一数值两种颜色。 */
  const tone = computed(() => usageTone(props.value))
</script>

<style scoped lang="scss">
  .wb {
    display: flex;
    gap: 6px;
    align-items: center;
    min-height: 20px;
  }

  .wb__track {
    flex: 1;
    min-width: 0;
    height: 6px;
    overflow: hidden;
    background: var(--el-fill-color-light);
    border-radius: 3px;
  }

  .wb__fill {
    height: 100%;
    border-radius: 3px;
    transition: width 0.35s ease;
  }

  .wb__value {
    flex-shrink: 0;
    /* 46px 是按 `100.0%`（最长可能值）在 13px 字号下的宽度定的：再窄会换行，
       换行会把行高顶开，整列高度参差。 */
    /* 42px：`100.0%`（最长可能值）在 12px + tabular-nums 下的宽度。
       再窄会换行，把该行行高顶开、整列高度参差。 */
    min-width: 42px;
    font-size: 12px;
    font-weight: 500;
    text-align: right;
    /* 等宽数字：三列数值的小数点在垂直方向对齐，便于逐行比对。 */
    font-variant-numeric: tabular-nums;
  }

  .wb__empty {
    font-size: 12px;
    color: var(--el-text-color-placeholder);
  }

  /* 尊重系统的「减少动态效果」设置：批量刷新时几十条同时补间会让人不适。 */
  @media (prefers-reduced-motion: reduce) {
    .wb__fill {
      transition: none;
    }
  }
</style>
