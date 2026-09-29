<template>
  <ElDialog
    :model-value="modelValue"
    :title="`${form.label}确认`"
    width="460px"
    :close-on-click-modal="false"
    :close-on-press-escape="!loading"
    @update:model-value="emit('update:modelValue', $event)"
    @closed="reset"
  >
    <!-- 卡片：这一屏只回答两件事 —— 要动的是谁、后果是什么。 -->
    <div class="ac-card" :class="{ 'is-danger': form.danger !== 'normal' }">
      <span v-if="form.protected" class="ac-card__lock" title="受保护">🔒</span>
      <div class="ac-card__body">
        <div class="ac-card__target">{{ form.targetText }}</div>
        <div v-if="form.conclusion" class="ac-card__line">{{ form.conclusion }}</div>
        <div v-if="form.protectedConclusion" class="ac-card__line">{{
          form.protectedConclusion
        }}</div>
        <!-- 批量操作等场景的补充结论（如逐条列出的目标名）由调用方给出。 -->
        <slot />
      </div>
    </div>

    <!-- 逐字确认：强档必须一字不差，输入不匹配时主按钮禁用。 -->
    <div v-if="form.needsInput" class="ac-input">
      <div class="ac-input__label">{{ form.inputLabel }}</div>
      <ElInput
        v-model="input"
        :placeholder="form.inputPlaceholder"
        :disabled="loading"
        @keyup.enter="onConfirm"
      />
    </div>

    <!-- 受保护目标 + 有强制权限才出现；勾选后才带 force 发给服务端。 -->
    <ElCheckbox v-if="form.showForce" v-model="force" class="ac-force" :disabled="loading">
      强制操作
    </ElCheckbox>

    <template #footer>
      <ElButton :disabled="loading" @click="emit('update:modelValue', false)">取消</ElButton>
      <ElButton type="danger" :disabled="!canSubmit" :loading="loading" @click="onConfirm">
        {{ form.buttonText }}
      </ElButton>
    </template>
  </ElDialog>
</template>

<script setup lang="ts">
  import { computed, ref } from 'vue'
  import { ElButton, ElCheckbox, ElDialog, ElInput } from 'element-plus'
  import { useAuth } from '@/hooks/core/useAuth'
  import { PermDockerExec } from '@/enums/permission'
  import type { DockerActionOptions } from '../utils/actions'
  import { confirmForm, confirmInputValid, type ConfirmOverride } from '../utils/confirm'

  defineOptions({ name: 'DockerActionConfirm' })

  interface Props {
    modelValue: boolean
    /** 要执行的动作（注册表内的二期写动作；只读动作不走确认弹窗）。 */
    action: string
    /** 目标名（展示 + 逐字比对的来源）。 */
    target?: string
    /** 判定确认档所需的 options（如 scale 的 n、save 的 overwrite/filename）。 */
    options?: DockerActionOptions
    /** 目标是否受保护（快照里的 protected 结论）。 */
    targetProtected?: boolean
    /** 目标名词（容器/镜像/数据卷/网络/项目/服务），用于逐字输入提示。 */
    targetKind?: string
    /** 提交中：禁用按钮与关闭。 */
    loading?: boolean
    /** 注册表之外的动作（四期配置编辑）：标签/结论/确认档/期望值由调用方给出。 */
    override?: ConfirmOverride
  }

  const props = withDefaults(defineProps<Props>(), {
    target: '',
    options: undefined,
    targetProtected: false,
    targetKind: '',
    loading: false,
    override: undefined
  })

  const emit = defineEmits<{
    (e: 'update:modelValue', value: boolean): void
    /** confirm 为空串 = 标准档（后端不要求逐字值）；force 仅在开关出现且被勾选时为 true。 */
    (e: 'confirm', payload: { confirm: string; force: boolean }): void
  }>()

  const { hasAuth } = useAuth()

  const input = ref('')
  const force = ref(false)

  const form = computed(() =>
    confirmForm(
      {
        action: props.action,
        target: props.target,
        options: props.options,
        targetProtected: props.targetProtected,
        targetKind: props.targetKind,
        override: props.override
      },
      hasAuth(PermDockerExec)
    )
  )

  const canSubmit = computed(
    () =>
      !props.loading &&
      confirmInputValid(form.value, input.value) &&
      // 受保护目标必须勾选「强制操作」：不勾就提交的话服务端一定拒（agent 的保护判定在它那侧），
      // 用户看到的会是一句「失败了」而不是「你还差一步」—— 把这一步做进按钮状态里。
      (!form.value.showForce || force.value)
  )

  function onConfirm() {
    if (!canSubmit.value) return
    emit('confirm', { confirm: form.value.needsInput ? input.value : '', force: force.value })
  }

  /** 关闭后清掉输入与勾选：下次打开是同一动作的不同目标，不能沿用上一次的确认状态。 */
  function reset() {
    input.value = ''
    force.value = false
  }
</script>

<style lang="scss" scoped>
  // 目标卡片：结论放在第一屏，用边框色区分「常规」与「有破坏性」。
  .ac-card {
    display: flex;
    gap: 10px;
    padding: 12px 14px;
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 8px;
    background: var(--default-box-color, var(--el-fill-color-light));

    &.is-danger {
      border-color: var(--el-color-danger-light-5);
      background: var(--el-color-danger-light-9);
    }

    &__lock {
      flex: none;
      font-size: 16px;
      line-height: 1.4;
    }

    &__body {
      min-width: 0;
    }

    &__target {
      font-size: 14px;
      font-weight: 600;
      word-break: break-all;
    }

    &__line {
      margin-top: 6px;
      color: var(--el-text-color-secondary);
      font-size: 13px;
      line-height: 1.5;
    }
  }

  .ac-input {
    margin-top: 14px;

    &__label {
      margin-bottom: 6px;
      font-size: 13px;
    }
  }

  .ac-force {
    margin-top: 12px;
  }
</style>
