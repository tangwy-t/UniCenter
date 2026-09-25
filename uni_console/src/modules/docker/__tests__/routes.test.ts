import { describe, expect, it } from 'vitest'
import plugin from '../index'

/** 五条列表路由的 path 与后端 v015 种子菜单**逐字一致**（跨仓比对由 Go 侧测试做）。 */
const MENU_PATHS = [
  '/docker/containers',
  '/docker/images',
  '/docker/volumes',
  '/docker/networks',
  '/docker/projects'
]

describe('docker 模块路由', () => {
  const routes = plugin.routes ?? []

  it('五条列表页 path 存在且不重复', () => {
    const paths = routes.map((r) => r.path)
    for (const p of MENU_PATHS) expect(paths, `缺少列表页路由 ${p}`).toContain(p)
    expect(new Set(paths).size).toBe(paths.length)
  })

  it('五条列表页都需要 docker:list 权限', () => {
    for (const p of MENU_PATHS) {
      const r = routes.find((x) => x.path === p)
      expect(r?.meta?.authMark, `${p} 缺少 authMark`).toBe('docker:list')
    }
  })

  it('两条详情页 isHide 且需要 docker:inspect（不在后端菜单里）', () => {
    for (const p of ['/docker/container-detail/:id', '/docker/image-detail/:id']) {
      const r = routes.find((x) => x.path === p)
      expect(r, `缺少详情页 ${p}`).toBeTruthy()
      expect(r?.meta?.isHide).toBe(true)
      expect(r?.meta?.authMark).toBe('docker:inspect')
    }
  })

  it('详情页 path 不是列表页 path 的子路径（否则会被菜单前缀匹配成子页面）', () => {
    for (const r of routes.filter((x) => x.meta?.isHide)) {
      for (const p of MENU_PATHS) {
        expect(r.path.startsWith(`${p}/`), `${r.path} 落在 ${p} 之下`).toBe(false)
      }
    }
  })
})
