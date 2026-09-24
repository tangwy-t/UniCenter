<template>
  <div class="device-page art-full-height">
    <ArtSearchBar
      v-show="showSearchBar"
      v-model="searchForm"
      :items="searchItems"
      @search="handleSearch"
      @reset="handleReset"
    />

    <ElCard class="art-table-card" shadow="never">
      <ArtTableHeader
        v-model:columns="columnChecks"
        v-model:showSearchBar="showSearchBar"
        :loading="loading"
        @refresh="loadList"
      >
        <!-- 批量升级的两个入口：多选（勾选后可用）与「按当前筛选下发」。
             后者是**全量**入口（命中当前筛选的全部设备），故它的确认框会写明
             命中台数 —— 多一次点击影响上百台，不能不给影响面。 -->
        <template #left>
          <div v-if="canUpgrade" class="up-bar">
            <span class="up-bar__sel">已选 {{ selected.length }} 台</span>
            <ElButton
              size="small"
              type="primary"
              :disabled="selected.length === 0"
              @click="openUpgradeDialog(selected.map((r) => r.id))"
            >
              升级选中
            </ElButton>
            <ElButton
              size="small"
              :disabled="selected.length === 0"
              @click="
                openUpgradeDialog(
                  selected.map((r) => r.id),
                  'rollback'
                )
              "
            >
              回滚选中
            </ElButton>
            <ElButton size="small" plain @click="openFilterDispatch">按当前筛选下发…</ElButton>
          </div>
        </template>
      </ArtTableHeader>

      <ArtTable
        :loading="loading"
        :data="data"
        :columns="columns"
        :pagination="pagination"
        @pagination:size-change="handleSizeChange"
        @pagination:current-change="handleCurrentChange"
        @selection-change="onSelectionChange"
      />
    </ElCard>

    <!-- ============ 升级/回滚对话框（单台与批量共用）============ -->
    <ElDialog v-model="upgradeVisible" :title="dialogTitle" width="480px">
      <div class="up-dialog">
        <div class="up-dialog__targets">
          目标设备：{{ targetRows.length }} 台
          <span v-if="targetRows.length === 1 && targetRows[0] && targetRows[0].hostname">
            （{{ targetRows[0].hostname }}）
          </span>
        </div>
        <ElSelect v-model="chosenVersion" class="w-full" placeholder="选择一个版本">
          <ElOption v-for="v in versionOptions" :key="v" :label="v" :value="v" />
        </ElSelect>
        <div v-if="versionOptions.length === 0" class="up-dialog__empty">
          没有可用于这些设备的版本 —— 请先在「Agent 版本」页上传并发布程序包
        </div>
        <div v-else class="up-dialog__hint"> 只列出「已发布且这些设备的平台都有程序包」的版本 </div>
      </div>
      <template #footer>
        <ElButton @click="upgradeVisible = false">取消</ElButton>
        <ElButton
          type="primary"
          :disabled="!chosenVersion"
          :loading="submitting"
          @click="submitUpgrade"
        >
          {{ dialogActionText }}
        </ElButton>
      </template>
    </ElDialog>

    <!-- ============ 按筛选下发：先看影响面，再确认 ============ -->
    <ElDialog v-model="filterVisible" title="按当前筛选下发升级" width="460px">
      <div class="up-filter">
        <div class="up-filter__hint">按列表当前的筛选条件下发到全部命中设备</div>
        <ElSelect
          v-model="filterVersion"
          class="w-full"
          placeholder="选择一个版本"
          @change="loadFilterPreview"
        >
          <ElOption v-for="v in publishedVersions" :key="v" :label="v" :value="v" />
        </ElSelect>
        <div v-if="filterPreview" class="up-filter__preview">
          <div class="up-filter__num">{{ filterPreview.willUpgrade }}</div>
          <div class="up-filter__label">台设备将升级到 {{ filterPreview.targetVersion }}</div>
          <div v-if="skipLine" class="up-filter__skip">
            跳过 {{ skipTotal(filterPreview.skip) }} 台：{{ skipLine }}
          </div>
        </div>
        <div v-else-if="filterVersion" class="up-filter__loading">正在计算影响面…</div>
      </div>
      <template #footer>
        <ElButton @click="filterVisible = false">取消</ElButton>
        <ElButton
          type="primary"
          :disabled="!filterPreview || filterPreview.willUpgrade === 0"
          :loading="submitting"
          @click="submitFilterDispatch"
        >
          下发
        </ElButton>
      </template>
    </ElDialog>
  </div>
