<template>
  <!-- 单根（single-root 守卫在库：布局的 Transition 只支持单根，双根切页白屏）。
       本组件是 resources 页「镜像」tab 的内容（9b 起为跨主机聚合表）：主机清单
       经 props 由页面下发（页面级一份，三 tab 共用），清单数据走自己的聚合端点
       （GET /docker/images，服务端过滤）；本组件只持有镜像表自己的筛选、勾选、
       底栏写操作与三个对话框。 -->
  <div class="docker-images-tab">
    <ArtSearchBar
      v-show="showSearchBar"
      v-model="searchForm"
      :items="searchItems"
      @search="onSearch"
      @reset="onReset"
    />

    <!-- 结构沿用收敛页范式：搜索栏在卡片外，表格与页脚在卡片内。 -->
    <ElCard class="art-table-card" shadow="never">
      <ArtTableHeader v-model:showSearchBar="showSearchBar" :loading="loading" @refresh="refresh">
        <template #left>
          <!-- total 如实报全量、items 截 500：截断必须说出口，否则用户以为
               「筛选完了就这么多」而漏看排在 500 名之后的行。 -->
          <span class="docker-count">
            共 {{ total }} 个镜像<template v-if="truncated">
              · 列表显示前 {{ rows.length }} 条</template
            >
          </span>
          <!-- 静默刷新失败：保留最后已知清单并说出口（与容器页副标题同一口径）。 -->
          <span v-if="refreshError" class="docker-refresh-note">刷新失败，正在显示上次结果</span>
        </template>
      </ArtTableHeader>

      <!-- 首拉失败（还没有任何行可给）：结论句 + 重试，不显示旧数据。 -->
      <div v-if="pageState === 'error'" class="docker-tab-error">
        <ElResult icon="error" title="镜像清单获取失败" sub-title="统一清单接口暂时不可用">
          <template #extra>
            <ElButton type="primary" @click="reloadAll">重试</ElButton>
          </template>
        </ElResult>
      </div>

      <template v-else>
        <!-- 两种空态分开：没有可管主机 vs 筛选没命中（后者给「清除筛选」）。
             纪律与容器页一致（「没有」与「筛没了」说成一句会让人以为机器空了）；
             空态渲染在本组件、不写进 ArtTable 的 `#empty` 插槽：ArtTable 不转发该插槽
             （内部把 ElTable 的空态写死成「暂无数据」），写进去会被静默丢弃。 -->
        <ArtTable
          v-if="showTable"
          :loading="listLoading"
          :data="rows"
          :columns="columns"
          @selection-change="onSelectionChange"
        />
        <ElEmpty
          v-else-if="pageState === 'empty'"
          class="docker-empty"
          description="尚无可管主机"
        />
        <ElEmpty v-else-if="hasFilter" class="docker-empty" description="没有符合筛选条件的镜像">
          <ElButton size="small" @click="onReset">清除筛选</ElButton>
        </ElEmpty>
        <ElEmpty v-else class="docker-empty" description="还没有镜像" />

        <!-- 底部合计是这一页的入口数字（「空间去哪了」），主/副两行是两种口径的分工：
             主行（合计）跟着**当前清单**走（服务端过滤后的这批；截断时 header 已另行
             说明），与表格里的行自洽；副行（可回收）读**后端的主机级账目**（本响应
             的 disk 数组），与可见行无关 —— prune 回收的就是主机上那批悬空镜像的
             独占层，keyword/开关过滤与 500 条截断都影响不到它（账目只随主机筛选
             收窄；旧实现 Σ 可见悬空行 SizeMB 在截断时少算、还把共享层算了进去）。
             没有任何主机报账（无 df 数据/无可管主机）时如实说「不可用」，不折算成 0。 -->
        <div class="docker-total">
          <span>合计 {{ totals.count }} 个 · {{ formatByUnit('MB', totals.totalMB) }}</span>
          <span class="docker-total__sub">
            <template v-if="reclaim">
              {{ reclaim.count }} 个可回收 · {{ formatByUnit('MB', reclaim.mb) }}
            </template>
            <template v-else>可回收账目不可用</template>
          </span>
        </div>

        <!-- 底栏写操作（spec §11.3）：清理悬空 / 仓库凭据 / 拉取 / 打标签 / 导出 tar / 载入 /
             构建镜像（P2 分发闭环）。**主机来源分两类**（跨主机表没有「当前主机」）：
             行目标动作（打标签/导出/删除/扫描）按**行主机**派发；无行目标的整体动作
             （清理/拉取/构建/载入）面向**主机筛选选定的那台**（没选且全场只有一台
             可管主机时就是它；否则按钮禁用并在旁边给出结论句）。仓库凭据（4c）与
             镜像写动作不同档（docker:config），不被 canWrite 顺带挡掉。 -->
        <div v-if="canWrite || canConfig" class="docker-bar">
          <ElButton
            v-if="canDelete"
            size="small"
            type="danger"
            plain
            :disabled="busy || !targetHostId"
            @click="openPrune"
          >
            清理悬空镜像…
          </ElButton>
          <ElButton v-if="canConfig" size="small" @click="registryVisible = true">
            仓库凭据…
          </ElButton>
          <ElButton
            v-if="canManage"
            size="small"
            :disabled="busy || !targetHostId"
            @click="askPull"
          >
            拉取镜像…
          </ElButton>
          <ElButton
            v-if="canManage"
            size="small"
            :disabled="tagSaveDisabled"
            @click="onTagSelected"
          >
            打标签…
          </ElButton>
          <ElButton
            v-if="canManage"
            size="small"
            :disabled="tagSaveDisabled"
            @click="onSaveSelected"
          >
            导出 tar…
          </ElButton>
          <ElButton
            v-if="canManage"
            size="small"
            :disabled="busy || !targetHostId"
            @click="askLoad"
          >
            载入镜像…
          </ElButton>
          <ElButton
            v-if="canManage"
            size="small"
            :disabled="busy || !targetHostId"
            @click="askBuild"
          >
            构建镜像…
          </ElButton>
          <span v-if="barHint" class="docker-bar__hint">{{ barHint }}</span>
        </div>
      </template>
    </ElCard>

    <!-- 确认弹窗覆盖五种入口：清理悬空（强档逐字 DELETE）、单删（标准档）、打标签
         （输入档收集新引用）、导出 tar（输入档收集文件名 → alreadyExists 时就地切
         文件名档的覆盖确认）、载入镜像（输入档收集文件名）。
         形态、逐字期望值、输入档的校验与提示全部由组件按动作注册表推导；页面只传
         事实与 options。清理的补充输入（是否连带清理未使用镜像）由本页作为插槽内容给出。 -->
    <DockerActionConfirm
      v-model="confirmState.visible"
      :action="confirmState.action"
      :target="confirmState.target"
      :options="confirmState.options"
      target-kind="镜像"
      :loading="confirmLoading"
      @confirm="onConfirmSubmit"
    >
      <ElCheckbox v-if="confirmState.kind === 'prune'" v-model="pruneIncludeAll" class="prune-all">
        同时清理未被任何容器使用的镜像
      </ElCheckbox>
    </DockerActionConfirm>

    <!-- 拉取进度对话框（4b）：底栏「拉取镜像…」的黑盒等待换成逐层实时进度。
         hostId 在受理时钉死（targetHostId = 发起时锁定的一台），成功关闭后重拉本 tab 清单。 -->
    <PullProgressDialog v-model="pullVisible" :host-id="targetHostId" :refresh="refreshSilent" />

    <!-- 构建进度对话框（P2 分发闭环）：底栏「构建镜像…」的表单 + 逐行构建播报 + 取消。
         hostId 与重拉口径同拉取对话框（成功后新镜像要进列表）。 -->
    <BuildProgressDialog v-model="buildVisible" :host-id="targetHostId" :refresh="refreshSilent" />

    <!-- 仓库凭据管理对话框（4c）：入口按钮只对 docker:config 渲染（不渲染 ≠ 禁用）。
         凭据是**全局**的（不属于任何一台主机 —— 服务端按仓库地址解析注入），
         与主机筛选无关。 -->
    <RegistryCredentialsDialog v-model="registryVisible" />
  </div>
