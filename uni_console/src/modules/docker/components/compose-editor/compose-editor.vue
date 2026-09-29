<template>
  <div class="compose-editor">
    <ElDialog
      v-model="visible"
      :title="`配置编辑 · ${project}`"
      width="min(900px, 94vw)"
      top="6vh"
      :close-on-click-modal="false"
      @closed="reset"
    >
      <div v-loading="loading" class="compose-editor__body">
        <!-- 载入失败（设备离线/路径未知/文件被删）：只给服务端/agent 的结论句 + 重试。 -->
        <ElAlert v-if="loadError" type="error" :title="loadError" :closable="false">
          <div class="compose-editor__retry">
            <ElButton size="small" @click="load">重新载入</ElButton>
          </div>
        </ElAlert>

        <template v-else>
          <div class="compose-editor__bar">
            <span class="compose-editor__file">{{ fileText }}</span>
            <ElRadioGroup size="small" :model-value="mode" @update:model-value="onModeChange">
              <ElRadioButton value="form">表单</ElRadioButton>
              <ElRadioButton value="yaml">YML</ElRadioButton>
            </ElRadioGroup>
          </div>

          <!-- 保存/回滚失败：服务端结论句优先（含「已被他人修改，请刷新」）。 -->
          <ElAlert
            v-if="saveError"
            class="compose-editor__save-error"
            type="warning"
            :title="saveError"
            :closable="false"
          >
            <div class="compose-editor__retry">
              <ElButton size="small" @click="load">刷新配置</ElButton>
            </div>
          </ElAlert>

          <FormMode v-if="mode === 'form'" :doc="model" :baseline="baseline" :auto-add="autoAdd" />
          <YamlMode
            v-else
            v-model="yamlText"
            :generated="!yamlEdited && formDirty"
            @update:valid="yamlValid = $event"
            @update:error="yamlError = $event"
          />

          <p class="compose-editor__note">
            保存与应用是两个动作：保存只写配置文件（先校验、再备份、后写入）；点「应用 up
            -d」才会按新配置重建容器。
          </p>
        </template>
      </div>

      <template #footer>
        <div class="compose-editor__footer">
          <BackupHistory
            :backups="backups"
            :current-hash="baseHash"
            :current-content="originalContent"
            :disabled="busy || loading"
            :can-rollback="canConfig"
            :rolling-back="busy"
            @rollback="onRollback"
          />
          <span class="compose-editor__spacer"></span>
          <ElButton :disabled="busy" @click="visible = false">取消</ElButton>
          <ElButton
            v-if="canConfig"
            type="primary"
            :disabled="!dirty || busy || loading || !!loadError"
            :loading="saving"
            @click="onSaveClick"
          >
            保存（校验+备份）
          </ElButton>
          <ElButton
            v-if="canManage"
            type="warning"
            plain
            :disabled="dirty || busy || loading || !!loadError || applyBlocked"
            :title="applyDisabledReason"
            @click="onApplyClick"
          >
            应用 up -d…
          </ElButton>
        </div>
      </template>
    </ElDialog>

    <!-- diff 预览（保存收尾的第 b 步；写全文时给逐行差异，走最小改动时给变更明细）。 -->
    <ElDialog v-model="preview.visible" title="保存预览" width="min(760px, 94vw)">
      <div class="compose-editor__preview">
        <p class="compose-editor__summary">{{ preview.summary }}</p>
        <ul v-if="preview.details.length > 0" class="compose-editor__details">
          <li v-for="line in preview.details" :key="line">{{ line }}</li>
        </ul>
        <div v-if="preview.lines.length > 0" class="compose-editor__diff">
          <div
            v-for="(line, i) in preview.lines"
            :key="i"
            class="compose-editor__diff-line"
            :class="`is-${line.kind}`"
          >
            {{ line.kind === 'add' ? '+ ' : line.kind === 'del' ? '- ' : '  ' }}{{ line.text }}
          </div>
          <div v-if="preview.truncated" class="compose-editor__more">
            差异较多，只显示前 {{ PREVIEW_MAX_LINES }} 行
          </div>
        </div>
        <p class="compose-editor__note">
          改动的写入与运行态无关：保存完成后，需要再说一次「应用」才会重建容器。
        </p>
      </div>
      <template #footer>
        <ElButton @click="preview.visible = false">取消</ElButton>
        <ElButton type="primary" @click="onPreviewContinue">继续保存…</ElButton>
      </template>
    </ElDialog>

    <!-- 强确认（照抄项目名）：保存/回滚/应用各自独立走它。保护档只对「应用」有
         语义（agent 的保护判定在 up 前做配置 diff）；保存/回滚是文件级动作，不带保护门控。 -->
    <DockerActionConfirm
      v-model="confirm.visible"
      :action="confirm.action"
      :target="project"
      :override="confirm.override"
      :target-protected="confirm.kind === 'apply' && projectProtected"
      target-kind="项目"
      :loading="confirmLoading"
      @confirm="onConfirmed"
    >
      <ElCheckbox
        v-if="confirm.kind === 'apply'"
        v-model="applyRemoveOrphans"
        class="compose-editor__opt"
      >
        回收孤儿容器
      </ElCheckbox>
    </DockerActionConfirm>
  </div>
