/** system-monitor · cache 域 API/类型。契约随后端 OpenAPI(snake_case),与后端 response/dto 对齐 */
import request from '@/utils/http'

const PREFIX = import.meta.env.VITE_API_PREFIX

/** 分页后的缓存 value 响应 → Api.Monitor.CacheValuePage(api.generated.d.ts 为唯一事实源) */
export type CacheValuePage = Api.Monitor.CacheValuePage

/** zset 成员条目 */
export interface ZSetEntry {
  member: string
  score: number
}

/** hash 字段条目 */
export interface HashEntry {
  field: string
  value: string
}

/** 分页取值参数：list/zset 用 offset+limit；set/hash 用 cursor+limit；string 用 limit 字节窗 */
export interface FetchPageOptions {
  key: string
  offset?: number
  limit?: number
  /** 首页请求传 0，续扫回传响应字符串 */
  cursor?: string | number
}

export type FetchPage = (params: FetchPageOptions) => Promise<CacheValuePage>

/** 缓存 key 列表查询参数 */
export interface CacheKeysParams {
  prefix?: string
  cursor?: number
  count?: number
}

/** 单个缓存 key（含 Redis 值类型） → Api.Monitor.CacheKeyInfo */
export type CacheKeyInfo = Api.Monitor.CacheKeyInfo

/** 缓存 key 列表响应 → Api.Monitor.ListKeysResponse */
export type CacheKeysResult = Api.Monitor.ListKeysResponse

/** 批量删除缓存参数 */
export interface DeleteCacheKeysParams {
  prefix?: string
  maxCount?: number
}
/** 缓存 key 列表 */
export function fetchCacheKeys(params: CacheKeysParams) {
  return request.get<CacheKeysResult>({ url: `${PREFIX}/monitor/cache/keys`, params })
}

/** 分页查询单个缓存 key 的 value（list/zset: offset+limit 分页；set/hash: cursor+limit 游标；string: limit 字节窗） */
export function fetchCacheValuePage(params: FetchPageOptions) {
  return request.get<CacheValuePage>({
    url: `${PREFIX}/monitor/cache/keys/value`,
    params
  })
}

/** 按前缀批量删除缓存 key */
export function deleteCacheKeys(params: DeleteCacheKeysParams) {
  return request.del<{ deleted: number }>({ url: `${PREFIX}/monitor/cache/keys`, params })
}
