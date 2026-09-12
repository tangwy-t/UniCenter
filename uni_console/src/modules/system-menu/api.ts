import request from '@/utils/http'

const PREFIX = import.meta.env.VITE_API_PREFIX

/** 菜单树查询参数：名称模糊、状态精确、类型精确。 */
export interface MenuQuery {
  name?: string
  status?: number
  type?: 'dir' | 'menu' | 'btn'
}

export function fetchMenus(params: MenuQuery = {}) {
  return request.get<Api.System.Menu[]>({ url: `${PREFIX}/menus`, params })
}
export function createMenu(data: Api.System.MenuForm) {
  return request.post<{ id: string }>({ url: `${PREFIX}/menus`, data })
}
export function updateMenu(id: string, data: Api.System.MenuForm) {
  return request.put<void>({ url: `${PREFIX}/menus/${id}`, data })
}
export function updateMenuSort(items: { id: string; sort: number }[]) {
  return request.put<void>({ url: `${PREFIX}/menus/sort`, data: { items } })
}
export function removeMenu(id: string) {
  return request.del<void>({ url: `${PREFIX}/menus/${id}` })
}
