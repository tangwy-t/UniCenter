<template>
  <!-- 单根（single-root 守卫在库：布局的 Transition 只支持单根，双根切页白屏）。
       守卫：__tests__/single-root.test.ts 扫描模块内所有 .vue，双根即红灯。 -->
  <div class="docker-projects-page art-full-height overflow-y-auto">
    <div class="wkl-page__inner p-4 pb-8 md:p-5">
      <!-- ============ 页头：图标 + 标题 + 副标题 + 刷新（对齐容器页/镜像与存储页）============ -->
      <div class="wkl-hero mb-4 flex flex-wrap items-center gap-3">
        <div class="wkl-hero__icon flex-cc">
          <ArtSvgIcon :icon="pageIcon" />
        </div>
        <div class="min-w-0">
          <h2 class="text-lg font-semibold text-[var(--el-text-color-primary)]">项目</h2>
          <p class="mt-0.5 flex flex-wrap items-center gap-1.5 text-xs text-g-600">
            <span
              class="live-dot inline-block h-1.5 w-1.5 rounded-full bg-success"
              :class="{ 'is-loading': loading }"
            />
            <span>{{ subtitle }}</span>
          </p>
        </div>
        <div class="ml-auto flex items-center gap-1">
          <!-- 刷新：清单 + 主机清单一起重拉（主机可能新入库；副标题的台数跟着走）。 -->
          <ArtButtonTable
            icon="ri:refresh-line"
            iconClass="bg-theme/12 text-theme"
            title="刷新"
            @click="refreshAll"
          />
        </div>
      </div>

      <!-- ============ 加载（首次，还没有任何行可给） ============ -->
      <div v-if="pageState === 'loading'" class="wkl-card wkl-state flex-cc flex-col gap-3 py-16">
        <div class="wkl-state__icon flex-cc">
          <ArtSvgIcon icon="ri:loader-4-line" class="wkl-state__spin" />
        </div>
        <div class="text-sm font-medium text-[var(--el-text-color-regular)]">
          正在拉取跨主机项目清单…
        </div>
        <div class="text-xs text-g-600">跨全部可管主机聚合，首次加载请稍候</div>
      </div>

      <!-- ============ 错误（首拉失败：结论句 + 重试，不显示旧数据） ============ -->
      <div v-else-if="pageState === 'error'" class="wkl-card wkl-state">
        <ElResult icon="error" title="项目清单获取失败" sub-title="统一清单接口暂时不可用">
          <template #extra>
            <ElButton type="primary" @click="reloadAll">重试</ElButton>
          </template>
        </ElResult>
      </div>

      <!-- ============ 正常态：筛选 + 索引表 ============ -->
      <template v-else>
        <ArtSearchBar
          v-show="showSearchBar"
          v-model="searchForm"
          :items="searchItems"
          @search="onSearch"
          @reset="onReset"
        />

        <ElCard class="art-table-card" shadow="never">
          <ArtTableHeader
            v-model:showSearchBar="showSearchBar"
            :loading="loading"
            @refresh="refresh"
          >
            <template #left>
              <!-- total 如实报全量、items 截 500：截断必须说出口，否则用户以为
                   「筛选完了就这么多」而漏看排在 500 名之后的行。 -->
              <span class="docker-count">
                共 {{ total }} 个项目<template v-if="truncated">
                  · 列表显示前 {{ rows.length }} 条</template
                >
              </span>
            </template>
          </ArtTableHeader>

          <!-- 两种空态分开：没有可管主机 vs 筛选没命中（后者给「清除筛选」）。
               空态渲染在本页、不写进 ArtTable 的 `#empty` 插槽：ArtTable 不转发该插槽
               （内部把 ElTable 的空态写死成「暂无数据」），写进去会被静默丢弃。 -->
          <ArtTable
            v-if="showTable"
            :loading="listLoading"
            :data="rows"
            :columns="columns"
            row-key="name"
          />
          <ElEmpty
            v-else-if="pageState === 'empty'"
            class="docker-empty"
            description="尚无可管主机"
          />
          <ElEmpty v-else-if="hasFilter" class="docker-empty" description="没有符合筛选条件的项目">
            <ElButton size="small" @click="onReset">清除筛选</ElButton>
          </ElEmpty>
          <ElEmpty v-else class="docker-empty" description="还没有项目" />
        </ElCard>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
  /**
   * 项目列表页（9b：跨主机化 + 筛选风格统一；7b 起的「薄索引」定位不变）。
   *
   * 数据源从「页面级单主机快照 + 本地 keyword 过滤」换成 GET /docker/projects
   * （跨主机聚合、服务端过滤：keyword/state/hostId 三项全部透传）—— 跨主机的表
   * 没法在单主机快照上筛。主机维度从页面级上下文（HostSwitcher）降为**筛选下拉**
   * 的一项；行归属由新增的主机列给出；`/docker/projects?host=` 深链照旧有效
   * （进入时作为主机筛选初始值 —— 工作台返回与站内入口的既有链路不动）。
   *
   * 筛选面板随本次改造归一到 ArtSearchBar 统一形态（「项目筛选风格统一」项：
   * 与其他页同一套形态 —— 查询/重置、展开收起都走组件，不自建搜索面板）。
   *
   * 本页仍只回答「哪些项目、什么状态、在哪台机器」；行上唯一的动作是「打开工作台」
   * （host 取**行主机** —— 项目是主机作用域的，工作台据此还原同一台机器），
   * 状态/网元动作/聚合日志与配置编辑都在工作台里。
   */
  import { computed, h, ref, watch } from 'vue'
  import { useRoute, useRouter } from 'vue-router'
  import { ElButton, ElCard, ElEmpty, ElResult } from 'element-plus'
  import { useAuth } from '@/hooks/core/useAuth'
  import { PermDockerInspect } from '@/enums/permission'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtSearchBar from '@/components/core/forms/art-search-bar/index.vue'
  import ArtSvgIcon from '@/components/core/base/art-svg-icon/index.vue'
  import ArtTable from '@/components/core/tables/art-table/index.vue'
  import ArtTableHeader from '@/components/core/tables/art-table-header/index.vue'
  import { usePageIcon } from '@/hooks/core/usePageIcon'
  import type { ColumnOption } from '@/types/component'
  import { fetchDockerProjects, type DockerProjectListItem } from '../api'
  import { useHostList, useResourceList } from '../composables/useResourceList'
  import { hostLabel } from '../utils/host'

  defineOptions({ name: 'DockerProjects' })

  const route = useRoute()
  const router = useRouter()
  // 页头图标与侧边栏/页签同源（取菜单图标，改「菜单管理」即同步；见 usePageIcon）。
  const pageIcon = usePageIcon('ri:stack-line')
  const { hasAuth } = useAuth()

  /** 工作台入口的权限门（与 /docker/projects/:name 路由的 authMark 同档）。 */
  const canInspect = computed(() => hasAuth(PermDockerInspect))

  // ── 数据源（跨主机聚合端点；服务端过滤，参数从筛选表单现取）──────────

  // 主机清单：本页是单表面，自拉一份（useHostList 进页面即拉）。
  const { hosts, hostsLoading, loadHosts } = useHostList()

  const { rows, total, loading, refreshError, truncated, pageState, load, refresh, reloadAll } =
    useResourceList<DockerProjectListItem>({
      fetcher: (p) =>
        fetchDockerProjects({
          hostId: p.hostId || undefined,
          keyword: p.keyword || undefined,
          state: (p.state as 'running' | 'stopped' | undefined) || undefined
        }),
      params: () => ({
        hostId: searchForm.value.host,
        keyword: searchForm.value.keyword,
        state: searchForm.value.state
      }),
      hosts: () => hosts.value,
      hostsLoading: () => hostsLoading.value
    })

  /** 表格的加载态：清单在拉，或主机清单还没到（后者尚不知有没有主机，不能先喊「没有」）。 */
  const listLoading = computed(() => loading.value || hostsLoading.value)

  /** 有命中行就渲染表格；纯加载中也用表格的 loading 遮罩，空态只在加载结束后判断。 */
  const showTable = computed(() => listLoading.value || rows.value.length > 0)

  /** 页头副标题：三种事实（在拉 / 刷新失败 / 正常），与容器页 subtitle 同一取向。 */
  const subtitle = computed(() => {
    if (pageState.value === 'loading') return '正在拉取项目清单'
    if (refreshError.value) return '上次数据仍在，本次刷新失败'
    return `跨 ${hosts.value.length} 台主机`
  })

  // ── 筛选（ArtSearchBar 统一形态；三项全部作为 query 发给端点，服务端过滤）──

  const showSearchBar = ref(false)
  const searchForm = ref<{ keyword?: string; state?: 'running' | 'stopped'; host?: string }>({})

  const hostOptions = computed(() => hosts.value.map((h) => ({ label: hostLabel(h), value: h.id })))

  const searchItems = computed(() => [
    {
      key: 'keyword',
      label: '项目名',
      type: 'input',
      placeholder: '按项目名过滤',
      clearable: true
    },
    {
      // 端点只有两档（stopped = 一切非 running，partial 归入 stopped，与容器页
      // 同一句口径）；项目态的细分（部分运行）在条目上的状态列承载。
      key: 'state',
      label: '状态',
      type: 'select',
      placeholder: '全部状态',
      clearable: true,
      options: [
        { label: '运行中', value: 'running' },
        { label: '已停止', value: 'stopped' }
      ]
    },
    {
      // 主机维度从页面级上下文（HostSwitcher）降为筛选项：全部主机 + 各台。
      // filterable：主机多了要能敲名字找（QA 实测不可搜），与容器/镜像/卷/网络页同款。
      key: 'host',
      label: '主机',
      type: 'select',
      placeholder: '全部主机',
      clearable: true,
      filterable: true,
      options: hostOptions.value
    }
  ])

  const hasFilter = computed(
    () =>
      Boolean(searchForm.value.keyword) ||
      Boolean(searchForm.value.state) ||
      Boolean(searchForm.value.host)
  )

  function onSearch() {
    // 服务端过滤：搜索按钮即重拉（无分页可重置）。
    showSearchBar.value = true
    void load()
  }

  function onReset() {
    searchForm.value = {}
    void load()
  }

  // `/docker/projects?host=` 深链兼容：query 里的主机作为**主机筛选初始值** ——
  // 单向（用户改筛选不回写 query，host 从此是筛选状态而不是页面状态）。watch
  // 同时兜住 worktab 缓存实例的「带 host 返回」场景（工作台返回链路）。
  watch(
    () => route.query.host,
    (raw, old) => {
      const id = raw ? String(raw) : ''
      const had = searchForm.value.host ?? ''
      searchForm.value.host = id
      // immediate 首跑（old 为 undefined）必拉一次；此后仅 query 真的变化时重拉。
      if (id !== had || old === undefined) void load()
    },
    { immediate: true }
  )

  function refreshAll() {
    // 主机清单可能已变化（新主机入库），与清单一起重拉。
    void loadHosts()
    void refresh()
  }

  // ── 展示口径（与工作台 hero 同一批文案/色系，两处不写两种话）──

  /** 项目状态：后端由成员容器运行态归纳出 running/partial/stopped 三态（未知给「—」）。 */
  function stateText(stateValue?: string): string {
    if (stateValue === 'running') return '运行中'
    if (stateValue === 'partial') return '部分运行'
    if (stateValue === 'stopped') return '已停止'
    return '—'
  }

  /** 状态点色（绿=运行中/琥珀=部分运行/灰=其余 —— 颜色即结论，但旁边永远有文字结论，
      不靠颜色单独传达 —— 无障碍，与工作台 hero 的 pwh__dot 同一口径）。 */
  function stateDotColor(stateValue?: string): string {
    if (stateValue === 'running') return 'var(--el-color-success)'
    if (stateValue === 'partial') return 'var(--el-color-warning)'
    return 'var(--el-text-color-disabled)'
  }

  // ── 表格列（:columns + formatter，同 workload-table 的形态）──
  // 状态点走内联样式而不是 scoped class：formatter 产出的 vnode 在 ArtTable 的渲染
  // 上下文里取不到本页的 scope id，scoped 规则贴不上（workload-table 的口径同此）。
  const columns = computed<ColumnOption<DockerProjectListItem>[]>(() => {
    // 显式建立依赖：操作列按 canInspect 过滤，而 formatter 要到表格渲染时才执行 ——
    // 不在这里读一次，列配置不会随权限信号重算（workload-table 同款注释）。
    void canInspect.value
    return [
      { type: 'index' as const, width: 60, label: '序号', hideBelow: 'tablet' },
      { prop: 'name', label: '项目名', minWidth: 200, showOverflowTooltip: true },
      {
        // 主机列是跨主机表的核心新增：同名项目可能散在多台机器，排查时
        // 「先定位在哪台」是第一句（平板竖屏起可见，与容器统一表同档）。
        prop: 'hostname',
        label: '主机',
        minWidth: 120,
        showOverflowTooltip: true,
        hideBelow: 'tablet',
        formatter: (row: DockerProjectListItem) => row.hostname || row.hostId
      },
      {
        prop: 'state',
        label: '状态',
        minWidth: 110,
        formatter: (row: DockerProjectListItem) =>
          h('span', { class: 'docker-proj-state' }, [
            h('span', {
              class: 'docker-proj-dot',
              style: { background: stateDotColor(row.state) },
              'aria-hidden': 'true'
            }),
            stateText(row.state)
          ])
      },
      {
        prop: 'services',
        label: '网元数',
        minWidth: 90,
        hideBelow: 'tablet'
      },
      { prop: 'containersCount', label: '容器数', minWidth: 90 },
      {
        prop: 'protected',
        label: '保护',
        minWidth: 110,
        // 动手前必须看见的事实（spec §11.1 的 🔒）。保护档结论句不在此重复 ——
        // 那是工作台 hero 的职责（本页只是索引，不承载动作）。
        // 标记用锁图标 + 文字（不用 🔒 emoji：无 emoji 字体的环境里是豆腐块）；
        // 布局类走 :global（formatter vnode 取不到本组件 scope id，与
        // docker-proj-state 同一手法）。
        hideBelow: 'tablet',
        formatter: (row: DockerProjectListItem) =>
          row.protected
            ? h('span', { class: 'docker-proj-lock' }, [
                h(ArtSvgIcon, { icon: 'ri:lock-2-line' }),
                '受保护'
              ])
            : '—'
      },
      {
        prop: 'operation',
        label: '操作',
        width: 130,
        fixed: 'right' as const,
        // 工作台路由 authMark 是 docker:inspect：无该权限的人连入口都不渲染
        // （不渲染 ≠ 禁用，spec §11.0 分期控件矩阵 —— 与 5b 引入时同一纪律）。
        formatter: (row: DockerProjectListItem) =>
          canInspect.value
            ? h(
                ElButton,
                { size: 'small', type: 'primary', plain: true, onClick: () => openWorkspace(row) },
                () => '打开工作台'
              )
            : null
      }
    ]
  })

  // ── 工作台入口（行上唯一的动作）────────────────────────────

  /** 打开工作台：项目是主机作用域的，host 取**行主机**必带（工作台据此还原同一台机器，
   *  返回列表时也带回同一台）。 */
  function openWorkspace(project: DockerProjectListItem) {
    void router.push({
      name: 'DockerProjectWorkspace',
      params: { name: project.name },
      query: { host: project.hostId }
    })
  }
