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
        <!-- 页面级动作只留「按当前筛选下发」（**全量**入口：命中当前筛选的全部设备，
             故确认框写明命中台数 —— 多一次点击影响上百台，不能不给影响面）。
             形态锚定兄弟列表页的工具条先例：#left → toolbar-left → ArtButtonTable
             （见 system-user/views/index.vue:33、system-role/views/index.vue:18）。
             选中相关动作改走下方批量条（本次统一：原「已选 0 台 + 三个常驻大按钮」
             与全站列表页不一致）。 -->
        <template #left>
          <div class="toolbar-left">
            <ArtButtonTable
              v-if="canUpgrade"
              icon="ri:send-plane-2-line"
              iconClass="bg-theme/12 text-theme"
              title="按当前筛选下发"
              @click="openFilterDispatch"
            />
          </div>
        </template>
      </ArtTableHeader>

      <!-- 首拉失败：表格区整块错误态 + 重试（不再静默留旧数据）。全局 toast
           仍由 utils/http 弹，这里是页面内联的结论与出路。 -->
      <ElEmpty v-if="loadError" class="dev-empty" description="设备列表加载失败">
        <ElButton size="small" @click="loadList">重试</ElButton>
      </ElEmpty>

      <ArtTable
        v-else
        ref="tableRef"
        :loading="loading"
        :data="data"
        :columns="columns"
        :pagination="pagination"
        :empty-text="emptyText"
        row-key="id"
        @pagination:size-change="handleSizeChange"
        @pagination:current-change="handleCurrentChange"
        @selection-change="onSelectionChange"
        @sort-change="handleSortChange"
      >
        <!-- 空态动作：筛选命中 0 时给「清除筛选」（无设备的空态不给——
             那时按钮没有去处）。 -->
        <template v-if="filterChips.length > 0 && !loading && data.length === 0" #empty-bottom>
          <ElButton size="small" @click="clearAllFilters">清除筛选</ElButton>
        </template>
      </ArtTable>
    </ElCard>

    <!-- ============ 升级/回滚对话框（单台升级与批量升级共用）============ -->
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

    <!-- ============ 批量回滚：每台回自己的上一版本（不共用版本下拉）============
         版本来源是服务端 detail.rollbackVersion（升级记录推导），按版本分组下发；
         无记录的设备不参与（列出并标注，静默跳过会让人以为回滚了）。 -->
    <ElDialog v-model="rollbackVisible" title="回滚 Agent" width="560px">
      <div class="rb-dialog">
        <div v-if="rollbackLoading" class="rb-dialog__loading">正在读取各设备的回滚版本…</div>
        <template v-else>
          <ElTable :data="rollbackTargets" size="small" max-height="22rem">
            <ElTableColumn prop="hostname" label="设备" min-width="180" show-overflow-tooltip />
            <ElTableColumn label="当前版本" width="130">
              <template #default="{ row }">{{ row.agentVersion || '—' }}</template>
            </ElTableColumn>
            <ElTableColumn label="回滚到" width="130">
              <template #default="{ row }">
                <span v-if="row.rollbackVersion" class="rb-dialog__ver">{{ row.rollbackVersion }}</span>
                <span v-else class="rb-dialog__skip">无回滚记录（将跳过）</span>
              </template>
            </ElTableColumn>
          </ElTable>
          <div class="rb-dialog__count">
            {{ rollbackDispatchable }} 台将回滚
          </div>
        </template>
      </div>
      <template #footer>
        <ElButton @click="rollbackVisible = false">取消</ElButton>
        <ElButton
          type="primary"
          :disabled="!rollbackTargets.length || rollbackDispatchable === 0 || rollbackLoading"
          :loading="submitting"
          @click="submitRollback"
        >
          回滚 {{ rollbackDispatchable }} 台
        </ElButton>
      </template>
    </ElDialog>

    <!-- ============ 按筛选下发：先看影响面，再确认 ============ -->
    <ElDialog v-model="filterVisible" title="按当前筛选下发升级" width="460px">
      <div class="up-filter">
        <div class="up-filter__hint">按列表当前的筛选条件下发到全部命中设备</div>
        <!-- 生效条件枚举（数据值）：与列表请求同一份 currentFilter()。 -->
        <div v-if="filterChips.length > 0" class="up-filter__conds">
          <span v-for="c in filterChips" :key="c.key">{{ c.label }}：{{ c.text }}</span>
        </div>
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

    <!-- ============ 设置分组 / 标签（两字段小编辑，部分更新）============ -->
    <ElDialog v-model="groupDialogVisible" title="设置分组" width="440px">
      <ElForm label-width="72px">
        <ElFormItem label="分组">
          <ElInput
            v-model="groupForm.group"
            placeholder="请输入分组"
            maxlength="64"
            show-word-limit
            clearable
          />
        </ElFormItem>
        <ElFormItem label="标签">
          <ElInput
            v-model="groupForm.tags"
            placeholder="多个标签用逗号分隔"
            maxlength="255"
            show-word-limit
            clearable
          />
        </ElFormItem>
      </ElForm>
      <template #footer>
        <ElButton @click="groupDialogVisible = false">取消</ElButton>
        <ElButton type="primary" :loading="groupSubmitting" @click="submitGroupDialog">
          保存
        </ElButton>
      </template>
    </ElDialog>

    <!-- ── 批量操作浮条 ───────────────────────────────────────────────
         形态照抄全站既有先例 system-file/views/index.vue（`batch-bar`：圆角浮条 +
         图标 + 计数 + 分隔线 + ArtButtonTable 图标钮），只在有选中时出现 ——
         与「零占用」取向一致：没选任何行时工具条里不出现批量按钮。
         统计/交互不一致时以 system-file 那份为准，改样式请两处同步（收敛路径）。 -->
    <transition name="batch-bar">
      <div v-if="selected.length" class="batch-bar">
        <span class="batch-icon"><ArtSvgIcon icon="ri:checkbox-multiple-line" /></span>
        <span class="batch-text">已选 <b class="text-theme-aa">{{ selected.length }}</b> 台</span>
        <span class="batch-divider" />
        <ArtButtonTable
          class="!mr-0"
          icon="ri:arrow-up-line"
          iconClass="bg-theme/12 text-theme"
          :title="`升级选中（${selected.length}）`"
          @click="openUpgradeDialog(selected.map((r) => r.id))"
        />
        <ArtButtonTable
          class="!mr-0"
          icon="ri:arrow-go-back-line"
          iconClass="bg-warning/12 text-warning"
          :title="`回滚选中（${selected.length}）`"
          @click="openRollbackDialog"
        />
      </div>
    </transition>
  </div>
