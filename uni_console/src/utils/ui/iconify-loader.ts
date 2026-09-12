/**
 * 离线图标加载器
 *
 * 用于在内网环境下支持 Iconify 图标的离线加载。
 * 通过预加载图标集数据，避免运行时从 CDN 获取图标。
 *
 * 使用方式：
 * 1. 安装所需图标集：pnpm add -D @iconify-json/[icon-set-name]
 * 2. 在此文件中导入并注册图标集
 * 3. 在组件中使用：<ArtSvgIcon icon="ri:home-line" />
 *
 * @module utils/ui/iconify-loader
 * @author Art Design Pro Team
 */
import { addCollection } from '@iconify/vue'

// 离线图标数据
// 系统必要图标库 —— Remix Icon：
// 全量内置可保证任意 ri: 图标离线渲染（如菜单图标、页面内装饰图标），
// 避免内网/断网环境下 @iconify/vue 从 CDN 按需拉取失败导致图标偶发不显示。
import riIcons from '@iconify-json/ri/icons.json'

// 注册离线图标集
addCollection(riIcons)