</script>

<style lang="scss" scoped>
  /* 页面骨架（hero/三态/动效降级）下沉在 views/wkl-shell.scss（本模块多页共用的
   * 范式样式）；这里只留本页特有的布局。 */
  @use './wkl-shell';
  @use './overview-tokens' as t;

  // 「清除筛选」等默认档按钮与行内「打开工作台」（plain 主色）的主色文字对比度 AA：
  // 病灶与处方见 overview-tokens 的 primary-text-aa（终审 QA D2·浅色实测 3.68:1/3.27:1）。
  @include t.primary-text-aa;

  // 空态渲染在本页（ArtTable 不转发 `#empty`）：给它接近表格空态的留白。
  .docker-empty {
    padding: 56px 0;
  }

  /* 次要文字对比度 AA（P2 打磨批，与总览页同款处置）：EP 默认
     --el-text-color-secondary(#909399) 对白底只有 3.08:1（QA 实测 2.97–3.08），低于
     AA 正文线 → 页面范围内把它升到 regular 档（浅色 6.1:1、暗色随主题同样达标）。
     只重定义变量值，不碰元素样式与布局。 */
  .docker-projects-page {
    --el-text-color-secondary: var(--el-text-color-regular);

    /* hero 副标题/g-600 辅助字的对比度 AA（终审 QA D2·浅色实测 3.5:1）：
       text-g-600 的工具变量指向 --art-gray-600(#7987a1) —— 对页底 #fafbfc 3.5:1，
       低于 AA 正文线。页面范围内抬一档到 g-700（浅色 #4d5875 对页底 ≈6.8:1；
       暗色 #ababba 对暗底 ≈8.9:1，随主题自适应）。只重定义工具变量值，页面内
       所有 text-g-600 文字（hero 副标题、加载提示）一起达标，页面外无副作用。 */
    --color-g-600: var(--art-gray-700);
  }

  .docker-count {
    font-size: 12px;
    color: var(--el-text-color-secondary);
  }

  // 状态点 + 文字（工作台 hero 的 pwh__dot 同一套口径）。类名全局前缀 docker-proj-*：
  // 这两个元素由 formatter 产出（ArtTable 的渲染上下文），scoped 规则贴不上 ——
  // 放进全局命名空间防撞（详见 columns 计算处的注释）。
  :global(.docker-proj-state) {
    display: inline-flex;
    gap: 6px;
    align-items: center;
  }

  :global(.docker-proj-dot) {
    display: inline-block;
    width: 8px;
    height: 8px;
    flex: none;
    border-radius: 50%;
  }

  // 保护列的锁 + 文字（与 wkl-lock / dv-lock 同一范式；formatter vnode 同上）。
  :global(.docker-proj-lock) {
    display: inline-flex;
    gap: 4px;
    align-items: center;
  }
</style>
