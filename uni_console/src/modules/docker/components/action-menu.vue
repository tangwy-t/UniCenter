<template>
  <ArtButtonMore :list="items" @click="onClick" />
</template>

<script setup lang="ts">
  import { computed } from 'vue'
  import ArtButtonMore from '@/components/core/forms/art-button-more/index.vue'
  import { useAuth } from '@/hooks/core/useAuth'
  import { PermDockerExec } from '@/enums/permission'
  import { lookupDockerAction } from '../utils/actions'

  defineOptions({ name: 'DockerActionMenu' })

  /** ArtButtonMore 的条目形状（只列本组件用到的字段；key 照它的 `string | number`）。 */
  interface MenuItem {
    key: string | number
    label: string
    icon?: string
    auth?: string
    color?: string
    disabled?: boolean
  }

  interface Props {
    /** 要生成条目的动作清单（注册表内的二期写动作，顺序照传入顺序）。 */
    actions: string[]
    /** 这一行的目标名（emit 原样带回，也用于行级 pending 比对）。 */
    target: string
    /** 目标是否受保护（快照里的 protected 结论）。 */
    protected?: boolean
    /** 当前行级 pending 的键（useDockerCmds 的 pendingId）：与 target 相等时整体禁用。 */
    pendingId?: string | null
    /** 额外禁用（如镜像 in_use 不能删）。 */
    disabled?: boolean
    /** 危险项的标红颜色。 */
    dangerColor?: string
    /**
     * 只读等**非写动作**的条目（如「详情」「日志」）：原样插在写动作之前。
     *
     * 为什么不让调用方自己拼 ArtButtonMore：写动作的权限/禁用/🔒 规则都在这份实现里，
     * 页面另拼一份就等于两处各维护一遍。注意它们不受 `protected`/`pendingId` 影响 ——
     * 只读操作任何时候都该能点开看。
     */
    extraItems?: MenuItem[]
  }

  const props = withDefaults(defineProps<Props>(), {
    protected: false,
    pendingId: null,
    disabled: false,
    dangerColor: 'var(--art-danger)',
    extraItems: () => []
  })

  const emit = defineEmits<{
    (e: 'select', payload: { action: string; target: string }): void
  }>()

  const { hasAuth } = useAuth()

  const canForce = computed(() => props.protected && hasAuth(PermDockerExec))

  const items = computed<MenuItem[]>(() => [
    ...props.extraItems,
    ...props.actions.flatMap((action) => {
      const entry = lookupDockerAction(action)
      // 注册表里没有的动作不进菜单：宁可少一项，也不生成一个没权限/没确认档的裸按钮。
      if (!entry) return []
      // 受保护目标 + 没有强制权限：受保护档约束的动作不可执行（勾了也没用），
      // 条目禁用并带上锁标记（结论句由页面在行上给出）。
      const blocked = props.protected && entry.guarded && !canForce.value
      const pending = props.pendingId != null && props.pendingId === props.target
      return [
        {
          key: action,
          label: blocked ? `🔒 ${entry.label}` : entry.label,
          icon: entry.icon,
          auth: entry.perm,
          color: entry.danger === 'normal' ? undefined : props.dangerColor,
          disabled: props.disabled || pending || blocked
        }
      ]
    })
  ])

  function onClick(item: MenuItem) {
    emit('select', { action: String(item.key), target: props.target })
  }
</script>
