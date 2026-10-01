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
export type DockerOverviewResp = Api.Docker.DockerOverviewResp
export type DockerContainerItem = Api.Docker.DockerContainerItem
export type DockerPortItem = Api.Docker.DockerPortItem
export type DockerImageItem = Api.Docker.DockerImageItem
export type DockerVolumeItem = Api.Docker.DockerVolumeItem
export type DockerNetworkItem = Api.Docker.DockerNetworkItem
export type DockerProjectItem = Api.Docker.DockerProjectItem
export type DockerCmdResultResp = Api.Docker.DockerCmdResultResp
export type DockerWorkloadItem = Api.Docker.DockerWorkloadItem
export type DockerWorkloadListResp = Api.Docker.DockerWorkloadListResp
export type DockerRegistryItem = Api.Docker.DockerRegistryItem
export type DockerRegistryListResp = Api.Docker.DockerRegistryListResp
export type DockerTaskItem = Api.Docker.DockerTaskItem
export type DockerTaskListResp = Api.Docker.DockerTaskListResp
export type DockerStatsHistoryResp = Api.Docker.DockerStatsHistoryResp
export type DockerStatsHistorySample = Api.Docker.DockerStatsHistorySample
export type DockerBuildContextUploadResp = Api.Docker.DockerBuildContextUploadResp

/**
 * 仓库凭据的创建/更新请求体（形状对齐 uni_core 的 request.DockerRegistrySaveReq）。
 *
 * **创建与更新同形**（更新按 registry 定位、密码必须重输 —— 后端不提供「读回旧
 * 密码再提交」的路径，列表里的 password 恒为掩码「****」，见 response 的说明）。
 */
export interface DockerRegistrySaveBody {
  registry: string
  username: string
  password: string
  remark?: string
}

/** 指令受理请求体（形状对齐 uni_core 的 request.DockerCmdReq）。 */
export interface DockerCmdBody {
  action: string
  target?: string
  options?: Record<string, unknown>
  confirm?: string
}

/** 统一工作负载表的查询参数（三项都可选且相互独立，先主机、再状态、再关键字收窄）。 */
export interface DockerWorkloadQuery {
  /** 限定单主机（留空 = 全部主机）。 */
  hostId?: string
  /** 运行态：running / stopped（stopped = 一切非 running 的统称）。 */
  state?: 'running' | 'stopped'
  /** 容器名/镜像名的子串匹配（大小写不敏感）。 */
  keyword?: string
}

/** 可管主机清单（主机切换器的数据源）。 */
export function fetchDockerHosts() {
  return request.get<DockerHostListResp>({ url: `${PREFIX}/docker/hosts` })
}

/**
 * 跨主机总览（控制塔）：航队 KPI + 主机清单 + 异常容器清单，一次请求拿全。
 *
 * 不带 showErrorMessage 之外的任何口径：四态（加载/空/错误/部分失败）由总览页
 * 自行编排 —— 主机条目里的 error 字段是单台的部分失败，整页级失败才走 catch。
 */
export function fetchDockerOverview() {
  return request.get<DockerOverviewResp>({ url: `${PREFIX}/docker/overview` })
}

/** 一台主机的资源快照（含服务端算好的陈旧结论）。 */
export function fetchDockerState(hostId: string) {
  return request.get<DockerStateResp>({ url: `${PREFIX}/docker/hosts/${hostId}/state` })
}

/**
 * 跨主机统一工作负载表（容器列表页的新数据源）：GET /docker/containers。
 *
 * 三个过滤参数**全部作为 query 发给端点**（服务端过滤）—— 列表页不再本地过滤快照：
 * 快照只有当前一台主机的事实，跨主机表的筛选必须在服务端做。空值不发（axios 对
 * undefined 参数自动省略），非法 state 由服务端给 400 结论句。
 */
export function fetchDockerContainers(params: DockerWorkloadQuery = {}) {
  return request.get<DockerWorkloadListResp>({ url: `${PREFIX}/docker/containers`, params })
}

