import request from '@/utils/http'
import type { PageResponse } from '@/types/common/response'

const PREFIX = import.meta.env.VITE_API_PREFIX

export function fetchUsers(params: Api.System.UserQuery) {
  return request.get<PageResponse<Api.System.User>>({ url: `${PREFIX}/users`, params })
}
export function fetchUser(id: string) {
  return request.get<Api.System.User>({ url: `${PREFIX}/users/${id}` })
}
export function createUser(data: Api.System.UserForm) {
  return request.post<{ id: string }>({ url: `${PREFIX}/users`, data })
}
export function updateUser(id: string, data: Api.System.UserForm) {
  return request.put<void>({ url: `${PREFIX}/users/${id}`, data })
}
export function assignUserRoles(id: string, roleIds: string[]) {
  return request.put<void>({ url: `${PREFIX}/users/${id}/roles`, data: { roleIds } })
}
export function removeUser(id: string) {
  return request.del<void>({ url: `${PREFIX}/users/${id}` })
}
export function enableUser(id: string) {
  return request.post<void>({ url: `${PREFIX}/users/${id}/enable` })
}
export function disableUser(id: string) {
  return request.post<void>({ url: `${PREFIX}/users/${id}/disable` })
}
export function resetUserPassword(id: string, newPassword: string) {
  return request.post<void>({ url: `${PREFIX}/users/${id}/password/reset`, data: { newPassword } })
}
export function unlockUser(id: string) {
  return request.post<void>({ url: `${PREFIX}/users/${id}/unlock` })
}

// 辅助数据：部门树 / 全部角色（用户表单用）
export function fetchDepts() {
  return request.get<Api.System.Dept[]>({ url: `${PREFIX}/depts` })
}
export function fetchAllRoles() {
  return request.get<Api.System.Role[]>({ url: `${PREFIX}/roles/all` })
}
