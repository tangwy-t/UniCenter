<template>
  <!-- 单根（single-root 守卫在库：布局的 Transition 只支持单根，双根切页白屏）。 -->
  <div class="docker-overview art-full-height overflow-y-auto">
    <div class="docker-overview__inner p-4 pb-8 md:p-5">
      <!-- ============ 页头：图标 + 标题 + 舰队摘要 + 自动刷新（对齐 sv-hero / do-hero）============ -->
      <div class="dov-hero mb-4 flex flex-wrap items-center gap-3">
        <div class="dov-hero__icon flex-cc">
          <ArtSvgIcon :icon="pageIcon" />
        </div>
        <div class="min-w-0">
          <h2 class="text-lg font-semibold text-[var(--el-text-color-primary)]">Docker 总览</h2>
          <p class="mt-0.5 flex flex-wrap items-center gap-1.5 text-xs text-g-600">
            <span
              class="live-dot inline-block h-1.5 w-1.5 rounded-full bg-success"
              :class="{ 'is-loading': loading }"
            />
            <span>{{ subtitle }}</span>
          </p>
        </div>
        <div class="ml-auto flex items-center gap-1">
          <!-- 任务中心入口（6b 抽屉 → 8c 整页）：hero 与容器页 hero 两条入口是同一双
               眼睛 —— 总览盯「舰队健康吗」，任务中心盯「刚才受理的操作都怎么样了」。
               权限与 GET /docker/tasks 同档（docker:list），无权限不渲染。不挂 pending
               徽标：计数要为它单独拉列表且必是过期数字，进行中的数量进任务页里看。 -->
          <ArtButtonTable
            v-if="canList"
            icon="ri:task-line"
            iconClass="bg-theme/12 text-theme"
            title="任务中心"
            @click="goTasks"
          />
          <!-- 自动刷新：盯控制塔的核心姿势是「开着不动」，手动刷会让人看过期数据而不自知；
               默认关 —— 10 秒一次的全量聚合不便宜，让用户自己决定开（与设备总览同一取舍）。 -->
          <ArtButtonTable
            :icon="'ri:timer-2-line'"
            :iconClass="autoRefresh ? 'bg-theme text-white shadow-sm' : 'bg-theme/12 text-theme'"
            :title="autoRefresh ? '关闭自动刷新（每 10 秒）' : '开启自动刷新（每 10 秒）'"
            @click="autoRefresh = !autoRefresh"
          />
          <ArtButtonTable
            icon="ri:refresh-line"
            :iconClass="`bg-theme/12 text-theme${loading ? ' dov-hero-refresh--loading' : ''}`"
            :title="loading ? '刷新中…' : '刷新'"
            :disabled="loading"
            @click="reload"
          />
        </div>
      </div>

      <!-- ============ 加载（首次，还没有任何数字可给） ============ -->
      <div v-if="pageState === 'loading'" class="dov-card dov-state flex-cc flex-col gap-3 py-16">
        <div class="dov-state__icon flex-cc">
          <ArtSvgIcon icon="ri:loader-4-line" class="dov-state__spin" />
        </div>
        <div class="text-sm font-medium text-[var(--el-text-color-regular)]">
          正在拉取舰队总览…
        </div>
        <div class="text-xs text-g-600">跨全部可管主机聚合，首次加载请稍候</div>
      </div>

      <!-- ============ 错误（结论句 + 重试；不显示旧数字） ============ -->
      <div v-else-if="pageState === 'error'" class="dov-card dov-state">
        <!-- ElResult 只有 icon/title/sub-title/extra 四个具名插槽，没有 default ——
             技术细节之类写进去会被静默丢弃（设备总览页记过同一坑），故只放 #extra。 -->
        <ElResult icon="error" title="总览数据获取失败" sub-title="后端聚合接口暂时不可用">
          <template #extra>
            <ElButton type="primary" @click="reload">重试</ElButton>
          </template>
        </ElResult>
      </div>

      <!-- ============ 空态（没有可管主机 —— 引导，不是一块空白） ============ -->
      <div v-else-if="pageState === 'empty'" class="dov-card dov-state">
        <!-- ElEmpty 渲染 description/image/default 三个插槽（没有 #extra），
             操作按钮要塞进 default —— 写错插槽名会被静默丢弃。 -->
        <ElEmpty description="尚无可管主机">
          <div class="dov-state__hint">
            可管主机来自已安装 agent 并上报 Docker 能力信号的设备；设备入库并上报后，
            这里会自动出现它的舰队账目。
          </div>
          <ElButton type="primary" @click="reload">刷新</ElButton>
        </ElEmpty>
      </div>

      <!-- ============ 正常态 ============ -->
      <template v-else>
        <!-- KPI 磁贴：舰队账目。六块资源维度（2 / 3 / 6 列，对齐服务监控的 6 磁贴栅格）
             + 第七块「磁盘占用」跨满整行（它是跨资源的存储总账，与逐资源计数不同类，
             满行也避免 7 块在 6 列栅格里留下孤块）。磁贴本体在子组件（可点击下钻；
             分项语义色见 buildOverviewKpis）。 -->
        <div class="dov-kpis kpi-grid grid grid-cols-2 gap-4 md:grid-cols-3 2xl:grid-cols-6">
          <OverviewKpiTile
            v-for="t in kpis"
            :key="t.key"
            :tile="t"
            :class="t.key === 'disk' ? 'col-span-2 md:col-span-3 2xl:col-span-6' : ''"
            @select="onKpiClick"
          />
        </div>

        <!-- ============ 主机卡片网格（控制塔的就绪板） ============ -->
        <section id="dov-hosts" class="dov-section">
          <div class="dov-section__head">
            <span class="dov-section__title">主机</span>
            <span class="dov-section__sub"> 共 {{ hosts.length }} 台 </span>
          </div>
          <div
            class="dov-hosts grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3 2xl:grid-cols-4"
          >
            <OverviewHostCard v-for="h in hosts" :key="h.id" :host="h" @open="goHostContainers" />
          </div>
        </section>

        <!-- ============ 磁盘占用（6a 磁盘治理：花在哪 + 怎么安全收回） ============ -->
        <!-- 放在主机板之后、异常清单之前：自上而下是「账目 → 就绪板 → 磁盘账 → 要闻」，
             磁盘是每主机一行的事实，紧贴主机板读起来是同一批机器的下一层账目。 -->
        <section id="dov-disk" class="dov-section">
          <div class="dov-section__head">
            <span class="dov-section__title">磁盘占用</span>
            <span class="dov-section__sub">{{ diskSub }}</span>
          </div>
          <OverviewDiskPanel
            :hosts="hosts"
            @open-images="goHostImages"
            @open-volumes="goHostVolumes"
          />
        </section>

        <!-- ============ 异常容器（要闻清单） ============ -->
        <section class="dov-section">
          <div class="dov-section__head">
            <span class="dov-section__title">异常容器</span>
          </div>
          <OverviewAnomalyTable :anomalies="anomalies" @open="goContainerDetail" />
        </section>

        <!-- ============ 活动流（刚才发生了什么） ============ -->
        <!-- 放在异常表下方（全宽）：本页自上而下是「账目 → 就绪板 → 要闻 → 尾迹」的
             控制塔扫读动线；异常表 6 列约 900px 最小宽度，与它并排会在宽屏把表挤进
             半栏横向滚动。面板内部自带 24rem 滚动区，长高不外溢。 -->
        <section class="dov-section">
          <div class="dov-section__head">
            <span class="dov-section__title">活动流</span>
            <!-- 「回放…后进入实时」是防误读口径句（旧事件先出现），按①/②纪律留在
               待裁清单里不擅删；「连接随页面激活期」是机制解释，已删。 -->
            <span class="dov-section__sub">
              跨主机 docker 事件 · 回放各主机最近 50 条后进入实时
            </span>
          </div>
          <OverviewEventsFeed />
        </section>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
  /**
   * Docker 总览（控制塔）：跨主机聚合的落地页 —— 一眼回答「舰队健康吗、哪里不对、
   * 去哪儿看」，然后把人交棒给各资源列表页 / 容器详情页。结构对齐 device/views/
   * overview.vue（该范式的最新落地）：hero + KPI 磁贴 + 分区卡；视觉令牌取自
   * monitor-tokens（复制进本模块 views/overview-tokens.scss，见其文件头）。
   *
   * 数据是**一份**（GET /docker/overview），没有主机切换 —— 主机维度以下钻
   * （?host= query）的方式交给列表页，本页不 provide 主机上下文。
   */
  import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'
  import { useRouter } from 'vue-router'
  import { ElButton, ElEmpty, ElResult } from 'element-plus'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import { useAuth } from '@/hooks/core/useAuth'
  import { PermDockerList } from '@/enums/permission'
  import { usePageIcon } from '@/hooks/core/usePageIcon'
  import OverviewAnomalyTable from '../components/overview-anomaly-table.vue'
  import OverviewDiskPanel from '../components/overview-disk-panel.vue'
  import OverviewEventsFeed from '../components/overview-events-feed.vue'
  import OverviewHostCard from '../components/overview-host-card.vue'
  import OverviewKpiTile from '../components/overview-kpi-tile.vue'
  import { fetchDockerOverview, type DockerHostItem } from '../api'
  import {
    buildOverviewKpis,
    diskPanelSubtitle,
    overviewPageState,
    overviewSubtitle
  } from '../utils/overview'
  // 磁贴数据形状：unplugin 已把 OverviewKpiTile 注册为组件名，这里起个别名避让
  import type { OverviewKpiTile as KpiTile } from '../utils/overview'

  defineOptions({ name: 'DockerOverview' })

  // 页头图标与侧边栏/页签同源（取菜单图标，改「菜单管理」即同步；见 usePageIcon）
  const pageIcon = usePageIcon('ri:ship-line')

  const router = useRouter()

  // ── 任务中心入口（6b → 8c 整页路由） ────────────────────────────
  // 权限与任务列表端点同档（docker:list，本页路由 authMark 同码）；入口是 navigation
  // —— 任务中心自己有 URL（页面态、可刷新），不再是本页的一个就地抽屉。
  const { hasAuth } = useAuth()
  const canList = computed(() => hasAuth(PermDockerList))

  // ── 数据与四态 ──────────────────────────────────────────
  const meta = ref<Api.Docker.DockerOverviewResp | null>(null)
  const loading = ref(false)
  const hasError = ref(false)
  /** 请求序号：手动刷新与 10s 自动刷新并发时，旧响应晚到不得覆盖新数据（seq 守卫）。 */
  let loadSeq = 0

  const hosts = computed<DockerHostItem[]>(() => meta.value?.hosts ?? [])
  /** 异常清单兜底空对象：ready 态下必然有值，兜底只防模板期 undefined 传参。 */
  const anomalies = computed<Api.Docker.DockerOverviewAnomalies>(
    () => meta.value?.anomalies ?? { total: 0, items: [] }
  )
  const kpis = computed(() => (meta.value ? buildOverviewKpis(meta.value.fleet) : []))
  /** 磁盘面板的分区副标题（上报口径：N 台里 M 台报了磁盘账，缺报不折算成零）。 */
  const diskSub = computed(() => diskPanelSubtitle(hosts.value))
  const subtitle = computed(() =>
    overviewSubtitle(meta.value, loading.value, hasError.value, autoRefresh.value)
  )

  /** hostCount 传 -1（而非 0）让「未就绪」走 loading 分支，empty 不可达（见纯函数注释）。 */
  const pageState = computed(() =>
    overviewPageState(
      hasError.value,
      loading.value && meta.value === null,
      meta.value === null ? -1 : hosts.value.length
    )
  )

  /**
   * 拉取总览。
   *
   * @param silent 自动刷新时为 true：不显示加载态 —— 每次刷新都把页面切成骨架，
   *   数字会反复消失，自动刷新反而不可用（设备总览同一取舍）。
   * 失败时**不保留**旧 meta：错误横幅旁边摆着旧数字，读者无法判断哪批是真的
   * —— 宁可整页错误态 + 重试，也不给出可能过期的账目（设备总览同一取舍）。
   */
  async function load(silent = false) {
    const seq = ++loadSeq
    if (!silent) loading.value = true
    try {
      const resp = await fetchDockerOverview()
      if (seq !== loadSeq) return
      meta.value = resp
      hasError.value = false
    } catch {
      if (seq !== loadSeq) return
      hasError.value = true
      meta.value = null
    } finally {
      if (seq === loadSeq) loading.value = false
    }
  }

  function reload() {
    return load()
  }

  // ── 自动刷新（10s；默认关） ──────────────────────────────
  const AUTO_REFRESH_MS = 10_000
  const autoRefresh = ref(false)
  let timer: number | undefined

  function stopTimer() {
    if (timer !== undefined) {
      clearInterval(timer)
      timer = undefined
    }
  }

  function restartTimer() {
    stopTimer()
    if (autoRefresh.value) {
      // 后台标签页的 setInterval 会被浏览器限流，切回来时数据已是旧的 ——
      // 隐藏期间跳过 tick（onActivated 里再补一次拉取）。
      timer = window.setInterval(() => {
        if (!document.hidden) void load(true)
      }, AUTO_REFRESH_MS)
    }
  }

  watch(autoRefresh, restartTimer)

  onMounted(() => {
    void load()
  })

  // 清理要覆盖两种「离开」：整页卸载（普通路由切换）与 keep-alive 失活（worktab
  // 缓存页面实例）。只挂 onBeforeUnmount 的话，被缓存的页面会带着 interval 一直轮询
  // —— 总览是全量聚合，泄漏代价尤其高（server.vue 只处理了前者，本页多一层失活钩子）。
  onBeforeUnmount(stopTimer)
  onDeactivated(stopTimer)

  onActivated(() => {
    // 从缓存回来说明刚才看不见：重启计时器并立即补一次静默拉取
    restartTimer()
    if (autoRefresh.value) void load(true)
  })

  // ── 下钻 ────────────────────────────────────────────────
  /** KPI 磁贴：列表页走 router.push；主机磁贴没有列表页 → 滚到本页的主机卡片网格。 */
  function onKpiClick(tile: KpiTile) {
    if (tile.to) {
      void router.push(tile.to)
      return
    }
    if (!tile.anchor) return
    document.getElementById(tile.anchor)?.scrollIntoView({
      behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth',
      block: 'start'
    })
  }

  /** 主机卡片 → 该主机的容器列表（模块主机上下文的 query 约定，见 host-context.ts）。 */
  function goHostContainers(host: DockerHostItem) {
    void router.push({ path: '/docker/containers', query: { host: String(host.id) } })
  }

  /** 磁盘面板行 → 该主机的镜像表（7a：三个旧列表页收敛为 /docker/resources 的
   *  tab；清理悬空镜像的确认档流在镜像 tab 底栏）。tab 进 query：深链/刷新还原。 */
  function goHostImages(host: DockerHostItem) {
    void router.push({
      path: '/docker/resources',
      query: { host: String(host.id), tab: 'images' }
    })
  }

  /** 磁盘面板行 → 该主机的卷表（volume:prune 的确认档流在数据卷 tab 底栏）。 */
  function goHostVolumes(host: DockerHostItem) {
    void router.push({
      path: '/docker/resources',
      query: { host: String(host.id), tab: 'volumes' }
    })
  }

  /** 异常行 → 容器详情页（8a：深链统一指 /docker/containers/:id，host 随行 ——
   *  目标容器不必在该页的任何筛选视图里，详情页自己有 inspect）。 */
  function goContainerDetail(row: Api.Docker.DockerOverviewAnomalyItem) {
    void router.push({
      path: `/docker/containers/${row.id}`,
      query: { host: row.hostId }
    })
  }

  /** 任务中心（8c 整页）：跨主机长任务收口页，入口在总览 hero 与容器页 hero 上。 */
  function goTasks() {
    void router.push({ path: '/docker/tasks' })
  }
