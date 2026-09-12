import type { AppRouteRecord } from '@/types/router'
import { pluginManager } from '@/framework/plugin/manager'

/** 动态路由：由插件清单自动收集 */
export const asyncRoutes: AppRouteRecord[] = pluginManager.collectRoutes()
