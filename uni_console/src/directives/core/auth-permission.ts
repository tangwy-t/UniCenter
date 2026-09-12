/**
 * v-auth / v-perm 共用的权限判定逻辑。
 *
 * 独立成模块(不 import 任何 store)**是刻意的**:本仓库 vitest 运行在
 * node 环境(无 DOM、无 localStorage),而 `@/store/modules/user` 的导入
 * 链会经由 pinia persist 触达 localStorage 而使测试在导入期就崩溃。
 * 判定逻辑与副作用分离后,权限语义可以被完整覆盖,且不引入 jsdom 依赖。
 */

/**
 * 判断权限集合是否满足所需权限。
 *
 * 契约:
 * - `required` 为字符串或字符串数组(数组 = 满足其一即可,OR)
 * - 空值(空串/空数组/全空白项)视为"无需权限" → true
 * - 其余情况要求 required 中至少一项命中 perms
 */
export function hasAuthPermission(perms: readonly string[], required: string | string[]): boolean {
  const list = Array.isArray(required) ? required : [required]
  const wanted = list.filter((p) => typeof p === 'string' && p.length > 0)
  if (wanted.length === 0) return true
  return wanted.some((p) => perms.includes(p))
}

// 保存元素被隐藏前的原始 display 值,用 WeakMap 避免元素属性污染与内存泄漏。
const originalDisplay = new WeakMap<HTMLElement, string>()

/**
 * 按权限结果切换元素可见性(显示/隐藏)。
 *
 * 相比旧实现的 `removeChild` 物理移除,这里用 `display:none` 隐藏:
 * - 元素仍在 DOM 树中,权限恢复(updated 钩子)时可重新显示;
 * - 隐藏前记录原始 display,恢复时还原(而非硬编码 block/flex),
 *   兼容 inline / inline-block / flex / grid 等原始布局。
 *
 * @param el 目标元素
 * @param allowed 是否有权限(显示)
 */
export function setElementVisibility(el: HTMLElement, allowed: boolean): void {
  if (allowed) {
    const original = originalDisplay.get(el)
    if (original !== undefined) {
      el.style.display = original
      originalDisplay.delete(el)
    }
    return
  }
  // 首次隐藏时记录原始 display;已隐藏则跳过(幂等)。
  if (!originalDisplay.has(el)) {
    originalDisplay.set(el, el.style.display)
  }
  el.style.display = 'none'
}
