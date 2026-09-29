/**
 * 触摸手势工具
 *
 * 提供移动端手势的纯函数判定，便于在组件中复用并单元测试。
 *
 * ## 主要功能
 *
 * - 判定"左滑"手势（用于关闭移动端抽屉）
 *
 * ## 使用场景
 *
 * - 移动端侧栏抽屉：左滑关闭
 *
 * @module utils/ui/gesture
 */

/** 判定为滑动手势所需的最小水平位移（px） */
export const SWIPE_MIN_DISTANCE = 60

/** 水平位移需超过垂直位移的倍数，避免与纵向滚动冲突 */
export const SWIPE_DIRECTION_RATIO = 1.5

/**
 * 判定一次触摸起止位移是否构成"左滑"手势
 *
 * 约束：水平位移超过阈值，且明显大于垂直位移——
 * 避免用户在菜单内纵向滚动时误触关闭。
 *
 * @param deltaX 水平位移（终点 - 起点，向左为负）
 * @param deltaY 垂直位移（终点 - 起点）
 * @returns 是否构成左滑
 */
export function isLeftSwipe(deltaX: number, deltaY: number): boolean {
  return (
    deltaX <= -SWIPE_MIN_DISTANCE && Math.abs(deltaX) > Math.abs(deltaY) * SWIPE_DIRECTION_RATIO
  )
}