</template>

<script setup lang="ts">
  /**
   * 镜像 tab（9b：跨主机化，同容器统一表范式）。
   *
   * 数据源从「页面级共享的单主机快照 + 本地过滤」换成 GET /docker/images
   * （跨主机聚合、服务端过滤，三项 query 全部透传）—— 跨主机的表没法在单主机
   * 快照上筛。主机维度从页面级上下文降为**筛选下拉**的一项；行归属由新增的
   * 主机列给出。`/docker/resources?host=` 深链照旧有效（进入时作为主机筛选
   * 初始值 —— 总览磁盘面板与镜像详情返回链路的既有链路不动）。
   *
   * 写操作按**行主机/筛选主机**派发：行目标动作的指令通道 hostId 绑定该行
   * （useDockerCmds 给了 hostId 就不碰 provide 上下文）；无行目标的整体动作
   * 面向主机筛选选定的那台（没选且只有一台可管主机时就是它，否则禁用 + 结论句）。
   * 下钻语义保持：详情仍是单主机窗口 —— `/docker/image-detail/:id?host=` 的
   * host 取**行主机**。
   */
  import { computed, h, ref, watch } from 'vue'
  import { useRoute, useRouter } from 'vue-router'
  import { ElButton, ElCard, ElCheckbox, ElEmpty, ElMessage, ElResult } from 'element-plus'
  import { formatByUnit } from '@/modules/device/utils/display'
  import { useAuth } from '@/hooks/core/useAuth'
  import {
    PermDockerConfig,
    PermDockerDelete,
    PermDockerInspect,
    PermDockerManage
  } from '@/enums/permission'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtSearchBar from '@/components/core/forms/art-search-bar/index.vue'
  import ArtTable from '@/components/core/tables/art-table/index.vue'
  import ArtTableHeader from '@/components/core/tables/art-table-header/index.vue'
  import DockerActionConfirm from '../action-confirm.vue'
  import DockerActionMenu from '../action-menu.vue'
  import BuildProgressDialog from '../build-progress-dialog.vue'
  import PullProgressDialog from '../pull-progress-dialog.vue'
  import RegistryCredentialsDialog from '../registry-credentials-dialog.vue'
  import type { ColumnOption } from '@/types/component'
  import {
    fetchDockerImages,
    sendDockerCmd,
    type DockerHostItem,
    type DockerImageListItem,
    type DockerImageListResp
  } from '../../api'
  import {
    classifyAcceptError,
    runErrorMessage,
    useDockerCmds,
    type DockerCmdRunResult
  } from '../../composables/useDockerCmds'
  import { useResourceList } from '../../composables/useResourceList'
  import { lookupDockerAction } from '../../utils/actions'
  import { imageReclaimTotals, imageTotals } from '../../utils/snapshot'
  import { formatRelativeTime, imageRefText, inUseText } from '../../utils/display'
  import { hostLabel } from '../../utils/host'

  defineOptions({ name: 'DockerImagesTab' })

  const props = defineProps<{
    /** 页面级主机清单（resources.vue 的 useHostList，三 tab 共用）。 */
    hosts: DockerHostItem[]
    /** 主机清单在拉：首拉期间不能先喊「没有」（那时还不知道有没有主机）。 */
    hostsLoading: boolean
  }>()

  const route = useRoute()
  const router = useRouter()
  const { hasAuth } = useAuth()

  const showSearchBar = ref(false)
  const searchForm = ref<{
    keyword?: string
    danglingOnly?: boolean
    unusedOnly?: boolean
    host?: string
  }>({})
  /** 当前勾选的行：底栏「打标签 / 导出 tar」一次只作用于选中的那一行。 */
  const selected = ref<DockerImageListItem[]>([])
  /** 拉取进度对话框的开关（4b）：hostId 取发起时的 targetHostId（受理时钉死）。 */
  const pullVisible = ref(false)
  /** 构建进度对话框的开关（P2）：同拉取。 */
  const buildVisible = ref(false)
  /** 仓库凭据对话框的开关（4c）：入口按钮受 canConfig 门控（模板里的 v-if）。 */
  const registryVisible = ref(false)

  const canManage = computed(() => hasAuth(PermDockerManage))
  const canDelete = computed(() => hasAuth(PermDockerDelete))
  /** 仓库凭据管理权限（4c）：与镜像写动作不同档 —— 服务端 /docker/registries 的静态 perm。 */
  const canConfig = computed(() => hasAuth(PermDockerConfig))
  /** 至少有一个写权限（或凭据权限）才渲染底栏操作区（没有可执行的动作就不占版面）。 */
  const canWrite = computed(() => canManage.value || canDelete.value)

  // ── 数据源（跨主机聚合端点；服务端过滤，参数从筛选表单现取）──────────

  /**
   * 可回收账目（后端 disk 数组，口径见 DockerImageListResp.Disk）：底栏副行的
   * 数据面。它与 rows 同一响应 —— 但 useResourceList 的 seq 守卫只守着 rows/total，
   * 这条走自己的 seq（同一「最新发起者胜出」纪律）：否则并发重拉时旧响应晚到，
   * 账目会跟当前行换批，底栏两个数字从此对不上。
   */
  const imageDisk = ref<DockerImageListResp['disk']>([])
  let diskSeq = 0

  const {
    rows,
    total,
    loading,
    refreshError,
    truncated,
    pageState,
    load,
    refresh,
    refreshSilent,
    reloadAll
  } = useResourceList<DockerImageListItem>({
    fetcher: async (p) => {
      const seq = ++diskSeq
      const resp = await fetchDockerImages({
        hostId: p.hostId || undefined,
        keyword: p.keyword || undefined,
        // 开关只发 true（「仅悬空/未被使用」是单侧开关）；false 侧语义留给端点。
        dangling: p.dangling ? true : undefined,
        unused: p.unused ? true : undefined
      })
      if (seq === diskSeq) imageDisk.value = resp.disk ?? []
      return resp
    },
    // UI 的 key（danglingOnly/unusedOnly）在这里映射成端点参数（dangling/unused）——
    // 不让筛选项的 key 泄漏进数据层（与容器页同一句分工）。
    params: () => ({
      hostId: searchForm.value.host,
      keyword: searchForm.value.keyword,
      dangling: searchForm.value.danglingOnly === true ? true : undefined,
      unused: searchForm.value.unusedOnly === true ? true : undefined
    }),
    hosts: () => props.hosts,
    hostsLoading: () => props.hostsLoading
  })

  /** 表格的加载态：清单在拉，或主机清单还没到（后者尚不知有没有主机，不能先喊「没有」）。 */
  const listLoading = computed(() => loading.value || props.hostsLoading)

  /** 有命中行就渲染表格；纯加载中也用表格的 loading 遮罩，空态只在加载结束后判断。 */
  const showTable = computed(() => listLoading.value || rows.value.length > 0)

  // `/docker/resources?host=` 深链兼容（总览磁盘面板的既有链路）：query 里的主机
  // 作为**主机筛选初始值** —— 单向（用户改筛选不回写 query，host 从此是筛选状态
  // 而不是页面状态）。watch 同时兜住 worktab 缓存实例的「带 host 返回」场景
  // （镜像详情返回链路会回到带 host 的本页，缓存实例经它跟上新 host）。
  watch(
    () => route.query.host,
    (raw, old) => {
      const id = raw ? String(raw) : ''
      const had = searchForm.value.host ?? ''
      searchForm.value.host = id
      // immediate 首跑（old 为 undefined）必拉一次；此后仅 query 真的变化时重拉 ——
      // 没 host 深链时 id 与 had 都是 ''，靠 old 区分「首次进入」与「同值重复」。
      if (id !== had || old === undefined) void load()
    },
    { immediate: true }
  )

  const hostOptions = computed(() => props.hosts.map((h) => ({ label: hostLabel(h), value: h.id })))

  const searchItems = computed(() => [
    {
      key: 'keyword',
      label: '仓库',
      type: 'input',
      placeholder: '仓库名或标签',
      clearable: true
    },
    { key: 'danglingOnly', label: '仅悬空', type: 'switch' },
    { key: 'unusedOnly', label: '未被使用', type: 'switch' },
    {
      // 主机维度从页面级上下文（HostSwitcher）降为筛选项：全部主机 + 各台。
      // filterable：主机多了要能敲名字找（QA 实测不可搜），与容器/卷/网络/项目页同款。
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
      Boolean(searchForm.value.danglingOnly) ||
      Boolean(searchForm.value.unusedOnly) ||
      Boolean(searchForm.value.host)
  )

  /** 底栏合计主行：与表格里的行同源（服务端过滤后的这批；截断口径见 header 计数）。 */
  const totals = computed(() => imageTotals(rows.value))

  /** 底栏可回收副行：后端主机级账目的求和（null = 没有任何主机报账 → 说「不可用」）。 */
  const reclaim = computed(() => imageReclaimTotals(imageDisk.value))

  function onSearch() {
    // 服务端过滤：搜索按钮即重拉（无分页可重置）。
    showSearchBar.value = true
    void load()
  }

  function onReset() {
    searchForm.value = {}
    void load()
  }

  // ── 底栏写操作的输入 ──

  const singleSelected = computed(() => selected.value.length === 1)
  /** 打标签/导出 tar 的门槛：指令不在途，且恰好选中一行（结论句在按钮旁给出）。 */
  const tagSaveDisabled = computed(() => busy.value || !singleSelected.value)

  // ── 主机来源（跨主机表的写操作按发起时锁定的主机派发）───────────────

  /**
   * 无行目标的整体动作（清理/拉取/构建/载入）面向哪台主机：主机筛选选定的那台；
   * 没选且全场只有一台可管主机时就是它（不让人先做无意义的单选）；否则留空 ——
   * 按钮禁用，结论句（先在主机筛选中选定一台）由 barHint 给出。
   */
  const targetHostId = computed(() => {
    if (searchForm.value.host) return searchForm.value.host
    return props.hosts.length === 1 ? props.hosts[0]!.id : ''
  })

  /** 底栏提示：优先说「整体动作缺主机」（按钮在禁用中），其次说选中行的要求。 */
  const barHint = computed(() => {
    if (!targetHostId.value) return '清理 / 拉取 / 构建 / 载入需先在「主机」筛选中选定一台主机'
    if (canManage.value && !singleSelected.value) {
      return '打标签与导出 tar 需先在列表中选中一行镜像'
    }
    return ''
  })

  /**
   * 指令通道的主机来源：行目标动作在**入口处锁定到该行的 hostId**（useDockerCmds
   * 在 run 入口求值，换 ref 即切主机）；同表不同机的行各发各的主机，不再有
   * 「页面当前主机」的概念。一次操作属于发起时锁定的那台主机。
   */
  const cmdHostId = ref('')
  const { run, pendingId, busy } = useDockerCmds({
    hostId: () => cmdHostId.value,
    refresh: refreshSilent
  })

  // ── 勾选与底栏选择入口 ──

  function onSelectionChange(rowsSel: DockerImageListItem[]) {
    selected.value = rowsSel
  }

  /** 底栏打标签/导出 tar：输入是选中的那一行（未选中时按钮已禁用，这里再兜一道）。 */
  function onTagSelected() {
    if (selected.value.length !== 1) return
    askTag(selected.value[0]!)
  }
  function onSaveSelected() {
    if (selected.value.length !== 1) return
    askSave(selected.value[0]!)
  }

  // ── 指令引用与结果回执 ──

  /** 该行的指令引用：有仓库标签用它；无标签的镜像退回镜像 id（两者都是协议认的引用）。 */
  function actionRef(image: DockerImageListItem): string {
    return image.repoTags?.[0] || image.id
  }

  /** 结果 → 界面回执：成功优先用结果的 detail（agent 的结论句），失败用统一的结论句。 */
  function reportResult(res: DockerCmdRunResult): void {
    if (res.ok) ElMessage.success(res.detail || '操作已完成')
    else ElMessage.error(runErrorMessage(res, '操作未完成'))
  }

  async function runWrite(
    action: string,
    target: string | undefined,
    options: Record<string, unknown>,
    confirm?: string
  ): Promise<DockerCmdRunResult> {
    const res = await run({ action, target, options, confirm })
    reportResult(res)
    return res
  }

  // ── 底栏动作 ──

  /** 清理悬空镜像：强档（组件按注册表给逐字 DELETE 形态），勾选项决定是否连带清理未使用镜像。 */
  function openPrune() {
    // 整体动作：主机在发起时锁定为筛选选定的那台（受理后不再随筛选变化）。
    cmdHostId.value = targetHostId.value
    pruneIncludeAll.value = false
    confirmState.value = {
      visible: true,
      kind: 'prune',
      action: 'image:prune',
      target: '',
      options: {}
    }
  }

  /** 拉取镜像（4b）：入口改开进度对话框 —— 逐层实时进度 + 取消，取代此前的
      prompt 黑盒等待（受理/轮询/重拉都收在对话框里，不走页面的 runWrite）。 */
  function askPull() {
    pullVisible.value = true
  }

  /** 构建镜像（P2 分发闭环）：入口开构建对话框 —— 表单（tag/上下文/Dockerfile/
      参数）+ 逐行播报 + 取消，受理/轮询/重拉都收在对话框里，不走页面的 runWrite
     （与拉取同一条分工：页面只出入口，长耗时写操作的生命周期归对话框）。 */
  function askBuild() {
    buildVisible.value = true
  }

  /**
   * 打标签：确认弹窗的**输入档**收集新引用（镜像引用格式校验、非法值禁提交）。
   * src 在打开时定住（选中那行的引用）；dst 由弹窗带回（payload.value）。
   * 协议对打标签不要求 confirm 值，target 也不发（主参数就是 src/dst）。
   */
  function askTag(image: DockerImageListItem) {
    cmdHostId.value = image.hostId
    confirmState.value = {
      visible: true,
      kind: 'tag',
      action: 'image:tag',
      target: actionRef(image),
      options: { src: actionRef(image) }
    }
  }

  /**
   * 导出 tar（两段时序，同一只确认弹窗内切换形态）：
   *   1) 输入档收集文件名 → 不带覆盖标记先发一次 —— 产物不存在时这就是常规路径；
   *   2) 结果 alreadyExists=true 时弹窗**就地**切到逐字档（照抄文件名确认覆盖），
   *      用户照抄后带 overwrite=true 重发（提交分支见 onConfirmSubmit 的 save 路径）。
   */
  function askSave(image: DockerImageListItem) {
    cmdHostId.value = image.hostId
    confirmState.value = {
      visible: true,
      kind: 'save',
      action: 'image:save',
      target: actionRef(image),
      options: {}
    }
  }

  /**
   * 载入镜像：确认弹窗的输入档收集文件名（「文件须已在 agent 下载目录」的提示
   * 在输入档的 hint 里，注册表条目给的）；主参数是文件名，无 target。
   */
  function askLoad() {
    cmdHostId.value = targetHostId.value
    confirmState.value = {
      visible: true,
      kind: 'load',
      action: 'image:load',
      target: '',
      options: {}
    }
  }

  /** 删除镜像：标准档确认；使用中的镜像不发指令（服务端也会拒绝），只给结论。 */
  function openRemove(image: DockerImageListItem) {
    if (image.inUse) {
      ElMessage.warning('镜像正被容器使用，需先删除相关容器后再删除镜像')
      return
    }
    cmdHostId.value = image.hostId
    confirmState.value = {
      visible: true,
      kind: 'remove',
      action: 'image:remove',
      target: actionRef(image),
      options: {}
    }
  }

  // ── 确认弹窗（清理 / 删除 / 打标签 / 导出两段 / 载入共用一个入口）──

  interface ConfirmState {
    visible: boolean
    /** 五种入口的提交分支不同：清理带勾选项、删除带 confirm、打标签/载入带输入值、导出分两段。 */
    kind: 'prune' | 'remove' | 'save' | 'tag' | 'load'
    action: string
    target: string
    options: Record<string, unknown>
  }

  const confirmState = ref<ConfirmState>({
    visible: false,
    kind: 'prune',
    action: '',
    target: '',
    options: {}
  })
  const confirmLoading = ref(false)
  /** 清理弹窗里的「同时清理未被任何容器使用的镜像」：勾选后请求带 all=true。 */
  const pruneIncludeAll = ref(false)

  async function onConfirmSubmit(payload: { confirm: string; force: boolean; value?: string }) {
    const st = confirmState.value
    confirmLoading.value = true
    try {
      if (st.kind === 'prune') {
        const options: Record<string, unknown> = pruneIncludeAll.value ? { all: true } : {}
        await runWrite('image:prune', undefined, options, payload.confirm)
      } else if (st.kind === 'remove') {
        await runWrite('image:remove', st.target, {}, payload.confirm)
      } else if (st.kind === 'tag') {
        // 输入档带回的新引用（弹窗已校验镜像引用格式）；src 在打开时定住，target 不发。
        await runWrite('image:tag', undefined, { src: st.options.src, dst: payload.value ?? '' })
      } else if (st.kind === 'load') {
        await runWrite('image:load', undefined, { filename: payload.value ?? '' })
      } else if (st.options.overwrite === true) {
        // 导出第二段：覆盖标记 + 文件名逐字确认一起带上，服务端逐字校验后才会覆盖
        //（payload.confirm 就是用户照抄的文件名 —— 弹窗校验已保证与 filename 一致）。
        const filename = String(st.options.filename ?? '')
        await runWrite('image:save', st.target, { filename, overwrite: true }, payload.confirm)
      } else {
        // 导出第一段：不带覆盖标记先发（文件名由输入档收集并校验过）。
        const filename = payload.value ?? ''
        const res = await run({ action: 'image:save', target: st.target, options: { filename } })
        if (res.ok) {
          reportResult(res)
          return
        }
        if (res.alreadyExists === true) {
          // 第二段：confirmState 换成带 overwrite 的新对象 —— 弹窗据 options 就地切到
          // 逐字档并清空输入（组件的形态 watch），用户须重新照抄文件名确认覆盖。
          confirmState.value = { ...st, options: { filename, overwrite: true } }
          return
        }
        reportResult(res)
      }
    } finally {
      confirmLoading.value = false
      // alreadyExists 分支已把 confirmState 换成第二段的新对象：弹窗要留在原地切形态，
      // 只有「状态还是本次受理的那份」时才关闭。
      if (confirmState.value === st) confirmState.value = { ...st, visible: false }
    }
  }

  // ── 行内操作 ──

  interface RowMenuItem {
    key: string
    label: string
    icon?: string
    auth?: string
    color?: string
    disabled?: boolean
  }

  /**
   * 行 ⋯ 菜单的条目。
   *
   * 只读的「详情」走 action-menu 的 extraItems（写动作的权限/禁用/🔒 规则都在它里面对，
   * 页面另拼一份等于两处各维护一遍）；写动作（扫描/打标签/导出/删除）走 actions，标签、
   * 权限、危险色全部取自动作注册表。扫描（P3·安全面）也走注册表条目：行内就近触发，
   * 分钟级的进行态由任务页呈现（见 onScanImage 的取舍注释）。
   *
   * 「使用中」的镜像不把删除放进 actions：action-menu 的 disabled 是**整组**禁用，表达不了
   * 「只禁删除」（打标签/导出/扫描对在用镜像依然合法），故删除改为 extraItems 里的**禁用条目**，
   * 结论写在条目上（禁用的条目点不动，结论只能在看得见的地方给）。这是基础设施的缺口。
   */
  function rowMenuItems(row: DockerImageListItem): {
    actions: string[]
    extraItems: RowMenuItem[]
  } {
    const extraItems: RowMenuItem[] = [
      { key: 'detail', label: '详情', icon: 'ri:eye-line', auth: PermDockerInspect }
    ]
    const actions = ['image:tag', 'image:save', 'image:scan']
    if (row.inUse) {
      const entry = lookupDockerAction('image:remove')
      extraItems.push({
        key: 'image:remove',
        label: '删除（需先删除使用该镜像的容器）',
        icon: entry?.icon ?? 'ri:delete-bin-5-line',
        auth: entry?.perm ?? PermDockerDelete,
        // 文字对比度 AA（收尾批）：原 --art-danger（白底 3.29）改走 token
        // （禁用态本可豁免，收进 token 层保持全族一档）。
        color: 'var(--aa-danger-text)',
        disabled: true
      })
    } else {
      actions.push('image:remove')
    }
    return { actions, extraItems }
  }

  function onRowMenu(row: DockerImageListItem, item: { action: string }) {
    switch (item.action) {
      case 'detail':
        openDetail(row)
        return
      case 'image:tag':
        askTag(row)
        return
      case 'image:save':
        askSave(row)
        return
      case 'image:scan':
        void onScanImage(row)
        return
      case 'image:remove':
        openRemove(row)
    }
  }

  /**
   * 安全扫描（P3·安全面）：行内触发 + 任务页看进度 —— **只受理、不轮询**。
   *
   * 为什么不像其它行内动作那样走 runWrite（受理 + 轮询到终态）：useDockerCmds 的
   * 在途伴随 busy（全局禁用操作栏），而真扫描以分钟计（trivy 首扫还要下载漏洞库）——
   * 把整页锁十几分钟换不来任何新信息；「pending 期间任务页可见」正是长任务的
   * 可见性要求（受理即留痕，终态结论句也落在任务页）。报告本体在镜像详情页的
   * 「安全」Tab 读取：再点一次扫描会命中服务端 24h 缓存秒回最近一次结果。
   */
  async function onScanImage(image: DockerImageListItem): Promise<void> {
    try {
      // 主机是**行主机**：跨主机表里扫描对象是「那台机器上的那个镜像」。
      await sendDockerCmd(image.hostId, { action: 'image:scan', target: actionRef(image) })
      ElMessage.success('已发起安全扫描，进度可在任务页查看')
    } catch (e) {
      // 受理期结论（离线/同目标已在执行/无权限）分类后给出：行内动作没有就地
      // 结论的容器，toast 是这条入口唯一的反馈位。
      ElMessage.error(classifyAcceptError(e).message)
    }
  }

  /** 详情：单主机窗口语义不变 —— host 取**行主机**（镜像属于那台机器）。 */
  function openDetail(row: DockerImageListItem) {
    void router.push({
      name: 'DockerImageDetail',
      // 镜像 id 带 `sha256:` 冒号，作为路由参数必须编码（详情页解码后回显短 id）。
      params: { id: encodeURIComponent(row.id) },
      query: { host: row.hostId }
    })
  }

  // ── 表格列 ──

  const columns = computed<ColumnOption<DockerImageListItem>[]>(() => {
    // 显式建立依赖：行内菜单的禁用态与权限显隐来自这些信号，而 formatter 要到表格
    // 渲染时才执行 —— 不在这里读一次，列配置就不会随它们变化而重算（表格会停在旧状态）。
    void pendingId.value
    void busy.value
    void canManage.value
    void canDelete.value
    // 列优先级：手机横屏（<768）只留「仓库:标签 / 使用 / 操作」与勾选列（「使用」是
    // 镜像的状态与回收决策依据）；序号与主机在平板竖屏起出现（跨主机表的行归属是
    // 排查第一线索）；大小、创建时间是元数据，低于桌面隐藏（底栏已给出合计，
    // 精确时刻在详情页）。数据列一律 minWidth，固定宽度只留给 selection/index/操作
    // 这类结构性列，见 responsive-columns.ts 的约定。
    return [
      // 勾选列只在有管理权限时出现：只有底栏的打标签/导出 tar 用得上它。
      ...(canManage.value ? [{ type: 'selection' as const, width: 46 }] : []),
      { type: 'index' as const, width: 60, label: '序号', hideBelow: 'tablet' },
      {
        prop: 'repoTags',
        label: '仓库:标签',
        minWidth: 240,
        showOverflowTooltip: true,
        formatter: (row: DockerImageListItem) => imageRefText(row)
      },
      {
        // 主机列是跨主机表的核心新增：同名镜像可能散在多台机器，排查时
        // 「先定位在哪台」是第一句（与容器统一表同一档：平板竖屏起可见）。
        prop: 'hostname',
        label: '主机',
        minWidth: 120,
        showOverflowTooltip: true,
        hideBelow: 'tablet',
        formatter: (row: DockerImageListItem) => row.hostname || row.hostId
      },
      {
        prop: 'sizeMb',
        label: '大小',
        minWidth: 110,
        // 大小是元数据（底栏合计里已有），低于桌面隐藏。
        hideBelow: 'desktop',
        formatter: (row: DockerImageListItem) => formatByUnit('MB', row.sizeMb)
      },
      {
        prop: 'created',
        label: '创建于',
        minWidth: 140,
        // 相对时间是元数据，精确时刻在详情页给。
        hideBelow: 'desktop',
        formatter: (row: DockerImageListItem) =>
          row.created ? formatRelativeTime(row.created) : '—'
      },
      {
        prop: 'inUse',
        label: '使用',
        minWidth: 150,
        showOverflowTooltip: true,
        formatter: (row: DockerImageListItem) => inUseText(row)
      },
      {
        prop: 'operation',
        label: '操作',
        width: 100,
        fixed: 'right' as const,
        formatter: (row: DockerImageListItem) => {
          const buttons: ReturnType<typeof h>[] = []
          if (hasAuth(PermDockerInspect)) {
            // 主操作「详情」常驻图标按钮，次要操作收进 ⋯（spec §11.0：不把详情埋进下拉）。
            buttons.push(
              h(ArtButtonTable, { type: 'view', title: '详情', onClick: () => openDetail(row) })
            )
          }
          if (canManage.value || canDelete.value) {
            const menu = rowMenuItems(row)
            buttons.push(
              h(DockerActionMenu, {
                actions: menu.actions,
                extraItems: menu.extraItems,
                target: actionRef(row),
                pendingId: pendingId.value,
                disabled: busy.value,
                onSelect: (item: { action: string; target: string }) => onRowMenu(row, item)
              })
            )
          }
          return buttons.length ? h('div', { class: 'flex items-center' }, buttons) : null
        }
      }
    ]
  })

  // 页面级刷新入口（resources.vue 的 hero 刷新按钮对当前 tab 调它）。
  defineExpose({ refresh, reloadAll })
</script>

<style lang="scss" scoped>
  @use '../../views/overview-tokens' as t;

  // 「清理悬空镜像…」等 plain danger 按钮的对比度 AA（浅色实测 2.87:1）：
  // EP 默认配色的病灶与处方见 overview-tokens 的 danger-plain-aa。
  @include t.danger-plain-aa;

  // 空态渲染在本组件（ArtTable 不转发 `#empty`）：给它接近表格空态的留白。
  .docker-empty {
    padding: 56px 0;
  }

  // 首拉失败的错误块：ElResult 自带留白，这里只补卡片内边距。
  .docker-tab-error {
    padding: 12px 0;
  }

  // 表头计数：辅助信息，小号次要色（与容器页的 docker-count 同款）。
  .docker-count {
    font-size: 12px;
    color: var(--el-text-color-secondary);
  }

  // 静默刷新失败标注：琥珀即「需要注意」（与模块内陈旧/离线标注同一套颜色语言）。
  .docker-refresh-note {
    margin-left: 10px;
    font-size: 12px;
    // 文字对比度 AA（收尾批）：原 el-color-warning（白底 1.85）改走 token，
    // 数字见 @styles/core/aa-text.scss。
    color: var(--aa-warning-text);
  }

  // 底栏合计：主行（可见清单的合计）与副行（后端账目的可回收量）同一行、副行弱化。
  .docker-total {
    display: flex;
    align-items: baseline;
    gap: 12px;
    padding: 12px 16px 0;
    font-size: 14px;

    &__sub {
      // Element Plus 的次要文字色：副行是补充信息，不与主数字争夺注意力。
      color: var(--el-text-color-secondary);
      font-size: 13px;
    }
  }

  // 底栏操作区：与合计同一套留白，按钮换行不挤压。
  .docker-bar {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;
    padding: 10px 16px 12px;

    &__hint {
      color: var(--el-text-color-secondary);
      font-size: 13px;
    }
  }

  // 清理弹窗里的补充勾选项：与上面的结论行拉开一点距离。
  .prune-all {
    display: flex;
    margin-top: 10px;
  }
</style>
