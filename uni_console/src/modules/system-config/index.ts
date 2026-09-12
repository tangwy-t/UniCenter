import type { PluginManifest } from '@/types/plugin'
import { PermConfigList } from '@/enums/permission'

const plugin: PluginManifest = {
  name: 'system-config',
  version: '1.0.0',
  routes: [
    {
      path: '/system/config',
      name: 'SystemConfig',
      component: () => import('./views/index.vue'),
      meta: {
        title: '参数配置',
        icon: 'ri:settings-3-line',
        authMark: PermConfigList
      }
    }
  ]
}

export default plugin