</template>

<script setup lang="ts">
  /**
   * 四期配置编辑器容器（spec §8 四步闭环）：
   *   载入（compose.file:read）→ 编辑（表单/ YML 双模式共享同一文档模型）
   *   → 保存（compose.file:validate 预检 → diff 预览 → 强确认 → patch/write）
   *   → 应用（compose:up，独立按钮独立强确认）——保存与应用永远是两个动作。
   *
   * 两条保存路径（§9）：表单可表达的修改永远走 `compose.file:patch`（只发被改过的
   * 键，未触碰段落含注释逐字节保留）；只有 YML 模式里**手改过**的文本才发全文
   * write。YML 里由表单改动带出的文本未再手改时，仍走 patch。
   *
   * 三条防注释丢失：① 切回表单前数注释并确认（countYamlComments）；②③ 在
   * form-mode 的卡片标注。写成功后以 agent 回读内容为准刷新基线；回读正文被省略
   * （超 256KB）时重新 read 一次。
   */
  import { computed, ref, watch } from 'vue'
  import {
    ElAlert,
    ElButton,
    ElCheckbox,
    ElDialog,
    ElMessage,
    ElMessageBox,
    ElRadioButton,
    ElRadioGroup
  } from 'element-plus'
  import { useAuth } from '@/hooks/core/useAuth'
  import { PermDockerConfig, PermDockerExec, PermDockerManage } from '@/enums/permission'
  import BackupHistory from './backup-history.vue'
  import FormMode from './form-mode.vue'
  import YamlMode from './yaml-mode.vue'
  import DockerActionConfirm from '../action-confirm.vue'
  import { runErrorMessage, useDockerCmds } from '../../composables/useDockerCmds'
  import { protectedGate } from '../../utils/actions'
  import type { ConfirmOverride } from '../../utils/confirm'
  import {
    cloneComposeDoc,
    buildComposePatch,
    COMPOSE_ACTIONS,
    composeModelIssues,
    countYamlComments,
    diffDetails,
    diffLines,
    diffSummary,
    emptyComposeDoc,
    formatBackupTime,
    parseComposeDoc,
    parseComposeFilePayload,
    serializeComposeDoc,
    type ComposeBackup,
    type ComposeDoc,
    type ComposeFileView,
    type ComposePatch,
    type DiffLine
  } from '../../utils/compose'

  const props = withDefaults(
    defineProps<{
      modelValue: boolean
      /** 项目名（同时也是强确认的期望值：协议要求照抄项目名）。 */
      project: string
      /** 项目是否受保护（apply 的保护档门控）。 */
      projectProtected?: boolean
      /** 从项目页「＋添加服务」打开：表单模式直接弹出模板选择。 */
      autoAdd?: boolean
    }>(),
    { projectProtected: false, autoAdd: false }
  )

  const emit = defineEmits<{
    (e: 'update:modelValue', value: boolean): void
    /** 保存或回滚成功（页面据此重拉快照与备份列表）。 */
    (e: 'saved'): void
    /** 应用（up -d）成功。 */
    (e: 'applied'): void
  }>()

  const { hasAuth } = useAuth()
  const canConfig = computed(() => hasAuth(PermDockerConfig))
  const canManage = computed(() => hasAuth(PermDockerManage))
  const canExec = computed(() => hasAuth(PermDockerExec))

  const { run, busy } = useDockerCmds()

  const visible = computed({
    get: () => props.modelValue,
    set: (value: boolean) => emit('update:modelValue', value)
  })

  const loading = ref(false)
  const loadError = ref('')
  const saveError = ref('')
  const saving = ref(false)
  const confirmLoading = ref(false)

  const originalContent = ref('')
  const baseHash = ref('')
  const filePath = ref('')
  const backups = ref<ComposeBackup[]>([])
  const baseline = ref<ComposeDoc | null>(null)
  const model = ref<ComposeDoc>(emptyComposeDoc())

  const mode = ref<'form' | 'yaml'>('form')
  const yamlText = ref('')
  const yamlGenerated = ref('')
  const yamlValid = ref(true)
  const yamlError = ref('')

  /** 未保存改动：表单模型 vs 基线（patch 非空）；YML 模式还要算上手改的文本。 */
  const patch = computed<ComposePatch | null>(() =>
    baseline.value ? buildComposePatch(baseline.value, model.value) : null
  )
  const formDirty = computed(() => patch.value !== null)
  const yamlEdited = computed(() => mode.value === 'yaml' && yamlText.value !== yamlGenerated.value)
  const dirty = computed(() =>
    mode.value === 'yaml' ? yamlEdited.value || formDirty.value : formDirty.value
  )

  const fileText = computed(() => {
    if (!filePath.value) return '配置文件（路径由主机上的项目记录给出）'
    const parts = filePath.value.split('/')
    return parts[parts.length - 1] || filePath.value
  })

  const applyGate = computed(() =>
    protectedGate({ protected: props.projectProtected }, canExec.value)
  )
  const applyBlocked = computed(() => !applyGate.value.allowed)
  const applyDisabledReason = computed(() => {
    if (dirty.value) return '有未保存的改动，先保存再应用'
    if (applyBlocked.value) return applyGate.value.conclusion
    return ''
  })

  // ── 载入与基线刷新 ──

  async function load(): Promise<void> {
    loading.value = true
    loadError.value = ''
    saveError.value = ''
    const res = await run({
      action: COMPOSE_ACTIONS.read,
      target: props.project,
      key: `compose-editor-read:${props.project}`
    })
    loading.value = false
    if (!res.ok) {
      loadError.value = runErrorMessage(res, '读取配置文件失败')
      return
    }
    applyView(parseComposeFilePayload(res.payload))
  }

  /** 用一份读结果重置编辑器（打开时、保存/回滚成功后、重读后都走这条）。 */
  function applyView(view: ComposeFileView): void {
    originalContent.value = view.content
    baseHash.value = view.hash
    filePath.value = view.path
    backups.value = view.backups
    const parsed = parseComposeDoc(view.content)
    if (parsed.ok && parsed.doc) {
      baseline.value = parsed.doc
      model.value = cloneComposeDoc(parsed.doc)
      mode.value = 'form'
      yamlText.value = ''
      yamlGenerated.value = ''
      yamlValid.value = true
      yamlError.value = ''
      return
    }
    // 文件当前不是合法 YAML（或最外层不是映射）：直接进 YML 模式修复。
    baseline.value = null
    model.value = emptyComposeDoc()
    mode.value = 'yaml'
    yamlText.value = view.content
    yamlGenerated.value = view.content
    yamlValid.value = false
    yamlError.value = parsed.error ?? '配置内容不是合法的 YAML'
  }

  /**
   * 写成功后的基线刷新：写路径的载荷是 agent 回读后的完整内容（唯一事实源）；
   * 正文超过 256KB 时载荷会省略 content —— 此时重新 read 一次拿正文。
   */
  async function refreshAfterWrite(payload: unknown): Promise<boolean> {
    const view = parseComposeFilePayload(payload)
    if (view.content !== '') {
      applyView(view)
      return true
    }
    baseHash.value = view.hash || baseHash.value
    if (view.backups.length > 0) backups.value = view.backups
    const res = await run({
      action: COMPOSE_ACTIONS.read,
      target: props.project,
      key: `compose-editor-reread:${props.project}`
    })
    if (res.ok) {
      applyView(parseComposeFilePayload(res.payload))
      return true
    }
    saveError.value = '保存已完成，但重新载入配置失败；请关闭后重新打开编辑器核对内容'
    return false
  }

  function reset(): void {
    loading.value = false
    loadError.value = ''
    saveError.value = ''
    saving.value = false
    confirmLoading.value = false
    originalContent.value = ''
    baseHash.value = ''
    filePath.value = ''
    backups.value = []
    baseline.value = null
    model.value = emptyComposeDoc()
    mode.value = 'form'
    yamlText.value = ''
    yamlGenerated.value = ''
    yamlValid.value = true
    yamlError.value = ''
    preview.value = {
      visible: false,
      write: false,
      summary: '',
      details: [],
      lines: [],
      truncated: false
    }
    confirm.value = {
      visible: false,
      action: '',
      kind: 'save',
      override: undefined,
      write: undefined
    }
    applyRemoveOrphans.value = false
  }

  watch(
    () => props.modelValue,
    (open) => {
      if (open) void load()
    }
  )

  // ── 模式切换（未保存修改跨模式保留；语法无效阻止切回表单；注释丢失先确认）──

  function onModeChange(target: string | number | boolean | undefined): void {
    const next = target === 'yaml' ? 'yaml' : 'form'
    if (next === mode.value) return
    if (next === 'yaml') {
      switchToYaml()
      return
    }
    void switchToForm()
  }

  function switchToYaml(): void {
    // 表单没有改动时直接用原文本（保留注释）；有改动才用序列化结果带出修改。
    const text = formDirty.value ? serializeComposeDoc(model.value) : originalContent.value
    yamlText.value = text
    yamlGenerated.value = text
    mode.value = 'yaml'
  }

  async function switchToForm(): Promise<void> {
    if (!baseline.value) {
      ElMessage.warning('当前配置无法按表单编辑，请在 YML 模式修改')
      return
    }
    const parsed = parseComposeDoc(yamlText.value)
    if (!parsed.ok || !parsed.doc) {
      ElMessage.warning(yamlError.value || parsed.error || '语法有误，修好后才能切回表单')
      return
    }
    if (yamlText.value !== yamlGenerated.value) {
      // 防线①：手改过的文本切回表单会在模型里丢掉注释，先如实告知再执行。
      const comments = countYamlComments(yamlText.value)
      if (comments > 0) {
        try {
          await ElMessageBox.confirm(
            `切回表单将丢失 ${comments} 处注释，要保留请在 YML 模式保存。`,
            '切换编辑模式',
            {
              confirmButtonText: '仍要切回',
              cancelButtonText: '留在 YML 模式',
              type: 'warning'
            }
          )
        } catch {
          return
        }
      }
      model.value = parsed.doc
    }
    mode.value = 'form'
  }

  // ── 保存：validate 预检 → diff 预览 → 强确认 → patch/write ──

  async function onSaveClick(): Promise<void> {
    saveError.value = ''
    const useWrite = mode.value === 'yaml' && yamlEdited.value
    if (mode.value === 'yaml' && !yamlValid.value) {
      ElMessage.warning(yamlError.value || '语法有误，请先修好再保存')
      return
    }
    if (!useWrite) {
      if (!patch.value) {
        ElMessage.info('没有需要保存的改动')
        return
      }
      const issues = composeModelIssues(model.value)
      if (issues.length > 0) {
        ElMessage.warning(issues[0])
        return
      }
    } else if (yamlText.value.trim() === '') {
      ElMessage.warning('配置内容为空，没有可保存的内容')
      return
    }

    // ① 权威预检：agent 用同目录临时文件跑 `config -q`，失败时透传 compose 的
    //    stderr 首行；预检不过就不进入确认环节（文件不会被改动）。
    const contentToValidate = useWrite ? yamlText.value : serializeComposeDoc(model.value)
    saving.value = true
    const res = await run({
      action: COMPOSE_ACTIONS.validate,
      target: props.project,
      options: { content: contentToValidate },
      key: `compose-editor-validate:${props.project}`
    })
    saving.value = false
    if (!res.ok) {
      saveError.value = runErrorMessage(res, '配置校验未通过，未保存')
      return
    }

    // ② diff 预览。
    preview.value = useWrite
      ? previewOfWrite()
      : {
          visible: true,
          write: false,
          summary: diffSummary(baseline.value as ComposeDoc, model.value),
          details: diffDetails(baseline.value as ComposeDoc, model.value),
          lines: [],
          truncated: false
        }
  }

  const PREVIEW_MAX_LINES = 300

  function previewOfWrite(): PreviewState {
    const all = diffLines(originalContent.value, yamlText.value)
    const lines = all.slice(0, PREVIEW_MAX_LINES)
    let added = 0
    let removed = 0
    for (const line of all) {
      if (line.kind === 'add') added++
      else if (line.kind === 'del') removed++
    }
    return {
      visible: true,
      write: true,
      summary: `将整体重写配置：新增 ${added} 行、删除 ${removed} 行`,
      details: [],
      lines,
      truncated: all.length > lines.length
    }
  }

  /** 预览确认 → 进入强确认（照抄项目名的独立弹窗）。 */
  function onPreviewContinue(): void {
    const useWrite = preview.value.write
    preview.value.visible = false
    confirm.value = {
      visible: true,
      action: useWrite ? COMPOSE_ACTIONS.write : COMPOSE_ACTIONS.patch,
      kind: 'save',
      override: saveConfirmOverride(useWrite),
      write: useWrite ? { content: yamlText.value } : { patch: patch.value }
    }
  }

  function saveConfirmOverride(useWrite: boolean): ConfirmOverride {
    return {
      kind: 'target-word',
      label: useWrite ? '重写配置' : '保存配置',
      danger: 'danger',
      conclusion: '原文件会先备份，再写入新内容；保存不会重启任何容器。',
      expected: props.project
    }
  }

  // ── 回滚与应用（各自独立的强确认）──

  function onRollback(backup: ComposeBackup): void {
    confirm.value = {
      visible: true,
      action: COMPOSE_ACTIONS.write,
      kind: 'rollback',
      override: {
        kind: 'target-word',
        label: '回滚配置',
        danger: 'danger',
        conclusion: `将把配置回滚到 ${formatBackupTime(backup.at)} 的版本；当前内容会先备份。`,
        expected: props.project
      },
      write: { backup: backup.token }
    }
  }

  function onApplyClick(): void {
    if (dirty.value) {
      ElMessage.warning('有未保存的改动，先保存再应用')
      return
    }
    applyRemoveOrphans.value = false
    confirm.value = { visible: true, action: 'compose:up', kind: 'apply' }
  }

  async function onConfirmed(payload: { confirm: string; force: boolean }): Promise<void> {
    const st = confirm.value
    confirmLoading.value = true
    try {
      const options: Record<string, unknown> = {}
      if (st.kind === 'apply') {
        if (applyRemoveOrphans.value) options.removeOrphans = true
      } else {
        options.baseHash = baseHash.value
        if (st.write?.content !== undefined) options.content = st.write.content
        if (st.write?.patch) options.patch = st.write.patch
        if (st.write?.backup !== undefined) options.backup = st.write.backup
      }
      if (payload.force) options.force = true
      const res = await run({
        action: st.action,
        target: props.project,
        options,
        confirm: payload.confirm,
        key: `compose-editor-${st.kind}:${props.project}`
      })
      if (!res.ok) {
        // 失败原因用服务端/agent 的结论句（乐观锁失败 =「文件已被他人修改，请刷新」；
        // 校验失败 = compose 的 stderr 首行），不再包一层「操作失败」。
        saveError.value = runErrorMessage(res, st.kind === 'apply' ? '应用未完成' : '保存未完成')
        ElMessage.error(saveError.value)
        return
      }
      if (st.kind === 'apply') {
        ElMessage.success(res.detail || '应用完成，容器已按配置重建')
        emit('applied')
        return
      }
      const isRollback = st.kind === 'rollback'
      const ok = await refreshAfterWrite(res.payload)
      if (ok) {
        ElMessage.success(isRollback ? '已回滚到所选备份' : '配置已保存（应用后才会生效）')
        emit('saved')
      }
    } finally {
      confirmLoading.value = false
      confirm.value = {
        visible: false,
        action: '',
        kind: 'save',
        override: undefined,
        write: undefined
      }
      applyRemoveOrphans.value = false
    }
  }

  interface PreviewState {
    visible: boolean
    write: boolean
    summary: string
    details: string[]
    lines: DiffLine[]
    truncated: boolean
  }

  interface SavePayload {
    content?: string
    patch?: ComposePatch | null
    backup?: string
  }

  interface ConfirmState {
    visible: boolean
    action: string
    kind: 'save' | 'rollback' | 'apply'
    override?: ConfirmOverride
    write?: SavePayload
  }

  const preview = ref<PreviewState>({
    visible: false,
    write: false,
    summary: '',
    details: [],
    lines: [],
    truncated: false
  })
  const confirm = ref<ConfirmState>({
    visible: false,
    action: '',
    kind: 'save',
    override: undefined,
    write: undefined
  })
  const applyRemoveOrphans = ref(false)
