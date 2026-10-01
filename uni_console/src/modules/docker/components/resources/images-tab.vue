<template>
  <!-- 单根（single-root 守卫在库：布局的 Transition 只支持单根，双根切页白屏）。
       本组件是 resources 页「镜像」tab 的内容（7a 由原 views/images.vue 平移）：
       页面级的主机条/快照/四态在 views/resources.vue，这里只持有镜像表自己的
       筛选、勾选、底栏写操作与三个对话框。 -->
  <div class="docker-images-tab">
    <ArtSearchBar
      v-show="showSearchBar"
      v-model="searchForm"
      :items="searchItems"
      @search="onSearch"
      @reset="onReset"
    />

    <!-- 结构对齐 DockerPage 的单列表形态：搜索栏在卡片外，表格与页脚在卡片内
         （平移前的页面走 DockerPage 的 search/table/footer 插槽，形态一致）。 -->
    <ElCard class="art-table-card" shadow="never">
      <ArtTableHeader v-model:showSearchBar="showSearchBar" :loading="loading" @refresh="refresh" />

      <!-- 两种空态分开：主机上没有镜像 vs 筛选没命中（后者给「清除筛选」）。
           纪律与容器页一致（「没有」与「筛没了」说成一句会让人以为机器空了）；
           空态渲染在本组件、不写进 ArtTable 的 `#empty` 插槽：ArtTable 不转发该插槽
           （内部把 ElTable 的空态写死成「暂无数据」），写进去会被静默丢弃。
           清单还没到时也不喊「没有镜像」（那时还不知道有没有主机），故 v-if 把
           主机清单的加载态一并算进来。 -->
      <ArtTable
        v-if="showTable"
        :loading="listLoading"
        :data="filtered"
        :columns="columns"
        @selection-change="onSelectionChange"
      />
      <!-- host-context.ts 的 reload 注释承诺：清单拉不到时页面显示「没有可管理的主机」 -->
      <ElEmpty v-else-if="!ctx.hosts.length" class="docker-empty" description="没有可管理的主机" />
      <ElEmpty v-else-if="hasFilter" class="docker-empty" description="没有符合筛选条件的镜像">
        <ElButton size="small" @click="onReset">清除筛选</ElButton>
      </ElEmpty>
      <ElEmpty v-else class="docker-empty" description="该主机上还没有镜像" />

      <!-- 底部合计是这一页的入口数字（「空间去哪了」）：总大小与可回收大小并列。
           合计跟着筛选走 —— 底栏与表格里的行必须自洽，否则「合计」会被当成
           与眼前行数无关的另一个数。 -->
      <div class="docker-total">
        <span>合计 {{ totals.count }} 个 · {{ formatByUnit('MB', totals.totalMB) }}</span>
        <span class="docker-total__sub">
          {{ totals.danglingCount }} 个可回收 · {{ formatByUnit('MB', totals.danglingMB) }}
        </span>
      </div>

      <!-- 底栏写操作（spec §11.3）：清理悬空 / 仓库凭据 / 拉取 / 打标签 / 导出 tar / 载入 /
           构建镜像（P2 分发闭环）。打标签与导出 tar 的输入是**选中的那一行**：未选中
           一行时按钮禁用，结论句（要选一行）写在按钮旁。仓库凭据（4c）与镜像写动作
           不同档（docker:config）：它有自己的权限门槛，不能被 canWrite 顺带挡掉 ——
           只有 config 权限的账号也要进得了这条入口，故底栏的渲染条件把它并进来。 -->
      <div v-if="canWrite || canConfig" class="docker-bar">
        <ElButton
          v-if="canDelete"
          size="small"
          type="danger"
          plain
          :disabled="busy"
          @click="openPrune"
        >
          清理悬空镜像…
        </ElButton>
        <ElButton v-if="canConfig" size="small" @click="registryVisible = true">
          仓库凭据…
        </ElButton>
        <ElButton v-if="canManage" size="small" :disabled="busy" @click="askPull">
          拉取镜像…
        </ElButton>
        <ElButton v-if="canManage" size="small" :disabled="tagSaveDisabled" @click="onTagSelected">
          打标签…
        </ElButton>
        <ElButton v-if="canManage" size="small" :disabled="tagSaveDisabled" @click="onSaveSelected">
          导出 tar…
        </ElButton>
        <ElButton v-if="canManage" size="small" :disabled="busy" @click="askLoad">
          载入镜像…
        </ElButton>
        <ElButton v-if="canManage" size="small" :disabled="busy" @click="askBuild">
          构建镜像…
        </ElButton>
        <span v-if="canManage && !singleSelected" class="docker-bar__hint">
          打标签与导出 tar 需先在列表中选中一行镜像
        </span>
      </div>
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
         hostId 跟当前主机走（对话框在受理时钉死）、成功关闭后双次重拉本页快照。 -->
    <PullProgressDialog v-model="pullVisible" :host-id="ctx.hostId" :refresh="refresh" />

    <!-- 构建进度对话框（P2 分发闭环）：底栏「构建镜像…」的表单（tag/上下文/
         Dockerfile/参数）+ 逐行构建播报 + 取消。hostId 与重拉口径同拉取对话框
        （成功后新镜像要进列表）。 -->
    <BuildProgressDialog v-model="buildVisible" :host-id="ctx.hostId" :refresh="refresh" />

    <!-- 仓库凭据管理对话框（4c）：入口按钮只对 docker:config 渲染（不渲染 ≠ 禁用）。
         凭据是**全局**的（不属于任何一台主机 —— 服务端按仓库地址解析注入），故
         主机切换不需要像拉取对话框那样把它关掉。 -->
    <RegistryCredentialsDialog v-model="registryVisible" />
  </div>
