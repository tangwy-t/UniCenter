import type { PluginManifest } from '@/types/plugin'
import { PermFileList } from '@/enums/permission'

const plugin: PluginManifest = {
  name: 'system-file',
  version: '1.0.0',
  routes: [
    {
      path: '/system/file',
      name: 'SystemFile',
      component: () => import('./views/index.vue'),
      meta: {
        title: '文件管理',
        icon: 'ri:drive-line',
        authMark: PermFileList
      }
    }
  ]
}

export default plugin
