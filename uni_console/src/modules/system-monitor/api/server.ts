/** system-monitor · server 域 API/类型。契约随后端 OpenAPI(snake_case),与后端 response/dto 对齐 */
import request from '@/utils/http'

const PREFIX = import.meta.env.VITE_API_PREFIX

/** 服务器监控统计 */
export function fetchServerStats() {
  return request.get<Api.Monitor.ServerStats>({ url: `${PREFIX}/monitor/server/stats` })
}

/** 服务器监控历史查询参数 */
export interface ServerHistoryParams {
  /** 时间窗口,如 15m、1h、6h、24h(后端合法范围 1m~24h) */
  window: string
  /** 分桶粒度,如 3s、12s、60s、240s */
  step: string
}

/** 服务器监控历史采样桶 → Api.Monitor.ServerHistoryPoint(api.generated.d.ts 为唯一事实源) */
export type ServerHistoryPoint = Api.Monitor.ServerHistoryPoint

/** 服务器监控历史查询结果 → Api.Monitor.ServerHistorySnapshot */
export type ServerHistory = Api.Monitor.ServerHistorySnapshot

/** 服务器监控历史时序 */
export function fetchServerHistory(params: ServerHistoryParams) {
  return request.get<ServerHistory>({ url: `${PREFIX}/monitor/server/history`, params })
}
