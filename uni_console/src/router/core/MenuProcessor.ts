/**
 * 菜单处理器
 *
 * 负责菜单数据的获取、过滤和处理
 *
 * @module router/core/MenuProcessor
 * @author Art Design Pro Team
 */

import type { AppRouteRecord } from '@/types/router'
import { useUserStore } from '@/store/modules/user'
import { useAppMode } from '@/hooks/core/useAppMode'
import { fetchGetMenuList } from '@/api/system-manage'
import { asyncRoutes } from '../routes/asyncRoutes'
import { RoutesAlias } from '../routesAlias'
import { formatMenuTitle } from '@/utils'

export class MenuProcessor {
  /**
   * 获取菜单数据
   */
  async getMenuList(): Promise<AppRouteRecord[]> {
    const { isFrontendMode } = useAppMode()

    let menuList: AppRouteRecord[]
    if (isFrontendMode.value) {
      menuList = await this.processFrontendMenu()
    } else {
      menuList = await this.processBackendMenu()
    }

    // 在规范化路径之前，验证原始路径配置
    this.validateMenuPaths(menuList)

    // 规范化路径（将相对路径转换为完整路径）
    return this.normalizeMenuPaths(menuList)
  }

  /**
   * 处理前端控制模式的菜单
   */
  private async processFrontendMenu(): Promise<AppRouteRecord[]> {
    const userStore = useUserStore()
    const perms = userStore.info?.permissions ?? []

    // 按权限码（meta.authMark）过滤菜单
    const filter = (menu: AppRouteRecord[]): AppRouteRecord[] =>
      menu.reduce((acc: AppRouteRecord[], item) => {
        const mark = item.meta?.authMark
        const visible = !mark || perms.includes(mark)
        if (!visible) return acc
        const next = { ...item }
        if (next.children?.length) next.children = filter(next.children)
        acc.push(next)
        return acc
      }, [])

    return this.filterEmptyMenus(filter([...asyncRoutes]))
  }

