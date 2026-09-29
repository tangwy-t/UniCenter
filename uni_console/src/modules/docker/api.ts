/**
 * docker · 模块 API
 *
 * 字段一律取 `Api.Docker.*`（apigen 生成物是唯一事实源）；四个端点的形状见
 * uni_core 的 response/docker.go 与 spec §4.1。
 */
import request from '@/utils/http'
import { useUserStore } from '@/store/modules/user'

const PREFIX = import.meta.env.VITE_API_PREFIX

export type DockerHostItem = Api.Docker.DockerHostItem
export type DockerHostListResp = Api.Docker.DockerHostListResp
export type DockerStateResp = Api.Docker.DockerStateResp
export type DockerContainerItem = Api.Docker.DockerContainerItem
export type DockerPortItem = Api.Docker.DockerPortItem
export type DockerImageItem = Api.Docker.DockerImageItem
export type DockerVolumeItem = Api.Docker.DockerVolumeItem
export type DockerNetworkItem = Api.Docker.DockerNetworkItem
export type DockerProjectItem = Api.Docker.DockerProjectItem
export type DockerCmdResultResp = Api.Docker.DockerCmdResultResp

/** 指令受理请求体（形状对齐 uni_core 的 request.DockerCmdReq）。 */
export interface DockerCmdBody {
  action: string
  target?: string
  options?: Record<string, unknown>
  confirm?: string
}

/** 可管主机清单（主机切换器的数据源）。 */
export function fetchDockerHosts() {
  return request.get<DockerHostListResp>({ url: `${PREFIX}/docker/hosts` })
}

/** 一台主机的资源快照（含服务端算好的陈旧结论）。 */
export function fetchDockerState(hostId: string) {
  return request.get<DockerStateResp>({ url: `${PREFIX}/docker/hosts/${hostId}/state` })
}

/**
 * 受理一条指令（202 + ref）。一期只用到四个只读动作。
 *
 * 不走 `showErrorMessage` 的默认封装：指令失败的原因由**轮询结果**给出（agent 的结论句），
 * 受理期的错误（403/409/503）则应在调用处按类型给出不同措辞 —— 统一弹一个 toast
 * 会把「设备离线」和「参数不合法」说成同一句话。
 */
export function sendDockerCmd(hostId: string, body: DockerCmdBody) {
  return request.post<{ ref: string }>({
    url: `${PREFIX}/docker/hosts/${hostId}/cmds`,
    data: body,
    showErrorMessage: false
  })
}

/** 轮询指令结果。 */
export function fetchDockerCmdResult(hostId: string, ref: string) {
  return request.get<DockerCmdResultResp>({
    url: `${PREFIX}/docker/hosts/${hostId}/cmds/${ref}`,
    showErrorMessage: false
  })
}

/**
 * 流端点的绝对地址。
 *
 * axios 的 baseURL（VITE_API_URL）可为 `/`（同源，开发走 Vite 代理）或绝对地址；
 * `fetch` 与 `WebSocket` 只吃绝对地址，故这里按与 axios 相同的拼接口径补全 origin。
 */
function streamUrl(path: string): string {
  const base = String(import.meta.env.VITE_API_URL || '/')
  const joined = `${base.replace(/\/+$/, '')}/${`${PREFIX}${path}`.replace(/^\/+/, '')}`
  if (/^https?:\/\//i.test(joined)) return joined
  const origin = typeof window === 'undefined' ? 'http://localhost' : window.location.origin
  return `${origin}${joined.startsWith('/') ? '' : '/'}${joined}`
}

/**
 * 日志 Follow：接入已建立的日志流会话（NDJSON）。
 *
 * 这条端点没有现成的 axios 封装可用 —— axios 会把响应体整段读成文本（流式就没了）。
 * 故用 fetch + ReadableStream，Authorization 头照 http 层的口径手工带；`signal`
 * 一断开，服务端（core）就会向 agent 下发取消，会话随之释放。
 */
export function openDockerLogStream(
  hostId: string,
  ref: string,
  signal: AbortSignal
): Promise<Response> {
  const { accessToken } = useUserStore()
  return fetch(streamUrl(`/docker/hosts/${hostId}/cmds/${ref}/stream`), {
    method: 'GET',
    headers: accessToken ? { Authorization: `Bearer ${accessToken}` } : {},
    signal
  })
}

/**
 * 终端：一次性票据的 WebSocket 地址。
 *
 * 浏览器 WebSocket 带不了 Authorization 头，故凭证走 ticket（TTL 30s、单次使用，
 * 来自**最新一次** `cmds/:ref` 轮询响应）；WS 建立失败后要用新票重试。
 */
export function dockerExecStreamUrl(hostId: string, ticket: string): string {
  const url = new URL(streamUrl(`/docker/hosts/${hostId}/stream/exec`))
  url.searchParams.set('ticket', ticket)
  url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:'
  return url.toString()
}
