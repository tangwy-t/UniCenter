import { describe, expect, it } from 'vitest'
import { permissionChanged, permissionFingerprint } from './permissionFingerprint'

describe('permissionFingerprint', () => {
  it('与顺序无关', () => {
    // 后端返回顺序变化不应触发无谓的路由重建。
    expect(permissionFingerprint(['b', 'a'])).toBe(permissionFingerprint(['a', 'b']))
  })

  it('集合内容不同则指纹不同', () => {
    expect(permissionFingerprint(['a'])).not.toBe(permissionFingerprint(['a', 'b']))
  })

  it('undefined 等价于空集合', () => {
    expect(permissionFingerprint(undefined)).toBe(permissionFingerprint([]))
  })

  it('分隔符不可被权限标识本身伪造', () => {
    // 若用逗号拼接,["a,b"] 与 ["a","b"] 会得到相同指纹。
    expect(permissionFingerprint(['a,b'])).not.toBe(permissionFingerprint(['a', 'b']))
  })

  it('重复项不影响指纹', () => {
    expect(permissionFingerprint(['a', 'a'])).toBe(permissionFingerprint(['a', 'a']))
  })
})

describe('permissionChanged', () => {
  it('无基线时不视为变化(避免首屏无谓重建)', () => {
    expect(permissionChanged(null, ['system:user:add'])).toBe(false)
  })

  it('权限新增 → 变化', () => {
    const base = permissionFingerprint(['system:user:add'])
    expect(permissionChanged(base, ['system:user:add', 'system:user:edit'])).toBe(true)
  })

  it('权限撤销 → 变化', () => {
    const base = permissionFingerprint(['system:user:add', 'system:user:edit'])
    expect(permissionChanged(base, ['system:user:add'])).toBe(true)
  })

  it('权限未变(仅顺序不同)→ 不变', () => {
    const base = permissionFingerprint(['a', 'b'])
    expect(permissionChanged(base, ['b', 'a'])).toBe(false)
  })

  it('权限清空 → 变化', () => {
    const base = permissionFingerprint(['a'])
    expect(permissionChanged(base, [])).toBe(true)
  })
})
