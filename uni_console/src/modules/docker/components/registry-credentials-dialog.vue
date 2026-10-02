<template>
  <!-- 单根（single-root 守卫扫描全模块的 .vue）：ElDialog 是唯一根，与
       pull-progress-dialog 同挂法（原地渲染、append-to-body 未开）。删除确认
       （DockerActionConfirm）挂在默认插槽里 —— EP 弹层各走 z-index 计数，后开的
       确认恒在上层；页内没有第二个嵌套层，不需要搬到 body。 -->
  <ElDialog
    :model-value="modelValue"
    :title="mode === 'form' ? (editing ? '编辑凭据' : '新增凭据') : '仓库凭据'"
    width="min(680px, 94vw)"
    :close-on-click-modal="false"
    :close-on-press-escape="!saving && !deleting && !confirmVisible"
    @update:model-value="onVisible"
    @closed="resetAfterClose"
  >
    <!-- ── 列表态：一行一条凭据 ──
         密码列刻意不存在：列表读路径恒为掩码「****」，画出来只会让人误以为
         「这就是密码」。用途说明（顶部一句）替它把「存的是什么」讲清。 -->
    <div v-if="mode === 'list'" class="rg-list">
      <p class="rg-list__hint">拉取私有仓库镜像时选用；密码只进不出（保存后不可读回）。</p>

      <p v-if="listError" class="rg-list__error">
        {{ listError }}
        <a class="rg-list__retry" @click.prevent="loadList">重试</a>
      </p>
      <p v-else-if="listLoading" class="rg-list__state">正在读取凭据清单…</p>

      <template v-else>
        <div v-if="items.length" class="rg-rows">
          <div class="rg-row rg-row--head">
            <span>仓库地址</span>
            <span>用户名</span>
            <span>备注</span>
            <span>创建于</span>
            <span>操作</span>
          </div>
          <div v-for="item in items" :key="item.registry" class="rg-row">
            <!-- 等宽：地址是「键」（拉取时要逐字对上），可纵向对读与复制。 -->
            <span class="rg-row__registry" :title="item.registry">{{ item.registry }}</span>
            <span class="rg-row__username" :title="item.username">{{ item.username }}</span>
            <span class="rg-row__remark" :title="item.remark">{{ item.remark || '—' }}</span>
            <span class="rg-row__created">{{ createdText(item) }}</span>
            <span class="rg-row__ops">
              <ElButton size="small" text :disabled="deleting" @click="openEdit(item)">
                编辑
              </ElButton>
              <ElButton
                size="small"
                text
                type="danger"
                :disabled="deleting"
                @click="askDelete(item)"
              >
                删除
              </ElButton>
            </span>
          </div>
        </div>
        <ElEmpty v-else description="还没有保存任何仓库凭据" />
      </template>
    </div>

    <!-- ── 表单态（新增与编辑共用）──
         编辑与新增的唯二差异：地址是定位键（不可改）、密码必须重输（后端对空
         密码给 400 —— 没有「留空保持原密码」的语义，见下方密码字段说明）。 -->
    <div v-else class="rg-form">
      <label class="rg-field">
        <span class="rg-field__label">仓库地址</span>
        <div class="rg-field__control">
          <ElInput
            v-model="form.registry"
            class="rg-field__input rg-mono"
            placeholder="例如 harbor.example.com 或 192.168.1.10:5000"
            :disabled="editing"
            clearable
          />
          <span v-if="registryIssue" class="rg-field__issue">{{ registryIssue }}</span>
          <span v-else-if="editing" class="rg-field__hint">
            地址是定位键，不能改；要换地址请新增一条再删旧条
          </span>
        </div>
      </label>

      <label class="rg-field">
        <span class="rg-field__label">用户名</span>
        <div class="rg-field__control">
          <ElInput
            v-model="form.username"
            class="rg-field__input"
            placeholder="仓库登录名"
            clearable
          />
          <span v-if="usernameIssue" class="rg-field__issue">{{ usernameIssue }}</span>
        </div>
      </label>

      <label class="rg-field">
        <span class="rg-field__label">密码</span>
        <div class="rg-field__control">
          <!-- type=password：明文只在输入的这一刻存在；本组件不落任何日志，
               提交后即随请求体离开（后端受理后加密封盒）。 -->
          <ElInput
            v-model="form.password"
            class="rg-field__input"
            type="password"
            show-password
            autocomplete="new-password"
            :placeholder="editing ? '必须重新输入（旧密码不可读回）' : '仓库密码'"
          />
          <span v-if="passwordIssue" class="rg-field__issue">{{ passwordIssue }}</span>
          <span v-else-if="editing" class="rg-field__hint">
            没有「留空保持原密码」：旧密码任何读路径都取不回，更新必须重新输入一遍
          </span>
        </div>
      </label>

      <label class="rg-field">
        <span class="rg-field__label">备注</span>
        <div class="rg-field__control">
          <ElInput
            v-model="form.remark"
            class="rg-field__input"
            placeholder="可选：这条凭据的用途说明"
            clearable
          />
        </div>
      </label>

      <!-- 保存失败（409 地址已存在 / 400 形态不合法 / 403 …）：结论句来自服务端
           msg，就地显示；表单留着可改可重试（对话框开着不放 toast）。 -->
      <p v-if="formError" class="rg-form__error">{{ formError }}</p>
    </div>

    <template #footer>
      <template v-if="mode === 'list'">
        <ElButton :disabled="deleting" @click="requestClose">关闭</ElButton>
        <ElButton type="primary" :disabled="listLoading" @click="openCreate">新增凭据</ElButton>
      </template>
      <template v-else>
        <ElButton :disabled="saving" @click="backToList">返回列表</ElButton>
        <ElButton type="primary" :disabled="!canSubmit" :loading="saving" @click="submit">
          保存
        </ElButton>
      </template>
    </template>

    <!-- 删除确认（标准档）：目标卡 = 仓库地址。凭据 CRUD 不是 docker 指令、
         不在二期写动作注册表里，形态与结论句由 override 显式给出（与四期配置
         编辑同一条路）；action 只是 confirm 的形态推导键，服务端不走它。 -->
    <DockerActionConfirm
      v-model="confirmVisible"
      action="registry:delete"
      :target="deleteTarget"
      :override="DELETE_OVERRIDE"
      target-kind="仓库"
      :loading="deleting"
      @confirm="onDeleteConfirm"
    />
  </ElDialog>
