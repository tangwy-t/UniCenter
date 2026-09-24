/**
 * device · 设备域 API
 *
 * 字段与查询参数**逐一对齐后端契约**(uni_core):
 * - 列表 `GET /devices`：query `page`/`pageSize`/`hostname`/`status`/`online`
 *   （后端 `request.DeviceQuery` 内嵌 `app.PageRequest`，故分页参数名为
 *   `page`/`pageSize`，与前端 `PageResponse` 信封同字）。
 * - 详情 `GET /devices/:id`、启用 `POST /devices/:id/enable`、
 *   停用 `POST /devices/:id/disable`、删除 `DELETE /devices/:id`。
 * - 整机趋势与资源下钻**共用** `GET /devices/:id/metrics`：`kind`+`name`
 *   同时存在即下钻，否则是整机趋势（两种语义一个端点）。
 * - 资源枚举 `GET /devices/:id/resources`：query `kind`（空 = 全部）。
 *
 * 响应类型一律取 `Api.Device.*`（api.generated.d.ts 为唯一事实源）；
 * 该响应命名空间未手工声明别名桥，故直接引用生成类型。
 */
import type { PageResponse } from '@/types/common/response'
import request from '@/utils/http'

const PREFIX = import.meta.env.VITE_API_PREFIX

/** 分页查询参数（`PageResponse<DeviceListItem>` 的元素即列表行）。 */
export type DeviceListItem = Api.Device.DeviceListItem

/** 设备详情。 */
export type DeviceResp = Api.Device.DeviceResp

/** 设备列表查询参数：主机名模糊、启停态精确、在线状态布尔。 */
export interface DeviceQuery {
  page?: number
  pageSize?: number
  /** 主机名模糊匹配（后端 LIKE %v%）。 */
  hostname?: string
  /** 启停态精确匹配：0=停用 1=启用（`entity.DeviceStatus*`）。 */
  status?: number
  /** 在线过滤：后端按 `sys.agent.offlineThreshold` 折算，前端不自行判定。 */
  online?: boolean
}

/**
 * 整机趋势 / 资源下钻的查询参数。
 *
 * `range` 是**窗口秒数**（不是档位名）；`metrics` 是以逗号分隔的列白名单，
 * 字符 `*`（后端 `request.MetricsAll`）表示该档全部可用列。
 */
export interface DeviceMetricsQuery {
  /** 时间窗口（秒）。 */
  range?: number
  /** 逗号分隔的指标列；`*` = 全量列。 */
  metrics?: string
  /** 资源种类：disk/disk_io/nic/sensor，与 `name` 成对出现。 */
  kind?: string
  /** 资源名（挂载点/设备名/网卡名/传感器名），与 `kind` 成对出现。 */
  name?: string
}

/** 设备分页列表。 */
export function fetchDevices(params: DeviceQuery = {}) {
  return request.get<PageResponse<DeviceListItem>>({ url: `${PREFIX}/devices`, params })
}

/** 设备详情。 */
export function fetchDevice(id: string) {
  return request.get<DeviceResp>({ url: `${PREFIX}/devices/${id}` })
}

/** 启用设备（`device:enable`）。 */
export function enableDevice(id: string) {
  return request.post<void>({ url: `${PREFIX}/devices/${id}/enable` })
}

/** 停用设备（`device:disable`）。 */
export function disableDevice(id: string) {
  return request.post<void>({ url: `${PREFIX}/devices/${id}/disable` })
}

/** 删除设备（`device:delete`）。 */
export function removeDevice(id: string) {
  return request.del<void>({ url: `${PREFIX}/devices/${id}` })
}

/**
 * 整机趋势：不带 `kind`/`name` 时后端返回 `DeviceMetricsResp`
 * （`buckets` 稀疏、`t` 为桶起始 unix 秒、`available_metrics` 为可用列集）。
 */
export function fetchDeviceMetrics(id: string, params: DeviceMetricsQuery = {}) {
  return request.get<Api.Device.DeviceMetricsResp>({
    url: `${PREFIX}/devices/${id}/metrics`,
    params
  })
}

/**
 * 资源下钻：`kind`+`name` 同时给出时同一个端点返回 `DeviceResourceResp`
 * （桶元素是 `DeviceResourcePoint`，值列是「列名 → 值」的开放形状）。
 */
export function fetchDeviceResourceMetrics(id: string, params: DeviceMetricsQuery) {
  return request.get<Api.Device.DeviceResourceResp>({
    url: `${PREFIX}/devices/${id}/metrics`,
    params
  })
}

/**
 * 资源枚举（下钻下拉的数据源）。
 *
 * `stale` 由后端给出：为 true 表示该资源**已消失**（如已卸载的挂载点），
 * 前端应可见地标注，而不是把它当成仍在采集的资源。
 */
