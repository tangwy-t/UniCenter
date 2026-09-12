import type { PluginManifest } from '@/types/plugin'
import { PermDeptList } from '@/enums/permission'

const plugin: PluginManifest = {
  name: 'system-dept',
  version: '1.0.0',
  routes: [
    {
      path: '/system/dept',
      name: 'SystemDept',
      component: () => import('./views/index.vue'),
      meta: {
        title: '部门管理',
        icon: 'ri:organization-chart',
        authMark: PermDeptList
      }
    }
  ]
}

export default plugin
