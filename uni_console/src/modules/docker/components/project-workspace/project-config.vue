<template>
  <!-- 单根（single-root 守卫：布局的 Transition 只支持单根，双根切页白屏）。 -->
  <div class="pwc">
    <div class="pwc__bar">
      <!-- 配置文件：列 basename（路径常很长），全路径挂 title 供悬浮核对；
           旧版 compose 不上报路径时把「未知」的原因一并说出来。 -->
      <span class="pwc__files" :title="configFilesTitle">{{ configFileText() }}</span>
      <span class="pwc__spacer"></span>
      <ElButton v-if="canConfig" size="small" text @click="openEditor(false)">编辑</ElButton>
      <ElButton v-if="canConfig" size="small" text @click="openEditor(true)">＋添加服务</ElButton>
      <BackupHistory
        :backups="backups"
        :loaded="backupLoaded"
        :loading="backupLoading"
        :current-hash="backupHash"
        :current-content="backupContent"
        :disabled="busy || loading"
        :can-rollback="canConfig"
        :rolling-back="busy"
        @load="loadBackups"
        @rollback="askRollback"
      />
    </div>

    <!-- 只读内容主体：等宽直显当前配置文件，宽度吃满内容区（展示面整块优先 ——
         旧版「查看配置」窄弹层已删，内容就是这一区的主体，不再要点开才见）。
         数据源与备份列表同一条 compose.file:read（就位即拉）：加载骨架 / 失败结论
         + 重试 / 正文三态；失败原因直接显示服务端/agent 给的结论句 —— 这里再包
         一层「操作失败」只会把「设备离线」「路径没记录」「文件被删」说成同一句话。 -->
    <div class="pwc__view">
      <ElSkeleton v-if="viewLoading" :rows="6" animated />
      <template v-else-if="backupError">
        <ElAlert type="warning" :title="backupError" :closable="false" />
        <div class="pwc__view-retry">
          <ElButton size="small" @click="loadBackups()">重新加载</ElButton>
        </div>
      </template>
      <pre v-else class="pwc__yml-body">{{ backupContent }}</pre>
    </div>

    <!-- 四期配置编辑器：双模式编辑 + 保存收尾 + 独立「应用」；载入/保存/回滚都在它里面。
         集成代码平移自 projects.vue，语义零改动（编辑器内部的指令通道仍是它自己的）。 -->
    <ComposeEditor
      :model-value="editor.visible"
      :project="editor.project"
      :project-protected="editor.protected"
      :auto-add="editor.addService"
      @update:model-value="onEditorVisible"
      @saved="onEditorSaved"
      @applied="refresh"
    />

    <!-- 回滚确认（强确认档 = 照抄项目名；与编辑器内的回滚同一协议路径）。 -->
    <DockerActionConfirm
      v-model="rollbackConfirm.visible"
      :action="rollbackConfirm.action"
      :target="rollbackConfirm.project"
      :override="rollbackConfirm.override"
      target-kind="项目"
      :loading="confirmLoading"
      @confirm="onRollbackConfirm"
    />
  </div>
</template>