</template>

<script setup lang="ts">
  /**
   * 仓库凭据管理对话框（4c 前端半边）：私有仓库凭据的 CRUD 入口。
   *
   * 权限口径：**入口**在 images 页底栏、只对 docker:config 渲染（不渲染 ≠ 禁用，
   * 模块纪律）；本组件不再自判 —— 打得开对话框的人已在入口处过了权限，服务端
   * 的静态 perm 是第二道闸（403 折成结论句就地显示）。
   *
   * 密码纪律（与后端契约逐字对齐）：
   *   - 列表读路径恒为掩码「****」，本组件不渲染密码列（画掩码只会让人误读）；
   *   - 创建与更新**都必输密码** —— 后端没有「读回再提交」的路径，更新留空 =
   *     400「密码不合法」，不是「保持原密码」。表单在编辑态用显式说明讲清这一点，
   *     而不是让用户在失败后猜；
   *   - 明文只在输入与请求体里瞬时存在：type=password、组件不落任何日志。
   *
   * 确认档：删除走 DockerActionConfirm 标准档（目标卡 = registry 地址），
   * override 给形态与结论句（删除即失效 —— 下一单带该地址的拉取在受理处即被拒）。
   */
  import { computed, ref, watch } from 'vue'
  import { ElButton, ElDialog, ElEmpty, ElInput, ElMessage } from 'element-plus'
  import DockerActionConfirm from './action-confirm.vue'
  import {
    createDockerRegistry,
    deleteDockerRegistry,
    fetchDockerRegistries,
    updateDockerRegistry,
    type DockerRegistryItem,
    type DockerRegistrySaveBody
  } from '../api'
  import { formatRelativeTime } from '../utils/display'
  import type { ConfirmOverride } from '../utils/confirm'
  import { isValidRegistryAddr } from '../utils/registry'

  defineOptions({ name: 'DockerRegistryCredentialsDialog' })

  const props = defineProps<{ modelValue: boolean }>()
  const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void }>()

  type Mode = 'list' | 'form'

  /** 删除确认的形态（标准档：无需逐字输入，结论句讲清「删除即失效」）。 */
  const DELETE_OVERRIDE: ConfirmOverride = {
    kind: 'confirm',
    label: '删除',
    danger: 'danger',
    conclusion: '删除即失效：用该仓库的后续拉取会被提示没有凭据。此操作不可恢复。'
  }

  // ── 列表态 ──

  const mode = ref<Mode>('list')
  const items = ref<DockerRegistryItem[]>([])
  const listLoading = ref(false)
  /** 列表读取失败结论句（含「重试」；空串 = 无失败）。 */
  const listError = ref('')

  async function loadList(): Promise<void> {
    listLoading.value = true
    listError.value = ''
    try {
      const resp = await fetchDockerRegistries()
      items.value = resp.list
    } catch (e) {
      items.value = []
      listError.value = `凭据清单读取失败：${errMsg(e, '请稍后重试')}`
    } finally {
      listLoading.value = false
    }
  }

  /** 创建于列：有时刻给相对时间（「上个月存的」比一串秒数可读），没有给占位符。 */
  function createdText(item: DockerRegistryItem): string {
    return item.createdAt ? formatRelativeTime(item.createdAt) : '—'
  }

  // ── 表单态（新增与编辑共用） ──

  interface FormState {
    registry: string
    username: string
    password: string
    remark: string
  }

  const EMPTY_FORM: FormState = { registry: '', username: '', password: '', remark: '' }

  const form = ref<FormState>({ ...EMPTY_FORM })
  /** true = 编辑既有凭据（地址是定位键、不可改）；false = 新增。 */
  const editing = ref(false)
  /** 首次点保存后置位：必填的空字段从「不吭声」转「报必填」（没碰过就骂是打扰）。 */
  const touched = ref(false)
  const saving = ref(false)
  /** 保存失败的结论句（服务端 msg 优先；空串 = 无失败）。 */
  const formError = ref('')

  const registryIssue = computed(() => {
    if (editing.value) return '' // 定位键不可改，无校验语义（禁用输入框）
    const v = form.value.registry.trim()
    if (v === '') return touched.value ? '仓库地址必填' : ''
    return isValidRegistryAddr(v)
      ? ''
      : '仓库地址不合法：主机名/IP + 可选端口（不带 https:// 与镜像路径）'
  })

  const usernameIssue = computed(() =>
    form.value.username.trim() === '' && touched.value ? '用户名必填' : ''
  )

  const passwordIssue = computed(() => {
    if (form.value.password !== '') return ''
    // 编辑态的空密码是后端唯一会拒的形态（400「密码不合法」）—— 挡在前端先说清。
    if (!touched.value) return ''
    return editing.value ? '更新必须重新输入密码（旧密码不可读回）' : '密码必填'
  })

  const canSubmit = computed(
    () =>
      !saving.value &&
      registryIssue.value === '' &&
      usernameIssue.value === '' &&
      passwordIssue.value === ''
  )

  function openCreate(): void {
    form.value = { ...EMPTY_FORM }
    editing.value = false
    touched.value = false
    formError.value = ''
    mode.value = 'form'
  }

  /** 编辑既有凭据：地址带入但禁改；密码**刻意**从空开始（必须重输，见组件头注释）。 */
  function openEdit(item: DockerRegistryItem): void {
    form.value = {
      registry: item.registry,
      username: item.username,
      password: '',
      remark: item.remark ?? ''
    }
    editing.value = true
    touched.value = false
    formError.value = ''
    mode.value = 'form'
  }

  function backToList(): void {
    mode.value = 'list'
  }

  async function submit(): Promise<void> {
    touched.value = true
    if (!canSubmit.value) return
    saving.value = true
    formError.value = ''
    // 密码不做 trim：空格可能是密码的一部分（trim 会把合法密码改错）；
    // 地址/用户名 trim 与后端规范化（小写 + 去首尾空白）同向。
    const body: DockerRegistrySaveBody = {
      registry: form.value.registry.trim(),
      username: form.value.username.trim(),
      password: form.value.password,
      remark: form.value.remark.trim()
    }
    try {
      if (editing.value) await updateDockerRegistry(body)
      else await createDockerRegistry(body)
      // 保存成功回列表并重拉：清单纯度交给服务端（409/掩码/排序都在它那侧定）。
      mode.value = 'list'
      await loadList()
    } catch (e) {
      formError.value = errMsg(e, '保存失败，请稍后重试')
    } finally {
      saving.value = false
    }
  }

  // ── 删除（标准档确认） ──

  const confirmVisible = ref(false)
  const deleteTarget = ref('')
  const deleting = ref(false)

  function askDelete(item: DockerRegistryItem): void {
    deleteTarget.value = item.registry
    confirmVisible.value = true
  }

  async function onDeleteConfirm(): Promise<void> {
    deleting.value = true
    try {
      await deleteDockerRegistry(deleteTarget.value)
      confirmVisible.value = false
      await loadList()
    } catch (e) {
      // 删除失败：确认弹窗收掉、结论句走 toast —— 与页面级确认弹窗的回执口径
      // 一致（images 页 onConfirmSubmit 的 reportResult），表单不在场就不就地给。
      confirmVisible.value = false
      ElMessage.error(`删除失败：${errMsg(e, '请稍后重试')}`)
    } finally {
      deleting.value = false
    }
  }

  // ── 开关与收口 ──

  function requestClose(): void {
    emit('update:modelValue', false)
  }

  /** X/ESC/遮罩路径：ElDialog 只回写 modelValue，父组件没接 v-model 时收不到。 */
  function onVisible(v: boolean): void {
    emit('update:modelValue', v)
  }

  /** 关闭动画收尾后清表单（action-confirm 的 reset 同款纪律）：下次打开不沿用
      上一次的草稿 —— 尤其密码，留在内存里的明文多活一屏没有任何收益。 */
  function resetAfterClose(): void {
    form.value = { ...EMPTY_FORM }
    editing.value = false
    touched.value = false
    formError.value = ''
    mode.value = 'list'
  }

  watch(
    () => props.modelValue,
    (v) => {
      // 打开即回列表态并拉最新清单（凭据可能在别处被改过 —— 拉取对话框、别的页
      // 不会写它，但「打开时重拉」是最便宜的自洽保证）。immediate：「带着打开态
      // 挂载」与「先挂载再打开」走同一条路。
      if (v) {
        mode.value = 'list'
        void loadList()
      }
    },
    { immediate: true }
  )

  /** 错误结论句：服务端 msg 优先（它比前端编的准），缺失给回退句。 */
  function errMsg(e: unknown, fallback: string): string {
    const msg = (e as { message?: string })?.message
    return msg && msg.trim() !== '' ? msg : fallback
  }
