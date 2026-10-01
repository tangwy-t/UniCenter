<template>
  <!-- 单根（single-root 守卫扫全模块的 .vue）：ElDrawer 是唯一根；append-to-body
       与 workload-drawer / create-container-drawer 同挂法（页面里没有嵌套层级冲突）。 -->
  <ElDrawer
    v-model="visibleModel"
    class="tcd-drawer"
    direction="rtl"
    append-to-body
    :size="drawerSize"
  >
    <template #header>
      <div class="tcd-head">
        <h3 class="tcd-head__title">任务中心</h3>
        <p class="tcd-head__sub">最近受理的指令 · 跨主机 · 最多 100 条 · 打开期间每 5 秒自动刷新</p>
        <div class="tcd-head__tools">
          <!-- 状态过滤（服务端过滤）：全部 / 进行中（pending）/ 已结束（一切终态）。 -->
          <ElRadioGroup v-model="filter" size="small">
            <ElRadioButton value="all">全部</ElRadioButton>
            <ElRadioButton value="pending">进行中</ElRadioButton>
            <ElRadioButton value="done">已结束</ElRadioButton>
          </ElRadioGroup>
          <ElButton size="small" :loading="loading" title="立即刷新" @click="manualRefresh">
            刷新
          </ElButton>
        </div>
      </div>
    </template>

    <!-- ── 首拉加载（还没有任何行可给） ── -->
    <ElSkeleton v-if="state === 'loading'" :rows="6" animated />

    <!-- ── 首拉失败：结论句 + 重试（不显示旧列表 —— 那时还没有旧列表可显示） ── -->
    <div v-else-if="state === 'error'" class="tcd-state">
      <p class="tcd-state__text">任务列表获取失败</p>
      <ElButton size="small" @click="manualRefresh">重试</ElButton>
    </div>

    <!-- ── 空态（「没有任务」与「筛完了没有」分开说，两种事实不是一句话） ── -->
    <ElEmpty v-else-if="state === 'empty'" :description="emptyText" />

    <!-- ── 正常态：轮询失败标注 + 任务行列表 ── -->
    <template v-else>
      <!-- 静默刷新失败：保留最后已知列表并说出口（与列表页「上次数据仍在」同一口径）。 -->
      <p v-if="refreshError" class="tcd-refresh-error">刷新失败，正在显示上次结果</p>
      <ul class="tcd-list">
        <li v-for="item in items" :key="item.ref" class="tcd-item">
          <!-- 状态点：pending = 旋转动画（在执行）；succeeded = 绿；failed/timeout = 红；
               未知终态照实给中性点 + 原文 —— 服务端加了新状态时页面不冒充它认识。 -->
          <span class="tcd-item__dot" :class="dotClass(item.status)">
            <ArtSvgIcon
              v-if="statusKind(item.status) === 'pending'"
              icon="ri:loader-4-line"
              class="tcd-spin"
            />
          </span>
          <div class="tcd-item__main">
            <div class="tcd-item__line">
              <!-- 动作：注册表 label（页面只说人话）；查不到的（只读指令/终端/编辑类，
                   本就不在二期写注册表里）原样等宽显示 —— 等宽本身就是「这是原始码」的提示。 -->
              <span class="tcd-item__action" :class="{ 'is-raw': !isKnownAction(item) }">
                {{ actionLabel(item) }}
              </span>
              <span class="tcd-item__target" :title="item.target">{{ item.target }}</span>
              <button
                v-if="isExpandable(item) || isExpanded(item.ref)"
                type="button"
                class="tcd-item__toggle"
                :title="isExpanded(item.ref) ? '收起' : '展开'"
                @click="toggleExpand(item)"
              >
                <ArtSvgIcon
                  :icon="isExpanded(item.ref) ? 'ri:arrow-up-s-line' : 'ri:arrow-down-s-line'"
                />
              </button>
            </div>
            <!-- 元信息行：状态词 + 归属主机（设备已删时回退 hostId）+ 发起人 + 相对时间。 -->
            <div class="tcd-item__meta">
              <span class="tcd-item__status" :class="`is-${statusKind(item.status)}`">
                {{ statusText(item.status) }}
              </span>
              <span class="tcd-item__host">@ {{ item.hostname || item.hostId }}</span>
              <span class="tcd-item__user">{{ item.username || '—' }}</span>
              <span class="tcd-item__time">{{
                eventRelativeTime(item.createdAt / 1000, nowSec)
              }}</span>
            </div>
            <!-- 展开区：进行中的拉取接实时进度流（收起即取消，语义见子组件）；终态行给
                 结论句原文（失败/超时的排障线索；成功拉取收尾时同样留在原地）。 -->
            <div v-if="isExpanded(item.ref)" class="tcd-item__detail">
              <TaskPullProgress
                v-if="item.status === 'pending' && item.action === 'image:pull'"
                :key="item.ref"
                :host-id="item.hostId"
                :cmd-ref="item.ref"
              />
              <p v-else-if="item.summary" class="tcd-item__summary">{{ item.summary }}</p>
            </div>
          </div>
        </li>
      </ul>
    </template>
  </ElDrawer>