<script setup lang="ts">
  /**
   * 配置区（5b 工作台；布局族统一后为工作台「配置」页签的宿主）：compose-editor 四件套的
   * 宿主 —— 编辑/校验/保存（diff 预览 + 强确认 + patch/write）/应用/备份/回滚全部在
   * ComposeEditor 里，本组件只做三件事：
   *   ① 打开它（「编辑」/「＋添加服务」两个入口）；
   *   ② 备份历史的懒加载与回滚确认（平移自 projects.vue，语义零改动）；
   *   ③ 只读内容主体：当前配置文件等宽直显（与备份列表同一条 compose.file:read
   *     载荷 —— 旧版「查看配置」窄弹层已删，内容直接吃满页签宽度）。
   * 指令通道（run）由父页传入：与列表页「同一 composable 服务全部动作」等价，
   * 备份读/回滚写都过同一条受理 + 轮询 + 重拉通道。
   */
  import { computed, ref, watch } from 'vue'
  import { ElAlert, ElButton, ElMessage, ElSkeleton } from 'element-plus'
  import { useAuth } from '@/hooks/core/useAuth'
  import { PermDockerConfig } from '@/enums/permission'
  import BackupHistory from '../compose-editor/backup-history.vue'
  import ComposeEditor from '../compose-editor/compose-editor.vue'
  import DockerActionConfirm from '../action-confirm.vue'
  import type { DockerProjectItem } from '../../api'
  import {
    runErrorMessage,
    type DockerCmdInput,
    type DockerCmdRunResult
  } from '../../composables/useDockerCmds'
  import type { ConfirmOverride } from '../../utils/confirm'
  import {
    COMPOSE_ACTIONS,
    formatBackupTime,
    parseComposeFilePayload,
    type ComposeBackup
  } from '../../utils/compose'
  import { useDockerHost } from '../../utils/host-context'

  defineOptions({ name: 'DockerProjectConfig' })

  const props = defineProps<{
    project: DockerProjectItem | null
    /** 父页的指令通道（备份读/回滚写都走它；成功后的重拉在 composable 里）。 */
    run: (input: DockerCmdInput) => Promise<DockerCmdRunResult>
    /** 全局在途（备份入口/回滚按钮的禁用信号之一）。 */
    busy: boolean
    /** 快照在拉（备份入口的禁用信号之一 —— 快照没到时项目事实还不完整）。 */
    loading: boolean
    /** 保存/回滚成功后的重拉（父页的 refresh）。 */
    refresh: () => void | Promise<void>
  }>()

  const emit = defineEmits<{
    /** 备份份数回传（hero 的元信息行显示它；加载失败不回传，hero 保持上一数字）。 */
    (e: 'backupsCount', count: number): void
  }>()

  const ctx = useDockerHost()
  const { hasAuth } = useAuth()

  /** 四期配置编辑的权限门：编辑/添加服务/回滚三个入口都由它决定渲染。 */
  const canConfig = computed(() => hasAuth(PermDockerConfig))

  /** 当前项目名（指令 target 与编辑器都用它；快照未到时为空串 → 入口不动作）。 */
  const projectName = computed(() => props.project?.name ?? '')

  const editor = ref({ visible: false, project: '', addService: false, protected: false })

  /** 打开编辑器（「编辑配置」/「＋添加服务」两个入口共用；权限门在模板上）。 */
  function openEditor(addService: boolean): void {
    if (!props.project) {
      return
    }
    editor.value = {
      visible: true,
      project: props.project.name,
      addService,
      protected: props.project.protected === true
    }
  }

  /** 关闭时把「添加服务」的意图收掉：下次打开是全新的一次。 */
  function onEditorVisible(value: boolean): void {
    editor.value = {
      ...editor.value,
      visible: value,
      addService: value ? editor.value.addService : false
    }
  }

  /** 编辑器保存/回滚成功：备份列表已变、快照里的配置指纹也可能变，都重拉一遍。 */
  function onEditorSaved(): void {
    void loadBackups(true)
    void props.refresh()
  }

  interface ProjectBackups {
    loading: boolean
    backups: ComposeBackup[]
    hash: string
    /** 当前文件正文（备份详情里对照着看；备份正文协议上不可读）。 */
    content: string
  }

  /** 备份历史（工作台页对当前项目**就位即拉**：hero 元信息要显示备份计数 ——
       列表页是点「历史备份」才发的懒加载，这里是页面形态差异，语义（失败可重试）
       不变）。 */
  const backupState = ref<ProjectBackups | null>(null)

  /** 读取失败的结论句（只读内容区就地呈现；成功即清）。失败时 backupState 归
   *  null —— 「已加载 0 份」的假象与「读失败」是两种事实。 */
  const backupError = ref('')

  const backups = computed(() => backupState.value?.backups ?? [])
  const backupLoaded = computed(() => backupState.value !== null)
  const backupLoading = computed(() => backupState.value?.loading === true)
  const backupHash = computed(() => backupState.value?.hash ?? '')
  const backupContent = computed(() => backupState.value?.content ?? '')

  /** 只读内容区加载态：拉取在途，或从未拿到过且还没有失败结论（失败即切结论态，
   *  不无限转骨架）。 */
  const viewLoading = computed(
    () => backupLoading.value || (backupState.value === null && backupError.value === '')
  )

  async function loadBackups(force = false): Promise<void> {
    const name = projectName.value
    if (!name) {
      return
    }
    const cur = backupState.value
    if (cur?.loading) {
      return
    }
    if (cur && !force) {
      return
    }
    backupError.value = ''
    backupState.value = {
      loading: true,
      backups: cur?.backups ?? [],
      hash: cur?.hash ?? '',
      content: cur?.content ?? ''
    }
    const res = await props.run({
      action: COMPOSE_ACTIONS.read,
      target: name,
      key: `backups:${name}`
    })
    if (!res.ok) {
      // 读失败不留下「已加载（0 份）」的假象：清掉条目，结论句就地给在内容区
      //（服务端/agent 给的原文优先），「重新加载」即重试。
      backupState.value = null
      backupError.value = runErrorMessage(res, '读取配置失败')
      return
    }
    const view = parseComposeFilePayload(res.payload)
    backupState.value = {
      loading: false,
      backups: view.backups,
      hash: view.hash,
      content: view.content
    }
    // 动作成功但正文为空：如实说「未取到」，不展示一块空 pre。
    if (!view.content) backupError.value = '未取到配置文件内容'
    emit('backupsCount', view.backups.length)
  }

  interface RollbackConfirmState {
    visible: boolean
    action: string
    project: string
    backup: ComposeBackup | null
    hash: string
    override?: ConfirmOverride
  }

  const rollbackConfirm = ref<RollbackConfirmState>({
    visible: false,
    action: COMPOSE_ACTIONS.write,
    project: '',
    backup: null,
    hash: ''
  })
  const confirmLoading = ref(false)

  /** 选了一版备份 → 强确认（照抄项目名）→ write 的 backup 模式（乐观锁基线随行）。 */
  function askRollback(backup: ComposeBackup): void {
    if (!props.project) return
    rollbackConfirm.value = {
      visible: true,
      action: COMPOSE_ACTIONS.write,
      project: props.project.name,
      backup,
      hash: backupHash.value,
      override: {
        kind: 'target-word',
        label: '回滚配置',
        danger: 'danger',
        conclusion: `将把「${props.project.name}」的配置回滚到 ${formatBackupTime(backup.at)} 的版本；当前内容会先备份。`,
        expected: props.project.name
      }
    }
  }

  async function onRollbackConfirm(payload: { confirm: string; force: boolean }): Promise<void> {
    const st = rollbackConfirm.value
    if (!st.backup) return
    confirmLoading.value = true
    try {
      const res = await props.run({
        action: st.action,
        target: st.project,
        options: { backup: st.backup.token, baseHash: st.hash },
        confirm: payload.confirm
      })
      if (res.ok) {
        ElMessage.success(res.detail || '操作已完成')
        // 回滚成功：备份列表与基线 hash 都变了，立即重拉（文件内容已由服务端回读校验）。
        await loadBackups(true)
      } else {
        ElMessage.error(runErrorMessage(res, '操作未完成'))
      }
    } finally {
      confirmLoading.value = false
      rollbackConfirm.value = { ...st, visible: false, backup: null }
    }
  }

  /** 取路径的 basename（条目里只放文件名，全路径放悬浮）。 */
  function baseName(path: string): string {
    const parts = path.split('/')
    return parts[parts.length - 1] || path
  }

  function configFileText(): string {
    const full = props.project?.configFiles?.[0]
    return full ? baseName(full) : '未知（旧版 compose 未记录）'
  }

  const configFilesTitle = computed(() => (props.project?.configFiles ?? []).join('\n'))

  // 主机切换/项目名变（同路由不同项目）：四期状态属于上一台机器/上一个项目 ——
  // 编辑器/回滚弹窗收起、备份缓存清空后重拉（平移 projects.vue 的 onHostSwitch 纪律）。
  //
  // ⚠ 写成**多源 watch**而不是 `() => [ctx.hostId, projectName.value]` 的单 getter：
  // 后者每次求值都产一个新数组（身份永不相等），任何被跟踪依赖一触发（例如快照重拉
  // 换了 project 对象身份）回调就会重放 —— loadBackups → run → refresh → state 更新
  // → 再触发，形成每轮快照都重拉备份的死循环（测试里直接把 worker 吃到 OOM）。
  // 多源 watch 按源逐个比较（字符串相等即不触发），主机/项目名不变就不再发指令。
  watch(
    [() => ctx.hostId, projectName],
    () => {
      editor.value = { visible: false, project: '', addService: false, protected: false }
      rollbackConfirm.value = { ...rollbackConfirm.value, visible: false, backup: null }
      backupState.value = null
      backupError.value = ''
      emit('backupsCount', 0)
      if (ctx.hostId && projectName.value) void loadBackups()
    },
    { immediate: true }
  )
