<template>
  <!-- 单根（single-root 守卫在库：布局的 Transition 只支持单根，双根切页白屏）。
       守卫：__tests__/single-root.test.ts 扫描模块内所有 .vue，双根即红灯。 -->
  <div class="docker-projects-page art-full-height">
    <ArtSearchBar
      v-show="showSearchBar"
      v-model="searchForm"
      :items="searchItems"
      @search="onSearch"
      @reset="onReset"
    />

    <ElCard class="art-table-card" shadow="never">
      <ArtTableHeader
        v-model:columns="columnChecks"
        v-model:showSearchBar="showSearchBar"
        :loading="loading"
        @refresh="refreshAll"
      >
        <template #left>
          <div class="toolbar-left">
            <!-- total 如实报全量、items 截 500：截断必须说出口，否则用户以为
                 「筛选完了就这么多」而漏看排在 500 名之后的行。
                 只在 pageState=ready（握有数据）时报数 —— 首拉/失败/无可管主机时
                 报「共 0 个」是假话（数据面不得伪装新鲜）。 -->
            <span v-if="pageState === 'ready'" class="docker-count">
              共 {{ total }} 个项目<template v-if="truncated">
                · 列表显示前 {{ rows.length }} 条</template
              >
            </span>
            <!-- 静默刷新失败：保留最后已知清单并说出口（与镜像/卷/网络页 的
                 docker-refresh-note 同一形态：小字、琥珀 token）。 -->
            <span v-if="refreshError" class="docker-refresh-note">刷新失败，正在显示上次结果</span>
          </div>
        </template>
      </ArtTableHeader>

      <!-- 有命中行（或主机/清单还在首拉）就渲染表格：纯加载中也用表格的 loading
           遮罩，空态只在加载结束后判断（不能先喊「没有」）。 -->
      <ArtTable
        v-if="showTable"
        :loading="listLoading"
        :data="rows"
        :columns="columns"
        row-key="name"
      />

      <!-- 首拉失败（还没有任何行可给）：紧凑失败态 —— 结论句 + 重试，不显示旧数据；
           全局错误 toast 由 utils/http 的 request() 统一弹出，这里不另做。 -->
      <ElEmpty
        v-else-if="pageState === 'error'"
        class="docker-empty"
        description="项目清单获取失败"
      >
        <ElButton size="small" @click="reloadAll">重试</ElButton>
      </ElEmpty>

      <!-- 三种空态分开（各有各的事实）：没有可管主机 vs 筛选没命中（给「清除筛选」）
           vs 还没有项目。空态渲染在本页、不写进 ArtTable 的 `#empty` 插槽：ArtTable
           不转发该插槽（内部把 ElTable 的空态写死成「暂无数据」），写进去会被静默丢弃。 -->
      <ElEmpty v-else-if="pageState === 'empty'" class="docker-empty" description="尚无可管主机" />
      <ElEmpty v-else-if="hasFilter" class="docker-empty" description="没有符合筛选条件的项目">
        <ElButton size="small" @click="onReset">清除筛选</ElButton>
      </ElEmpty>
      <ElEmpty v-else class="docker-empty" description="还没有项目" />
    </ElCard>
  </div>
</template>

<script setup lang="ts">
  /**
   * 项目列表页（9b：跨主机化 + 筛选风格统一；7b 起的「薄索引」定位不变）。
   * 本波「列表页纯化」后 = 搜索栏 + 表卡（工具条内置钮 + 计数/刷新标注 + 表 + 空态）
   * 的纯清单形态（对齐系统管理的列表形态）；页头（hero）与副标题整块删除。
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
  import { ElButton, ElCard, ElEmpty } from 'element-plus'
  import { useAuth } from '@/hooks/core/useAuth'
  import { PermDockerInspect } from '@/enums/permission'
  import ArtSearchBar from '@/components/core/forms/art-search-bar/index.vue'
  import ArtSvgIcon from '@/components/core/base/art-svg-icon/index.vue'
  import ArtTable from '@/components/core/tables/art-table/index.vue'
  import ArtTableHeader from '@/components/core/tables/art-table-header/index.vue'
  import { useTableColumns } from '@/hooks/core/useTableColumns'
  import type { ColumnOption } from '@/types/component'
  import { fetchDockerProjects, type DockerProjectListItem } from '../api'
  import { useHostList, useResourceList } from '../composables/useResourceList'
  import { hostLabel } from '../utils/host'

  defineOptions({ name: 'DockerProjects' })

  const route = useRoute()
  const router = useRouter()
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
    // 工具条刷新（内置钮）：主机清单可能已变化（新主机入库），与清单一起重拉。
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

  // ── 表格列（useTableColumns：列设置随工具条的列设置弹层生效，对齐系统管理）──
  // 状态点走内联样式而不是 scoped class：formatter 产出的 vnode 在 ArtTable 的渲染
  // 上下文里取不到本页的 scope id，scoped 规则贴不上（workload-table 的口径同此）。
  const { columns, columnChecks } = useTableColumns<DockerProjectListItem>(() => {
    const cols: ColumnOption<DockerProjectListItem>[] = [
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
        // 列结构不随权限变（列集合稳定，勾选/排序状态才不会被列集合漂移冲掉）；
        // 权限门在 formatter 渲染时判定：工作台路由 authMark 是 docker:inspect，
        // 无该权限的人连入口都不渲染（不渲染 ≠ 禁用，spec §11.0 分期控件矩阵 —
        // 与 5b 引入时同一纪律）。
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
    return cols
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
  /* 工具条左组：计数文本 + 刷新标注一行排开（对齐系统管理的 toolbar-left）。 */
  .toolbar-left {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;
  }

  /* 次要文字对比度 AA（P2 打磨批，与总览页同款处置）：EP 默认
     --el-text-color-secondary(#909399) 对白底只有 3.08:1（QA 实测 2.97–3.08），低于
     AA 正文线 → 页面范围内把它升到 regular 档（浅色 6.1:1、暗色随主题同样达标）。
     只重定义变量值，不碰元素样式与布局。 */
  .docker-projects-page {
    --el-text-color-secondary: var(--el-text-color-regular);
  }

  /* 工具条计数：辅助信息，小号次要色（与镜像/卷/网络页 的 docker-count 同款）。 */
  .docker-count {
    font-size: 12px;
    color: var(--el-text-color-secondary);
  }

  /* 静默刷新失败标注：琥珀即「需要注意」（与镜像/卷/网络页 / 任务页同一套颜色语言）。 */
  .docker-refresh-note {
    margin-left: 10px;
    font-size: 12px;
    // 文字对比度 AA（收尾批）：原 el-color-warning（白底 1.85）改走 token，
    // 数字见 @styles/core/aa-text.scss。
    color: var(--aa-warning-text);
  }

  // 空态渲染在本页（ArtTable 不转发 `#empty`）：给它接近表格空态的留白。
  .docker-empty {
    padding: 56px 0;
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
