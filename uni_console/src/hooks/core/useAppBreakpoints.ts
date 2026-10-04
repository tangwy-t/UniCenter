import { type ComputedRef, type Ref } from 'vue'
import { useBreakpoints, useMediaQuery } from '@vueuse/core'
import {
  BREAKPOINTS,
  HEIGHT_BREAKPOINTS,
  type BreakpointName,
  type HeightBreakpointName
} from '@/config/breakpoints'

/**
 * 应用级断点判定（模块级单例 + 判定结果缓存）
 *
 * VueUse 的 `useBreakpoints` 每次调用 `smaller()` / `greaterOrEqual()` 都会新建一组
 * matchMedia 监听：在 computed、模板或循环中反复调用会产生重复订阅（只增不减）。
 * 这里按断点名缓存判定结果，使全应用共享同一份订阅。
 *
 * 宽度轴（BREAKPOINTS）与高度轴（HEIGHT_BREAKPOINTS）是两张独立的表：
 * 前者走 useBreakpoints（只认 min/max-width），后者走 useMediaQuery 直写
 * max-height 查询 —— 两个轴各自缓存，互不干扰。
 */
const breakpoints = useBreakpoints({ ...BREAKPOINTS })

const smallerCache = new Map<BreakpointName, ComputedRef<boolean>>()
const greaterOrEqualCache = new Map<BreakpointName, ComputedRef<boolean>>()

/**
 * 高度轴判定（模块级单例：与宽度轴同一纪律 —— 判定结果全应用共享一份订阅）
 *
 * 为什么在**模块加载时**就建好全部档位，而不是沿用宽度轴那种「首次调用时缓存」：
 * useMediaQuery 的订阅挂在创建它的 effect scope 上 —— 若首次调用发生在某个
 * 组件 setup 里，该组件卸载时订阅会被一并回收，缓存下来的 ref 从此不再更新
 * （高度档位冻结在卸载前的值）。模块级创建没有 scope，订阅随应用生命周期存在。
 * 档位数量恒等于 HEIGHT_BREAKPOINTS 的条数（目前两档），订阅成本可忽略。
 */
const heightAtMostCache = new Map<HeightBreakpointName, Ref<boolean>>(
  (Object.keys(HEIGHT_BREAKPOINTS) as HeightBreakpointName[]).map((name) => [
    name,
    useMediaQuery(`(max-height: ${HEIGHT_BREAKPOINTS[name]}px)`)
  ])
)

/**
 * 全局断点判定
 *
 * 断点阈值来自 `src/config/breakpoints.ts`（单一事实源），
 * 样式侧使用同名的 `src/assets/styles/core/_breakpoints.scss`。
 *
 * 用法：
 * ```ts
 * const { smaller, heightAtMost } = useAppBreakpoints()
 * const isPhone = smaller('tablet') // 视口 < 768px（缓存过的 computed，可安全复用）
 * const isShortViewport = heightAtMost('short') // 视口高度 <= 640px（矮窗/手机横屏）
 * ```
 *
 * @module hooks/core/useAppBreakpoints
 */
export function useAppBreakpoints() {
  return {
    /** 视口宽度 < 断点 */
    smaller(name: BreakpointName): ComputedRef<boolean> {
      let matched = smallerCache.get(name)
      if (!matched) {
        matched = breakpoints.smaller(name)
        smallerCache.set(name, matched)
      }
      return matched
    },

    /** 视口宽度 >= 断点 */
    greaterOrEqual(name: BreakpointName): ComputedRef<boolean> {
      let matched = greaterOrEqualCache.get(name)
      if (!matched) {
        matched = breakpoints.greaterOrEqual(name)
        greaterOrEqualCache.set(name, matched)
      }
      return matched
    },

    /**
     * 视口高度 <= 断点（高度轴；矮视口/手机横屏的 JS 闸门都用它）
     *
     * 为什么不用 useBreakpoints：它只生成 min/max-**width** 查询，高度轴塞不进去。
     * useMediaQuery 的取值在视口高度变化（旋转、拉窗口）时自动更新 —— 依赖它的
     * computed（侧栏态）随之响应，keep-alive 的缓存页面也不会停在旧档。
     */
    heightAtMost(name: HeightBreakpointName): Ref<boolean> {
      return heightAtMostCache.get(name)!
    }
  }
}
