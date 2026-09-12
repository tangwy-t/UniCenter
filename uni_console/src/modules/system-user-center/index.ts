import type { PluginManifest } from '@/types/plugin'

/**
 * 个人中心模块(路由 /system/user-center)
 *
 * - 不在侧边菜单展示(isHide):入口为顶栏头像下拉菜单(
 *   /system/user-center),与常规系统管理页区分。
 * - 仅需登录即可访问(无 authMark):接口层 GET/PUT /user/info、
 *   POST /user/password 同样只校验会话。
 */
const plugin: PluginManifest = {
  name: 'system-user-center',
  version: '1.0.0',
  routes: [
    {
      path: '/system/user-center',
      name: 'SystemUserCenter',
      component: () => import('./views/index.vue'),
      meta: {
        title: '个人中心',
        icon: 'ri:user-settings-line',
        isHide: true
      }
    }
  ]
}

export default plugin
