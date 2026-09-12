import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

// 测试环境(node)下隔离重依赖:element-plus 入口会引 CSS/SCSS,
// user store 会引 router/api 链,socket 客户端会引 http 链。仅测 setUnreadCount 状态面。
vi.mock('element-plus', () => ({
  ElMessageBox: { alert: vi.fn() }
}))
vi.mock('./user', () => ({
  useUserStore: () => ({ accessToken: '', logOut: vi.fn() })
}))
vi.mock('@/utils/socket', () => ({ default: class {} }))
vi.mock('@/utils/socket/protocol', () => ({
  buildWsUrl: () => '',
  buildAuthMessage: () => '',
  buildPingMessage: () => '',
  parseServerMessage: () => null,
  ServerMsgType: {
    AUTH_OK: 'auth_ok',
    AUTH_ERR: 'auth_err',
    NEW_NOTICE: 'new_notice',
    PONG: 'pong',
    KICKED: 'kicked'
  }
}))

import { useSocketStore } from './socket'

describe('socketStore.setUnreadCount', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('直接设定未读数', () => {
    const store = useSocketStore()
    store.setUnreadCount(5)
    expect(store.unreadCount).toBe(5)
  })

  it('负数钳制为 0', () => {
    const store = useSocketStore()
    store.setUnreadCount(6)
    store.setUnreadCount(-3)
    expect(store.unreadCount).toBe(0)
  })

  it('归零', () => {
    const store = useSocketStore()
    store.setUnreadCount(9)
    store.setUnreadCount(0)
    expect(store.unreadCount).toBe(0)
  })

  it('非整数向下取整', () => {
    const store = useSocketStore()
    store.setUnreadCount(3.9)
    expect(store.unreadCount).toBe(3)
  })
})
