/**
 * 路由全局前置守卫模块
 *
 * 提供完整的路由导航守卫功能
 *
 * ## 主要功能
 *
 * - 登录状态验证和重定向
 * - 动态路由注册和权限控制
 * - 菜单数据获取和处理（前端/后端模式）
 * - 用户信息获取和缓存
 * - 页面标题设置
 * - 工作标签页管理
 * - 进度条和加载动画控制
 * - 静态路由识别和处理
 * - 错误处理和异常跳转
 *
 * ## 使用场景
 *
 * - 路由跳转前的权限验证
 * - 动态菜单加载和路由注册
 * - 用户登录状态管理
 * - 页面访问控制
 * - 路由级别的加载状态管理
 *
 * ## 工作流程
 *
 * 1. 检查登录状态，未登录跳转到登录页
 * 2. 首次访问时获取用户信息和菜单数据
 * 3. 根据权限动态注册路由
 * 4. 设置页面标题和工作标签页
 * 5. 处理根路径重定向到首页
 * 6. 未匹配路由跳转到 404 页面
 *
 * @module router/guards/beforeEach
 * @author Art Design Pro Team
 */
import type { Router, RouteLocationNormalized, NavigationGuardNext } from 'vue-router'
import { nextTick } from 'vue'
import NProgress from 'nprogress'
import { useSettingStore } from '@/store/modules/setting'
import { useUserStore } from '@/store/modules/user'
import { useMenuStore } from '@/store/modules/menu'
import { setWorktab } from '@/utils/navigation'
import { setPageTitle } from '@/utils/router'
import { RoutesAlias } from '../routesAlias'
import { staticRoutes } from '../routes/staticRoutes'
import { loadingService } from '@/utils/ui'
import { useCommon } from '@/hooks/core/useCommon'
import { useWorktabStore } from '@/store/modules/worktab'
import { fetchGetUserInfo } from '@/api/auth'
import { ApiStatus } from '@/utils/http/status'
import { isHttpError } from '@/utils/http/error'
import { RouteRegistry, MenuProcessor, IframeRouteManager, RoutePermissionValidator } from '../core'
import { matchRouteInTree } from '../core/routePathMatch'
import { permissionChanged, permissionFingerprint } from '../core/permissionFingerprint'

// 路由注册器实例
let routeRegistry: RouteRegistry | null = null

// 菜单处理器实例
const menuProcessor = new MenuProcessor()

// 跟踪是否需要关闭 loading
let pendingLoading = false

// 路由初始化失败标记，防止死循环
// 一旦设置为 true，只有刷新页面或重新登录才能重置
let routeInitFailed = false

// 路由初始化进行中标记，防止并发请求
let routeInitInProgress = false

/**
 * 获取 pendingLoading 状态
 */
export function getPendingLoading(): boolean {
  return pendingLoading
}

/**
 * 重置 pendingLoading 状态
 */
export function resetPendingLoading(): void {
  pendingLoading = false
}

/**
 * 获取路由初始化失败状态
 */
export function getRouteInitFailed(): boolean {
  return routeInitFailed
}

/**
 * 重置路由初始化状态（用于重新登录场景）
 */
export function resetRouteInitState(): void {
  routeInitFailed = false
  routeInitInProgress = false
}

/**
 * 设置路由全局前置守卫
 */
export function setupBeforeEachGuard(router: Router): void {
  // 初始化路由注册器
  routeRegistry = new RouteRegistry(router)

  router.beforeEach(
    async (
      to: RouteLocationNormalized,
      from: RouteLocationNormalized,
      next: NavigationGuardNext
    ) => {
      try {
        await handleRouteGuard(to, from, next, router)
      } catch (error) {
        console.error('[RouteGuard] 路由守卫处理失败:', error)
        closeLoading()
        next({ name: 'Exception500' })
      }
    }
  )
}

/**
 * 关闭 loading 效果
 */
function closeLoading(): void {
  if (pendingLoading) {
    nextTick(() => {
      loadingService.hideLoading()
      pendingLoading = false
    })
  }
}

/**
 * 处理路由守卫逻辑
 */
