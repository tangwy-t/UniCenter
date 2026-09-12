import { describe, expect, it } from 'vitest'
import { compileRoutePattern, matchRouteInTree, matchRoutePath } from './routePathMatch'

describe('matchRoutePath', () => {
  it('非动态路径走精确比较', () => {
    expect(matchRoutePath('/system/user', '/system/user')).toBe(true)
    expect(matchRoutePath('/system/users', '/system/user')).toBe(false)
  })

  it('动态段 :param 匹配单个路径段', () => {
    expect(matchRoutePath('/system/dict/123', '/system/dict/:typeId')).toBe(true)
    expect(matchRoutePath('/system/job/9/logs', '/system/job/:jobId/logs')).toBe(true)
  })

  it('动态段不跨路径分隔符', () => {
    // :typeId 只能吃一段,/system/dict/a/b 不应命中
    expect(matchRoutePath('/system/dict/a/b', '/system/dict/:typeId')).toBe(false)
  })

  it('catch-all * 匹配任意层级', () => {
    expect(matchRoutePath('/a/b/c', '/:pathMatch(.*)*')).toBe(false)
    expect(matchRoutePath('/a/b/c', '/a/*')).toBe(true)
  })

  // 这是本工具存在的核心理由:正则元字符必须被转义。
  // 旧的双实现中,isStaticRoute 直接拼 RegExp 未做转义,
  // 导致含 `.` 的路径会匹配到本不该命中的目标。
  it('正则元字符被转义:`.` 只匹配字面量', () => {
    expect(matchRoutePath('/a.b', '/a.b')).toBe(true)
    expect(matchRoutePath('/axb', '/a.b')).toBe(false)
  })

  it('正则元字符被转义:括号与加号', () => {
    expect(matchRoutePath('/a(1)', '/a(1)')).toBe(true)
    expect(matchRoutePath('/a1', '/a(1)')).toBe(false)
    expect(matchRoutePath('/a+', '/a+')).toBe(true)
    expect(matchRoutePath('/aa', '/a+')).toBe(false)
  })
})

describe('matchRouteInTree', () => {
  const tree = [
    { path: '/system/user', name: 'SystemUser' },
    {
      path: '/monitor',
      name: 'Monitor',
      children: [{ path: '/monitor/server', name: 'MonitorServer' }]
    }
  ]

  it('命中顶层路由', () => {
    expect(matchRouteInTree('/system/user', tree)).toBe(true)
  })

  it('递归命中子路由', () => {
    expect(matchRouteInTree('/monitor/server', tree)).toBe(true)
  })

  it('未命中返回 false', () => {
    expect(matchRouteInTree('/nope', tree)).toBe(false)
  })

  it('excludeNames 排除指定路由(404 catch-all 不可匿名访问)', () => {
    const withCatchAll = [...tree, { path: '/:pathMatch(.*)*', name: 'Exception404' }]
    expect(matchRouteInTree('/anything', withCatchAll)).toBe(true)
    expect(matchRouteInTree('/anything', withCatchAll, ['Exception404'])).toBe(false)
  })
})

describe('compileRoutePattern', () => {
  it('生成锚定的正则', () => {
    const re = compileRoutePattern('/system/dict/:typeId')
    expect(re.test('/system/dict/7')).toBe(true)
    expect(re.test('/x/system/dict/7')).toBe(false)
  })
})
