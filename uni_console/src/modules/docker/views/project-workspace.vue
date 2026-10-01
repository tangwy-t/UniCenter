<template>
  <!-- 单根（single-root 守卫：布局的 Transition 只支持单根，双根切页白屏）。 -->
  <div class="project-workspace art-full-height overflow-y-auto">
    <div class="pw-inner p-4 pb-8 md:p-5">
      <!-- ============ 首屏：主机/快照还没到 ============
           ⚠ 骨架只认「从未拿到过快照」：一旦握有快照（hasState），刷新期间的 loading
           不再把页面切回骨架 —— 那会把四个分区整体卸载再重挂，配置区的「就位即拉备份」
           immediate watch 会随每次重挂重放（loadBackups → run → refresh → loading 又
           翻转 → 再重挂），形成无限重挂循环（与 D-1 的「保留最后已知数据」同一条纪律：
           刷新期间显示旧数据，而不是闪回骨架）。 -->
      <div v-if="!ctx.hostId || (listLoading && !hasState)" class="pw-card pw-state">
        <ElSkeleton :rows="6" animated />
      </div>

      <!-- ============ 没有可管主机 / 主机 Docker 不可用 ============ -->
      <ElEmpty v-else-if="!ctx.hosts.length" class="pw-empty" description="没有可管理的主机" />
      <ElEmpty
        v-else-if="ctx.host && !ctx.host.dockerOk"
        :description="ctx.host.error || '该主机 Docker 不可用'"
      >
        <ElButton size="small" @click="refresh">重新检测</ElButton>
      </ElEmpty>

      <!-- ============ 首拉失败（D-1：失败不清数据、也不冒充空清单）============ -->
      <ElEmpty v-else-if="loadError && !hasState" description="数据获取失败">
        <ElButton size="small" @click="refresh">重试</ElButton>
      </ElEmpty>

      <!-- ============ 项目不在快照里（名字错/已下线/换了主机）：给结论与回路 ============ -->
      <ElEmpty v-else-if="!project" :description="`该主机上没有项目「${projectName}」`">
        <ElButton size="small" @click="back">返回项目列表</ElButton>
      </ElEmpty>

      <!-- ============ 正常态：hero → 网元卡片区 → 聚合日志 → 配置 ============ -->
      <template v-else>
        <ProjectHero
          :project="project"
          :containers-text="containersText"
          :backup-count="backupCount"
          :missing-count="missingCount"
          :action-disabled="actionDisabled"
          :blocked-conclusion="blockedConclusion"
          :sync-text="syncText"
          :sync-warn="stale"
          :offline="ctx.host != null && !ctx.host.online"
          :loading="loading"
          @action="onProjectAction"
          @refresh="refresh"
          @back="back"
        />

        <section class="pw-section">
          <div class="pw-section__head">
            <span class="pw-section__title">网元</span>
            <span class="pw-section__sub">
              每张卡是一个网元 · 启停/重启按该网元的容器逐个发 · 点击容器行进容器详情
            </span>
          </div>
          <ProjectServices
            :project="projectName"
            :services="serviceRows"
            :missing-count="missingCount"
            :run="run"
            :busy="busy"
            :pending-id="pendingId"
          />
        </section>

        <!-- 聚合日志：compose:logs 是只读流，权限与 container:logs 同档（docker:inspect）。 -->
        <section v-if="canInspect" class="pw-section">
          <div class="pw-section__head">
            <span class="pw-section__title">聚合日志</span>
            <span class="pw-section__sub">
              项目全部网元的日志合流 · 行首是网元名 · 可按网元过滤
            </span>
          </div>
          <div class="pw-card">
            <ProjectLogs :project="projectName" :services="serviceNames" :followable="canInspect" />
          </div>
        </section>

        <section v-if="canInspect || canConfig" class="pw-section">
          <div class="pw-section__head">
            <span class="pw-section__title">配置</span>
            <span class="pw-section__sub">
              表单/YML 双模式编辑 · 保存先校验与备份 · 应用是独立的动作 · 历史可回滚
            </span>
          </div>
          <div class="pw-card">
            <ProjectConfig
              :project="project"
              :run="run"
              :busy="busy"
              :loading="listLoading"
              :refresh="refresh"
              @backups-count="backupCount = $event"
            />
          </div>
        </section>
      </template>
    </div>

    <!-- 项目级动作的确认弹窗（平移自 projects.vue）：Up/Down/停止是「输入项目名」强档，
         启/停/重启/拉取在受保护目标上也必须经它（「强制操作」开关是它的输入面）。
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
        class="pw-opt"
      >
        回收孤儿容器
      </ElCheckbox>
      <ElCheckbox
        v-else-if="projConfirm.action === 'compose:down'"
        v-model="downVolumes"
        class="pw-opt"
      >
        同时删除数据卷
      </ElCheckbox>
    </DockerActionConfirm>
  </div>
