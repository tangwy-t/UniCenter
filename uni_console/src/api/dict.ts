import request from '@/utils/http'

const PREFIX = import.meta.env.VITE_API_PREFIX

/** 按 code 获取字典项（消费端） */
export function fetchDictByCode(code: string) {
  return request.get<Api.Dict.DictItem[]>({ url: `${PREFIX}/dict/codes/${code}` })
}