</script>

<style lang="scss" scoped>
  /* 令牌数值复制自 monitor-tokens（经本模块 views/overview-tokens，见其文件头注释） */
  @use './overview-tokens' as t;
  /* 断点单一事实源：JS 侧见 src/config/breakpoints.ts */
  @use '@styles/core/breakpoints.scss' as *;

  @include t.rise-keyframes;
  @include t.pulse-keyframes;

  /* ---------- 骨架：对齐 server.vue 的 .server-page__inner 分栏与留白 ---------- */
  .docker-overview__inner {
    display: flex;
    flex-direction: column;
    gap: 16px;
  }

  /* ---------- 次要文字对比度 AA（P2 打磨批） ----------
     EP 默认 --el-text-color-secondary(#909399) 对白底/浅底只有 3.08/2.97:1（QA 实测
     2.97–3.08），低于 AA 正文线。在页面范围内把它升到 regular 档（浅色 6.1:1、暗色
     随主题同样达标）—— 只重定义变量值，页面内所有消费该变量的元素（分区副标题、
     KPI 副行、主机卡同步行、磁盘条标签、表格表头…）一起达标，不碰任何元素样式与布局。 */
  .docker-overview {
    --el-text-color-secondary: var(--el-text-color-regular);

    /* hero 副标题/g-600 辅助字的对比度 AA（终审 QA D2·浅色实测 3.5:1）：
       text-g-600 的工具变量指向 --art-gray-600(#7987a1) —— 对页底 #fafbfc 3.5:1，
       低于 AA 正文线。页面范围内抬一档到 g-700（浅色 #4d5875 对页底 ≈6.8:1；
       暗色 #ababba 对暗底 ≈8.9:1，随主题自适应）。只重定义工具变量值，页面内
       所有 text-g-600 文字（hero 副标题、加载提示）一起达标，页面外无副作用。 */
    --color-g-600: var(--art-gray-700);
  }

  /* 刷新钮 loading 态：唯一既有线索是 live-dot 变琥珀（359ms 一闪而过），刷新本身
     没有进行中反馈 —— 图标自转 + 禁点 + 标题换「刷新中…」，三件同时给。
     ArtButtonTable 的 svg 在其内部渲染（本页 scoped 贴不上），:deep 穿透。 */
  .dov-hero-refresh--loading :deep(svg) {
    animation: dov-spin 1.1s linear infinite;
  }

  /* ---------- 基础卡片 ---------- */
  .dov-card {
    @include t.card;
    @include t.rise;
  }

  .dark .dov-card {
    @include t.card-dark;
  }

  /* ---------- 页头（对齐 .sv-hero / .do-hero） ---------- */
  .dov-hero__icon {
    @include t.hero-icon;
  }

  .live-dot {
    @include t.live-dot;
  }

  /* 加载中的圆点转琥珀、节奏加快（对齐 .sv-hero 的 is-loading 口径） */
  .live-dot.is-loading {
    background: var(--el-color-warning);
    animation-duration: 0.9s;
  }

  /* ---------- 三态（加载 / 错误 / 空） ---------- */
  .dov-state__icon {
    width: 64px;
    height: 64px;
    border-radius: 20px;
    font-size: 30px;
    color: var(--el-color-primary);
    background: color-mix(in srgb, var(--el-color-primary) 12%, transparent);
  }

  .dov-state__spin {
    animation: dov-spin 1.1s linear infinite;
  }

  @keyframes dov-spin {
    to {
      transform: rotate(360deg);
    }
  }

  .dov-state__hint {
    margin-bottom: 12px;
    max-width: 480px;
    color: var(--el-text-color-secondary);
    font-size: 12px;
    line-height: 1.7;
  }

  /* ---------- KPI 栅格（磁贴本体在子组件 overview-kpi-tile） ---------- */

  /* 入场错峰：序号越大越晚（与监控 KPI 栅格同一节奏，七块补第七档） */
  .kpi-grid > *:nth-child(2) {
    animation-delay: 40ms;
  }
  .kpi-grid > *:nth-child(3) {
    animation-delay: 80ms;
  }
  .kpi-grid > *:nth-child(4) {
    animation-delay: 120ms;
  }
  .kpi-grid > *:nth-child(5) {
    animation-delay: 160ms;
  }
  .kpi-grid > *:nth-child(6) {
    animation-delay: 200ms;
  }
  .kpi-grid > *:nth-child(7) {
    animation-delay: 240ms;
  }

  /* ---------- 分区标题（对齐 .do-section__head） ---------- */
  .dov-section {
    &__head {
      display: flex;
      align-items: baseline;
      gap: 8px;
      flex-wrap: wrap;
      margin-bottom: 12px;
    }

    &__title {
      font-size: 14px;
      font-weight: 600;
      color: var(--el-text-color-primary);
    }

    &__sub {
      color: var(--el-text-color-secondary);
      font-size: 11px;
    }
  }

  /* ---------- 动效降级：与监控收敛点同一口径（磁贴/卡片本体在各子组件内自包） ---------- */
  @include t.reduced-motion('.dov-card', '.live-dot', '.dov-state__spin');
</style>
