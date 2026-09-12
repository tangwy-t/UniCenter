import type { PluginManifest } from '@/types/plugin'
import { PermJobList, PermJobLogList } from '@/enums/permission'

const plugin: PluginManifest = {
  name: 'system-job',
  version: '1.0.0',
  routes: [
    {
      path: '/system/job',
      name: 'SystemJob',
      component: () => import('./views/index.vue'),
      meta: {
        title: '定时任务',
        icon: 'ri:timer-2-line',
        authMark: PermJobList
      }
    },
    {
      path: '/system/job/:jobId/logs',
      name: 'SystemJobLogs',
      component: () => import('./views/logs.vue'),
      meta: {
        title: '任务日志',
        icon: 'ri:file-list-3-line',
        isHide: true,
        authMark: PermJobLogList
      }
    }
  ]
}

export default plugin