</template>

<script setup lang="ts">
  /**
   * Compose 项目工作台（5b，`/docker/projects/:name`）。
   *
   * Dockge 的洞见嫁接：项目不只是列表里的一个资源类型，而是一个「变更工作台」——
   * 状态、动作、日志、配置在同一屏围着**这一个项目**组织。本页是纯编排：
   *   - hero（实体头 + 生命周期动作条 + 主机行）在 components/project-workspace/project-hero；
   *   - 网元卡片区（服务级动作 + 容器行入口）在 project-services；
   *   - 聚合日志（compose:logs 消费 + 服务过滤）在 project-logs；
   *   - 配置区（compose-editor 四件套宿主 + 备份/回滚）在 project-config。
   * 业务语义全部平移自 views/projects.vue（确认档/保护档/循环/备份/回滚零改动），
   * 列表页保留不动；服务行归纳的派生口径在 utils/projects.ts（两处同步改的提醒
   * 写在那份文件头）。
   *
   * 主机跟随（单主机详情页的既有纪律，先例出自被删的 container-detail）：
   * hostId 落定/切换 → 快照重拉（seq 守卫在
   * useDockerHostState），各子区自己 watch hostId 重置/断流；query host 是页面事实源
   * （项目是主机作用域的，入口链接都带它）。
   */
  import { computed, ref } from 'vue'
  import { useRoute, useRouter } from 'vue-router'
  import { ElButton, ElCheckbox, ElEmpty, ElMessage, ElSkeleton } from 'element-plus'
  import { useAuth } from '@/hooks/core/useAuth'
  import { PermDockerConfig, PermDockerExec, PermDockerInspect } from '@/enums/permission'
  import DockerActionConfirm from '../components/action-confirm.vue'
  import ProjectConfig from '../components/project-workspace/project-config.vue'
  import ProjectHero from '../components/project-workspace/project-hero.vue'
  import ProjectLogs from '../components/project-workspace/project-logs.vue'
  import ProjectServices from '../components/project-workspace/project-services.vue'
  import { runErrorMessage, useDockerCmds } from '../composables/useDockerCmds'
  import { useDockerHostState } from '../composables/useDockerHostState'
  import { actionGuarded, confirmKind, protectedGate } from '../utils/actions'
  import { syncTextWithError } from '../utils/host'
  import { deriveServiceRows, projectContainersOf } from '../utils/projects'
  import { provideDockerHost } from '../utils/host-context'

  defineOptions({ name: 'DockerProjectWorkspace' })

  const route = useRoute()
  const router = useRouter()
  // 主机上下文是**页面级** provide/inject（与列表页/详情页同一约定）：直接打开链接时
  // 主机从 query/清单还原。不要解构：上下文字段是 getter，解构会把 hostId 定格成
  // 进入页面时的 ''（主机清单尚未到达）。
  const ctx = provideDockerHost()
  const { hasAuth } = useAuth()

  const canInspect = computed(() => hasAuth(PermDockerInspect))
  const canExec = computed(() => hasAuth(PermDockerExec))
  const canConfig = computed(() => hasAuth(PermDockerConfig))

  /** 路由参数里的项目名（列表页入口传的就是快照里的项目名，同一口径）。 */
  const projectName = computed(() => String(route.params.name ?? ''))

  // 快照与四态收口在 composable（hosts 清单、seq 守卫、主机切换后的重拉都在它里面）。
  // 主机切换 = 换一台机器：收掉上一台主机的确认弹窗（它属于上一台机器）并重拉快照；
  // 配置区/日志区的自重置由它们各自 watch hostId 完成。
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
    host: ctx,
    onHostSwitch: () => {
      projConfirm.value.visible = false
      upRemoveOrphans.value = false
      downVolumes.value = false
    }
  })

  // 指令通道（全页共享一条：hero 的项目级动作、服务卡的循环、配置区的备份/回滚都走
  // 它 —— 与列表页「同一 composable 服务全部动作」结构等价）。
  const { run, pendingId, busy } = useDockerCmds({ host: ctx, refresh })

  // ── 派生（口径与 projects.vue 同源，实现收在 utils/projects.ts）──

  const project = computed(
    () => state.value?.projects.find((p) => p.name === projectName.value) ?? null
  )
  const containers = computed(() => state.value?.containers ?? [])
  const serviceRows = computed(() => deriveServiceRows(containers.value, projectName.value))
  const serviceNames = computed(() => serviceRows.value.map((s) => s.name))

  /** 展开区头部的容器计数（与网元卡同源：都从容器的状态归纳）。 */
  const containersText = computed(() => {
    const list = projectContainersOf(containers.value, projectName.value)
    const running = list.filter((c) => c.state === 'running').length
    return `容器 ${running}/${list.length}`
  })

  /** 项目有网元没有容器（缩到 0 等）：快照里没有它的名字，只能如实说「未列出」。 */
  const missingCount = computed(() =>
    project.value ? Math.max(0, (project.value.services ?? 0) - serviceRows.value.length) : 0
  )

  /** 备份数（配置区懒加载回传；未到时如实显示 0）。 */
  const backupCount = ref(0)

  const syncText = computed(() =>
    syncTextWithError(
      loadError.value,
      hasState.value,
      stale.value,
      ageSeconds.value,
      neverReported.value
    )
  )

  /** 项目保护：agent 算好的 `project:<名>` 结论；没有强制权限时按钮禁用并给结论句。 */
  const gate = computed(() =>
    project.value ? protectedGate({ protected: project.value.protected }, canExec.value) : null
  )
  const blockedConclusion = computed(() =>
    gate.value && !gate.value.allowed ? gate.value.conclusion : ''
  )
  /** hero 动作条的禁用：指令在途，或保护档不允许。 */
  const actionDisabled = computed(() => busy.value || (gate.value ? !gate.value.allowed : false))

  // ── 项目级动作（平移自 projects.vue，语义零改动）──

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
  const confirmLoading = ref(false)

  /**
   * 项目级动作入口：注册表标了确认档的（Up/停止/Down 是输入项目名强档）走弹窗；
   * 受保护目标上的受保护档动作也必须走弹窗（「强制操作」开关是它唯一的输入面），
   * 其余（启动/重启/拉取）点了直接发。
   */
  function onProjectAction(action: string) {
    const p = project.value
    if (!p) return
    const needsForceInput = p.protected === true && actionGuarded(action)
    if (confirmKind(action) === 'none' && !needsForceInput) {
      void runWrite(action, p.name)
      return
    }
    // 每次打开都是新的一次确认：补充勾选项回到默认（取消后重开不沿用上一次的选择）。
    upRemoveOrphans.value = false
    downVolumes.value = false
    projConfirm.value = {
      visible: true,
      action,
      target: p.name,
      protected: p.protected === true
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

  /** 返回项目列表：带上当前主机，列表页据此还原到同一台机器。 */
  function back() {
    void router.push({ name: 'DockerProjects', query: { host: ctx.hostId } })
  }
</script>

<style lang="scss" scoped>
  @use './overview-tokens' as t;

  @include t.rise-keyframes;

  .pw-inner {
    display: flex;
    flex-direction: column;
    gap: 16px;
  }

  .pw-card {
    @include t.card;
    @include t.rise;

    padding: 12px;

    .dark & {
      @include t.card-dark;
    }
  }

  // 首屏加载态（骨架）：给一块与正常卡片同形的留白，避免首屏跳动。
  .pw-state {
    padding: 20px;
  }

  .pw-empty {
    padding: 56px 0;
  }

  // 分区头：标题 + 一句说明（说清这一区「是什么、能做什么」，不写实现细节）。
  .pw-section__head {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: baseline;
    margin-bottom: 8px;
  }

  .pw-section__title {
    font-size: 14px;
    font-weight: 600;
  }

  .pw-section__sub {
    color: var(--el-text-color-secondary);
    font-size: 12px;
  }

  // 确认弹窗里的补充勾选项：与结论行拉开一点距离。
  .pw-opt {
    display: flex;
    margin-top: 10px;
  }
</style>
