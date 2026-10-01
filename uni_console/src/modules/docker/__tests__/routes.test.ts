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

/**
 * 总览路由与后端 v016 种子菜单**逐字一致**（`/docker`；跨仓比对同样由 Go 侧的
 * v016_seed_docker_overview_frontend_test.go 做）。注意它**不进** MENU_PATHS：
 * 下面「详情页不是列表页子路径」的守卫对 `/docker` 前缀天然会命中（详情页都是
 * /docker/xxx），那是路径层级的正常现象 —— MenuProcessor 按完整 path 精确匹配，
 * 不按前缀，平级菜单不受影响。
 */
const OVERVIEW_PATH = '/docker'

describe('docker 模块路由', () => {
  const routes = plugin.routes ?? []

  it('五条列表页 path 存在且不重复', () => {
    const paths = routes.map((r) => r.path)
    for (const p of MENU_PATHS) expect(paths, `缺少列表页路由 ${p}`).toContain(p)
    expect(new Set(paths).size).toBe(paths.length)
  })

  it('总览路由 /docker 逐字存在、菜单可见、权限与接口一致（v016 种子）', () => {
    const r = routes.find((x) => x.path === OVERVIEW_PATH)
    expect(r, '缺少总览路由 /docker（后端 v016 菜单会整条静默消失）').toBeTruthy()
    expect(r?.name).toBe('DockerOverview')
    expect(r?.meta?.title).toBe('Docker 总览')
    // docker:list：与 GET /docker/overview 的静态 perm 及菜单种子同源（割裂 = 看得到点不进）
    expect(r?.meta?.authMark).toBe('docker:list')
    // 菜单项必须可见（不设 isHide；隐藏路由不进侧边栏，v016 菜单就白种了）
    expect(r?.meta?.isHide).toBeFalsy()
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

  it('项目工作台路由（5b）：isHide + docker:inspect + name/组件落位', () => {
    const r = routes.find((x) => x.path === '/docker/projects/:name')
    expect(r, '缺少项目工作台路由 /docker/projects/:name').toBeTruthy()
    expect(r?.name).toBe('DockerProjectWorkspace')
    // 隐藏路由（不在后端菜单里）；authMark 与 compose:logs / 快照读取同档（docker:inspect）。
    expect(r?.meta?.isHide).toBe(true)
    expect(r?.meta?.authMark).toBe('docker:inspect')
    expect(typeof r?.component).toBe('function')
  })

  // 本守卫的原始动机（device 先例）：详情页别取列表页菜单 path 的**子路径** ——
  // 旧版菜单解析按前缀把子路径认成「子页面」。5b 的项目工作台**刻意**落在
  // `/docker/projects/:name`（Dockge 式深链：工作台地址就是项目在列表里的地址 + 名字），
  // 而 MenuProcessor 现按完整 path 精确匹配（routeMap.get(menu.path)），带参数的
  // path 撞不上菜单的 `/docker/projects` —— 实测列表页照常出菜单。故给它**显式豁免**：
  // 其余隐藏路由仍不许落在列表页 path 之下（新增豁免必须像这里一样写明理由）。
  const HIDDEN_SUBPATH_EXEMPT = ['/docker/projects/:name']

  it('详情页 path 不是列表页 path 的子路径（工作台显式豁免，见上方注释）', () => {
    for (const r of routes.filter((x) => x.meta?.isHide)) {
      if (HIDDEN_SUBPATH_EXEMPT.includes(r.path)) continue
      for (const p of MENU_PATHS) {
        expect(r.path.startsWith(`${p}/`), `${r.path} 落在 ${p} 之下`).toBe(false)
      }
    }
  })
})