/**
 * stats 历史回看（P2 监控面 · 历史半边）：GET /docker/hosts/:id/containers/:cid/
 * stats-history —— 升序、最多 60 样本（30s × 60 = 30 分钟），无历史给空数组。
 *
 * 用途是抽屉打开时的**预填**：container-stats 先拉这一份让曲线立即有 30 分钟
 * 形状，再开实时流，两段衔接见 utils/stats 的 mergeStatsHistory。样本与流帧
 * 同义不同名（这里走 apigen 的 camelCase，流帧是 agent 的 snake_case 裸行），
 * 对齐收在衔接函数里。showErrorMessage 关掉：历史是回看增强不是门槛，拉不到
 * 就静默降级为纯实时 —— 抽屉还开着，结论句不进 toast。
 */
export function fetchDockerStatsHistory(hostId: string, containerId: string) {
  return request.get<DockerStatsHistoryResp>({
    url: `${PREFIX}/docker/hosts/${hostId}/containers/${containerId}/stats-history`,
    showErrorMessage: false
  })
}

/** 受理一条指令（202 + ref）。一期只用到四个只读动作。
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
 * 构建上下文上传（P3·分发输入段）：POST /docker/hosts/:id/build-context，
 * 请求体就是 tar.gz 字节流本身（不是 multipart —— 服务端按 Content-Type 判形），
 * 200 回 `{ filename }`（core 由会话号推导的产物名，直接喂给 image:build 的
 * options.context）。
 *
 * 为什么**留在这条 http 层**而不是像流端点那样另起 fetch：axios 在浏览器里
 * 走的就是 XHR 适配器 —— 上传进度事件（xhr.upload.onprogress）axios 原生以
 * `onUploadProgress` 暴露，经 request.post 的透传配置直通；fetch 没有上传进度
 * 事件（下载才有），为进度换 fetch 反而要自己拼 XHR —— 留在 http 层还能保住
 * 401 刷新重放与 {code,msg,data} 信封解包（新写一套 XHR 就要重做这两件事）。
 * 两处**必须**显式覆盖默认值的配置：
 *   - `timeout: 0`：实例默认 15s 是给 JSON 往返的；512MB 慢链路上传以分钟计，
 *     不覆盖会在第 15 秒被掐断（进度条的尽头是超时）；
 *   - `Content-Type: application/octet-stream`：请求拦截器对「无类型头的对象体」
 *     会 JSON.stringify —— 显式给头，File/Blob 才能原样进 body。
 * 512MB 上限与 gzip 魔数（1f 8b）由服务端执法、客户端预检前移（utils/build-push）；
 * 结论就地显示（对话框还开着），故 showErrorMessage 关掉 —— 与 sendDockerCmd
 * 同一条「结论不放 toast」的纪律。
 *
 * @param onProgress 上传进度回调（0-100 整数；服务端不回「已收字节」之外的
 *   阶段信息，进度就是传输进度的全部事实）。
 * @param signal 断开用（关对话框/卸载时中止在途上传；服务端收尾见下）。
 */
export function uploadDockerBuildContext(
  hostId: string,
  file: Blob,
  onProgress?: (percent: number) => void,
  signal?: AbortSignal
) {
  return request.post<DockerBuildContextUploadResp>({
    url: `${PREFIX}/docker/hosts/${hostId}/build-context`,
    data: file,
    headers: { 'Content-Type': 'application/octet-stream' },
    timeout: 0,
    signal,
    onUploadProgress: (e) => {
      if (!onProgress) return
      // total 缺席（分块上传无长度头）时不报进度：未知总量的「50%」是编造。
      if (!e.total || e.total <= 0) return
      onProgress(Math.min(100, Math.floor((e.loaded / e.total) * 100)))
    },
    showErrorMessage: false
  })
}

// ── 私有仓库凭据（4c）────────────────────────────────────────────────
// 四条都是「对话框开着」时调的：错误就地显示在对话框里（表单错误句/列表失败态），
// 不走 toast —— 与 sendDockerCmd 同一条纪律（对话框还开着，结论不放 toast，
// 统一弹 toast 会把「地址已存在」和「没权限」说成同一句话）。静态 perm
// docker:config 在路由侧，前端入口的门控见各组件的 canConfig。

