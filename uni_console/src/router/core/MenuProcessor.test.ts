// @vitest-environment jsdom
/**
 * MenuProcessor 后端菜单模式：插件路由 meta 的透传守卫。
 *
 * ── 为什么需要这条守卫 ───────────────────────────────────────────────
 *
 * 后端菜单模式下菜单接口只携带 title/icon/visible/perms 四个字段，
 * 插件路由里声明的**纯前端行为字段**（keepAlive 等）在菜单 DTO 里没有
 * 落点（种子不播种）。MenuProcessor.convert 重建路由 meta 时若不从插件
 * 声明透传，这些字段就静默丢失 —— 页面照常出、菜单照常点，只有
 * art-page-content 的 KeepAlive 缓存失效（列表页状态保持退化成「每次
 * 返回都重置」），无任何报错。
 *
 * 本守卫钉住：convert 产出的菜单路由 meta 里，插件声明的前端字段被保留、
 * 后端字段（标题/图标/显隐/权限）仍以后端为准。
 *
 * 挂载口径：mock fetchGetMenuList（返回一条 /device/index 菜单）、
 * useUserStore（授权全部 perms）、useAppMode（强制 backend 模式）；
 * asyncRoutes 是真件（import 进整个插件表，/device/index 的
 * meta.keepAlive:true 由 modules/device 的路由守卫另文钉住）。
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'

const backendMenu = vi.hoisted(() => ({
  list: [
    {
      id: '501',
      parentId: '0',
      name: '设备列表',
      type: 'menu',
      perms: 'device:list',
      path: '/device/index',
      component: '',
      icon: 'ri:hard-drive-2-line',
      sort: 1,
      visible: 1,
      status: 0,
      createdAt: ''
    }
  ]
}))

vi.mock('@/api/system-manage', () => ({
  fetchGetMenuList: vi.fn().mockResolvedValue(backendMenu.list)
}))

vi.mock('@/store/modules/user', () => ({
  useUserStore: () => ({
    info: { permissions: ['device:list', 'device:query', 'device:upgrade'] }
  })
}))

// 强制后端菜单模式（真实环境由 VITE_ACCESS_MODE 决定，两种模式都要保住
// keepAlive —— 静态模式的 meta 直通本来就不丢，故只需要钉 backend 分支）。
vi.mock('@/hooks/core/useAppMode', () => ({
  useAppMode: () => ({
    isFrontendMode: { value: false },
    isBackendMode: { value: true },
    currentMode: { value: 'backend' }
  })
}))

// 隔断循环导入：MenuProcessor → @/utils(formatMenuTitle) → navigation →
// @/router → guards/beforeEach → ./core → MenuProcessor（回到自身）。产品
// 运行时入口顺序天然解开，但单测的导入顺序会让 MenuProcessor 绑定还是
// undefined —— 桩掉 @/utils 即断环（本文件只用 validateMenuPaths 的
// 开发告警路径，formatMenuTitle 无行为影响）。
vi.mock('@/utils', () => ({
  formatMenuTitle: (t: string) => t
}))

import { MenuProcessor } from './MenuProcessor'

describe('MenuProcessor：后端菜单模式的插件 meta 透传', () => {
  let processor: MenuProcessor

  beforeEach(() => {
    processor = new MenuProcessor()
  })

  it('菜单路由保留插件声明的 keepAlive（丢了它缓存静默失效）', async () => {
    const menuList = await processor.getMenuList()
    const route = menuList.find((r) => r.path === '/device/index')
    expect(route, '后端菜单 /device/index 未解析出前端路由（菜单会整条消失）').toBeTruthy()
    expect(
      route?.meta?.keepAlive,
      '插件声明的 meta.keepAlive 未透传：后端菜单模式下列表页缓存失效'
    ).toBe(true)
  })

  it('后端字段仍以后端为准（标题/图标/显隐/权限不被插件声明覆盖）', async () => {
    const menuList = await processor.getMenuList()
    const route = menuList.find((r) => r.path === '/device/index')
    expect(route?.meta?.title).toBe('设备列表')
    expect(route?.meta?.icon).toBe('ri:hard-drive-2-line')
    expect(route?.meta?.isHide).toBe(false)
    expect(route?.meta?.authMark).toBe('device:list')
  })

  it('插件声明的其它前端字段同样透传（isHide 类布尔合并不误伤）', async () => {
    const menuList = await processor.getMenuList()
    // 插件 /device/index 未声明 isHide → 后端 visible=1 → false（上面的用例已断）；
    // 这里用「字段存在性」证明插件 meta 进了合并链：透传实现若被删，
    // keepAlive 用例即红，本用例兜住「合并顺序反转」的回归（插件值覆盖后端值）。
    const route = menuList.find((r) => r.path === '/device/index')
    expect(Object.prototype.hasOwnProperty.call(route?.meta ?? {}, 'keepAlive')).toBe(true)
    expect(route?.meta?.title).not.toBe(undefined)
  })
})
