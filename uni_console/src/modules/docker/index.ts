import type { PluginManifest } from '@/types/plugin'
import { PermDockerInspect, PermDockerList } from '@/enums/permission'

/**
 * Docker 管理模块（路由挂载靠目录约定：src/modules/<name>/index.ts 默认导出
 * PluginManifest，由 framework/plugin/manager.ts 的 import.meta.glob 收集）。
 *
 * 六个可导航页面的 path 必须与后端种子菜单**逐字一致**：五个列表页对 v015
 * （`/docker/containers` 等），总览页对 v016（`/docker`）—— 菜单模式下
 * MenuProcessor 用菜单 path 去插件路由表取组件，差一个字符该菜单就会被判定为
 * 「无对应前端页面」而**整条消失**（无报错、无日志）。该约束由 uni_core 的
 * v015_seed_docker_frontend_test.go 与 v016_seed_docker_overview_frontend_test.go
 * 做字面量比对钉住。
 */
const plugin: PluginManifest = {
  name: 'docker',
  version: '1.0.0',
  routes: [
    {
      // 总览页（控制塔）：与后端 v016 种子菜单 path **逐字一致**（`/docker`）。
      // 排在五个列表页之前写（v016 的 Sort=0：点开目录先看到跨主机总览），
      // 但 path 上它是 `/docker`、列表页是 `/docker/containers` —— MenuProcessor
      // 按完整 path 精确匹配取组件，不按前缀，平级不冲突。
      //
      // 权限码与 GET /docker/overview 的路由静态 perm 及 v016 菜单种子一致
      // （docker:list）：菜单可见性与接口鉴权必须同源，否则「看得到菜单、
      // 点进去满屏 403」。
      path: '/docker',
      name: 'DockerOverview',
      component: () => import('./views/overview.vue'),
      meta: { title: 'Docker 总览', icon: 'ri:ship-line', authMark: PermDockerList }
    },
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
      // 项目工作台（5b）：把项目升格为「变更工作台」（Dockge 的洞见嫁接）。
      // 隐藏路由，与 container-detail 同形态：不在后端菜单里，isHide + authMark
      // 过滤（权限码与 compose:logs / 快照读取同档：docker:inspect）。
      //
      // path 取 `/docker/projects/:name`（列表页 path 的子路径形态）——
      // MenuProcessor 按完整 path **精确匹配**取组件（routeMap.get(menu.path)），
      // 带参数的 path 撞不上菜单的 `/docker/projects`，列表页照常工作；
      // routes.test 里「详情页不取列表页子路径」的守卫为本路由留了显式豁免
      //（先例见 device 的 detail 路由注释 —— 那是历史匹配行为的防御，此处实测无碰撞）。
      //
      // query host 必带：项目是主机作用域的（入口链接都带 host；直接输 URL 时
      // host-context 落到第一台并写回 query）。
      path: '/docker/projects/:name',
      name: 'DockerProjectWorkspace',
      component: () => import('./views/project-workspace.vue'),
      meta: {
        title: '项目工作台',
        icon: 'ri:stack-line',
        isHide: true,
        authMark: PermDockerInspect
      }
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
