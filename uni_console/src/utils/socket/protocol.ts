/**
 * WebSocket 消息协议层 —— 镜像服务端 uni_core/internal/pkg/ws/message.go。
 * 纯函数、无副作用，可单测。传输细节见 utils/socket/index.ts（WebSocketClient）。
 */

export interface ClientMessage {
  type: 'auth' | 'ping'
  token?: string
}

export interface ServerMessage {
  type: string
  data?: unknown
}

/** new_notice 的 data 结构（服务端 SysNotice 实体 JSON） */
export interface NoticeData {
  id: string
  title: string
  content?: string
  noticeType?: number
  status?: number
  priority?: number
  publishType?: number
  publishTime?: string | null
  createdAt?: string
  updatedAt?: string
}

/** 服务端 → 客户端消息类型（与 uni_core/internal/pkg/ws/message.go 常量一一对应） */
export const ServerMsgType = {
  AUTH_OK: 'auth_ok',
  AUTH_ERR: 'auth_err',
  NEW_NOTICE: 'new_notice',
  PONG: 'pong',
  KICKED: 'kicked'
} as const

/** 构造 auth 消息（connect/每次重连后发送） */
export function buildAuthMessage(token: string): string {
  return JSON.stringify({ type: 'auth', token } satisfies ClientMessage)
}

/** 构造应用层 ping 消息 */
export function buildPingMessage(): string {
  return JSON.stringify({ type: 'ping' } satisfies ClientMessage)
}

/** 解析服务端消息；非文本/非法 JSON/缺 type 均返回 null */
export function parseServerMessage(data: unknown): ServerMessage | null {
  if (typeof data !== 'string') return null
  try {
    const parsed: unknown = JSON.parse(data)
    if (typeof parsed !== 'object' || parsed === null) return null
    const { type, ...rest } = parsed as { type?: unknown; data?: unknown }
    if (typeof type !== 'string') return null
    return rest.data === undefined ? { type } : { type, data: rest.data }
  } catch {
    return null
  }
}

/** 构造 ws 连接地址：同源 + VITE_API_PREFIX + /ws（dev 由 Vite 代理转发到后端） */
export function buildWsUrl(): string {
  const protocol = location.protocol === 'https:' ? 'wss://' : 'ws://'
  const prefix = import.meta.env.VITE_API_PREFIX || '/api/v1'
  return `${protocol}${location.host}${prefix}/ws`
}
