import type { PluginManifest } from '@/types/plugin'

const plugin: PluginManifest = {
  name: 'dashboard',
  version: '1.0.0',
  routes: [
    {
      path: '/dashboard/index',
      name: 'Dashboard',
      component: () => import('./views/index.vue'),
      meta: { title: '首页', icon: 'ri:home-5-line' }
    }
  ]
}

export default plugin
