/**
 * 插件管理器
 *
 * 通过 import.meta.glob 自动发现 src/modules/{name}/index.ts,
 * 收集各模块声明的路由,供 asyncRoutes 与菜单处理使用。
 *
 * ## 关于"插件生命周期"
 *
 * 此前这里还提供了 installStore / install / mount / unmount 四个生命周期
 * 钩子,外加一整套 PluginContext(app / router / pinia / registerComponent /
 * registerDirective / registerCommand / addRoutes)与 installed/mounted
 * 去重集合。但仓库内 13 个模块**全部只声明了 routes**,没有任何一个使用
 * 这些钩子:PluginManager.install() 与 mount() 遍历时每次都是空转,
 * addRoutes/registerXxx 更是一处调用都没有。
 *
 * 保留这套未被使用的抽象有实际代价:它让"模块"看起来可以注册组件、指令、
 * 全局命令和路由,维护者会据此写出永不生效的代码(与 v-auth 读
 * meta.authList 是同一类问题 —— 接了一个不存在的接口)。
 * 因此在此收敛为只保留真正被使用的 routes 收集能力;
 * 若将来确有需要,应连同第一个真实使用方一起恢复,而不是预先留桩。
 */
import type { AppRouteRecord } from '@/types/router'
import type { PluginManifest } from '@/types/plugin'

type ModuleIndex = { default?: PluginManifest }

const pluginModules = import.meta.glob<ModuleIndex>('../../modules/*/index.ts', { eager: true })

class PluginManager {
  private plugins: PluginManifest[]

  constructor() {
    this.plugins = Object.values(pluginModules)
      .map((m) => m.default)
      .filter((p): p is PluginManifest => !!p)
      .filter((p) => p.enabled !== false)
  }

  getPluginNames(): string[] {
    return this.plugins.map((p) => p.name)
  }

  collectRoutes(): AppRouteRecord[] {
    return this.plugins.flatMap((p) => p.routes ?? [])
  }
}

export const pluginManager = new PluginManager()