/** 仓库凭据清单（密码恒为掩码「****」，任何读路径不回明文）。 */
export function fetchDockerRegistries() {
  return request.get<DockerRegistryListResp>({
    url: `${PREFIX}/docker/registries`,
    showErrorMessage: false
  })
}

/** 新建一条凭据（409 = 该仓库地址已有凭据）。 */
export function createDockerRegistry(data: DockerRegistrySaveBody) {
  return request.post<DockerRegistryItem>({
    url: `${PREFIX}/docker/registries`,
    data,
    showErrorMessage: false
  })
}

/**
 * 更新既有凭据（按 body.registry 定位，用户名/密码/备注整体重写）。
 *
 * **密码必须重输**：后端对空密码给 400（没有任何「留空 = 保持原密码」的语义），
 * 表单侧的显式标注见 registry-credentials-dialog。
 */
export function updateDockerRegistry(data: DockerRegistrySaveBody) {
  return request.put<DockerRegistryItem>({
    url: `${PREFIX}/docker/registries`,
    data,
    showErrorMessage: false
  })
}

/**
 * 删除一条凭据（删除即失效：下一单带该 registry 的拉取在受理处即被拒）。
 *
 * registry 是**路径参数**（不带斜杠 —— 协议的地址形态本就不含路径，无需编码；
 * 端口里的冒号在路径段里合法，gin 按斜杠分段取参）。
 */
export function deleteDockerRegistry(registry: string) {
  return request.del<void>({
    url: `${PREFIX}/docker/registries/${registry}`,
    showErrorMessage: false
  })
}

// ── 任务中心（6b）──────────────────────────────────────────────────
// 读面在 uni_core 的 service/docker_tasks.go：≤100 条、受理时刻降序、跨主机聚合；
// 条目含 ref（轮询与拉取进度流的钥匙）、发起人、终态与结论句原文。

/** 任务中心查询参数（三项都可选且相互独立，全部作为 query 发给端点、服务端过滤）。 */
export interface DockerTasksQuery {
  /** 限定单主机（留空 = 跨主机聚合）。 */
  hostId?: string
  /** 阶段过滤：pending（仍在执行）/ done（一切终态：succeeded/failed/timeout）。 */
  status?: 'pending' | 'done'
  /** 动作码过滤（如 image:pull；未登记动作由服务端给 400 结论句）。 */
  action?: string
}

/**
 * 任务中心列表（GET /docker/tasks）。
 *
 * 不走 showErrorMessage：抽屉开着时结论就地显示（首拉失败给错误态 + 重试、
 * 静默轮询失败保留最后已知列表并标注 —— 与 fetchDockerRegistries 同一条
 * 「对话框/抽屉还开着，结论不放 toast」的纪律）。
 */
