<template>
  <!-- 单根（single-root 守卫：布局的 Transition 只支持单根，双根切页白屏）。 -->
  <div class="pws">
    <!-- 网元卡片区：每服务一卡 —— 头部是服务级动作，卡体是它的容器行（点击进容器详情）。
         空态与缺口照列表页口径分开说：没有容器（一条都没有）≠ 有网元没容器。 -->
    <div class="pws__grid">
      <div v-for="svc in services" :key="svc.name" class="pws-card">
        <div class="pws-card__head">
          <span class="pws-card__name" :title="svc.name">{{ svc.name }}</span>
          <span class="pws-card__replicas">副本 {{ svc.running }}/{{ svc.total }}</span>
          <!-- 锁语义与列表保护列同款（锁图标 + 受保护）：不用 🔒 emoji ——
               无 emoji 字体的环境里会渲染成豆腐块；锁走 ArtSvgIcon 的图标范式。 -->
          <span v-if="svc.protected" class="pws-card__lock">
            <ArtSvgIcon icon="ri:lock-2-line" />
            受保护
          </span>
          <span v-if="blockedConclusionOf(svc)" class="pws-card__blocked">
            {{ blockedConclusionOf(svc) }}
          </span>
          <span class="pws-card__spacer"></span>
          <ElButton
            v-if="canManage"
            size="small"
            :disabled="actionDisabledOf(svc)"
            @click="onServiceToggle(svc)"
          >
            {{ svc.running > 0 ? '停止' : '启动' }}
          </ElButton>
          <ElButton
            v-if="canManage"
            size="small"
            :disabled="actionDisabledOf(svc)"
            @click="onServiceRestart(svc)"
          >
            ⟳ 重启
          </ElButton>
          <DockerActionMenu
            :actions="serviceMenuActions"
            :target="`${project}/${svc.name}`"
            :protected="svc.protected"
            :pending-id="pendingId"
            :disabled="svcLoopRunning || busy"
            :extra-items="serviceExtraItems(svc)"
            @select="onServiceMenuSelect(svc, $event)"
          />
        </div>

        <div class="pws-card__body">
          <!-- 容器行即「进容器详情」的入口：语义上是一个动作，用 button（键盘可达、
               悬浮/聚焦态不另造）。状态点颜色即结论，旁边永远有文字结论。 -->
          <button
            v-for="c in svc.containers"
            :key="c.id"
            type="button"
            class="pws-container"
            :title="`查看容器 ${c.name} 的详情`"
            @click="openContainer(c)"
          >
            <span
              class="pws-container__dot"
              :class="c.state === 'running' ? 'is-run' : 'is-stop'"
              aria-hidden="true"
            />
            <span class="pws-container__name">{{ c.name }}</span>
            <span class="pws-container__state">{{ c.statusText || c.state }}</span>
          </button>
          <div v-if="svc.containers.length === 0" class="pws-card__note"> 该网元当前没有容器 </div>
        </div>
      </div>
    </div>

    <div v-if="services.length === 0" class="pws__note">该项目的网元当前没有容器，暂不在此列出</div>
    <div v-else-if="missingCount > 0" class="pws__note">
      另有 {{ missingCount }} 个网元当前没有容器，未在此列出
    </div>

    <!-- 网元级动作的确认弹窗：仅删容器（照抄服务名强档）、缩容到 0（照抄服务名），
         以及受保护服务上「按容器逐个发」的启/停/重启（只为收「强制操作」勾选）。
         全部从 projects.vue 平移，语义零改动。 -->
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
  </div>
</template>

