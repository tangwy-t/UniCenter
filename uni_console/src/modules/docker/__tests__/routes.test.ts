import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { createMemoryHistory, createRouter } from 'vue-router'
import plugin from '../index'

/** 五个带菜单的列表页 path 与后端 v015 种子菜单**逐字一致**（菜单平铺后的最终
 *  菜单面：容器/镜像/数据卷/网络/项目；跨仓比对由 Go 侧
 *  v015_seed_docker_frontend_test.go 做）。拆分切片：原收敛页 /docker/resources
 *  退役，镜像/数据卷/网络三条独立列表页复活（与被 7a 收敛前的旧 path 同名）。 */
const MENU_PATHS = [
  '/docker/containers',
  '/docker/images',
  '/docker/volumes',
  '/docker/networks',
  '/docker/projects'
]

/** 7b 收敛退役的容器详情页 path：路由与 view 整体删除，深链改指
 * /docker/containers?host=&id=（统一表页用行桩打开抽屉）。同样无 redirect，
 * 活引用即 404。（8a 页面化后深链又改指 /docker/containers/:id —— 但那个**旧**
 * path 仍然不许复活：它是另一条路由，只多了抽屉这一层壳。） */
const RETIRED_7B_PATHS = ['/docker/container-detail/:id']

/**
 * 总览路由与后端 v015 种子菜单**逐字一致**（`/docker`；跨仓比对同样由 Go 侧的
 * v015_seed_docker_frontend_test.go 做）。注意它**不进** MENU_PATHS：
 * 下面「详情页不是列表页子路径」的守卫对 `/docker` 前缀天然会命中（详情页都是
 * /docker/xxx），那是路径层级的正常现象 —— MenuProcessor 按完整 path 精确匹配，
 * 不按前缀，平级菜单不受影响。
 */
const OVERVIEW_PATH = '/docker'

