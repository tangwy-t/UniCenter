<template>
  <!-- ⚠ 单根包装：页面**必须只有一个根节点**。
       布局把页面放进 `<Transition mode="out-in">`（components/core/layouts/art-page-content），
       而 Vue 的 Transition 只支持单根元素。此前本页是 `<DockerPage>` 与 `<ElDialog>` 两个兄弟
       根节点（fragment），它作为「离场方」参与一次 out-in 切换后，过渡内部的元素记账就坏了 ——
       症状是**从本页切到任何其它页面都白屏，必须刷新**（本页渲染时出口区给的 .art-page-view
       类也落不下）。只有本模块踩到，因为其余页面都是单根。
       守卫：__tests__/single-root.test.ts 扫描模块内所有 .vue，双根即红灯。 -->
  <div class="docker-projects-page">
    <DockerPage
      :loading="loading"
      :stale="stale"
      :age-seconds="ageSeconds"
      :never-reported="neverReported"
      :load-error="loadError"
      :has-state="hasState"
      @refresh="refresh"
    >
      <template #table>
        <!-- 提示行：只陈述当前能力边界；四期的配置编辑/新增服务/历史回滚入口在下方，
             权限不足时整组不渲染（分期控件矩阵）。 -->
        <div class="docker-hint">
          本页可操作项目与网元；配置可点「编辑配置」按表单或 YML 修改，历史备份可一键回滚
        </div>

        <ArtTableHeader :loading="loading" @refresh="refresh" />

        <!-- 两种空态分开：主机上没有项目 vs 清单还没到（后者尚不知有没有主机，
             不能先喊「没有项目」），故 v-if 把主机清单的加载态一并算进来。
             空态渲染在本页、不写进 ArtTable 的 `#empty` 插槽：ArtTable 不转发该插槽
             （内部把 ElTable 的空态写死成「暂无数据」），写进去会被静默丢弃。

             ⚠ 列全部写在 ArtTable 的默认插槽里（ArtTable 支持 el-table 的插槽）：
             展开行（type="expand"）的内容必须是模板，列配置的 formatter 表达不了
             带按钮与菜单的面板；写在这一处，列顺序也由模板顺序直接决定。 -->
        <ArtTable v-if="showTable" :loading="listLoading" :data="projects" row-key="name">
          <!-- 展开区 = spec §11.5 的项目视图：头部是项目级按钮，下面是网元行。 -->
          <ElTableColumn type="expand">
            <template #default="{ row }">
              <div class="proj-view">
                <div class="proj-view__head">
                  <span class="proj-view__name">{{ row.name }}</span>
                  <span class="proj-view__meta">
                    {{ projectStateText(row.state) }} · {{ containerCountText(row) }}
                  </span>
                  <span v-if="projectBlockedConclusion(row)" class="proj-view__blocked">
                    {{ projectBlockedConclusion(row) }}
                  </span>
                  <span class="proj-view__spacer"></span>
                  <!-- 5b 工作台入口：项目是主机作用域的，host 必带（工作台据此还原同一台机器）；
                       路由 authMark 是 docker:inspect，无该权限的人连入口都不渲染（不渲染 ≠ 禁用）。 -->
                  <ElButton
                    v-if="canInspect"
                    size="small"
                    type="primary"
                    plain
                    @click="openWorkspace(row)"
                  >
                    打开工作台
                  </ElButton>
                  <ElButton
                    v-if="canManage"
                    size="small"
                    :disabled="projectActionDisabled(row)"
                    @click="onProjectAction(row, 'compose:up')"
                  >
                    Up -d
                  </ElButton>
                  <ElButton
                    v-if="canManage"
                    size="small"
                    :disabled="projectActionDisabled(row)"
                    @click="onProjectAction(row, 'compose:stop')"
                  >
                    停止
                  </ElButton>
                  <ElButton
                    v-if="canManage"
                    size="small"
                    :disabled="projectActionDisabled(row)"
                    @click="onProjectAction(row, 'compose:start')"
                  >
                    启动
                  </ElButton>
                  <ElButton
                    v-if="canManage"
                    size="small"
                    :disabled="projectActionDisabled(row)"
                    @click="onProjectAction(row, 'compose:restart')"
                  >
                    重启
                  </ElButton>
                  <ElButton
                    v-if="canManage"
                    size="small"
                    :disabled="projectActionDisabled(row)"
                    @click="onProjectAction(row, 'compose:pull')"
                  >
                    拉取
                  </ElButton>
                  <ElButton
                    v-if="canDelete"
                    size="small"
                    type="danger"
                    plain
                    :disabled="projectActionDisabled(row)"
                    @click="onProjectAction(row, 'compose:down')"
                  >
                    Down…
                  </ElButton>
                </div>

                <!-- 网元行：启/停/重启按该服务的容器逐个发（协议没有服务级启停动作，
                     见下方网元级动作的说明）；⋯ 菜单里的扩缩容与仅删容器走
                     「项目名/服务名」形态的 target。 -->
                <div v-for="svc in servicesOf(row.name)" :key="svc.name" class="proj-svc">
                  <span class="proj-svc__name">{{ svc.name }}</span>
                  <span class="proj-svc__meta">容器 {{ svc.running }}/{{ svc.total }}</span>
                  <span v-if="svc.protected" class="proj-svc__lock" title="受保护">🔒</span>
                  <span v-if="serviceBlockedConclusion(svc)" class="proj-svc__blocked">
                    {{ serviceBlockedConclusion(svc) }}
                  </span>
                  <span class="proj-svc__spacer"></span>
                  <ElButton
                    v-if="canManage"
                    size="small"
                    :disabled="serviceActionDisabled(svc)"
                    @click="onServiceToggle(row, svc)"
                  >
                    {{ svc.running > 0 ? '停止' : '启动' }}
                  </ElButton>
                  <ElButton
                    v-if="canManage"
                    size="small"
                    :disabled="serviceActionDisabled(svc)"
                    @click="onServiceRestart(row, svc)"
                  >
                    ⟳ 重启
                  </ElButton>
                  <DockerActionMenu
                    :actions="serviceMenuActions"
                    :target="`${row.name}/${svc.name}`"
                    :protected="svc.protected"
                    :pending-id="pendingId"
                    :disabled="svcLoopRunning || busy"
                    :extra-items="serviceExtraItems(svc)"
                    @select="onServiceMenuSelect(row, svc, $event)"
                  />
                </div>

                <div v-if="canInspect || canConfig" class="proj-view__config">
                  <span class="proj-view__config-label">配置</span>
                  <span class="proj-view__config-file" :title="row.configFiles?.[0]">
                    {{ configFileText(row) }}
                  </span>
                  <ElButton v-if="canConfig" size="small" text @click="openEditor(row, true)">
                    ＋添加服务
                  </ElButton>
                  <ElButton v-if="canConfig" size="small" text @click="openEditor(row, false)">
                    编辑
                  </ElButton>
                  <BackupHistory
                    :backups="backupsOf(row.name)"
                    :loaded="backupLoaded(row.name)"
                    :loading="backupLoading(row.name)"
                    :current-hash="backupHash(row.name)"
                    :current-content="backupContent(row.name)"
                    :disabled="busy || loading"
                    :can-rollback="canConfig"
                    :rolling-back="busy"
                    @load="loadBackups(row.name)"
                    @rollback="askRollback(row, $event)"
                  />
                </div>

                <div v-if="servicesOf(row.name).length === 0" class="proj-view__note">
                  该项目的网元当前没有容器，暂不在此列出
                </div>
                <div v-else-if="missingServiceCount(row) > 0" class="proj-view__note">
                  另有 {{ missingServiceCount(row) }} 个网元当前没有容器，未在此列出
                </div>
              </div>
            </template>
          </ElTableColumn>

          <ElTableColumn type="index" width="60" label="序号" />
          <ElTableColumn prop="name" label="项目名" min-width="180" show-overflow-tooltip />
          <ElTableColumn label="状态" width="110">
            <template #default="{ row }">{{ projectStateText(row.state) }}</template>
          </ElTableColumn>
          <ElTableColumn prop="services" label="网元数" width="90" />
          <ElTableColumn prop="containersCount" label="容器数" width="90" />
          <ElTableColumn label="配置文件" min-width="240">
            <template #default="{ row }">
              <!-- 列里放 basename（路径常很长），全路径挂在 title 上供悬浮核对；
                   旧版 compose 不上报路径时把「未知」的原因一并说出来，而不是留一个空格子。 -->
              <span :title="row.configFiles?.[0]">{{ configFileText(row) }}</span>
            </template>
          </ElTableColumn>
          <ElTableColumn label="保护" width="180">
            <template #default="{ row }">{{ protectionText(row) }}</template>
          </ElTableColumn>
          <ElTableColumn label="操作" width="150" fixed="right">
            <template #default="{ row }">
              <!-- 一期唯一的项目级只读动作就是「看配置」；没有该权限时不渲染按钮，
                   空操作列会让人以为「有东西没加载出来」（与容器/镜像页同一纪律）。
                   四期「编辑配置」受 docker:config 门控（页面渲染即受权限门控）。 -->
              <ArtButtonTable
                v-if="hasAuth(PermDockerInspect)"
                type="view"
                title="查看配置"
                @click="viewComposeFile(row)"
              />
              <ArtButtonTable
                v-if="canConfig"
                type="edit"
                title="编辑配置"
                @click="openEditor(row, false)"
              />
            </template>
          </ElTableColumn>
        </ArtTable>
        <!-- host-context.ts 的 reload 注释承诺：清单拉不到时页面显示「没有可管理的主机」 -->
        <ElEmpty
          v-else-if="!ctx.hosts.length"
          class="docker-empty"
          description="没有可管理的主机"
        />
        <ElEmpty v-else class="docker-empty" description="该主机上还没有项目" />
      </template>
    </DockerPage>

    <!-- 配置查看器：一期**只读**（无编辑入口 —— 编辑是四期能力，spec §11.0）。
         失败原因直接显示服务端/agent 给的结论句：这里再包一层「操作失败」只会把
         「设备离线」「路径没记录」「文件被删」说成同一句话。 -->
    <ElDialog
      v-model="configDialog.visible"
      :title="`配置 · ${configDialog.project}`"
      width="760px"
    >
      <div v-loading="configDialog.loading" class="docker-yml">
        <ElAlert
          v-if="configDialog.error"
          type="warning"
          :title="configDialog.error"
          :closable="false"
        />
        <pre v-else class="docker-yml__body">{{ configDialog.content }}</pre>
      </div>
      <template #footer>
        <ElButton @click="configDialog.visible = false">关闭</ElButton>
      </template>
    </ElDialog>

    <!-- 项目级动作的确认弹窗：Up/Down/停止是「输入项目名」强档，启/停/重启/拉取在
         受保护目标上也必须经它（「强制操作」开关是它的输入面）。
         「回收孤儿容器」与「同时删除数据卷」两个补充输入由本页作为插槽内容给出。 -->
    <DockerActionConfirm
      v-model="projConfirm.visible"
      :action="projConfirm.action"
      :target="projConfirm.target"
      :target-protected="projConfirm.protected"
      target-kind="项目"
      :loading="confirmLoading"
      @confirm="onProjectConfirm"
    >
      <ElCheckbox
        v-if="projConfirm.action === 'compose:up'"
        v-model="upRemoveOrphans"
        class="proj-opt"
      >
        回收孤儿容器
      </ElCheckbox>
      <ElCheckbox
        v-else-if="projConfirm.action === 'compose:down'"
        v-model="downVolumes"
        class="proj-opt"
      >
        同时删除数据卷
      </ElCheckbox>
    </DockerActionConfirm>

    <!-- 网元级动作的确认弹窗：仅删容器（照抄服务名强档）、缩容到 0（照抄服务名），
         以及受保护服务上「按容器逐个发」的启/停/重启（只为收「强制操作」勾选）。 -->
    <DockerActionConfirm
      v-model="svcConfirm.visible"
      :action="svcConfirm.action"
      :target="svcConfirm.target"
      :options="svcConfirm.options"
      :target-protected="svcConfirm.protected"
      target-kind="服务"
      :loading="confirmLoading"
      @confirm="onServiceConfirm"
    />

    <!-- 四期配置编辑器：双模式编辑 + 保存收尾 + 独立「应用」；载入/保存/回滚都在它里面。 -->
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
  import { computed, ref } from 'vue'
  import { useRouter } from 'vue-router'
  import {
    ElAlert,
    ElButton,
    ElCheckbox,
    ElDialog,
    ElEmpty,
    ElMessage,
    ElMessageBox,
    ElTableColumn
  } from 'element-plus'
  import { useAuth } from '@/hooks/core/useAuth'
  import {
    PermDockerConfig,
    PermDockerDelete,
    PermDockerExec,
    PermDockerInspect,
    PermDockerManage
  } from '@/enums/permission'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtTable from '@/components/core/tables/art-table/index.vue'
  import ArtTableHeader from '@/components/core/tables/art-table-header/index.vue'
  import DockerActionConfirm from '../components/action-confirm.vue'
  import DockerActionMenu from '../components/action-menu.vue'
  import DockerPage from '../components/docker-page.vue'
  import ComposeEditor from '../components/compose-editor/compose-editor.vue'
  import BackupHistory from '../components/compose-editor/backup-history.vue'
  import type { DockerContainerItem, DockerProjectItem } from '../api'
  import { runErrorMessage, useDockerCmds } from '../composables/useDockerCmds'
  import { useDockerHostState } from '../composables/useDockerHostState'
  import { actionGuarded, confirmKind, protectedGate } from '../utils/actions'
  import type { ConfirmOverride } from '../utils/confirm'
  import {
    COMPOSE_ACTIONS,
    formatBackupTime,
    parseComposeFilePayload,
    type ComposeBackup
  } from '../utils/compose'
  import { provideDockerHost } from '../utils/host-context'

  // 主机上下文是**页面级** provide/inject：DockerPage 与 HostSwitcher 都用 useDockerHost() 取它，
  // 而模块里没有别的 provide 调用方 —— 页面就是这一层的提供者，故在这里 provide 并直接用其返回值。
  // 不要解构：上下文字段是 getter，解构会把 hostId 定格成进入页面时的 ''（主机清单尚未到达）。
  const ctx = provideDockerHost()
  const { hasAuth } = useAuth()
  const router = useRouter()

  const canManage = computed(() => hasAuth(PermDockerManage))
  const canDelete = computed(() => hasAuth(PermDockerDelete))
  const canExec = computed(() => hasAuth(PermDockerExec))
  const canInspect = computed(() => hasAuth(PermDockerInspect))
  /** 四期配置编辑的权限门：编辑/添加服务/回滚三个入口都由它决定渲染。 */
  const canConfig = computed(() => hasAuth(PermDockerConfig))

  /** 配置对话框的状态（一个对象而不是四个 ref：它们总是同时被写，分开只会漏更新）。 */
  const configDialog = ref({
    visible: false,
    project: '',
    loading: false,
    content: '',
    error: '',
    hash: ''
  })

  // ── 四期：配置编辑器、备份历史与回滚（spec §8/§9/§11.5）──

  const editor = ref({ visible: false, project: '', addService: false, protected: false })

  /** 打开编辑器（「编辑配置」/「＋添加服务」两个入口共用；权限门在模板上）。 */
  function openEditor(project: DockerProjectItem, addService: boolean): void {
    editor.value = {
      visible: true,
      project: project.name,
      addService,
      protected: project.protected === true
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
    void loadBackups(editor.value.project, true)
    void refresh()
  }

  interface ProjectBackups {
    loading: boolean
    backups: ComposeBackup[]
    hash: string
    /** 当前文件正文（备份详情里对照着看；备份正文协议上不可读）。 */
    content: string
  }

  /** 项目名 → 备份历史（懒加载：点「历史备份」才发 read，不在展开行时自动请求）。 */
  const backupState = ref<Record<string, ProjectBackups>>({})

  function backupsOf(name: string): ComposeBackup[] {
    return backupState.value[name]?.backups ?? []
  }

  function backupLoaded(name: string): boolean {
    return backupState.value[name] !== undefined
  }

  function backupLoading(name: string): boolean {
    return backupState.value[name]?.loading === true
  }

  function backupHash(name: string): string {
    return backupState.value[name]?.hash ?? ''
  }

  function backupContent(name: string): string {
    return backupState.value[name]?.content ?? ''
  }

  async function loadBackups(name: string, force = false): Promise<void> {
    const cur = backupState.value[name]
    if (cur?.loading) return
    if (cur && !force) return
    backupState.value = {
      ...backupState.value,
      [name]: {
        loading: true,
        backups: cur?.backups ?? [],
        hash: cur?.hash ?? '',
        content: cur?.content ?? ''
      }
    }
    const res = await run({
      action: COMPOSE_ACTIONS.read,
      target: name,
      key: `backups:${name}`
    })
    if (!res.ok) {
      ElMessage.error(runErrorMessage(res, '读取备份列表失败'))
      // 读失败不留下「已加载（0 份）」的假象：清掉条目，入口回到可重试的按钮。
      const rest = { ...backupState.value }
      delete rest[name]
      backupState.value = rest
      return
    }
    const view = parseComposeFilePayload(res.payload)
    backupState.value = {
      ...backupState.value,
      [name]: { loading: false, backups: view.backups, hash: view.hash, content: view.content }
    }
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

  /** 选了一版备份 → 强确认（照抄项目名）→ write 的 backup 模式（乐观锁基线随行）。 */
  function askRollback(project: DockerProjectItem, backup: ComposeBackup): void {
    rollbackConfirm.value = {
      visible: true,
      action: COMPOSE_ACTIONS.write,
      project: project.name,
      backup,
      hash: backupHash(project.name),
      override: {
        kind: 'target-word',
        label: '回滚配置',
        danger: 'danger',
        conclusion: `将把「${project.name}」的配置回滚到 ${formatBackupTime(backup.at)} 的版本；当前内容会先备份。`,
        expected: project.name
      }
    }
  }

  async function onRollbackConfirm(payload: { confirm: string; force: boolean }): Promise<void> {
    const st = rollbackConfirm.value
    if (!st.backup) return
    confirmLoading.value = true
    try {
      const res = await runWrite(
        st.action,
        st.project,
        { backup: st.backup.token, baseHash: st.hash },
        payload.confirm
      )
      // 回滚成功：备份列表与基线 hash 都变了，立即重拉（文件内容已由服务端回读校验）。
      if (res.ok) await loadBackups(st.project, true)
    } finally {
      confirmLoading.value = false
      rollbackConfirm.value = { ...st, visible: false, backup: null }
    }
  }

  // ── 写操作的确认状态（先声明：主机切换时要一并收掉它们）──

  interface ProjectConfirmState {
    visible: boolean
    action: string
    target: string
    /** 目标是否受保护（快照里 agent 算好的结论，前端不重复判断）。 */
    protected: boolean
  }

  const projConfirm = ref<ProjectConfirmState>({
    visible: false,
    action: '',
    target: '',
    protected: false
  })
  /** Up -d 的「回收孤儿容器」：勾选后请求带 removeOrphans=true。 */
  const upRemoveOrphans = ref(false)
  /** Down 的「同时删除数据卷」：勾选后请求带 volumes=true。 */
  const downVolumes = ref(false)

  interface ServiceConfirmState {
    visible: boolean
    action: string
    target: string
    options: Record<string, unknown>
    protected: boolean
    /** 非空 = 提交后按容器逐个执行（服务行启/停/重启没有协议级动作）。 */
    loop?: { action: string; targets: DockerContainerItem[] }
  }

  const svcConfirm = ref<ServiceConfirmState>({
    visible: false,
    action: '',
    target: '',
    options: {},
    protected: false
  })

  const confirmLoading = ref(false)
  /** 服务行的容器循环是否在途（全局 busy 之外的整行禁用信号）。 */
  const svcLoopRunning = ref(false)

  // 快照与四态收口在 composable（hosts 清单、seq 守卫、主机切换后的重拉都在它里面）。
  // 主机切换 = 换一台机器：关掉全部对话框（它们属于上一台主机）并重新拉快照。
  const {
    state,
    loading,
    listLoading,
    stale,
    ageSeconds,
    neverReported,
    loadError,
    hasState,
    refresh
  } = useDockerHostState({
    onHostSwitch: () => {
      configDialog.value.visible = false
      projConfirm.value.visible = false
      svcConfirm.value.visible = false
      upRemoveOrphans.value = false
      downVolumes.value = false
      // 四期状态也属于上一台主机：编辑器/回滚弹窗收起，备份缓存清空（换机后重拉）。
      editor.value = { visible: false, project: '', addService: false, protected: false }
      rollbackConfirm.value = { ...rollbackConfirm.value, visible: false, backup: null }
      backupState.value = {}
    }
  })

  // 写指令通道：受理 + 轮询 + 成功后重拉（重拉就是上面的 refresh）。「查看配置」也走它，
  // 受理/轮询/结论句的处置与写动作同一套（展示行为不变：结果落进配置对话框）。
  const { run, pendingId, busy } = useDockerCmds({ refresh })

  /** 项目清单不经筛选（一期不在项目页做搜索），故直接用快照里的顺序。 */
  const projects = computed(() => state.value?.projects ?? [])

  /** 有行就渲染表格；纯加载中也用表格的 loading 遮罩，空态只在加载结束后判断。 */
  const showTable = computed(() => listLoading.value || projects.value.length > 0)

  // ── 网元（服务）行的归纳 ──
  //
  // 快照里没有服务清单：项目条目只带数量（services），服务名只在容器的
  // composeProject/composeService 标签上。故服务行从容器的这两个标签归纳，
  // 「容器 N/M」也由同一份容器算出来 —— 不额外发明判断。

  interface ProjectServiceRow {
    name: string
    /** 运行中的容器数 / 容器总数。 */
    running: number
    total: number
    /**
     * 服务级保护由**该服务下容器的 protected** 承载（项目条目的 protected 只到
     * `project:<名>` 粒度）。取不到容器就不显示锁，不自己发明判断。
     */
    protected: boolean
    containers: DockerContainerItem[]
  }

  /** 项目名 → 该项目的容器（按 composeProject 标签归纳，裸容器不进本页）。 */
  const projectContainers = computed(() => {
    const map = new Map<string, DockerContainerItem[]>()
    for (const c of state.value?.containers ?? []) {
      if (!c.composeProject) continue
      const list = map.get(c.composeProject)
      if (list) list.push(c)
      else map.set(c.composeProject, [c])
    }
    return map
  })

  /** 项目名 → 网元行（按 composeService 归纳，按名字排序保证展开区的顺序稳定）。 */
  const servicesByProject = computed(() => {
    const map = new Map<string, ProjectServiceRow[]>()
    for (const c of state.value?.containers ?? []) {
      if (!c.composeProject || !c.composeService) continue
      let list = map.get(c.composeProject)
      if (!list) {
        list = []
        map.set(c.composeProject, list)
      }
      let row = list.find((s) => s.name === c.composeService)
      if (!row) {
        row = { name: c.composeService, running: 0, total: 0, protected: false, containers: [] }
        list.push(row)
      }
      row.containers.push(c)
      row.total += 1
      if (c.state === 'running') row.running += 1
      if (c.protected) row.protected = true
    }
    for (const list of map.values()) list.sort((a, b) => a.name.localeCompare(b.name))
    return map
  })

  function servicesOf(projectName: string): ProjectServiceRow[] {
    return servicesByProject.value.get(projectName) ?? []
  }

  /** 项目有网元没有容器（缩到 0 等）：快照里没有它的名字，只能如实说明「未列出」。 */
  function missingServiceCount(project: DockerProjectItem): number {
    return Math.max(0, (project.services ?? 0) - servicesOf(project.name).length)
  }

  // ── 展示口径（与一期同一批文案）──

  /** 项目状态：后端由成员容器运行态归纳出 running/partial/stopped 三态（未知给「—」）。 */
  function projectStateText(stateValue?: string): string {
    if (stateValue === 'running') return '运行中'
    if (stateValue === 'partial') return '部分运行'
    if (stateValue === 'stopped') return '已停止'
    return '—'
  }

  /** 取路径的 basename（列里只放文件名，全路径放悬浮）。 */
  function baseName(path: string): string {
    const parts = path.split('/')
    return parts[parts.length - 1] || path
  }

  function configFileText(project: DockerProjectItem): string {
    const full = project.configFiles?.[0]
    return full ? baseName(full) : '未知（旧版 compose 未记录）'
  }

  /** 展开区头部的容器计数（与网元行同源：都从容器的状态归纳）。 */
  function containerCountText(project: DockerProjectItem): string {
    const list = projectContainers.value.get(project.name) ?? []
    const running = list.filter((c) => c.state === 'running').length
    return `容器 ${running}/${list.length}`
  }

  /** 项目保护：agent 算好的 `project:<名>` 结论；没有强制权限时按钮禁用并给结论句。 */
  function projectGate(project: DockerProjectItem) {
    return protectedGate({ protected: project.protected }, canExec.value)
  }

  function protectionText(project: DockerProjectItem): string {
    const gate = projectGate(project)
    if (!gate.protected) return '—'
    return gate.allowed ? '🔒 受保护' : `🔒 ${gate.conclusion}`
  }

  function projectBlockedConclusion(project: DockerProjectItem): string {
    const gate = projectGate(project)
    return gate.allowed ? '' : gate.conclusion
  }

  function projectActionDisabled(project: DockerProjectItem): boolean {
    // 指令在途整组禁用（所有动作共享同一台主机与同一条指令通道），保护档不允许时也禁用。
    return busy.value || !projectGate(project).allowed
  }

  /** 服务保护：服务行的 protected 来自该服务下容器（见 ProjectServiceRow）。 */
  function serviceGate(svc: ProjectServiceRow) {
    return protectedGate({ protected: svc.protected }, canExec.value)
  }

  function serviceBlockedConclusion(svc: ProjectServiceRow): string {
    const gate = serviceGate(svc)
    return gate.allowed ? '' : gate.conclusion
  }

  function serviceActionDisabled(svc: ProjectServiceRow): boolean {
    return busy.value || svcLoopRunning.value || !serviceGate(svc).allowed
  }

  // ── 结果回执 ──

  /** 发一条写指令并把结论给用户；成功后的重拉在 composable 里（立即 + 落定各一次）。 */
  async function runWrite(
    action: string,
    target?: string,
    options?: Record<string, unknown>,
    confirm?: string
  ) {
    const res = await run({ action, target, options, confirm })
    if (res.ok) ElMessage.success(res.detail || '操作已完成')
    else ElMessage.error(runErrorMessage(res, '操作未完成'))
    return res
  }

  // ── 项目级动作（展开区头部按钮）──

  /**
   * 5b 工作台入口：本切片只加入口，列表页其余行为不动。host 随行（工作台是
   * 主机作用域的页面，返回列表时也带回同一台机器）。
   */
  function openWorkspace(project: DockerProjectItem) {
    void router.push({
      name: 'DockerProjectWorkspace',
      params: { name: project.name },
      query: { host: ctx.hostId }
    })
  }

  /**
   * 项目级动作入口：注册表标了确认档的（Up/停止/Down 是输入项目名强档）走弹窗；
   * 受保护目标上的受保护档动作也必须走弹窗（「强制操作」开关是它唯一的输入面），
   * 其余（启动/重启/拉取）点了直接发。
   */
  function onProjectAction(project: DockerProjectItem, action: string) {
    const needsForceInput = project.protected === true && actionGuarded(action)
    if (confirmKind(action) === 'none' && !needsForceInput) {
      void runWrite(action, project.name)
      return
    }
    // 每次打开都是新的一次确认：补充勾选项回到默认（取消后重开不沿用上一次的选择）。
    upRemoveOrphans.value = false
    downVolumes.value = false
    projConfirm.value = {
      visible: true,
      action,
      target: project.name,
      protected: project.protected === true
    }
  }

  /** 确认弹窗提交：force 只在「强制操作」开关出现且被勾选时为 true。 */
  async function onProjectConfirm(payload: { confirm: string; force: boolean }) {
    const st = projConfirm.value
    const options: Record<string, unknown> = {}
    if (st.action === 'compose:up' && upRemoveOrphans.value) options.removeOrphans = true
    if (st.action === 'compose:down' && downVolumes.value) options.volumes = true
    if (payload.force) options.force = true
    confirmLoading.value = true
    try {
      await runWrite(st.action, st.target, options, payload.confirm)
    } finally {
      confirmLoading.value = false
      upRemoveOrphans.value = false
      downVolumes.value = false
      projConfirm.value = { ...st, visible: false }
    }
  }

  // ── 网元级动作 ──

  /**
   * 网元行的启/停/重启：协议没有服务级启停动作（只有「仅删容器」与扩缩容接受
   * 「项目/服务」形态的 target），故按该服务的
   * 容器逐个发 container:start/stop/restart —— 语义等同「启停这个网元」，
   * 且全部是注册表内的既有动作（不会因 target 形态被服务端拒绝）。
   * 服务行 ⋯ 菜单里的扩缩容与仅删容器仍走「项目名/服务名」形态的 target。
   */
  const serviceMenuActions = ['compose.service:remove-containers', 'compose.service:scale']

  /** 服务行 ⋯ 菜单的「重启」：不是注册表动作（按容器逐个发），权限与注册表同码。 */
  function serviceExtraItems(svc: ProjectServiceRow) {
    const gate = serviceGate(svc)
    return [
      {
        key: 'service:restart',
        label: gate.protected && !gate.allowed ? '🔒 重启（需要更高权限）' : '重启',
        icon: 'ri:refresh-line',
        auth: PermDockerManage,
        disabled: serviceActionDisabled(svc)
      }
    ]
  }

  function onServiceToggle(project: DockerProjectItem, svc: ProjectServiceRow) {
    const action = svc.running > 0 ? 'container:stop' : 'container:start'
    const targets = svc.containers.filter((c) =>
      action === 'container:stop' ? c.state === 'running' : c.state !== 'running'
    )
    runServiceContainers(project, svc, action, targets)
  }

  function onServiceRestart(project: DockerProjectItem, svc: ProjectServiceRow) {
    runServiceContainers(project, svc, 'container:restart', [...svc.containers])
  }

  /** 受保护 + 有强制权限：先弹窗收「强制操作」勾选，勾选后整个循环都带 force。 */
  function runServiceContainers(
    project: DockerProjectItem,
    svc: ProjectServiceRow,
    action: string,
    targets: DockerContainerItem[]
  ) {
    const gate = serviceGate(svc)
    if (!gate.allowed) return
    if (gate.needForce) {
      svcConfirm.value = {
        visible: true,
        action,
        target: `${project.name}/${svc.name}`,
        options: {},
        protected: true,
        loop: { action, targets }
      }
      return
    }
    void runContainerLoop(action, targets, false)
  }

  function onServiceMenuSelect(
    project: DockerProjectItem,
    svc: ProjectServiceRow,
    item: { action: string }
  ) {
    switch (item.action) {
      case 'service:restart':
        onServiceRestart(project, svc)
        break
      case 'compose.service:remove-containers':
        // 仅删容器：照抄服务名强档，永远经确认弹窗。
        svcConfirm.value = {
          visible: true,
          action: item.action,
          target: `${project.name}/${svc.name}`,
          options: {},
          protected: svc.protected === true
        }
        break
      case 'compose.service:scale':
        void askScale(project, svc)
        break
    }
  }

  /**
   * 扩缩容：先用一段输入收实例数（0 = 缩到零，是合法且高危的值），
   * n>0 可逆不弹确认；n=0 走「照抄服务名」强档；受保护服务上的任何 n 都要带 force。
   */
  async function askScale(project: DockerProjectItem, svc: ProjectServiceRow) {
    let n: number
    try {
      const { value } = await ElMessageBox.prompt(
        `为网元「${svc.name}」设置要运行的实例数（0 表示停止并移除它的容器）。`,
        '扩缩容',
        {
          confirmButtonText: '确定',
          cancelButtonText: '取消',
          inputPattern: /^\d+$/,
          inputErrorMessage: '请输入 0 或正整数'
        }
      )
      n = Number((value ?? '').trim())
    } catch {
      // 取消/关闭输入框：中止，不发送指令。
      return
    }
    const target = `${project.name}/${svc.name}`
    const needsForceInput = svc.protected === true && actionGuarded('compose.service:scale')
    if (confirmKind('compose.service:scale', { n }) === 'none' && !needsForceInput) {
      await runWrite('compose.service:scale', target, { n })
      return
    }
    svcConfirm.value = {
      visible: true,
      action: 'compose.service:scale',
      target,
      options: { n },
      protected: svc.protected === true
    }
  }

  /** 网元确认弹窗提交：loop 分支按容器逐个发（force 已勾选），其余就是一条服务级指令。 */
  async function onServiceConfirm(payload: { confirm: string; force: boolean }) {
    const st = svcConfirm.value
    confirmLoading.value = true
    try {
      if (st.loop) {
        await runContainerLoop(st.loop.action, st.loop.targets, payload.force)
      } else {
        const options = { ...st.options }
        if (payload.force) options.force = true
        await runWrite(st.action, st.target, options, payload.confirm)
      }
    } finally {
      confirmLoading.value = false
      svcConfirm.value = { ...st, visible: false, loop: undefined }
    }
  }

  interface LoopFailure {
    name: string
    message: string
  }

  /**
   * 按容器逐个发一条指令（服务行的启/停/重启）。
   *
   * 逐条 await、单条失败不中断：「3 项成功、1 项失败」是可解释的结果，中途放弃会让
   * 剩下的项处于说不清的状态（与容器页批量的纪律一致）。未带 force 时受保护的容器
   * **不发送**：没有强制勾选时发出去必被拒，直接按保护档的结论句计入失败。
   */
  async function runContainerLoop(action: string, targets: DockerContainerItem[], force: boolean) {
    if (targets.length === 0) {
      ElMessage.info('该网元没有需要处理的容器')
      return
    }
    // 批量跨主机是**同名不同机**的错误操作：主机一换，剩余项就不再发送。
    const hostAtStart = ctx.hostId
    svcLoopRunning.value = true
    let done = 0
    const failures: LoopFailure[] = []
    try {
      for (const c of targets) {
        if (ctx.hostId !== hostAtStart) {
          failures.push({ name: c.name, message: '主机已切换，未执行' })
          continue
        }
        if (!force) {
          const gate = protectedGate({ protected: c.protected }, canExec.value)
          if (gate.protected) {
            failures.push({ name: c.name, message: gate.conclusion })
            continue
          }
        }
        const res = await run({
          action,
          target: c.name,
          options: force ? { force: true } : undefined,
          key: c.name
        })
        if (res.ok) done += 1
        else failures.push({ name: c.name, message: runErrorMessage(res, '操作未完成') })
      }
    } finally {
      svcLoopRunning.value = false
    }
    const message = loopSummary(done, failures)
    if (failures.length > 0) ElMessage({ type: 'warning', message, duration: 5000 })
    else ElMessage.success(message)
  }

  /** 结论句汇总：成功数 + 失败数 + 前三条失败原因（再多也读不完，余下只计数）。 */
  function loopSummary(done: number, failures: LoopFailure[]): string {
    const head = `已执行 ${done} 项`
    if (failures.length === 0) return head
    const shown = failures.slice(0, 3).map((f) => `${f.name}（${f.message}）`)
    if (failures.length > shown.length) shown.push(`另有 ${failures.length - shown.length} 项`)
    return `${head}，${failures.length} 项失败：${shown.join('；')}`
  }

  // ── 查看配置（一期只读流程，受理/轮询改用同一 composable）──

  /** 「查看配置」：受理 → 轮询 → 展示；结论句与写操作同一口径（服务端给的优先）。 */
  async function viewComposeFile(project: DockerProjectItem) {
    configDialog.value = {
      visible: true,
      project: project.name,
      loading: true,
      content: '',
      error: '',
      hash: ''
    }
    const res = await run({
      action: 'compose.file:read',
      target: project.name,
      key: `read:${project.name}`
    })
    if (res.ok) {
      const p = res.payload as { content?: string; hash?: string } | undefined
      configDialog.value.content = p?.content ?? ''
      configDialog.value.hash = p?.hash ?? ''
      if (!configDialog.value.content) configDialog.value.error = '未取到配置文件内容'
    } else {
      // 失败原因由服务端/agent 给的**结论句**承载（不再包一层「操作失败」）。
      configDialog.value.error = runErrorMessage(res, '读取配置文件失败')
    }
    configDialog.value.loading = false
  }
</script>

<style lang="scss" scoped>
  // 空态渲染在本页（ArtTable 不转发 `#empty`）：给它接近表格空态的留白。
  .docker-empty {
    padding: 56px 0;
  }

  .docker-hint {
    margin-bottom: 12px;
    font-size: 12px;
    color: var(--el-text-color-secondary);
  }

  // 项目视图（展开区）：头部一行放项目级按钮，下面逐个网元一行。
  .proj-view {
    padding: 4px 0 8px;

    &__head {
      display: flex;
      flex-wrap: wrap;
      gap: 8px;
      align-items: center;
      margin-bottom: 8px;
    }

    &__name {
      font-size: 14px;
      font-weight: 600;
    }

    &__meta {
      color: var(--el-text-color-secondary);
      font-size: 13px;
    }

    // 保护档结论句：与陈旧标注同一套颜色语言（琥珀色 = 需要注意的结论）。
    &__blocked {
      color: var(--el-color-warning);
      font-size: 12px;
    }

    &__spacer {
      flex: 1 1 auto;
    }

    // 配置行（spec §11.5）：文件名 + 编辑/添加服务/历史备份入口。
    &__config {
      display: flex;
      flex-wrap: wrap;
      gap: 8px;
      align-items: center;
      padding: 4px 0 2px;
      border-top: 1px dashed var(--el-border-color-lighter);

      &-label {
        color: var(--el-text-color-secondary);
        font-size: 12px;
      }

      &-file {
        font-family: var(--art-font-family-mono, monospace);
        font-size: 12px;
      }
    }

    &__note {
      margin-top: 6px;
      color: var(--el-text-color-secondary);
      font-size: 12px;
    }
  }

  // 网元行：与展开区头部同一套排布，缩进一级（它是项目的成员）。
  .proj-svc {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;
    padding: 6px 0 6px 16px;
    border-top: 1px dashed var(--el-border-color-lighter);

    &__name {
      font-size: 13px;
    }

    &__meta {
      color: var(--el-text-color-secondary);
      font-size: 12px;
    }

    &__lock {
      font-size: 13px;
    }

    &__blocked {
      color: var(--el-color-warning);
      font-size: 12px;
    }

    &__spacer {
      flex: 1 1 auto;
    }
  }

  // 确认弹窗里的补充勾选项：与上面的结论行拉开一点距离。
  .proj-opt {
    display: flex;
    margin-top: 10px;
  }

  // 配置文本：等宽字体 + 保留原始换行；长行折行（yml 里可能有很长的 environment）。
  .docker-yml {
    max-height: 60vh;
    overflow: auto;

    &__body {
      margin: 0;
      font-family: var(--art-font-family-mono, monospace);
      font-size: 12px;
      line-height: 1.6;
      white-space: pre-wrap;
      word-break: break-all;
      color: var(--el-text-color-primary);
    }
  }
</style>
