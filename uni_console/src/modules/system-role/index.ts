import type { PluginManifest } from '@/types/plugin'
import { PermRoleList } from '@/enums/permission'

const plugin: PluginManifest = {
  name: 'system-role',
  version: '1.0.0',
  routes: [
    {
      path: '/system/role',
      name: 'SystemRole',
      component: () => import('./views/index.vue'),
      meta: {
        title: '角色管理',
        icon: 'ri:shield-user-line',
        authMark: PermRoleList
      }
    }
  ]
}

export default plugin
