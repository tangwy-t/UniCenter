/**
 * docker · 模块 API
 *
 * 字段一律取 `Api.Docker.*`（apigen 生成物是唯一事实源）；四个端点的形状见
 * uni_core 的 response/docker.go 与 spec §4.1。
 */
import request from '@/utils/http'

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
