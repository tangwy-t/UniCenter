/**
 * 权限指纹 —— 判断"登录后权限是否发生变化"的纯逻辑。
 *
 * 独立成模块(不 import 任何 store / router)**是刻意的**:
 * 本仓库 vitest 运行在 node 环境(无 DOM、无 localStorage),
 * `@/router/guards/beforeEach` 的导入链会经 router/index.ts 触达
 * createWebHashHistory 而崩溃。判定逻辑分离后即可被完整覆盖。
 *
 * 用途:管理员在另一会话中调整了当前用户的角色/菜单权限后,
 * 用户这边的路由表仍是旧的(新授予的菜单点不到、已撤销的仍可点击)。
 * 守卫据此检测变化并重建动态路由,无需用户手动刷新整页。
 */

/**
 * 计算权限集合的指纹。
 *
 * 排序后以 NUL 拼接:与顺序无关、与集合内容相关。
 * 用 NUL 而非逗号等可见字符作分隔符,避免权限标识本身含分隔符时
 * 两个不同集合产生相同指纹(如 ["a,b"] 与 ["a","b"])。
 */
export function permissionFingerprint(permissions: readonly string[] | undefined): string {
  return [...(permissions ?? [])].sort().join('\u0000')
}

/**
 * 判断权限是否相对基线发生了变化。
 *
 * @param baseline 上次记录的指纹;null 表示尚无基线
 *   (例如机制引入前就已登录),此时**不**视为变化 ——
 *   否则首次导航会触发一次无谓的路由重建,造成首屏抖动。
 */
export function permissionChanged(
  baseline: string | null,
  permissions: readonly string[] | undefined
): boolean {
  if (baseline === null) return false
  return permissionFingerprint(permissions) !== baseline
}
