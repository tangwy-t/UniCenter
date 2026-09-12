import { describe, it, expect } from 'vitest'
import {
  buildAuthMessage,
  buildPingMessage,
  parseServerMessage,
  buildWsUrl,
  ServerMsgType
} from './protocol'

describe('buildAuthMessage', () => {
  it('构造 auth 消息 JSON', () => {
    expect(JSON.parse(buildAuthMessage('token-abc'))).toEqual({ type: 'auth', token: 'token-abc' })
  })
})

describe('buildPingMessage', () => {
  it('构造 ping 消息 JSON', () => {
    expect(JSON.parse(buildPingMessage())).toEqual({ type: 'ping' })
  })
})

describe('parseServerMessage', () => {
  it('解析合法消息', () => {
    const raw = JSON.stringify({ type: ServerMsgType.NEW_NOTICE, data: { id: '1', title: 't' } })
    expect(parseServerMessage(raw)).toEqual({ type: 'new_notice', data: { id: '1', title: 't' } })
  })

  it('data 缺省时返回 { type }', () => {
    expect(parseServerMessage(JSON.stringify({ type: 'pong' }))).toEqual({ type: 'pong' })
  })

  it('非字符串输入返回 null', () => {
    expect(parseServerMessage(new ArrayBuffer(8))).toBeNull()
    expect(parseServerMessage(undefined)).toBeNull()
    expect(parseServerMessage(null)).toBeNull()
  })

  it('非法 JSON 返回 null', () => {
    expect(parseServerMessage('not json')).toBeNull()
  })

  it('缺少 type 字段返回 null', () => {
    expect(parseServerMessage(JSON.stringify({ data: 1 }))).toBeNull()
  })
})

describe('buildWsUrl', () => {
  it('基于 location 与 VITE_API_PREFIX 构造 ws 地址', () => {
    const { location } = globalThis
    // @ts-expect-error 测试中替换 location
    delete globalThis.location
    // @ts-expect-error jsdom 缺省 location
    globalThis.location = { protocol: 'http:', host: 'localhost:3006' }
    try {
      // vite 注入 import.meta.env；vitest 会用当前 VITE_API_PREFIX=.env 的值 /api/v1
      expect(buildWsUrl()).toMatch(/^ws:\/\/localhost:3006\/api\/v1\/ws$/)
    } finally {
      globalThis.location = location
    }
  })
})
