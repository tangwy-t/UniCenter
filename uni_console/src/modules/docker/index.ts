import type { PluginManifest } from '@/types/plugin'
import { PermDockerInspect, PermDockerList } from '@/enums/permission'

/**
 * Docker 管理模块（路由挂载靠目录约定：src/modules/<name>/index.ts 默认导出
 * PluginManifest，由 framework/plugin/manager.ts 的 import.meta.glob 收集）。
 *
 * 五个列表页的 path 必须与后端 v015 种子菜单**逐字一致**（`/docker/containers` 等）：
 * 菜单模式下 MenuProcessor 用菜单 path 去插件路由表取组件，差一个字符该菜单就会被
 * 判定为「无对应前端页面」而**整条消失**（无报错、无日志）。该约束由
 * uni_core 的 v015_seed_docker_frontend_test.go 做字面量比对钉住。
 */
const plugin: PluginManifest = {
  name: 'docker',
  version: '1.0.0',
  routes: [
    {
      path: '/docker/containers',
      name: 'DockerContainers',
      component: () => import('./views/containers.vue'),
      meta: { title: '容器', icon: 'ri:ship-line', authMark: PermDockerList }
    },
    {
      path: '/docker/images',
      name: 'DockerImages',
      component: () => import('./views/images.vue'),
      meta: { title: '镜像', icon: 'ri:box-1-line', authMark: PermDockerList }
    },
    {
      path: '/docker/volumes',
      name: 'DockerVolumes',
      component: () => import('./views/volumes.vue'),
      meta: { title: '数据卷', icon: 'ri:hard-drive-3-line', authMark: PermDockerList }
    },
    {
      path: '/docker/networks',
      name: 'DockerNetworks',
      component: () => import('./views/networks.vue'),
      meta: { title: '网络', icon: 'ri:share-forward-line', authMark: PermDockerList }
    },
    {
      path: '/docker/projects',
      name: 'DockerProjects',
      component: () => import('./views/projects.vue'),
      meta: { title: '项目', icon: 'ri:stack-line', authMark: PermDockerList }
    },
    {
      // 详情页路由**不取**列表页 path 的子路径：`/docker/containers/:id` 会被列表页
      // 菜单的 path 前缀匹配出「子页面」（先例与说明见 modules/device/index.ts）。
      // 它不在后端菜单里，故必须 isHide；隐藏路由按 authMark 过滤，权限码与
      // `/docker/hosts/:id/state` 及两个只读指令一致（docker:inspect）。
      path: '/docker/container-detail/:id',
      name: 'DockerContainerDetail',
      component: () => import('./views/container-detail.vue'),
      meta: { title: '容器详情', icon: 'ri:ship-line', isHide: true, authMark: PermDockerInspect }
    },
    {
      path: '/docker/image-detail/:id',
      name: 'DockerImageDetail',
      component: () => import('./views/image-detail.vue'),
      meta: { title: '镜像详情', icon: 'ri:box-1-line', isHide: true, authMark: PermDockerInspect }
    }
  ]
}

export default plugin
