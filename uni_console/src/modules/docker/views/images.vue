<template>
  <!-- ⚠ 单根包装：页面**必须只有一个根节点**（布局把页面放进 `<Transition mode="out-in">`，
       而 Transition 只支持单根元素）。二期起本页多了确认弹窗，与容器页同一处理：
       页面与弹窗收进一个根 div —— 双根会在切页时白屏（single-root.test.ts 扫描钉住）。 -->
  <div class="docker-images-page">
    <DockerPage
      :loading="loading"
      :stale="stale"
      :age-seconds="ageSeconds"
      :never-reported="neverReported"
      :load-error="loadError"
      :has-state="hasState"
      @refresh="refresh"
    >
      <template #search>
        <ArtSearchBar
          v-show="showSearchBar"
          v-model="searchForm"
          :items="searchItems"
          @search="onSearch"
          @reset="onReset"
        />
      </template>

      <template #table>
        <ArtTableHeader
          v-model:showSearchBar="showSearchBar"
          :loading="loading"
          @refresh="refresh"
        />

        <!-- 两种空态分开：主机上没有镜像 vs 筛选没命中（后者给「清除筛选」）。
             纪律与容器页一致（「没有」与「筛没了」说成一句会让人以为机器空了）；
             空态渲染在本页、不写进 ArtTable 的 `#empty` 插槽：ArtTable 不转发该插槽
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
        <ElEmpty
          v-else-if="!ctx.hosts.length"
          class="docker-empty"
          description="没有可管理的主机"
        />
        <ElEmpty v-else-if="hasFilter" class="docker-empty" description="没有符合筛选条件的镜像">
          <ElButton size="small" @click="onReset">清除筛选</ElButton>
        </ElEmpty>
        <ElEmpty v-else class="docker-empty" description="该主机上还没有镜像" />
      </template>

      <template #footer>
        <!-- 底部合计是这一页的入口数字（「空间去哪了」）：总大小与可回收大小并列。
             合计跟着筛选走 —— 底栏与表格里的行必须自洽，否则「合计」会被当成
             与眼前行数无关的另一个数。 -->
        <div class="docker-total">
          <span>合计 {{ totals.count }} 个 · {{ formatByUnit('MB', totals.totalMB) }}</span>
          <span class="docker-total__sub">
            {{ totals.danglingCount }} 个可回收 · {{ formatByUnit('MB', totals.danglingMB) }}
          </span>
        </div>

        <!-- 底栏写操作（spec §11.3）：清理悬空 / 仓库凭据 / 拉取 / 打标签 / 导出 tar / 载入。
             打标签与导出 tar 的输入是**选中的那一行**：未选中一行时按钮禁用，
             结论句（要选一行）写在按钮旁。仓库凭据（4c）与镜像写动作不同档
             （docker:config）：它有自己的权限门槛，不能被 canWrite 顺带挡掉 ——
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
          <ElButton v-if="canManage" size="small" :disabled="busy" @click="askLoad">
            载入镜像…
          </ElButton>
          <span v-if="canManage && !singleSelected" class="docker-bar__hint">
            打标签与导出 tar 需先在列表中选中一行镜像
          </span>
        </div>
      </template>
    </DockerPage>

    <!-- 确认弹窗覆盖三种入口：清理悬空（强档逐字 DELETE）、单删（标准档）、导出覆盖（文件名档）。
         形态、逐字期望值与保护提示全部由组件按动作注册表推导；页面只传事实与 options。
         清理的补充输入（是否连带清理未使用镜像）由本页作为插槽内容给出。 -->
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

    <!-- 仓库凭据管理对话框（4c）：入口按钮只对 docker:config 渲染（不渲染 ≠ 禁用）。
         凭据是**全局**的（不属于任何一台主机 —— 服务端按仓库地址解析注入），故
         主机切换不需要像拉取对话框那样把它关掉。 -->
    <RegistryCredentialsDialog v-model="registryVisible" />
  </div>
</template>