</template>

<script setup lang="ts">
  /**
   * 任务中心抽屉（6b 前端半边）：长任务（拉取/启停/重建…）的跨主机收口面板 ——
   * 「刚才受理了什么、进行到哪、结论是什么」一张列表读完，不再靠各页弹窗转圈。
   *
   * 数据是**一份**（GET /docker/tasks：≤100 条、受理时刻降序、跨主机），不与任何
   * 单主机页面状态耦合 —— 这也是入口虽然长在主机条/总览 hero 上、列表却不按当前
   * 主机过滤的原因：hostname 列已经回答「在哪台机器上」，再按入口主机收窄会把
   * 「跨主机收口」这个立身之本又交回去。
   *
   * 刷新纪律（对齐列表页自动刷新）：打开即拉、打开期间 5 秒一轮（后台标签页跳过
   * tick）、手动刷新随时可插队（seq 守卫防旧响应晚到覆盖新数据）；关抽屉/keep-alive
   * 失活停表，重新打开/激活回来补拉。**进度流不在此列**：展开中的拉取进度流在抽屉
   * 关闭时保持连接（断开 = 取消拉取，监控窗口关掉不等于放弃拉取 —— 详见
   * task-pull-progress 的生命周期取舍），停表停的是这份列表的轮询开销。
   *
   * 状态映射是本地纯函数（statusKind/statusText/dotClass）：后端照实投影四种终态
   * （pending/succeeded/failed/timeout；取消语义在结论句里，如「拉取已取消」——
   * 后端 6b 盘点的裁决），前端不发明状态、也不把 timeout 冒充 failed。
   */
  import { computed, onActivated, onBeforeUnmount, onDeactivated, ref, watch } from 'vue'
  import {
    ElButton,
    ElDrawer,
    ElEmpty,
    ElRadioButton,
    ElRadioGroup,
    ElSkeleton
  } from 'element-plus'
  import { useAppBreakpoints } from '@/hooks/core/useAppBreakpoints'
  import { fetchDockerTasks, type DockerTaskItem } from '../api'
  import { lookupDockerAction } from '../utils/actions'
  import { eventRelativeTime } from '../utils/events'
  import TaskPullProgress from './task-pull-progress.vue'

  defineOptions({ name: 'DockerTaskCenterDrawer' })

  const props = defineProps<{
    modelValue: boolean
  }>()

  const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void }>()

  const visibleModel = computed({
    get: () => props.modelValue,
    set: (v: boolean) => emit('update:modelValue', v)
  })

  // 窄屏（平板竖屏以下）抽屉占满宽度 —— 与 create-container-drawer 同一条断点口径。
  const { smaller } = useAppBreakpoints()
  const drawerSize = computed(() => (smaller('tablet').value ? '100%' : '560px'))

  // ── 数据与四态（首拉 loading/error、空、ready；静默刷新失败保留旧列表） ──

  const items = ref<DockerTaskItem[]>([])
  const loading = ref(false)
  /** 首拉失败（还没有任何行可给）：错误态 + 重试。 */
  const firstError = ref(false)
  /** 其后静默刷新失败：保留最后已知列表，页头说出口。 */
  const refreshError = ref(false)
  /** 是否成功拉到过一次（「首拉」与「静默刷新」的分界）。 */
  const hasLoaded = ref(false)
  /** 请求序号：手动刷新、轮询、筛选变化并发时，旧响应晚到不得覆盖新数据。 */
  let loadSeq = 0

  /** 相对时间的「现在」：随每次成功拉取前移（列表 5 秒一轮，时间文案跟着走即可，
      不为它单开 1 秒心跳 —— 事件流的 1 秒节流服务的是实时滚动，这里是静态受理时刻）。 */
  const nowSec = ref(Math.floor(Date.now() / 1000))

  const filter = ref<'all' | 'pending' | 'done'>('all')

  const state = computed<'loading' | 'error' | 'empty' | 'ready'>(() => {
    if (firstError.value) return 'error'
    if (loading.value && !hasLoaded.value) return 'loading'
    if (hasLoaded.value && items.value.length === 0) return 'empty'
    return 'ready'
  })

  /** 空态文案：与筛选分开说（「没有任务」与「没有进行中的任务」是两种事实）。 */
  const emptyText = computed(() =>
    filter.value === 'pending' ? '没有进行中的任务' : '还没有受理过任何任务'
  )

  /**
   * 拉一次任务列表（筛选作为 query 全部透传，服务端过滤 —— 与容器列表页同一条
   * 纪律：前端不做本地过滤，筛选条件与请求一一对应）。
   */
  async function load(silent = false) {
    const seq = ++loadSeq
    if (!silent) loading.value = true
    try {
      const resp = await fetchDockerTasks(filter.value === 'all' ? {} : { status: filter.value })
      if (seq !== loadSeq) return
      items.value = resp.items ?? []
      hasLoaded.value = true
      firstError.value = false
      refreshError.value = false
      nowSec.value = Math.floor(Date.now() / 1000)
    } catch {
      if (seq !== loadSeq) return
      if (hasLoaded.value) {
        // 已有列表：静默失败保留旧数据 + 标注（不整页切错误态 —— 抽屉开着，把刚看完
        // 的列表整个换成错误屏反而丢上下文）。
        refreshError.value = true
      } else {
        firstError.value = true
      }
    } finally {
      if (seq === loadSeq) loading.value = false
    }
  }

  function manualRefresh() {
    void load()
  }

  // ── 5 秒轮询（打开期间；关抽屉/失活停表） ──────────────────
  const POLL_MS = 5000
  let timer: number | undefined

  function stopPoll() {
    if (timer !== undefined) {
      clearInterval(timer)
      timer = undefined
    }
  }

  function restartPoll() {
    stopPoll()
    if (!props.modelValue) return
    // 后台标签页的 setInterval 会被浏览器限流：隐藏期间跳过 tick（与总览自动刷新
    // 同一纪律），回到前台后最多一轮之内追上。
    timer = window.setInterval(() => {
      if (!document.hidden) void load(true)
    }, POLL_MS)
  }

  watch(
    () => props.modelValue,
    (v, old) => {
      if (v) {
        // 打开即拉（旧数据超过 5 秒也是旧数据）+ 起表。
        void load()
        restartPoll()
      } else if (old) {
        // 关抽屉只停轮询表：展开中的进度流保持连接（断开即取消拉取 —— 关监控窗口
        // 不等于放弃拉取；再打开时列表重拉，行还在就还在看）。
        stopPoll()
      }
    },
    { immediate: true }
  )

  // 筛选变化即重拉（服务端过滤没有本地回退，不重拉列表就不会变）。
  watch(filter, () => {
    if (props.modelValue) void load()
  })

  // keep-alive 失活停表（页面被 worktab 缓存时不轮询）；激活回来补拉 —— 后台期间
  // 浏览器限流了 interval，切回来的列表已是旧的。首次激活（= 挂载）不算「回来」：
  // watch 的 immediate 已经拉过一次，这里再拉就是同屏双请求（KeepAlive 内挂载时
  // activated 与 mounted 同时走，见 events-feed 的 start 幂等注释 —— 这里用标记闸）。
  let activatedOnce = false

  onDeactivated(stopPoll)

  onActivated(() => {
    const first = !activatedOnce
    activatedOnce = true
    if (first) {
      restartPoll() // 幂等：表已在走就重建一次（watch 已起过表），不加请求
      return
    }
    if (props.modelValue) {
      restartPoll()
      void load(true)
    }
  })

  onBeforeUnmount(stopPoll)

  // ── 行渲染的映射（纯函数，测试经组件渲染断言钉住） ──────────

  type StatusKind = 'pending' | 'ok' | 'fail' | 'unknown'

  function statusKind(status: string): StatusKind {
    switch (status) {
      case 'pending':
        return 'pending'
      case 'succeeded':
        return 'ok'
      case 'failed':
      case 'timeout':
        return 'fail'
      default:
        return 'unknown'
    }
  }

  function statusText(status: string): string {
    switch (status) {
      case 'pending':
        return '进行中'
      case 'succeeded':
        return '成功'
      case 'failed':
        return '失败'
      case 'timeout':
        return '超时'
      default:
        return status || '—'
    }
  }

  /** 状态点 class（pending 用旋转图标，见模板；颜色即结论：绿 = 成功、红 = 失败/超时）。 */
  function dotClass(status: string): string {
    switch (statusKind(status)) {
      case 'pending':
        return 'is-pending'
      case 'ok':
        return 'is-ok'
      case 'fail':
        return 'is-fail'
      default:
        return 'is-unknown'
    }
  }

  /** 动作的展示名：注册表 label 优先，查不到原样显示（调用处加等宽 class）。 */
  function actionLabel(item: DockerTaskItem): string {
    return lookupDockerAction(item.action)?.label ?? item.action
  }

  function isKnownAction(item: DockerTaskItem): boolean {
    return lookupDockerAction(item.action) !== undefined
  }

  /**
   * 可展开的两类行：进行中的拉取（接实时进度流，后端 6b 契约「前端凭 action 判断
   * 可流」）；失败/超时且有结论句（排障线索原文）。已展开的行即使转入终态也保留
   * 展开态（收尾结论就地接上，用户不用再点一次），收起按钮随之常驻。
   */
  function isExpandable(item: DockerTaskItem): boolean {
    if (item.status === 'pending' && item.action === 'image:pull') return true
    return (item.status === 'failed' || item.status === 'timeout') && item.summary !== ''
  }

  const expandedRefs = ref<string[]>([])

  function isExpanded(ref: string): boolean {
    return expandedRefs.value.includes(ref)
  }

  function toggleExpand(item: DockerTaskItem) {
    const i = expandedRefs.value.indexOf(item.ref)
    if (i >= 0) expandedRefs.value.splice(i, 1)
    else expandedRefs.value.push(item.ref)
  }

  // 重新拉列表后，从窗口里消失的行带着展开态一起走（连带的进度流子组件卸载即断流
  // —— 100 条窗口内进行中的拉取被挤出是极端情况，见 task-pull-progress 的取舍注释）。
