/**
 * usePageIcon - 页头图标
 *
 * 页面页头那个渐变色方块里的图标，取**当前路由的菜单图标**，取不到时回退到
 * 页面自带的常量。
 *
 * ## 为什么要跟菜单图标同源
 *
 * 侧边栏与页签渲染的都是 `route.meta.icon`——后端菜单模式下它就是
 * `sys_menu.icon`，运维在「系统管理 → 菜单管理」里改一次即可生效。页头此前
 * 是各页面写死的字面量，于是同一个页面出现两个不同图标（侧边栏一个、页头
 * 一个），而且改菜单图标永远改不动页头。改为同源后三处一起变。
 *
 * 前端菜单模式下 `meta.icon` 来自插件清单（modules/<name>/index.ts），同样是
 * 侧边栏所见的那一个——即两种模式都"页头 = 侧边栏所见图标"。
 *
 * ## 回退
 *
 * 菜单里没配图标（或该页不在菜单里，如隐藏路由）时用页面自带的常量，
 * 保证页头永远有图标，不会出现空白方块。
 *
 * ## 与离线图标的关系
 *
 * 页头图标自此来自数据库，而"随包发布"的图标是有限的（见 utils/ui/iconify-loader
 * 的 isOfflineIcon）：运维在菜单管理里手输一个未内置的图标名，页头在断网环境
 * 会空白。选择器已对这类名字给出提示，页头这里不再重复兜底——否则"改了这个
 * 图标为什么不生效"会变成更难查的问题。
 *
 * @module usePageIcon
 */
import { computed } from 'vue'
import { useRoute } from 'vue-router'

export function usePageIcon(fallback: string) {
  const route = useRoute()
  return computed(() => (route.meta?.icon as string) || fallback)
}
