import request from '@/utils/http'
import type { PageResponse } from '@/types/common/response'

const PREFIX = import.meta.env.VITE_API_PREFIX

/** 分页查询操作日志 */
export function fetchOperationLogs(params: Api.Log.OperationLogQuery) {
  return request.get<PageResponse<Api.Log.OperationLog>>({
    url: `${PREFIX}/logs/operation`,
    params
  })
}

/** 清空某日期之前的操作日志 */
export function clearOperationLogs(before: string) {
  return request.del<void>({ url: `${PREFIX}/logs/operation`, params: { before } })
}

/** 分页查询登录日志 */
export function fetchLoginLogs(params: Api.Log.LoginLogQuery) {
  return request.get<PageResponse<Api.Log.LoginLog>>({
    url: `${PREFIX}/logs/login`,
    params
  })
}
