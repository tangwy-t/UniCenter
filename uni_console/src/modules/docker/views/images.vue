<template>
  <!-- 单根（single-root 守卫在库：布局的 Transition 只支持单根，双根切页白屏）。
       本页是「镜像」整页列表（原「镜像与存储」收敛页的镜像 tab 平移）：搜索栏在
       卡片外，动作工具条与表格在卡片内；确认/进度/凭据对话框随页收在同一根里。 -->
  <div class="docker-images-page art-full-height">
    <ArtSearchBar
      v-show="showSearchBar"
      v-model="searchForm"
      :items="searchItems"
      :default-expanded="true"
      @search="onSearch"
      @reset="onReset"
    />

    <ElCard class="art-table-card" shadow="never">
      <ArtTableHeader
        v-model:columns="columnChecks"
        v-model:showSearchBar="showSearchBar"
        :loading="loading"
        :has-active-filter="hasFilter"
        @refresh="refreshAll"
      >
        <template #left>
          <div class="toolbar-left">
            <!-- 常驻动作（权限门控：不渲染 ≠ 禁用）。**主机来源分两类**（跨主机表
                 没有「当前主机」）：无行目标的整体动作（拉取/构建/载入/清理）一律
                 **两段式** —— 第一屏选目标主机（主机筛选选定或全场只有一台时略过），
                 选定后才是业务弹窗（确认档 / 进度对话框）。按钮不因「没先选主机」变灰：
                 前置条件在流程第一屏回答。仓库凭据（4c）与镜像写动作不同档
                 （docker:config），不被 manage/delete 顺带挡掉。
                 **统一中性色**（Wave 3a 去彩虹）：色相是装饰而非信息 —— 统一主色档，
                 破坏性动作的危险语汇由确认弹窗承载（弹窗里仍是 danger 档）。 -->
            <ArtButtonTable
              v-if="canManage"
              type="add"
              :disabled="busy"
              :title="hostActionTitle('拉取镜像…')"
              @click="askPull"
            />
            <ArtButtonTable
              v-if="canManage"
              icon="ri:hammer-line"
              :iconClass="hostIconClass"
              :disabled="busy"
              :title="hostActionTitle('构建镜像…')"
              @click="askBuild"
            />
            <ArtButtonTable
              v-if="canManage"
              icon="ri:file-upload-line"
              :iconClass="hostIconClass"
              :disabled="busy"
              :title="hostActionTitle('载入镜像…')"
              @click="askLoad"
            />
            <ArtButtonTable
              v-if="canDelete"
              icon="ri:brush-line"
              :iconClass="hostIconClass"
              :disabled="busy"
              :title="hostActionTitle('清理悬空镜像…')"
              @click="openPrune"
            />
            <ArtButtonTable
              v-if="canConfig"
              icon="ri:key-2-line"
              iconClass="bg-theme/12 text-theme"
              title="仓库凭据…"
              @click="registryVisible = true"
            />

            <!-- 版本一致性视图入口（Wave 4b）：按 repoTag 聚合各主机的镜像摘要，报出
                 多数派/少数派（同名标签未必是同一个镜像）。与「任务中心/事件流」同款 ——
                 隐藏路由（isHide，不进侧边栏），入口在工具条；权限与镜像页读面同档
                 （docker:list），无权限不渲染。 -->
            <ArtButtonTable
              v-if="canList"
              icon="ri:git-branch-line"
              iconClass="bg-theme/12 text-theme"
              title="版本一致性"
              @click="goDrift"
            />

            <!-- 选中态动作（打标签 / 导出 tar 一次只作用于**一行**）：恒在版面上，
                 恰好选中一行且指令空闲时可点；其余时候变灰并说明理由 —— 不再
                 「勾多了按钮无提示消失」（多选的语义由表下的批量栏承载：勾中即出现，
                 给出批量删除 / 批量导出 tar）。 -->
            <ArtButtonTable
              v-if="canManage"
              icon="ri:price-tag-3-line"
              :iconClass="selectedIconClass"
              :disabled="busy || !singleSelected"
              :title="selectedActionTitle('打标签…')"
              @click="onTagSelected"
            />
            <ArtButtonTable
              v-if="canManage"
              icon="ri:file-download-line"
              :iconClass="selectedIconClass"
              :disabled="busy || !singleSelected"
              :title="selectedActionTitle('导出 tar…')"
              @click="onSaveSelected"
            />

            <!-- 硬数据文本：计数（total = 过滤后全量，翻页由分页器表达 —— 不再有
                 「列表显示前 N 条」那半句）+ 可见行合计容量 + 可回收（后端的主机级
                 账目求和，null = 没有任何主机报账 → 如实说「不可用」，不折算成 0）
                 + 不可用主机清单 + 静默刷新失败标注。
                 两处只在 pageState=ready（握有数据）时报 —— 首拉/失败/无可管主机时
                 报「共 0 个」是假话（数据面不得伪装新鲜）；刷新失败标注不受它管
                 （那是「上次数据仍在」的另一种事实）。 -->
            <span v-if="pageState === 'ready'" class="docker-count">
              共 {{ total }} 个镜像 · {{ formatByUnit('MB', totals.totalMB) }}
            </span>
            <span v-if="pageState === 'ready'" class="docker-total__sub">
              <template v-if="reclaim">
                {{ reclaim.count }} 个可回收 · {{ formatByUnit('MB', reclaim.mb) }}
              </template>
              <template v-else>可回收账目不可用</template>
            </span>
            <!-- 部分主机不可用（Wave 2）：只列主机名 + reason 原文，不加解释句。零值不渲染。 -->
            <span v-if="unavailableText" class="docker-refresh-note">{{ unavailableText }}</span>
            <span v-if="refreshError" class="docker-refresh-note">
              刷新失败，正在显示上次结果
            </span>
          </div>
        </template>
      </ArtTableHeader>

      <!-- 首拉失败（还没有任何行可给）：空态区里的紧凑失败态 —— 结论句 + 重试，
           不显示旧数据（失败不得伪装成新鲜）。 -->
      <ElEmpty v-if="pageState === 'error'" class="docker-empty" description="镜像清单获取失败">
        <ElButton size="small" @click="reloadAll">重试</ElButton>
      </ElEmpty>

      <template v-else>
        <!-- 两种空态分开：没有可管主机 vs 筛选没命中（后者给「清除筛选」）。
             纪律与容器页一致（「没有」与「筛没了」说成一句会让人以为机器空了）；
             空态渲染在本页、不写进 ArtTable 的 `#empty` 插槽：ArtTable 不转发该插槽
             （内部把 ElTable 的空态写死成「暂无数据」），写进去会被静默丢弃。 -->
        <ArtTable
          v-if="showTable"
          :loading="listLoading"
          :data="rows"
          :columns="columns"
          :pagination="pagination"
          @pagination:size-change="handleSizeChange"
          @pagination:current-change="handleCurrentChange"
          @sort-change="handleSortChange"
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

        <!-- 批量栏（资源无关组件：本页给的是镜像动作集与行投影）——勾中即出现，
             承载批量删除（逐字 DELETE 档，与容器页同强度）与批量导出 tar（逐行按
             镜像引用命名）。吸底贴表卡底缘不与分页器相叠（错位量由栏自量）。 -->
        <WorkloadBatchBar
          v-if="showTable"
          noun="镜像"
          :selected="batchRows"
          :actions="IMAGE_BATCH_ACTIONS"
          :busy="busy"
          :refresh="refreshSilent"
          @busy-change="batchBusy = $event"
        />
      </template>
    </ElCard>

    <!-- 确认弹窗覆盖五种入口：清理悬空（强档逐字 DELETE）、单删（标准档）、打标签
         （输入档收集新引用）、导出 tar（输入档收集文件名 → alreadyExists 时就地切
         文件名档的覆盖确认）、载入镜像（输入档收集文件名）。
         形态、逐字期望值、输入档的校验与提示全部由组件按动作注册表推导；页面只传
         事实与 options —— 目标主机不在本弹窗里（已由流程第一屏选定）。 -->
    <DockerActionConfirm
      v-model="confirmState.visible"
      :action="confirmState.action"
      :target="confirmState.target"
      :options="confirmState.options"
      target-kind="镜像"
      :loading="confirmLoading"
      @confirm="onConfirmSubmit"
    >
      <!-- 清理的补充输入（是否连带清理未使用镜像）由本页作为插槽内容给出。
           目标主机不在这里：四条整体动作（拉取/构建/载入/清理）同一套两段式，
           主机在**流程第一屏**（主机选择对话框）选定，弹窗只留业务内容。 -->
      <ElCheckbox v-if="confirmState.kind === 'prune'" v-model="pruneIncludeAll" class="prune-all">
        同时清理未被任何容器使用的镜像
      </ElCheckbox>
    </DockerActionConfirm>

    <!-- 拉取进度对话框（4b）：「拉取镜像…」的入口开逐层实时进度。
         hostId 在受理时钉死（actionHost = 发起时锁定的那台），成功关闭后重拉本页清单。 -->
    <PullProgressDialog v-model="pullVisible" :host-id="actionHost" :refresh="refreshSilent" />

    <!-- 构建进度对话框（P2 分发闭环）：「构建镜像…」的命令表单 + 逐行构建播报 + 取消。
         hostId 与重拉口径同拉取对话框（成功后新镜像要进列表）。 -->
    <BuildProgressDialog v-model="buildVisible" :host-id="actionHost" :refresh="refreshSilent" />

    <!-- 仓库凭据管理对话框（4c）：入口只对 docker:config 渲染（不渲染 ≠ 禁用）。
         凭据是**全局**的（不属于任何一台主机 —— 服务端按仓库地址解析注入），
         与主机筛选无关。 -->
    <RegistryCredentialsDialog v-model="registryVisible" />

    <!-- 四条整体动作（拉取/构建/载入/清理悬空）的**目标主机选择**：动作流程的第一屏
         —— 只在「没有已确定的主机」（没筛选且可管主机不止一台）时出现，其余情况
         按钮直达业务弹窗（进度对话框 / 确认档），路径短。 -->
    <ElDialog
      v-model="hostPick.visible"
      :title="hostPick.title"
      width="420px"
      :close-on-click-modal="false"
    >
      <div class="host-pick">
        <div class="host-pick__label">目标主机</div>
        <ElSelect
          v-model="hostPick.hostId"
          class="host-pick__select"
          filterable
          placeholder="选择主机"
        >
          <ElOption v-for="h in hostOptions" :key="h.value" :label="h.label" :value="h.value" />
        </ElSelect>
      </div>
      <template #footer>
        <ElButton @click="hostPick.visible = false">取消</ElButton>
        <ElButton type="primary" :disabled="!hostPick.hostId" @click="onHostPicked">继续</ElButton>
      </template>
    </ElDialog>
  </div>
