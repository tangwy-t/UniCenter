/**
 * 路由工具函数
 *
 * 提供路由相关的工具函数
 *
 * @module utils/router
 */
import { RouteLocationNormalized, RouteRecordRaw } from 'vue-router'
import AppConfig from '@/config'
import NProgress from 'nprogress'
import 'nprogress/nprogress.css'

/** 扩展的路由配置类型 */
export type AppRouteRecordRaw = RouteRecordRaw & {
  hidden?: boolean
}

/** 顶部进度条配置 */
export const configureNProgress = () => {
  NProgress.configure({
    easing: 'ease',
    speed: 600,
    showSpinner: false,
    parent: 'body'
  })
}

/**
 * 设置页面标题，根据路由元信息和系统信息拼接标题
 * @param to 当前路由对象
 */
export const setPageTitle = (to: RouteLocationNormalized): void => {
  const { title } = to.meta
  if (title) {
    setTimeout(() => {
      document.title = `${formatMenuTitle(String(title))} - ${AppConfig.systemInfo.name}`
    }, 150)
  }
}

/**
 * 格式化菜单标题
 * @param title 菜单标题(后端菜单/路由配置的中文面量)
 * @returns 原样标题(空串原样返回)
 */
export const formatMenuTitle = (title: string): string => {
  return title || ''
}