export function fetchDockerTasks(params: DockerTasksQuery = {}) {
  return request.get<DockerTaskListResp>({
    url: `${PREFIX}/docker/tasks`,
    params,
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
 * stats 实时流（五期监控面）：接入已建立的 stats 会话（NDJSON 样本行）。
 *
 * 与 openDockerLogStream 同一条纪律（fetch + ReadableStream、Authorization 手工带、
 * `signal` 断开即让服务端向 agent 下发 cancel）—— 三条流通道（日志 NDJSON / stats
 * NDJSON / 终端 WS）里它和日志流唯一的不同是行形状：这里每行是一个打平的样本
 * （cpu_percent / mem_usage_mb / net_*_bytes_sec），首帧即当前值。
 */
export function openDockerStatsStream(
  hostId: string,
  ref: string,
  signal: AbortSignal
): Promise<Response> {
  const { accessToken } = useUserStore()
  return fetch(streamUrl(`/docker/hosts/${hostId}/cmds/${ref}/stats`), {
    method: 'GET',
    headers: accessToken ? { Authorization: `Bearer ${accessToken}` } : {},
    signal
  })
}

/**
 * 拉取进度流（4b 进度面）：GET cmds/:ref/pull —— NDJSON 进度行。
 *
 * 与日志/stats 流同一条纪律（fetch + ReadableStream、Authorization 头照 http 层的
 * 口径手工带）。三条与同族的差异：
 *   ① **指令 pending 期间即可接入**：会话不是由 result 终态里的 session_id 给出的
 *     （拉取的 result 只在结束时回），而是 core 在受理指令时就按「句柄 = pull_ + ref」
 *     预登记 —— 受理一回来就能开流，拉取全程逐层可见；
 *   ② 断开的语义重量不同：日志/stats 断开只是停流，这里断开（Abort）会让服务端向
 *     agent 下发 cancel，**终止这场拉取** —— 进度对话框关掉等于放弃拉取，是契约
 *     而不是副作用；
 *   ③ 行形状是打平的进度记录（id/status/current/total/done/error），eof 挂在最后
 *     一条进度行上（终态项与 eof 同行），消费端见 utils/pull.ts 的解析与折叠。
 */
export function openDockerPullStream(
  hostId: string,
  ref: string,
  signal: AbortSignal
): Promise<Response> {
  const { accessToken } = useUserStore()
  return fetch(streamUrl(`/docker/hosts/${hostId}/cmds/${ref}/pull`), {
    method: 'GET',
    headers: accessToken ? { Authorization: `Bearer ${accessToken}` } : {},
    signal
  })
}

// ── 构建与推送的进度流（P2·分发闭环）────────────────────────────────
// 与 openDockerPullStream 同一条纪律（fetch + ReadableStream、Authorization 头照
// http 层的口径手工带、**指令 pending 期间即可接入** —— 会话由 core 受理时按
// 句柄 build_<ref>/push_<ref> 预登记、断开 = 服务端向 agent 下发 cancel 终止操作
// 本身）。三条端点各自只承载自己的进度族（handler 按记录的 action 拒绝交叉接入），
// 行形状不同：build 是步骤/文本行（id/status/stream），push 与 pull 同字段集
//（daemon 的同一个 JSON 进度流）。

/**
 * 构建进度流（P2）：GET cmds/:ref/build —— NDJSON 构建记录行
 * （{"seq","t","id","status","stream","done","error","eof"}，eof 挂在最后一条
 * 记录行上；消费端见 utils/build-push.ts 的解析与播报折叠）。
 */
export function openDockerBuildStream(
  hostId: string,
  ref: string,
  signal: AbortSignal
): Promise<Response> {
  const { accessToken } = useUserStore()
  return fetch(streamUrl(`/docker/hosts/${hostId}/cmds/${ref}/build`), {
    method: 'GET',
    headers: accessToken ? { Authorization: `Bearer ${accessToken}` } : {},
    signal
  })
}

/**
 * 推送进度流（P2）：GET cmds/:ref/push —— NDJSON 进度行（字段集与 pull 端点
 * 逐字相同：{"seq","t","id","status","current","total","done","error","eof"}，
 * 只是层的语义词换成推送侧 —— Pushing/Pushed；消费端复用 utils/pull.ts 的折叠器
 * 经 createPushFeed 换语义）。
 */
export function openDockerPushStream(
  hostId: string,
  ref: string,
  signal: AbortSignal
): Promise<Response> {
  const { accessToken } = useUserStore()
  return fetch(streamUrl(`/docker/hosts/${hostId}/cmds/${ref}/push`), {
    method: 'GET',
    headers: accessToken ? { Authorization: `Bearer ${accessToken}` } : {},
    signal
  })
}

/**
 * 活动流聚合端点（六期）：GET /docker/events —— 跨主机 docker 事件的实时 NDJSON
 * 流；连上先回放各主机最近 50 条（按主机升序），再进实时，断开即结束。
 *
 * 与日志/stats 流同一条纪律（fetch + ReadableStream、Authorization 头照 http 层
 * 的口径手工带）；不同的两点：
 *   ① 它是**聚合流**（路由静态 perm docker:list，与总览同档），不带 hostId/ref
 *     —— 不接任何单主机会话，没有「先受理指令再接流」的两步走；
 *   ② 断开即结束由**消费端重连**（重连会重放最近 50 条/主机，去重守卫在
 *     utils/events 的 createEventsFeed 里），abort 之外还多一条重连路径。
 */
export function openDockerEventsStream(signal: AbortSignal): Promise<Response> {
  const { accessToken } = useUserStore()
  return fetch(streamUrl('/docker/events'), {
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
