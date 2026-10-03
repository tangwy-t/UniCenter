<template>
  <div class="form-mode">
    <!-- ── 服务分节：每张折叠卡 = 一个网元（spec §11.7） ────────────────── -->
    <div class="form-mode__head">
      <span class="form-mode__title">服务（{{ doc.services.keys.length }}）</span>
      <ElButton size="small" type="primary" plain @click="openTemplateDialog()"
        >＋添加服务</ElButton
      >
    </div>

    <ElCollapse v-if="doc.services.keys.length > 0" v-model="activeNames" class="fm-cards">
      <ElCollapseItem v-for="name in doc.services.keys" :key="name" :name="name">
        <template #title>
          <span class="fm-svc__name">{{ name }}</span>
          <!-- 三条防注释丢失的②③：两种标注只讲结论（spec §8） -->
          <ElTag v-if="isModified(name)" size="small" type="warning" effect="plain">
            已修改 · 保存将重写此块，块内注释不保留
          </ElTag>
          <ElTag v-else size="small" type="info" effect="plain">未修改，保存时原样保留</ElTag>
        </template>

        <div class="fm-grid">
          <label class="fm-field">
            <span class="fm-field__label">镜像</span>
            <ElInput
              size="small"
              placeholder="例如 nginx:1.27"
              :model-value="svc(imageOf(name))"
              @update:model-value="setField(name, 'image', $event)"
            />
          </label>
          <label class="fm-field">
            <span class="fm-field__label">容器名</span>
            <ElInput
              size="small"
              placeholder="默认由 compose 生成"
              :model-value="svc(serviceOf(name).container_name)"
              @update:model-value="setField(name, 'container_name', $event)"
            />
          </label>
          <label class="fm-field">
            <span class="fm-field__label">重启策略</span>
            <ElSelect
              size="small"
              placeholder="未设置"
              clearable
              filterable
              allow-create
              :model-value="serviceOf(name).restart ?? ''"
              @update:model-value="setField(name, 'restart', $event)"
            >
              <ElOption v-for="r in RESTART_OPTIONS" :key="r" :label="r" :value="r" />
            </ElSelect>
          </label>
          <label class="fm-field">
            <span class="fm-field__label">CPU 上限</span>
            <ElInput
              size="small"
              placeholder="例如 1.0"
              :model-value="svc(serviceOf(name).cpus)"
              @update:model-value="setField(name, 'cpus', $event)"
            />
          </label>
          <label class="fm-field">
            <span class="fm-field__label">内存上限</span>
            <ElInput
              size="small"
              placeholder="例如 512m"
              :model-value="svc(serviceOf(name).mem_limit)"
              @update:model-value="setField(name, 'mem_limit', $event)"
            />
          </label>
        </div>

        <div class="fm-field">
          <span class="fm-field__label">端口</span>
          <div class="fm-list">
            <div v-for="(p, i) in serviceOf(name).ports ?? []" :key="`p${i}`" class="fm-list__row">
              <ElInput
                size="small"
                placeholder="主机端口:容器端口"
                :model-value="p"
                @update:model-value="setListItem(name, 'ports', i, $event)"
              />
              <ElButton size="small" text type="danger" @click="removeListItem(name, 'ports', i)">
                删除
              </ElButton>
            </div>
            <ElButton size="small" text @click="addListItem(name, 'ports')">＋ 添加端口</ElButton>
          </div>
        </div>

        <div class="fm-field">
          <span class="fm-field__label">卷</span>
          <div class="fm-list">
            <div
              v-for="(v, i) in serviceOf(name).volumes ?? []"
              :key="`v${i}`"
              class="fm-list__row"
            >
              <ElInput
                size="small"
                placeholder="卷名:容器内路径"
                :model-value="v"
                @update:model-value="setListItem(name, 'volumes', i, $event)"
              />
              <ElButton size="small" text type="danger" @click="removeListItem(name, 'volumes', i)">
                删除
              </ElButton>
            </div>
            <ElButton size="small" text @click="addListItem(name, 'volumes')">＋ 添加卷</ElButton>
          </div>
        </div>

        <div class="fm-field">
          <span class="fm-field__label">环境变量</span>
          <div class="fm-list">
            <div v-for="[key, val] in envEntries(name)" :key="`e${key}`" class="fm-list__row">
              <ElInput
                size="small"
                class="fm-list__key"
                placeholder="变量名"
                :model-value="key"
                @update:model-value="setEnvKey(name, key, $event)"
              />
              <ElInput
                size="small"
                placeholder="值"
                :model-value="String(val ?? '')"
                @update:model-value="setEnvValue(name, key, $event)"
              />
              <ElButton size="small" text type="danger" @click="removeEnv(name, key)"
                >删除</ElButton
              >
            </div>
            <ElButton size="small" text @click="addEnv(name)">＋ 添加变量</ElButton>
          </div>
        </div>

        <div class="fm-grid">
          <label class="fm-field">
            <span class="fm-field__label">依赖</span>
            <ElSelect
              size="small"
              multiple
              placeholder="未设置"
              :model-value="serviceOf(name).depends_on ?? []"
              @update:model-value="setField(name, 'depends_on', $event)"
            >
              <ElOption v-for="dep in otherServices(name)" :key="dep" :label="dep" :value="dep" />
            </ElSelect>
          </label>
          <label class="fm-field">
            <span class="fm-field__label">网络</span>
            <ElSelect
              size="small"
              multiple
              filterable
              allow-create
              placeholder="未设置"
              :model-value="serviceOf(name).networks ?? []"
              @update:model-value="setField(name, 'networks', $event)"
            >
              <ElOption v-for="net in doc.networks.keys" :key="net" :label="net" :value="net" />
            </ElSelect>
          </label>
        </div>

        <div class="fm-field">
          <span class="fm-field__label">命令</span>
          <ElInput
            size="small"
            type="textarea"
            :rows="2"
            placeholder="一行就是一条启动命令；多条时每行一条"
            :model-value="commandText(name)"
            @update:model-value="setCommand(name, $event)"
          />
        </div>

        <!-- 未建模字段只读展示：表单不认就不碰它，保存时原样透传（spec §9 取舍 2） -->
        <div v-if="rawKeys(name).length > 0" class="fm-raw">
          <div class="fm-raw__title">高级字段（只读）</div>
          <div v-for="key in rawKeys(name)" :key="key" class="fm-raw__row">
            <span class="fm-raw__key">{{ key }}</span>
            <span class="fm-raw__value">{{ rawPreview(name, key) }}</span>
          </div>
          <div class="fm-raw__hint">
            表单不编辑这些字段；保存时原样保留。如需编辑请切 YML 模式。
          </div>
        </div>

        <div class="fm-svc__actions">
          <ElButton size="small" type="danger" plain @click="removeService(name)">
            移除这个网元
          </ElButton>
        </div>
      </ElCollapseItem>
    </ElCollapse>
    <div v-else class="form-mode__empty">配置里还没有服务，用「＋添加服务」从模板开始</div>

    <!-- ── 网络分节 ─────────────────────────────────────────────────── -->
    <div class="form-mode__head">
      <span class="form-mode__title">网络（{{ doc.networks.keys.length }}）</span>
      <ElButton size="small" plain @click="askAddEntry('networks')">＋</ElButton>
    </div>
    <div v-for="name in doc.networks.keys" :key="name" class="fm-entry">
      <span class="fm-entry__name">{{ name }}</span>
      <ElInput
        size="small"
        class="fm-entry__input"
        placeholder="驱动（默认 bridge）"
        :model-value="doc.networks.items[name]?.driver ?? ''"
        @update:model-value="setSectionField(doc.networks.items[name], 'driver', $event)"
      />
      <span class="fm-entry__switch">
        <ElSwitch
          size="small"
          :model-value="doc.networks.items[name]?.external === true"
          @update:model-value="
            setSectionField(doc.networks.items[name], 'external', $event ? true : undefined)
          "
        />
        外部网络
      </span>
      <ElButton size="small" text type="danger" @click="removeEntry('networks', name)">
        删除
      </ElButton>
    </div>
    <div v-if="doc.networks.keys.length === 0" class="form-mode__hint">没有网络定义</div>

    <!-- ── 数据卷分节 ───────────────────────────────────────────────── -->
    <div class="form-mode__head">
      <span class="form-mode__title">数据卷（{{ doc.volumes.keys.length }}）</span>
      <ElButton size="small" plain @click="askAddEntry('volumes')">＋</ElButton>
    </div>
    <div v-for="name in doc.volumes.keys" :key="name" class="fm-entry">
      <span class="fm-entry__name">{{ name }}</span>
      <ElInput
        size="small"
        class="fm-entry__input"
        placeholder="驱动（默认 local）"
        :model-value="doc.volumes.items[name]?.driver ?? ''"
        @update:model-value="setSectionField(doc.volumes.items[name], 'driver', $event)"
      />
      <span class="fm-entry__switch">
        <ElSwitch
          size="small"
          :model-value="doc.volumes.items[name]?.external === true"
          @update:model-value="
            setSectionField(doc.volumes.items[name], 'external', $event ? true : undefined)
          "
        />
        外部数据卷
      </span>
      <ElButton size="small" text type="danger" @click="removeEntry('volumes', name)">
        删除
      </ElButton>
    </div>
    <div v-if="doc.volumes.keys.length === 0" class="form-mode__hint">没有数据卷定义</div>

    <!-- ── 添加服务：模板 + 名称（spec §8 模板化） ──────────────────────── -->
    <ElDialog v-model="tplDialog.visible" title="添加服务" width="min(560px, 92vw)">
      <div class="fm-tpl">
        <ElRadioGroup v-model="tplDialog.templateKey" class="fm-tpl__list">
          <ElRadio v-for="t in SERVICE_TEMPLATES" :key="t.key" :value="t.key" class="fm-tpl__item">
            <span class="fm-tpl__label">{{ t.label }}</span>
            <span class="fm-tpl__desc">{{ t.description }}</span>
          </ElRadio>
        </ElRadioGroup>
        <div class="fm-tpl__name">
          <span class="fm-field__label">网元名称</span>
          <ElInput v-model="tplDialog.name" size="small" placeholder="字母或数字开头，可含 . _ -" />
        </div>
      </div>
      <template #footer>
        <ElButton @click="tplDialog.visible = false">取消</ElButton>
        <ElButton type="primary" @click="confirmAdd">添加</ElButton>
      </template>
    </ElDialog>
  </div>