</template>

<script setup lang="ts">
  /**
   * 镜像列表页（由「镜像与存储」收敛页的镜像 tab 平移为独立整页，菜单平铺）。
   *
   * 数据源 GET /docker/images（跨主机聚合、服务端过滤，三项 query 全部透传）——
   * 跨主机的表没法在单主机快照上筛。主机维度是**筛选下拉**的一项；行归属由主机列
   * 给出。`?host=` 深链作主机筛选初始值（单向：用户改筛选不回写 query）。
   *
   * 写操作按**行主机/选定主机**派发：行目标动作的指令通道 hostId 绑定该行
   * （useDockerCmds 给了 hostId 就不碰 provide 上下文）；无行目标的整体动作
   * （拉取/构建/载入/清理）一律**两段式**：先选目标主机（主机筛选已选定或全场
   * 只有一台时略过），选定后才是业务弹窗（进度对话框 / 确认档）—— 按钮不再因为
   * 「没先选主机」而点不动。下钻语义保持：详情仍是单主机窗口
   * —— `/docker/image-detail/:id?host=` 的 host 取**行主机**。
   */
  import { computed, h, reactive, ref, watch } from 'vue'
  import { useRoute, useRouter } from 'vue-router'
  import {
    ElButton,
    ElCard,
    ElCheckbox,
    ElDialog,
    ElEmpty,
    ElMessage,
    ElOption,
    ElSelect
  } from 'element-plus'
  import { formatByUnit } from '@/modules/device/utils/display'
  import { useAuth } from '@/hooks/core/useAuth'
  import { useTableColumns } from '@/hooks/core/useTableColumns'
  import {
    PermDockerConfig,
    PermDockerDelete,
    PermDockerInspect,
    PermDockerList,
    PermDockerManage
  } from '@/enums/permission'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtSearchBar from '@/components/core/forms/art-search-bar/index.vue'
  import ArtTable from '@/components/core/tables/art-table/index.vue'
  import ArtTableHeader from '@/components/core/tables/art-table-header/index.vue'
  import DockerActionConfirm from '../components/action-confirm.vue'
  import DockerActionMenu from '../components/action-menu.vue'
  import BuildProgressDialog from '../components/build-progress-dialog.vue'
  import PullProgressDialog from '../components/pull-progress-dialog.vue'
  import RegistryCredentialsDialog from '../components/registry-credentials-dialog.vue'
  import WorkloadBatchBar from '../components/workload-batch-bar.vue'
  import { IMAGE_BATCH_ACTIONS, type BatchRow } from '../components/batch-actions'
  import {
    fetchDockerImages,
    sendDockerCmd,
    type DockerImageListItem,
    type DockerImageListResp
  } from '../api'
  import {
    classifyAcceptError,
    runErrorMessage,
    useDockerCmds,
    type DockerCmdRunResult
  } from '../composables/useDockerCmds'
  import { useHostList, useResourceList } from '../composables/useResourceList'
  import { lookupDockerAction } from '../utils/actions'
  import { imageReclaimTotals, imageTotals } from '../utils/snapshot'
  import {
    formatRelativeTime,
    imageRefText,
    IMAGE_MID_HEAD,
    IMAGE_MID_TAIL,
    inUseText,
    midEllipsis
  } from '../utils/display'
  import { hostLabel } from '../utils/host'

  defineOptions({ name: 'DockerImages' })

  const route = useRoute()
  const router = useRouter()
  const { hasAuth } = useAuth()

  // 筛选面板**默认展开**：主机（整体动作的前置条件）与关键词都要看得见，
  // 收在面板里等于把前置条件藏起来（P1-5 的同一口径）。
  const showSearchBar = ref(true)
  const searchForm = ref<{
    keyword?: string
    danglingOnly?: boolean
    unusedOnly?: boolean
    host?: string
  }>({})
  /** 当前勾选的行：工具条「打标签 / 导出 tar」一次只作用于其中一行；批量动作用批量栏。 */
  const selected = ref<DockerImageListItem[]>([])
  /** 批量在途（批量栏 emit 上报）：行菜单据此禁用，防与批量并发提交同一目标。 */
  const batchBusy = ref(false)
  /** 拉取进度对话框的开关（4b）：hostId 取发起时的 actionHost（受理时钉死）。 */
  const pullVisible = ref(false)
  /** 构建进度对话框的开关（P2）：同拉取。 */
  const buildVisible = ref(false)
  /** 仓库凭据对话框的开关（4c）：入口受 canConfig 门控（模板里的 v-if）。 */
  const registryVisible = ref(false)

  /**
   * 拉取/构建发起时锁定的主机（受理后不再随筛选/弹窗变化）：一次指令只属于那台机器。
   * 这两条动作的进度对话框按 prop 收 hostId，故单立一个 ref 供其读取（prune/load
   * 走确认弹窗，主机在入口写进指令通道的 cmdHostId）。
   */
  const actionHost = ref('')

  /** 整体动作的目标主机选择对话框（四条整体动作共用的流程第一屏）。 */
  const hostPick = ref<{
    visible: boolean
    title: string
    hostId: string
    next: ((hostId: string) => void) | null
  }>({ visible: false, title: '', hostId: '', next: null })
  function openHostPick(title: string, next: (hostId: string) => void) {
    hostPick.value = { visible: true, title, hostId: targetHostId.value, next }
  }
  function onHostPicked() {
    const { hostId, next } = hostPick.value
    hostPick.value = { ...hostPick.value, visible: false, next: null }
    if (hostId && next) next(hostId)
  }

  /**
   * 批量栏的行投影：镜像条目没有 `name`（名字要从 repoTags 推），且「使用中」只挡
   * 删除（导出照常）—— 两条资源侧的事实都在这里投影，批量栏本身不认识镜像。
   */
  const batchRows = computed<BatchRow[]>(() =>
    selected.value.map((row) => ({
      hostId: row.hostId,
      hostname: row.hostname,
      name: actionRef(row),
      protected: false,
      blocked: (action) =>
        action === 'image:remove' && row.inUse ? '镜像正被容器使用，需先删除相关容器' : null
    }))
  )

  const canManage = computed(() => hasAuth(PermDockerManage))
  const canDelete = computed(() => hasAuth(PermDockerDelete))
  /** 仓库凭据管理权限（4c）：与镜像写动作不同档 —— 服务端 /docker/registries 的静态 perm。 */
  const canConfig = computed(() => hasAuth(PermDockerConfig))
  /** 版本一致性视图（Wave 4b）入口权限：与 GET /docker/image-drift 及该隐藏路由同档（docker:list）。 */
  const canList = computed(() => hasAuth(PermDockerList))

  // ── 可管主机清单（筛选下拉与「尚无可管主机」判定）────────────────────
  const { hosts, hostsLoading, loadHosts } = useHostList()

  // ── 数据源（跨主机聚合端点；服务端过滤，参数从筛选表单现取）──────────

  /**
   * 可回收账目（后端 disk 数组，口径见 DockerImageListResp.Disk）：工具条副行的
   * 数据面。它与 rows 同一响应 —— 但 useResourceList 的 seq 守卫只守着 rows/total，
   * 这条走自己的 seq（同一「最新发起者胜出」纪律）：否则并发重拉时旧响应晚到，
   * 账目会跟当前行换批，两个数字从此对不上。
   */
  const imageDisk = ref<DockerImageListResp['disk']>([])
  let diskSeq = 0

  const {
    rows,
    total,
    unavailableHosts,
    loading,
    refreshError,
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
        unused: p.unused ? true : undefined,
        // 服务端分页/排序：pageSize 显式传；未选定排序时不发两键。
        page: p.page,
        pageSize: p.pageSize,
        sortBy: p.sortBy as 'size' | 'created' | 'hostId' | 'name' | undefined,
        sortOrder: p.sortBy ? p.sortOrder : undefined
      })
      if (seq === diskSeq) imageDisk.value = resp.disk ?? []
      return resp
    },
    // UI 的 key（danglingOnly/unusedOnly）在这里映射成端点参数（dangling/unused）——
    // 不让筛选项的 key 泄漏进数据层（与容器页同一句分工）。分页/排序态一并在快照里现取。
    params: () => ({
      hostId: searchForm.value.host,
      keyword: searchForm.value.keyword,
      dangling: searchForm.value.danglingOnly === true ? true : undefined,
      unused: searchForm.value.unusedOnly === true ? true : undefined,
      page: pagination.current,
      pageSize: pagination.size,
      sortBy: sortBy.value || undefined,
      sortOrder: sortBy.value ? sortOrder.value : undefined
    }),
    hosts: () => hosts.value,
    hostsLoading: () => hostsLoading.value
  })

  // ── 服务端分页 / 排序（Wave 2）────────────────────────────────────
  // 分页态照 device：page/pageSize 显式发（pageSize 取 50，与后端默认一致）；载入后
  // total 从响应同步。排序只认白名单列（列 prop → 后端 sortBy 键），未知 prop 忽略。
  const pagination = reactive({ current: 1, size: 50, total: 0 })
  watch(total, (v) => {
    pagination.total = v
  })
  /** 列 prop → 后端排序键（白名单：size/created + 通用 hostId/name）。 */
  const SORT_KEYS: Record<string, string> = {
    repoTags: 'name',
    hostname: 'hostId',
    sizeMb: 'size',
    created: 'created'
  }
  const sortBy = ref('')
  const sortOrder = ref<'asc' | 'desc'>('asc')
  function handleSortChange({ prop, order }: { prop?: string; order?: string | null }) {
    const key = prop ? SORT_KEYS[prop] : undefined
    if (!key) return
    if (order === 'ascending') {
      sortBy.value = key
      sortOrder.value = 'asc'
    } else if (order === 'descending') {
      sortBy.value = key
      sortOrder.value = 'desc'
    } else {
      // 三态循环的第三态：清回默认排序（sortBy 空即忽略方向）。
      sortBy.value = ''
    }
    pagination.current = 1
    void load()
  }
  function handleSizeChange(size: number) {
    pagination.size = size
    pagination.current = 1
    void load()
  }
  function handleCurrentChange(current: number) {
    pagination.current = current
    void load()
  }

  /** 部分主机不可用（Wave 2）：只列主机名 + reason 原文（页面零解释文案）。 */
  const unavailableText = computed(() =>
    unavailableHosts.value.length === 0
      ? ''
      : `${unavailableHosts.value.length} 台主机不可用：` +
        unavailableHosts.value.map((h) => `${h.hostname || h.hostId}（${h.reason}）`).join('、')
  )

  /** 表格的加载态：清单在拉，或主机清单还没到（后者尚不知有没有主机，不能先喊「没有」）。 */
  const listLoading = computed(() => loading.value || hostsLoading.value)

  /** 有命中行就渲染表格；纯加载中也用表格的 loading 遮罩，空态只在加载结束后判断。 */
  const showTable = computed(() => listLoading.value || rows.value.length > 0)

  // `?host=&keyword=` 深链（总览磁盘面板 + 全局检索下钻的既有链路）：query 里的主机
  // 与关键词作为**筛选初始值** —— 单向（用户改筛选不回写 query，两者从此是筛选状态
  // 而不是页面状态）。watch 同时兜住 worktab 缓存实例的「带 host 返回」场景（镜像
  // 详情返回链路会回到带 host 的本页，缓存实例经它跟上新 host）。
  let deepLinkInited = false
  watch(
    [() => route.query.host, () => route.query.keyword],
    ([rawHost, rawKeyword]) => {
      const id = rawHost ? String(rawHost) : ''
      const keyword = rawKeyword ? String(rawKeyword) : undefined
      const had = searchForm.value.host ?? ''
      const hadKeyword = searchForm.value.keyword ?? ''
      searchForm.value.host = id
      searchForm.value.keyword = keyword
      // 深链进入时自动展开搜索栏：从总览磁盘面板/检索结果下钻进来的人看到的是一张
      // 被过滤的表，筛选线索必须在明处。immediate 首跑也走这句。
      if (id || keyword) showSearchBar.value = true
      // 筛选变化（含深链首拉）回第一页。
      pagination.current = 1
      // 首跑（deepLinkInited=false）必拉一次；此后仅 query 真的变化时重拉 ——
      // 没深链时两值都是空，靠它区分「首次进入」与「同值重复」。
      if (id !== had || keyword !== hadKeyword || !deepLinkInited) void load()
      deepLinkInited = true
    },
    { immediate: true }
  )

  // 主机选项按分组渲染（ArtSearchBar 追加式分组能力：option 带 group 即分组）。
  // 无分组的归入「未分组」组；组名是数据分类，不是解释文案。
  const hostOptions = computed(() =>
    hosts.value.map((h) => ({ label: hostLabel(h), value: h.id, group: h.group || '未分组' }))
  )

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
      // 主机维度是筛选项：全部主机 + 各台。
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

  /** 工具条合计主行：与表格里的行同源（服务端过滤后的这批；截断口径见计数文本）。 */
  const totals = computed(() => imageTotals(rows.value))

  /** 工具条可回收副行：后端主机级账目的求和（null = 没有任何主机报账 → 说「不可用」）。 */
  const reclaim = computed(() => imageReclaimTotals(imageDisk.value))

  function onSearch() {
    // 服务端过滤：搜索按钮即重拉，筛选变化回第一页。
    showSearchBar.value = true
    pagination.current = 1
    void load()
  }

  function onReset() {
    searchForm.value = {}
    pagination.current = 1
    void load()
  }

  /**
   * 工具条刷新（内置钮）：刷新 = 把眼前这页看新 —— 主机筛选源与清单一起重拉
   * （主机可能刚入库，只看清单会留下过期的筛选源；旧资源页 hero 的刷新即此语义）。
   * 清单走非静默 refresh：表头 loading 语义不变。
   */
  function refreshAll() {
    void loadHosts()
    void refresh()
  }

  // ── 工具条写操作的输入 ──

  const singleSelected = computed(() => selected.value.length === 1)

  // ── 主机来源（跨主机表的写操作按发起时锁定的主机派发）───────────────

  /**
   * 整体动作（清理/拉取/构建/载入）的**已定主机**：主机筛选选定的那台；没选且全场
   * 只有一台可管主机时就是它（不让人先做无意义的单选）；否则留空 —— 留空**不禁用
   * 按钮**，动作照常发起，只是流程第一屏是主机选择（withTargetHost）。
   */
  const targetHostId = computed(() => {
    if (searchForm.value.host) return searchForm.value.host
    return hosts.value.length === 1 ? hosts.value[0]!.id : ''
  })

  /** 主机尚未确定时的整钮弱化（动作仍可点：点了先选主机 —— 不是死控件）。 */
  const hostIconClass = computed(() =>
    targetHostId.value ? 'bg-theme/12 text-theme' : 'bg-theme/12 text-theme opacity-45'
  )

  /** 选中态动作的图标：未恰好选中一行时弱化（同样的「可点点看」纪律）。 */
  const selectedIconClass = computed(() =>
    singleSelected.value ? 'bg-theme/12 text-theme' : 'bg-theme/12 text-theme opacity-45'
  )

  /** 指令在途的禁用理由（title 后缀）。 */
  const BUSY_REASON = '指令在途，暂不可用'

  /** 整体动作的 title：只说动作名（主机缺失由流程第一屏回答，不写解释句）。 */
  function hostActionTitle(label: string): string {
    if (busy.value) return `${label}（${BUSY_REASON}）`
    return label
  }

  /** 选中态动作（打标签/导出 tar）的 title：一次只作用于一行，故说明何时可点。 */
  function selectedActionTitle(label: string): string {
    if (busy.value) return `${label}（${BUSY_REASON}）`
    if (!singleSelected.value) return `${label}（一次作用于 1 行；多行请用批量栏）`
    return label
  }

  /**
   * 指令通道的主机来源：行目标动作在**入口处锁定到该行的 hostId**，无行目标的整体
   * 动作（清理/载入）在流程第一屏选定后同样在入口写死（useDockerCmds 在 run 入口
   * 求值，换 ref 即切主机）；同表不同机的行各发各的主机，不再有「页面当前主机」的
   * 概念。一次操作属于发起时锁定的那台主机。
   */
  const cmdHostId = ref('')
  const { run, pendingId, busy } = useDockerCmds({
    hostId: () => cmdHostId.value,
    refresh: refreshSilent
  })

  // ── 勾选与工具条选择入口 ──

  function onSelectionChange(rowsSel: DockerImageListItem[]) {
    selected.value = rowsSel
  }

  /** 工具条打标签/导出 tar：输入是选中的那一行（未选中时按钮不出现，这里再兜一道）。 */
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

  // ── 工具条动作 ──

  /** 清理悬空镜像：第一条是选目标主机（与拉取/构建同套两段式），选定后开强档确认
      弹窗（组件按注册表给逐字 DELETE 形态），勾选项决定是否连带清理未使用镜像。 */
  function openPrune() {
    withTargetHost('清理悬空镜像：选择目标主机', (hostId) => {
      cmdHostId.value = hostId
      pruneIncludeAll.value = false
      confirmState.value = {
        visible: true,
        kind: 'prune',
        action: 'image:prune',
        target: '',
        options: {}
      }
    })
  }

  /**
   * 无行目标动作的**两段式入口**（四条整体动作共用）：已定主机（主机筛选 / 唯一
   * 主机）时流程第一屏略过，直达业务弹窗；否则先开主机选择的那一屏，选定后走 next
   * （业务弹窗）。选择只发生在缺主机时，不缺就是一步到位。
   */
  function withTargetHost(title: string, next: (hostId: string) => void) {
    if (busy.value) return
    if (targetHostId.value) {
      next(targetHostId.value)
      return
    }
    openHostPick(title, next)
  }

  /** 拉取镜像（4b）：先定目标主机（已定则直达），再开进度对话框 —— 逐层实时进度 +
      取消，受理/轮询/重拉都收在对话框里，不走页面的 runWrite。 */
  function askPull() {
    withTargetHost('拉取镜像：选择目标主机', (hostId) => {
      actionHost.value = hostId
      pullVisible.value = true
    })
  }

  /** 构建镜像（P2 分发闭环）：先定目标主机（已定则直达），再开构建对话框 —— 表单
      （tag/上下文/Dockerfile/参数）+ 逐行播报 + 取消，受理/轮询/重拉都收在对话框里，
      不走页面的 runWrite（与拉取同一条分工：页面只出入口，长耗时写操作的生命周期归
      对话框）。 */
  function askBuild() {
    withTargetHost('构建镜像：选择目标主机', (hostId) => {
      actionHost.value = hostId
      buildVisible.value = true
    })
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
   * 载入镜像：第一条是选目标主机（与拉取/构建/清理同套两段式），选定后开确认弹窗的
   * 输入档收集文件名（「文件须已在 agent 下载目录」的提示在输入档的 hint 里，注册表
   * 条目给的）；主参数是文件名，无 target。
   */
  function askLoad() {
    withTargetHost('载入镜像：选择目标主机', (hostId) => {
      cmdHostId.value = hostId
      confirmState.value = {
        visible: true,
        kind: 'load',
        action: 'image:load',
        target: '',
        options: {}
      }
    })
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
        // 目标主机已在流程第一屏写进指令通道（cmdHostId），这里只补动作参数。
        const options: Record<string, unknown> = pruneIncludeAll.value ? { all: true } : {}
        await runWrite('image:prune', undefined, options, payload.confirm)
      } else if (st.kind === 'remove') {
        await runWrite('image:remove', st.target, {}, payload.confirm)
      } else if (st.kind === 'tag') {
        // 输入档带回的新引用（弹窗已校验镜像引用格式）；src 在打开时定住，target 不发。
        await runWrite('image:tag', undefined, { src: st.options.src, dst: payload.value ?? '' })
      } else if (st.kind === 'load') {
        // 同清理：hostId 在主机选择那一屏已钉死。
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

  /**
   * 版本一致性视图（Wave 4b）：跨主机按 repoTag 聚合的漂移面。隐藏路由（无菜单入口），
   * 站内跳转一律 path 形态（菜单页注册名是 menu-<menuId>，只有隐藏路由保名 —— 而本路由
   * 保名也照 path 走，与 tasks/events 的入口同款）。
   */
  function goDrift() {
    void router.push({ path: '/docker/image-drift' })
  }

  // ── 批量栏吸底 × 分页器 ──────────────────────────────────────────
  // 吸底块与分页器的错位量由**批量栏自己量**（只有栏知道自己在哪个滚动容器里，
  // 口径见 batch-actions 的 measureBatchOffset）—— 页面不再参与定位。

  // ── 表格列 ──

  // 列设置经 useTableColumns（对齐系统管理，列弹层可用）：工厂只在 setup 求值一次
  //（权限在会话内不变化）；行内菜单的禁用态/权限显隐由 formatter 在渲染时读取
  // pendingId/busy —— 每次指令完成后 refreshSilent 重拉清单，表格随之重渲染。
  const { columns, columnChecks } = useTableColumns<DockerImageListItem>(() => {
    // 列优先级：手机横屏（<768）只留「仓库:标签 / 使用 / 操作」与勾选列（「使用」是
    // 镜像的状态与回收决策依据）；序号与主机在平板竖屏起出现（跨主机表的行归属是
    // 排查第一线索）；大小、创建时间是元数据，低于桌面隐藏（工具条已给出合计，
    // 精确时刻在详情页）。数据列一律 minWidth，固定宽度只留给 selection/index/操作
    // 这类结构性列，见 responsive-columns.ts 的约定。
    return [
      // 勾选列在有写权限时出现：工具条的打标签/导出 tar（单行）与批量栏（多行）都用它。
      ...(canManage.value || canDelete.value ? [{ type: 'selection' as const, width: 46 }] : []),
      { type: 'index' as const, width: 60, label: '序号', hideBelow: 'tablet' },
      {
        prop: 'repoTags',
        label: '仓库:标签',
        minWidth: 240,
        // 单行不变量（Wave 3c）：3a 只留 midEllipsis、漏了 nowrap —— 窄视口下这条
        // 会折行堆高行。补回 showOverflowTooltip（EP 负责 nowrap/省略号），
        // midEllipsis 负责可见那截（仓库前缀 + :tag），全文由 title 承载。
        showOverflowTooltip: true,
        sortable: 'custom' as const,
        // 中段省略（Wave 3a）：尾部省略会切掉 tag（唯一有信息的那段），改保留头+尾
        // （仓库前缀 + `:tag`）。
        formatter: (row: DockerImageListItem) => {
          const full = imageRefText(row)
          return h('span', { title: full }, midEllipsis(full, IMAGE_MID_HEAD, IMAGE_MID_TAIL))
        }
      },
      {
        // 主机列是跨主机表的核心：同名镜像可能散在多台机器，排查时
        // 「先定位在哪台」是第一句（与容器统一表同一档：平板竖屏起可见）。
        // 渲染成胶囊 chip：主机是事实上的分组键，不再是最弱的灰文本。
        prop: 'hostname',
        label: '主机',
        minWidth: 120,
        showOverflowTooltip: true,
        hideBelow: 'tablet',
        sortable: 'custom' as const,
        formatter: (row: DockerImageListItem) =>
          h('span', { class: 'docker-host' }, hostLabel({ id: row.hostId, hostname: row.hostname }))
      },
      {
        prop: 'sizeMb',
        label: '大小',
        minWidth: 110,
        // 单行不变量（Wave 3c）：数据列一律 nowrap。
        showOverflowTooltip: true,
        align: 'right' as const,
        className: 'is-num',
        // 大小是元数据（工具条合计里已有），低于桌面隐藏。
        hideBelow: 'desktop',
        sortable: 'custom' as const,
        formatter: (row: DockerImageListItem) => formatByUnit('MB', row.sizeMb)
      },
      {
        prop: 'created',
        label: '创建于',
        minWidth: 140,
        // 单行不变量（Wave 3c）：数据列一律 nowrap。
        showOverflowTooltip: true,
        // 相对时间是元数据，精确时刻在详情页给。
        hideBelow: 'desktop',
        sortable: 'custom' as const,
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
                // 指令在途或批量在途都禁用行菜单：防与批量并发提交同一目标。
                disabled: busy.value || batchBusy.value,
                onSelect: (item: { action: string; target: string }) => onRowMenu(row, item)
              })
            )
          }
          return buttons.length ? h('div', { class: 'flex items-center' }, buttons) : null
        }
      }
    ]
  })