</script>

<style lang="scss" scoped>
  @use '@styles/core/breakpoints.scss' as *;
  @use '../../views/overview-tokens' as t;

  .pwc__bar {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;
  }

  .pwc__files {
    min-width: 0;
    font-family: var(--art-font-family-mono, monospace);
    font-size: 12px;
    word-break: break-all;
  }

  .pwc__spacer {
    flex: 1 1 auto;
  }

  // 只读内容主体：与工具条拉开一点距离；**不再限高**（旧窄弹层的 60vh 约束随弹层
  // 一起删除）—— 内容在页签里自然生长，滚动交给页面自身。
  .pwc__view {
    margin-top: 10px;
  }

  .pwc__view-retry {
    margin-top: 8px;
  }

  // 配置文本：等宽字体 + 保留原始换行；长行折行（yml 里可能有很长的 environment）。
  .pwc__yml-body {
    margin: 0;
    font-family: var(--art-font-family-mono, monospace);
    font-size: 12px;
    line-height: 1.6;
    white-space: pre-wrap;
    word-break: break-all;
    color: var(--el-text-color-primary);
  }

  // 手机横屏（窄于 tablet 断点 768）：文件名独占一行，动作按钮换到第二行（不再互相挤压）。
  @include respond-below('tablet') {
    .pwc__files {
      flex: 1 1 100%;
    }
  }
</style>