</script>

<style lang="scss" scoped>
  @use '@styles/core/breakpoints.scss' as *;

  .compose-editor {
    &__body {
      min-height: 200px;
    }

    &__bar {
      display: flex;
      align-items: center;
      justify-content: space-between;
      margin-bottom: 10px;
    }

    &__file {
      color: var(--el-text-color-secondary);
      font-family: var(--art-font-family-mono, monospace);
      font-size: 12px;
    }

    &__save-error {
      margin-bottom: 10px;
    }

    &__retry {
      margin-top: 6px;
    }

    &__note {
      margin: 12px 0 0;
      color: var(--el-text-color-secondary);
      font-size: 12px;
      line-height: 1.6;
    }

    &__footer {
      display: flex;
      align-items: center;
    }

    &__spacer {
      flex: 1 1 auto;
    }

    &__opt {
      display: flex;
      margin-top: 10px;
    }

    &__preview {
      max-height: 60vh;
      overflow: auto;
    }

    &__summary {
      margin: 0 0 10px;
      font-size: 14px;
      font-weight: 600;
    }

    &__details {
      margin: 0;
      padding-left: 18px;
      font-size: 13px;
      line-height: 1.9;
    }

    &__diff {
      margin-top: 8px;
      border: 1px solid var(--el-border-color-lighter);
      border-radius: 6px;
      font-family: var(--art-font-family-mono, monospace);
      font-size: 12px;
      line-height: 1.7;
    }

    &__diff-line {
      padding: 0 8px;
      white-space: pre-wrap;
      word-break: break-all;

      &.is-add {
        background: var(--el-color-success-light-9);
        color: var(--el-color-success);
      }

      &.is-del {
        background: var(--el-color-danger-light-9);
        color: var(--el-color-danger);
      }
    }

    &__more {
      padding: 4px 8px;
      color: var(--el-text-color-secondary);
    }
  }

  /* ── 响应式 ─────────────────────────────────────── */

  // 手机横屏（<768）：文件行与模式切换分成上下两行；页脚改成堆叠 ——
  // 「历史备份」独占一行，保存/应用等动作留在第二行右对齐，不再互相挤压。
  @include respond-below('tablet') {
    .compose-editor__bar {
      flex-wrap: wrap;
      gap: 8px;
    }

    .compose-editor__file {
      flex: 1 1 100%;
    }

    .compose-editor__footer {
      flex-wrap: wrap;
      justify-content: flex-end;
      gap: 8px;
    }

    .compose-editor__spacer {
      flex: 1 1 100%;
    }
  }
</style>
