import type { PluginManifest } from '@/types/plugin'
import { PermLogLoginList, PermLogOperationList } from '@/enums/permission'

const plugin: PluginManifest = {
  name: 'system-log',
  version: '1.0.0',
  routes: [
    {
      path: '/monitor/operlog',
      name: 'LogOperation',
      component: () => import('./views/operation.vue'),
      meta: {
        title: '操作日志',
        icon: 'ri:file-list-3-line',
        authMark: PermLogOperationList
      }
    },
    {
      path: '/monitor/logininfor',
      name: 'LogLogin',
      component: () => import('./views/login.vue'),
      meta: {
        title: '登录日志',
        icon: 'ri:login-circle-line',
        authMark: PermLogLoginList
      }
    }
  ]
}

export default plugin
