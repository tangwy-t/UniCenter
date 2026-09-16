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