<script setup lang="ts">
  import { computed, h, ref } from 'vue'
  import { useRouter } from 'vue-router'
  import { ElButton, ElCheckbox, ElEmpty, ElMessage, ElMessageBox } from 'element-plus'
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
  import DockerActionConfirm from '../components/action-confirm.vue'
  import DockerActionMenu from '../components/action-menu.vue'
  import DockerPage from '../components/docker-page.vue'
  import PullProgressDialog from '../components/pull-progress-dialog.vue'
  import RegistryCredentialsDialog from '../components/registry-credentials-dialog.vue'
  import type { ColumnOption } from '@/types/component'
  import type { DockerImageItem } from '../api'
  import {
    runErrorMessage,
    useDockerCmds,
    type DockerCmdRunResult
  } from '../composables/useDockerCmds'
  import { useDockerHostState } from '../composables/useDockerHostState'
  import { lookupDockerAction } from '../utils/actions'
  import { filterImages, imageTotals } from '../utils/snapshot'
  import { formatRelativeTime, imageRefText, inUseText } from '../utils/display'
  import { provideDockerHost } from '../utils/host-context'

  const router = useRouter()
  // 主机上下文是**页面级** provide/inject：DockerPage 与 HostSwitcher 都用 useDockerHost() 取它，
  // 而模块里没有别的 provide 调用方 —— 页面就是这一层的提供者，故在这里 provide 并直接用其返回值。
  // 不要解构：上下文字段是 getter，解构会把 hostId 定格成进入页面时的 ''（主机清单尚未到达）。
  const ctx = provideDockerHost()
  const { hasAuth } = useAuth()

  const showSearchBar = ref(false)
  const searchForm = ref<{ keyword?: string; danglingOnly?: boolean; unusedOnly?: boolean }>({})
  /** 当前勾选的行：底栏「打标签 / 导出 tar」一次只作用于选中的那一行。 */
  const selected = ref<DockerImageItem[]>([])
  /** 拉取进度对话框的开关（4b）：开在「当前主机」上，切换主机时关掉（见 onHostSwitch）。 */
  const pullVisible = ref(false)
  /** 仓库凭据对话框的开关（4c）：入口按钮受 canConfig 门控（模板里的 v-if）。 */
  const registryVisible = ref(false)

  const canManage = computed(() => hasAuth(PermDockerManage))
  const canDelete = computed(() => hasAuth(PermDockerDelete))
  /** 仓库凭据管理权限（4c）：与镜像写动作不同档 —— 服务端 /docker/registries 的静态 perm。 */
  const canConfig = computed(() => hasAuth(PermDockerConfig))
  /** 至少有一个写权限（或凭据权限）才渲染底栏操作区（没有可执行的动作就不占版面）。 */
  const canWrite = computed(() => canManage.value || canDelete.value)

  // 快照与四态收口在 composable（hosts 清单、seq 守卫、主机切换后的重拉都在它里面）。
  // 主机切换 = 换一台机器：本页既有重置纪律是「清空筛选 + 清空勾选」。
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
  } = useDockerHostState({
    onHostSwitch: () => {
      selected.value = []
      searchForm.value = {}
      // 拉取进度对话框跟着关：一场拉取属于受理它的那台主机（对话框把 hostId 在
      // 受理时钉死），切机后让它继续跑只会让进度与结论挂在错误的主机名下；关掉
      // 即断流，服务端随之取消那场拉取（断开 = 取消是端点契约，不是副作用）。
      pullVisible.value = false
    }
  })

  // 写指令通道：受理 + 轮询 + 成功后重拉（重拉就是上面的 refresh）。
  const { run, pendingId, busy } = useDockerCmds({ refresh })

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

  const filtered = computed(() => filterImages(state.value?.images ?? [], searchForm.value))

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
    void askTag(selected.value[0])
  }
  function onSaveSelected() {
    if (selected.value.length !== 1) return
    void askSave(selected.value[0])
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

  /** 收集一段输入的弹窗；取消/关闭返回空串（调用方据此中止，不发送指令）。 */
  async function promptText(title: string, message: string, placeholder: string): Promise<string> {
    try {
      const { value } = await ElMessageBox.prompt(message, title, {
        confirmButtonText: '确定',
        cancelButtonText: '取消',
        inputPlaceholder: placeholder,
        inputPattern: /\S/,
        inputErrorMessage: '内容不能为空'
      })
      return (value ?? '').trim()
    } catch {
      return ''
    }
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

  /** 打标签：src 取目标镜像的引用，dst 由用户输入。 */
  async function askTag(image: DockerImageItem) {
    const src = actionRef(image)
    const dst = await promptText(
      '打标签',
      `为镜像「${imageRefText(image)}」输入新的引用。`,
      '例如 仓库/名称:标签'
    )
    if (!dst) return
    await runWrite('image:tag', undefined, { src, dst })
  }

  /**
   * 导出 tar（两段时序）：
   *   1) 先不带覆盖标记发一次 —— 产物不存在时这就是常规路径；
   *   2) 结果 alreadyExists=true 时弹确认（组件据此推导「输入文件名」形态），
   *      用户照抄文件名后带 overwrite=true 重发。
   */
  async function askSave(image: DockerImageItem) {
    const target = actionRef(image)
    const filename = await promptText(
      '导出 tar',
      `导出镜像「${imageRefText(image)}」。只填文件名，产物落在该主机的 agent 下载目录。`,
      '例如 镜像名.tar'
    )
    if (!filename) return
    const res = await run({ action: 'image:save', target, options: { filename } })
    if (res.ok) {
      reportResult(res)
      return
    }
    if (res.alreadyExists === true) {
      confirmState.value = {
        visible: true,
        kind: 'save',
        action: 'image:save',
        target,
        options: { filename, overwrite: true }
      }
      return
    }
    reportResult(res)
  }

  /** 载入镜像：产物需已在该主机的 agent 下载目录（结论句写在输入提示里）。 */
  async function askLoad() {
    const filename = await promptText(
      '载入镜像',
      '只填文件名，文件需已放在该主机的 agent 下载目录。',
      '例如 镜像名.tar'
    )
    if (!filename) return
    await runWrite('image:load', undefined, { filename })
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

  // ── 确认弹窗（清理 / 删除 / 导出覆盖共用一个入口）──

  interface ConfirmState {
    visible: boolean
    /** 三种入口的提交分支不同：清理带勾选项、删除带 confirm、导出覆盖带文件名。 */
    kind: 'prune' | 'remove' | 'save'
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

  async function onConfirmSubmit(payload: { confirm: string; force: boolean }) {
    const st = confirmState.value
    confirmLoading.value = true
    try {
      if (st.kind === 'prune') {
        const options: Record<string, unknown> = pruneIncludeAll.value ? { all: true } : {}
        await runWrite('image:prune', undefined, options, payload.confirm)
      } else if (st.kind === 'remove') {
        await runWrite('image:remove', st.target, {}, payload.confirm)
      } else {
        // 第二段：覆盖标记 + 文件名逐字确认一起带上，服务端逐字校验后才会覆盖。
        const filename = String(st.options.filename ?? '')
        await runWrite('image:save', st.target, { filename, overwrite: true }, filename)
      }
    } finally {
      confirmLoading.value = false
      confirmState.value = { ...st, visible: false }
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
   * 页面另拼一份等于两处各维护一遍）；写动作（打标签/导出/删除）走 actions，标签、权限、
   * 危险色全部取自动作注册表。
   *
   * 「使用中」的镜像不把删除放进 actions：action-menu 的 disabled 是**整组**禁用，表达不了
   * 「只禁删除」（打标签/导出对在用镜像依然合法），故删除改为 extraItems 里的**禁用条目**，
   * 结论写在条目上（禁用的条目点不动，结论只能在看得见的地方给）。这是基础设施的缺口。
   */
  function rowMenuItems(row: DockerImageItem): { actions: string[]; extraItems: RowMenuItem[] } {
    const extraItems: RowMenuItem[] = [
      { key: 'detail', label: '详情', icon: 'ri:eye-line', auth: PermDockerInspect }
    ]
    const actions = ['image:tag', 'image:save']
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
        void askTag(row)
        return
      case 'image:save':
        void askSave(row)
        return
      case 'image:remove':
        openRemove(row)
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
  // 空态渲染在本页（ArtTable 不转发 `#empty`）：给它接近表格空态的留白。
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
