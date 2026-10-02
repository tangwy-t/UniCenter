import type { PluginManifest } from '@/types/plugin'
import { PermDockerInspect, PermDockerList, PermDockerManage } from '@/enums/permission'

/**
 * Docker 管理模块（路由挂载靠目录约定：src/modules/<name>/index.ts 默认导出
 * PluginManifest，由 framework/plugin/manager.ts 的 import.meta.glob 收集）。
 *
 * 四个可导航页面的 path 必须与后端 v015 种子菜单**逐字一致**（7a 后 v015 直种子
 * 最终菜单面：总览 /docker、容器 /docker/containers、镜像与存储 /docker/resources、
 * 项目 /docker/projects）—— 菜单模式下 MenuProcessor 用菜单 path 去插件路由表取
 * 组件，差一个字符该菜单就会被判定为「无对应前端页面」而**整条消失**（无报错、
 * 无日志）。该约束由 uni_core 的 v015_seed_docker_frontend_test.go 做字面量比对钉住。
 *
 * 7a 旧页收敛：/docker/images、/docker/volumes、/docker/networks 三个列表路由与
 * 三个旧 view **整体删除**（不做 redirect，站内深链一律改指 /docker/resources?tab=…）。
 *
 * 7b 详情面收敛：/docker/container-detail/:id 路由与 view **整体删除** —— 能力
 * （概览/日志/终端/环境）收敛到统一表页的 workload-drawer，深链一律改指
 * `/docker/containers?host=&id=`（抽屉外部打开模式）。旧 path / 路由名在模块
 * 源码里必须清零 —— 由 __tests__/routes.test.ts 的源码扫描守卫钉住。
 *
 * 8a/8b/8c 展示面一律整页路由（用户硬性设计原则：不用抽屉 —— 多分辨率适配）：
 * 三个抽屉（workload / create-container / task-center）整体删除，能力原样平移
 * 成正页面：`/docker/containers/:id`（容器详情）、`/docker/containers/create`
 * （创建容器）、`/docker/tasks`（任务中心）。7b 的 `?id=` 深链语义随之删除，
 * 站内深链一律改指详情页 path。
 */
const plugin: PluginManifest = {
  name: 'docker',
  version: '1.0.0',
  routes: [
    {
      // 总览页（控制塔）：与后端 v015 种子菜单 path **逐字一致**（`/docker`）。
      // 排在其余页面之前写（v015 的 Sort=0：点开目录先看到跨主机总览），
      // 但 path 上它是 `/docker`、其余页面是 `/docker/containers` —— MenuProcessor
      // 按完整 path 精确匹配取组件，不按前缀，平级不冲突。
      //
      // 权限码与 GET /docker/overview 的路由静态 perm 及种子菜单一致
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
      // 镜像与存储（7a 收敛页）：镜像 / 数据卷 / 网络三张旧列表页收敛为一个
      // tab 容器页。页面级共享一个主机上下文（切 tab 不换主机），当前 tab 记在
      // query（?tab=images|volumes|networks，深链/刷新可还原）。
      // 旧三页的 path 已删除：总览磁盘面板、镜像详情返回链路都改指本页 + tab。
      path: '/docker/resources',
      name: 'DockerResources',
      component: () => import('./views/resources.vue'),
      meta: { title: '镜像与存储', icon: 'ri:database-2-line', authMark: PermDockerList }
    },
    {
      path: '/docker/projects',
      name: 'DockerProjects',
      component: () => import('./views/projects.vue'),
      meta: { title: '项目', icon: 'ri:stack-line', authMark: PermDockerList }
    },
    {
      // 创建容器（8b 页面化）：抽屉的表单平移成整页（多分辨率适配：表单里那些
      // 加减行的编辑器在半宽抽屉里只能挤成一条缝）。
      //
      // **必须排在 `/docker/containers/:id` 之前**：两者是同层的参数段与静态段，
      // vue-router 按「静态段优先」打分（gin 的路由树同理），故 `create` 不会被
      // `:id` 吃掉。顺序与分数双保险由 __tests__/routes.test.ts 的匹配用例钉住。
      //
      // 隐藏路由（isHide，不进侧边栏）+ docker:manage：与「创建容器」入口按钮
      // 同一权限档（创建不删不停任何现存目标，manage 即够）。
      path: '/docker/containers/create',
      name: 'DockerContainerCreate',
      component: () => import('./views/container-create.vue'),
      meta: {
        title: '创建容器',
        icon: 'ri:add-circle-line',
        isHide: true,
        authMark: PermDockerManage
      }
    },
    {
      // 容器详情（8a 页面化）：被删的 workload-drawer 四个 Tab（概览 + stats 曲线 /
      // 日志 / 终端 / 环境）整页化。隐藏路由，不在后端菜单里（isHide + authMark
      // 过滤，权限码与 container:inspect 同档：docker:inspect）。
      //
      // path 取 `/docker/containers/:id`（列表页 path 的子路径形态）—— MenuProcessor
      // 按完整 path **精确匹配**取组件（routeMap.get(menu.path)），带参数的 path
      // 撞不上菜单的 `/docker/containers`，列表页照常工作（routes.test 里那一条
      // 「详情页不取列表页子路径」的守卫为本路由与创建页留了显式豁免，先例见
      // /docker/projects/:name）。
      //
      // query host 必带：容器是主机作用域的（入口链接都带 host；直接输 URL 时
      // host-context 落到第一台并写回 query）。`?tab=` 记当前 Tab（行菜单「日志」
      // 落日志 Tab 靠它，刷新/分享也能回到同一屏）。
      path: '/docker/containers/:id',
      name: 'DockerWorkloadDetail',
      component: () => import('./views/container-detail/index.vue'),
      meta: {
        title: '容器详情',
        icon: 'ri:ship-line',
        isHide: true,
        authMark: PermDockerInspect
      }
    },
    {
      // 任务中心（8c 页面化）：被删的 task-center-drawer 整页化（列表 / 过滤 /
      // 5 秒轮询 / 拉取进度内联全部原样平移）。
      //
      // **不加后端菜单种子**（本次裁定：它从总览 hero 与容器页 hero 入口进）——
      // 而 RouteRegistry 只注册菜单树里出现的路由：可见（非 isHide）且没有菜单
      // 种子的插件路由根本不会进 menuList，既注册不了、也过不了守卫的
      // 「路径在菜单权限内」检查（直接输 URL 404）。isHide 的语义恰好是
      // 「注册但不上侧边栏」，正是本页要的形态。authMark 与 GET /docker/tasks
      // 的静态 perm 同档（docker:list）。
      path: '/docker/tasks',
      name: 'DockerTasks',
      component: () => import('./views/tasks.vue'),
      meta: { title: '任务中心', icon: 'ri:task-line', isHide: true, authMark: PermDockerList }
    },
    {
      // 项目工作台（5b）：把项目升格为「变更工作台」（Dockge 的洞见嫁接）。
      // 隐藏路由，不在后端菜单里，isHide + authMark 过滤（权限码与 compose:logs /
      // 快照读取同档：docker:inspect）。
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
      path: '/docker/image-detail/:id',
      name: 'DockerImageDetail',
      component: () => import('./views/image-detail.vue'),
      meta: { title: '镜像详情', icon: 'ri:box-1-line', isHide: true, authMark: PermDockerInspect }
    }
  ]
}

export default plugin
