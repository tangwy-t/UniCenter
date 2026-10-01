import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import plugin from '../index'

/** 三条带菜单的列表页 path 与后端 v015 种子菜单**逐字一致**（7a 最终面；跨仓比对
 *  由 Go 侧 v015_seed_docker_frontend_test.go 做）。 */
const MENU_PATHS = ['/docker/containers', '/docker/resources', '/docker/projects']

/** 7a 收敛退役的三个旧列表 path：路由已删、不做 redirect，站内也不许再指它们
 *（有活引用 = 深链 404）。清零由文末的源码扫描守卫钉住。 */
const RETIRED_PATHS = ['/docker/images', '/docker/volumes', '/docker/networks']

/** 7b 收敛退役的容器详情页 path：路由与 view 整体删除，深链改指
 * /docker/containers?host=&id=（统一表页用行桩打开抽屉）。同样无 redirect，
 * 活引用即 404。 */
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

  it('三条带菜单的列表页 path 存在且不重复', () => {
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

  it('镜像与存储路由（7a 收敛页）：path/name/标题/图标与菜单种子一致', () => {
    const r = routes.find((x) => x.path === '/docker/resources')
    expect(r, '缺少收敛页路由 /docker/resources（后端菜单会整条静默消失）').toBeTruthy()
    expect(r?.name).toBe('DockerResources')
    expect(r?.meta?.title).toBe('镜像与存储')
    // 图标与 v015 菜单种子一致（侧边栏/页签同源）；ri: 前缀整套离线内置。
    expect(r?.meta?.icon).toBe('ri:database-2-line')
    expect(r?.meta?.authMark).toBe('docker:list')
    expect(r?.meta?.isHide).toBeFalsy()
    expect(typeof r?.component).toBe('function')
  })

  it('三条列表页都需要 docker:list 权限', () => {
    for (const p of MENU_PATHS) {
      const r = routes.find((x) => x.path === p)
      expect(r?.meta?.authMark, `${p} 缺少 authMark`).toBe('docker:list')
    }
  })

  it('7a 退役的三个旧列表 path 不再是路由（收敛 = 整体删除，不做 redirect）', () => {
    const paths = routes.map((r) => r.path)
    for (const p of RETIRED_PATHS) {
      expect(
        paths,
        `旧 path ${p} 不应再注册为路由（深链已改指 /docker/resources?tab=…）`
      ).not.toContain(p)
    }
    // 旧路由名（DockerImages 等）同样退役：image-detail 的返回链路已改指 DockerResources，
    // 按旧名 push 会静默失败（vue-router 找不到名字时打警告并留在原页）。
    const names = routes.map((r) => r.name)
    for (const n of ['DockerImages', 'DockerVolumes', 'DockerNetworks']) {
      expect(names, `旧路由名 ${n} 不应残留`).not.toContain(n)
    }
  })

  it('7b 退役的容器详情 path 不再是路由（能力收敛到统一表页的抽屉）', () => {
    const paths = routes.map((r) => r.path)
    for (const p of RETIRED_7B_PATHS) {
      expect(
        paths,
        `旧 path ${p} 不应再注册为路由（深链已改指 /docker/containers?host=&id=）`
      ).not.toContain(p)
    }
    // 旧路由名同样退役：overview 异常表 / 工作台容器行 / 镜像详情关联容器都改指
    // DockerContainers，按旧名 push 会静默失败（留在原页）。
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

  // 本守卫的原始动机（device 先例）：详情页别取列表页菜单 path 的**子路径** ——
  // 旧版菜单解析按前缀把子路径认成「子页面」。5b 的项目工作台**刻意**落在
  // `/docker/projects/:name`（Dockge 式深链：工作台地址就是项目在列表里的地址 + 名字），
  // 而 MenuProcessor 现按完整 path 精确匹配（routeMap.get(menu.path)），带参数的
  // path 撞不上菜单的 `/docker/projects` —— 实测列表页照常出菜单。故给它**显式豁免**：
  // 其余隐藏路由仍不许落在列表页 path 之下（新增豁免必须像这里一样写明理由）。
  const HIDDEN_SUBPATH_EXEMPT = ['/docker/projects/:name']

  it('详情页 path 不是列表页 path 的子路径（工作台显式豁免，见上方注释）', () => {
    for (const r of routes.filter((x) => x.meta?.isHide)) {
      if (HIDDEN_SUBPATH_EXEMPT.includes(r.path)) continue
      for (const p of MENU_PATHS) {
        expect(r.path.startsWith(`${p}/`), `${r.path} 落在 ${p} 之下`).toBe(false)
      }
    }
  })
})

/* ── 旧 path 引用清零守卫（7a）────────────────────────────────────────
 * 三个旧列表页收敛为 /docker/resources 的 tab 后，旧 path **没有 redirect 兜底** ——
 * 任何活引用（router.push / 字符串跳转 / 路由表）都是深链 404。引用清零只能靠
 * 源码扫描钉住：路由表守卫（上文）只管 index.ts，这里扫整个模块的 .ts/.vue。
 *
 * 扫描前剥掉注释（模板/块/行）：注释里解释历史时提到旧 path 是正当的文档，
 * 不是活引用；`//` 的剥法避开 'https://' 里的斜杠对（路径字面量不会出现在那里）。
 * __tests__ 与点开头目录（.mimosa 等工具产物）不扫 —— 本文件自己就持有旧 path
 * 字面量（要拿它当禁止清单）。
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

describe('旧列表 path 引用清零（7a：无 redirect，活引用即死链）', () => {
  it('扫描不是空转：确实扫到了模块源码', () => {
    const files = sourceFiles(new URL('..', import.meta.url).pathname)
    expect(files.length).toBeGreaterThan(0)
    expect(files.some((f) => f.endsWith('views/resources.vue'))).toBe(true)
  })

  it('模块源码（剥注释后）不再出现三个旧 path', () => {
    const files = sourceFiles(new URL('..', import.meta.url).pathname)
    for (const file of files) {
      const code = stripComments(readFileSync(file, 'utf8'))
      for (const p of RETIRED_PATHS) {
        expect(
          code,
          `${file} 仍引用旧 path ${p}（深链会 404；应改指 /docker/resources?tab=…）`
        ).not.toContain(p)
      }
    }
  })
})

/* ── 7b 双表面清除的引用清零守卫 ────────────────────────────────────
 * ① 容器详情页（/docker/container-detail/:id）整体删除：path 与路由名的活引用即
 *   死链（同 7a 口径，剥注释后清零）。
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
          `${file} 仍引用 ${p}（详情深链会 404；应改指 /docker/containers?host=&id=）`
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
