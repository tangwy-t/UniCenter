import request from '@/utils/http'
import type { PageResponse } from '@/types/common/response'

const PREFIX = import.meta.env.VITE_API_PREFIX

export function fetchRoles(params: Api.System.RoleQuery) {
  return request.get<PageResponse<Api.System.Role>>({ url: `${PREFIX}/roles`, params })
}
export function fetchRole(id: string) {
  return request.get<Api.System.Role>({ url: `${PREFIX}/roles/${id}` })
}
export function createRole(data: Api.System.RoleForm) {
  return request.post<{ id: string }>({ url: `${PREFIX}/roles`, data })
}
export function updateRole(id: string, data: Api.System.RoleForm) {
  return request.put<void>({ url: `${PREFIX}/roles/${id}`, data })
}
export function removeRole(id: string) {
  return request.del<void>({ url: `${PREFIX}/roles/${id}` })
}
// 行内状态开关(0=停用 1=启用)
export function updateRoleStatus(id: string, status: number) {
  return request.put<void>({ url: `${PREFIX}/roles/${id}/status`, data: { status } })
}
// 批量保存排序(同菜单管理:直接覆盖 sort,不触发关联替换)
export function updateRoleSort(items: { id: string; sort: number }[]) {
  return request.put<void>({ url: `${PREFIX}/roles/sort`, data: { items } })
}
// 分配用户(角色维度)
export function fetchRoleUsers(roleId: string, params: Api.System.UserQuery) {
  return request.get<PageResponse<Api.System.User>>({
    url: `${PREFIX}/roles/${roleId}/users`,
    params
  })
}
export function addRoleUsers(roleId: string, userIds: string[]) {
  return request.post<void>({ url: `${PREFIX}/roles/${roleId}/users`, data: { userIds } })
}
export function removeRoleUsers(roleId: string, userIds: string[]) {
  return request.del<void>({ url: `${PREFIX}/roles/${roleId}/users`, data: { userIds } })
}
