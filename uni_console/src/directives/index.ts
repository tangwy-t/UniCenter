import type { App } from 'vue'
import { setupAuthDirective, type AuthDirective } from './core/auth'
import { setupRippleDirective, type RippleDirective } from './business/ripple'
import { setupRolesDirective, type RolesDirective } from './core/roles'
import { perm } from './core/perm'

export function setupGlobDirectives(app: App) {
  app.directive('perm', perm) // v-perm 按钮级权限
  setupAuthDirective(app) // 权限指令
  setupRolesDirective(app) // 角色权限指令
  setupRippleDirective(app) // 水波纹指令
}

export type { AuthDirective, RippleDirective, RolesDirective }
