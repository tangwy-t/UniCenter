<!-- 调用目标 + 参数编辑：
     - 下拉数据源 GET /jobs/targets（显示名 + target id 副文本）
     - 目标带 paramSchema → JSON-Schema 驱动动态表单（string/enum/object:KV 行）
     - 目标带参数但无 schema → JSON 编辑区
     - 目标无参数 → 隐藏参数区
     - 任意带参数目标均可切换"以 JSON 直接编辑"高级模式（双向同步） -->
<template>
  <div class="job-target-params">
    <ElSelect
      :model-value="target"
      filterable
      class="target-select"
      placeholder="请选择调用目标"
      aria-label="调用目标"
      @update:model-value="onTargetChange"
    >
      <ElOption v-for="t in options" :key="t.value" :label="t.label" :value="t.value">
        <div class="target-option">
          <span>{{ t.label }}</span>
          <span class="target-sub font-mono">{{ t.value }}</span>
        </div>
      </ElOption>
    </ElSelect>

    <div v-if="unknownTarget" class="target-warning">
      <ArtSvgIcon icon="ri:error-warning-line" />
      当前目标 <span class="font-mono">{{ target }}</span> 不在注册表中，保存将被服务端拒绝
    </div>

    <!-- 参数区（有参数的目标） -->
    <div v-if="selectedInfo?.hasParams" class="params-box">
      <div class="params-head">
        <span class="params-title">调用参数</span>
        <ElCheckbox v-model="rawMode" size="small">以 JSON 直接编辑</ElCheckbox>
      </div>

      <!-- 高级：JSON 直接编辑 -->
      <template v-if="rawMode">
        <ElInput
          v-model="rawText"
          type="textarea"
          :rows="5"
          class="font-mono"
          placeholder='{"url":"https://example.com/hook"}'
          @blur="validateRawText"
        />
        <div v-if="rawError" class="params-error">
          <ArtSvgIcon icon="ri:error-warning-line" />
          {{ rawError }}
        </div>
      </template>

      <!-- 无 schema：等同 JSON 编辑（无表单模式） -->
      <template v-else-if="!schemaProps.length">
        <ElInput
          v-model="rawText"
          type="textarea"
          :rows="5"
          class="font-mono"
          placeholder="{}"
          @blur="validateRawText"
        />
        <div v-if="rawError" class="params-error">
          <ArtSvgIcon icon="ri:error-warning-line" />
          {{ rawError }}
        </div>
      </template>

      <!-- schema 驱动动态表单 -->
      <template v-else>
        <div v-for="def in schemaProps" :key="def.key" class="param-row">
          <div class="param-label">
            <span>{{ def.title || def.key }}</span>
            <span v-if="def.required" class="required">*</span>
          </div>
          <div class="param-control">
            <!-- enum → 下拉 -->
            <ElSelect
              v-if="def.enum?.length"
              :model-value="String(model[def.key] ?? def.default ?? '')"
              @update:model-value="(v) => setField(def.key, v)"
            >
              <ElOption v-for="e in def.enum" :key="e" :label="e" :value="e" />
            </ElSelect>
            <!-- string → 输入（multiline 标记时渲染为多行 textarea） -->
            <ElInput
              v-else-if="def.type === 'string'"
              :type="def.multiline ? 'textarea' : 'text'"
              :rows="def.multiline ? 4 : undefined"
              :model-value="model[def.key]"
              :placeholder="def.description || (isUrl(def.key) ? 'https://' : '')"
              @update:model-value="(v) => setField(def.key, v)"
            />
            <!-- object → KV 行编辑器 -->
            <div v-else-if="def.type === 'object'" class="kv-editor">
              <div v-for="(row, i) in kvRows(def.key)" :key="i" class="kv-row">
                <ElInput
                  :model-value="row.k"
                  placeholder="名称"
                  class="kv-key font-mono"
                  @update:model-value="(v) => setKvKey(def.key, i, v)"
                />
                <ElInput
                  :model-value="row.v"
                  placeholder="值"
                  class="kv-value font-mono"
                  @update:model-value="(v) => setKvValue(def.key, i, v)"
                />
                <div class="kv-remove" aria-label="删除该行" @click="removeKvRow(def.key, i)">
                  <ArtSvgIcon icon="ri:delete-bin-6-line" />
                </div>
              </div>
              <ArtButtonTable
                class="!mr-0"
                icon="ri:add-line"
                iconClass="bg-theme/12 text-theme"
                :title="`添加${def.title || '条目'}`"
                @click="addKvRow(def.key)"
              />
            </div>
            <!-- 未知类型兜底：JSON 文本 -->
            <ElInput
              v-else
              :model-value="stringifyField(model[def.key])"
              :placeholder="def.description || ''"
              class="font-mono"
              @update:model-value="(v) => setFieldRaw(def.key, v)"
            />
          </div>
        </div>
        <div v-if="paramsError" class="params-error">
          <ArtSvgIcon icon="ri:error-warning-line" />
          {{ paramsError }}
        </div>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
  import ArtSvgIcon from '@/components/core/base/art-svg-icon/index.vue'
  import { useJobTargets } from '../composables/useJobTargets'

  defineOptions({ name: 'JobTargetParams' })

  const props = defineProps<{ target: string; params: string }>()
  const emit = defineEmits<{
    (e: 'update:target', value: string): void
    (e: 'update:params', value: string): void
  }>()

  interface SchemaPropDef {
    key: string
    type?: string
    title?: string
    description?: string
    enum?: string[]
    default?: unknown
    required?: boolean
    multiline?: boolean
  }

  const { targets, ensure } = useJobTargets()
  const rawMode = ref(false)
  const rawText = ref('')
  const rawError = ref('')
  const paramsError = ref('')
  const model = ref<Record<string, any>>({})

  // KV 行编辑器（type: object 的字段）：以"行"为单位维护，包含尚未填写 key 的
  // 临时行。临时行无法放进 JSON（空 key 无意义），故与 model/params 分离存储，
  // 提交到 model 时只保留已填写 key 的行。否则 addKvRow 追加的 { k: '', v: '' }
  // 会被 writeKv 的空 key 过滤直接丢弃，页面上点击"添加"毫无反应。
  const kvRowsState = reactive<Record<string, { k: string; v: string }[]>>({})
  // 最近一次主动上抛的 params，用于识别"自己 emit 的回声"：
  // 回声不应重建 kv 行状态（否则会把刚添加、尚未填 key 的临时行清掉）。
  const lastEmittedParams = ref<string | null>(null)

  const selectedInfo = computed(() => targets.value.find((t) => t.target === props.target))
  const unknownTarget = computed(() => !!props.target && !selectedInfo.value)

  /** 下拉选项：注册目标 + 未注册回退项 */
  const options = computed(() => {
    const list = targets.value.map((t) => ({
      label: t.displayName,
      value: t.target,
      displayName: t.displayName
    }))
    if (unknownTarget.value) {
      list.push({ label: `${props.target}`, value: props.target, displayName: props.target })
    }
    return list.filter((o) => o && o.value)
  })

  /** schema 驱动的属性定义列表 */
  const schemaProps = computed<SchemaPropDef[]>(() => {
    const schema = selectedInfo.value?.paramSchema
    if (!schema || typeof schema !== 'object') return []
    const properties = schema.properties
    if (!properties || typeof properties !== 'object') return []
    const required = Array.isArray(schema.required) ? schema.required : []
    return Object.keys(properties).map((key) => {
      const def = (properties as Record<string, any>)[key] ?? {}
      return {
        key,
        type: def.type,
        title: def.title,
        description: def.description,
        enum: Array.isArray(def.enum) ? def.enum.map(String) : undefined,
        default: def.default,
        required: required.includes(key),
        multiline: def.multiline === true
      }
    })
  })

  /** 正在使用 JSON 文本编辑（高级模式，或无 schema 的降级形态） */
  const usesRawEditor = computed(() => rawMode.value || schemaProps.value.length === 0)

  function parseModel(json: string): Record<string, any> {
    if (!json?.trim()) return {}
    try {
      const parsed = JSON.parse(json)
      return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed : {}
    } catch {
      return {}
    }
  }

  /** 用 schema 默认值补全缺失字段（如 http-call 的 method=GET） */
  function withDefaults(obj: Record<string, any>): Record<string, any> {
    const out = { ...obj }
    for (const def of schemaProps.value) {
      if (out[def.key] === undefined && def.default !== undefined) out[def.key] = def.default
    }
    return out
  }

  function serialize(obj: Record<string, any>): string {
    // 过滤空字符串字段，保持存储整洁
    const cleaned = Object.fromEntries(
      Object.entries(obj).filter(([, v]) => v !== '' && v !== null && v !== undefined)
    )
    return Object.keys(cleaned).length ? JSON.stringify(cleaned) : ''
  }

  function ensureRawText() {
    try {
      const parsed = parseModel(props.params)
      rawText.value = props.params?.trim() ? JSON.stringify(parsed, null, 2) : ''
    } catch {
      rawText.value = props.params ?? ''
    }
  }

  /** 外部 params 变化 → 同步内部表单模型（编辑回填） */
  watch(
    () => props.params,
    (val) => {
      if (rawMode.value) {
        ensureRawText()
        return
      }
      // 外部回填（编辑/目标切换）才重建模型与 KV 行；自己上抛 params 触发的
      // 回声只更新模型，避免清掉正在编辑的临时 KV 行。
      const isEcho = val != null && val === lastEmittedParams.value
      model.value = withDefaults(parseModel(val ?? ''))
      if (!isEcho) syncKvFromModel()
      ensureRawText()
      paramsError.value = ''
    },
    { immediate: true }
  )

  /** target 变化 → 重置参数模型（新 schema 默认值 / 清空） */
  function onTargetChange(v: string) {
    const next = targets.value.find((t) => t.target === v)
    const schema = next?.paramSchema
    let obj: Record<string, any> = {}
    if (schema?.properties) {
      const required = Array.isArray(schema.required) ? schema.required : []
      obj = Object.fromEntries(
        Object.entries(schema.properties as Record<string, any>)
          .filter(([key, def]: [string, any]) =>
            def?.default !== undefined ? true : required.includes(key)
          )
          .map(([key, def]: [string, any]) => [
            key,
            def?.default ?? (def?.type === 'object' ? {} : '')
          ])
      )
    }
    model.value = obj
    syncKvFromModel()
    const nextParams = next?.hasParams ? serialize(obj) : ''
    emit('update:target', v)
    rawMode.value = false
    rawText.value = nextParams ? JSON.stringify(parseModel(nextParams), null, 2) : ''
    rawError.value = ''
    paramsError.value = ''
    emitParams(nextParams)
  }

  /** 表单字段更新 */
  function setField(key: string, v: unknown) {
    model.value = { ...model.value, [key]: v }
    emitParams(serialize(model.value))
  }

  function setFieldRaw(key: string, raw: string) {
    let parsed: unknown = raw
    try {
      parsed = JSON.parse(raw)
    } catch {
      /* 保留原字符串 */
    }
    setField(key, parsed)
  }

  function stringifyField(v: unknown): string {
    if (typeof v === 'string') return v
    try {
      return JSON.stringify(v)
    } catch {
      return ''
    }
  }

  function isUrl(key: string) {
    return /url/i.test(key)
  }

  /** KV 行编辑器（type: object 且值为对象）。返回的数组引用会被模板 v-for 消费 */
  function kvRows(key: string): Array<{ k: string; v: string }> {
    if (!kvRowsState[key]) kvRowsState[key] = []
    return kvRowsState[key]
  }

  /** 把已填写 key 的行提交到 model 并上抛参数（空 key 的临时行不进入 model） */
  function commitKv(key: string) {
    const obj: Record<string, string> = {}
    for (const r of kvRowsState[key] ?? []) {
      if (r.k.trim()) obj[r.k.trim()] = r.v
    }
    setField(key, obj)
  }

  /** model（外部回填 / 目标切换 / 默认值）→ 重建 object 字段的行状态 */
  function syncKvFromModel() {
    for (const def of schemaProps.value) {
      if (def.type !== 'object') continue
      const obj = model.value[def.key]
      kvRowsState[def.key] =
        obj && typeof obj === 'object' && !Array.isArray(obj)
          ? Object.entries(obj as Record<string, unknown>).map(([k, v]) => ({
              k,
              v: typeof v === 'string' ? v : stringifyField(v)
            }))
          : []
    }
  }

  function addKvRow(key: string) {
    kvRows(key).push({ k: '', v: '' })
  }

  function removeKvRow(key: string, index: number) {
    kvRows(key).splice(index, 1)
    commitKv(key)
  }

  function setKvKey(key: string, index: number, v: string) {
    const row = kvRows(key)[index]
    if (row) kvRowsState[key][index] = { ...row, k: v }
    commitKv(key)
  }

  function setKvValue(key: string, index: number, v: string) {
    const row = kvRows(key)[index]
    if (row) kvRowsState[key][index] = { ...row, v }
    commitKv(key)
  }

  /** 高级 JSON 模式：解析合法后上抛 */
  function syncRaw() {
    if (!usesRawEditor.value) return
    try {
      const parsed = rawText.value.trim() ? JSON.parse(rawText.value) : {}
      if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
        model.value = parsed
        rawError.value = ''
        emitParams(rawText.value.trim())
      } else {
        rawError.value = '参数必须是 JSON 对象'
      }
    } catch (e) {
      rawError.value = '参数 JSON 格式错误：' + (e as Error).message
    }
  }

  function validateRawText() {
    if (!usesRawEditor.value) return
    try {
      if (rawText.value.trim()) JSON.parse(rawText.value)
      rawError.value = ''
    } catch (e) {
      rawError.value = '参数 JSON 格式错误：' + (e as Error).message
    }
  }

  watch(rawText, () => {
    if (usesRawEditor.value) syncRaw()
  })

  function emitParams(v: string) {
    if (v !== props.params) {
      lastEmittedParams.value = v
      emit('update:params', v)
    }
  }

  /** 供表单规则调用 */
  function validate(): { valid: boolean; message: string } {
    if (unknownTarget.value) return { valid: false, message: `调用目标 "${props.target}" 未注册` }
    if (!selectedInfo.value?.hasParams) return { valid: true, message: '' }
    if (schemaProps.value.length === 0) {
      // 纯 JSON 模式
      try {
        if (props.params.trim()) JSON.parse(props.params)
        return { valid: true, message: '' }
      } catch (e) {
        return { valid: false, message: '调用参数 JSON 格式错误：' + (e as Error).message }
      }
    }
    if (rawMode.value) {
      const r = validateRawTextSilent()
      return r.valid ? r : { valid: false, message: rawError.value || '参数 JSON 格式错误' }
    }
    for (const def of schemaProps.value) {
      if (!def.required) continue
      const v = model.value[def.key]
      if (
        v === undefined ||
        v === null ||
        v === '' ||
        (typeof v === 'object' && Object.keys(v).length === 0)
      ) {
        return { valid: false, message: `请填写「${def.title || def.key}」` }
      }
    }
    return { valid: true, message: '' }
  }

  function validateRawTextSilent(): { valid: boolean; message: string } {
    try {
      if (props.params.trim()) JSON.parse(props.params)
      return { valid: true, message: '' }
    } catch (e) {
      return { valid: false, message: '调用参数 JSON 格式错误：' + (e as Error).message }
    }
  }

  onMounted(() => {
    ensure()
  })

  defineExpose({ validate })