describe('docker 模块路由', () => {
  const routes = plugin.routes ?? []

  it('带菜单的列表页 path 存在且不重复', () => {
    const paths = routes.map((r) => r.path)
    for (const p of MENU_PATHS) expect(paths, `缺少列表页路由 ${p}`).toContain(p)
    expect(new Set(paths).size).toBe(paths.length)
  })

  it('总览路由 /docker 逐字存在、菜单可见、权限与接口一致（v015 最终面）', () => {
    const r = routes.find((x) => x.path === OVERVIEW_PATH)
    expect(r, '缺少总览路由 /docker（后端菜单会整条静默消失）').toBeTruthy()
    expect(r?.name).toBe('DockerOverview')
    expect(r?.meta?.title).toBe('Docker 总览')
    // docker:list：与 GET /docker/overview 的静态 perm 及菜单种子同源（割裂 = 看得到点不进）
    expect(r?.meta?.authMark).toBe('docker:list')
    // 菜单项必须可见（不设 isHide；隐藏路由不进侧边栏，菜单就白种了）
    expect(r?.meta?.isHide).toBeFalsy()
  })

  it('三条拆分列表页路由的形状：path/name/标题/图标与菜单种子一致，可见且懒加载', () => {
    // 拆分切片：镜像/数据卷/网络从收敛页的三个 tab 升格为独立整页（菜单平铺），
    // path 与 v015 种子菜单**逐字一致**；图标沿用三条旧菜单的图标（box/硬盘/转发）。
    const shapes = [
      { path: '/docker/images', name: 'DockerImages', title: '镜像', icon: 'ri:box-1-line' },
      {
        path: '/docker/volumes',
        name: 'DockerVolumes',
        title: '数据卷',
        icon: 'ri:hard-drive-3-line'
      },
      {
        path: '/docker/networks',
        name: 'DockerNetworks',
        title: '网络',
        icon: 'ri:share-forward-line'
      }
    ]
    for (const s of shapes) {
      const r = routes.find((x) => x.path === s.path)
      expect(r, `缺少列表页路由 ${s.path}（后端菜单会整条静默消失）`).toBeTruthy()
      expect(r?.name, `${s.path} 的 name`).toBe(s.name)
      expect(r?.meta?.title, `${s.path} 的标题`).toBe(s.title)
      expect(r?.meta?.icon, `${s.path} 的图标`).toBe(s.icon)
      expect(r?.meta?.authMark, `${s.path} 的权限码`).toBe('docker:list')
      // 菜单项必须可见（不设 isHide；隐藏路由不进侧边栏，菜单就白种了）
      expect(r?.meta?.isHide).toBeFalsy()
      // 懒加载：component 是 () => import(...) 形态
      expect(typeof r?.component).toBe('function')
    }
  })

  it('带菜单的列表页都需要 docker:list 权限', () => {
    for (const p of MENU_PATHS) {
      const r = routes.find((x) => x.path === p)
      expect(r?.meta?.authMark, `${p} 缺少 authMark`).toBe('docker:list')
    }
  })

  it('7b 退役的容器详情 path 不再是路由（能力收敛到 8a 的整页容器详情）', () => {
    const paths = routes.map((r) => r.path)
    for (const p of RETIRED_7B_PATHS) {
      expect(
        paths,
        `旧 path ${p} 不应再注册为路由（深链已改指 /docker/containers/:id）`
      ).not.toContain(p)
    }
    // 旧路由名同样退役：overview 异常表 / 工作台容器行 / 镜像详情关联容器都改指
    // DockerWorkloadDetail，按旧名 push 会静默失败（留在原页）。
    const names = routes.map((r) => r.name)
    expect(names, '旧路由名 DockerContainerDetail 不应残留').not.toContain('DockerContainerDetail')
  })

  it('镜像详情页 isHide 且需要 docker:inspect（不在后端菜单里）', () => {
    for (const p of ['/docker/image-detail/:id']) {
      const r = routes.find((x) => x.path === p)
      expect(r, `缺少详情页 ${p}`).toBeTruthy()
      expect(r?.meta?.isHide).toBe(true)
      expect(r?.meta?.authMark).toBe('docker:inspect')
    }
  })

  it('项目工作台路由（5b）：isHide + docker:inspect + name/组件落位', () => {
    const r = routes.find((x) => x.path === '/docker/projects/:name')
    expect(r, '缺少项目工作台路由 /docker/projects/:name').toBeTruthy()
    expect(r?.name).toBe('DockerProjectWorkspace')
    // 隐藏路由（不在后端菜单里）；authMark 与 compose:logs / 快照读取同档（docker:inspect）。
    expect(r?.meta?.isHide).toBe(true)
    expect(r?.meta?.authMark).toBe('docker:inspect')
    expect(typeof r?.component).toBe('function')
  })

  /* ── 8a/8b/8c：三个抽屉页面化的新路由 ─────────────────────────────
   * 展示面一律整页路由（用户硬性设计原则：不用抽屉 —— 多分辨率适配）。三条路由的
   * 共同形态是**隐藏路由**（isHide：注册但不进侧边栏），理由分两种：
   *   - 详情/创建：子资源页（列表页的下钻目标），从来不在菜单里；
   *   - 任务中心：不加菜单种子（裁定：从各页入口进）—— 而 RouteRegistry 只注册
   *     菜单树里出现的路由，**可见（非 isHide）且无菜单种子的插件路由根本注册不上**
   *     （既进不了 menuList，也过不了守卫的「路径在菜单权限内」检查，直接输 URL 404）。
   *     isHide 的语义恰好是「注册 + 不上面板」，是本页唯一可用的形态。
   */
  it('8a 容器详情路由：/docker/containers/:id 隐藏 + docker:inspect + 组件落位', () => {
    const r = routes.find((x) => x.path === '/docker/containers/:id')
    expect(r, '缺少容器详情路由 /docker/containers/:id').toBeTruthy()
    expect(r?.name).toBe('DockerWorkloadDetail')
    expect(r?.meta?.isHide).toBe(true)
    // 与 container:inspect 指令、镜像详情同档（页面读的就是它）。
    expect(r?.meta?.authMark).toBe('docker:inspect')
    expect(typeof r?.component).toBe('function')
  })

  it('8b 创建容器路由：/docker/containers/create 隐藏 + docker:manage（与入口按钮同档）', () => {
    const r = routes.find((x) => x.path === '/docker/containers/create')
    expect(r, '缺少创建容器路由 /docker/containers/create').toBeTruthy()
    expect(r?.name).toBe('DockerContainerCreate')
    expect(r?.meta?.isHide).toBe(true)
    // 与「创建容器…」入口按钮同一权限档（创建不删不停任何现存目标）。
    expect(r?.meta?.authMark).toBe('docker:manage')
    expect(typeof r?.component).toBe('function')
  })

  it('8c 任务中心路由：/docker/tasks 隐藏注册 + docker:list（不加菜单种子）', () => {
    const r = routes.find((x) => x.path === '/docker/tasks')
    expect(r, '缺少任务中心路由 /docker/tasks').toBeTruthy()
    expect(r?.name).toBe('DockerTasks')
    // 与 GET /docker/tasks 的静态 perm 同档；入口（总览 hero/容器页 hero）也按它门控。
    expect(r?.meta?.authMark).toBe('docker:list')
    // 隐藏注册（不是菜单页）：menu path 期望集里没有它，MENU_PATHS 守卫不动。
    expect(r?.meta?.isHide).toBe(true)
    expect(MENU_PATHS).not.toContain('/docker/tasks')
    expect(typeof r?.component).toBe('function')
  })

  it('事件流详版页路由：/docker/events 隐藏注册 + docker:list（不加菜单种子，本波）', () => {
    const r = routes.find((x) => x.path === '/docker/events')
    expect(r, '缺少事件流详版页路由 /docker/events').toBeTruthy()
    expect(r?.name).toBe('DockerEvents')
    // 与两条端点（GET /docker/events 与 /docker/events/history）的静态 perm 同档；
    // 入口（总览活动流面板头部的「查看全部」）也按它门控。
    expect(r?.meta?.authMark).toBe('docker:list')
    // 隐藏注册（不是菜单页）：menu path 期望集里没有它，MENU_PATHS 守卫不动。
    expect(r?.meta?.isHide).toBe(true)
    expect(MENU_PATHS).not.toContain('/docker/events')
    expect(typeof r?.component).toBe('function')
  })

  /* ── 路由匹配顺序：静态段优先 ──────────────────────────────────────
   * `/docker/containers/create` 与 `/docker/containers/:id` 是同层的静态段与参数段，
   * 后者会把「create」吃成 id —— gin 的路由树与 vue-router 的打分都是**静态段优先**，
   * 但「两侧都一样」这件事必须被钉住：任何一侧的写法变了（顺序/前缀/正则），
   * 创建页都会静默变成「id 为 create 的容器详情」。这里用插件路由表起一个真
   * router（组件换成占位：本用例只关心匹配到哪条记录），把两条实际命中钉死。
   */
  describe('匹配顺序：静态段优先（创建页不会被 :id 吃掉）', () => {
    async function pushAndResolve(path: string) {
      const router = createRouter({
        history: createMemoryHistory(),
        // 只留 path/name（被测的就是它们），组件换占位避免拉起真实页面。
        routes: routes.map((r) => ({
          path: r.path,
          name: r.name,
          component: { template: '<div />' }
        }))
      })
      await router.push(path)
      await router.isReady()
      return router.currentRoute.value
    }

    it('/docker/containers/create 命中创建页', async () => {
      const cur = await pushAndResolve('/docker/containers/create')
      expect(cur.name).toBe('DockerContainerCreate')
    })

    it('/docker/containers/<id> 命中容器详情，id 落进 params', async () => {
      const cur = await pushAndResolve('/docker/containers/abcdef123456')
      expect(cur.name).toBe('DockerWorkloadDetail')
      expect(cur.params.id).toBe('abcdef123456')
    })

    it('列表页与任务中心照常命中（新路由没有抢走既有 path）', async () => {
      expect((await pushAndResolve('/docker/containers')).name).toBe('DockerContainers')
      expect((await pushAndResolve('/docker/tasks')).name).toBe('DockerTasks')
      // 事件流详版页同理：/docker/events 是静态段，与 /docker 总览不互相抢。
      expect((await pushAndResolve('/docker/events')).name).toBe('DockerEvents')
      expect((await pushAndResolve('/docker')).name).toBe('DockerOverview')
    })
  })

  // 本守卫的原始动机（device 先例）：详情页别取列表页菜单 path 的**子路径** ——
  // 旧版菜单解析按前缀把子路径认成「子页面」。两处**刻意**落在列表页 path 之下，
  // 而 MenuProcessor 现按完整 path 精确匹配（routeMap.get(menu.path)），带参数/带
  // 静态段的 path 都撞不上菜单的 `/docker/containers` —— 实测列表页照常出菜单：
  //   - 5b 的项目工作台 `/docker/projects/:name`（Dockge 式深链：工作台地址就是项目
  //     在列表里的地址 + 名字）；
  //   - 8a/8b 的容器详情与创建页（详情是容器的子资源，创建是容器集合上的动作 ——
  //     地址形态本来就该长这样，8a 前的 7b 时代反而是 `?id=` query 的将就）。
  // 故给它们**显式豁免**：其余隐藏路由仍不许落在列表页 path 之下（新增豁免必须像
  // 这里一样写明理由）。
  const HIDDEN_SUBPATH_EXEMPT = [
    '/docker/projects/:name',
    '/docker/containers/:id',
    '/docker/containers/create'
  ]

  it('详情页 path 不是列表页 path 的子路径（工作台显式豁免，见上方注释）', () => {
    for (const r of routes.filter((x) => x.meta?.isHide)) {
      if (HIDDEN_SUBPATH_EXEMPT.includes(r.path)) continue
      for (const p of MENU_PATHS) {
        expect(r.path.startsWith(`${p}/`), `${r.path} 落在 ${p} 之下`).toBe(false)
      }
    }
  })
})

