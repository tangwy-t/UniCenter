/**
 * 路由转换器
 *
 * 负责将菜单数据转换为 Vue Router 路由配置。
 * 兼容两种组件形式：字符串路径（经 ComponentLoader 解析）与函数式懒加载组件（插件视图）。
 *
 * @module router/core/RouteTransformer
 * @author Art Design Pro Team
 */

import type { RouteRecordRaw } from 'vue-router'
import type { AppRouteRecord } from '@/types/router'
import { ComponentLoader } from './ComponentLoader'
import { IframeRouteManager } from './IframeRouteManager'

type ComponentOption = string | (() => Promise<any>) | undefined

interface ConvertedRoute extends Omit<RouteRecordRaw, 'children'> {
  id?: number
  children?: ConvertedRoute[]
  component?: RouteRecordRaw['component'] | (() => Promise<any>)
}

export class RouteTransformer {
  private componentLoader: ComponentLoader
  private iframeManager: IframeRouteManager

  constructor(componentLoader: ComponentLoader) {
    this.componentLoader = componentLoader
    this.iframeManager = IframeRouteManager.getInstance()
  }

  /**
   * 转换路由配置
   */
  transform(route: AppRouteRecord, depth = 0): ConvertedRoute {
    const { component, children, ...routeConfig } = route

    // 基础路由配置
    const converted: ConvertedRoute = {
      ...routeConfig,
      component: undefined
    }

    // 处理不同类型的路由
    if (route.meta.isIframe) {
      this.handleIframeRoute(converted, route, depth)
    } else if (this.isFirstLevelRoute(route, depth)) {
      this.handleFirstLevelRoute(converted, route, component)
    } else {
      this.handleNormalRoute(converted, component)
    }

    // 递归处理子路由
    if (children?.length) {
      converted.children = children.map((child) => this.transform(child, depth + 1))
    }

    return converted
  }

  /**
   * 判断是否为一级路由（需要 Layout 包裹）
   */
  private isFirstLevelRoute(route: AppRouteRecord, depth: number): boolean {
    return depth === 0 && (!route.children || route.children.length === 0)
  }

  /**
   * 解析组件：函数式组件直接使用，字符串路径经 ComponentLoader 解析
   */
  private resolveComponent(component: ComponentOption): (() => Promise<any>) | undefined {
    if (typeof component === 'function') return component
    if (typeof component === 'string') return this.componentLoader.load(component)
    return undefined
  }

  /**
   * 处理 iframe 类型路由
   */
  private handleIframeRoute(
    targetRoute: ConvertedRoute,
    sourceRoute: AppRouteRecord,
    depth: number
  ): void {
    if (depth === 0) {
      // 顶级 iframe：用 Layout 包裹
      targetRoute.component = this.componentLoader.loadLayout()
      targetRoute.path = this.extractFirstSegment(sourceRoute.path || '')
      targetRoute.name = ''

      targetRoute.children = [
        {
          ...sourceRoute,
          component: this.componentLoader.loadIframe()
        } as ConvertedRoute
      ]
    } else {
      // 非顶级（嵌套）iframe：直接使用 Iframe.vue
      targetRoute.component = this.componentLoader.loadIframe()
    }

    // 记录 iframe 路由
    this.iframeManager.add(sourceRoute)
  }

  /**
   * 处理一级菜单路由（无子路由的叶子页，自动用 Layout 包裹）
   */
  private handleFirstLevelRoute(
    converted: ConvertedRoute,
    route: AppRouteRecord,
    component: ComponentOption
  ): void {
    converted.component = this.componentLoader.loadLayout()
    converted.path = this.extractFirstSegment(route.path || '')
    converted.name = ''
    route.meta.isFirstLevel = true

    converted.children = [
      {
        ...route,
        component: this.resolveComponent(component)
      } as ConvertedRoute
    ]
  }

  /**
   * 处理普通路由
   */
  private handleNormalRoute(converted: ConvertedRoute, component: ComponentOption): void {
    converted.component = this.resolveComponent(component)
  }

  /**
   * 提取路径的第一段
   */
  private extractFirstSegment(path: string): string {
    const segments = path.split('/').filter(Boolean)
    return segments.length > 0 ? `/${segments[0]}` : '/'
  }
}