</template>

<script setup lang="ts">
  /**
   * 表单模式（四期，spec §11.7）：服务折叠卡 + 网络/卷分节 + 未建模字段只读展示。
   *
   * 三条防注释丢失的②③在这里落地：已修改卡标「保存将重写此块，块内注释不保留」，
   * 未修改卡标「未修改，保存时原样保留」（①在容器里做模式切换确认）。
   *
   * 所有改动都写进父级的文档模型（同一份模型跨模式共享）；保存路径由容器决定 ——
   * 表单可表达的修改永远走最小 patch，未触碰段落（含注释）由 agent 逐字节保留。
   */
  import { ref, watch } from 'vue'
  import {
    ElButton,
    ElCollapse,
    ElCollapseItem,
    ElDialog,
    ElInput,
    ElMessage,
    ElMessageBox,
    ElOption,
    ElRadio,
    ElRadioGroup,
    ElSelect,
    ElSwitch,
    ElTag
  } from 'element-plus'
  import {
    addSectionEntry,
    isServiceModified,
    removeSectionEntry,
    SERVICE_TEMPLATES,
    setSectionField,
    setServiceField,
    templateService,
    uniqueServiceName,
    type ComposeDoc,
    type ComposeScalar,
    type ComposeService,
    type ServiceFieldKey
  } from '../../utils/compose'

  const props = withDefaults(
    defineProps<{
      /** 当前编辑中的文档模型（跨模式共享的那一份）。 */
      doc: ComposeDoc
      /** 载入时的模型（判断「已修改/未修改」的基线）。 */
      baseline: ComposeDoc | null
      /** 从项目页「＋添加服务」打开时，直接弹出模板选择。 */
      autoAdd?: boolean
    }>(),
    { baseline: null, autoAdd: false }
  )

  const RESTART_OPTIONS = ['no', 'always', 'on-failure', 'unless-stopped']
  const NAME_RE = /^[a-zA-Z0-9][a-zA-Z0-9_.-]*$/

  const activeNames = ref<string[]>([])

  function serviceOf(name: string): ComposeService {
    return props.doc.services.items[name]
  }

  function imageOf(name: string): string {
    return serviceOf(name).image ?? ''
  }

  /** 标量字段的统一渲染：null 与 undefined 都显示空串。 */
  function svc(value: unknown): string {
    return value === undefined || value === null ? '' : String(value)
  }

  function isModified(name: string): boolean {
    return props.baseline ? isServiceModified(props.baseline, props.doc, name) : false
  }

  function setField(name: string, key: ServiceFieldKey, value: unknown): void {
    setServiceField(serviceOf(name), key, value)
  }

  function rawKeys(name: string): string[] {
    return Object.keys(serviceOf(name)._raw ?? {})
  }

  function rawPreview(name: string, key: string): string {
    const value = serviceOf(name)._raw[key]
    const text = typeof value === 'string' ? value : JSON.stringify(value)
    return text.length > 160 ? `${text.slice(0, 160)}…` : text
  }

  function otherServices(name: string): string[] {
    return props.doc.services.keys.filter((n) => n !== name)
  }

  // ── 列表型字段（端口/卷）：空行允许存在，保存时由 flattenEntry 剔除空项 ──

  function setListItem(name: string, key: 'ports' | 'volumes', index: number, value: string): void {
    const list = [...(serviceOf(name)[key] ?? [])]
    list[index] = value
    setField(name, key, list)
  }

  function addListItem(name: string, key: 'ports' | 'volumes'): void {
    setField(name, key, [...(serviceOf(name)[key] ?? []), ''])
  }

  function removeListItem(name: string, key: 'ports' | 'volumes', index: number): void {
    const list = [...(serviceOf(name)[key] ?? [])]
    list.splice(index, 1)
    setField(name, key, list)
  }

  // ── 环境变量（映射模型）──

  function envEntries(name: string): [string, ComposeScalar][] {
    return Object.entries(serviceOf(name).environment ?? {})
  }

  function setEnvKey(name: string, oldKey: string, newKey: string): void {
    const env = { ...(serviceOf(name).environment ?? {}) }
    const value = env[oldKey]
    delete env[oldKey]
    env[newKey] = value
    setField(name, 'environment', env)
  }

  function setEnvValue(name: string, key: string, value: string): void {
    setField(name, 'environment', { ...(serviceOf(name).environment ?? {}), [key]: value })
  }

  function addEnv(name: string): void {
    const env = serviceOf(name).environment ?? {}
    if ('' in env) return
    setField(name, 'environment', { ...env, '': '' })
  }

  function removeEnv(name: string, key: string): void {
    const env = { ...(serviceOf(name).environment ?? {}) }
    delete env[key]
    setField(name, 'environment', env)
  }

  // ── 命令：字符串保持字符串；多行/原本是列表 → 字符串列表 ──

  function commandText(name: string): string {
    const cmd = serviceOf(name).command
    if (Array.isArray(cmd)) return cmd.join('\n')
    return cmd ?? ''
  }

  function setCommand(name: string, text: string): void {
    const lines = text
      .split('\n')
      .map((line) => line.trim())
      .filter((line) => line !== '')
    const wasArray = Array.isArray(serviceOf(name).command)
    if (lines.length === 0) {
      setField(name, 'command', undefined)
      return
    }
    setField(name, 'command', wasArray || lines.length > 1 ? lines : lines[0])
  }

  // ── 删除与新增 ──

  async function removeService(name: string): Promise<void> {
    try {
      await ElMessageBox.confirm(
        `将从配置里移除网元「${name}」，保存后生效。应用项目时可勾选「回收孤儿容器」清理它的容器。`,
        '移除网元',
        { confirmButtonText: '移除', cancelButtonText: '取消', type: 'warning' }
      )
    } catch {
      return
    }
    removeSectionEntry(props.doc, 'services', name)
  }

  async function removeEntry(section: 'networks' | 'volumes', name: string): Promise<void> {
    const noun = section === 'networks' ? '网络' : '数据卷'
    try {
      await ElMessageBox.confirm(`将从配置里移除${noun}「${name}」，保存后生效。`, `移除${noun}`, {
        confirmButtonText: '移除',
        cancelButtonText: '取消',
        type: 'warning'
      })
    } catch {
      return
    }
    removeSectionEntry(props.doc, section, name)
  }

  async function askAddEntry(section: 'networks' | 'volumes'): Promise<void> {
    const noun = section === 'networks' ? '网络' : '数据卷'
    let name: string
    try {
      const { value } = await ElMessageBox.prompt(
        '输入名称（字母或数字开头，可含 . _ -）',
        `添加${noun}`,
        {
          confirmButtonText: '添加',
          cancelButtonText: '取消',
          inputPattern: NAME_RE,
          inputErrorMessage: '名称需以字母或数字开头，可含 . _ -'
        }
      )
      name = (value ?? '').trim()
    } catch {
      return
    }
    if (!name) return
    if (props.doc[section].items[name]) {
      ElMessage.warning('同名已存在')
      return
    }
    addSectionEntry(props.doc, section, name, {})
  }

  // ── 模板对话框 ──

  const tplDialog = ref({ visible: false, templateKey: 'redis', name: '' })

  function openTemplateDialog(key = 'redis'): void {
    tplDialog.value = {
      visible: true,
      templateKey: key,
      name: uniqueServiceName(props.doc, key === 'blank' ? 'service' : key)
    }
  }

  watch(
    () => tplDialog.value.templateKey,
    (key) => {
      tplDialog.value.name = uniqueServiceName(props.doc, key === 'blank' ? 'service' : key)
    }
  )

  watch(
    () => props.autoAdd,
    (v) => {
      if (v) openTemplateDialog()
    },
    { immediate: true }
  )

  function confirmAdd(): void {
    const name = tplDialog.value.name.trim()
    if (!NAME_RE.test(name)) {
      ElMessage.warning('名称需以字母或数字开头，可含 . _ -')
      return
    }
    if (props.doc.services.items[name]) {
      ElMessage.warning('同名网元已存在')
      return
    }
    const service = templateService(tplDialog.value.templateKey)
    if (!service) return
    addSectionEntry(props.doc, 'services', name, service as unknown as Record<string, unknown>)
    tplDialog.value.visible = false
    if (!activeNames.value.includes(name)) activeNames.value.push(name)
  }
