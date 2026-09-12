import type { PluginManifest } from '@/types/plugin'
import { PermDictDataList, PermDictTypeList } from '@/enums/permission'

const plugin: PluginManifest = {
  name: 'system-dict',
  version: '1.0.0',
  routes: [
    {
      path: '/system/dict',
      name: 'SystemDict',
      component: () => import('./views/index.vue'),
      meta: {
        title: '字典管理',
        icon: 'ri:book-2-line',
        authMark: PermDictTypeList
      }
    },
    {
      path: '/system/dict/:typeId',
      name: 'SystemDictData',
      component: () => import('./views/data.vue'),
      meta: {
        title: '字典数据',
        icon: 'ri:list-check-3',
        isHide: true,
        authMark: PermDictDataList
      }
    }
  ]
}

export default plugin
