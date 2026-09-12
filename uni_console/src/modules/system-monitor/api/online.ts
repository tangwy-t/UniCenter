/** system-monitor · online 域 API/类型 */
import type { PageResponse } from '@/types/common/response'
import request from '@/utils/http'

const PREFIX = import.meta.env.VITE_API_PREFIX

/** 在线会话行 → Api.Monitor.OnlineSession（api.generated.d.ts 为唯一事实源） */
export type OnlineSession = Api.Monitor.OnlineSession

/** 在线用户分页查询参数 */
export interface OnlineUserParams {
  page?: number
  pageSize?: number
  keyword?: string
}

/** 在线用户分页列表 */
export function fetchOnlineUsers(params: OnlineUserParams) {
  return request.get<PageResponse<OnlineSession>>({ url: `${PREFIX}/monitor/online`, params })
}

/** 强制下线一个用户（下线该用户全部会话，uid 为列表行 userId） */
export function kickOnlineSession(payload: { uid: string }) {
  return request.post<null>({ url: `${PREFIX}/monitor/online/kick`, data: payload })
}