<template>
  <!-- ⚠ 单根包装：页面**必须只有一个根节点**。
       布局把页面放进 `<Transition mode="out-in">`（components/core/layouts/art-page-content），
       而 Vue 的 Transition 只支持单根元素。此前本页是 `<DockerPage>` 与多个 `<ElDialog>` 的
       兄弟根节点（fragment），它作为「离场方」参与一次 out-in 切换后，过渡内部的元素记账
       就坏了 —— 症状是**从本页切到任何其它页面都白屏，必须刷新**。只有本模块踩到，
       因为其余页面都是单根。
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
        <!-- 提示行：本页只是工作台的**索引**（7b 起）—— 项目的事实与动作全部在
             /docker/projects/:name 工作台里，这里一行说清去哪。 -->
        <div class="docker-hint">
          本页是项目索引；状态、网元动作、聚合日志与配置编辑都在「打开工作台」里
        </div>

        <ArtTableHeader
          v-model:show-search-bar="showSearchBar"
          :loading="loading"
          @refresh="refresh"
        >
          <template #left>
            <span class="docker-count">
              共 {{ projects.length }} 个项目<template v-if="keyword"
                >，命中 {{ visibleProjects.length }}</template
              >
            </span>
          </template>
        </ArtTableHeader>

        <!-- keyword 筛选（D-9 补课）：项目清单来自单主机快照，量级是一台机器上的
             compose 项目数 —— 本地过滤即可，没有服务端参数可透传。 -->
        <ArtSearchBar
          v-show="showSearchBar"
          v-model="searchForm"
          :items="searchItems"
          @search="onSearch"
          @reset="onReset"
        />

        <!-- 两种空态分开：主机上没有项目 vs 清单还没到（后者尚不知有没有主机，
             不能先喊「没有项目」），故 v-if 把主机清单的加载态一并算进来。
             空态渲染在本页、不写进 ArtTable 的 `#empty` 插槽：ArtTable 不转发该插槽
             （内部把 ElTable 的空态写死成「暂无数据」），写进去会被静默丢弃。 -->
        <ArtTable
          v-if="showTable"
          :loading="listLoading"
          :data="visibleProjects"
          :columns="columns"
          row-key="name"
        />
        <!-- host-context.ts 的 reload 注释承诺：清单拉不到时页面显示「没有可管理的主机」 -->
        <ElEmpty
          v-else-if="!ctx.hosts.length"
          class="docker-empty"
          description="没有可管理的主机"
        />
        <ElEmpty v-else class="docker-empty" description="该主机上还没有项目" />
      </template>
    </DockerPage>
  </div>
</template>

