// 读源文件断言（样式 token 单一事实源守卫，参照 src/config/breakpoints.test.ts 的
// 「读源文件 + 字符串级断言」范式；不测真实色值 —— 色值由 QA 静态计算口径验收）。
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

// secondary 灰槽（QA 批：EP 浅色 #909399 对白底 3.08:1 过不了 4.5:1 线）是
// 「aa-text.scss 定义 token + el-ui.scss 总闸收口」两半合起来的机制，两个文件
// 各改各的都不会让现有测试变红（token 没人引用 / 总闸引用不存在的 token 都
// 只在真机上表现为次要文字变浅/回退），这里钉住两半的契约：
//   ① aa-text.scss 必须定义 --aa-secondary-text（处方唯一持有处）；
//   ② el-ui.scss 必须有 html:root:not(.dark) 前缀的 --el-text-color-secondary
//      覆盖且引用该 token（placeholder 先例的总闸姿势）；
//   ③ 总闸限定浅色 —— 暗色 EP 自带值本就达标，必须 :not(.dark) 排除防回归。
const aaText = readFileSync(fileURLToPath(new URL('../aa-text.scss', import.meta.url)), 'utf8')
const elUi = readFileSync(fileURLToPath(new URL('../el-ui.scss', import.meta.url)), 'utf8')

describe('secondary 灰槽 AA token 单一事实源（aa-text.scss ↔ el-ui.scss）', () => {
  it('aa-text.scss 定义了 --aa-secondary-text', () => {
    expect(aaText).toMatch(/--aa-secondary-text\s*:\s*color-mix\(/)
  })

  it('el-ui.scss 以 html:root:not(.dark) 总闸覆盖 --el-text-color-secondary 并引用该 token', () => {
    // html:root:not(.dark) 块内含对 --el-text-color-secondary 的赋值，
    // 值必须走 --aa-secondary-text（不复制混色处方 —— 一处改值全站生效）。
    const gate =
      /html:root:not\(\.dark\)\s*\{[^}]*--el-text-color-secondary\s*:\s*var\(--aa-secondary-text\)\s*;?[^}]*\}/
    expect(elUi, '总闸缺失或未引用 --aa-secondary-text').toMatch(gate)
  })

  it('总闸的 :not(.dark) 前缀存在（暗色不回归：EP 暗色自带值本就达标）', () => {
    expect(elUi).toMatch(/html:root:not\(\.dark\)\s*\{[^}]*--el-text-color-secondary/)
  })

  it('el-ui.scss 不再有无替换的 secondary 字面压制（防回退到旧写法）', () => {
    // 旧病灶形态：直接把 --el-text-color-secondary 指回 EP 浅色原值族。
    // 只盯「无 AA 语义的字面 #909399」，引用 token 的写法不受影响。
    expect(elUi).not.toMatch(/--el-text-color-secondary\s*:\s*#909399/)
  })
})