async function handleRouteGuard(
  to: RouteLocationNormalized,
  from: RouteLocationNormalized,
  next: NavigationGuardNext,
  router: Router
): Promise<void> {
  const settingStore = useSettingStore()
  const userStore = useUserStore()

  // 启动进度条
  if (settingStore.showNprogress) {
    NProgress.start()
  }

  // 1. 检查登录状态
  if (!handleLoginStatus(to, userStore, next)) {
    return
  }

  // 2. 检查路由初始化是否已失败（防止死循环）
  if (routeInitFailed) {
    // 已经失败过，直接放行到错误页面，不再重试
    if (to.matched.length > 0) {
      next()
    } else {
      // 未匹配到路由，跳转到 500 页面
      next({ name: 'Exception500', replace: true })
    }
    return
  }

  // 3. 处理动态路由注册
  //
  // 除"尚未注册"外,还要处理**已注册但权限已变化**的情况:
  // 管理员在另一个会话中调整了当前用户的角色/菜单权限后,用户这边的
  // 路由表仍是旧的 —— 新授予的菜单点不到(路由不存在),已撤销的菜单
  // 仍可点击(接口返回 403)。此前只有整页刷新才能生效。
  //
  // 注意:必须**重新拉取** user/info 才能发现变化 —— store 里的权限是
  // 登录时写入的缓存,不会自己变。因此判定不能只比较内存中的旧指纹,
  // 否则条件恒为 false(这是个容易写错的点)。
  if (routeRegistry?.isRegistered() && userStore.isLogin) {
    if (await refreshPermissionsIfChanged()) {
      if (routeInitInProgress) {
        next(false)
        return
      }
      rebuildDynamicRoutes()
    }
  }

  if (!routeRegistry?.isRegistered() && userStore.isLogin) {
    // 防止并发请求（快速连续导航场景）
    if (routeInitInProgress) {
      // 正在初始化中，等待完成后重新导航
      next(false)
      return
    }
    await handleDynamicRoutes(to, next, router)
    return
  }

  // 4. 处理根路径重定向
  if (handleRootPathRedirect(to, next)) {
    return
  }

  // 5. 处理已匹配的路由
  if (to.matched.length > 0) {
    setWorktab(to)
    setPageTitle(to)
    next()
    return
  }

  // 6. 未匹配到路由，跳转到 404
  next({ name: 'Exception404' })
}

/**
 * 处理登录状态
 * @returns true 表示可以继续，false 表示已处理跳转
 */
function handleLoginStatus(
  to: RouteLocationNormalized,
  userStore: ReturnType<typeof useUserStore>,
  next: NavigationGuardNext
): boolean {
  // 已登录用户访问登录页，直接回到首页，避免停留在登录页（如 redirect 被异常累积的情况）
  if (userStore.isLogin && to.path === RoutesAlias.Login) {
    next({ path: '/', replace: true })
    return false
  }

  // 已登录或访问登录页或静态路由，直接放行
  if (userStore.isLogin || to.path === RoutesAlias.Login || isStaticRoute(to.path)) {
    return true
  }

  // 未登录且访问需要权限的页面，跳转到登录页并携带 redirect 参数
  userStore.logOut()
  next({
    name: 'Login',
    query: { redirect: to.fullPath }
  })
  return false
}

/**
 * 检查路由是否为静态路由。
 *
 * 委托给 routePathMatch 的共享实现:此前本文件内联了一份
 * "拼 RegExp 但不转义正则元字符"的路径匹配,与
 * RoutePermissionValidator.isDynamicRouteMatch(先转义)行为不一致 ——
 * 同一路径在两处可能得到不同判定。收敛到单一实现并配套单测。
 *
 * Exception404 作为 excludeNames 传入:catch-all 不应被视为可匿名访问的
 * 静态页,否则未登录时手动输入任意地址会直接落到 404 而非登录页。
 */
function isStaticRoute(path: string): boolean {
  return matchRouteInTree(path, staticRoutes, ['Exception404'])
}

/**
 * 处理动态路由注册
 */
