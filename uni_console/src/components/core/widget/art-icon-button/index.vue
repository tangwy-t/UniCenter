<!-- 按钮组件 -->
<template>
  <!-- 无障碍范式照 art-button-table：role/tabindex/aria-label + Enter/Space 触发
       click。纯 div 无原生按钮语义，AT 与键盘此前完全不可达。 -->
  <div
    role="button"
    tabindex="0"
    class="size-8.5 inline-flex flex-cc c-p text-g-600 dark:text-g-800 text-xl rounded tad-300 hover:bg-hover-color"
    :class="{ 'rounded-full': circle }"
    :aria-label="ariaLabel"
    @click="emit('click', $event)"
    @keydown.enter.prevent="emit('click', $event)"
    @keydown.space.prevent="emit('click', $event)"
  >
    <ArtSvgIcon :icon="icon"></ArtSvgIcon>
    <slot></slot>
  </div>
</template>

<script lang="ts" setup>
  defineOptions({ name: 'ArtIconButton' })

  interface Props {
    /** 图标名称 */
    icon: string
    /** 圆角按钮 */
    circle?: boolean
    /** 无障碍名称（读屏播报；调用方未传时回退给图标名兜底） */
    ariaLabel?: string
  }

  withDefaults(defineProps<Props>(), {
    ariaLabel: undefined
  })

  // 透传原生事件载荷（主题切换的圆形扩散动画取 e.clientX/Y 定圆心）；
  // 键盘触发时原生事件是 KeyboardEvent，消费方按 DOM 惯例读坐标字段（undefined
  // 时落视口中心兜底），与原生 <button> 的行为一致。
  const emit = defineEmits<{
    (e: 'click', event: MouseEvent | KeyboardEvent): void
  }>()
</script>
