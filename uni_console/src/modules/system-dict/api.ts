import request from '@/utils/http'
import type { PageResponse } from '@/types/common/response'
import type { DictTypeQuery } from './types'

const PREFIX = import.meta.env.VITE_API_PREFIX

export function fetchDictTypes(params: DictTypeQuery) {
  return request.get<PageResponse<Api.Dict.DictType>>({ url: `${PREFIX}/dict/types`, params })
}
export function fetchDictType(id: string) {
  return request.get<Api.Dict.DictType>({ url: `${PREFIX}/dict/types/${id}` })
}
export function createDictType(data: Api.Dict.DictTypeForm) {
  return request.post<{ id: string }>({ url: `${PREFIX}/dict/types`, data })
}
export function updateDictType(id: string, data: Api.Dict.DictTypeForm) {
  return request.put<void>({ url: `${PREFIX}/dict/types/${id}`, data })
}
export function removeDictType(id: string) {
  return request.del<void>({ url: `${PREFIX}/dict/types/${id}` })
}
export function fetchDictData(typeId: string) {
  return request.get<Api.Dict.DictData[]>({ url: `${PREFIX}/dict/types/${typeId}/data` })
}
export function createDictData(typeId: string, data: Api.Dict.DictDataForm) {
  return request.post<{ id: string }>({ url: `${PREFIX}/dict/types/${typeId}/data`, data })
}
export function updateDictData(typeId: string, dataId: string, data: Api.Dict.DictDataForm) {
  return request.put<void>({ url: `${PREFIX}/dict/types/${typeId}/data/${dataId}`, data })
}
export function removeDictData(typeId: string, dataId: string) {
  return request.del<void>({ url: `${PREFIX}/dict/types/${typeId}/data/${dataId}` })
}
