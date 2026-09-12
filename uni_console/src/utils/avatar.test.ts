import { afterEach, describe, expect, it, vi } from 'vitest'
import { resolveAvatar } from './avatar'
import defaultAvatar from '@imgs/user/avatar.webp'

afterEach(() => {
  vi.unstubAllEnvs()
})

describe('resolveAvatar', () => {
  it('空值回落默认头像', () => {
    expect(resolveAvatar()).toBe(defaultAvatar)
    expect(resolveAvatar('   ')).toBe(defaultAvatar)
  })

  it('http(s) 外链原样返回', () => {
    expect(resolveAvatar('https://cdn.example.com/a.png')).toBe('https://cdn.example.com/a.png')
    expect(resolveAvatar('http://cdn.example.com/b.png')).toBe('http://cdn.example.com/b.png')
  })

  it('站内相对路径拼接 VITE_API_URL', () => {
    vi.stubEnv('VITE_API_URL', '/api')
    expect(resolveAvatar('/uploads/a.png')).toBe('/api/uploads/a.png')
    expect(resolveAvatar('uploads/b.png')).toBe('/api/uploads/b.png')
  })
})
