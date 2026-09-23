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