</template>

<script setup lang="ts">
  import { computed, h, onActivated, onMounted, reactive, ref } from 'vue'
  import { ElMessage, ElMessageBox, ElTag } from 'element-plus'
  import { useRoute, useRouter } from 'vue-router'
  import {
    PermDeviceDelete,
    PermDeviceDisable,
    PermDeviceEnable,
    PermDeviceGroup,
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
  import type { DeviceGroupTagsPayload, DeviceQuery, DeviceSortBy } from '../api'
  import {
    disableDevice,
    dispatchDeviceUpgrade,
    enableDevice,
    fetchAgentReleases,
    fetchDevice,
    fetchDevices,
    previewDeviceUpgrade,
    removeDevice,
    updateDeviceGroup
  } from '../api'
  import { availableVersions, skipText, skipTotal, tagTypeOf, upgradeBadge } from '../utils/upgrade'
  import { EMPTY_TEXT, formatRelative, formatUnixSeconds } from '../utils/display'

  defineOptions({ name: 'DeviceList' })

  /**
   * 启停态复用系统开关字典（`sys_normal_disable`：1=正常 0=停用），
   * 与 `entity.DeviceStatusEnabled/Disabled` 的取值一致 —— 不新造字典。
   */
  const statusDict = useDict('sys_normal_disable', { numeric: true })
  const { hasAuth } = useAuth()
  const router = useRouter()
  const route = useRoute()

  const searchForm = ref<{
    hostname?: string
    status?: number
    online?: boolean
    primaryIp?: string
    agentVersion?: string
    group?: string
  }>({})
  const showSearchBar = ref(false)

  // 分组筛选下拉的选项源：跨次加载**累积**去重（列表分页，单页去重会漏掉不在
  // 本页的分组；保留历史值保证「切到某分组后仍能切回其它分组」）。
  const knownGroups = ref<string[]>([])
  const groupOptions = computed(() => knownGroups.value.map((g) => ({ label: g, value: g })))
  const searchItems = computed(() => [
    {
      key: 'hostname',
      label: '主机名',
      type: 'input',
      placeholder: '请输入主机名',
      clearable: true,
      // 回车即提交（props 透传到 ElInput，onKeyup 随 attrs 落到原生 input 上）。
      props: { onKeyup: (e: KeyboardEvent) => e.key === 'Enter' && handleSearch() }
    },
    {
      key: 'primaryIp',
      label: 'IP',
      type: 'input',
      placeholder: '请输入 IP',
      clearable: true,
      // 模糊匹配（后端 LIKE），前缀/片段都命中；回车即提交，与主机名同款。
      props: { onKeyup: (e: KeyboardEvent) => e.key === 'Enter' && handleSearch() }
    },
    {
      key: 'agentVersion',
      label: 'Agent 版本',
      // 标签含空格 + 二字中文，约 70px 文本；默认 70px 宽的 label 盒
      //（border-box，右内边距 12px → 内容仅 ~58px）装不下，会在空格处折行。
      // 给本项单独放宽到 92px（内容 ~80px），单行显示，不动 art-search-bar 全局默认。
      labelWidth: '92px',
      type: 'input',
      placeholder: '精确匹配',
      clearable: true,
      // 精确匹配（后端 =）：placeholder 直接给结论「精确匹配」，不解释。
      props: { onKeyup: (e: KeyboardEvent) => e.key === 'Enter' && handleSearch() }
    },
    {
      // 分组筛选：**select**（非 input）—— 分组名是自填的自由文本，input 精确匹配
      // 要人凭记忆复述、错一个字符即静默 0 命中；下拉给出「当前确实存在的分组」，
      // 免记忆、免拼错。选项来自跨次加载累积去重的 knownGroups（分页列表的可靠源）。
      key: 'group',
      label: '分组',
      type: 'select',
      placeholder: '请选择分组',
      clearable: true,
      filterable: true,
      options: groupOptions.value
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

  // ── 服务端排序（sortBy/sortOrder → 后端白名单）────────────────────────
  //
  // 状态只存这两条，翻页/筛选/重查都随 query 一起发。空 = 默认排序
  // （后端 lastSeenAt desc，与不传排序等价）—— 不在本地复制默认值，
  // 保证「前端空 ⇔ 后端默认」永远同口径。
  const sortBy = ref<DeviceSortBy | ''>('')
  const sortOrder = ref<'asc' | 'desc' | ''>('')

  /**
   * ElTable sort-change 事件的落点（列声明 sortable:'custom'，排序由服务端做）。
   *
   * EP 的三态循环：升 → 降 → 取消（order 为 null）。取消排序 = 清回默认
   * （清空两条状态，后端回落 lastSeenAt desc），而不是停在最后一次方向。
   * 未知 prop 一律忽略（列集合里只有三个可排键，多出来的列头点击不该改排序）。
   */
  const SORTABLE_PROPS = new Set(['lastSeenAt', 'hostname', 'agentVersion'])
  function handleSortChange({ prop, order }: { prop?: string; order?: string | null }) {
    if (!prop || !SORTABLE_PROPS.has(prop)) return
    if (order === 'ascending') {
      sortBy.value = prop as DeviceSortBy
      sortOrder.value = 'asc'
    } else if (order === 'descending') {
      sortBy.value = prop as DeviceSortBy
      sortOrder.value = 'desc'
    } else {
      // 三态循环的第三态：清回默认排序
      sortBy.value = ''
      sortOrder.value = ''
    }
    pagination.current = 1
    loadList()
  }

  const canUpgrade = computed(() => hasAuth(PermDeviceUpgrade))
  const loading = ref(false)
  const data = ref<Api.Device.DeviceListItem[]>([])
  /** 首拉失败：表格区整块错误态 + 重试（未成功过一次才置位，其后刷新失败保旧数据）。 */
  const loadError = ref(false)
  /** 表格 ref：clearSelection 用（下发/回滚完成后清勾选）。 */
  const tableRef = ref<{ elTableRef?: { clearSelection: () => void } }>()

  // ── 升级 / 回滚（W1 / W3 / W4）───────────────────────────────────────
  //
  // 三条入口：多选升级/单台升级（版本下拉按**选中设备的平台**过滤）、批量回滚
  //（每台回**它自己的** rollbackVersion，不共用版本）、按筛选全量（先预演
  // 影响面再确认）。入口不同、动作相同 —— 都是「把目标版本写下去」，
  // 故升级路径共用 submitUpgrade，回滚单走 submitRollback（按版本分组）。
  const selected = ref<Api.Device.DeviceListItem[]>([])
  const releases = ref<Api.Device.AgentReleaseItem[]>([])
  const publishedVersions = ref<string[]>([])
  const upgradeVisible = ref(false)
  const rollbackVisible = ref(false)
  const filterVisible = ref(false)
  const submitting = ref(false)
  const mode = ref<'upgrade' | 'rollback'>('upgrade')
  const targetRows = ref<Api.Device.DeviceListItem[]>([])
  const chosenVersion = ref('')
  const filterVersion = ref('')
  const filterPreview = ref<Api.Device.DeviceUpgradePreviewResp | null>(null)

  // ── 批量回滚（对话内容与提交逻辑独立于升级对话框）──────────────────
  /** 回滚目标清单：hostname / 当前版本 / rollbackVersion（无则不参与）。 */
  interface RollbackTarget {
    id: string
    hostname: string
    agentVersion: string
    rollbackVersion: string
  }
  const rollbackTargets = ref<RollbackTarget[]>([])
  const rollbackLoading = ref(false)
  const rollbackDispatchable = computed(
    () => rollbackTargets.value.filter((t) => t.rollbackVersion).length
  )

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

  /** 勾选态随数据/翻页复原后清空本地副本并通知 ElTable（两处都要做）。 */
  function clearSelection() {
    selected.value = []
    tableRef.value?.elTableRef?.clearSelection()
  }

  async function ensureReleaseData() {
    if (releases.value.length > 0) return
    const res = await fetchAgentReleases()
    releases.value = res.list || []
    publishedVersions.value = res.publishedVersions || []
  }

  /**
   * 打开升级/单台回滚对话框。
   *
   * 只有 ids.length === 1 且 mode === 'rollback' 时走服务端推导 —— 批量回滚
   * 不走这里（openRollbackDialog），那是一个完全不同的对话框（每台各回各的
   * rollbackVersion，而不是把一批设备都写到一个自选版本上）。
   */
  async function openUpgradeDialog(
    ids: string[],
    nextMode: 'upgrade' | 'rollback' = 'upgrade',
    presetVersion = ''
  ) {
    mode.value = nextMode
    targetRows.value = data.value.filter((r) => ids.includes(r.id))
    chosenVersion.value = presetVersion
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

  /**
   * 批量回滚入口：对选中设备逐一取服务端 rollbackVersion，构造「设备→上一版本」
   * 映射；无记录的行进清单但**不参与**下发（「将跳过」标注在行上）。
   * 并发取详情（一次选中通常 ≤ 百台，fetchDevice 是廉价的单查）。
   */
  async function openRollbackDialog() {
    if (selected.value.length === 0) return
    rollbackTargets.value = []
    rollbackVisible.value = true
    rollbackLoading.value = true
    const rows = [...selected.value]
    try {
      const details = await Promise.allSettled(rows.map((r) => fetchDevice(r.id)))
      rollbackTargets.value = rows.map((row, i) => {
        const res = details[i]
        const rollbackVersion =
          res.status === 'fulfilled' ? res.value.rollbackVersion || '' : ''
        return {
          id: row.id,
          hostname: row.hostname,
          agentVersion: row.agentVersion || '',
          rollbackVersion
        }
      })
    } finally {
      rollbackLoading.value = false
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
      // 多台批量：toast 讲结论，去向是任务页深链（?id= 落到刚建的任务上）；
      // 单台：留在本页继续操作（一次回车就是一条结论，跳页反而打断节奏）。
      if (targetRows.value.length > 1 && res.taskId) {
        upgradeVisible.value = false
        clearSelection()
        void router.push({ path: '/device/upgrade-task', query: { id: res.taskId } })
        return
      }
      ElMessage.success(
        skipped > 0
          ? `已下发 ${res.dispatched} 台，跳过 ${skipped} 台（${skipText(res.skip)}）`
          : `已下发 ${res.dispatched} 台`
      )
      upgradeVisible.value = false
      clearSelection()
      await loadList()
    } catch (e) {
      ElMessage.error(errText(e, '下发失败'))
    } finally {
      submitting.value = false
    }
  }

  /**
   * 批量回滚提交：按「目标版本」分组、每组一次 dispatchDeviceUpgrade
   * （{version, ids:[…]}）。一组失败不停其余组（单组失败只报它自己），
   * 全部结果汇成一条结论。
   */
  async function submitRollback() {
    const dispatchable = rollbackTargets.value.filter((t) => t.rollbackVersion)
    if (dispatchable.length === 0) return
    try {
      await ElMessageBox.confirm(`确认把 ${dispatchable.length} 台设备回滚到各自上一版本吗？`, '提示', {
        type: 'warning'
      })
    } catch {
      return
    }
    submitting.value = true
    // 按目标版本分组（一次回滚通常都落在同一个版本上，但退化情形也要正确）。
    const groups = new Map<string, string[]>()
    for (const t of dispatchable) {
      const ids = groups.get(t.rollbackVersion) ?? []
      ids.push(t.id)
      groups.set(t.rollbackVersion, ids)
    }
    let ok = 0
    const failures: string[] = []
    try {
      for (const [version, ids] of groups) {
        try {
          const res = await dispatchDeviceUpgrade({ version, ids })
          ok += res.dispatched
        } catch (e) {
          failures.push(`${version}：${errText(e, '下发失败')}`)
        }
      }
      if (failures.length > 0) {
        ElMessage.error(`已回滚 ${ok} 台；${failures.join('；')}`)
      } else {
        ElMessage.success(`已回滚 ${ok} 台`)
      }
      rollbackVisible.value = false
      clearSelection()
      await loadList()
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
      // 全量下发是高影响面操作：发完直接去任务页深链看逐台进度
      //（toast 的台数在这里只是复核，进度才是要盯的东西）。
      filterVisible.value = false
      if (res.taskId) {
        void router.push({ path: '/device/upgrade-task', query: { id: res.taskId } })
        return
      }
      ElMessage.success(
        skipped > 0
          ? `已下发 ${res.dispatched} 台，跳过 ${skipped} 台（${skipText(res.skip)}）`
          : `已下发 ${res.dispatched} 台`
      )
      await loadList()
    } catch (e) {
      ElMessage.error(errText(e, '下发失败'))
      // 409（筛选结果已变化）之后旧数字已无意义：清掉逼着重新预演一次。
      filterPreview.value = null
    } finally {
      submitting.value = false
    }
  }

  /** 当前筛选条件（与列表请求**同一份**，保证「看到的就是发下去的」）。
   *  排序不进筛选（它不改变命中集合，只改变行序——「按当前筛选下发」的
   *  影响面与排序无关，expectedCount 的比对不该被排序扰动）。 */
  function currentFilter(): DeviceQuery {
    return {
      ...(searchForm.value.hostname ? { hostname: searchForm.value.hostname } : {}),
      ...(searchForm.value.primaryIp ? { primaryIp: searchForm.value.primaryIp } : {}),
      ...(searchForm.value.agentVersion ? { agentVersion: searchForm.value.agentVersion } : {}),
      ...(searchForm.value.group ? { group: searchForm.value.group } : {}),
      ...(searchForm.value.status !== undefined && searchForm.value.status !== null
        ? { status: Number(searchForm.value.status) }
        : {}),
      ...(searchForm.value.online !== undefined && searchForm.value.online !== null
        ? { online: Boolean(searchForm.value.online) }
        : {})
    }
  }

  /** 生效筛选的 chips（label + 实际值）：纯数据，不解释。 */
  interface FilterChip {
    key: 'hostname' | 'status' | 'online' | 'primaryIp' | 'agentVersion' | 'group'
    label: string
    text: string
  }
  const filterChips = computed<FilterChip[]>(() => {
    const chips: FilterChip[] = []
    if (searchForm.value.hostname) {
      chips.push({ key: 'hostname', label: '主机名', text: searchForm.value.hostname })
    }
    if (searchForm.value.primaryIp) {
      chips.push({ key: 'primaryIp', label: 'IP', text: searchForm.value.primaryIp })
    }
    if (searchForm.value.agentVersion) {
      chips.push({
        key: 'agentVersion',
        label: 'Agent 版本',
        text: searchForm.value.agentVersion
      })
    }
    if (searchForm.value.group) {
      chips.push({ key: 'group', label: '分组', text: searchForm.value.group })
    }
    if (searchForm.value.status !== undefined && searchForm.value.status !== null) {
      chips.push({
        key: 'status',
        label: '启停状态',
        text: statusDict.labelOf(searchForm.value.status) || String(searchForm.value.status)
      })
    }
    if (searchForm.value.online !== undefined && searchForm.value.online !== null) {
      chips.push({
        key: 'online',
        label: '在线状态',
        text: searchForm.value.online ? '在线' : '离线'
      })
    }
    return chips
  })

  function clearAllFilters() {
    searchForm.value = {}
    handleSearch()
  }

  function errText(e: unknown, fallback: string): string {
    const msg = (e as { message?: string }) && (e as { message?: string }).message
    return msg && msg.trim() !== '' ? msg : fallback
  }

  /** 复制文本（IP 列点击即复制；与详情页 copyText 同款交互）。 */
  async function copyText(text: string) {
    if (!text) return
    try {
      await navigator.clipboard.writeText(text)
      ElMessage.success('已复制')
    } catch {
      ElMessage.warning('复制失败，请手动选择文本')
    }
  }
  const pagination = reactive({ current: 1, size: 10, total: 0 })
  /** 正在切启停态的设备 id：用于禁用行内按钮，避免重复提交。 */
  const togglingId = ref<string | null>(null)

  /** 累积本次会话已见过的非空分组（升序去重）：分组筛选下拉的选项源。 */
  function collectGroups(rows: Api.Device.DeviceListItem[]) {
    const set = new Set(knownGroups.value)
    for (const r of rows) if (r.group) set.add(r.group)
    knownGroups.value = [...set].sort((a, b) => a.localeCompare(b))
  }

  async function loadList() {
    loading.value = true
    try {
      const res = await fetchDevices({
        page: pagination.current,
        pageSize: pagination.size,
        ...(searchForm.value.hostname ? { hostname: searchForm.value.hostname } : {}),
        ...(searchForm.value.primaryIp ? { primaryIp: searchForm.value.primaryIp } : {}),
        ...(searchForm.value.agentVersion ? { agentVersion: searchForm.value.agentVersion } : {}),
        ...(searchForm.value.group ? { group: searchForm.value.group } : {}),
        ...(searchForm.value.status !== undefined && searchForm.value.status !== null
          ? { status: Number(searchForm.value.status) }
          : {}),
        // online 的合法值含 false（离线），必须比 null/undefined 判空，
        // 不能用真值判断 —— 否则「只看离线」会退化成「不过滤」。
        ...(searchForm.value.online !== undefined && searchForm.value.online !== null
          ? { online: Boolean(searchForm.value.online) }
          : {}),
        // 服务端排序：空 = 默认（后端 lastSeenAt desc）；两键只成对出现。
        ...(sortBy.value ? { sortBy: sortBy.value } : {}),
        ...(sortBy.value && sortOrder.value ? { sortOrder: sortOrder.value } : {}),
        // 字典只用于把 status 渲染成中文标签，与列表请求并行拉取。
        // （此处的 void 只是把它串进同一个 await，无其它副作用。）
        ...(await statusDict.ensure(), {})
      })
      data.value = res.list
      collectGroups(res.list)
      pagination.total = res.total
      loadError.value = false
    } catch {
      // 首拉失败：置错误态（表格区换整块重试）；曾成功过则保旧数据不换脸
      // —— 全局 toast 已由 utils/http 弹过，这里只决定页面形态。
      if (data.value.length === 0) loadError.value = true
    } finally {
      loading.value = false
    }
  }

  /** 空态两分：有筛选命中 0 →「无匹配结果」（给清除按钮）；无设备 →「暂无数据」。 */
  const emptyText = computed(() =>
    filterChips.value.length > 0 ? '无匹配结果' : '暂无数据'
  )

  function onUpgradeAction(key: string, row: Api.Device.DeviceListItem) {
    if (key === 'upgrade') void openUpgradeDialog([row.id], 'upgrade')
    if (key === 'rollback') void openUpgradeDialog([row.id], 'rollback')
  }

  function openDetail(row: Api.Device.DeviceListItem) {
    // 用 path 跳转（不用 name）：后端菜单模式下菜单托管路由注册名是
    // `menu-<id>`（MenuProcessor.convert），插件声明的 name（DeviceDetail 等）
    // 根本不存在，按 name 导航会报 `No match for {"name":"DeviceDetail"...}`。
    // path 在两种菜单模式下都逐字一致（隐藏路由按原 path 追加注册），与注册形态解耦。
    router.push({ path: `/device/detail/${row.id}` })
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
    try {
      await removeDevice(row.id)
    } catch (e) {
      // 删除失败走 errText toast（与下发/回滚同一口径），列表不动（行还在）。
      ElMessage.error(errText(e, '删除失败'))
      return
    }
    ElMessage.success('已删除')
    // 删掉最后一页的最后一条时回退一页，避免停在空页。
    if (data.value.length === 1 && pagination.current > 1) pagination.current -= 1
    await loadList()
  }

  // ── 设置分组 / 标签（部分更新对话框）──────────────────────────────
  //
  // 轻量编辑（两个字段）：用**小对话框**而非抽屉/新页 —— 本仓裁决「页面优先于
  // 抽屉」针对的是**展示面**；两字段写操作与模块内其它小编辑（删除/回滚确认）
  // 同形制，对话框是恰当的载体。
  const groupDialogVisible = ref(false)
  const groupTarget = ref<Api.Device.DeviceListItem | null>(null)
  const groupSubmitting = ref(false)
  /** 编辑态与提交前的快照：只有**变化**的字段才进 PATCH body（部分更新语义）。 */
  const groupForm = reactive({ group: '', tags: '' })
  const groupOriginal = reactive({ group: '', tags: '' })

  function openGroupDialog(row: Api.Device.DeviceListItem) {
    groupTarget.value = row
    groupForm.group = row.group ?? ''
    groupForm.tags = row.tags ?? ''
    groupOriginal.group = groupForm.group
    groupOriginal.tags = groupForm.tags
    groupDialogVisible.value = true
  }

  /**
   * 提交分组/标签：只把**与打开时不同**的字段放进 body（未动的字段不出现，
   * 后端不动它）；清空是显式空串（如把有分组的设备改回未分组）。两字段都没变
   * 就不发请求，直接关框。
   */
  async function submitGroupDialog() {
    const row = groupTarget.value
    if (!row) return
    const payload: DeviceGroupTagsPayload = {}
    if (groupForm.group !== groupOriginal.group) payload.group = groupForm.group
    if (groupForm.tags !== groupOriginal.tags) payload.tags = groupForm.tags
    if (Object.keys(payload).length === 0) {
      groupDialogVisible.value = false
      return
    }
    groupSubmitting.value = true
    try {
      await updateDeviceGroup(row.id, payload)
      ElMessage.success('已保存')
      groupDialogVisible.value = false
      await loadList()
    } catch (e) {
      // 400（超长）/404（设备不存在）的结论句由 HttpError 透出，如实呈现。
      ElMessage.error(errText(e, '保存失败'))
    } finally {
      groupSubmitting.value = false
    }
  }

  const { columns, columnChecks } = useTableColumns<Api.Device.DeviceListItem>(() => {
    const operationColumnConfig = operationColumn<Api.Device.DeviceListItem>({
      // 列宽按「实际渲染的按钮位」计算（助手公式：max(80, count×44+16)）。
      // 这里 = 1 个常驻图标按钮（详情）+ 1 个「更多」下拉按钮 = 2 位。
      // 注意：下拉**内部**的条目不再各占一位 —— 它们共享「更多」那一个按钮位。
      count:
        (hasAuth(PermDeviceQuery) ? 1 : 0) +
        (hasAuth(PermDeviceEnable) ||
        hasAuth(PermDeviceDisable) ||
        hasAuth(PermDeviceDelete) ||
        hasAuth(PermDeviceGroup)
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
              {
                // 设置分组/标签（部分更新）：按 device:index:group 门控，
                // ArtButtonMore 会按 item.auth 自行过滤（无权限即不出现）。
                key: 'setGroup',
                label: '设置分组',
                icon: 'ri:price-tag-3-line',
                auth: PermDeviceGroup
              },
              enabled
                ? {
                    key: 'toggle',
                    label: '停用',
                    icon: 'ri:stop-circle-line',
                    // 文字对比度 AA（收尾批）：原老字面量 #e6a23c（旧 EP warning，
                    // 白底 2.19）改走 token，数字见 @styles/core/aa-text.scss。
                    color: 'var(--aa-warning-text)',
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
                // 文字对比度 AA（收尾批）：原 --art-danger（白底 3.29）改走 token。
                color: 'var(--aa-danger-text)',
                auth: PermDeviceDelete
              }
            ],
            onClick: (item: { key: string | number }) => onMore(item, row)
          })
        ])
      }
    })

    return [
      // 列的多设备优先级按**运维排查顺序**排定（不是按重要性一刀切全隐藏）：
      //   「是哪台、活没活、能怎么处置」三件事任何宽度都必须看得见；
      //   版本 / 启停 / 最后上报是第二层（平板起，`hideBelow: 'tablet'`）；
      //   静态档案与资源细节是第三层（桌面起，`hideBelow: 'desktop'`）——
      //   水位在总览页有专门的图与快照表，列表页不占窄屏宽度。
      // 列宽口径：数据列一律 minWidth（随容器伸缩），仅 selection / index /
      // 操作列这类结构性列用固定 width。断点定义见 src/config/breakpoints.ts。
      // 选择列只在有升级权限时出现：没这个权限的人看到一个用不上的勾选框只会困惑。
      // reserve-selection：跨页勾选保持（翻页/删除/重拉后勾选不丢）——前提是
      // 表级 row-key="id"（模板上已传），EP 按行 id 记忆勾选态。
      ...(canUpgrade.value
        ? [
            {
              type: 'selection' as const,
              width: 46,
              selectable: () => true,
              reserveSelection: true
            }
          ]
        : []),
      // 序号用跨页连续的 globalIndex（不随翻页重置）；手机上让位给数据列。
      { type: 'globalIndex', width: 60, label: '序号', hideBelow: 'tablet' },
      // 主机名 = 设备标识，任何宽度都不隐藏；固定左（与 selection/index 构成
      // 稳定左前缀），横滚时身份列钉在原地。fixed 与 hideBelow 在本列**不同用**
      //（该列无 hideBelow），两者在 ArtTable 的渲染层互不干涉（hideBelow 只把列
      // 从渲染集里过滤掉、fixed 交给 EP 布局）—— 结论见本文件底部注释。
      {
        prop: 'hostname',
        label: '主机名',
        minWidth: 180,
        fixed: 'left',
        showOverflowTooltip: true,
        // 服务端排序（后端白名单列 hostname）；'custom' = 排序不发本地，
        // 只抛 sort-change 事件由页面换页重查。
        sortable: 'custom',
        formatter: (row: Api.Device.DeviceListItem) =>
          h(
            'a',
            { class: 'dev-host-link', onClick: () => openDetail(row) },
            row.hostname || EMPTY_TEXT
          )
      },
      // 系统类型影响升级包与兼容判断，平板起显示。
      {
        prop: 'os',
        label: '操作系统',
        minWidth: 140,
        hideBelow: 'tablet',
        showOverflowTooltip: true
      },
      // 架构只在排查兼容性问题时才需要，优先级最低，桌面起显示。
      { prop: 'arch', label: '架构', minWidth: 100, hideBelow: 'desktop' },
      // 来源 IP（服务端观测，非设备自述）：定位/SSH 前在列表里就能看到是哪台。
      // 缺值「—」（未观测到时字段整个不出现，见 DeviceListItem.PrimaryIP 注释）；
      // 点击复制（复用详情页 copyText 交互），空值不渲染按钮。
      {
        prop: 'primaryIp',
        label: 'IP',
        minWidth: 130,
        hideBelow: 'desktop',
        formatter: (row: Api.Device.DeviceListItem) =>
          row.primaryIp
            ? h(
                'span',
                {
                  class: 'dev-ip',
                  title: '点击复制',
                  role: 'button',
                  tabindex: 0,
                  onClick: () => copyText(row.primaryIp || ''),
                  onKeydown: (e: KeyboardEvent) => e.key === 'Enter' && copyText(row.primaryIp || '')
                },
                row.primaryIp
              )
            : h('span', null, EMPTY_TEXT)
      },
      // 主机分组（运维自填的自由文本，可按它把设备按角色/机房归类）：平板起显示，
      // 单行溢出省略（与列表页其它文本列同形制）。缺值「—」（未分组 devices 后端
      // 该字段缺省），不把「未分组」渲染成字面值混淆真实分组名。
      {
        prop: 'group',
        label: '分组',
        minWidth: 130,
        hideBelow: 'tablet',
        showOverflowTooltip: true,
        formatter: (row: Api.Device.DeviceListItem) => row.group || EMPTY_TEXT
      },
      {
        prop: 'agentVersion',
        label: 'Agent 版本',
        minWidth: 190,
        // 版本决定「该不该升级 / 能不能回滚」，平板起显示。
        hideBelow: 'tablet',
        // 服务端排序（后端白名单列 agent_version）。
        sortable: 'custom',
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
        minWidth: 100,
        // 直接消费后端算好的 online（后端按 sys.agent.offlineThreshold
        // 折算），前端不得拿 lastSeenAt 自行算阈值 —— 阈值可热更。
        // 不声明 hideBelow：任何宽度都要能一眼看到「活没活」。
        formatter: (row) =>
          h(ElTag, { size: 'small', type: row.online ? 'success' : 'info', effect: 'light' }, () =>
            row.online ? '在线' : '离线'
          )
      },
      {
        prop: 'status',
        label: '启停状态',
        minWidth: 100,
        // 被停用的设备同样不会上报，排查时必须与在线状态一起看；平板起显示。
        hideBelow: 'tablet',
        formatter: (row) => statusDict.render(row.status)
      },
      {
        prop: 'lastSeenAt',
        label: '最后上报',
        minWidth: 170,
        // 「失联多久」是离线的第一追问，平板起显示；更细的采样时刻见水位列。
        hideBelow: 'tablet',
        // 服务端排序（后端默认列 last_seen_at，点表头即显式指定同一口径）。
        sortable: 'custom',
        // 相对时间在前（5 分钟前），绝对时刻放 title（悬浮可见，读屏可读）。
        formatter: (row: Api.Device.DeviceListItem) =>
          h(
            'span',
            {
              class: 'dev-rel-time',
              title: row.lastSeenAt ? formatUnixSeconds(row.lastSeenAt) : ''
            },
            formatRelative(row.lastSeenAt)
          )
      },
      // 水位三列来自列表接口拼的 Redis latest：缺值时**整个字段不出现**
      // （omitempty），一律显示「—」，不得把缺值当成 0。
      // 渲染成「条 + 数值」：多列并排时**条**负责一眼比高低，**数值**负责精确值，
      // 缺值则由组件渲染「—」且不画空条（空条会被误读成 0%）。见 watermark-bar.vue。
      // 三列合计 360px：窄屏（含平板竖屏）完整看水位去总览页的快照表，桌面起在列表内联显示。
      {
        prop: 'cpuUsedPercent',
        label: 'CPU 使用率',
        minWidth: 120,
        hideBelow: 'desktop',
        formatter: (row) => h(WatermarkBar, { value: row.cpuUsedPercent })
      },
      {
        prop: 'memUsedPercent',
        label: '内存使用率',
        minWidth: 120,
        hideBelow: 'desktop',
        formatter: (row) => h(WatermarkBar, { value: row.memUsedPercent })
      },
      {
        prop: 'diskUsedPercent',
        label: '磁盘使用率',
        minWidth: 120,
        hideBelow: 'desktop',
        formatter: (row) => h(WatermarkBar, { value: row.diskUsedPercent })
      },
      {
        prop: 'watermarkAt',
        label: '水位采样时间',
        minWidth: 170,
        // 最细粒度的时间元数据（与水位列配套核对数据新鲜度），桌面起显示。
        hideBelow: 'desktop',
        formatter: (row) => formatUnixSeconds(row.watermarkAt)
      },
      // 操作列固定右侧、宽随按钮数计算（结构性列），任何宽度都不隐藏：
      // 窄屏放不下的次要动作本来就已收进「更多」下拉。
      ...(operationColumnConfig ? [operationColumnConfig] : [])
    ]
  })

  /** 兜底：窄屏时操作列放不下，走「更多」下拉执行同样的动作。 */
  async function onMore(item: { key: string | number }, row: Api.Device.DeviceListItem) {
    const key = String(item.key)
    if (key === 'detail') openDetail(row)
    else if (key === 'upgrade' || key === 'rollback') onUpgradeAction(key, row)
    else if (key === 'setGroup') openGroupDialog(row)
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

  // ── 「重试失败项」深链（升级任务页 retryFailed push 来的 query）──────
  // retryIds 是逗号分隔的设备 id、retryVersion 是那次任务的目标版本。
  // 打开升级对话框（预选 retryVersion）；query 随即 replace 清掉 —— 防刷新
  // /复进时二次触发。挂载时机只认 onMounted：KeepAlive 的返回不重挂，
  // 该深链本来也只在「从任务页点过来」时出现一次。
  // ── 「重试失败项」深链（升级任务页 retryFailed push 来的 query）──────
  // retryIds 是逗号分隔的设备 id、retryVersion 是那次任务的目标版本。
  // 等**首拉落定后**再打开升级对话框（openUpgradeDialog 用 data.value 过滤
  // 目标行，首拉未回来时它是空的 —— 对话框会开成「0 台」）。query 随即
  // replace 清掉 —— 防刷新/复进时二次触发。挂载时机只认 onMounted：
  // KeepAlive 的返回不重挂，该深链本来也只在「从任务页点过来」时出现一次。
  onMounted(async () => {
    const retryIds = typeof route.query.retryIds === 'string' ? route.query.retryIds : ''
    const retryVersion =
      typeof route.query.retryVersion === 'string' ? route.query.retryVersion : ''
    if (retryIds) {
      const ids = retryIds
        .split(',')
        .map((s) => s.trim())
        .filter(Boolean)
      if (ids.length > 0) {
        // 首拉在 setup 尾部已 void 出发，这里等同一份落定（loadList 幂等，
        // onActivated 的补拉不会与本流程竞争 —— 它们都是「拉当前页」）。
        await firstLoad
        void openUpgradeDialog(ids, 'upgrade', retryVersion)
      }
    }
    void router.replace({ path: '/device/index', query: {} })
  })

  // ── KeepAlive 回显后的数据同步 ──────────────────────────────────────
  // 路由 meta.keepAlive=true（见 modules/device/index.ts）：翻页/筛选/列配置/
  // 勾选都由缓存实例保持；但列表数据会陈旧（在详情页停留期间设备可能变化）。
  // onActivated 在**首次挂载也会触发**，与 setup 尾部的 loadList 重复拉一次的
  // 代价可接受（一次分页查询），换来「返回列表永远不是陈旧快照」。
  onActivated(() => {
    loadList()
  })

  const firstLoad = loadList()
</script>

<style lang="scss" scoped>
  /* 批量升级操作条（表头左侧插槽）：与表头图标按钮同一行，故只做对齐与分隔 */
  /* ── 批量操作浮条（照抄 system-file/views/index.vue 的 batch-bar 家族）── */
  .batch-bar {
    position: fixed;
    bottom: 24px;
    left: 50%;
    z-index: 1200;
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 10px 8px 14px;
    border: 1px solid var(--default-border);
    border-radius: 999px;
    background: var(--default-box-color);
    box-shadow: 0 10px 32px rgba(15, 23, 42, 0.16);
    transform: translateX(-50%);
  }

  .batch-icon {
    display: flex;
    align-items: center;
    font-size: 17px;
    color: var(--theme-color);
  }

  .batch-text {
    font-size: 13px;
    color: var(--art-gray-700);
  }

  .batch-divider {
    width: 1px;
    height: 16px;
    background: var(--default-border);
  }

  .batch-bar-enter-active,
  .batch-bar-leave-active {
    transition:
      opacity 0.2s ease,
      transform 0.2s ease;
  }

  .batch-bar-enter-from,
  .batch-bar-leave-to {
    opacity: 0;
    transform: translate(-50%, 12px);
  }

  /* 首拉失败的内联错误态（ElEmpty）：给接近表格空态的留白 */
  .dev-empty {
    padding: 56px 0;
  }

  /* 主机名链接（点击进详情；showOverflowTooltip 仍在列级生效） */
  .dev-host-link {
    font-size: 13px;
    font-weight: 500;
    color: var(--aa-primary-text);
    text-decoration: none;
    cursor: pointer;

    &:hover {
      color: var(--aa-primary-text-hover);
      text-decoration: underline;
      text-underline-offset: 3px;
    }
  }

  /* 相对时间（title 里是绝对时刻）；时间戳原子值不折行 */
  .dev-rel-time {
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
  }

  /* IP 列：点击复制（等宽数字对齐；空值「—」无按钮语义） */
  .dev-ip {
    font-variant-numeric: tabular-nums;
    cursor: pointer;

    &:hover {
      color: var(--aa-primary-text-hover);
    }
  }

  /* 版本列：当前版本 + 目标 + 标记（单行，窄列不挤压操作区） */
  .up-ver {
    display: flex;
    gap: 6px;
    align-items: center;

    &__now {
      font-variant-numeric: tabular-nums;
      white-space: nowrap;
    }

    &__arrow {
      font-size: 12px;
      // 文字对比度 AA（收尾批）：「→ 目标版本」是文字（非图标），原
      // el-color-warning（白底 1.85）改走 token，数字见 @styles/core/aa-text.scss。
      color: var(--aa-warning-text);
      font-variant-numeric: tabular-nums;
      /* 「→ 0.3.0」含空格，版本原子值不折行 */
      white-space: nowrap;
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

  /* 批量回滚：设备 → 各自上一版本的清单（右侧结论列给了去留语义） */
  .rb-dialog {
    display: flex;
    flex-direction: column;
    gap: 10px;

    &__loading {
      padding: 32px 0;
      text-align: center;
      font-size: 12px;
      color: var(--el-text-color-secondary);
    }

    &__ver {
      font-variant-numeric: tabular-nums;
    }

    &__skip {
      font-size: 12px;
      color: var(--el-text-color-secondary);
    }

    &__count {
      font-size: 13px;
      color: var(--el-text-color-primary);
      font-variant-numeric: tabular-nums;
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

    /* 生效条件枚举（数据值）：一行一条，不下拉折叠（最多 3 项） */
    &__conds {
      display: flex;
      flex-direction: column;
      gap: 2px;
      padding: 6px 10px;
      border-radius: 6px;
      background: var(--el-fill-color-lighter);
      font-size: 12px;
      color: var(--el-text-color-regular);
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

<!--
  ── fixed 与 hideBelow 的共存结论（本页的取用证据）─────────────────────
  ArtTable 的渲染层先按 hideBelow 过滤列集（responsive-columns.
  filterColumnsForViewport —— 只把列从渲染集里**去掉**，不碰 fixed 字段），
  再把保留列的 fixed 原样交给 ElTableColumn；EP 按保留列里的声明序重排
  fixed 前缀（responsive-columns.warnNonPrefixFixedLeft 钉住「左固定列
  必须是渲染列表前缀」的约束）。两者作用在不同层、互不干涉：一列可以
  同时声明 fixed:'left' 与 hideBelow —— 视口宽时固定显示、窄时整列消失，
  固定组自动收缩为剩下的前缀。
  本页的左前缀是 selection → globalIndex → hostname（hostname 固定且
  无 hideBelow）；手机档序号被过滤后前缀仍是 selection → hostname，
  依然连续 —— 不会触发列序告警。故「身份列任何宽度可见」与「宽屏横滚时
  身份列钉在原地」两个目标同时成立，无需取舍。
-->
