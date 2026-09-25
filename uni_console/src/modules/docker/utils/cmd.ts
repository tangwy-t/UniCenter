/** 一期可用的动作、轮询节奏与结果载荷解析（纯函数）。 */

/**
 * 一期页面上**唯一允许出现**的四个只读动作。
 *
 * 这份常量与「分期控件矩阵」（spec §11.0）同源：页面据此渲染操作入口，
 * 二/三/四期的动作连字符串都不该出现在模块里（由 __tests__/phase-gate.test.ts 扫描钉住）。
 */
export const PHASE1_ACTIONS = [
  'container:inspect',
  'container:logs',
  'image:inspect',
  'compose.file:read'
] as const
export type Phase1Action = (typeof PHASE1_ACTIONS)[number]

export function isPhase1Action(action: string): action is Phase1Action {
  return (PHASE1_ACTIONS as readonly string[]).includes(action)
}

/**
 * 轮询间隔（毫秒）：1 秒起指数退避，5 秒封顶（spec §4.1 的轮询节奏）。
 *
 * 退避的意义：轻量指令（30s 超时）通常 1 秒内就有结果 —— 固定 1 秒轮询把一个
 * 亚秒级的操作拖成 1 秒的体感；而 15 分钟的大指令不该让前端每秒打一次接口。
 */
export function pollDelay(attempt: number): number {
  return Math.min(1000 * 2 ** Math.max(0, attempt), 5000)
}

export interface LogsPayload {
  lines: string
  truncated: boolean
}

/**
 * 解析日志结果载荷。
 *
 * 形状不符时返回空结果而不是抛错：载荷来自 agent，页面不该因为一次形状意外白屏 ——
 * 空文本 + 「未取到日志」的提示是可恢复的，白屏不是。
 */
export function parseLogsPayload(payload: unknown): LogsPayload {
  const p = payload as Partial<LogsPayload> | null | undefined
  if (!p || typeof p.lines !== 'string') return { lines: '', truncated: false }
  return { lines: p.lines, truncated: Boolean(p.truncated) }
}

export interface ImageInspectView {
  id: string
  repoTags: string[]
  sizeBytes: number
  created?: number
  architecture?: string
  os?: string
  labels: Record<string, string>
  env: string[]
  exposedPorts: string[]
  entrypoint: string[]
  cmd: string[]
  history: {
    sizeBytes?: number
    created?: number
    createdBy?: string
    emptyLayer?: boolean
    comment?: string
  }[]
}

/**
 * 线上原始载荷的键名 —— **snake_case**。
 *
 * 这些形状不是猜的：core 对结果载荷**原样透传**（`service/docker_cmd.go` 的 `Result`），
 * 而协议侧的 json tag 就是 snake_case（`uni_protocol/docker.go` 里
 * `DockerImageInspectPayload` 的 `repo_tags`/`size_bytes`/`exposed_ports`、
 * `DockerContainerInspectPayload` 的 `started_at`/`exit_code`/`restart_policy`）。
 * 视图（View）用 camelCase 是为了跟随本仓库前端的命名习惯，**转换只在这里做一次**。
 */
interface RawImageInspect {
  id?: string
  repo_tags?: string[]
  size_bytes?: number
  created?: number
  architecture?: string
  os?: string
  labels?: Record<string, string>
  env?: string[]
  exposed_ports?: string[]
  entrypoint?: string[]
  cmd?: string[]
  history?: {
    size_bytes?: number
    created?: number
    created_by?: string
    empty_layer?: boolean
    comment?: string
  }[]
}

/** 解析镜像详情载荷（形状不符时给空历史，页面显示「没有分层信息」）。 */
export function parseImageInspectPayload(payload: unknown): ImageInspectView {
  const p = (payload ?? {}) as RawImageInspect
  return {
    id: p.id ?? '',
    repoTags: p.repo_tags ?? [],
    sizeBytes: p.size_bytes ?? 0,
    created: p.created,
    architecture: p.architecture,
    os: p.os,
    labels: p.labels ?? {},
    env: p.env ?? [],
    exposedPorts: p.exposed_ports ?? [],
    entrypoint: p.entrypoint ?? [],
    cmd: p.cmd ?? [],
    history: (p.history ?? []).map((l) => ({
      sizeBytes: l.size_bytes,
      created: l.created,
      createdBy: l.created_by,
      emptyLayer: l.empty_layer,
      comment: l.comment
    }))
  }
}

/** 容器详情载荷（环境变量/配置 Tab 用）。 */
export interface ContainerInspectView {
  id: string
  name: string
  image: string
  imageId?: string
  state: string
  createdAt?: number
  startedAt?: number
  finishedAt?: number
  exitCode?: number
  restartPolicy?: string
  env: string[]
  labels: Record<string, string>
  mounts: { type?: string; source?: string; destination?: string; rw?: boolean }[]
  entrypoint: string[]
  cmd: string[]
  health?: string
  networks: string[]
}

/** 容器 inspect 的线上原始形状（snake_case，理由同 RawImageInspect）。 */
interface RawContainerInspect {
  id?: string
  name?: string
  image?: string
  image_id?: string
  state?: string
  created?: number
  started_at?: number
  finished_at?: number
  exit_code?: number
  restart_policy?: string
  env?: string[]
  labels?: Record<string, string>
  mounts?: { type?: string; source?: string; destination?: string; rw?: boolean }[]
  entrypoint?: string[]
  cmd?: string[]
  health?: string
  networks?: string[]
}

export function parseContainerInspectPayload(payload: unknown): ContainerInspectView {
  const p = (payload ?? {}) as RawContainerInspect
  return {
    id: p.id ?? '',
    name: p.name ?? '',
    image: p.image ?? '',
    imageId: p.image_id,
    state: p.state ?? '',
    createdAt: p.created,
    startedAt: p.started_at,
    finishedAt: p.finished_at,
    exitCode: p.exit_code,
    restartPolicy: p.restart_policy,
    env: p.env ?? [],
    labels: p.labels ?? {},
    mounts: p.mounts ?? [],
    entrypoint: p.entrypoint ?? [],
    cmd: p.cmd ?? [],
    health: p.health,
    networks: p.networks ?? []
  }
}