</script>

<style scoped>
  .job-target-params {
    width: 100%;
  }

  .target-select {
    width: 100%;
  }

  .target-option {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;

    .target-sub {
      font-size: 12px;
      color: var(--color-g-500, #949eb7);
    }
  }

  .target-warning {
    display: flex;
    align-items: center;
    gap: 6px;
    margin-top: 6px;
    font-size: 12px;
    color: var(--color-warning, #d97706);
  }

  .params-box {
    margin-top: 10px;
    padding: 12px;
    border: 1px dashed var(--default-border-dashed, #dbdfe9);
    border-radius: 8px;
    background: var(--default-bg-color, #fafbfc);
  }

  .dark .params-box {
    background: var(--art-gray-200, #17171c);
  }

  .params-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 10px;
  }

  .params-title {
    font-size: 13px;
    font-weight: 600;
    color: var(--color-g-700, #4d5875);
  }

  .param-row {
    display: flex;
    align-items: flex-start;
    gap: 12px;
    margin-bottom: 10px;
  }

  .param-label {
    flex: 0 0 90px;
    padding-top: 4px;
    font-size: 13px;
    color: var(--color-g-600, #7987a1);
    text-align: right;
  }

  .required {
    color: var(--color-danger, #dc2626);
    margin-left: 2px;
  }

  .param-control {
    flex: 1;
    min-width: 0;
  }

  .params-error {
    display: flex;
    align-items: center;
    gap: 6px;
    margin-top: 6px;
    font-size: 12px;
    color: var(--color-danger, #dc2626);
  }

  .kv-editor {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .kv-row {
    display: flex;
    align-items: center;
    gap: 6px;
  }

  .kv-key {
    width: 34%;
  }

  .kv-value {
    flex: 1;
  }

  .kv-remove {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 28px;
    height: 28px;
    flex: none;
    border-radius: 6px;
    color: var(--color-g-500, #949eb7);
    cursor: pointer;
    transition:
      background-color 150ms ease,
      color 150ms ease;
  }

  .kv-remove:hover {
    background: color-mix(in srgb, var(--color-danger) 12%, transparent);
    color: var(--color-danger, #dc2626);
  }

  @media (prefers-reduced-motion: reduce) {
    .kv-remove {
      transition: none;
    }
  }
</style>
