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
      />

      <ArtTable
        :loading="loading"
        :data="data"
        :columns="columns"
        :pagination="pagination"
        @pagination:size-change="handleSizeChange"
        @pagination:current-change="handleCurrentChange"
      />
    </ElCard>
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
    PermDeviceQuery
  } from '@/enums/permission'
  import { useAuth } from '@/hooks/core/useAuth'
  import { useDict } from '@/hooks/core/useDict'
  import { useTableColumns } from '@/hooks/core/useTableColumns'
  import { operationColumn } from '@/components/core/tables/operation-column'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtButtonMore from '@/components/core/forms/art-button-more/index.vue'
  import WatermarkBar from '../components/watermark-bar.vue'
  import { disableDevice, enableDevice, fetchDevices, removeDevice } from '../api'

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

  const loading = ref(false)
  const data = ref<Api.Device.DeviceListItem[]>([])
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
      { type: 'index', width: 60, label: '序号' },
      { prop: 'hostname', label: '主机名', minWidth: 180, showOverflowTooltip: true },
      { prop: 'os', label: '操作系统', width: 140, showOverflowTooltip: true },
      { prop: 'arch', label: '架构', width: 100 },
      { prop: 'agentVersion', label: 'Agent 版本', width: 120, showOverflowTooltip: true },
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