  /**
   * 处理后端控制模式的菜单
   *
   * 后端返回的是菜单树（Api.System.Menu），不含路由组件与元数据，
   * 这里将其转换为路由配置：
   * - 目录节点 → Layout 组件
   * - 菜单节点 → 按 path 匹配前端插件路由的组件
   * - 按钮节点 → 不进侧边栏，跳过
   * - 再按当前用户权限（perms）过滤，并追加插件的隐藏路由（如字典数据页，
   *   隐藏路由同样按 authMark 过滤）
   */
  private async processBackendMenu(): Promise<AppRouteRecord[]> {
    const list = await fetchGetMenuList()
    const userStore = useUserStore()
    const perms = userStore.info?.permissions ?? []

    // 插件路由：path → 组件加载函数，供后端菜单解析组件
    const routeMap = new Map<string, AppRouteRecord['component']>()
    for (const route of asyncRoutes) {
      const component = route.component as (() => Promise<unknown>) | undefined
      const hasComponent = typeof component === 'function'
      if (route.path && hasComponent) {
        routeMap.set(route.path, component)
      }
    }

    // 顶层目录的 path 可能推导撞车(如“日志管理”“服务监控”的子菜单都含 /monitor/*),
    // 撞车会同时导致:vue-router 重复注册同路径顶级路由、el-menu index 相同(展开状态互相串扰)。
    // 已占用过的顶级路径记录在此,冲突时改用唯一伪路径 /dir-<id>,仅用于路由与菜单内部。
    const usedTopPaths = new Set<string>()

    const convert = (menus: Api.System.Menu[], depth = 0): AppRouteRecord[] => {
      const result: AppRouteRecord[] = []
      for (const menu of menus) {
        // 按钮权限节点不进侧边栏
        if (menu.type === 'btn') continue

        // 目录节点：仅顶层目录用 Layout 包裹，嵌套目录作为穿透分组（无组件）
        if (menu.type === 'dir') {
          const children = convert(menu.children ?? [], depth + 1)
          if (!children.length) continue
          let dirPath = menu.path || ''
          // 后端顶层目录 path 为空，从首个子节点推导路径（如 /system、/monitor）
          if (depth === 0 && !dirPath && children.length) {
            const segment = (children[0].path || '').split('/').filter(Boolean)[0]
            if (segment) dirPath = `/${segment}`
          }
          if (depth === 0) {
            if (dirPath && usedTopPaths.has(dirPath)) {
              dirPath = `/dir-${menu.id}`
            }
            if (dirPath) usedTopPaths.add(dirPath)
          }
          result.push({
            path: dirPath,
            name: `dir-${menu.id}`,
            component: depth === 0 ? RoutesAlias.Layout : undefined,
            meta: {
              title: menu.name,
              icon: menu.icon || undefined,
              isHide: menu.visible === 0
            },
            children
          })
          continue
        }

        // 菜单节点：按权限过滤
        const mark = menu.perms
        if (mark && !perms.includes(mark)) continue

        // 无对应前端页面的菜单不显示
        const component = routeMap.get(menu.path)
        if (!component) continue

        result.push({
          path: menu.path,
          name: `menu-${menu.id}`,
          component: component as () => Promise<unknown>,
          meta: {
            title: menu.name,
            icon: menu.icon || undefined,
            isHide: menu.visible === 0,
            authMark: mark || undefined
          }
        })
      }
      return result
    }

    const converted = convert(list)

    // 追加插件隐藏路由(如字典数据页 /system/dict/:typeId、任务日志页
    // /system/job/:jobId/logs),它们不在后端菜单里但需要可访问。
    //
    // 必须按 authMark 过滤,与上方菜单节点同口径:此前无条件追加,
    // 使 `/system/dict/:typeId`(authMark: system:dict:data:list)与
    // `/system/job/:jobId/logs`(authMark: system:job:log:list)对
    // **所有登录用户**都注册为可达路由 —— 用户从侧边栏看不到,但直接
    // 输入 URL 即可进入页面。后端接口仍会 403,因此不是数据泄露,
    // 但前端宣称的授权状态与实际不符,且丢失了纵深防御。
    // 无 authMark 的隐藏路由(如 /system/user-center,仅需登录)照常追加。
    const hiddenRoutes = asyncRoutes
      .filter((route) => route.meta?.isHide)
      .filter((route) => {
        const mark = route.meta?.authMark
        return !mark || perms.includes(mark)
      })
      .map((route) => ({ ...route }))

    return this.filterEmptyMenus([...converted, ...hiddenRoutes])
  }

  /**
   * 递归过滤空菜单项
   */
  private filterEmptyMenus(menuList: AppRouteRecord[]): AppRouteRecord[] {
    return menuList
      .map((item) => {
        // 如果有子菜单，先递归过滤子菜单
        if (item.children && item.children.length > 0) {
          const filteredChildren = this.filterEmptyMenus(item.children)
          return {
            ...item,
            children: filteredChildren
          }
        }
        return item
      })
      .filter((item) => {
        // 如果定义了 children 属性（即使是空数组），说明这是一个目录菜单，应该保留
        if ('children' in item) {
          return true
        }

        // 如果有外链或 iframe，保留
        if (item.meta?.isIframe === true || item.meta?.link) {
          return true
        }

        // 如果有有效的 component，保留
        if (item.component && item.component !== '' && item.component !== RoutesAlias.Layout) {
          return true
        }

        // 其他情况过滤掉
        return false
      })
  }

  /**
   * 验证菜单列表是否有效
   */
  validateMenuList(menuList: AppRouteRecord[]): boolean {
    return Array.isArray(menuList) && menuList.length > 0
  }

  /**
   * 规范化菜单路径
   * 将相对路径转换为完整路径，确保菜单跳转正确
   */
  private normalizeMenuPaths(menuList: AppRouteRecord[], parentPath = ''): AppRouteRecord[] {
    return menuList.map((item) => {
      // 构建完整路径
      const fullPath = this.buildFullPath(item.path || '', parentPath)

      // 递归处理子菜单
      const children = item.children?.length
        ? this.normalizeMenuPaths(item.children, fullPath)
        : item.children

      const redirect = item.redirect || this.resolveDefaultRedirect(children)

      return {
        ...item,
        path: fullPath,
        redirect,
        children
      }
    })
  }

