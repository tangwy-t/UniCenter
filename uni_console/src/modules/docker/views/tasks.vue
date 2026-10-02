<template>
  <!-- 单根（single-root 守卫在库：布局的 Transition 只支持单根，双根切页白屏）。 -->
  <div class="docker-tasks art-full-height overflow-y-auto">
    <div class="tk-inner p-4 pb-8 md:p-5">
      <!-- ============ 页头：返回 + 图标 + 标题 + 过滤/刷新（对齐其余页面 hero）============ -->
      <div class="tk-hero mb-4 flex flex-wrap items-center gap-3">
        <ArtButtonTable
          icon="ri:arrow-left-line"
          icon-class="bg-g-300/55 text-g-700"
          title="返回上一页"
          @click="back"
        />
        <div class="wkl-hero__icon flex-cc">
          <ArtSvgIcon :icon="pageIcon" />
        </div>
        <div class="min-w-0">
          <h2 class="text-lg font-semibold text-[var(--el-text-color-primary)]">任务中心</h2>
          <p class="mt-0.5 flex flex-wrap items-center gap-1.5 text-xs text-g-600">
            <span
              class="live-dot inline-block h-1.5 w-1.5 rounded-full bg-success"
              :class="{ 'is-loading': loading }"
            />
            <span>{{ subtitle }}</span>
          </p>
        </div>
        <div class="ml-auto flex items-center gap-2">
          <!-- 状态过滤（服务端过滤）：全部 / 进行中（pending）/ 已结束（一切终态）。 -->
          <ElRadioGroup v-model="filter" size="small">
            <ElRadioButton value="all">全部</ElRadioButton>
            <ElRadioButton value="pending">进行中</ElRadioButton>
            <ElRadioButton value="done">已结束</ElRadioButton>
          </ElRadioGroup>
          <ArtButtonTable
            icon="ri:refresh-line"
            iconClass="bg-theme/12 text-theme"
            title="刷新"
            @click="manualRefresh"
          />
        </div>
      </div>

      <!-- ── 首拉加载（还没有任何行可给） ── -->
      <div v-if="state === 'loading'" class="wkl-card tk-state">
        <ElSkeleton :rows="6" animated />
      </div>

      <!-- ── 首拉失败：结论句 + 重试（不显示旧列表 —— 那时还没有旧列表可显示） ── -->
      <div v-else-if="state === 'error'" class="wkl-card tk-state">
        <p class="tk-state__text">任务列表获取失败</p>
        <ElButton size="small" @click="manualRefresh">重试</ElButton>
      </div>

      <!-- ── 空态（「没有任务」与「筛完了没有」分开说，两种事实不是一句话） ── -->
      <div v-else-if="state === 'empty'" class="wkl-card tk-state">
        <ElEmpty :description="emptyText" />
      </div>

      <!-- ── 正常态：轮询失败标注 + 任务行列表 ── -->
      <div v-else class="wkl-card tk-body">
        <!-- 静默刷新失败：保留最后已知列表并说出口（与列表页「上次数据仍在」同一口径）。 -->
        <p v-if="refreshError" class="tk-refresh-error">刷新失败，正在显示上次结果</p>
        <ul class="tk-list">
          <li v-for="item in items" :key="item.ref" class="tk-item">
            <!-- 状态点：pending = 旋转动画（在执行）；succeeded = 绿；failed/timeout = 红；
                 未知终态照实给中性点 + 原文 —— 服务端加了新状态时页面不冒充它认识。 -->
            <span class="tk-item__dot" :class="dotClass(item.status)">
              <ArtSvgIcon
                v-if="statusKind(item.status) === 'pending'"
                icon="ri:loader-4-line"
                class="tk-spin"
              />
            </span>
            <div class="tk-item__main">
              <div class="tk-item__line">
                <!-- 动作：注册表 label（页面只说人话）；查不到的（终端/配置编辑类，
                     本就不在二期写注册表里）原样等宽显示 —— 等宽本身就是「这是原始码」的提示。 -->
                <span class="tk-item__action" :class="{ 'is-raw': !isKnownAction(item) }">
                  {{ actionLabel(item) }}
                </span>
                <span class="tk-item__target" :title="item.target">{{
                  targetText(item.target)
                }}</span>
                <button
                  v-if="isExpandable(item) || isExpanded(item.ref)"
                  type="button"
                  class="tk-item__toggle"
                  :title="isExpanded(item.ref) ? '收起' : '展开'"
                  @click="toggleExpand(item)"
                >
                  <ArtSvgIcon
                    :icon="isExpanded(item.ref) ? 'ri:arrow-up-s-line' : 'ri:arrow-down-s-line'"
                  />
                </button>
              </div>
              <!-- 元信息行：状态词 + 归属主机（设备已删时回退 hostId）+ 发起人 + 相对时间。 -->
              <div class="tk-item__meta">
                <span class="tk-item__status" :class="`is-${statusKind(item.status)}`">
                  {{ statusText(item.status) }}
                </span>
                <span class="tk-item__host">@ {{ item.hostname || item.hostId }}</span>
                <span class="tk-item__user">{{ item.username || '—' }}</span>
                <!-- 相对时间：createdAt 是毫秒，除千的浮点秒交给 eventRelativeTime 整段取整
                    （QA 路 1 B1 的浮点文案修复在函数内，惠及全部调用方）。 -->
                <span class="tk-item__time">{{
                  eventRelativeTime(item.createdAt / 1000, nowSec)
                }}</span>
              </div>
              <!-- 展开区：进行中的拉取接实时进度流（收起仅停止观看，结局按实际结算 ——
                   语义见子组件）；终态行给结论句原文（失败/超时的排障线索；
                   成功拉取收尾时同样留在原地）。 -->
              <div v-if="isExpanded(item.ref)" class="tk-item__detail">
                <TaskPullProgress
                  v-if="item.status === 'pending' && item.action === 'image:pull'"
                  :key="item.ref"
                  :host-id="item.hostId"
                  :cmd-ref="item.ref"
                />
                <p v-else-if="item.summary" class="tk-item__summary">{{ item.summary }}</p>
              </div>
            </div>
          </li>
        </ul>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
  /**
   * 任务中心页（8c 页面化，`/docker/tasks`）：长任务（拉取/启停/重建…）的跨主机收口
   * 面板 —— 「刚才受理了什么、进行到哪、结论是什么」一张列表读完。被删的
   * task-center-drawer 内容**原样**平移（列表渲染 / 服务端过滤 / 5 秒轮询 / 拉取进度
   * 内联一个不改），宿主从抽屉换成页面：整页化后它有了自己的 URL 与刷新语义
   * （多分辨率适配：任务行在窄屏不再被抽屉宽度切成两行元信息）。
   *
   * 数据是**一份**（GET /docker/tasks：≤100 条、受理时刻降序、跨主机），不与任何
   * 单主机页面状态耦合 —— 这也是入口虽然长在总览 hero 与容器页 hero 上、列表却不按
   * 当前主机过滤的原因：hostname 列已经回答「在哪台机器上」，再按入口主机收窄会把
   * 「跨主机收口」这个立身之本又交回去。
   *
   * 列表**只含变更/长任务动作**：只读动作（inspect 档的日志/统计/详情/配置读取 ——
   * 页面浏览就会被自动受理）在服务端任务面就被剔除（docker_tasks.go 的读面收口，
   * QA 路 1 P2 的「被只读指令刷屏」修复），本页的动作原文兜底因此只剩终端/配置
   * 编辑这类不在写注册表里的动作。
   *
   * 刷新纪律（对齐列表页自动刷新）：进页面即拉、可见期间 5 秒一轮（后台标签页跳过
   * tick）、手动刷新随时可插队（seq 守卫防旧响应晚到覆盖新数据）；keep-alive 失活/
   * 卸载停表，重新激活回来补拉。**进度流不在此列**：展开中的拉取进度流在页面失活时
   * 保持连接（断开仅停止观看，不承诺因果：是否被截止由拉取的**实际结局**结算，后端
   * pull_progress.go 的「取消与完成的竞态」注释；监控窗口关掉不等于放弃拉取 —— 详见
   * task-pull-progress 的生命周期取舍），停表停的是这份列表的轮询开销。
   *
   * 过滤是**就地状态**（不进 query，原样平移抽屉口径）：服务端过滤每次切换即重拉，
   * 进 query 会让后退键变成「筛选项切换器」，与「回上一页」的直觉打架。
   *
   * 本页没有后端菜单种子（裁定：不加菜单，从总览 hero 与容器页 hero 入口进）—— 路由
   * 取 isHide 形态，理由见 index.ts 的注释（可见且无菜单的路由根本注册不上）。
   */
  import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'
  import { useRouter } from 'vue-router'
  import { ElButton, ElEmpty, ElRadioButton, ElRadioGroup, ElSkeleton } from 'element-plus'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import { usePageIcon } from '@/hooks/core/usePageIcon'
  import { fetchDockerTasks, type DockerTaskItem } from '../api'
  import TaskPullProgress from '../components/task-pull-progress.vue'
  import { lookupDockerAction } from '../utils/actions'
  import { eventRelativeTime } from '../utils/events'

  defineOptions({ name: 'DockerTasksPage' })

  const router = useRouter()
  // 页头图标与侧边栏/页签同源（取菜单图标，改「菜单管理」即同步；见 usePageIcon）。
  const pageIcon = usePageIcon('ri:task-line')

  /** 返回上一页：本页没有菜单入口（从各页入口进），返回键就是它的出口。 */
  function back() {
    router.back()
  }

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

  /** 空态文案：三档各说各的（「还没有受理过任何任务」是全域真空，「没有进行中的任务」
      与「没有已结束的任务」都是筛选结果 —— 已结束档曾说全量档的话，是文案错位）。 */
  const emptyText = computed(() => {
    if (filter.value === 'pending') return '没有进行中的任务'
    if (filter.value === 'done') return '没有已结束的任务'
    return '还没有受理过任何任务'
  })

  /** 页头副标题：三个硬事实（数据范围 / 窗口上限 / 自动刷新节奏）。原「最近受理的
   * 指令」定位句与「可见期间」机制说明属解释性文案，已按「零解释文案」纪律删除。 */
  const subtitle = computed(
    () => `跨主机 · 最多 100 条 · 每 ${POLL_MS / 1000} 秒自动刷新`
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
        // 已有列表：静默失败保留旧数据 + 标注（不整页切错误态 —— 页面开着，把刚看完
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

  // ── 5 秒轮询（页面可见期间；失活/卸载停表） ──────────────────
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
    // 后台标签页的 setInterval 会被浏览器限流：隐藏期间跳过 tick（与总览自动刷新
    // 同一纪律），回到前台后最多一轮之内追上。
    timer = window.setInterval(() => {
      if (!document.hidden) void load(true)
    }, POLL_MS)
  }

  // 进页面即拉（旧数据超过 5 秒也是旧数据）+ 起表。
  onMounted(() => {
    void load()
    restartPoll()
  })

  // 筛选变化即重拉（服务端过滤没有本地回退，不重拉列表就不会变）。
  watch(filter, () => {
    void load()
  })

  // keep-alive 失活停表（页面被 worktab 缓存时不轮询）；激活回来补拉 —— 后台期间
  // 浏览器限流了 interval，切回来的列表已是旧的。首次激活（= 挂载）不算「回来」：
  // onMounted 已经拉过并起了表，这里再拉就是同屏双请求（KeepAlive 内挂载时
  // activated 与 mounted 同时走，见 events-feed 的 start 幂等注释 —— 这里用标记闸）。
  let activatedOnce = false

  onDeactivated(stopPoll)

  onActivated(() => {
    const first = !activatedOnce
    activatedOnce = true
    if (first) {
      restartPoll() // 幂等：表已在走就重建一次（onMounted 已起过表），不加请求
      return
    }
    restartPoll()
    void load(true)
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
   * 目标的展示文本：长十六进制 id（容器/镜像的 64/40 位 id，可带 sha256: 前缀）
   * 截成 12 位短 id —— 模块「长 ID 短显 + title 全显」的惯例（container-detail
   * 的页头 id、events-feed 的 actor 兜底同款）；名称与镜像引用原样显示。全值仍在
   * title 里（完整引用是排障钥匙）。为什么不是只靠 CSS 省略：省略号截在哪由容器
   * 宽度决定，用户不知道被截掉了什么；短 id 是「按惯例这段就够用」的稳定显示。
   */
  const HEX_ID_RE = /^[0-9a-f]{32,}$/i

  function targetText(target: string): string {
    const bare = target.replace(/^sha256:/, '')
    return HEX_ID_RE.test(bare) ? bare.slice(0, 12) : target
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
  /* 页面骨架（hero/三态/动效降级）复用列表页范式样式（本模块四页共用）。 */
  @use './wkl-shell';
  @use './overview-tokens' as t;

  // 「重试」等默认档按钮的主色文字对比度 AA：病灶与处方见 overview-tokens
  // 的 primary-text-aa（终审 QA D2·浅色实测 3.68:1）。
  @include t.primary-text-aa;

  /* 本页的两个页面级对比度令牌（终审 QA D2 实测后的收口）：
     - --el-text-color-secondary：本页是六页「secondary 升 regular」批的漏网页
       （元信息行的状态词/主机/发起人仍吃 EP 默认 #909399，对白底 3.08:1）——
       补齐同款处置：升到 regular 档（浅色 6.1:1、暗色随主题同样达标）；
     - --color-g-600：hero 副标题 text-g-600 的工具变量指向 --art-gray-600(#7987a1)，
       对页底 #fafbfc 3.5:1，低于 AA 正文线 → 抬一档到 g-700（浅色 #4d5875 ≈6.8:1、
       暗色 #ababba ≈8.9:1，随主题自适应）。只重定义变量值，页面外无副作用。 */
  .docker-tasks {
    --el-text-color-secondary: var(--el-text-color-regular);
    --color-g-600: var(--art-gray-700);
  }

  .tk-state {
    display: flex;
    flex-direction: column;
    gap: 10px;
    align-items: center;
    padding: 32px 16px;

    &__text {
      margin: 0;
      color: var(--el-text-color-regular);
      font-size: 13px;
    }
  }

  .tk-body {
    padding: 4px 12px;
  }

  // 静默刷新失败标注：琥珀即「需要注意」（与模块内陈旧/离线标注同一套颜色语言）。
  .tk-refresh-error {
    margin: 8px 0;
    color: var(--el-color-warning);
    font-size: 12px;
    line-height: 1.6;
  }

  // 任务行列表：行间以分隔线分组（无分页 —— 窗口就是「最近 100 条」这一屏）。
  .tk-list {
    margin: 0;
    padding: 0;
    list-style: none;
  }

  .tk-item {
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
      // 非文本图形对比度（WCAG 1.4.11，门槛 3:1；终审 QA D2 同源 #a8abb2 盘点）：
      // placeholder 档 #a8abb2 对白底 2.3:1 不达标 → 升到 secondary 档（本页已把
      // secondary 定为 regular，#606266 于白底 ≈6.1:1；状态点与文字同源一档）。
      &.is-unknown::after {
        background: var(--el-text-color-secondary);
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
    // 对比度 AA（终审 QA D2·浅色实测 2.3:1）：placeholder 档（#a8abb2）对白底只有
    // 2.3:1，低于 AA 4.5:1 —— 升到 regular 档（#606266 于白底 ≈6.1:1；暗色主题的
    // regular 同样只升不降，与 overview-events-feed 的口径句同款处置）。「次要」
    // 的表意由位置（行尾右对齐）承载，不再用低对比色承载。只换色值，形态不动。
    &__time {
      margin-left: auto;
      color: var(--el-text-color-regular);
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
  .tk-spin {
    animation: tk-rotate 1.2s linear infinite;
  }

  @keyframes tk-rotate {
    from {
      transform: rotate(0deg);
    }

    to {
      transform: rotate(360deg);
    }
  }

  // prefers-reduced-motion：旋转降级为静态图标（动效不参与信息表达时的兜底）。
  @media (prefers-reduced-motion: reduce) {
    .tk-spin {
      animation: none;
    }
  }

  // 手机横屏（<768）：过滤/刷新工具行占满一行（hero 的动作组换行铺开，详情页同一口径）。
  @include respond-below('tablet') {
    .tk-hero > .ml-auto {
      width: 100%;
    }
  }
</style>