</template>

<script setup lang="ts">
  import { computed, h, reactive, ref } from 'vue'
  import { ElMessage, ElMessageBox, ElTag } from 'element-plus'
  import { useRouter } from 'vue-router'
  import {
    PermDeviceDelete,
    PermDeviceDisable,
    PermDeviceEnable,
    PermDeviceQuery,
    PermDeviceUpgrade
  } from '@/enums/permission'
  import { useAuth } from '@/hooks/core/useAuth'
  import { useDict } from '@/hooks/core/useDict'
  import { useTableColumns } from '@/hooks/core/useTableColumns'
  import { operationColumn } from '@/components/core/tables/operation-column'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtButtonMore from '@/components/core/forms/art-button-more/index.vue'
  import WatermarkBar from '../components/watermark-bar.vue'
  import type { DeviceQuery } from '../api'
  import {
    disableDevice,
    dispatchDeviceUpgrade,
    enableDevice,
    fetchAgentReleases,
    fetchDevice,
    fetchDevices,
    previewDeviceUpgrade,
    removeDevice
  } from '../api'
  import { availableVersions, skipText, skipTotal, tagTypeOf, upgradeBadge } from '../utils/upgrade'

  defineOptions({ name: 'DeviceList' })

  /**
   * 启停态复用系统开关字典（`sys_normal_disable`：1=正常 0=停用），
   * 与 `entity.DeviceStatusEnabled/Disabled` 的取值一致 —— 不新造字典。
   */
  const statusDict = useDict('sys_normal_disable', { numeric: true })
  const { hasAuth } = useAuth()
  const router = useRouter()

  const searchForm = ref<{ hostname?: string; status?: number; online?: boolean }>({})
  const showSearchBar = ref(false)
  const searchItems = computed(() => [
    {
      key: 'hostname',
      label: '主机名',
      type: 'input',
      placeholder: '请输入主机名',
      clearable: true
    },
    {
      key: 'status',
      label: '启停状态',
      type: 'select',
      placeholder: '请选择启停状态',
      clearable: true,
      options: statusDict.options.value
    },
    {
      key: 'online',
      label: '在线状态',
      type: 'select',
      placeholder: '请选择在线状态',
      clearable: true,
      // value 是布尔：后端 `online` 是 *bool，Service 才把它折算成 onlineSince。
      options: [
        { label: '在线', value: true },
        { label: '离线', value: false }
      ]
    }
  ])

  const canUpgrade = computed(() => hasAuth(PermDeviceUpgrade))
  const loading = ref(false)
  const data = ref<Api.Device.DeviceListItem[]>([])

  // ── 升级 / 回滚（W1 / W3 / W4）───────────────────────────────────────
  //
  // 两条入口共用同一个对话框：多选/单台（版本下拉按**选中设备的平台**过滤）
  // 与按筛选全量（先预演影响面再确认）。入口不同、动作相同 —— 都是「把目标
  // 版本写下去」，故不并成两套表单。
  const selected = ref<Api.Device.DeviceListItem[]>([])
  const releases = ref<Api.Device.AgentReleaseItem[]>([])
  const publishedVersions = ref<string[]>([])
  const upgradeVisible = ref(false)
  const filterVisible = ref(false)
  const submitting = ref(false)
  const mode = ref<'upgrade' | 'rollback'>('upgrade')
  const targetRows = ref<Api.Device.DeviceListItem[]>([])
  const chosenVersion = ref('')
  const filterVersion = ref('')
  const filterPreview = ref<Api.Device.DeviceUpgradePreviewResp | null>(null)

  const dialogTitle = computed(() => (mode.value === 'rollback' ? '回滚 Agent' : '升级 Agent'))
  const dialogActionText = computed(() => {
    const verb = mode.value === 'rollback' ? '回滚' : '升级'
    if (targetRows.value.length > 1) {
      return `${verb} ${targetRows.value.length} 台到 ${chosenVersion.value || '…'}`
    }
    return `${verb}到 ${chosenVersion.value || '…'}`
  })
  /** 版本下拉：已发布 ∩ 目标设备平台都有产物（不满足的版本不列出来，免得点完才知道跳过）。 */
  const versionOptions = computed(() =>
    availableVersions(publishedVersions.value, releases.value, targetRows.value)
  )
  const skipLine = computed(() => skipText(filterPreview.value && filterPreview.value.skip))

  function onSelectionChange(rows: Api.Device.DeviceListItem[]) {
    selected.value = rows
  }

  async function ensureReleaseData() {
    if (releases.value.length > 0) return
    const res = await fetchAgentReleases()
    releases.value = res.list || []
    publishedVersions.value = res.publishedVersions || []
  }

  async function openUpgradeDialog(ids: string[], nextMode: 'upgrade' | 'rollback' = 'upgrade') {
    mode.value = nextMode
    targetRows.value = data.value.filter((r) => ids.includes(r.id))
    chosenVersion.value = ''
    if (ids.length === 1 && nextMode === 'rollback') {
      // 单台回滚的版本由**服务端**从升级记录推导（「升级前是哪个版本」不该让人回忆）。
      try {
        const detail = await fetchDevice(ids[0])
        chosenVersion.value = detail.rollbackVersion || ''
      } catch {
        chosenVersion.value = ''
      }
    }
    upgradeVisible.value = true
    try {
      await ensureReleaseData()
    } catch (e) {
      ElMessage.error(errText(e, '版本列表加载失败'))
    }
  }

  async function submitUpgrade() {
    if (!chosenVersion.value || targetRows.value.length === 0) return
    const verb = mode.value === 'rollback' ? '回滚' : '升级'
    try {
      await ElMessageBox.confirm(
        `确认把 ${targetRows.value.length} 台设备${verb}到 ${chosenVersion.value} 吗？`,
        '提示',
        { type: 'warning' }
      )
    } catch {
      return
    }
    submitting.value = true
    try {
      const res = await dispatchDeviceUpgrade({
        version: chosenVersion.value,
        ids: targetRows.value.map((r) => r.id)
      })
      const skipped = skipTotal(res.skip)
      ElMessage.success(
        skipped > 0
          ? `已下发 ${res.dispatched} 台，跳过 ${skipped} 台（${skipText(res.skip)}）`
          : `已下发 ${res.dispatched} 台`
      )
      upgradeVisible.value = false
      selected.value = []
      await loadList()
    } catch (e) {
      ElMessage.error(errText(e, '下发失败'))
    } finally {
      submitting.value = false
    }
  }

  async function openFilterDispatch() {
    filterVersion.value = ''
    filterPreview.value = null
    filterVisible.value = true
    try {
      await ensureReleaseData()
    } catch (e) {
      ElMessage.error(errText(e, '版本列表加载失败'))
    }
  }

  /** 预演影响面（下发前给人看的数字：命中多少、跳过多少、分别为什么）。 */
  async function loadFilterPreview() {
    if (!filterVersion.value) return
    filterPreview.value = null
    try {
      filterPreview.value = await previewDeviceUpgrade({
        version: filterVersion.value,
        filter: currentFilter()
      })
    } catch (e) {
      ElMessage.error(errText(e, '影响面计算失败'))
    }
  }

  /**
   * 按筛选下发（带**影响面确认**）。
   *
   * `expectedCount` 是预演给出的命中台数：服务端重新求值后比对，不一致即 409 ——
   * 两次之间设备集合变了（新注册 / 被删 / 被停用）就说明影响面已经不是操作者
   * 确认过的那个，宁可让人重新看一眼，也不能静默照做或静默缩小。
   */
  async function submitFilterDispatch() {
    if (!filterVersion.value || !filterPreview.value) return
    submitting.value = true
    try {
      const res = await dispatchDeviceUpgrade({
        version: filterVersion.value,
        filter: currentFilter(),
        expectedCount: filterPreview.value.matched
      })
      const skipped = skipTotal(res.skip)
      ElMessage.success(
        skipped > 0
          ? `已下发 ${res.dispatched} 台，跳过 ${skipped} 台（${skipText(res.skip)}）`
          : `已下发 ${res.dispatched} 台`
      )
      filterVisible.value = false
      await loadList()
    } catch (e) {
      ElMessage.error(errText(e, '下发失败'))
      // 409（筛选结果已变化）之后旧数字已无意义：清掉逼着重新预演一次。
      filterPreview.value = null
    } finally {
      submitting.value = false
    }
  }

  /** 当前筛选条件（与列表请求**同一份**，保证「看到的就是发下去的」）。 */
  function currentFilter(): DeviceQuery {
    return {
      ...(searchForm.value.hostname ? { hostname: searchForm.value.hostname } : {}),
      ...(searchForm.value.status !== undefined && searchForm.value.status !== null
        ? { status: Number(searchForm.value.status) }
        : {}),
      ...(searchForm.value.online !== undefined && searchForm.value.online !== null
        ? { online: Boolean(searchForm.value.online) }
        : {})
    }
  }

  function errText(e: unknown, fallback: string): string {
    const msg = (e as { message?: string }) && (e as { message?: string }).message
    return msg && msg.trim() !== '' ? msg : fallback
  }
  const pagination = reactive({ current: 1, size: 10, total: 0 })
  /** 正在切启停态的设备 id：用于禁用行内按钮，避免重复提交。 */
  const togglingId = ref<string | null>(null)

  async function loadList() {
    loading.value = true
    try {
      const res = await fetchDevices({
        page: pagination.current,
        pageSize: pagination.size,
        ...(searchForm.value.hostname ? { hostname: searchForm.value.hostname } : {}),
        ...(searchForm.value.status !== undefined && searchForm.value.status !== null
          ? { status: Number(searchForm.value.status) }
          : {}),
        // online 的合法值含 false（离线），必须比 null/undefined 判空，
        // 不能用真值判断 —— 否则「只看离线」会退化成「不过滤」。
        ...(searchForm.value.online !== undefined && searchForm.value.online !== null
          ? { online: Boolean(searchForm.value.online) }
          : {}),
        // 字典只用于把 status 渲染成中文标签，与列表请求并行拉取。
        // （此处的 void 只是把它串进同一个 await，无其它副作用。）
        ...(await statusDict.ensure(), {})
      })
      data.value = res.list
      pagination.total = res.total
    } finally {
      loading.value = false
    }
  }

  /** unix 秒 → 本地时间；缺值显示「—」（与水位列同一约定）。 */
  function fmtUnixSeconds(v?: number | null) {
    if (v === undefined || v === null) return '—'
    const d = new Date(v * 1000)
    const p = (n: number) => String(n).padStart(2, '0')
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(
      d.getMinutes()
    )}:${p(d.getSeconds())}`
  }

  function onUpgradeAction(key: string, row: Api.Device.DeviceListItem) {
    if (key === 'upgrade') void openUpgradeDialog([row.id], 'upgrade')
    if (key === 'rollback') void openUpgradeDialog([row.id], 'rollback')
  }

  function openDetail(row: Api.Device.DeviceListItem) {
    // 用路由 name 跳转，不拼路径：详情路由是插件的隐藏路由（isHide），
    // 后端菜单里没有它，拼路径会在菜单模式切换时悄悄失效。
    router.push({ name: 'DeviceDetail', params: { id: row.id } })
  }

  async function onToggleStatus(row: Api.Device.DeviceListItem) {
    const next = row.status !== 1
    const act = next ? '启用' : '停用'
    try {
      await ElMessageBox.confirm(`确认要「${act}」设备「${row.hostname}」吗？`, '提示', {
        type: 'warning',
        confirmButtonText: '确定',
        cancelButtonText: '取消'
      })
    } catch {
      return // 用户取消
    }
    togglingId.value = row.id
    try {
      if (next) await enableDevice(row.id)
      else await disableDevice(row.id)
      row.status = next ? 1 : 0
      ElMessage.success(`已${act}`)
    } finally {
      togglingId.value = null
    }
  }

  async function onRemove(row: Api.Device.DeviceListItem) {
    try {
      await ElMessageBox.confirm(
        `确认删除设备「${row.hostname}」吗？删除后不可恢复。`,
        '删除确认',
        {
          type: 'warning',
          confirmButtonText: '确定',
          cancelButtonText: '取消'
        }
      )
    } catch {
      return // 用户取消
    }
    await removeDevice(row.id)
    ElMessage.success('已删除')
    // 删掉最后一页的最后一条时回退一页，避免停在空页。
    if (data.value.length === 1 && pagination.current > 1) pagination.current -= 1
    await loadList()
  }

  const { columns, columnChecks } = useTableColumns<Api.Device.DeviceListItem>(() => {
    const operationColumnConfig = operationColumn<Api.Device.DeviceListItem>({
      // 列宽按「实际渲染的按钮位」计算（助手公式：max(80, count×44+16)）。
      // 这里 = 1 个常驻图标按钮（详情）+ 1 个「更多」下拉按钮 = 2 位。
      // 注意：下拉**内部**的条目不再各占一位 —— 它们共享「更多」那一个按钮位。
      count:
        (hasAuth(PermDeviceQuery) ? 1 : 0) +
        (hasAuth(PermDeviceEnable) || hasAuth(PermDeviceDisable) || hasAuth(PermDeviceDelete)
          ? 1
          : 0),
      formatter: (row) => {
        const enabled = row.status === 1
        return h('div', { class: 'flex items-center' }, [
          // 主操作放外面
          h(ArtButtonTable, {
            type: 'view',
            title: '详情',
            auth: PermDeviceQuery,
            onClick: () => openDetail(row)
          }),
          // 次要操作收进「更多」——与外部按钮**不重复**（此前详情/启停/删除
          // 既在外面又在下拉里，内容重复且宽度不够，导致「更多」被截一半）
          h(ArtButtonMore, {
            list: [
              // 升级 / 回滚放在最前（这是设备域最常用的运维动作），
              // 且都只在有权限时出现 —— ArtButtonMore 的 item.auth 会自己过滤。
              {
                key: 'upgrade',
                label: '升级 Agent',
                icon: 'ri:upload-cloud-2-line',
                auth: PermDeviceUpgrade
              },
              {
                key: 'rollback',
                label: '回滚 Agent',
                icon: 'ri:arrow-go-back-line',
                auth: PermDeviceUpgrade
              },
              enabled
                ? {
                    key: 'toggle',
                    label: '停用',
                    icon: 'ri:stop-circle-line',
                    color: '#e6a23c',
                    auth: PermDeviceDisable
                  }
                : {
                    key: 'toggle',
                    label: '启用',
                    icon: 'ri:play-circle-line',
                    auth: PermDeviceEnable
                  },
              {
                key: 'delete',
                label: '删除',
                icon: 'ri:delete-bin-5-line',
                color: 'var(--art-danger)',
                auth: PermDeviceDelete
              }
            ],
            onClick: (item: { key: string | number }) => onMore(item, row)
          })
        ])
      }
    })

    return [
      // 选择列只在有升级权限时出现：没这个权限的人看到一个用不上的勾选框只会困惑。
      ...(canUpgrade.value
        ? [{ type: 'selection' as const, width: 46, selectable: () => true }]
        : []),
      { type: 'index', width: 60, label: '序号' },
      { prop: 'hostname', label: '主机名', minWidth: 180, showOverflowTooltip: true },
      { prop: 'os', label: '操作系统', width: 140, showOverflowTooltip: true },
      { prop: 'arch', label: '架构', width: 100 },
      {
        prop: 'agentVersion',
        label: 'Agent 版本',
        width: 190,
        // 版本列要一眼回答「这台机器该不该动」：当前版本 + （有目标时）目标与标记。
        // 标记的语义规则在 utils/upgrade.upgradeBadge 里（含「不支持远程升级」
        // 这类必须说清楚的结论），这里只负责渲染。
        formatter: (row: Api.Device.DeviceListItem) => {
          const children: any[] = [h('span', { class: 'up-ver__now' }, row.agentVersion || '—')]
          if (row.targetVersion && row.targetVersion !== row.agentVersion) {
            children.push(h('span', { class: 'up-ver__arrow' }, `→ ${row.targetVersion}`))
          }
          const badge = upgradeBadge(row)
          if (badge) {
            children.push(
              h(
                ElTag,
                {
                  size: 'small',
                  effect: 'light',
                  type: tagTypeOf(badge.tone),
                  title: badge.hint || undefined
                },
                () => badge.text
              )
            )
          }
          return h('div', { class: 'up-ver' }, children)
        }
      },
      {
        prop: 'online',
        label: '在线状态',
        width: 100,
        // 直接消费后端算好的 online（后端按 sys.agent.offlineThreshold
        // 折算），前端不得拿 lastSeenAt 自行算阈值 —— 阈值可热更。
        formatter: (row) =>
          h(ElTag, { size: 'small', type: row.online ? 'success' : 'info', effect: 'light' }, () =>
            row.online ? '在线' : '离线'
          )
      },
      {
        prop: 'status',
        label: '启停状态',
        width: 100,
        formatter: (row) => statusDict.render(row.status)
      },
      {
        prop: 'lastSeenAt',
        label: '最后上报',
        width: 170,
        formatter: (row) => fmtUnixSeconds(row.lastSeenAt)
      },
      // 水位三列来自列表接口拼的 Redis latest：缺值时**整个字段不出现**
      // （omitempty），一律显示「—」，不得把缺值当成 0。
      // 渲染成「条 + 数值」：多列并排时**条**负责一眼比高低，**数值**负责精确值，
      // 缺值则由组件渲染「—」且不画空条（空条会被误读成 0%）。见 watermark-bar.vue。
      {
        prop: 'cpuUsedPercent',
        label: 'CPU 使用率',
        width: 120,
        formatter: (row) => h(WatermarkBar, { value: row.cpuUsedPercent })
      },
      {
        prop: 'memUsedPercent',
        label: '内存使用率',
        width: 120,
        formatter: (row) => h(WatermarkBar, { value: row.memUsedPercent })
      },
      {
        prop: 'diskUsedPercent',
        label: '磁盘使用率',
        width: 120,
        formatter: (row) => h(WatermarkBar, { value: row.diskUsedPercent })
      },
      {
        prop: 'watermarkAt',
        label: '水位采样时间',
        width: 170,
        formatter: (row) => fmtUnixSeconds(row.watermarkAt)
      },
      ...(operationColumnConfig ? [operationColumnConfig] : [])
    ]
  })

  /** 兜底：窄屏时操作列放不下，走「更多」下拉执行同样的动作。 */
  async function onMore(item: { key: string | number }, row: Api.Device.DeviceListItem) {
    const key = String(item.key)
    if (key === 'detail') openDetail(row)
    else if (key === 'upgrade' || key === 'rollback') onUpgradeAction(key, row)
    else if (key === 'toggle') await onToggleStatus(row)
    else if (key === 'delete') await onRemove(row)
  }

  function handleSearch() {
    pagination.current = 1
    loadList()
  }
  function handleReset() {
    pagination.current = 1
    loadList()
  }
  function handleSizeChange(size: number) {
    pagination.size = size
    pagination.current = 1
    loadList()
  }
  function handleCurrentChange(current: number) {
    pagination.current = current
    loadList()
  }

  loadList()
</script>

<style lang="scss" scoped>
  /* 批量升级操作条（表头左侧插槽）：与表头图标按钮同一行，故只做对齐与分隔 */
  .up-bar {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;
    padding-left: 4px;

    &__sel {
      font-size: 12px;
      color: var(--el-text-color-regular);
      font-variant-numeric: tabular-nums;
    }
  }

  /* 版本列：当前版本 + 目标 + 标记（单行，窄列不挤压操作区） */
  .up-ver {
    display: flex;
    gap: 6px;
    align-items: center;

    &__now {
      font-variant-numeric: tabular-nums;
    }

    &__arrow {
      font-size: 12px;
      color: var(--el-color-warning);
      font-variant-numeric: tabular-nums;
    }
  }

  .up-dialog {
    display: flex;
    flex-direction: column;
    gap: 10px;

    &__targets {
      font-size: 13px;
      color: var(--el-text-color-primary);
    }

    &__hint,
    &__empty {
      font-size: 12px;
      color: var(--el-text-color-secondary);
    }
  }

  /* 影响面：大号数字是这个确认框唯一的「仪式感」，花在影响面上而不是装饰上 */
  .up-filter {
    display: flex;
    flex-direction: column;
    gap: 12px;

    &__hint {
      font-size: 12px;
      color: var(--el-text-color-secondary);
    }

    &__preview {
      padding: 8px 0 4px;
      text-align: center;
    }

    &__num {
      font-size: 34px;
      font-weight: 650;
      line-height: 1.1;
      color: var(--el-text-color-primary);
      font-variant-numeric: tabular-nums;
    }

    &__label {
      margin-top: 2px;
      font-size: 13px;
      color: var(--el-text-color-regular);
    }

    &__skip {
      margin-top: 8px;
      font-size: 12px;
      color: var(--el-text-color-secondary);
    }

    &__loading {
      font-size: 12px;
      color: var(--el-text-color-secondary);
    }
  }
</style>