/* ── 源码引用清零守卫的公共工具 ──────────────────────────────────────
 * 退役 path / 路由名 / 已被删组件名的活引用即死链或双表面，清零靠源码扫描钉住：
 * 路由表守卫（上文）只管 index.ts，这些守卫扫整个模块的 .ts/.vue。
 *
 * 扫描前剥掉注释（模板/块/行）：注释里解释历史时提到旧 path 是正当的文档，
 * 不是活引用；`//` 的剥法避开 'https://' 里的斜杠对（路径字面量不会出现在那里）。
 * __tests__ 与点开头目录（.mimosa 等工具产物）不扫 —— 各守卫文件自己就持有
 * 被禁字面量（要拿它们当禁止清单）。
 */
function stripComments(src: string): string {
  return src
    .replace(/<!--[\s\S]*?-->/g, '')
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/(^|[^:'"\\])\/\/[^\n]*/g, '$1')
}

function sourceFiles(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    if (name === '__tests__' || name.startsWith('.')) continue
    const p = join(dir, name)
    if (statSync(p).isDirectory()) sourceFiles(p, out)
    else if (name.endsWith('.ts') || name.endsWith('.vue')) out.push(p)
  }
  return out
}

/* ── 7b 双表面清除（8a 后仍生效）的引用清零守卫 ──────────────────────
 * ① 容器详情页（/docker/container-detail/:id）整体删除：path 与路由名的活引用即
 *   死链（同 7a 口径，剥注释后清零）。8a 页面化把详情能力搬进 /docker/containers/:id，
 *   但**没有**让旧 path 复活 —— 这条守卫因此原样留着（防「回滚成旧形态」）。
 * ② projects 列表页薄化：三层展开/确认弹窗/编辑器全部收敛到 project-workspace，
 *   projects.vue 里再出现 action-confirm / compose-editor 引用 = 能力又长回列表页
 *   （双表面复发）。用「旧引用出现即红灯」钉住。
 */
describe('7b 双表面清除：旧引用清零', () => {
  it('模块源码（剥注释后）不再出现容器详情 path 与路由名', () => {
    const files = sourceFiles(new URL('..', import.meta.url).pathname)
    for (const file of files) {
      const code = stripComments(readFileSync(file, 'utf8'))
      for (const p of [...RETIRED_7B_PATHS, 'DockerContainerDetail']) {
        expect(
          code,
          `${file} 仍引用 ${p}（详情深链会 404；应改指 /docker/containers/:id）`
        ).not.toContain(p)
      }
    }
  })

  it('薄索引 projects.vue 不再引用确认弹窗/编辑器（能力唯一实现是 project-workspace）', () => {
    const src = readFileSync(
      join(new URL('..', import.meta.url).pathname, 'views/projects.vue'),
      'utf8'
    )
    const code = stripComments(src)
    for (const marker of [
      'action-confirm',
      'DockerActionConfirm',
      'compose-editor',
      'ComposeEditor',
      'backup-history',
      'BackupHistory',
      'action-menu',
      'DockerActionMenu'
    ]) {
      expect(
        code,
        `projects.vue 仍引用 ${marker}：列表页是薄索引，该能力属于 project-workspace（双表面复发）`
      ).not.toContain(marker)
    }
  })
})

/* ── 8a/8b/8c 抽屉页面化：抽屉不得复活 ────────────────────────────────
 * 用户硬性设计原则：展示面一律整页路由、不用抽屉（多分辨率适配）。三个抽屉
 * （workload / create-container / task-center）整体删除，能力平移成三个页面；
 * 「删除」这件事只能靠源码扫描钉住 —— 重新 import 一个抽屉组件不会让任何行为
 * 用例变红（页面照常渲染），却会让「展示面唯一」这条纪律悄悄破功。
 * 同时把 7b 的 `?id=` 深链语义（在统一表页开抽屉）钉死：详情已是一条路由。
 */
describe('8a/8b/8c 抽屉页面化：抽屉与 ?id= 深链语义不得复活', () => {
  it('模块源码（剥注释后）不再引用三个抽屉组件', () => {
    const files = sourceFiles(new URL('..', import.meta.url).pathname)
    const markers = [
      'workload-drawer',
      'create-container-drawer',
      'task-center-drawer',
      'WorkloadDrawer',
      'CreateContainerDrawer',
      'TaskCenterDrawer'
    ]
    for (const file of files) {
      const code = stripComments(readFileSync(file, 'utf8'))
      for (const m of markers) {
        expect(
          code,
          `${file} 仍引用 ${m}：展示面一律整页路由（抽屉已删，能力在 views/ 的页面上）`
        ).not.toContain(m)
      }
    }
  })

  it('抽屉组件文件确实已删除（不留双表面）', () => {
    const files = sourceFiles(new URL('..', import.meta.url).pathname)
    for (const name of [
      'components/workload-drawer.vue',
      'components/create-container-drawer.vue',
      'components/task-center-drawer.vue'
    ]) {
      expect(
        files.some((f) => f.endsWith(name)),
        `${name} 还在：抽屉能力与整页能力并存 = 双表面`
      ).toBe(false)
    }
  })

  it('?id= 不再是容器详情的深链形态（行为层守卫在 workloads.test.ts：该 query 已无落点）', () => {
    // 这里只留一条文字级的锚：容器列表页不再读 query.id。
    const src = readFileSync(
      join(new URL('..', import.meta.url).pathname, 'views/containers.vue'),
      'utf8'
    )
    const code = stripComments(src)
    expect(
      code.includes('query.id') || code.includes('query?.id'),
      'containers.vue 仍读 query.id：详情深链应改指 /docker/containers/:id（列表页不再开详情）'
    ).toBe(false)
  })
})

/* ── 菜单页命名跳转清零守卫（V1 浏览器实测回归，2026-10-04）───────────
 * 机制：框架按**菜单树**注册路由（RouteRegistry 用 menu.path 取插件组件、按菜单
 * 记录注册），可见菜单页在 router 里的注册名是 `menu-<menuId>`；插件里声明的
 * name（DockerImages / DockerProjects / DockerContainers…）**根本没注册**
 * （浏览器内实测 router.hasRoute('DockerImages') = false）。对它们做
 * `push({ name })` 时 vue-router 只在 console 打一句 `No match for {...}`，
 * 页面纹丝不动 —— V1 实测中镜像详情「返回」、项目工作台「返回」、创建容器
 * 「取消」三处导航全断（隐藏路由 tasks/events/详情/创建/工作台才保名，命名跳转
 * 照常）。纪律：站内跳转一律 path 形态。
 *
 * 本守卫把这条纪律钉成源码级断言：菜单页名单**从插件路由表动态推导**
 * （meta.isHide 为假的那些 name），模块源码（views/ + components/ + utils/，
 * 剥注释）里不得出现对它们的 push({ name })。
 *   - 正则兼容多行写法（`push({\n  name: 'X'`，\s 跨行匹配）；
 *   - `defineOptions({ name: 'X' })` 是组件名声明、不含 `push(`，不会误伤；
 *   - 名字都是 [A-Za-z] 组成，无需转义即可安全嵌进正则。
 * 自检两道：名单推导不失焦（恰好是 /docker 六页的 name）+ 正则确实抓得多行、
 * 不误伤 defineOptions（防守卫自己腐烂成空转）。
 */
describe('菜单页命名跳转清零：push({ name }) 不得指向菜单页', () => {
  const routes = plugin.routes ?? []
  /** 动态推导：可见（非 isHide）菜单页的 name —— 这些名字在 router 里不存在。 */
  const menuNames = routes.filter((r) => !r.meta?.isHide).map((r) => String(r.name))
  const namedPushRe = (name: string) =>
    new RegExp(`push\\(\\s*\\{\\s*name\\s*:\\s*['"]${name}['"]`)

  it('守卫自检：名单恰好是 /docker 六页（动态推导不失焦）', () => {
    // 期望集由既有常量推导（总览 + 五条列表页），与动态名单同源不同路 —— 失焦即红。
    const expected = [...MENU_PATHS, OVERVIEW_PATH].map((p) => {
      const r = routes.find((x) => x.path === p)
      expect(r, `期望名单里的路由 ${p} 缺失`).toBeTruthy()
      return String(r?.name)
    })
    expect(new Set(menuNames)).toEqual(new Set(expected))
  })

  it('守卫自检：正则抓多行 push、不误伤 defineOptions（组件名声明）', () => {
    const re = namedPushRe('DockerImages')
    expect(
      re.test("void router.push({\n    name: 'DockerImages',\n    query: { host: 'h1' }\n  })\n"),
      '多行写法 push({\\n name: …) 没被抓住 —— 守卫是漏勺'
    ).toBe(true)
    expect(re.test("defineOptions({ name: 'DockerImages' })\n"), 'defineOptions 被误伤').toBe(
      false
    )
    // 数组 push（批量失败清单等）的 name 是变量引用，不会命中字符串字面量形态。
    expect(re.test('failures.push({ name: c.name, message })\n')).toBe(false)
  })

  it('模块源码（剥注释后）不存在对菜单页路由名的 push —— 一律 path 形态', () => {
    const files = sourceFiles(new URL('..', import.meta.url).pathname)
    let scanned = 0
    for (const file of files) {
      scanned++
      const code = stripComments(readFileSync(file, 'utf8'))
      for (const name of menuNames) {
        expect(
          namedPushRe(name).test(code),
          `${file} 仍有 push({ name: '${name}' })：菜单页注册名是 menu-<menuId>，` +
            `插件名没注册 —— 命名跳转静默失败（页面不动），一律改 path push`
        ).toBe(false)
      }
    }
    expect(scanned, '源码扫描是空转（一个文件都没扫到）').toBeGreaterThan(0)
  })
})