async function handleDynamicRoutes(
  to: RouteLocationNormalized,
  next: NavigationGuardNext,
  router: Router
): Promise<void> {
  // 标记初始化进行中
  routeInitInProgress = true

  // 显示 loading
  pendingLoading = true
  loadingService.showLoading()

  try {
    // 1. 获取用户信息(并记录权限基线,供后续导航检测权限变化)
    await fetchUserInfo()
    rememberPermissionFingerprint(useUserStore().info?.permissions)

    // 2. 获取菜单数据
    const menuList = await menuProcessor.getMenuList()

    // 3. 验证菜单数据
    if (!menuProcessor.validateMenuList(menuList)) {
      throw new Error('获取菜单列表失败，请重新登录')
    }

    // 4. 注册动态路由
    routeRegistry?.register(menuList)

    // 5. 保存菜单数据到 store
    const menuStore = useMenuStore()
    menuStore.setMenuList(menuList)
    menuStore.addRemoveRouteFns(routeRegistry?.getRemoveRouteFns() || [])

    // 6. 保存 iframe 路由
    IframeRouteManager.getInstance().save()

    // 7. 验证工作标签页
    useWorktabStore().validateWorktabs(router)

    // 8. 静态路由不依赖菜单权限，初始化后直接恢复目标地址。
    if (isStaticRoute(to.path)) {
      routeInitInProgress = false
      next({
        path: to.path,
        query: to.query,
        hash: to.hash,
        replace: true
      })
      return
    }

    // 8. 验证目标路径权限
    // validatePath 仍返回 fallback path,但无权限时我们改跳 403 页面
    // (不再静默回首页),故此处只需 hasPermission。
    const { homePath } = useCommon()
    const { hasPermission } = RoutePermissionValidator.validatePath(
      to.path,
      menuList,
      homePath.value || '/'
    )

    // 初始化成功，重置进行中标记
    routeInitInProgress = false

    // 9. 重新导航到目标路由
    if (!hasPermission) {
      // 无权限访问:跳转到 403 页面,而不是静默回首页。
      //
      // 此前这里直接 replace 到 homePath 并只打一条 console.warn ——
      // 用户点击自己无权的菜单/链接后,看到的是"莫名其妙回到了首页",
      // 无从判断是权限问题还是页面出错;而 Exception403 页面与 /403 路由
      // 虽已定义,却全仓库无任何代码跳转过去,是死 UI。
      // 同时 404 由 to.matched.length 判定,与"无权限"语义不同,
      // 不应共用同一个用户可见结果。
      //
      // 带上 from 便于 403 页面展示"你尝试访问的地址"。
      console.warn(`[RouteGuard] 用户无权限访问路径: ${to.path}，已跳转到 403 页面`)
      next({
        name: 'Exception403',
        query: to.path === '/' ? undefined : { from: to.fullPath },
        replace: true
      })
    } else {
      // 有权限，正常导航
      next({
        path: to.path,
        query: to.query,
        hash: to.hash,
        replace: true
      })
    }
  } catch (error) {
    console.error('[RouteGuard] 动态路由注册失败:', error)

    // 关闭 loading
    closeLoading()

    // 401 错误：axios 拦截器已处理退出登录，取消当前导航
    if (isUnauthorizedError(error)) {
      // 重置状态，允许重新登录后再次初始化
      routeInitInProgress = false
      next(false)
      return
    }

    // 标记初始化失败，防止死循环
    routeInitFailed = true
    routeInitInProgress = false

    // 输出详细错误信息，便于排查
    if (isHttpError(error)) {
      console.error(`[RouteGuard] 错误码: ${error.code}, 消息: ${error.message}`)
    }

    // 跳转到 500 页面，使用 replace 避免产生历史记录
    next({ name: 'Exception500', replace: true })
  }
}

/**
 * 获取用户信息
 */
async function fetchUserInfo(): Promise<void> {
  const userStore = useUserStore()
  const data = await fetchGetUserInfo()
  userStore.setUserInfo(data)
  // 检查并清理工作台标签页（如果是不同用户登录）
  userStore.checkAndClearWorktabs()
}

/**
 * 权限指纹基线:用于判断"登录后权限是否发生了变化"。
 * 判定逻辑见 router/core/permissionFingerprint.ts(纯函数,可单测)。
 *
 * 基线在 handleDynamicRoutes 首次拉取用户信息后写入,此后每次导航
 * 通过 refreshPermissionsIfChanged 对比新拉取的权限。
 */
let lastPermissionFingerprint: string | null = null

