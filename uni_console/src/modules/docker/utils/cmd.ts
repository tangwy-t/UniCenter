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
  history: { sizeBytes?: number; created?: number; createdBy?: string; emptyLayer?: boolean }[]
}

/** 解析镜像详情载荷（形状不符时给空历史，页面显示「没有分层信息」）。 */
export function parseImageInspectPayload(payload: unknown): ImageInspectView {
  const p = (payload ?? {}) as Partial<ImageInspectView>
  return {
    id: p.id ?? '',
    repoTags: p.repoTags ?? [],
    sizeBytes: p.sizeBytes ?? 0,
    created: p.created,
    architecture: p.architecture,
    os: p.os,
    labels: p.labels ?? {},
    env: p.env ?? [],
    exposedPorts: p.exposedPorts ?? [],
    entrypoint: p.entrypoint ?? [],
    cmd: p.cmd ?? [],
    history: p.history ?? []
  }
}

/** 容器详情载荷（环境变量/配置 Tab 用）。 */
export interface ContainerInspectView {
  id: string
  name: string
  image: string
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

export function parseContainerInspectPayload(payload: unknown): ContainerInspectView {
  const p = (payload ?? {}) as Partial<ContainerInspectView>
  return {
    id: p.id ?? '',
    name: p.name ?? '',
    image: p.image ?? '',
    state: p.state ?? '',
    createdAt: p.createdAt,
    startedAt: p.startedAt,
    finishedAt: p.finishedAt,
    exitCode: p.exitCode,
    restartPolicy: p.restartPolicy,
    env: p.env ?? [],
    labels: p.labels ?? {},
    mounts: p.mounts ?? [],
    entrypoint: p.entrypoint ?? [],
    cmd: p.cmd ?? [],
    health: p.health,
    networks: p.networks ?? []
  }
}
