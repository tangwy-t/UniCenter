/**
 * 路由路径匹配工具 —— 单一事实源。
 *
 * 此前仓库里存在**两份**同形但语义不同的实现:
 *   - `router/core/RoutePermissionValidator.isDynamicRouteMatch` 先转义
 *     正则元字符再替换动态段(正确处理 `/a.b` 这类含 `.` 的路径);
 *   - `router/guards/beforeEach.isStaticRoute` 直接拼 `new RegExp`,
 *     未转义 —— `.`/`(`/`+` 等会被当作正则元字符,导致路径匹配结果
 *     与预期不符(`/a.b` 会把 `/axb` 也判定为命中)。
 *
 * 两份实现的存在让"路径是否命中"在不同调用点可能得到不同答案,
 * 因此收敛到本模块,并配套单测。
 */

/**
 * 把路由路径模式编译为正则表达式。
 *
 * 支持的语法:
 * - `:param`  → 匹配单个路径段(不含 `/`)
 * - `*`       → 匹配任意字符(含 `/`),用于 catch-all
 * - 其余字符  → 字面量匹配(正则元字符会被转义)
 */
export function compileRoutePattern(routePath: string): RegExp {
  // 先整体转义,再把转义后的占位符还原为动态段正则。
  const escaped = routePath.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  // 转义后 `:param` 不变(: 不是元字符),`*` 变成 `\*`。
  const pattern = escaped.replace(/:[^/]+/g, '[^/]+').replace(/\\\*/g, '.*')
  return new RegExp(`^${pattern}$`)
}

/**
 * 判断目标路径是否匹配给定的路由路径模式。
 * 非动态路径(不含 `:` 与 `*`)走字符串精确比较,避免无谓的正则开销。
 */
export function matchRoutePath(targetPath: string, routePath: string): boolean {
  if (!routePath.includes(':') && !routePath.includes('*')) {
    return routePath === targetPath
  }
  return compileRoutePattern(routePath).test(targetPath)
}

/**
 * 判断目标路径是否命中路由树中的任意节点(含子路由)。
 *
 * @param excludeNames 命中时需要跳过的路由名(如 404 catch-all ——
 *   它不应被视为"可匿名访问的静态页",否则未登录时手动输入任意地址
 *   会直接落到 404 而不是登录页)。
 */
export function matchRouteInTree(
  targetPath: string,
  routes: readonly RouteLike[],
  excludeNames: readonly string[] = []
): boolean {
  for (const route of routes) {
    if (route.name && excludeNames.includes(String(route.name))) {
      continue
    }
    if (route.path && matchRoutePath(targetPath, route.path)) {
      return true
    }
    if (route.children?.length && matchRouteInTree(targetPath, route.children, excludeNames)) {
      return true
    }
  }
  return false
}

/** 匹配所需的最小路由形状(避免与具体路由类型耦合)。 */
export interface RouteLike {
  path?: string
  name?: string | symbol
  children?: readonly RouteLike[]
}