</script>

<style lang="scss" scoped>
  @use '@styles/core/breakpoints.scss' as *;
  @use '../views/overview-tokens' as t;

  // 「关闭 / 返回列表 / 编辑」等默认档按钮（含 text 变体）的主色文字对比度 AA：
  // 病灶与处方见 overview-tokens 的 primary-text-aa（终审 QA D2·浅色实测 3.68:1）。
  @include t.primary-text-aa;

  // 等宽字体（地址是「键」：要逐字对上，等宽才可对读与肉眼校对）。
  .rg-mono {
    font-family: var(--el-font-family-mono, ui-monospace, 'SFMono-Regular', Consolas, monospace);
  }

  // 列表态：顶部一句用途说明（替不存在的密码列把「存的是什么」讲清）。
  .rg-list {
    &__hint {
      margin: 0 0 10px;
      color: var(--el-text-color-secondary);
      font-size: 12px;
      line-height: 1.6;
    }

    &__error {
      margin: 0;
      color: var(--el-color-danger);
      font-size: 13px;
      line-height: 1.6;
    }

    &__state {
      margin: 0;
      padding: 24px 0;
      color: var(--el-text-color-secondary);
      font-size: 13px;
      text-align: center;
    }

    &__retry {
      margin-left: 6px;
      color: var(--el-color-primary);
      cursor: pointer;
    }
  }

  // 凭据行（表头 + 数据行同一套网格模板，列天然对齐）。
  .rg-rows {
    --rg-grid: minmax(0, 1.3fr) minmax(0, 0.7fr) minmax(0, 1fr) 92px 116px;
  }

  .rg-row {
    display: grid;
    grid-template-columns: var(--rg-grid);
    gap: 8px;
    align-items: center;
    padding: 8px 0;
    font-size: 13px;
    line-height: 1.5;

    & + .rg-row {
      border-top: 1px solid var(--el-border-color-lighter);
    }

    // 表头：弱化色、不换行（数据行的省略规则不适用于它）。
    &--head {
      color: var(--el-text-color-secondary);
      font-size: 12px;
      white-space: nowrap;
    }

    &__registry {
      overflow: hidden;
      font-family: var(--el-font-family-mono, ui-monospace, 'SFMono-Regular', Consolas, monospace);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    &__username,
    &__remark {
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    &__remark,
    &__created {
      color: var(--el-text-color-secondary);
    }

    &__ops {
      display: flex;
      justify-content: flex-end;
      gap: 4px;
      white-space: nowrap;
    }
  }

  // 表单态：字段自上而下按「键 → 身份 → 秘密 → 说明」排（地址是定位键在先）。
  .rg-form {
    padding-bottom: 4px;

    &__error {
      margin: 12px 0 0;
      color: var(--el-color-danger);
      font-size: 12px;
      line-height: 1.6;
    }
  }

  .rg-field {
    display: block;
    margin-bottom: 14px;

    &__label {
      display: block;
      margin-bottom: 6px;
      font-size: 13px;
    }

    &__issue {
      display: block;
      margin-top: 6px;
      color: var(--el-color-danger);
      font-size: 12px;
      line-height: 1.6;
    }

    &__hint {
      display: block;
      margin-top: 6px;
      color: var(--el-text-color-secondary);
      font-size: 12px;
      line-height: 1.6;
    }
  }

  // 平板竖屏以下（对话框占满宽度）：备注与创建时间是元数据，让位给地址/用户名/
  // 操作（地址是键、操作是动作，窄屏上这两者不能丢）。
  @include respond-below('tablet') {
    .rg-rows {
      --rg-grid: minmax(0, 1.2fr) minmax(0, 0.8fr) 104px;
    }

    .rg-row__remark,
    .rg-row--head span:nth-child(3),
    .rg-row__created,
    .rg-row--head span:nth-child(4) {
      display: none;
    }
  }
</style>
