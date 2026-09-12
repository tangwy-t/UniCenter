import type { PluginManifest } from '@/types/plugin'
import { PermCacheList, PermOnlineList, PermPprofList, PermServerList, PermSqlList } from '@/enums/permission'

const plugin: PluginManifest = {
  name: 'system-monitor',
  version: '1.0.0',
  routes: [
    {
      path: '/monitor/server',
      name: 'MonitorServer',
      component: () => import('./views/server.vue'),
      meta: {
        title: '服务器监控',
        icon: 'ri:server-line',
        authMark: PermServerList
      }
    },
    {
      path: '/monitor/cache',
      name: 'MonitorCache',
      component: () => import('./views/cache.vue'),
      meta: {
        title: '缓存管理',
        icon: 'ri:database-2-line',
        authMark: PermCacheList
      }
    },
    {
      path: '/monitor/sql',
      name: 'MonitorSql',
      component: () => import('./views/sql.vue'),
      meta: {
        title: 'SQL监控',
        icon: 'ri:terminal-box-line',
        authMark: PermSqlList
      }
    },
    {
      path: '/monitor/pprof',
      name: 'MonitorPprof',
      component: () => import('./views/pprof.vue'),
      meta: {
        title: 'pprof 性能分析',
        icon: 'ri:fire-line',
        authMark: PermPprofList
      }
    },
    {
      path: '/monitor/online',
      name: 'MonitorOnline',
      component: () => import('./views/online.vue'),
      meta: {
        title: '在线用户',
        icon: 'ri:user-follow-line',
        authMark: PermOnlineList
      }
    }
  ]
}

export default plugin