export function fetchDeviceResources(id: string, kind?: string) {
  return request.get<Api.Device.DeviceResourcesResp>({
    url: `${PREFIX}/devices/${id}/resources`,
    params: kind ? { kind } : {}
  })
}

/**
 * 设备监控总览查询参数（`GET /devices/overview`）。
 *
 * 与 `DeviceMetricsQuery` 的 `range`/`metrics` **逐字同义**（同一个后端选档函数
 * 与同一套列白名单），额外多了页面级过滤与设备白名单。
 */
export interface DeviceOverviewQuery {
  /** 时间窗口秒数（1h ~ 180d）；后端按窗口选档（≤24h Redis / ≤30d 5min / >30d 1h）。 */
  range?: number
  /** 逗号分隔的指标列白名单；`*` = 该档全部可用列；空 = 该档默认列集。 */
  metrics?: string
  /** 设备 ID 白名单（逗号分隔）：只看选定的这几台做横向对比。 */
  ids?: string
  /** 主机名模糊匹配。 */
  hostname?: string
  /** 启停状态：0=停用 1=启用。 */
  status?: number
  /** 在线过滤（后端按 sys.agent.offlineThreshold 判定）。 */
  online?: boolean
}

/**
 * 设备监控总览：**一次请求**取回 N 台设备 × 多类指标。
 *
 * 这是总览页唯一的数据入口。之所以不复用 `GET /devices` + 逐台 `metrics`：
 * 列表项只承接 3 个水位字段，而逐台拉趋势在 100 台时是 100 次往返、
 * 约 17 MB —— 后端为此提供了批量聚合端点。
 *
 * 响应要点（前端必须按此消费）：
 * - `axis` 是**所有设备共享**的时间轴（升序 unix 秒），每条 series 的
 *   `values` 与它等长且一一对应；
 * - `values[i]` 为 `null` 表示**该桶无数据**（必须显示「—」/画空洞），
 *   与「采集到的 0」是两件事，前端**不得**用 `?? 0` 兜底；
 * - `watermark` 是「列名 → 值」的最新快照，缺列即「没采到」；
 * - 单台设备的趋势取数失败只体现在该设备的 `error` 上，其余设备照常返回。
 */
export function fetchDeviceOverview(params: DeviceOverviewQuery = {}) {
  return request.get<Api.Device.DeviceOverviewResp>({
    url: `${PREFIX}/devices/overview`,
    params
  })
}

// ── Agent 升级（命令 / 发布物 / 任务）──────────────────────────────────
//
// 契约要点（与后端 handler 一一对应）：
// - 所有下发入口都返回 `DeviceUpgradeDispatchResp`（含 task_id，页面据此跳任务页）；
//   按筛选下发必须带 `expectedCount`（先调 preview 拿到的命中台数），不一致后端 409；
// - `pin: true` 是「固定在当前版本」的**独立意图**（版本与当前相同时也写目标），
//   普通升级不要传它 —— 否则一次批量下发会把每台设备都钉死，不再跟随全站；
// - 发布物与任务都走设备域前缀 `/devices/...`（与 agent 下载端点 `/agent/...` 分开：
//   后者是设备侧凭 agent token 访问的，前端不碰）。

/** 升级/回滚的请求体（单台与批量共用形状）。 */
export interface DeviceUpgradeTargetPayload {
  version: string
  /** 仅单台「固定在当前版本」时传 true（见文件头说明）。 */
  pin?: boolean
}

/** 批量下发请求体：`ids` 与 `filter` 二选一（后端会拒绝同时给）。 */
export interface DeviceBatchUpgradePayload {
  version: string
  /** 多选：设备 ID 列表（雪花 ID 以字符串传输，避免 JS 精度丢失）。 */
  ids?: string[]
  /** 按筛选全量：与列表页同一套筛选字段。 */
  filter?: DeviceQuery
  /** 按筛选下发时的**影响面确认**（preview 返回的 matched）。 */
  expectedCount?: number
}

/** 单台下发（升级到指定版本；`pin` 表示固定在当前版本）。 */
export function upgradeDevice(id: string, payload: DeviceUpgradeTargetPayload) {
  return request.post<Api.Device.DeviceUpgradeDispatchResp>({
    url: `${PREFIX}/devices/${id}/upgrade`,
    data: payload
  })
}

/** 清空设备级目标（恢复跟随全站）。 */
export function clearDeviceUpgradeTarget(id: string) {
  return request.del<void>({ url: `${PREFIX}/devices/${id}/upgrade` })
}

