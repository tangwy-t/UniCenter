/** system-monitor · pprof 域 API/类型。契约随后端 OpenAPI(snake_case),与后端 response/dto 对齐 */
import request from '@/utils/http'

const PREFIX = import.meta.env.VITE_API_PREFIX

// ════════════════════════ pprof 性能分析 ════════════════════════

/** pprof profile 概览条目 → Api.Monitor.PprofProfileEntry(api.generated.d.ts 为唯一事实源) */
export type PprofProfileEntry = Api.Monitor.PprofProfileEntry

/** pprof 运行状态概览 → Api.Monitor.PprofStatusResponse */
export type PprofStatus = Api.Monitor.PprofStatusResponse

/** 火焰图树节点 → Api.Monitor.FlameNode */
export type PprofFlameNode = Api.Monitor.FlameNode

/** 热点函数一行 → Api.Monitor.PprofTopFunc */
export type PprofTopFunc = Api.Monitor.PprofTopFunc

/** 单个 profile 的火焰树 + 热点函数 → Api.Monitor.PprofProfileResponse */
export type PprofFlameData = Api.Monitor.PprofProfileResponse

/** 查询 pprof 运行状态 */
export function fetchPprofStatus() {
  return request.get<PprofStatus>({ url: `${PREFIX}/monitor/pprof/status` })
}

/** 启用 pprof 采集 */
export function enablePprof() {
  return request.post<{ enabled: boolean }>({ url: `${PREFIX}/monitor/pprof/enable` })
}

/** 停用 pprof 采集 */
export function disablePprof() {
  return request.post<{ enabled: boolean }>({ url: `${PREFIX}/monitor/pprof/disable` })
}

/** 解析指定 profile 的火焰树与热点函数 */
export function fetchPprofFlame(name: string, top = 15) {
  return request.get<PprofFlameData>({
    url: `${PREFIX}/monitor/pprof/profile/${name}`,
    params: { top }
  })
}

/** 下载 pprof 原始数据（携带鉴权头）。
 *  name 为 snapshot 名(如 heap)时自动附加 debug=0(二进制 proto)；
 *  为 profile/trace 时传递 seconds 等采集参数即可。 */
export function downloadPprofRaw(name: string, extraParams: Record<string, string | number> = {}) {
  const params: Record<string, string | number> =
    name === 'profile' || name === 'trace' ? { ...extraParams } : { debug: 0, ...extraParams }
  // CPU profile/trace 由服务端阻塞采集 seconds 秒,超时需覆盖默认 15s
  return request.download({ url: `${PREFIX}/monitor/debug/pprof/${name}`, params, timeout: 180000 })
}
