<!-- 表格按钮 -->
<template>
  <div
    v-show="visible"
    role="button"
    :tabindex="props.disabled ? -1 : 0"
    :title="props.title"
    :aria-label="props.title"
    :aria-disabled="props.disabled ? 'true' : undefined"
    :class="[
      'inline-flex items-center justify-center min-w-8 h-8 px-2.5 mr-2.5 text-sm rounded-md align-middle',
      'transition-opacity duration-150',
      buttonClass,
      props.disabled ? 'cursor-not-allowed opacity-45' : 'c-p hover:opacity-80'
    ]"
    :style="{ backgroundColor: buttonBgColor, color: iconColor }"
    @click="handleClick"
    @keydown.enter.prevent="handleClick"
    @keydown.space.prevent="handleClick"
  >
    <ArtSvgIcon :icon="iconContent" />
  </div>
</template>

<script setup lang="ts">
  import { computed } from 'vue'
  import { useUserStore } from '@/store/modules/user'
  import { hasAuthPermission } from '@/directives/core/auth-permission'

  defineOptions({ name: 'ArtButtonTable' })

  interface Props {
    /** 按钮类型 */
    type?: 'add' | 'edit' | 'delete' | 'more' | 'view'
    /** 按钮图标 */
    icon?: string
    /** 按钮样式类 */
    iconClass?: string
    /** icon 颜色 */
    iconColor?: string
    /** 按钮背景色 */
    buttonBgColor?: string
    /** 提示文本(tooltip + aria-label) */
    title?: string
    /** 禁用(不可聚焦、不可点击、半透明) */
    disabled?: boolean
    /** 权限标识(无权限时整钮隐藏，display:none) */
    auth?: string | string[]
  }

  const props = withDefaults(defineProps<Props>(), {})

  // 权限显隐：无 auth 视为放行；有 auth 时按 v-perm 语义(字符串/数组 OR)判断。
  const visible = computed(() => {
    if (!props.auth) return true
    const perms = useUserStore().info?.permissions ?? []
    return hasAuthPermission(perms, props.auth)
  })

  const emit = defineEmits<{
    (e: 'click'): void
  }>()

  // 默认按钮配置
  const defaultButtons = {
    add: { icon: 'ri:add-fill', class: 'bg-theme/12 text-theme' },
    edit: { icon: 'ri:pencil-line', class: 'bg-secondary/12 text-secondary' },
    delete: { icon: 'ri:delete-bin-5-line', class: 'bg-danger/12 text-danger' },
    view: { icon: 'ri:eye-line', class: 'bg-info/12 text-info' },
    more: { icon: 'ri:more-2-fill', class: '' }
  } as const

  // 获取图标内容
  const iconContent = computed(() => {
    return props.icon || (props.type ? defaultButtons[props.type]?.icon : '') || ''
  })

  // 获取按钮样式类
  const buttonClass = computed(() => {
    return props.iconClass || (props.type ? defaultButtons[props.type]?.class : '') || ''
  })

  const handleClick = () => {
    if (props.disabled) return
    emit('click')
  }
</script>
