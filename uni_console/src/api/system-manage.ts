import request from '@/utils/http'

// 获取后端菜单树（backend 权限模式下使用）
export function fetchGetMenuList() {
  return request.get<Api.System.Menu[]>({ url: `${import.meta.env.VITE_API_PREFIX}/menus` })
}
