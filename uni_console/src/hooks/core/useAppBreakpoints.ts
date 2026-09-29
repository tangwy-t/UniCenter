import { type ComputedRef } from 'vue'
import { useBreakpoints } from '@vueuse/core'
import { BREAKPOINTS, type BreakpointName } from '@/config/breakpoints'

/**
 * 应用级断点判定（模块级单例 + 判定结果缓存）
 *
 * VueUse 的 `useBreakpoints` 每次调用 `smaller()` / `greaterOrEqual()` 都会新建一组
 * matchMedia 监听：在 computed、模板或循环中反复调用会产生重复订阅（只增不减）。
 * 这里按断点名缓存判定结果，使全应用共享同一份订阅。
 */
const breakpoints = useBreakpoints({ ...BREAKPOINTS })

const smallerCache = new Map<BreakpointName, ComputedRef<boolean>>()
const greaterOrEqualCache = new Map<BreakpointName, ComputedRef<boolean>>()

/**
 * 全局断点判定
 *
 * 断点阈值来自 `src/config/breakpoints.ts`（单一事实源），
 * 样式侧使用同名的 `src/assets/styles/core/_breakpoints.scss`。
 *
 * 用法：
 * ```ts
 * const { smaller } = useAppBreakpoints()
 * const isPhone = smaller('tablet') // 视口 < 768px（缓存过的 computed，可安全复用）
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
    }
  }
}
