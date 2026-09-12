import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import WebSocketClient from './index'
import { buildAuthMessage } from './protocol'

/** 最小 WebSocket 替身：记录实例与发送内容，可控地触发 open */
class FakeWebSocket {
  static CONNECTING = 0
  static OPEN = 1
  static CLOSING = 2
  static CLOSED = 3
  static instances: FakeWebSocket[] = []

  readyState: number = FakeWebSocket.CONNECTING
  url: string
  sent: string[] = []
  onopen: ((e: Event) => void) | null = null
  onmessage: ((e: MessageEvent) => void) | null = null
  onclose: ((e: CloseEvent) => void) | null = null
  onerror: ((e: Event) => void) | null = null

  constructor(url: string) {
    this.url = url
    FakeWebSocket.instances.push(this)
  }
  send(data: string) {
    this.sent.push(data)
  }
  close() {
    this.readyState = FakeWebSocket.CLOSED
  }
}

beforeEach(() => {
  FakeWebSocket.instances = []
  vi.stubGlobal('WebSocket', FakeWebSocket)
  WebSocketClient.destroyInstance()
})

afterEach(() => {
  WebSocketClient.destroyInstance()
  vi.unstubAllGlobals()
})

describe('WebSocketClient', () => {
  it('getInstance 首次创建即建立连接（回归：此前只 new 不 init）', () => {
    WebSocketClient.getInstance({
      url: 'ws://test/api/v1/ws',
      messageHandler: () => {}
    })
    expect(FakeWebSocket.instances.length).toBe(1)
    expect(FakeWebSocket.instances[0].url).toBe('ws://test/api/v1/ws')
  })

  it('连接打开后触发 onOpen（用于发送认证）', () => {
    let opened = false
    WebSocketClient.getInstance({
      url: 'ws://test/api/v1/ws',
      messageHandler: () => {},
      onOpen: () => {
        opened = true
      }
    })
    const fake = FakeWebSocket.instances[0]
    fake.readyState = FakeWebSocket.OPEN
    fake.onopen?.(new Event('open'))
    expect(opened).toBe(true)
  })

  it('onOpen 中调用 send 可发出 auth 消息', () => {
    const client = WebSocketClient.getInstance({
      url: 'ws://test/api/v1/ws',
      messageHandler: () => {},
      onOpen: () => {
        client.send(buildAuthMessage('token-1'))
      }
    })
    const fake = FakeWebSocket.instances[0]
    fake.readyState = FakeWebSocket.OPEN
    fake.onopen?.(new Event('open'))
    expect(fake.sent).toEqual([JSON.stringify({ type: 'auth', token: 'token-1' })])
  })

  it('destroyInstance 后再次 getInstance 会重新建立连接', () => {
    WebSocketClient.getInstance({ url: 'ws://test/api/v1/ws', messageHandler: () => {} })
    expect(FakeWebSocket.instances.length).toBe(1)
    WebSocketClient.destroyInstance()
    WebSocketClient.getInstance({ url: 'ws://test/api/v1/ws', messageHandler: () => {} })
    expect(FakeWebSocket.instances.length).toBe(2)
  })

  it('心跳发送 pingMessage 工厂产物（JSON ping，非裸字符串）', () => {
    vi.useFakeTimers()
    try {
      WebSocketClient.getInstance({
        url: 'ws://test/api/v1/ws',
        messageHandler: () => {},
        pingMessage: () => JSON.stringify({ type: 'ping' })
      })
      const fake = FakeWebSocket.instances[0]
      fake.readyState = FakeWebSocket.OPEN
      fake.onopen?.(new Event('open'))
      vi.advanceTimersByTime(10_000)
      expect(fake.sent).toContain(JSON.stringify({ type: 'ping' }))
    } finally {
      vi.useRealTimers()
    }
  })

  it('当前连接关闭时触发 onClose', () => {
    let closed = 0
    WebSocketClient.getInstance({
      url: 'ws://test/api/v1/ws',
      messageHandler: () => {},
      onClose: () => {
        closed++
      }
    })
    const fake = FakeWebSocket.instances[0]
    fake.onclose?.({
      code: 1006,
      reason: '',
      wasClean: false,
      target: fake
    } as unknown as CloseEvent)
    expect(closed).toBe(1)
  })

  it('已销毁实例的延迟 close 事件不会误触发新实例的 onClose', () => {
    let closed = 0
    WebSocketClient.getInstance({
      url: 'ws://test/api/v1/ws',
      messageHandler: () => {},
      onClose: () => {
        closed++
      }
    })
    const oldFake = FakeWebSocket.instances[0]
    WebSocketClient.destroyInstance()
    WebSocketClient.getInstance({
      url: 'ws://test/api/v1/ws',
      messageHandler: () => {},
      onClose: () => {
        closed++
      }
    })
    expect(FakeWebSocket.instances.length).toBe(2)
    oldFake.onclose?.({
      code: 1006,
      reason: '',
      wasClean: false,
      target: oldFake
    } as unknown as CloseEvent)
    expect(closed).toBe(0)
  })

  it('重连次数耗尽（close(true)）后 ensureConnection 可重新拉起连接', () => {
    const client = WebSocketClient.getInstance({
      url: 'ws://test/api/v1/ws',
      messageHandler: () => {}
    })
    expect(FakeWebSocket.instances.length).toBe(1)
    client.close(true)
    client.ensureConnection()
    expect(FakeWebSocket.instances.length).toBe(2)
  })
})
