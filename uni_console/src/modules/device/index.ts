import type { PluginManifest } from '@/types/plugin'
import { PermDeviceList } from '@/enums/permission'

/**
 * 设备管理模块（路由挂载靠目录约定：src/modules/<name>/index.ts 默认导出
 * PluginManifest，由 framework/plugin/manager.ts 的 import.meta.glob 收集）。
 *
 * 路径必须与后端 v008 种子菜单**逐字一致**（`Path: "/device/index"`）：
 * 后端菜单模式下，MenuProcessor 用菜单的 path 去插件路由表里取组件，
 * 差一个字符该菜单就会被判定为「无对应前端页面」而整条消失。
 */
const plugin: PluginManifest = {
  name: 'device',
  version: '1.0.0',
  routes: [
    {
      path: '/device/index',
      name: 'DeviceList',
      component: () => import('./views/index.vue'),
      meta: {
        title: '设备列表',
        icon: 'ri:hard-drive-2-line',
        authMark: PermDeviceList
      }
    }
  ]
}

export default plugin
