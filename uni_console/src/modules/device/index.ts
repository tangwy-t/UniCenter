import type { PluginManifest } from '@/types/plugin'
import { PermDeviceList, PermDeviceQuery } from '@/enums/permission'

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
    },
    {
      // 总览页：与后端菜单 `/device/overview` **逐字一致**（v012 种子播种）。
      // 它是本模块的「聚合视图」入口，与列表页并列（都在侧边栏一级）。
      //
      // 权限码用 PermDeviceQuery 而不是 PermDeviceList：总览展示的是
      // 与详情页同一批指标的聚合（CPU/内存/磁盘/网络的时序与水位），
      // 其数据面与 `/devices/overview` 后端接口一致（那个接口用 device:query）。
      // 若这里写 PermDeviceList，会出现「有列表权限的用户看得到菜单，
      // 点进去却因接口 403 而满屏错误」的割裂。
      path: '/device/overview',
      name: 'DeviceOverview',
      component: () => import('./views/overview.vue'),
      meta: {
        title: '设备监控总览',
        icon: 'ri:dashboard-3-line',
        authMark: PermDeviceQuery
      }
    },
    {
      // 详情页**闭合** views/index.vue 的 openDetail 悬空引用：
      // 那里 `router.push({ name: 'DeviceDetail', params: { id: row.id } })`，
      // 而此前本模块只注册了 DeviceList —— 点「详情」会因 name 不存在而报错。
      // 这里注册的 name 与之**逐字一致**（DeviceDetail）。
      //
      // path 不取 `/device/index/:id` 而取 `/device/detail/:id`：前者会让
      // 列表页菜单的 path 前缀匹配出「子页面」（MenuProcessor 的菜单匹配以
      // 菜单 path 取组件），且 `/device/detail/:id` 语义更清晰。
      path: '/device/detail/:id',
      name: 'DeviceDetail',
      component: () => import('./views/detail.vue'),
      meta: {
        title: '设备详情',
        icon: 'ri:hard-drive-2-line',
        // 详情页不在后端菜单里（v008 只播种 /device/index），故必须在侧边栏
        // 隐藏；隐藏字段是 meta.isHide（不是 hidden，先例 /system/job/:jobId/logs）。
        isHide: true,
        // 隐藏路由按 authMark 过滤（MenuProcessor 与菜单节点同口径），
        // 无权限时连路由都不注册；权限码与后端 /devices/:id 的 PermDeviceQuery 一致。
        authMark: PermDeviceQuery
      }
    }
  ]
}

export default plugin
