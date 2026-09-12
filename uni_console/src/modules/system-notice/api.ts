import request from '@/utils/http'
import type { PageResponse } from '@/types/common/response'

const PREFIX = import.meta.env.VITE_API_PREFIX

export function fetchNotices(params: Api.Notice.Query) {
  return request.get<PageResponse<Api.Notice.Notice>>({ url: `${PREFIX}/notices`, params })
}
export function createNotice(data: Api.Notice.Form) {
  return request.post<{ id: string }>({ url: `${PREFIX}/notices`, data })
}
export function updateNotice(id: string, data: Api.Notice.Form) {
  return request.put<void>({ url: `${PREFIX}/notices/${id}`, data })
}
export function removeNotice(id: string) {
  return request.del<void>({ url: `${PREFIX}/notices/${id}` })
}
export function publishNotice(id: string) {
  return request.post<void>({ url: `${PREFIX}/notices/${id}/publish`, data: {} })
}
export function revokeNotice(id: string) {
  return request.post<void>({ url: `${PREFIX}/notices/${id}/revoke`, data: {} })
}
export function fetchNoticeReadUsers(id: string, params: Api.Notice.ReadUsersQuery) {
  return request.get<PageResponse<Api.Notice.ReadUser>>({
    url: `${PREFIX}/notices/${id}/read-users`,
    params
  })
}
export function fetchNoticeTargetUsers(ids: string) {
  return request.get<Api.Notice.TargetUser[]>({
    url: `${PREFIX}/notices/target-users`,
    params: { ids }
  })
}
/** 我的公告收件箱(白名单,登录即可) */
export function fetchMyNotices() {
  return request.get<Api.Notice.MyList>({ url: `${PREFIX}/notices/my` })
}
/** 单个标记已读(白名单) */
export function markNoticeRead(id: string) {
  return request.post<void>({ url: `${PREFIX}/notices/${id}/read` })
}
/** 全部标为已读(白名单,幂等) */
export function markAllNoticesRead() {
  return request.post<void>({ url: `${PREFIX}/notices/read-all` })
}
