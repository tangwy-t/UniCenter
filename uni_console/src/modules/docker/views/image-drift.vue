<template>
  <!-- 单根（single-root 守卫在库：布局的 Transition 只支持单根，双根切页白屏）。
       本页是「版本一致性」整页列表（隐藏路由，从镜像页工具条进）：搜索栏在卡片外，
       动作工具条与表格在卡片内 —— 与五个列表页同一形态。 -->
  <div class="docker-image-drift-page art-full-height">
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
            <!-- 返回上一页：本页没有菜单入口（从镜像页工具条进），返回键就是它的出口。 -->
            <ArtButtonTable
              icon="ri:arrow-left-line"
              iconClass="bg-g-300/55 text-g-700"
              title="返回上一页"
              @click="back"
            />
            <!-- 硬数据文本：计数（total = 过滤后全量，翻页由分页器表达）+ 不可用主机
                 清单 + 静默刷新失败标注。只在 pageState=ready（握有数据）时报 ——
                 首拉/失败/无可管主机时报「共 0 个」是假话（数据面不得伪装新鲜）。 -->
            <span v-if="pageState === 'ready'" class="docker-count">
              共 {{ total }} 个仓库标签
            </span>
            <!-- 部分主机不可用（Wave 2）：**本页最关键的诚实性设计** —— 采集失败的主机
                 是一次「数据不全」的硬事实，不能让「9 台一致」掩盖「第 10 台没算进来」。
                 只列主机名 + reason 原文，不加解释句（页面零解释文案）。零值不渲染。 -->
            <span v-if="unavailableText" class="docker-refresh-note">{{ unavailableText }}</span>
            <span v-if="refreshError" class="docker-refresh-note">
              刷新失败，正在显示上次结果
            </span>
          </div>
        </template>
      </ArtTableHeader>

      <!-- 首拉失败（还没有任何行可给）：空态区里的紧凑失败态 —— 结论句 + 重试，
           不显示旧数据（失败不得伪装成新鲜）。 -->
      <ElEmpty v-if="pageState === 'error'" class="docker-empty" description="版本一致性获取失败">
        <ElButton size="small" @click="reloadAll">重试</ElButton>
      </ElEmpty>

      <template v-else>
        <!-- 两种空态分开：没有可管主机 vs 筛选没命中（后者给「清除筛选」）。
             纪律与镜像/容器页一致（「没有」与「筛没了」说成一句会让人以为机器空了）；
             空态渲染在本页、不写进 ArtTable 的 `#empty` 插槽（ArtTable 不转发该插槽）。 -->
        <ArtTable
          v-if="showTable"
          :loading="listLoading"
          :data="rows"
          :columns="columns"
          :pagination="pagination"
          @pagination:size-change="handleSizeChange"
          @pagination:current-change="handleCurrentChange"
          @sort-change="handleSortChange"
        />
        <ElEmpty
          v-else-if="pageState === 'empty'"
          class="docker-empty"
          description="尚无可管主机"
        />
        <ElEmpty
          v-else-if="hasFilter"
          class="docker-empty"
          description="没有符合筛选条件的仓库标签"
        >
          <ElButton size="small" @click="onReset">清除筛选</ElButton>
        </ElEmpty>
        <ElEmpty v-else class="docker-empty" description="还没有镜像" />

        <!-- 分发进度（Wave 4c）：逐台待对齐主机派发 image:pull 的行内账目 —— 形态照批量栏
             （进行行 + 逐行结论 + 失败重试），随分发出现、放表下。一轮拉取后重拉漂移逐台
             复核：收敛到目标报「已对齐」、未收敛如实报实际摘要，完成态报「已对齐 / 仍是
             其他摘要 / 失败」三档；拉取命令失败项可只对失败集重发一轮。 -->
        <div v-if="dispatchRunning || dispatchDone" class="drift-dispatch">
          <div v-if="dispatchCurrent" class="drift-dispatch__current">
            正在执行 {{ dispatchCurrent.host }}…（{{ dispatchCurrent.done }}/{{
              dispatchCurrent.total
            }}）
          </div>
          <div v-else-if="dispatchVerifying" class="drift-dispatch__current">正在复核对齐…</div>
          <div
            v-for="(note, i) in dispatchNotes"
            :key="i"
            class="drift-dispatch__note"
            :class="{ 'is-failed': note.failed }"
          >
            {{ note.text }}
          </div>
          <div class="drift-dispatch__foot">
            <ElButton
              v-if="dispatchRunning"
              size="small"
              :disabled="dispatchStopping"
              @click="stopDispatch"
            >
              停止后续分发
            </ElButton>
            <template v-else>
              <span
                class="drift-dispatch__summary"
                :class="{ 'is-failed': dispatchFailed.length > 0 || dispatchNotAligned > 0 }"
              >
                {{ dispatchSummaryText }}
              </span>
              <ElButton v-if="dispatchFailed.length" size="small" @click="retryFailed">
                重试失败的 {{ dispatchFailed.length }} 台
              </ElButton>
              <ElButton size="small" link @click="clearDispatch">关闭</ElButton>
            </template>
          </div>
        </div>
      </template>
    </ElCard>

    <!-- 分发 / 取消目标的确认档（action-confirm 形制）：分发的动作语义是**按标签拉取**
         （在待对齐主机上拉取该标签、收敛到仓库当前提供的内容），列出影响面主机名，并如实
         区分「更新」与「新拉」（本机已有该标签的主机是更新、无该标签的是新拉取）；取消目标
         如实说明只停判定、不回退已拉取的镜像。 -->
    <DockerActionConfirm
      v-model="confirm.visible"
      action="image:pull"
      :target="confirm.row?.repoTag ?? ''"
      :override="confirmOverride"
      :loading="dispatchRunning"
      @confirm="onConfirmAction"
    >
      <template v-if="confirm.kind === 'distribute' && confirm.row">
        <div class="drift-impact">
          <span v-for="h in confirm.row.pendingHosts ?? []" :key="h.hostId" class="docker-host">
            {{ h.hostname || h.hostId }}
          </span>
        </div>
      </template>
    </DockerActionConfirm>

    <!-- 声明 / 修改目标版本的小对话框：候选摘要点选 + 粘贴两条路。 -->
    <ImageTargetDialog v-model="targetDialogVisible" :row="targetRow" @saved="onTargetSaved" />
  </div>