/** 上次检查权限变化的时间戳与最小检查间隔(见 refreshPermissionsIfChanged)。 */
let lastPermissionCheckAt = 0
const PERMISSION_CHECK_INTERVAL_MS = 60_000

/** 记录当前权限指纹(登录/初始化拉取用户信息后调用)。 */
export function rememberPermissionFingerprint(permissions: readonly string[] | undefined): void {
  lastPermissionFingerprint = permissionFingerprint(permissions)
}

/**
 * 重新拉取用户信息,若权限相对基线发生变化则更新指纹并返回 true。
 *
 * 为什么要真的发请求:权限只在服务端变化,前端内存里的副本不会自己变,
 * 只比较旧副本等于永远检测不到。
 *
 * 失败时不抛错(网络抖动不应阻断导航),返回 false 走原有导航流程;
 * 权限仍以内存中的旧值为准,行为与改动前一致。
 */
async function refreshPermissionsIfChanged(): Promise<boolean> {
  // 节流:每次导航都打一次 user/info 会给每个页面切换增加一个往返,
  // 而权限变化的时效性要求并不高(管理员改权限后数十秒内生效即可)。
  // 60s 内的重复导航直接复用上次结论。
  const now = Date.now()
  if (now - lastPermissionCheckAt < PERMISSION_CHECK_INTERVAL_MS) {
    return false
  }
  lastPermissionCheckAt = now

  let data: Api.Auth.UserInfo
  try {
    data = await fetchGetUserInfo()
  } catch {
    return false
  }
  useUserStore().setUserInfo(data)

  if (!permissionChanged(lastPermissionFingerprint, data.permissions)) {
    return false
  }
  lastPermissionFingerprint = permissionFingerprint(data.permissions)
  return true
}

/**
 * 拆除并重建动态路由(权限变化后调用)。
 *
 * 必须**同步**拆完再让守卫继续:若异步等待,期间用户可能导航到
 * 已被撤销的路由上。unregister 是同步的(router.removeRoute),
 * 重建则复用原有的 handleDynamicRoutes 路径。
 */
function rebuildDynamicRoutes(): void {
  console.warn('[RouteGuard] 检测到用户权限已变化，重建动态路由')
  routeRegistry?.unregister()
  const menuStore = useMenuStore()
  menuStore.removeAllDynamicRoutes()
  menuStore.setMenuList([])
  resetRouteInitState()
  lastPermissionFingerprint = null
}

/**
 * 重置路由相关状态
 *
 * 用 generation 防竞态:固定延迟的 setTimeout 在用户「快速登出→重新登录」
 * 时,可能在新路由初始化完成后才触发,误拆新登录用户的动态路由。
 * 每次调用递增 generation,回调执行时若 generation 已变(期间有新的
 * reset 或登录初始化)则放弃,避免拆掉新状态。
 */
let resetGeneration = 0

export function resetRouterState(delay: number): void {
  const generation = ++resetGeneration
  setTimeout(() => {
    if (generation !== resetGeneration) {
      // 期间发生了新的 reset/登录初始化,放弃本次延迟重置。
      return
    }
    routeRegistry?.unregister()
    IframeRouteManager.getInstance().clear()

    const menuStore = useMenuStore()
    menuStore.removeAllDynamicRoutes()
    menuStore.setMenuList([])

    // 重置路由初始化状态，允许重新登录后再次初始化
    resetRouteInitState()
  }, delay)
}

/**
 * 处理根路径重定向到首页
 * @returns true 表示已处理跳转，false 表示无需跳转
 */
function handleRootPathRedirect(to: RouteLocationNormalized, next: NavigationGuardNext): boolean {
  if (to.path !== '/') {
    return false
  }

  const { homePath } = useCommon()
  if (homePath.value && homePath.value !== '/') {
    next({ path: homePath.value, replace: true })
    return true
  }

  return false
}

/**
 * 判断是否为未授权错误（401）
 */
function isUnauthorizedError(error: unknown): boolean {
  // HttpError.code 承载的是 **HTTP 状态码**(由 handleError 赋 statusCode),
  // 业务码另存 bizCode —— 故此处必须用 isHttpStatus,不能与 BizCode 混用。
  return isHttpError(error) && error.isHttpStatus(ApiStatus.unauthorized)
}
