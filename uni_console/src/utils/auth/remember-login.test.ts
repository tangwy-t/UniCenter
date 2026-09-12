/**
 * remember-login 工具单元测试
 *
 * 覆盖：保存/读取回填、UTF-8（中文）密码往返、损坏数据容错、清除。
 * node 环境下无 localStorage，用内存 Map 替身。
 */
import { describe, it, expect, beforeEach, vi } from 'vitest'
import {
  saveRememberedLogin,
  loadRememberedLogin,
  clearRememberedLogin,
  type RememberedLogin
} from './remember-login'
import { StorageConfig } from '@/utils/storage/storage-config'

/** 内存版 localStorage 替身 */
function createStorageMock() {
  let store = new Map<string, string>()
  return {
    getItem: vi.fn((key: string) => store.get(key) ?? null),
    setItem: vi.fn((key: string, value: string) => {
      store.set(key, String(value))
    }),
    removeItem: vi.fn((key: string) => {
      store.delete(key)
    }),
    /** 测试助手：读取原始存储内容 */
    raw: (key: string) => store.get(key) ?? null,
    /** 测试助手：重置内部存储 */
    reset: () => {
      store = new Map()
    }
  }
}

const storageMock = createStorageMock()

describe('utils/auth/remember-login', () => {
  beforeEach(() => {
    storageMock.reset()
    vi.stubGlobal('localStorage', storageMock)
  })

  it('保存后能读取回账号和密码', () => {
    saveRememberedLogin('admin', '123456')

    const saved = loadRememberedLogin()
    expect(saved).toEqual({ username: 'admin', password: '123456' })
  })

  it('密码存储为编码形式，不与明文相同', () => {
    saveRememberedLogin('admin', '123456')

    const raw = storageMock.raw(StorageConfig.REMEMBER_LOGIN_KEY)
    expect(raw).toBeTruthy()
    expect(raw).not.toContain('123456')

    const parsed = JSON.parse(raw!) as RememberedLogin
    expect(parsed.encodedPassword).toBeTruthy()
  })

  it('UTF-8 多字节密码（中文/emoji）可无损往返', () => {
    const pass = '密码🔐123'
    saveRememberedLogin('admin', pass)

    const saved = loadRememberedLogin()
    expect(saved?.password).toBe(pass)
  })

  it('未保存过时读取返回 null', () => {
    expect(loadRememberedLogin()).toBeNull()
  })

  it('存储内容损坏时容错返回 null 并清理', () => {
    storageMock.setItem(StorageConfig.REMEMBER_LOGIN_KEY, '{{not-json')
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {})

    try {
      expect(loadRememberedLogin()).toBeNull()
      expect(warnSpy).toHaveBeenCalled()
      expect(storageMock.getItem(StorageConfig.REMEMBER_LOGIN_KEY)).toBeNull()
    } finally {
      warnSpy.mockRestore()
    }
  })

  it('数据缺字段时按无凭据处理', () => {
    storageMock.setItem(StorageConfig.REMEMBER_LOGIN_KEY, JSON.stringify({ username: 'admin' }))
    expect(loadRememberedLogin()).toBeNull()
  })

  it('清除后读取返回 null', () => {
    saveRememberedLogin('admin', '123456')
    clearRememberedLogin()

    expect(loadRememberedLogin()).toBeNull()
    expect(storageMock.getItem(StorageConfig.REMEMBER_LOGIN_KEY)).toBeNull()
  })
})