  /**
   * 为目录型菜单推导默认跳转地址
   */
  private resolveDefaultRedirect(children?: AppRouteRecord[]): string | undefined {
    if (!children?.length) {
      return undefined
    }

    for (const child of children) {
      if (this.isNavigableRoute(child)) {
        return child.path
      }

      const nestedRedirect = this.resolveDefaultRedirect(child.children)
      if (nestedRedirect) {
        return nestedRedirect
      }
    }

    return undefined
  }

  /**
   * 判断子路由是否可以作为默认落点
   */
  private isNavigableRoute(route: AppRouteRecord): boolean {
    return Boolean(
      route.path &&
      route.path !== '/' &&
      !route.meta?.link &&
      route.meta?.isIframe !== true &&
      route.component &&
      route.component !== ''
    )
  }

  /**
   * 验证菜单路径配置
   * 检测非一级菜单是否错误使用了 / 开头的路径
   */
  /**
   * 验证菜单路径配置
   * 检测非一级菜单是否错误使用了 / 开头的路径
   */
  private validateMenuPaths(menuList: AppRouteRecord[], level = 1): void {
    // 后端模式下菜单路径为绝对路径，属正常情况，无需校验相对路径约定
    const { isFrontendMode } = useAppMode()
    if (!isFrontendMode.value) return

    menuList.forEach((route) => {
      if (!route.children?.length) return

      const parentName = String(route.name || route.path || '未知路由')

      route.children.forEach((child) => {
        const childPath = child.path || ''

        // 跳过合法的绝对路径：外部链接和 iframe 路由
        if (this.isValidAbsolutePath(childPath)) return

        // 检测非法的绝对路径
        if (childPath.startsWith('/')) {
          this.logPathError(child, childPath, parentName, level)
        }
      })

      // 递归检查更深层级的子路由
      this.validateMenuPaths(route.children, level + 1)
    })
  }

  /**
   * 判断是否为合法的绝对路径
   */
  private isValidAbsolutePath(path: string): boolean {
    return (
      path.startsWith('http://') ||
      path.startsWith('https://') ||
      path.startsWith('/outside/iframe/')
    )
  }

  /**
   * 输出路径配置错误日志
   */
  private logPathError(
    route: AppRouteRecord,
    path: string,
    parentName: string,
    level: number
  ): void {
    const routeName = String(route.name || path || '未知路由')
    const menuTitle = route.meta?.title || routeName
    const suggestedPath = path.split('/').pop() || path.slice(1)

    console.error(
      `[路由配置错误] 菜单 "${formatMenuTitle(menuTitle)}" (name: ${routeName}, path: ${path}) 配置错误\n` +
        `  位置: ${parentName} > ${routeName}\n` +
        `  问题: ${level + 1}级菜单的 path 不能以 / 开头\n` +
        `  当前配置: path: '${path}'\n` +
        `  应该改为: path: '${suggestedPath}'`
    )
  }

  /**
   * 构建完整路径
   */
  private buildFullPath(path: string, parentPath: string): string {
    if (!path) return ''

    // 外部链接直接返回
    if (path.startsWith('http://') || path.startsWith('https://')) {
      return path
    }

    // 如果已经是绝对路径，直接返回
    if (path.startsWith('/')) {
      return path
    }

    // 拼接父路径和当前路径
    if (parentPath) {
      // 移除父路径末尾的斜杠，移除子路径开头的斜杠，然后拼接
      const cleanParent = parentPath.replace(/\/$/, '')
      const cleanChild = path.replace(/^\//, '')
      return `${cleanParent}/${cleanChild}`
    }

    // 没有父路径，添加前导斜杠
    return `/${path}`
  }
}
