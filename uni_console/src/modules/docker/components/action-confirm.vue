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
      <!-- 锁语义与列表保护列同款（锁图标 + 受保护）：不用 🔒 emoji ——
           无 emoji 字体的环境里会渲染成豆腐块；锁走 ArtSvgIcon 的图标范式。 -->
      <span v-if="form.protected" class="ac-card__lock">
        <ArtSvgIcon icon="ri:lock-2-line" />
        受保护
      </span>
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

    <!-- 逐字档与输入档共用这只输入框：强档一字不差（期望值比对），输入档走
         条目校验（非法值禁提交）；不通过时主按钮禁用。 -->
    <div v-if="form.needsInput" class="ac-input">
      <div class="ac-input__label">{{ form.inputLabel }}</div>
      <ElInput
        v-model="input"
        :placeholder="form.inputPlaceholder"
        :disabled="loading"
        @keyup.enter="onConfirm"
      />
      <!-- 输入档的操作提示（如「只填文件名，产物落在 agent 下载目录」）。 -->
      <div v-if="form.inputHint" class="ac-input__hint">{{ form.inputHint }}</div>
      <!-- 输入档的就地错误（空串不显：空值靠按钮禁用表达，不该一开口就挨骂）。 -->
      <div v-if="inputError" class="ac-input__error">{{ inputError }}</div>
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
  import { computed, ref, watch } from 'vue'
  import { ElButton, ElCheckbox, ElDialog, ElInput } from 'element-plus'
  import ArtSvgIcon from '@/components/core/base/art-svg-icon/index.vue'
  import { useAuth } from '@/hooks/core/useAuth'
  import { PermDockerExec } from '@/enums/permission'
  import type { DockerActionOptions } from '../utils/actions'
  import {
    actionInputError,
    confirmForm,
    confirmInputValid,
    needsWordInput,
    type ConfirmOverride
  } from '../utils/confirm'

  defineOptions({ name: 'DockerActionConfirm' })

  interface Props {
    modelValue: boolean
    /** 要执行的动作（注册表内的写动作；只读动作不走确认弹窗）。 */
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
    /** 注册表之外的动作（配置编辑）：标签/结论/确认档/期望值由调用方给出。 */
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
    /**
     * confirm 为空串 = 标准档/输入档（后端不要求逐字值）；force 仅在开关出现且
     * 被勾选时为 true；value 仅输入档给出 —— 收集到的参数值（已裁剪首尾空白，
     * 调用方把它放进自己的 options 字段）。
     */
    (e: 'confirm', payload: { confirm: string; force: boolean; value?: string }): void
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

  /** 输入档的就地错误句（非空但不合法时显示；其余形态恒空）。 */
  const inputError = computed(() => actionInputError(form.value, input.value))

  // 形态或期望值变化时清空已输入的值：image:save 的两段式会在**同一只弹窗**里从
  // 输入档（填文件名）切到逐字档（照抄文件名确认覆盖）—— 第二段必须重新照抄，
  // 不能沿用第一段已填的值（否则「照抄一遍」退化成「直接点确定」）。关闭重开的
  // 路径由 @closed 的 reset 兜底，这里只兜「不关就切」的那条。
  watch(
    () => [form.value.kind, form.value.expected],
    () => {
      input.value = ''
    }
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
    // 逐字档把输入**原样**作为 confirm 值（协议逐字比对，不裁剪）；输入档的
    // confirm 恒空（协议不要求），值走 value 单独带回（裁剪首尾空白）。
    emit('confirm', {
      confirm: form.value.needsInput && needsWordInput(form.value.kind) ? input.value : '',
      force: force.value,
      value: form.value.kind === 'input' ? input.value.trim() : undefined
    })
  }

  /** 关闭后清掉输入与勾选：下次打开是同一动作的不同目标，不能沿用上一次的确认状态。 */
  function reset() {
    input.value = ''
    force.value = false
  }
</script>

<style lang="scss" scoped>
  @use '../views/overview-tokens' as t;

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
      display: inline-flex;
      flex: none;
      gap: 4px;
      align-items: center;
      font-size: 13px;
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

    // 输入档的操作提示（原自建 prompt 的正文口径搬过来的那句）：弱化、随输入框。
    &__hint {
      margin-top: 6px;
      color: var(--el-text-color-secondary);
      font-size: 12px;
      line-height: 1.5;
    }

    // 输入档的就地错误句：与拉取对话框同一形态（红字、紧跟输入框）。
    &__error {
      margin-top: 6px;
      // 文字对比度 AA（收尾批）：原 el-color-danger（白底 3.27）改走 token，
      // 数字见 @styles/core/aa-text.scss。
      color: var(--aa-danger-text);
      font-size: 12px;
      line-height: 1.5;
    }
  }

  .ac-force {
    margin-top: 12px;
  }
</style>
