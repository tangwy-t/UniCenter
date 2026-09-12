import request from '@/utils/http'

const PREFIX = import.meta.env.VITE_API_PREFIX

/** 部门树查询参数：名称模糊、状态精确。 */
export interface DeptQuery {
  name?: string
  status?: number
}

export function fetchDepts(params: DeptQuery = {}) {
  return request.get<Api.System.Dept[]>({ url: `${PREFIX}/depts`, params })
}
export function createDept(data: Api.System.DeptForm) {
  return request.post<{ id: string }>({ url: `${PREFIX}/depts`, data })
}
export function updateDept(id: string, data: Api.System.DeptForm) {
  return request.put<void>({ url: `${PREFIX}/depts/${id}`, data })
}
export function updateDeptSort(items: { id: string; sort: number }[]) {
  return request.put<void>({ url: `${PREFIX}/depts/sort`, data: { items } })
}
export function removeDept(id: string) {
  return request.del<void>({ url: `${PREFIX}/depts/${id}` })
}