</script>

<style lang="scss" scoped>
  @use './overview-tokens' as t;

  // 主机 chip（胶囊）：主机是事实上的分组键，跨主机镜像表里不再是最弱的灰文本
  // （与项目/容器/卷/网络页同一全局类 docker-host + t.pill 制）。
  :global(.docker-host) {
    @include t.pill;
  }

  // 目标主机选择（四条整体动作共用的流程第一屏，挂在页面自己的对话框里）。
  // **挂在顶层**：对话框内容可能被 teleport 到 body，嵌在 .docker-images-page 下的
  // 规则会因祖先不在而失配（scoped 只剩末尾那截属性选择器）。
  .host-pick {
    &__label {
      margin-bottom: 6px;
      font-size: 13px;
    }

    &__select {
      width: 100%;
    }
  }

  .docker-images-page {
    /* 次要文字对比度 AA（逐页口径，与收敛页同款处置）：EP 默认
       --el-text-color-secondary(#909399) 对白底只有 3.08:1，低于 AA 正文线 →
       页面范围内把它升到 regular 档（浅色 6.1:1、暗色随主题同样达标）。 */
    --el-text-color-secondary: var(--el-text-color-regular);

    // 工具条：动作钮 + 硬数据文本同排，换行不挤压。
    .toolbar-left {
      display: flex;
      align-items: center;
      gap: 8px;
      flex-wrap: wrap;
    }

    // 表头计数：辅助信息，小号次要色。
    .docker-count {
      font-size: 12px;
      color: var(--el-text-color-secondary);
    }

    // 可回收（副行）随工具条同排、弱化。合计容量已并入计数句（.docker-count），
    // 不再是独立一行（去重后的新形态）。
    .docker-total__sub {
      font-size: 12px;
      color: var(--el-text-color-secondary);
    }

    // 静默刷新失败标注：琥珀即「需要注意」（与模块内陈旧/离线标注同一套颜色语言）。
    .docker-refresh-note {
      font-size: 12px;
      // 文字对比度 AA（收尾批）：原 el-color-warning（白底 1.85）改走 token，
      // 数字见 @styles/core/aa-text.scss。
      color: var(--aa-warning-text);
    }

    // 空态渲染在本页（ArtTable 不转发 `#empty`）：给它接近表格空态的留白。
    .docker-empty {
      padding: 56px 0;
    }

    // 清理弹窗里的补充勾选项：与上面的结论行拉开一点距离。
    .prune-all {
      display: flex;
      margin-top: 10px;
    }
  }
</style>
