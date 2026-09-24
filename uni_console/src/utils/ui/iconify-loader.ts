/**
 * 离线图标加载器
 *
 * 用于在内网环境下支持 Iconify 图标的离线加载。
 * 通过预加载图标集数据，避免运行时从 CDN 获取图标。
 *
 * ## 内置了什么
 *
 * ① `ri:` —— **整套** Remix Icon 全量注册。这是刻意的：选择器允许运维随手敲
 *    任意 ri 名（见 art-icon-picker），全量内置才能保证"随手敲的名字"离线可用。
 * ② 其它集合 —— 只注册源码、后端菜单种子与选择器预设里**真正用到**的子集，
 *    由 `pnpm icons:sync` 生成（src/utils/ui/offline-icons.generated.ts）。
 *    全量内置现有 15 套要 +38MB，子集只要十几 KB —— 但代价是"新图标必须
 *    重新生成 + 重新构建前端"，这条边界由 `pnpm check:icons` 与选择器界面的
 *    提示兜住（否则运维会在界面上把菜单图标改成一个断网就空白的名字）。
 *
 * ## 使用方式
 *
 * 1. 直接用：<ArtSvgIcon icon="ri:home-line" />
 * 2. 要新增非 ri 图标：把名字写进代码/种子（或选择器预设清单），执行
 *    `pnpm add -D @iconify-json/<集合>`（该集合首次出现时）再 `pnpm icons:sync`
 * 3. 判断某个名字是否离线可用：isOfflineIcon(name)
 *
 * @module utils/ui/iconify-loader
 * @author Art Design Pro Team
 */
import { addCollection, iconLoaded } from '@iconify/vue'

// 离线图标数据
// 系统必要图标库 —— Remix Icon：
// 全量内置可保证任意 ri: 图标离线渲染（如菜单图标、页面内装饰图标），
// 避免内网/断网环境下 @iconify/vue 从 CDN 按需拉取失败导致图标偶发不显示。
import riIcons from '@iconify-json/ri/icons.json'
import { OFFLINE_ICON_COLLECTIONS, OFFLINE_ICON_NAMES } from './offline-icons.generated'

// 注册离线图标集
addCollection(riIcons)
for (const collection of OFFLINE_ICON_COLLECTIONS) addCollection(collection)

/** 随包发布的非 ri 图标名（生成物导出的清单，见 offline-icons.generated.ts）。 */
const bundledNames = new Set(OFFLINE_ICON_NAMES)

/**
 * 该图标名是否"随前端包发布"（内网/断网环境也能渲染）。
 *
 * 刻意**不用** iconLoaded 一把梭：它会因为 CDN 兜底拉取成功而变 true，
 * 于是"这个浏览器碰巧连得上外网"会被当成"离线可用"—— 而真正要回答的是
 * "部署到断网环境后还显不显示"。故：
 *   - `ri:` 走 iconLoaded：整套已注册，同步判定既准确又能顺带挡下拼写错的名字；
 *   - 其余集合走生成物清单：与网络、缓存状态无关，结论恒定。
 */
export function isOfflineIcon(name?: string): boolean {
  const n = (name ?? '').trim()
  if (!n) return false
  if (n.startsWith('ri:')) return iconLoaded(n)
  return bundledNames.has(n)
}
