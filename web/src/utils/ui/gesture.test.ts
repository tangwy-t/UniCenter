import { describe, expect, it } from 'vitest'
import { isLeftSwipe, SWIPE_MIN_DISTANCE } from './gesture'

// 手势判定为纯函数：约束是"水平左滑超过阈值，且明显大于垂直位移"，
// 后者用于避免用户在菜单内纵向滚动时误触抽屉关闭。

describe('isLeftSwipe（移动端抽屉左滑关闭判定）', () => {
  it('水平左滑超过阈值且远大于垂直位移时返回 true', () => {
    expect(isLeftSwipe(-100, 10)).toBe(true)
    expect(isLeftSwipe(-SWIPE_MIN_DISTANCE, 0)).toBe(true)
  })

  it('水平位移不足阈值时返回 false（轻微抖动不关闭）', () => {
    expect(isLeftSwipe(-(SWIPE_MIN_DISTANCE - 1), 0)).toBe(false)
    expect(isLeftSwipe(-20, 0)).toBe(false)
  })

  it('右滑返回 false（方向不符）', () => {
    expect(isLeftSwipe(100, 0)).toBe(false)
  })

  it('纵向滚动优先时返回 false（避免菜单滚动误触）', () => {
    // 位移 100px 且垂直位移同为 100px：纵向成分占主导
    expect(isLeftSwipe(-100, 100)).toBe(false)
    // 典型的纵向滚动中的轻微水平漂移
    expect(isLeftSwipe(-70, 200)).toBe(false)
  })

  it('水平位移需严格超过垂直位移的 1.5 倍', () => {
    // -90/60 = 1.5，恰好等于比例要求 → 拒绝（需"明显大于"）
    expect(isLeftSwipe(-90, 60)).toBe(false)
    // -91/60 ≈ 1.52，严格超过 → 通过
    expect(isLeftSwipe(-91, 60)).toBe(true)
  })

  it('起点抬指（无位移）返回 false', () => {
    expect(isLeftSwipe(0, 0)).toBe(false)
  })
})