<script setup lang="ts">
  /**
   * 服务卡片区（5b 工作台）：把 projects.vue 展开区里的网元行升格为卡片 ——
   * 头部承载服务级动作（照列表页的服务级语义），卡体承载该服务的容器行。
   *
   * 动作语义全部**平移自 projects.vue，零改动**：
   *   - 启/停/重启按该服务的容器逐个发（协议没有服务级启停动作）；
   *   - ⋯ 菜单里的扩缩容与仅删容器走「项目名/服务名」形态的 target；
   *   - 受保护服务先弹窗收「强制操作」勾选，勾选后整个循环都带 force；
   *   - 逐条 await、单条失败不中断（批量跨主机是错误操作，主机一换就不再发送）。
   * 指令通道（run/busy/pendingId）由父页传入 —— 全页共享一条通道与一个 busy，
   * 与列表页「同一 composable 服务全部动作」的结构等价。
   */
  import { computed, ref } from 'vue'
  import { useRouter } from 'vue-router'
  import { ElButton, ElMessage, ElMessageBox } from 'element-plus'
  import { useAuth } from '@/hooks/core/useAuth'
  import { PermDockerExec, PermDockerManage } from '@/enums/permission'
  import ArtSvgIcon from '@/components/core/base/art-svg-icon/index.vue'
  import DockerActionConfirm from '../action-confirm.vue'
  import DockerActionMenu from '../action-menu.vue'
  import type { DockerContainerItem } from '../../api'
  import {
    runErrorMessage,
    type DockerCmdInput,
    type DockerCmdRunResult
  } from '../../composables/useDockerCmds'
  import { actionGuarded, confirmKind, protectedGate } from '../../utils/actions'
  import { useDockerHost } from '../../utils/host-context'
  import type { ProjectServiceRow } from '../../utils/projects'

  defineOptions({ name: 'DockerProjectServices' })

  const props = defineProps<{
    /** 项目名（「项目/服务」形态 target 的前半段）。 */
    project: string
    services: ProjectServiceRow[]
    /** 有网元没有容器（缩到 0 等）的缺口数（快照的项目条目给的 services 数减归纳出的行数）。 */
    missingCount: number
    /** 父页的指令通道（全页共享，见文件头）。 */
    run: (input: DockerCmdInput) => Promise<DockerCmdRunResult>
    /** 全局在途判断（父页 composable 的 busy：项目级/服务级指令共享）。 */
    busy: boolean
    /** 行级 pending 的键（父页 composable 的 pendingId）。 */
    pendingId: string | null
  }>()

  const router = useRouter()
  // 子组件注入页面级主机上下文是合法路径（页面是提供者）；同组件 provide 后自用才需要
  // 显式传参（见 host-context 文件头）。runContainerLoop 的「主机一换就不再发送」用它。
  const ctx = useDockerHost()
  const { hasAuth } = useAuth()

  const canManage = computed(() => hasAuth(PermDockerManage))
  const canExec = computed(() => hasAuth(PermDockerExec))

  // ── 服务保护档（平移：服务行的 protected 来自该服务下容器）──

  function serviceGate(svc: ProjectServiceRow) {
    return protectedGate({ protected: svc.protected }, canExec.value)
  }

  function blockedConclusionOf(svc: ProjectServiceRow): string {
    return serviceGate(svc).allowed ? '' : serviceGate(svc).conclusion
  }

  function actionDisabledOf(svc: ProjectServiceRow): boolean {
    return props.busy || svcLoopRunning.value || !serviceGate(svc).allowed
  }

  // ── 服务级动作（平移自 projects.vue，语义零改动）──────────────────────

  /** 服务行 ⋯ 菜单：仅删容器与扩缩容（「项目名/服务名」形态的 target）。 */
  const serviceMenuActions = ['compose.service:remove-containers', 'compose.service:scale']

  /** 服务行 ⋯ 菜单的「重启」：不是注册表动作（按容器逐个发），权限与注册表同码。 */
  function serviceExtraItems(svc: ProjectServiceRow) {
    const gate = serviceGate(svc)
    return [
      {
        key: 'service:restart',
        label: gate.protected && !gate.allowed ? '重启（需要更高权限）' : '重启',
        icon: 'ri:refresh-line',
        auth: PermDockerManage,
        disabled: actionDisabledOf(svc)
      }
    ]
  }

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
  /** 服务行的容器循环是否在途（全局 busy 之外的整卡禁用信号）。 */
  const svcLoopRunning = ref(false)

  function onServiceToggle(svc: ProjectServiceRow) {
    const action = svc.running > 0 ? 'container:stop' : 'container:start'
    const targets = svc.containers.filter((c) =>
      action === 'container:stop' ? c.state === 'running' : c.state !== 'running'
    )
    runServiceContainers(svc, action, targets)
  }

  function onServiceRestart(svc: ProjectServiceRow) {
    runServiceContainers(svc, 'container:restart', [...svc.containers])
  }

  /** 受保护 + 有强制权限：先弹窗收「强制操作」勾选，勾选后整个循环都带 force。 */
  function runServiceContainers(
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
        target: `${props.project}/${svc.name}`,
        options: {},
        protected: true,
        loop: { action, targets }
      }
      return
    }
    void runContainerLoop(action, targets, false)
  }

  function onServiceMenuSelect(svc: ProjectServiceRow, item: { action: string }) {
    switch (item.action) {
      case 'service:restart':
        onServiceRestart(svc)
        break
      case 'compose.service:remove-containers':
        // 仅删容器：照抄服务名强档，永远经确认弹窗。
        svcConfirm.value = {
          visible: true,
          action: item.action,
          target: `${props.project}/${svc.name}`,
          options: {},
          protected: svc.protected === true
        }
        break
      case 'compose.service:scale':
        void askScale(svc)
        break
    }
  }

  /**
   * 扩缩容：先用一段输入收实例数（0 = 缩到零，是合法且高危的值），
   * n>0 可逆不弹确认；n=0 走「照抄服务名」强档；受保护服务上的任何 n 都要带 force。
   */
  async function askScale(svc: ProjectServiceRow) {
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
    const target = `${props.project}/${svc.name}`
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

  /** 发一条写指令并把结论给用户（平移：成功后的重拉在 composable 里）。 */
  async function runWrite(
    action: string,
    target?: string,
    options?: Record<string, unknown>,
    confirm?: string
  ) {
    const res = await props.run({ action, target, options, confirm })
    if (res.ok) ElMessage.success(res.detail || '操作已完成')
    else ElMessage.error(runErrorMessage(res, '操作未完成'))
    return res
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
        const res = await props.run({
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

  // ── 容器行 → 容器详情页（8a：深链指 /docker/containers/:id，host 随行）──

  function openContainer(c: DockerContainerItem) {
    void router.push({
      path: `/docker/containers/${c.id}`,
      query: { host: ctx.hostId }
    })
  }
</script>

<style lang="scss" scoped>
  @use '@styles/core/breakpoints.scss' as *;
  @use '../../views/overview-tokens' as t;

  // 「停止 / 启动 / ⟳ 重启」等默认档按钮的主色文字对比度 AA：病灶与处方见
  // overview-tokens 的 primary-text-aa（终审 QA D2·浅色实测 3.68:1）。
  @include t.primary-text-aa;

  // 卡片网格：一列起步（手机横屏），平板两列，宽屏三列 —— 卡片是同质单元，
  // 栅格随宽度扩张；容器行多的卡不会被拉伸成不等高（align-items 起点对齐）。
  .pws__grid {
    display: grid;
    grid-template-columns: 1fr;
    gap: 12px;
    align-items: start;

    @media (width >= 768px) {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }

    @media (width >= 1440px) {
      grid-template-columns: repeat(3, minmax(0, 1fr));
    }
  }

  .pws__note {
    color: var(--el-text-color-secondary);
    font-size: 12px;
  }

  .pws-card {
    @include t.card;
    @include t.rise;

    padding: 12px;

    .dark & {
      @include t.card-dark;
    }
  }

  .pws-card__head {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;
  }

  .pws-card__name {
    font-size: 14px;
    font-weight: 600;
    word-break: break-all;
  }

  .pws-card__replicas {
    color: var(--el-text-color-secondary);
    font-size: 12px;
    font-variant-numeric: tabular-nums;
  }

  .pws-card__lock {
    display: inline-flex;
    gap: 4px;
    align-items: center;
    font-size: 13px;
  }

  // 保护档结论句：与陈旧标注同一套颜色语言（琥珀色 = 需要注意的结论）。
  .pws-card__blocked {
    color: var(--el-color-warning);
    font-size: 12px;
  }

  .pws-card__spacer {
    flex: 1 1 auto;
  }

  .pws-card__body {
    margin-top: 8px;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }

  .pws-card__note {
    padding: 6px 0;
    color: var(--el-text-color-secondary);
    font-size: 12px;
  }

  // 容器行：整行是「进容器详情」的动作（button），视觉上是表行 ——
  // 悬浮用 EP 的 fill 色阶，不另造颜色。
  .pws-container {
    display: flex;
    gap: 8px;
    align-items: center;
    width: 100%;
    padding: 4px 8px;
    border: none;
    border-radius: 6px;
    background: transparent;
    color: var(--el-text-color-regular);
    font-size: 12px;
    text-align: left;
    cursor: pointer;
    transition: background-color 0.15s ease;

    &:hover,
    &:focus-visible {
      background: var(--el-fill-color-light);
    }
  }

  .pws-container__dot {
    display: inline-block;
    width: 6px;
    height: 6px;
    flex: none;
    border-radius: 50%;

    &.is-run {
      background: var(--el-color-success);
    }

    &.is-stop {
      background: var(--el-text-color-disabled);
    }
  }

  .pws-container__name {
    min-width: 0;
    word-break: break-all;
  }

  .pws-container__state {
    margin-left: auto;
    flex: none;
    color: var(--el-text-color-secondary);
    word-break: break-all;
  }
</style>
