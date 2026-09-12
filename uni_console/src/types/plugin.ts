/**
 * 插件框架类型定义
 * 模块即插件：src/modules/<name>/ 下的 index.ts 默认导出 PluginManifest。
 *
 * ## 说明
 *
 * PluginManifest 此前还声明了 installStore / install / mount / unmount
 * 四个可选钩子,以及配套的 PluginContext(app、router、pinia、
 * registerComponent、registerDirective、registerCommand、addRoutes)。
 * 仓库内 13 个模块全部只声明 routes,没有任何模块使用这些钩子,
 * PluginManager.install()/mount() 遍历时每次都是空转,registerXxx 与
 * addRoutes 更是一处调用都没有。未被使用的扩展点会让维护者以为存在
 * 可用的插件能力,从而写出永不生效的代码(与 v-auth 读 meta.authList
 * 属同一类问题),故一并移除。
 *
 * 如需恢复插件生命周期,请连同**第一个真实使用方**一起提交。
 */
import type { AppRouteRecord } from './router'

/** 插件清单 */
export interface PluginManifest {
  name: string
  version: string
  enabled?: boolean
  routes?: AppRouteRecord[]
}