</template>

<script lang="ts">
  /**
   * 分发落定复核的时间缝（毫秒）：一轮分发结束后，等 agent 的采帧到达 core 缓存再重拉
   * 漂移逐台比对。默认 1500 与 useDockerCmds 的 settleMs 同档；单测把它置 0 立即复核。
   */
  export const dispatchTiming = { verifySettleMs: 1500 }
</script>

<script setup lang="ts">
  /**
   * 镜像版本一致性（漂移）视图（Wave 4b，隐藏路由 `/docker/image-drift`）。
   *
   * 立身之本：同名 repoTag 在不同主机上**未必是同一个镜像摘要**（digest）—— 本页
   * 按标签聚合，报出多数派（dominantDigest + 其主机数）与少数派（outliers：每种
   * 摘要落在哪些主机），让「版本不齐」一眼可见。默认服务端按 distinctDigests 降序，
   * 最不齐的排最前。
   *
   * 数据源 GET /docker/image-drift（跨主机、服务端过滤/分页/排序）—— keyword（repoTag
   * 子串）、group（主机分组，单值精确）两项筛选。契约的 `summary` 字段恒空：一句话
   * 口径由本页按结构化字段自拼（driftSummaryText），只陈述硬事实、无解释句。
   *
   * 入口在**镜像页工具条**（ArtButtonTable 图标钮），与任务中心/事件流同款——
   * 不加后端菜单种子，路由取 isHide 形态（理由见 index.ts）。
   */
  import { computed, h, onMounted, reactive, ref, watch } from 'vue'
  import { useRouter } from 'vue-router'
  import { ElButton, ElCard, ElEmpty, ElMessage } from 'element-plus'
  import { useAuth } from '@/hooks/core/useAuth'
  import { PermDockerManage } from '@/enums/permission'
  import { useTableColumns } from '@/hooks/core/useTableColumns'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtSearchBar from '@/components/core/forms/art-search-bar/index.vue'
  import ArtTable from '@/components/core/tables/art-table/index.vue'
  import ArtTableHeader from '@/components/core/tables/art-table-header/index.vue'
  import DockerActionConfirm from '../components/action-confirm.vue'
  import ImageTargetDialog from '../components/image-target-dialog.vue'
  import {
    clearImageTarget,
    fetchImageDrift,
    type DockerImageDriftHostRef,
    type DockerImageDriftItem,
    type DockerImageDriftSortKey
  } from '../api'
  import { runErrorMessage, useDockerCmds } from '../composables/useDockerCmds'
  import { useHostList, useResourceList } from '../composables/useResourceList'
  import type { ConfirmOverride } from '../utils/confirm'
  import { IMAGE_MID_HEAD, IMAGE_MID_TAIL, midEllipsis } from '../utils/display'
  import { hostLabel } from '../utils/host'

  defineOptions({ name: 'DockerImageDrift' })

  const router = useRouter()

  /** 返回上一页：本页没有菜单入口（从镜像页工具条进），返回键就是它的出口。 */
  function back() {
    router.back()
  }

  const showSearchBar = ref(false)
  const searchForm = ref<{ keyword?: string; group?: string }>({})

  // ── 可管主机清单（分组筛选选项与「尚无可管主机」判定）──────────────────
  const { hosts, hostsLoading, loadHosts } = useHostList()

  // ── 数据源（跨主机聚合端点；服务端过滤/分页/排序）─────────────────────
  // 分页/排序态与筛选一起，由 params 快照透传；`group` 不在共享的
  // ResourceQueryParams 形状里（那是四个资源清单端点的公共面），故在 fetcher 里
  // 从筛选表单**现取** —— 数据层不为此改公共类型（本波不得触碰 useResourceList）。
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
  } = useResourceList<DockerImageDriftItem>({
    fetcher: (p) =>
      fetchImageDrift({
        keyword: p.keyword || undefined,
        group: searchForm.value.group || undefined,
        page: p.page,
        pageSize: p.pageSize,
        sortBy: p.sortBy as DockerImageDriftSortKey | undefined,
        sortOrder: p.sortBy ? p.sortOrder : undefined
      }),
    params: () => ({
      keyword: searchForm.value.keyword,
      page: pagination.current,
      pageSize: pagination.size,
      sortBy: sortBy.value || undefined,
      sortOrder: sortBy.value ? sortOrder.value : undefined
    }),
    hosts: () => hosts.value,
    hostsLoading: () => hostsLoading.value
  })

  // ── 服务端分页 / 排序（Wave 2 范式）────────────────────────────────
  // 分页态照 device：page/pageSize 显式发（pageSize 取 50，与后端默认一致）；载入后
  // total 从响应同步。排序只认白名单列（列 prop → 后端 sortBy 键），未知 prop 忽略。
  const pagination = reactive({ current: 1, size: 50, total: 0 })
  watch(total, (v) => {
    pagination.total = v
  })
  /** 列 prop → 后端排序键（白名单三列：repoTag/hostCount/distinctDigests）。 */
  const SORT_KEYS: Record<string, DockerImageDriftSortKey> = {
    repoTag: 'repoTag',
    hostCount: 'hostCount',
    distinctDigests: 'distinctDigests'
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
      // 三态循环的第三态：清回默认排序（sortBy 空即落回后端默认 distinctDigests 降序）。
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

  // 分组筛选项 = 主机清单里出现过的分组（去重）。契约的 `group` 是**单值精确**匹配，
  // 分组名是单维值 —— 再套一层 ElOptionGroup 是空转（分组的「组」就是它自己），
  // 故用扁平选项；ArtSearchBar 的分组渲染能力已由各页主机筛选（hostOptions）使用。
  const groupOptions = computed(() => {
    const seen = new Set<string>()
    for (const host of hosts.value) if (host.group) seen.add(host.group)
    return [...seen].map((g) => ({ label: g, value: g }))
  })

  const searchItems = computed(() => [
    {
      key: 'keyword',
      label: '仓库',
      type: 'input',
      placeholder: '仓库名或标签',
      clearable: true
    },
    {
      key: 'group',
      label: '分组',
      type: 'select',
      placeholder: '全部分组',
      clearable: true,
      filterable: true,
      options: groupOptions.value
    }
  ])

  const hasFilter = computed(
    () => Boolean(searchForm.value.keyword) || Boolean(searchForm.value.group)
  )

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

  /** 工具条刷新（内置钮）：刷新 = 把眼前这页看新 —— 分组筛选源（主机清单）与清单一起重拉。 */
  function refreshAll() {
    void loadHosts()
    void refresh()
  }

  // 进页面即拉一次（与四个资源清单页同一口径：轻量表页不做自动刷新，进页拉一次、
  // 表头刷新随时手动）。主机清单由 useHostList 自拉（分组选项与空态判定的前置）。
  onMounted(() => {
    void load()
  })

  // ══ 声明式目标版本 + 按待对齐分发（Wave 4c）══════════════════════════════
  // 旗舰能力：为一个 repoTag 声明目标摘要（digest，即「期望的内容摘要」），据此把
  // 「待对齐」的主机一次性拉齐 —— 从「逐台手拉」降到「声明 + 分发」两步。目标是对
  // 漂移的增强：仅当某 tag 有声明时行上才产出 targetDigest / alignedCount /
  // pendingHosts（无声明三者缺席，页面不臆造目标）。
  //
  // 分发复用既有指令通道（image:pull，逐台）——不新建后端。**拉取按标签进行**（引用即
  // repoTag，如 `alpine:3.19`），收敛到仓库当前提供的内容；目标摘要只用于**判定谁待
  // 对齐**与**拉取后的复核**，不是拉取的引用。一轮结束后重拉漂移逐台比对，未收敛如实报。

  const { hasAuth } = useAuth()
  /** 声明 / 分发 / 取消目标都需 docker:manage（与两条写端点的静态 perm 同档）。 */
  const canManage = computed(() => hasAuth(PermDockerManage))

  // ── 声明 / 修改目标（小对话框：候选摘要点选 + 粘贴两条路）──────────────
  const targetDialogVisible = ref(false)
  const targetRow = ref<DockerImageDriftItem | null>(null)

  function openTargetDialog(row: DockerImageDriftItem) {
    targetRow.value = row
    targetDialogVisible.value = true
  }

  /** 声明成功后重拉本页：行上随即出现目标短显与已对齐计数（静默，不闪骨架）。 */
  function onTargetSaved() {
    void refreshSilent()
  }

  // ── 确认档（分发 / 取消目标共用一只 action-confirm 形制的弹窗）──────────
  // 取消目标如实告诉用户：清目标只停止「待对齐」的判定，**不会**回退任何已拉取到
  // 主机的镜像（这是**再声明旧摘要 + 再分发**的活，不是一条「回滚」能兑现的）。
  const CLEAR_CONCLUSION = '取消目标只停止「待对齐」的判定；不会回退任何已拉取到主机的镜像。'

  const confirm = ref<{
    visible: boolean
    kind: 'distribute' | 'clear'
    row: DockerImageDriftItem | null
  }>({ visible: false, kind: 'distribute', row: null })

  const confirmOverride = computed<ConfirmOverride>(() => {
    const row = confirm.value.row
    if (confirm.value.kind === 'distribute' && row) {
      return {
        kind: 'confirm',
        label: `分发到待对齐 ${row.pendingHosts?.length ?? 0} 台`,
        conclusion: distributeConclusion(row)
      }
    }
    return { kind: 'confirm', label: '取消目标', conclusion: CLEAR_CONCLUSION }
  })

  /**
   * 影响面结论句：分发按**标签**拉取（不钉摘要），如实区分「更新」与「新拉」——
   *   本机已有该标签 = 更新；本机无该标签 = 新拉取（对它是新东西，与更新不是一回事）。
   *   两类计数从硬字段推：taggedPending = hostCount − alignedCount（有该标签却没对齐
   *   的主机数），tagless = 待对齐 − taggedPending。
   */
  function distributeConclusion(row: DockerImageDriftItem): string {
    const n = row.pendingHosts?.length ?? 0
    const tagged = Math.max(0, row.hostCount - (row.alignedCount ?? 0))
    const tagless = Math.max(0, n - tagged)
    if (tagless > 0 && tagged > 0) {
      return `${n} 台待对齐：${tagged} 台已有该标签（按标签更新），${tagless} 台本机无该标签（按标签新拉取）。`
    }
    if (tagless > 0) {
      return `${n} 台待对齐均本机无该标签，按标签新拉取。`
    }
    return `${n} 台待对齐均已有该标签，按标签更新。`
  }

  function askDistribute(row: DockerImageDriftItem) {
    if (dispatchRunning.value) return
    if (!row.targetDigest || (row.pendingHosts?.length ?? 0) === 0) return
    confirm.value = { visible: true, kind: 'distribute', row }
  }

  function askClearTarget(row: DockerImageDriftItem) {
    if (dispatchRunning.value) return
    confirm.value = { visible: true, kind: 'clear', row }
  }

  function onConfirmAction() {
    const { kind, row } = confirm.value
    confirm.value = { ...confirm.value, visible: false }
    if (!row) return
    if (kind === 'clear') {
      void doClearTarget(row)
    } else {
      void startDispatch(row.repoTag, row.targetDigest ?? '', row.pendingHosts ?? [])
    }
  }

  function errMsg(e: unknown, fallback: string): string {
    const msg = (e as { message?: string })?.message
    return msg && msg.trim() !== '' ? msg : fallback
  }

  /** 清除目标（DELETE，幂等）：成功后重拉本页（行上目标字段随即消失）。 */
  async function doClearTarget(row: DockerImageDriftItem) {
    try {
      await clearImageTarget(row.repoTag)
      ElMessage.success('已取消目标')
      await refreshSilent()
    } catch (e) {
      ElMessage.error(errMsg(e, '取消目标失败'))
    }
  }

  // ── 分发（按待对齐主机逐台 image:pull，分波语义照 workload-batch-bar）──
  /**
   * 轮询上限：镜像拉取以分钟计（大镜像十几分钟），远比启停/删除慢 —— 默认 20 次
   * （约 1.5 分钟）会误判超时。按 pollDelay 5s 封顶估，120 次 ≈ 10 分钟档。
   */
  const DISPATCH_POLL_ATTEMPTS = 120

  const dispatchRunning = ref(false)
  const dispatchStopping = ref(false)
  /** 落定复核中（一轮拉取结束、重拉漂移逐台比对的那一拍）：footer 的进行行读它。 */
  const dispatchVerifying = ref(false)
  const dispatchCurrent = ref<{ host: string; done: number; total: number } | null>(null)
  const dispatchNotes = ref<{ text: string; failed: boolean }[]>([])
  /** 拉取命令**失败**的主机（重试集；拉取成功项按复核结果另计对齐 / 未对齐）。 */
  const dispatchFailed = ref<DockerImageDriftHostRef[]>([])
  /** 拉取成功且复核 == 目标的主机数。 */
  const dispatchAligned = ref(0)
  /** 拉取成功但复核 != 目标的主机数（如实计入，不谎报成功）。 */
  const dispatchNotAligned = ref(0)
  const dispatchDone = ref(false)
  /** 本轮分发的源（重试失败集时要原样复用同一 repoTag + 目标摘要）。 */
  const dispatchRepoTag = ref('')
  const dispatchDigest = ref('')

  const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms))

  /**
   * 指令通道：hostId 在 run 入口求值（换 ref 即切主机）—— 逐台分发时每次 run 前
   * 把它置为该台的 hostId（与 workload-batch-bar 同款；useDockerCmds 在 setup 期
   * 把函数定住）。成功后的重拉用本页的静默刷新（对齐计数随之更新）。
   */
  const cmdHostId = ref('')
  const { run: runCmd } = useDockerCmds({
    hostId: () => cmdHostId.value,
    refresh: refreshSilent,
    maxPollAttempts: DISPATCH_POLL_ATTEMPTS
  })

  /**
   * 完成态口径（三档，只讲硬事实）：已对齐 / 仍是其他摘要 / 失败。零值分项不出现
   * （不拿「0 台」凑句子），但**至少留一档** —— 分发必然处理过主机。
   */
  const dispatchSummaryText = computed(() => {
    const parts = [`${dispatchAligned.value} 台已对齐`]
    if (dispatchNotAligned.value > 0) parts.push(`${dispatchNotAligned.value} 台仍是其他摘要`)
    if (dispatchFailed.value.length > 0) parts.push(`${dispatchFailed.value.length} 台失败`)
    return `分发完成：${parts.join(' / ')}`
  })

  /** 摘要比较（剥 `sha256:` 后逐字比、大小写不敏感）：相等 = 已对齐。 */
  function sameDigest(a: string | undefined, b: string | undefined): boolean {
    const norm = (d: string | undefined) => (d ?? '').replace(/^sha256:/i, '').toLowerCase()
    const na = norm(a)
    return na !== '' && na === norm(b)
  }

  /**
   * 从**复核后的漂移行**读出某主机在该标签上的当前摘要（= 镜像 ID）。
   *
   * 少数派带主机清单 → 直接命中；否则该主机必然落在多数派（分布里除多数派外都进了
   * 少数派）—— 成功拉取后该主机必有此标签，故回退到 dominantDigest 是准确的。
   */
  function hostDigestOf(row: DockerImageDriftItem, hostId: string): string {
    for (const o of row.outliers ?? []) {
      if ((o.hosts ?? []).some((h) => h.hostId === hostId)) return o.digest
    }
    return row.dominantDigest
  }

  /**
   * 一轮结束的**诚实复核**：重拉漂移（按标签精确匹配该行），逐台读回当前摘要。
   * agent 写操作成功会触发一次立即采帧，但帧到达 core 缓存有先后 —— 先等一拍
   * （dispatchTiming.verifySettleMs；单测置 0）再读，避免把「还没到」误报成「不齐」。
   * 复核只针对**拉取成功**的主机；返回 hostId → 当前摘要（未取到该行时退化为空串）。
   */
  async function verifyAlignment(
    repoTag: string,
    succeeded: DockerImageDriftHostRef[]
  ): Promise<Map<string, string>> {
    const verdicts = new Map<string, string>()
    if (succeeded.length === 0) return verdicts
    dispatchVerifying.value = true
    try {
      await sleep(dispatchTiming.verifySettleMs)
      let row: DockerImageDriftItem | undefined
      try {
        const resp = await fetchImageDrift({ keyword: repoTag, page: 1, pageSize: 50 })
        row = (resp.items ?? []).find((it) => it.repoTag === repoTag)
      } catch {
        row = undefined // 复核读失败不谎报成功：按「未取到」处理
      }
      for (const h of succeeded) verdicts.set(h.hostId, row ? hostDigestOf(row, h.hostId) : '')
    } finally {
      dispatchVerifying.value = false
    }
    return verdicts
  }

  /** 一台主机的行内结论：拉取失败照原文；拉取成功则按复核给「已对齐 / 仍是 …」。 */
  function hostNote(
    host: string,
    ok: boolean,
    error: string | undefined,
    actual: string | undefined,
    target: string
  ): { text: string; failed: boolean } {
    if (!ok) return { text: `${host}：失败（${error ?? '拉取未完成'}）`, failed: true }
    if (sameDigest(actual, target)) return { text: `${host}：已对齐`, failed: false }
    return {
      text: `${host}：仍是 ${shortDigest(actual ?? '')}，与目标 ${shortDigest(target)} 不同`,
      failed: true
    }
  }

  /**
   * 分发一轮：对传入的待对齐主机逐台发 image:pull，**引用即 repoTag**（按标签拉取，
   * 不钉摘要 —— 收敛到仓库当前提供的内容）；目标摘要只用于复核（见 verifyAlignment）。
   *
   * 分波：**一台主机一波**（pendingHosts 已按 hostId 升序），逐波推进、波内单行 ——
   * 与 workload-batch-bar 的 groupBatchWaves 同一分波语义（这里列数据已是逐主机
   * 粒度，波即一行）。单台失败不中断后续；一轮拉取后重拉漂移逐台复核，未收敛如实报。
   * 拉取命令失败集记下来供「重试」只对失败集再发一轮。
   */
  async function startDispatch(repoTag: string, digest: string, hosts: DockerImageDriftHostRef[]) {
    if (dispatchRunning.value || hosts.length === 0 || !digest) return
    const target = repoTag // 按标签拉取：引用就是 repoTag，不拼 @digest
    dispatchRepoTag.value = repoTag
    dispatchDigest.value = digest
    const waves = hosts.map((x) => ({ hostId: x.hostId, hostname: x.hostname }))
    dispatchRunning.value = true
    dispatchStopping.value = false
    dispatchNotes.value = []
    dispatchCurrent.value = null
    dispatchFailed.value = []
    dispatchAligned.value = 0
    dispatchNotAligned.value = 0
    dispatchDone.value = false
    const failures: DockerImageDriftHostRef[] = []
    const succeeded: DockerImageDriftHostRef[] = []
    // 逐台结果（供复核后重建结论句）：{host, hostId, ok, error}
    const results: { host: string; hostId: string; ok: boolean; error?: string }[] = []
    try {
      for (const wave of waves) {
        if (dispatchStopping.value) break
        const host = wave.hostname || wave.hostId
        dispatchCurrent.value = { host, done: 0, total: 1 }
        cmdHostId.value = wave.hostId
        const res = await runCmd({ action: 'image:pull', target, key: 'dispatch' })
        const error = res.ok ? undefined : runErrorMessage(res, '拉取未完成')
        if (res.ok) succeeded.push({ hostId: wave.hostId, hostname: wave.hostname })
        else failures.push({ hostId: wave.hostId, hostname: wave.hostname })
        results.push({ host, hostId: wave.hostId, ok: res.ok, error })
        dispatchCurrent.value = { host, done: 1, total: 1 }
        // 进行中的临时结论（复核后按对齐结果重建）。
        dispatchNotes.value = [
          ...dispatchNotes.value,
          res.ok
            ? { text: `${host}：成功`, failed: false }
            : { text: `${host}：失败（${error}）`, failed: true }
        ]
      }
      // 一轮结束的诚实复核：重拉漂移逐台比对（仅拉取成功的主机）。
      dispatchCurrent.value = null
      const verdicts = await verifyAlignment(repoTag, succeeded)
      dispatchNotes.value = results.map((r) =>
        hostNote(r.host, r.ok, r.error, verdicts.get(r.hostId), digest)
      )
      dispatchAligned.value = succeeded.filter((h) =>
        sameDigest(verdicts.get(h.hostId), digest)
      ).length
      dispatchNotAligned.value = succeeded.length - dispatchAligned.value
      dispatchFailed.value = failures
    } finally {
      dispatchRunning.value = false
      dispatchCurrent.value = null
      dispatchVerifying.value = false
    }
    dispatchDone.value = true
    const message = dispatchSummaryText.value
    if (failures.length > 0 || dispatchNotAligned.value > 0) {
      ElMessage({ type: 'warning', message, duration: 5000 })
    } else {
      ElMessage.success(message)
    }
  }

  /** 停止：不再分发**后续**主机（已在途的那一台照常收尾，结果如实入账）。 */
  function stopDispatch() {
    dispatchStopping.value = true
  }

  /** 重试：只对失败集再发一轮（同一 repoTag + 目标摘要）。 */
  function retryFailed() {
    if (dispatchRunning.value || dispatchFailed.value.length === 0) return
    void startDispatch(dispatchRepoTag.value, dispatchDigest.value, dispatchFailed.value)
  }

  /** 关闭分发账目（完成态）：收起行内反馈，下一轮开始时重新记账。 */
  function clearDispatch() {
    dispatchDone.value = false
    dispatchNotes.value = []
    dispatchFailed.value = []
    dispatchAligned.value = 0
    dispatchNotAligned.value = 0
  }

  // ── 目标列 / 操作列（行上看出「已声明」与入口）────────────────────────
  /** 目标列：有声明给「目标摘要短显 + 已对齐 X/范围总数」，无声明「—」（不臆造）。 */
  function targetCellText(row: DockerImageDriftItem): string {
    if (!row.targetDigest) return '—'
    const total = (row.alignedCount ?? 0) + (row.pendingHosts?.length ?? 0)
    return `目标 ${shortDigest(row.targetDigest)} · 已对齐 ${row.alignedCount ?? 0}/${total}`
  }

  /**
   * 操作列：声明/修改目标（入口）、分发到待对齐（仅「有目标且有 pendingHosts」时
   * 出现）、取消目标（幂等）。全部按 docker:manage 门控（无权限整列不渲染）。
   */
  function operationCell(row: DockerImageDriftItem): unknown {
    if (!canManage.value) return null
    const buttons: ReturnType<typeof h>[] = []
    if (row.targetDigest) {
      buttons.push(
        h(
          ElButton,
          { link: true, type: 'primary', size: 'small', onClick: () => openTargetDialog(row) },
          '改目标'
        )
      )
      const pending = row.pendingHosts?.length ?? 0
      if (pending > 0) {
        buttons.push(
          h(
            ElButton,
            { link: true, type: 'primary', size: 'small', onClick: () => askDistribute(row) },
            `分发到待对齐 ${pending} 台`
          )
        )
      }
      buttons.push(
        h(ElButton, { link: true, size: 'small', onClick: () => askClearTarget(row) }, '取消目标')
      )
    } else {
      buttons.push(
        h(
          ElButton,
          { link: true, type: 'primary', size: 'small', onClick: () => openTargetDialog(row) },
          '声明目标'
        )
      )
    }
    return h('div', { class: 'drift-actions' }, buttons)
  }

  // ── 展示口径（纯函数；只陈述硬事实，零解释文案）──────────────────────

  /** 摘要短显：剥 `sha256:` 前缀取前 12 位（模块「长 id 短显」惯例）；缺值「—」。 */
  function shortDigest(digest: string): string {
    const bare = (digest ?? '').replace(/^sha256:/, '')
    return bare ? bare.slice(0, 12) : '—'
  }

  /**
   * 一句话口径（由结构化字段自拼，契约的 summary 恒空）：只陈述硬事实 ——
   * 「N 台 · M 种摘要 · 多数派 K 台」。不含定位/去向/口径解释句。
   */
  function driftSummaryText(row: DockerImageDriftItem): string {
    return `${row.hostCount} 台 · ${row.distinctDigests} 种摘要 · 多数派 ${row.dominantHostCount} 台`
  }

  /** 少数派行内摘要：「N 种 · 共 M 台」（无少数派「—」）。 */
  function outliersSummary(row: DockerImageDriftItem): string {
    const outliers = row.outliers ?? []
    if (outliers.length === 0) return '—'
    const hostsTotal = outliers.reduce((n, o) => n + (o.hostCount ?? 0), 0)
    return `${outliers.length} 种 · 共 ${hostsTotal} 台`
  }

  /**
   * 展开区（少数派明细）：每种摘要一行 —— 摘要短显 + 主机数 + 该种摘要的主机 chip
   * （主机名走模块既有的 .docker-host 胶囊形制）。无少数派「—」。
   */
  function expandDetail(row: DockerImageDriftItem) {
    const outliers = row.outliers ?? []
    if (outliers.length === 0) return h('div', { class: 'drift-expand' }, '—')
    return h(
      'div',
      { class: 'drift-expand' },
      outliers.map((o) =>
        h('div', { class: 'drift-outlier', key: o.digest }, [
          h('span', { class: 'drift-outlier__head' }, [
            h('span', { class: 'drift-mono' }, shortDigest(o.digest)),
            ` · ${o.hostCount} 台`
          ]),
          h(
            'span',
            { class: 'drift-outlier__hosts' },
            (o.hosts ?? []).map((host) =>
              h(
                'span',
                { class: 'docker-host', key: host.hostId },
                hostLabel({ id: host.hostId, hostname: host.hostname })
              )
            )
          )
        ])
      )
    )
  }

  // ── 表格列（列设置经 useTableColumns，对齐系统管理）────────────────────
  // 数据列一律 minWidth + showOverflowTooltip（单行不变量，Wave 3c）：行内容单行。
  const { columns, columnChecks } = useTableColumns<DockerImageDriftItem>(() => [
    { type: 'index' as const, width: 60, label: '序号', hideBelow: 'tablet' },
    {
      // 展开列：行内摘要装不下的少数派明细（每种摘要的主机清单）落在这里 ——
      // 展开区自占一行，主行内容仍单行不折。
      type: 'expand' as const,
      width: 46,
      formatter: (row: DockerImageDriftItem) => expandDetail(row)
    },
    {
      prop: 'repoTag',
      label: '仓库:标签',
      minWidth: 240,
      // 单行不变量：showOverflowTooltip（EP 负责 nowrap/省略号）+ midEllipsis（可见
      // 那截保留头+尾 —— 尾部 tag 是唯一有信息的那段），全文由 title 承载。
      showOverflowTooltip: true,
      sortable: 'custom' as const,
      formatter: (row: DockerImageDriftItem) => {
        const full = row.repoTag ?? ''
        return h(
          'span',
          { class: 'drift-mono', title: full },
          midEllipsis(full, IMAGE_MID_HEAD, IMAGE_MID_TAIL)
        )
      }
    },
    {
      // 数字列：右对齐 + is-num（tabular-nums，Wave 3a 数字排版）。
      prop: 'hostCount',
      label: '主机数',
      minWidth: 96,
      showOverflowTooltip: true,
      align: 'right' as const,
      className: 'is-num',
      sortable: 'custom' as const
    },
    {
      prop: 'distinctDigests',
      label: '摘要数',
      minWidth: 96,
      showOverflowTooltip: true,
      align: 'right' as const,
      className: 'is-num',
      sortable: 'custom' as const
    },
    {
      // 多数派：摘要短显 + 其主机数。
      prop: 'dominantDigest',
      label: '多数派',
      minWidth: 150,
      showOverflowTooltip: true,
      hideBelow: 'tablet',
      formatter: (row: DockerImageDriftItem) =>
        h('span', {}, [
          h('span', { class: 'drift-mono' }, shortDigest(row.dominantDigest)),
          ` · ${row.dominantHostCount} 台`
        ])
    },
    {
      // 少数派：行内摘要 + 展开列看明细。
      prop: 'outliers',
      label: '少数派',
      minWidth: 120,
      showOverflowTooltip: true,
      formatter: (row: DockerImageDriftItem) => outliersSummary(row)
    },
    {
      // 一句话口径（结构化字段自拼）：数字列已给精确值，这列给「一眼读」。
      prop: 'summary',
      label: '一致性',
      minWidth: 200,
      showOverflowTooltip: true,
      hideBelow: 'desktop',
      formatter: (row: DockerImageDriftItem) => driftSummaryText(row)
    },
    {
      // 目标版本（Wave 4c）：有声明给目标摘要短显 + 已对齐计数；无声明「—」。
      // 数据列一律 minWidth + showOverflowTooltip（行内容单行，Wave 3c）。
      prop: 'targetDigest',
      label: '目标版本',
      minWidth: 220,
      showOverflowTooltip: true,
      formatter: (row: DockerImageDriftItem) => targetCellText(row)
    },
    // 操作列（声明/改目标、分发、取消目标）只在有 docker:manage 时出现 —— 无权限
    // 的人连入口都不该看到（控件矩阵：不渲染 ≠ 禁用）。
    ...(canManage.value
      ? [
          {
            prop: 'operation',
            label: '操作',
            width: 260,
            fixed: 'right' as const,
            formatter: (row: DockerImageDriftItem) => operationCell(row)
          }
        ]
      : [])
  ])
