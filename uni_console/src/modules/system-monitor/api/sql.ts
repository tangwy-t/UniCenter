/** system-monitor · sql 域 API/类型。契约随后端 OpenAPI(snake_case),与后端 response/dto 对齐 */
import request from '@/utils/http'

const PREFIX = import.meta.env.VITE_API_PREFIX

/**
 * SQL 监控统计类型。
 *
 * 全部为 api.generated.d.ts 的别名(由 uni_core/tools/apigen 从
 * internal/model/dto/response/sql_stats.go 生成),不再手写:
 * 此前这里是人工维护的副本,字段名(snake_case)只能靠约定对齐 ——
 * 后端改字段前端不会有任何编译期提示。现在后端 DTO 变更后执行
 * `pnpm gen:api` 即会让这里产生类型错误,由 CI 的 `pnpm check:api-types` 拦住。
 */
export type SqlGlobalStats = Api.Monitor.SQLGlobalStats
export type SqlDimStats = Api.Monitor.SQLDimStats
export type SqlQueryEntry = Api.Monitor.SQLQueryEntry
export type SqlStats = Api.Monitor.SQLStatsSnapshot

/** 时序快照中的单个时间桶 → Api.Monitor.SQLHistoryPoint(api.generated.d.ts 为唯一事实源) */
export type SqlHistoryPoint = Api.Monitor.SQLHistoryPoint

/** SQL 监控时序快照 → Api.Monitor.SQLHistorySnapshot */
export type SqlHistory = Api.Monitor.SQLHistorySnapshot

/** SQL 监控统计查询参数 */
export interface SqlStatsParams {
  /** 时间窗口（如 5m,1h），留空返回累计统计 */
  window?: string
}

/** SQL 监控时序查询参数 */
export interface SqlHistoryParams {
  window: string
  step: string
}

/** SQL 监控累计统计 */
export function fetchSQLStats(params: SqlStatsParams = {}) {
  return request.get<SqlStats>({ url: `${PREFIX}/monitor/sql/stats`, params })
}

/** SQL 监控时序数据（QPS/耗时趋势） */
export function fetchSQLHistory(params: SqlHistoryParams) {
  return request.get<SqlHistory>({ url: `${PREFIX}/monitor/sql/history`, params })
}
