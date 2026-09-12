import request from '@/utils/http'
import type { PageResponse } from '@/types/common/response'

const PREFIX = import.meta.env.VITE_API_PREFIX

export function fetchConfigs(params: Api.Config.Query) {
  return request.get<PageResponse<Api.Config.Config>>({ url: `${PREFIX}/configs`, params })
}
export function createConfig(data: Api.Config.Form) {
  return request.post<{ id: string }>({ url: `${PREFIX}/configs`, data })
}
export function updateConfig(id: string, data: Api.Config.Form) {
  return request.put<void>({ url: `${PREFIX}/configs/${id}`, data })
}
export function removeConfig(id: string) {
  return request.del<void>({ url: `${PREFIX}/configs/${id}` })
}
