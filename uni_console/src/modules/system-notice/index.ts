import type { PluginManifest } from '@/types/plugin'
import { PermNoticeList } from '@/enums/permission'

const plugin: PluginManifest = {
  name: 'system-notice',
  version: '1.0.0',
  routes: [
    {
      path: '/system/notice',
      name: 'SystemNotice',
      component: () => import('./views/index.vue'),
      meta: {
        title: '通知公告',
        icon: 'ri:notification-3-line',
        authMark: PermNoticeList
      }
    }
  ]
}

export default plugin
