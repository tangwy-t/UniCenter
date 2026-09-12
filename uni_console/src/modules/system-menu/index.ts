import type { PluginManifest } from '@/types/plugin'
import { PermMenuList } from '@/enums/permission'

const plugin: PluginManifest = {
  name: 'system-menu',
  version: '1.0.0',
  routes: [
    {
      path: '/system/menu',
      name: 'SystemMenu',
      component: () => import('./views/index.vue'),
      meta: {
        title: '菜单管理',
        icon: 'ri:menu-line',
        authMark: PermMenuList
      }
    }
  ]
}

export default plugin
