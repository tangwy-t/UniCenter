import { describe, expect, it } from 'vitest'
import { hasAuthPermission } from './auth-permission'

// 本仓库 vitest 运行在 node 环境(无 DOM),因此针对 v-auth 的判定逻辑
// 做纯函数测试,而非挂载指令断言 DOM。
//
// 历史缺陷:v-auth 读取 router.currentRoute.value.meta.authList,而全仓库
// 无任何代码写入 meta.authList(`|| []` 兜底使权限集合恒为空),
// hasPermission 恒为 false —— 任何使用 v-auth 的元素都会被无条件移除,
// 无报错、无提示。下面的断言覆盖了"持有权限时应保留"这一此前必然失败的情形。
describe('v-auth 权限判定 hasAuthPermission', () => {
  it('持有该权限 → 保留元素', () => {
    expect(hasAuthPermission(['system:user:add'], 'system:user:add')).toBe(true)
  })

  it('不持有该权限 → 移除元素', () => {
    expect(hasAuthPermission(['system:user:list'], 'system:user:add')).toBe(false)
  })

  it('无任何权限 → 移除元素', () => {
    expect(hasAuthPermission([], 'system:user:add')).toBe(false)
  })

  it('数组形式满足其一即可(OR)', () => {
    expect(hasAuthPermission(['system:user:edit'], ['system:user:add', 'system:user:edit'])).toBe(
      true
    )
  })

  it('数组形式均不满足 → 移除元素', () => {
    expect(hasAuthPermission(['system:user:list'], ['system:user:add', 'system:user:edit'])).toBe(
      false
    )
  })

  it('空数组视为无需权限 → 保留元素', () => {
    expect(hasAuthPermission([], [])).toBe(true)
  })

  it('空字符串视为无需权限 → 保留元素', () => {
    expect(hasAuthPermission([], '')).toBe(true)
  })

  it('权限集合中多余的权限不影响判定', () => {
    expect(hasAuthPermission(['a', 'b', 'c'], 'b')).toBe(true)
  })
})
