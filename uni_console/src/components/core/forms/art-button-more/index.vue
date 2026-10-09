<!-- 更多按钮 -->
<template>
  <div>
    <ElDropdown v-if="hasAnyAuthItem" trigger="click">
      <!-- trigger="click"：默认 hover 触发下键盘 Enter 打不开菜单（tooltip trigger 的
           keydown 只在 click/hover 显式匹配时 toggle；click 让 Enter/Space 生效）。
           aria-label 给行级「更多」一个读屏名称，ArtIconButton 的 keydown 不冒泡重复
           开关（ElDropdown 的 triggerKeys 在同一 keydown 上先 preventDefault 并 toggle）。
           hover 打开路径保留在移入后的下拉区，行为不回退。 -->
      <ArtIconButton
        icon="ri:more-2-fill"
        class="!size-8 bg-g-200 dark:bg-g-300/45 text-sm"
        aria-label="更多操作"
      />
      <template #dropdown>
        <ElDropdownMenu>
          <template v-for="item in list" :key="item.key">
            <ElDropdownItem
              v-if="!item.auth || hasAuth(item.auth)"
              :disabled="item.disabled"
              @click="handleClick(item)"
            >
              <div class="flex-c gap-2" :style="{ color: item.color }">
                <ArtSvgIcon v-if="item.icon" :icon="item.icon" />
                <span>{{ item.label }}</span>
              </div>
            </ElDropdownItem>
          </template>
        </ElDropdownMenu>
      </template>
    </ElDropdown>
  </div>
</template>

<script setup lang="ts">
  import { useAuth } from '@/hooks/core/useAuth'

  defineOptions({ name: 'ArtButtonMore' })

  const { hasAuth } = useAuth()

  export interface ButtonMoreItem {
    /** 按钮标识，可用于点击事件 */
    key: string | number
    /** 按钮文本 */
    label: string
    /** 是否禁用 */
    disabled?: boolean
    /** 权限标识 */
    auth?: string
    /** 图标组件 */
    icon?: string
    /** 文本颜色 */
    color?: string
    /** 图标颜色（优先级高于 color） */
    iconColor?: string
  }

  interface Props {
    /** 下拉项列表 */
    list: ButtonMoreItem[]
    /** 整体权限控制 */
    auth?: string
  }

  const props = withDefaults(defineProps<Props>(), {})

  // 检查是否有任何有权限的 item
  const hasAnyAuthItem = computed(() => {
    return props.list.some((item) => !item.auth || hasAuth(item.auth))
  })

  const emit = defineEmits<{
    (e: 'click', item: ButtonMoreItem): void
  }>()

  const handleClick = (item: ButtonMoreItem) => {
    emit('click', item)
  }
</script>