<script setup lang="ts">
  /**
   * 项目列表页（7b 薄索引）：**只做索引，不做工作台**。
   *
   * 此前本页自带三层展开（项目级动作条 + 网元行 + 配置行）与 Compose 编辑器、
   * 备份/回滚、四个确认弹窗 —— 与 5b 落地的 /docker/projects/:name 工作台**全部
   * 重复**。7b 按零兼容口径删掉这套实现，能力收敛到工作台唯一处；本页只回答
   * 「这台机器上有哪些项目、什么状态」，行上给唯一的动作「打开工作台」。派生口径
   * （网元行归纳、容器 N/M 计数）不再需要 —— 那些数字在快照项目条目上自带
   * （services / containersCount），工作台自己另有 utils/projects.ts 的归纳。
   *
   * 顺手修掉 D-9：项目页此前无任何筛选，项目多了只能翻 —— 补 keyword 本地过滤。
   */
  import { computed, h, ref } from 'vue'
  import { useRouter } from 'vue-router'
  import { ElButton, ElEmpty } from 'element-plus'
  import { useAuth } from '@/hooks/core/useAuth'
  import { PermDockerInspect } from '@/enums/permission'
  import ArtSearchBar from '@/components/core/forms/art-search-bar/index.vue'
  import ArtTable from '@/components/core/tables/art-table/index.vue'
  import ArtTableHeader from '@/components/core/tables/art-table-header/index.vue'
  import type { ColumnOption } from '@/types/component'
  import DockerPage from '../components/docker-page.vue'
  import type { DockerProjectItem } from '../api'
  import { useDockerHostState } from '../composables/useDockerHostState'
  import { provideDockerHost } from '../utils/host-context'

  // 主机上下文是**页面级** provide/inject：DockerPage 与 HostSwitcher 都用 useDockerHost() 取它，
  // 而模块里没有别的 provide 调用方 —— 页面就是这一层的提供者，故在这里 provide 并直接用其返回值。
  // 不要解构：上下文字段是 getter，解构会把 hostId 定格成进入页面时的 ''（主机清单尚未到达）。
  const ctx = provideDockerHost()
  const { hasAuth } = useAuth()
  const router = useRouter()

  /** 工作台入口的权限门（与 /docker/projects/:name 路由的 authMark 同档）。 */
  const canInspect = computed(() => hasAuth(PermDockerInspect))

  // 快照与四态收口在 composable（hosts 清单、seq 守卫、主机切换后的重拉都在它里面）。
  // 薄索引没有随主机的临时状态（弹窗/编辑器都在工作台）—— 无需 onHostSwitch 重置。
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
  } = useDockerHostState({ host: ctx })

  /** 项目清单（快照原序）；keyword 过滤是**本地**的（见 searchItems 注释）。 */
  const projects = computed(() => state.value?.projects ?? [])

  // ── keyword 筛选（D-9 补课）──────────────────────────────────
  const showSearchBar = ref(false)
  const searchForm = ref<{ keyword?: string }>({})

  /** 过滤条件里生效的关键字（点「查询」生效，与容器页搜索栏同一交互形态）。 */
  const keyword = computed(() => (searchForm.value.keyword ?? '').trim().toLowerCase())

  const visibleProjects = computed(() =>
    keyword.value
      ? projects.value.filter((p) => p.name.toLowerCase().includes(keyword.value))
      : projects.value
  )

  const searchItems = [
    {
      key: 'keyword',
      label: '项目名',
      type: 'input',
      placeholder: '按项目名过滤',
      clearable: true
    }
  ]

  function onSearch() {
    showSearchBar.value = true
  }

  function onReset() {
    searchForm.value = {}
  }

  /** 有行就渲染表格；纯加载中也用表格的 loading 遮罩，空态只在加载结束后判断。 */
  const showTable = computed(() => listLoading.value || visibleProjects.value.length > 0)

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
  const columns = computed<ColumnOption<DockerProjectItem>[]>(() => {
    // 显式建立依赖：操作列按 canInspect 过滤，而 formatter 要到表格渲染时才执行 ——
    // 不在这里读一次，列配置不会随权限信号重算（workload-table 同款注释）。
    void canInspect.value
    return [
      { type: 'index' as const, width: 60, label: '序号', hideBelow: 'tablet' },
      { prop: 'name', label: '项目名', minWidth: 200, showOverflowTooltip: true },
      {
        prop: 'state',
        label: '状态',
        minWidth: 110,
        formatter: (row: DockerProjectItem) =>
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
        hideBelow: 'tablet',
        formatter: (row: DockerProjectItem) => (row.protected ? '🔒 受保护' : '—')
      },
      {
        prop: 'operation',
        label: '操作',
        width: 130,
        fixed: 'right' as const,
        // 工作台路由 authMark 是 docker:inspect：无该权限的人连入口都不渲染
        // （不渲染 ≠ 禁用，spec §11.0 分期控件矩阵 —— 与 5b 引入时同一纪律）。
        formatter: (row: DockerProjectItem) =>
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

  /** 打开工作台：项目是主机作用域的，host 必带（工作台据此还原同一台机器，
   *  返回列表时也带回同一台）。 */
  function openWorkspace(project: DockerProjectItem) {
    void router.push({
      name: 'DockerProjectWorkspace',
      params: { name: project.name },
      query: { host: ctx.hostId }
    })
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
</style>
