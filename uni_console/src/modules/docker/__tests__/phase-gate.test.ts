/**
 * 守卫：一期页面**不得出现**二期及以后的动作。
 *
 * 为什么需要它：spec §11.0 的分期控件矩阵写着「不渲染 ≠ 禁用」——一期页面上画一个
 * 二期按钮，用户点下去只会拿到 400，而他无从知道原因（「为什么点不了」）。这条纪律
 * 靠人记是不可靠的（把后期按钮顺手加上去是极自然的事），故把它变成红灯。
 *
 * 扫描口径：docker 模块的 .vue 与 .ts 源码里出现任何非一期 action 字面量即失败。
 * 一期允出现的就是 PHASE1_ACTIONS 那四个（它们出现在 utils/cmd.ts 的常量里）。
 *
 * ── 为什么要跳过隐藏目录 ──────────────────────────────────────────
 * 模块目录里可能落下工具产物（例如安全扫描 hook 的 `.mimosa/`，它已在 .gitignore
 * 但确实存在于磁盘上）。遍历时连它们一起读毫无意义，还会让守卫的失败信息被噪音
 * 淹没（prettier 撞上同类目录会直接报 `No parser could be inferred`）。以 `.` 开头
 * 的目录一律跳过 —— 源码目录不存在合法的「点开头」子目录。
 */
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { PHASE1_ACTIONS } from '../utils/cmd'

const ROOT = new URL('..', import.meta.url).pathname

/**
 * 全部后期动作 = 协议白名单去掉一期四个。
 *
 * **来源与核对**（跨语言没法互相 import，这份清单必须自己与协议对齐，差一条就等于
 * 少守一个动作）：`uni_protocol/docker.go` 的 `AllDockerActions()`（由
 * `dockerActionSpecs` 枚举，共 29 条）去掉一期四个后恰为 25 条；2026-09-24 用脚本
 * 做过一次逐条比对：`protocol − PHASE1` 与下面这份的**差集为空、条数一致**
 * （29 = 25 + 4）。以后往 `dockerActionSpecs` 里加动作时，要同步往这里加一条。
 */
const LATER_ACTIONS = [
  'container:start',
  'container:stop',
  'container:restart',
  'container:remove',
  'container:exec',
  'image:remove',
  'image:prune',
  'image:pull',
  'image:tag',
  'image:save',
  'image:load',
  'volume:remove',
  'volume:prune',
  'network:remove',
  'compose:up',
  'compose:stop',
  'compose:start',
  'compose:restart',
  'compose:pull',
  'compose:down',
  'compose.service:scale',
  'compose.service:remove-containers',
  'compose.file:write',
  'compose.file:validate',
  'compose.file:patch'
]

/** 协议白名单的总条数（`AllDockerActions()` 的返回长度）：25 条后期 + 4 条一期。 */
const PROTOCOL_ACTION_COUNT = 29

function walk(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) {
      if (name === '__tests__') continue // 守卫文件自身会引用这些字符串
      if (name.startsWith('.')) continue // 工具产物目录（如 .mimosa）：不是源码
      walk(p, out)
    } else if (/\.(vue|ts)$/.test(name)) {
      out.push(p)
    }
  }
  return out
}

describe('分期控件矩阵（一期页面不出现后期动作）', () => {
  it('扫描不是空转：确实扫到了模块源码', () => {
    const files = walk(ROOT)
    expect(files.length).toBeGreaterThan(0)
    expect(files.some((f) => f.endsWith('.vue'))).toBe(true)
    expect(files.some((f) => f.endsWith('utils/cmd.ts'))).toBe(true)
  })

  it('模块源码里没有任何后期动作字面量', () => {
    for (const file of walk(ROOT)) {
      const src = readFileSync(file, 'utf8')
      for (const action of LATER_ACTIONS) {
        expect(
          src.includes(action),
          `${file} 出现了后期动作 ${action}（一期不该渲染它的入口）`
        ).toBe(false)
      }
    }
  })

  it('一期动作清单恰好四个（与 spec §11.0 的一期矩阵一致）', () => {
    expect([...PHASE1_ACTIONS]).toHaveLength(4)
  })

  it('两份清单互补：29 条 = 25 条后期 + 4 条一期，无重复、无交集', () => {
    // 防的是「从 LATER_ACTIONS 里删掉一条」这种静默失守：条数不对就红灯。
    expect(new Set(LATER_ACTIONS).size).toBe(LATER_ACTIONS.length)
    expect(new Set(PHASE1_ACTIONS).size).toBe(PHASE1_ACTIONS.length)
    expect(LATER_ACTIONS.length + PHASE1_ACTIONS.length).toBe(PROTOCOL_ACTION_COUNT)
    for (const a of PHASE1_ACTIONS) {
      expect(LATER_ACTIONS, `一期动作 ${a} 不该出现在后期清单里`).not.toContain(a)
    }
  })

  it('终端与 Follow 这两类三期控件不出现在模板里', () => {
    for (const file of walk(ROOT).filter((f) => f.endsWith('.vue'))) {
      const src = readFileSync(file, 'utf8')
      expect(src.includes('xterm'), `${file} 引入了三期终端`).toBe(false)
      expect(/Follow/.test(src), `${file} 出现 Follow 开关（三期才渲染）`).toBe(false)
    }
  })
})