</script>

<style lang="scss" scoped>
  @use './overview-tokens' as t;

  // 主机 chip（胶囊）：与镜像/容器/卷/网络/项目页同一全局类 docker-host + t.pill 制。
  :global(.docker-host) {
    @include t.pill;
  }

  // 等宽文本（仓库引用 / 摘要 id）：formatter 里 h() 造的 span 不进 scoped 作用域，
  // 故走全局类（与 .docker-host 同一手法）；色值/字族走模块既有 token。
  :global(.drift-mono) {
    font-family: var(--art-font-family-mono);
  }

  .docker-image-drift-page {
    /* 次要文字对比度 AA（逐页口径）：EP 默认 --el-text-color-secondary(#909399) 对
       白底只有 3.08:1，低于 AA 正文线 → 页面范围内升到 regular 档。 */
    --el-text-color-secondary: var(--el-text-color-regular);

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

    // 不可用主机 / 静默刷新失败标注：琥珀即「需要注意」（与模块内陈旧/离线标注同一套）。
    .docker-refresh-note {
      font-size: 12px;
      color: var(--aa-warning-text);
    }

    // 空态渲染在本页（ArtTable 不转发 `#empty`）：给它接近表格空态的留白。
    .docker-empty {
      padding: 56px 0;
    }

    // 分发进度（Wave 4c）：放表下的行内账目 —— 进行行 + 逐行结论 + 失败重试。形态
    // 与批量栏同源，只是这里不吸底（分发由漂移行入口一次性触发，不依赖勾选）。
    .drift-dispatch {
      display: flex;
      flex-direction: column;
      gap: 2px;
      padding: 10px 16px;
      border-top: 1px solid var(--el-border-color-lighter);
      font-size: 12px;
      line-height: 1.6;

      &__current {
        // 主色文字对比度 AA：token 与数字见 @styles/core/aa-text.scss。
        color: var(--aa-primary-text);
      }

      &__note {
        color: var(--el-text-color-secondary);
      }

      // 失败项与含失败的总结论：如实用警示色（白底可读走 AA token）。
      &__note.is-failed,
      &__summary.is-failed {
        color: var(--aa-warning-text);
      }

      &__foot {
        display: flex;
        gap: 8px;
        align-items: center;
      }

      &__summary {
        color: var(--el-text-color-regular);
      }
    }

    // 确认档里的影响面主机清单：主机名走模块既有 .docker-host 胶囊（与展开区同款）。
    .drift-impact {
      display: flex;
      flex-wrap: wrap;
      gap: 6px;
      margin-top: 6px;
    }

    // 操作列：几个链接动作排一行（行内容单行不折）。
    .drift-actions {
      display: flex;
      flex-wrap: nowrap;
      gap: 12px;
      align-items: center;
    }

    // 展开区（少数派明细）：每种摘要一行，主机 chip 跟随。
    .drift-expand {
      display: flex;
      flex-direction: column;
      gap: 6px;
      padding: 4px 0;
      color: var(--el-text-color-regular);
      font-size: 12px;
    }

    .drift-outlier {
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      gap: 8px;

      &__head {
        color: var(--el-text-color-regular);
      }

      &__hosts {
        display: inline-flex;
        flex-wrap: wrap;
        gap: 6px;
      }
    }
  }
</style>
