/**
 * v-auth 权限指令（与 v-perm 同源的别名）
 *
 * 基于**用户权限标识**控制 DOM 元素的显示和隐藏:
 * 用户不具备所声明的任一权限时,元素从 DOM 中移除。
 *
 * ## 与 v-perm 的关系
 *
 * `v-auth` 与 `v-perm` 现在是同一套逻辑的两个名字,权限来源同为
 * `useUserStore().info.permissions`(由后端 /user/info 下发)。
 * 保留两个名字仅为兼容既有写法;新代码建议统一用 `v-perm`。
 *
 * ## 主要功能
 *
 * - 权限验证 - 按用户权限标识集合判断
 * - 多权限支持 - 数组表示"满足其一即可"(OR)
 * - DOM 控制 - 无权限时移除元素,而非隐藏
 *
 * ## 使用示例
 *
 * ```vue
 * <el-button v-auth="'system:user:add'">新增</el-button>
 * <el-button v-auth="['system:user:edit', 'system:user:add']">编辑</el-button>
 * ```
 *
 * ## 注意事项
 *
 * - 无权限时隐藏元素(display:none),而非物理移除 —— 元素仍在 DOM 树中,
 *   权限变化(updated 钩子)时可响应式恢复显示;旧实现 removeChild 一旦移除
 *   便无法恢复。
 * - 空值(空串/空数组)视为"无需权限",元素保留(与 v-perm 一致)
 *
 * @module directives/auth
 */

import { useUserStore } from '@/store/modules/user'
import { hasAuthPermission, setElementVisibility } from './auth-permission'
import { App, Directive, DirectiveBinding } from 'vue'

export type AuthDirective = Directive<HTMLElement, string | string[]>

function checkAuthPermission(el: HTMLElement, binding: DirectiveBinding<string | string[]>): void {
  const perms = useUserStore().info?.permissions ?? []
  setElementVisibility(el, hasAuthPermission(perms, binding.value))
}

const authDirective: AuthDirective = {
  mounted: checkAuthPermission,
  updated: checkAuthPermission
}

export function setupAuthDirective(app: App): void {
  app.directive('auth', authDirective)
}
