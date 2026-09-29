<template>
  <div class="yaml-mode">
    <!-- CodeMirror 宿主；高度由 CSS 给（源码编辑需要足够视野）。 -->
    <div ref="hostRef" class="yaml-mode__editor"></div>

    <!-- 语法校验结论：错误带位置（阻止切回表单的依据），通过时只在右上角给一句。 -->
    <ElAlert
      v-if="errorText"
      class="yaml-mode__alert"
      type="error"
      :title="errorText"
      :closable="false"
    />
    <ElAlert
      v-else-if="generated"
      class="yaml-mode__alert"
      type="info"
      :closable="false"
      title="当前内容由表单改动生成：直接保存仍按最小改动写入（未触碰段落原样保留）；手动编辑后再保存会整体重写文件，注释不保留。"
    />
    <div class="yaml-mode__status">
      {{ errorText ? '语法有误，修好后才能切回表单' : '语法校验通过' }}
    </div>
  </div>
</template>

<script setup lang="ts">
  /**
   * YML 模式（四期，spec §11.8）：CodeMirror 6 直编 + 语法校验。
   *
   * 校验器就是文档模型解析器（utils/compose.ts 的 parseComposeDoc）——「YAML 语法
   * 有效」与「能切回表单」共用同一个判定，不会出现「编辑器说通过、切表单又说报错」
   * 的两套口径。父组件据 update:valid 阻止无效文本切回表单。
   */
  import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
  import { ElAlert } from 'element-plus'
  import { basicSetup, EditorView } from 'codemirror'
  import { yaml } from '@codemirror/lang-yaml'
  import { parseComposeDoc } from '../../utils/compose'

  const props = withDefaults(
    defineProps<{
      modelValue: string
      /** 当前内容由表单改动序列化而来（会在编辑器下方如实标注保存路径差异）。 */
      generated?: boolean
    }>(),
    { generated: false }
  )

  const emit = defineEmits<{
    (e: 'update:modelValue', value: string): void
    (e: 'update:valid', value: boolean): void
    (e: 'update:error', value: string): void
  }>()

  const hostRef = ref<HTMLElement | null>(null)
  const errorText = ref('')
  let view: EditorView | null = null

  function validate(value: string): void {
    const parsed = parseComposeDoc(value)
    const ok = parsed.ok && !!parsed.doc
    if (ok) {
      errorText.value = ''
      emit('update:error', '')
    } else {
      const pos = parsed.line != null ? `第 ${parsed.line} 行第 ${parsed.column ?? 1} 列：` : ''
      errorText.value = `${pos}${parsed.error ?? '配置内容不是合法的 YAML'}`
      emit('update:error', errorText.value)
    }
    emit('update:valid', ok)
  }

  onMounted(() => {
    if (!hostRef.value) return
    view = new EditorView({
      doc: props.modelValue,
      extensions: [
        basicSetup,
        yaml(),
        EditorView.lineWrapping,
        EditorView.updateListener.of((update) => {
          if (update.docChanged) emit('update:modelValue', update.state.doc.toString())
        })
      ],
      parent: hostRef.value
    })
    validate(props.modelValue)
  })

  // 外部（切模式/保存后刷新基线）替换内容时同步进编辑器；相同内容不重放（避免光标跳动）。
  watch(
    () => props.modelValue,
    (value) => {
      if (view && value !== view.state.doc.toString()) {
        view.dispatch({
          changes: { from: 0, to: view.state.doc.length, insert: value }
        })
      }
      validate(value)
    }
  )

  onBeforeUnmount(() => {
    view?.destroy()
    view = null
  })
</script>

<style lang="scss" scoped>
  .yaml-mode {
    &__editor {
      height: 420px;
      overflow: hidden;
      border: 1px solid var(--el-border-color);
      border-radius: 6px;
      background: var(--default-box-color, #fff);

      // CodeMirror 默认高度 100%；宿主盒子高度独立于字体。
      :deep(.cm-editor) {
        height: 100%;
        font-size: 12px;
      }

      :deep(.cm-scroller) {
        font-family: var(--art-font-family-mono, monospace);
      }
    }

    &__alert {
      margin-top: 10px;
    }

    &__status {
      margin-top: 6px;
      font-size: 12px;
      color: var(--el-text-color-secondary);
      text-align: right;
    }
  }
</style>