</template>

<script setup lang="ts">
  /**
   * 镜像 tab（7a 平移自 views/images.vue，逻辑零改动）：
   *
   * 数据源从「本组件自己拉快照（useDockerHostState）」换成页面级共享上下文 ——
   * props.state / props.loading / props.refresh 由 views/resources.vue 下发（一份快照
   * 三 tab 共用，切 tab 不重拉）；主机上下文仍是模块的 provide/inject（页面是提供者），
   * 本组件经 useDockerHost() 注入后取 ctx.hosts / ctx.hostId。
   *
   * 主机切换的重置纪律（清勾选、清筛选、关拉取对话框）**没有**留在本组件监听 ——
   * 三 tab 各写一份 watch 会在收敛页上三处漂移，故收拢为页面级一份
   * （resources.vue 的 onHostSwitch 调用本组件暴露的 resetForHostSwitch）。
   */
  import { computed, h, ref } from 'vue'
  import { useRouter } from 'vue-router'
  import { ElButton, ElCard, ElCheckbox, ElEmpty, ElMessage } from 'element-plus'
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
  import { sendDockerCmd, type DockerImageItem, type DockerStateResp } from '../../api'
  import {
    classifyAcceptError,
    runErrorMessage,
    useDockerCmds,
    type DockerCmdRunResult
  } from '../../composables/useDockerCmds'
  import { lookupDockerAction } from '../../utils/actions'
  import { filterImages, imageTotals } from '../../utils/snapshot'
  import { formatRelativeTime, imageRefText, inUseText } from '../../utils/display'
  import { useDockerHost } from '../../utils/host-context'

  defineOptions({ name: 'DockerImagesTab' })

  const props = defineProps<{
    /** 页面级共享快照（resources.vue 的 useDockerHostState.state，三 tab 同源）。 */
    state: DockerStateResp | null
    /** 快照拉取在途（页面级一份；本 tab 的表格加载态还叠加主机清单加载）。 */
    loading: boolean
    /** 重拉共享快照（页面级 refresh：写指令成功后、表头刷新按钮都走它）。 */
    refresh: () => void | Promise<void>
  }>()

  // 主机上下文经 provide/inject 注入（页面 resources.vue 是提供者）。
  // 不要解构：上下文字段是 getter，解构会把 hostId 定格成 inject 那一刻的值。
  const ctx = useDockerHost()
  const router = useRouter()
  const { hasAuth } = useAuth()

  const showSearchBar = ref(false)
  const searchForm = ref<{ keyword?: string; danglingOnly?: boolean; unusedOnly?: boolean }>({})
  /** 当前勾选的行：底栏「打标签 / 导出 tar」一次只作用于选中的那一行。 */
  const selected = ref<DockerImageItem[]>([])
  /** 拉取进度对话框的开关（4b）：开在「当前主机」上，切换主机时关掉（resetForHostSwitch）。 */
  const pullVisible = ref(false)
  /** 构建进度对话框的开关（P2）：同拉取 —— 一场构建属于受理它的那台主机。 */
  const buildVisible = ref(false)
  /** 仓库凭据对话框的开关（4c）：入口按钮受 canConfig 门控（模板里的 v-if）。 */
  const registryVisible = ref(false)

  const canManage = computed(() => hasAuth(PermDockerManage))
  const canDelete = computed(() => hasAuth(PermDockerDelete))
  /** 仓库凭据管理权限（4c）：与镜像写动作不同档 —— 服务端 /docker/registries 的静态 perm。 */
  const canConfig = computed(() => hasAuth(PermDockerConfig))
  /** 至少有一个写权限（或凭据权限）才渲染底栏操作区（没有可执行的动作就不占版面）。 */
  const canWrite = computed(() => canManage.value || canDelete.value)

  /** 表格的加载态：快照在拉，或主机清单还没到（后者尚不知有没有主机，不能先喊「没有」）。 */
  const listLoading = computed(() => props.loading || ctx.loading)

  /** 主机切换的重置纪律（原页面 onHostSwitch 的正文，平移零改动）：
   *  清空勾选 + 清空筛选 + 关掉拉取/构建进度对话框（一场操作属于受理它的那台
   *  主机，对话框把 hostId 在受理时钉死，切机后让它继续跑只会让进度与结论挂在
   *  错误的主机名下；关掉即断流，服务端随之取消那场操作——断开 = 取消是端点契约）。 */
  function resetForHostSwitch() {
    selected.value = []
    searchForm.value = {}
    pullVisible.value = false
    buildVisible.value = false
  }
  defineExpose({ resetForHostSwitch })

  // 写指令通道：受理 + 轮询 + 成功后重拉（重拉就是页面级共享的 refresh）。
  // 主机来源走 inject（本组件是页面级 provide 的后代，setup 期注入合法）。
  const { run, pendingId, busy } = useDockerCmds({ refresh: () => props.refresh() })

  const searchItems = computed(() => [
    {
      key: 'keyword',
      label: '仓库',
      type: 'input',
      placeholder: '仓库名或标签',
      clearable: true
    },
    { key: 'danglingOnly', label: '仅悬空', type: 'switch' },
    { key: 'unusedOnly', label: '未被使用', type: 'switch' }
  ])

  const hasFilter = computed(
    () =>
      Boolean(searchForm.value.keyword) ||
      Boolean(searchForm.value.danglingOnly) ||
      Boolean(searchForm.value.unusedOnly)
  )

  const filtered = computed(() => filterImages(props.state?.images ?? [], searchForm.value))

  /** 底栏合计：与表格里的行同源（筛选后合计的是筛出来的这批）。 */
  const totals = computed(() => imageTotals(filtered.value))

  /** 有命中行就渲染表格；纯加载中也用表格的 loading 遮罩，空态只在加载结束后判断。 */
  const showTable = computed(() => listLoading.value || filtered.value.length > 0)

  // ── 底栏写操作的输入 ──

  const singleSelected = computed(() => selected.value.length === 1)
  /** 打标签/导出 tar 的门槛：指令不在途，且恰好选中一行（结论句在按钮旁给出）。 */
  const tagSaveDisabled = computed(() => busy.value || !singleSelected.value)

  function onSelectionChange(rows: DockerImageItem[]) {
    selected.value = rows
  }

  /** 底栏打标签/导出 tar：输入是选中的那一行（未选中时按钮已禁用，这里再兜一道）。 */
  function onTagSelected() {
    if (selected.value.length !== 1) return
    askTag(selected.value[0])
  }
  function onSaveSelected() {
    if (selected.value.length !== 1) return
    askSave(selected.value[0])
  }

  // ── 指令引用与结果回执 ──

  /** 该行的指令引用：有仓库标签用它；无标签的镜像退回镜像 id（两者都是协议认的引用）。 */
  function actionRef(image: DockerImageItem): string {
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
  function askTag(image: DockerImageItem) {
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
  function askSave(image: DockerImageItem) {
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
    confirmState.value = {
      visible: true,
      kind: 'load',
      action: 'image:load',
      target: '',
      options: {}
    }
  }

  /** 删除镜像：标准档确认；使用中的镜像不发指令（服务端也会拒绝），只给结论。 */
  function openRemove(image: DockerImageItem) {
    if (image.inUse) {
      ElMessage.warning('镜像正被容器使用，需先删除相关容器后再删除镜像')
      return
    }
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
   * 分钟级的进行态由任务中心呈现（见 onScanImage 的取舍注释）。
   *
   * 「使用中」的镜像不把删除放进 actions：action-menu 的 disabled 是**整组**禁用，表达不了
   * 「只禁删除」（打标签/导出/扫描对在用镜像依然合法），故删除改为 extraItems 里的**禁用条目**，
   * 结论写在条目上（禁用的条目点不动，结论只能在看得见的地方给）。这是基础设施的缺口。
   */
  function rowMenuItems(row: DockerImageItem): { actions: string[]; extraItems: RowMenuItem[] } {
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
        color: 'var(--art-danger)',
        disabled: true
      })
    } else {
      actions.push('image:remove')
    }
    return { actions, extraItems }
  }

  function onRowMenu(row: DockerImageItem, item: { action: string }) {
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
   * 安全扫描（P3·安全面）：行内触发 + 任务中心看进度 —— **只受理、不轮询**。
   *
   * 为什么不像其它行内动作那样走 runWrite（受理 + 轮询到终态）：useDockerCmds 的
   * 在途伴随 busy（全局禁用操作栏），而真扫描以分钟计（trivy 首扫还要下载漏洞库）——
   * 把整页锁十几分钟换不来任何新信息；「pending 期间任务中心可见」正是长任务的
   * 可见性要求（受理即留痕，终态结论句也落在任务中心）。报告本体在镜像详情页的
   * 「安全」Tab 读取：再点一次扫描会命中服务端 24h 缓存秒回最近一次结果。
   */
  async function onScanImage(image: DockerImageItem): Promise<void> {
    try {
      await sendDockerCmd(ctx.hostId, { action: 'image:scan', target: actionRef(image) })
      ElMessage.success('已发起安全扫描，进度可在任务中心查看')
    } catch (e) {
      // 受理期结论（离线/同目标已在执行/无权限）分类后给出：行内动作没有就地
      // 结论的容器，toast 是这条入口唯一的反馈位。
      ElMessage.error(classifyAcceptError(e).message)
    }
  }

  function openDetail(row: DockerImageItem) {
    void router.push({
      name: 'DockerImageDetail',
      // 镜像 id 带 `sha256:` 冒号，作为路由参数必须编码（详情页解码后回显短 id）。
      params: { id: encodeURIComponent(row.id) },
      query: { host: ctx.hostId }
    })
  }

  // ── 表格列 ──

  const columns = computed<ColumnOption<DockerImageItem>[]>(() => {
    // 显式建立依赖：行内菜单的禁用态与权限显隐来自这些信号，而 formatter 要到表格
    // 渲染时才执行 —— 不在这里读一次，列配置就不会随它们变化而重算（表格会停在旧状态）。
    void pendingId.value
    void busy.value
    void canManage.value
    void canDelete.value
    // 列优先级：手机横屏（<768）只留「仓库:标签 / 使用 / 操作」与勾选列（「使用」是
    // 镜像的状态与回收决策依据）；序号在平板竖屏起出现；大小、创建时间是元数据，
    // 低于桌面隐藏（底栏已给出合计，精确时刻在详情页）。数据列一律 minWidth，
    // 固定宽度只留给 selection/index/操作这类结构性列，见 responsive-columns.ts 的约定。
    return [
      // 勾选列只在有管理权限时出现：只有底栏的打标签/导出 tar 用得上它。
      ...(canManage.value ? [{ type: 'selection' as const, width: 46 }] : []),
      { type: 'index' as const, width: 60, label: '序号', hideBelow: 'tablet' },
      {
        prop: 'repoTags',
        label: '仓库:标签',
        minWidth: 240,
        showOverflowTooltip: true,
        formatter: (row: DockerImageItem) => imageRefText(row)
      },
      {
        prop: 'sizeMb',
        label: '大小',
        minWidth: 110,
        // 大小是元数据（底栏合计里已有），低于桌面隐藏。
        hideBelow: 'desktop',
        formatter: (row: DockerImageItem) => formatByUnit('MB', row.sizeMb)
      },
      {
        prop: 'created',
        label: '创建于',
        minWidth: 140,
        // 相对时间是元数据，精确时刻在详情页给。
        hideBelow: 'desktop',
        formatter: (row: DockerImageItem) => (row.created ? formatRelativeTime(row.created) : '—')
      },
      {
        prop: 'inUse',
        label: '使用',
        minWidth: 150,
        showOverflowTooltip: true,
        formatter: (row: DockerImageItem) => inUseText(row)
      },
      {
        prop: 'operation',
        label: '操作',
        width: 100,
        fixed: 'right' as const,
        formatter: (row: DockerImageItem) => {
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

  function onSearch() {
    // 搜索是**本地筛选**（快照已在手）：不打接口，故不需要分页重置。
    showSearchBar.value = true
  }
  function onReset() {
    searchForm.value = {}
  }
</script>

<style lang="scss" scoped>
  // 空态渲染在本组件（ArtTable 不转发 `#empty`）：给它接近表格空态的留白。
  .docker-empty {
    padding: 56px 0;
  }

  // 底栏合计：主数字（总量）与副行（可回收量）同一行、副行弱化。
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
