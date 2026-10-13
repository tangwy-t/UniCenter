import type { PluginManifest } from '@/types/plugin'
import { PermDockerInspect, PermDockerList, PermDockerManage } from '@/enums/permission'

/**
 * Docker 管理模块（路由挂载靠目录约定：src/modules/<name>/index.ts 默认导出
 * PluginManifest，由 framework/plugin/manager.ts 的 import.meta.glob 收集）。
 *
 * 六个可导航页面的 path 必须与后端 v015 种子菜单**逐字一致**（菜单平铺后的最终
 * 菜单面：总览 /docker、容器 /docker/containers、镜像 /docker/images、
 * 数据卷 /docker/volumes、网络 /docker/networks、项目 /docker/projects）——
 * 菜单模式下 MenuProcessor 用菜单 path 去插件路由表取组件，差一个字符该菜单就会
 * 被判定为「无对应前端页面」而**整条消失**（无报错、无日志）。该约束由 uni_core
 * 的 v015_seed_docker_frontend_test.go 做字面量比对钉住。
 *
 * 拆分切片（本波）：原「镜像与存储」收敛页 /docker/resources 与其 ElTabs 三 tab
 * **整体删除**（不做 redirect，`?tab=` 语义整体消灭，站内深链一律改指三个新页）——
 * 镜像/数据卷/网络各自平铺为独立整页列表（views/images.vue / volumes.vue /
 * networks.vue，路径与被 7a 收敛前的三条旧列表路由同名复活）。三个新页同时按
 * 「系统管理」形态收口：ArtSearchBar + art-table-card + ArtTableHeader 工具条
 * （计数/合计/动作全在 #left）。
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
      meta: { title: 'Docker 总览', icon: 'simple-icons:distrobox', authMark: PermDockerList }
    },
    {
      path: '/docker/containers',
      name: 'DockerContainers',
      component: () => import('./views/containers.vue'),
      meta: { title: '容器', icon: 'cib:docker', authMark: PermDockerList }
    },
    {
      // 镜像列表页（拆分切片）：原「镜像与存储」收敛页的镜像 tab 升格为独立整页
      // （菜单平铺），path 与 v015 种子菜单**逐字一致**。与容器页同档形态：
      // 跨主机聚合表（GET /docker/images）、`?host=` 深链作主机筛选初始值。
      path: '/docker/images',
      name: 'DockerImages',
      component: () => import('./views/images.vue'),
      meta: { title: '镜像', icon: 'pajamas:container-image', authMark: PermDockerList }
    },
    {
      // 数据卷列表页（拆分切片）：原收敛页的数据卷 tab 升格为独立整页。
      path: '/docker/volumes',
      name: 'DockerVolumes',
      component: () => import('./views/volumes.vue'),
      meta: { title: '数据卷', icon: 'ri:hard-drive-3-line', authMark: PermDockerList }
    },
    {
      // 网络列表页（拆分切片）：原收敛页的网络 tab 升格为独立整页。
      path: '/docker/networks',
      name: 'DockerNetworks',
      component: () => import('./views/networks.vue'),
      meta: { title: '网络', icon: 'material-symbols:graph-2', authMark: PermDockerList }
    },
    {
      path: '/docker/projects',
      name: 'DockerProjects',
      component: () => import('./views/projects.vue'),
      meta: { title: 'Compose', icon: 'catppuccin:docker-compose', authMark: PermDockerList }
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
        icon: 'cib:docker',
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
      meta: { title: '任务中心', icon: 'carbon:ibm-event-automation', isHide: true, authMark: PermDockerList }
    },
    {
      // 事件流详版页（本波）：活动流面板的整页形态 —— 历史查询（游标分页、服务端
      // 过滤）+ 实时流一张表。入口长在总览页活动流面板的头部（「查看全部」），
      // **不加后端菜单种子**（与任务中心同款裁定）。
      //
      // 形态与 /docker/tasks 逐条同源：RouteRegistry 只注册菜单树里出现的路由，
      // 可见（非 isHide）且无菜单种子的插件路由根本进不了 menuList（既注册不了、
      // 也过不了守卫的「路径在菜单权限内」检查 —— 直接输 URL 404）；isHide 的语义
      // 恰好是「注册但不上侧边栏」。authMark 与两条端点（GET /docker/events 与
      // /docker/events/history）的静态 perm 同档（docker:list）。
      path: '/docker/events',
      name: 'DockerEvents',
      component: () => import('./views/events.vue'),
      meta: { title: '事件流', icon: 'ri:pulse-line', isHide: true, authMark: PermDockerList }
    },
    {
      // 镜像版本一致性（漂移）视图（Wave 4b）：跨主机按 repoTag 聚合镜像摘要，
      // 报出每个标签的多数派/少数派分布 —— 排查「同名标签在不同机器上其实不是
      // 同一个镜像」。入口长在**镜像页工具条**（ArtButtonTable 图标钮），
      // **不加后端菜单种子**（与任务中心/事件流同款裁定：docker 六页平铺是既有
      // 裁决，v015 种子守卫钉住「六菜单」）。
      //
      // 形态与 /docker/tasks、/docker/events 逐条同源：RouteRegistry 只注册菜单树里
      // 出现的路由，可见（非 isHide）且无菜单种子的插件路由根本进不了 menuList
      //（既注册不了、也过不了守卫的「路径在菜单权限内」检查 —— 直接输 URL 404）；
      // isHide 的语义恰好是「注册但不上侧边栏」。authMark 与 GET /docker/image-drift
      // 的静态 perm 同档（docker:list）。
      path: '/docker/image-drift',
      name: 'DockerImageDrift',
      component: () => import('./views/image-drift.vue'),
      meta: {
        title: '版本一致性',
        icon: 'ri:git-branch-line',
        isHide: true,
        authMark: PermDockerList
      }
    },
    {
      // 全局跨资源检索（Wave 4d 收尾件）：一个关键字跨容器/镜像/数据卷/网络/Compose
      // 五类资源子串匹配（每组独立限量 + 逐组 truncated）。入口长在**总览 hero**
      // （图标钮），**不加后端菜单种子**（与任务中心/事件流/版本一致性同款裁定：
      // docker 六页平铺是既有裁决，v015 种子守卫钉住「六菜单」）。
      //
      // 形态与 /docker/tasks、/docker/events、/docker/image-drift 逐条同源：
      // RouteRegistry 只注册菜单树里出现的路由，可见（非 isHide）且无菜单种子的插件
      // 路由根本进不了 menuList（既注册不了、也过不了守卫的「路径在菜单权限内」检查
      // —— 直接输 URL 404）；isHide 的语义恰好是「注册但不上侧边栏」。authMark 与
      // GET /docker/search 的静态 perm 同档（docker:list）。
      path: '/docker/search',
      name: 'DockerSearch',
      component: () => import('./views/search.vue'),
      meta: { title: '全局检索', icon: 'ri:search-line', isHide: true, authMark: PermDockerList }
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
        title: 'Compose 工作台',
        icon: 'catppuccin:docker-compose',
        isHide: true,
        authMark: PermDockerInspect
      }
    },
    {
      path: '/docker/image-detail/:id',
      name: 'DockerImageDetail',
      component: () => import('./views/image-detail.vue'),
      meta: {
        title: '镜像详情',
        icon: 'pajamas:container-image',
        isHide: true,
        authMark: PermDockerInspect
      }
    }
  ]
}

export default plugin