</script>

<style lang="scss" scoped>
  @use '@styles/core/breakpoints.scss' as *;
  @use '../../views/overview-tokens' as t;

  // 「删除服务」等 plain danger：对比度 AA 的病灶与处方见 overview-tokens
  // 的 danger-plain-aa（浅色 QA 实测 2.87:1）。
  @include t.danger-plain-aa;

  .form-mode {
    &__head {
      display: flex;
      align-items: center;
      justify-content: space-between;
      margin: 14px 0 8px;
    }

    &__title {
      font-size: 14px;
      font-weight: 600;
    }

    &__empty,
    &__hint {
      padding: 8px 0;
      color: var(--el-text-color-secondary);
      font-size: 12px;
    }
  }

  .fm-cards {
    border-top: none;
    border-bottom: none;
  }

  .fm-svc {
    &__name {
      margin-right: 10px;
      font-weight: 600;
    }

    &__actions {
      margin-top: 12px;
    }
  }

  .fm-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
    gap: 8px 16px;
  }

  .fm-field {
    display: flex;
    gap: 8px;
    align-items: flex-start;
    margin-bottom: 8px;

    &__label {
      flex: none;
      width: 68px;
      padding-top: 5px;
      color: var(--el-text-color-secondary);
      font-size: 12px;
      text-align: right;
    }
  }

  .fm-list {
    flex: 1 1 auto;
    min-width: 0;

    &__row {
      display: flex;
      gap: 6px;
      align-items: center;
      margin-bottom: 6px;
    }

    &__key {
      max-width: 180px;
    }
  }

  .fm-raw {
    margin-top: 10px;
    padding: 8px 10px;
    border: 1px dashed var(--el-border-color);
    border-radius: 6px;
    background: var(--el-fill-color-lighter);

    &__title {
      margin-bottom: 4px;
      color: var(--el-text-color-secondary);
      font-size: 12px;
      font-weight: 600;
    }

    &__row {
      display: flex;
      gap: 8px;
      font-size: 12px;
      line-height: 1.7;
    }

    &__key {
      flex: none;
      width: 120px;
      font-family: var(--art-font-family-mono, monospace);
    }

    &__value {
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    &__hint {
      margin-top: 4px;
      color: var(--el-text-color-secondary);
      font-size: 12px;
    }
  }

  .fm-entry {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;
    padding: 6px 0;
    border-bottom: 1px dashed var(--el-border-color-lighter);

    &__name {
      min-width: 120px;
      font-size: 13px;
      font-weight: 600;
    }

    &__input {
      width: 220px;
    }

    &__switch {
      display: inline-flex;
      gap: 6px;
      align-items: center;
      color: var(--el-text-color-secondary);
      font-size: 12px;
    }
  }

  .fm-tpl {
    &__list {
      display: flex;
      flex-direction: column;
      gap: 6px;
      align-items: stretch;
    }

    &__item {
      height: auto;
      padding: 6px 8px;
      border: 1px solid var(--el-border-color-lighter);
      border-radius: 6px;
    }

    &__label {
      margin-right: 8px;
      font-weight: 600;
    }

    &__desc {
      color: var(--el-text-color-secondary);
      font-size: 12px;
    }

    &__name {
      display: flex;
      gap: 10px;
      align-items: center;
      margin-top: 14px;
    }
  }

  /* ── 响应式 ─────────────────────────────────────── */

  // 手机横屏（<768）：折叠卡标题里的「已修改 · 保存将重写此块…」标注较长，
  // 允许换行（原为固定 48px 单行），否则网元名会被挤出可视区；
  // 网络/数据卷行的驱动输入改为可伸缩（原为固定 220px）。
  @include respond-below('tablet') {
    .fm-cards :deep(.el-collapse-item__header) {
      height: auto;
      min-height: 48px;
      padding: 8px 0;
      flex-wrap: wrap;
      row-gap: 4px;
      line-height: 1.5;
    }

    .fm-svc__name {
      word-break: break-all;
    }

    .fm-entry__input {
      width: auto;
      min-width: 0;
      flex: 1 1 160px;
    }
  }
</style>