/** 影响面预演（下发前给人看的数字：命中多少、跳过多少、分别为什么）。 */
export function previewDeviceUpgrade(payload: DeviceBatchUpgradePayload) {
  return request.post<Api.Device.DeviceUpgradePreviewResp>({
    url: `${PREFIX}/devices/upgrade/preview`,
    data: payload
  })
}

/** 批量下发（多选或按筛选）。 */
export function dispatchDeviceUpgrade(payload: DeviceBatchUpgradePayload) {
  return request.post<Api.Device.DeviceUpgradeDispatchResp>({
    url: `${PREFIX}/devices/upgrade`,
    data: payload
  })
}

/** 设置/清除全站目标版本（`version` 为空 = 关闭全站升级）。 */
export function setAgentGlobalTarget(version: string) {
  return request.post<Api.Device.DeviceUpgradeGlobalResp>({
    url: `${PREFIX}/devices/upgrade/global`,
    data: { version }
  })
}

/** 升级状态汇总（当前状态视角：按生效目标分桶 + 版本分布）。 */
export function fetchAgentUpgradeSummary() {
  return request.get<Api.Device.DeviceUpgradeSummaryResp>({
    url: `${PREFIX}/devices/upgrade/summary`
  })
}

/** 任务列表查询参数。 */
export interface UpgradeTaskQuery {
  page?: number
  pageSize?: number
  /** 目标版本精确匹配。 */
  targetVersion?: string
  /** 来源：manual/batch/filter/global。 */
  source?: string
  /** 只看未收口的任务。 */
  running?: boolean
}

/** 升级任务列表（每行带明细状态分布）。 */
export function fetchAgentUpgradeTasks(params: UpgradeTaskQuery = {}) {
  return request.get<PageResponse<Api.Device.AgentUpgradeTaskItem>>({
    url: `${PREFIX}/devices/upgrade/tasks`,
    params
  })
}

/**
 * 任务明细查询参数。
 *
 * `filter` 是受限枚举：`active` 只看进行中、`failed` 只看失败/回滚/超时；
 * **空串表示不过滤**（后端约定，与「不传」等价）——故这里允许空串，
 * 否则页面在「全部」这一档上只能不传字段，多一份分支。
 */
export interface UpgradeAttemptQuery {
  page?: number
  pageSize?: number
  filter?: '' | 'active' | 'failed'
}

/** 任务详情（任务 + 逐台明细）。 */
export function fetchAgentUpgradeTaskDetail(id: string, params: UpgradeAttemptQuery = {}) {
  return request.get<Api.Device.AgentUpgradeTaskDetailResp>({
    url: `${PREFIX}/devices/upgrade/tasks/${id}`,
    params
  })
}

/** 某设备的升级记录（详情页用；与任务明细同源）。 */
export function fetchDeviceUpgradeRecords(id: string) {
  return request.get<Api.Device.DeviceUpgradeRecord[]>({
    url: `${PREFIX}/devices/${id}/upgrade/records`
  })
}

/** 发布物列表（含「能否删除」的结论与已发布版本号）。 */
export function fetchAgentReleases() {
  return request.get<Api.Device.AgentReleaseListResp>({
    url: `${PREFIX}/devices/releases`
  })
}

/**
 * 上传 agent 程序包（草稿态，需再发布）。
 *
 * `timeout: 0` = 不限时（程序包 20MB 级，内网也要几十秒；默认超时会在中途掐断）；
 * `onUploadProgress` 驱动进度条，`signal` 支持取消 —— 与文件模块的 upload 同款。
 */
export function uploadAgentRelease(
  data: FormData,
  options: {
    onUploadProgress?: (percent: number) => void
    signal?: AbortSignal
  } = {}
) {
  return request.post<Api.Device.AgentReleaseItem>({
    url: `${PREFIX}/devices/releases`,
    data,
    timeout: 0,
    signal: options.signal,
    onUploadProgress: (e: { loaded: number; total?: number }) => {
      if (!options.onUploadProgress || !e.total) return
      options.onUploadProgress(Math.round((e.loaded / e.total) * 100))
    }
  })
}

/** 发布（草稿 → 已发布；此后可被选为目标版本）。 */
export function publishAgentRelease(id: string) {
  return request.post<void>({ url: `${PREFIX}/devices/releases/${id}/publish` })
}

/** 撤回发布（只影响新下发；已指向该版本的设备仍可下载）。 */
export function unpublishAgentRelease(id: string) {
  return request.post<void>({ url: `${PREFIX}/devices/releases/${id}/unpublish` })
}

/** 删除发布物（被升级记录用过的版本会被后端拒绝：回滚余量保护）。 */
export function removeAgentRelease(id: string) {
  return request.del<void>({ url: `${PREFIX}/devices/releases/${id}` })
}