</script>

<style lang="scss" scoped>
  @use '@styles/core/breakpoints.scss' as *;

  // 头部：标题 + 说明 + 过滤/刷新工具行（ElDrawer 的 header 插槽，右侧给 EP 自带
  // 的关闭按钮让位）。
  .tcd-head {
    min-width: 0;
    padding-right: 24px;

    &__title {
      margin: 0;
      font-size: 16px;
      font-weight: 600;
    }

    &__sub {
      margin: 4px 0 0;
      color: var(--el-text-color-secondary);
      font-size: 12px;
      line-height: 1.6;
    }

    &__tools {
      display: flex;
      flex-wrap: wrap;
      gap: 8px;
      align-items: center;
      margin-top: 10px;
    }
  }

  // 静默刷新失败标注：琥珀即「需要注意」（与模块内陈旧/离线标注同一套颜色语言）。
  .tcd-refresh-error {
    margin: 0 0 8px;
    color: var(--el-color-warning);
    font-size: 12px;
    line-height: 1.6;
  }

  // 首拉失败的错误态：结论句 + 重试，居中弱化（抽屉还开着，结论就地给）。
  .tcd-state {
    display: flex;
    flex-direction: column;
    gap: 10px;
    align-items: center;
    padding: 32px 0;

    &__text {
      margin: 0;
      color: var(--el-text-color-regular);
      font-size: 13px;
    }
  }

  // 任务行列表：行间以分隔线分组（无分页 —— 窗口就是「最近 100 条」这一屏）。
  .tcd-list {
    margin: 0;
    padding: 0;
    list-style: none;
  }

  .tcd-item {
    display: flex;
    gap: 10px;
    padding: 10px 0;

    & + & {
      border-top: 1px solid var(--el-border-color-lighter);
    }

    // 状态点：pending 是旋转图标（8px 圆点放不下动画的表意），其余是 8px 圆点。
    &__dot {
      display: flex;
      flex: none;
      width: 16px;
      height: 16px;
      align-items: center;
      justify-content: center;
      margin-top: 2px;

      &.is-ok::after,
      &.is-fail::after,
      &.is-unknown::after {
        display: block;
        width: 8px;
        height: 8px;
        border-radius: 50%;
        content: '';
      }

      &.is-ok::after {
        background: var(--el-color-success);
      }

      &.is-fail::after {
        background: var(--el-color-danger);
      }

      // 未知终态：中性点 —— 服务端加了新状态时页面不冒充认识（原文显示在元信息行）。
      &.is-unknown::after {
        background: var(--el-text-color-placeholder);
      }
    }

    &__main {
      flex: 1;
      min-width: 0;
    }

    &__line {
      display: flex;
      gap: 10px;
      align-items: center;
    }

    &__action {
      flex: none;
      color: var(--el-text-color-primary);
      font-size: 13px;

      // 注册表查不到的 action：原样等宽显示（等宽即「这是原始码」的提示）。
      &.is-raw {
        font-family: var(
          --el-font-family-mono,
          ui-monospace,
          'SFMono-Regular',
          Consolas,
          monospace
        );
        word-break: break-all;
      }
    }

    // 目标：等宽 + 截断（悬停看全名 —— 完整引用是排障钥匙，不给截断丢掉）。
    &__target {
      flex: 1;
      min-width: 0;
      overflow: hidden;
      color: var(--el-text-color-regular);
      font-size: 13px;
      font-family: var(--el-font-family-mono, ui-monospace, 'SFMono-Regular', Consolas, monospace);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    // 展开开关：与行同高的小热区（图标按钮），可展开/已展开才出现。
    &__toggle {
      display: flex;
      flex: none;
      width: 24px;
      height: 24px;
      align-items: center;
      justify-content: center;
      padding: 0;
      border: none;
      background: none;
      color: var(--el-text-color-secondary);
      cursor: pointer;

      &:hover {
        color: var(--el-color-primary);
      }
    }

    // 元信息行：状态词带语义色（与状态点同色系），其余次要色。
    &__meta {
      display: flex;
      flex-wrap: wrap;
      gap: 10px;
      margin-top: 2px;
      font-size: 12px;
      line-height: 1.6;
    }

    &__status {
      color: var(--el-text-color-secondary);

      &.is-ok {
        color: var(--el-color-success);
      }

      &.is-fail {
        color: var(--el-color-danger);
      }

      // pending 保持次要色：旋转图标已经在说「在动」，文字再上色只会喧宾夺主。
    }

    &__host,
    &__user {
      color: var(--el-text-color-secondary);
    }

    // 相对时间：右对齐到行尾（受理时刻是次要事实，不与目标抢视觉）。
    &__time {
      margin-left: auto;
      color: var(--el-text-color-placeholder);
    }

    // 终态结论句：展开区的非流形态（失败/超时的排障线索；收尾的拉取也落在这里）。
    &__summary {
      margin: 6px 0 0;
      color: var(--el-text-color-regular);
      font-size: 12px;
      line-height: 1.6;
      word-break: break-all;
    }
  }

  // pending 的旋转动画：与总览/列表页加载态同一「转圈 = 在途」的表意。
  .tcd-spin {
    animation: tcd-rotate 1.2s linear infinite;
  }

  @keyframes tcd-rotate {
    from {
      transform: rotate(0deg);
    }

    to {
      transform: rotate(360deg);
    }
  }

  // prefers-reduced-motion：旋转降级为静态图标（动效不参与信息表达时的兜底）。
  @media (prefers-reduced-motion: reduce) {
    .tcd-spin {
      animation: none;
    }
  }

  // 手机宽度（抽屉占满）：元信息行允许换行堆叠（已在 flex-wrap 内），展开开关
  // 热区不变 —— 触屏下它是最小可达目标。
</style>
