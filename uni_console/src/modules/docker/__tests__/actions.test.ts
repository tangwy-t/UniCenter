/**
 * 动作注册表与确认档前端镜像的核对测试。
 *
 * ── 与协议对齐的核对方式（跨语言无法 import）─────────────────────────
 * 事实源：`uni_protocol/docker.go` 的 `dockerActionSpecs`（33 条，含不走六前缀的
 * docker:events）与 `ExpectedDockerConfirm`；权限/期次：`uni_core/internal/pkg/dockerpolicy`
 * 的 `policies`（spec §4.3.1）。
 *
 * 32 条的分解：协议白名单（六前缀口径）32 =
 *   4 条一期只读（container:logs / container:inspect / image:inspect / compose.file:read）
 * + 22 条二期写动作（本注册表，4a 起 container:create 归操作面二期）
 * + 1 条三期（container:exec，交互面）
 * + 3 条四期（compose.file:write/validate/patch）
 * + 1 条五期（container:stats，监控面实时流 —— 会话制只读，不进本注册表）
 * + 1 条 5a（compose:logs，项目聚合日志 —— 会话制只读，收在 utils/cmd 的
 *   COMPOSE_LOGS_ACTIONS，同样不进本注册表）。
 * 22 才是二期写的条数：dockerpolicy 里 Phase=2 的行逐条数即 22，实验证伪不了这一点
 * （把 container:exec 算进二期会让 phase-gate 守卫红灯 —— 它扫注册表里的后期动作）。
 * 以后协议加动作时，先改协议/策略，再改这里，最后两处测试与 phase-gate 一起对齐。
 */
import { describe, expect, it } from 'vitest'
import { COMPOSE_LOGS_ACTIONS, PHASE1_ACTIONS } from '../utils/cmd'
import {
  actionGuarded,
  confirmKind,
  DOCKER_ACTION_REGISTRY,
  expectedConfirm,
  isPhase2Action,
  lookupDockerAction,
  needsConfirm,
  PHASE2_ACTIONS,
  protectedGate,
  splitDockerProjectService
} from '../utils/actions'

/** 三期动作（与 phase-gate 的 PHASE3_ACTIONS 同源；此处只为互补条数断言）。 */
const PHASE3_ACTIONS = ['container:exec']

/** 四期动作（与 phase-gate 的 PHASE4_ACTIONS 同源；此处只为互补条数断言）。 */
const PHASE4_ACTIONS = ['compose.file:write', 'compose.file:validate', 'compose.file:patch']

/** 五期动作（监控面 · stats 实时流；与 phase-gate 的 PHASE5_ACTIONS 同源）。 */
const PHASE5_ACTIONS = ['container:stats']

/** 协议白名单总条数（六前缀口径；docker:events 走流订阅不在该口径内）。 */
const PROTOCOL_ACTION_COUNT = 32

describe('二期写动作注册表', () => {
  it('恰好覆盖 22 条二期写动作，无重复、顺序与 PHASE2_ACTIONS 一致', () => {
    expect(DOCKER_ACTION_REGISTRY).toHaveLength(22)
    const actions = DOCKER_ACTION_REGISTRY.map((e) => e.action)
    expect(new Set(actions).size).toBe(actions.length)
    expect(actions).toEqual([...PHASE2_ACTIONS])
  })

  it('与协议互补：32 = 4 一期只读 + 22 二期写 + 1 三期 + 3 四期 + 1 五期 + 1 条 5a 聚合日志，无交集', () => {
    const phase2 = DOCKER_ACTION_REGISTRY.map((e) => e.action)
    const all = [
      ...PHASE1_ACTIONS,
      ...phase2,
      ...PHASE3_ACTIONS,
      ...PHASE4_ACTIONS,
      ...PHASE5_ACTIONS,
      ...COMPOSE_LOGS_ACTIONS
    ]
    expect(new Set(all).size).toBe(all.length)
    expect(all).toHaveLength(PROTOCOL_ACTION_COUNT)
    for (const a of [...PHASE3_ACTIONS, ...PHASE4_ACTIONS, ...PHASE5_ACTIONS]) {
      expect(isPhase2Action(a), `后期动作 ${a} 不该进二期注册表`).toBe(false)
    }
    for (const a of COMPOSE_LOGS_ACTIONS) {
      // compose:logs 是只读流动作：不进注册表（无确认档/保护档语义），也不进一期清单。
      expect(isPhase2Action(a), `只读流动作 ${a} 不该进二期注册表`).toBe(false)
      expect([...PHASE1_ACTIONS], `${a} 不该进一期 runRead 清单`).not.toContain(a)
    }
  })

  it('每条都有标签/图标/权限码，且权限码只取 spec §4.3.1 的两档', () => {
    for (const e of DOCKER_ACTION_REGISTRY) {
      expect(e.label, `${e.action} 缺中文标签`).not.toBe('')
      expect(e.label, `${e.action} 的标签不该泄露 action 名`).not.toContain(e.action)
      expect(e.icon, `${e.action} 缺图标`).toMatch(/^ri:/)
      expect(['docker:manage', 'docker:delete'], `${e.action} 的权限码`).toContain(e.perm)
      expect(e.phase).toBe(2)
      expect(lookupDockerAction(e.action)).toBe(e)
    }
  })

  it('危险/毁灭档必须给结论句（弹窗上关于后果的唯一文案）；常规档可以留空', () => {
    for (const e of DOCKER_ACTION_REGISTRY) {
      if (e.danger === 'normal') continue
      expect(e.conclusion, `${e.action} 缺结论句`).not.toBe('')
    }
  })

  it('删除类动作（docker:delete）都必须有确认档，管理类可以没有', () => {
    for (const e of DOCKER_ACTION_REGISTRY) {
      if (e.perm !== 'docker:delete') continue
      expect(e.confirm, `${e.action} 是删除类却没有确认档`).not.toBe('none')
    }
  })

  it('动作名不存在于注册表时按未登记处理（不猜权限、不猜确认档）', () => {
    expect(lookupDockerAction('container:inspect')).toBeUndefined()
    expect(isPhase2Action('container:inspect')).toBe(false)
    expect(confirmKind('container:inspect')).toBe('none')
    expect(expectedConfirm('container:inspect')).toBe('')
    expect(actionGuarded('container:inspect')).toBe(true) // 未登记保守视为受保护档约束
  })
})

describe('expectedConfirm：照抄协议 ExpectedDockerConfirm', () => {
  it.each([
    // action, options, 期望
    // create 是标准档：无逐字值（弹窗只要用户核对镜像名/容器名，不需要照抄什么）。
    ['container:create', { image: 'mysql:8', name: 'db' }, ''],
    ['container:start', { target: 'mysql' }, ''],
    ['container:stop', { target: 'mysql' }, ''],
    ['container:restart', { target: 'mysql' }, ''],
    ['container:remove', { target: 'mysql' }, ''],
    ['image:remove', { target: 'mysql:8.0' }, ''],
    ['image:pull', { target: 'mysql:8.0' }, ''],
    ['image:tag', { target: 'a:1', src: 'a:1', dst: 'a:2' }, ''],
    ['image:load', { filename: 'a.tar' }, ''],
    ['volume:remove', { target: 'uploads' }, ''],
    ['network:remove', { target: 'app-net' }, ''],
    ['compose:start', { target: 'uni-center' }, ''],
    ['compose:restart', { target: 'uni-center' }, ''],
    ['compose:pull', { target: 'uni-center' }, ''],
    // 固定文本档：没有单一目标名的批量清理
    ['image:prune', { all: true }, 'DELETE'],
    ['volume:prune', undefined, 'DELETE'],
    // 照抄目标名档
    ['compose:up', { target: 'uni-center' }, 'uni-center'],
    ['compose:stop', { target: 'uni-center' }, 'uni-center'],
    ['compose:down', { target: 'uni-center' }, 'uni-center'],
    ['compose.service:remove-containers', { target: 'uni-center/core' }, 'core'],
    // 服务级动作的「项目/服务」形态取 service 部分
    ['compose.service:scale', { target: 'uni-center/core', n: 0 }, 'core'],
    ['compose.service:scale', { target: 'uni-center/core', n: 2 }, ''],
    ['compose.service:scale', { target: 'uni-center', n: 0 }, 'uni-center'],
    ['compose.service:scale', { target: 'uni-center', n: 3 }, ''],
    ['compose.service:scale', { target: 'uni-center/core' }, 'core'], // 未给 n = 未确认缩容（照协议 o.N==nil）
    // 覆盖时强：image:save 只有 overwrite=true 的重发才要文件名
    ['image:save', { target: 'mysql:8.0', filename: 'mysql.tar' }, ''],
    ['image:save', { target: 'mysql:8.0', filename: 'mysql.tar', overwrite: false }, ''],
    ['image:save', { target: 'mysql:8.0', filename: 'mysql.tar', overwrite: true }, 'mysql.tar'],
    // 缺席 options 的形态（协议 o == nil）
    ['compose:down', undefined, ''],
    ['compose:up', { target: '' }, '']
  ] as const)('%s %j → %j', (action, options, want) => {
    expect(expectedConfirm(action, options as never)).toBe(want)
  })

  it('大小写敏感：DELETE 不是 delete（逐字比对，模糊匹配会退化确认档）', () => {
    expect(expectedConfirm('volume:prune', {})).toBe('DELETE')
    expect(expectedConfirm('volume:prune', {})).not.toBe('delete')
  })
})

describe('confirmKind：弹窗形态', () => {
  it.each([
    ['container:create', { image: 'mysql:8' }, 'confirm'],
    ['container:start', undefined, 'none'],
    ['container:remove', { target: 'mysql' }, 'confirm'],
    ['image:remove', { target: 'mysql:8.0' }, 'confirm'],
    ['volume:remove', { target: 'v' }, 'confirm'],
    ['network:remove', { target: 'n' }, 'confirm'],
    ['image:prune', {}, 'delete-word'],
    ['volume:prune', {}, 'delete-word'],
    ['compose:up', { target: 'p' }, 'target-word'],
    ['compose:stop', { target: 'p' }, 'target-word'],
    ['compose:down', { target: 'p' }, 'target-word'],
    ['compose.service:remove-containers', { target: 'p/s' }, 'target-word'],
    ['compose.service:scale', { target: 'p/s', n: 2 }, 'none'],
    ['compose.service:scale', { target: 'p/s', n: 0 }, 'target-word'],
    ['compose.service:scale', { target: 'p/s' }, 'target-word'],
    ['image:save', { filename: 'a.tar' }, 'none'],
    ['image:save', { filename: 'a.tar', overwrite: true }, 'filename-word']
  ] as const)('%s %j → %s', (action, options, want) => {
    expect(confirmKind(action, options as never)).toBe(want)
    expect(needsConfirm(action, options as never)).toBe(want !== 'none')
  })

  it('形态不看 expectedConfirm 的字符串结果：目标为空也仍是强档', () => {
    // 目标还没填时 expectedConfirm 返回空串，但那不代表「不用弹窗」
    expect(confirmKind('compose:down', { target: '' })).toBe('target-word')
    expect(expectedConfirm('compose:down', { target: '' })).toBe('')
  })
})

describe('splitDockerProjectService：照抄协议的同名函数', () => {
  it('合法「项目/服务」按第一个斜杠切', () => {
    expect(splitDockerProjectService('uni-center/core')).toEqual({
      project: 'uni-center',
      service: 'core'
    })
    // 第二个斜杠之后不是合法名字：按第一个斜杠切完后 service 含 `/` → 不合法 → null
    //（协议的服务名正则有 `/` 白名单之外，故 `a/b/c` 整体不成立）。
    expect(splitDockerProjectService('a/b/c')).toBeNull()
  })

  it('不合法形态返回 null（调用方回退整串，与协议一致）', () => {
    expect(splitDockerProjectService('uni-center')).toBeNull()
    expect(splitDockerProjectService('/core')).toBeNull()
    expect(splitDockerProjectService('uni-center/')).toBeNull()
    expect(splitDockerProjectService('a b/c')).toBeNull()
    expect(splitDockerProjectService('a/ b')).toBeNull()
  })
})

describe('protectedGate：受保护目标的额外要求（spec §10.1）', () => {
  it('未受保护：放行，无需强制开关', () => {
    const g = protectedGate({ protected: false }, false)
    expect(g.allowed).toBe(true)
    expect(g.needForce).toBe(false)
    expect(g.conclusion).toBe('')
    expect(protectedGate(undefined, false).allowed).toBe(true)
  })

  it('受保护 + 有 exec 权限：放行，弹窗必须出现强制开关', () => {
    const g = protectedGate({ protected: true }, true)
    expect(g.protected).toBe(true)
    expect(g.allowed).toBe(true)
    expect(g.needForce).toBe(true)
    expect(g.conclusion).not.toBe('')
  })

  it('受保护 + 无 exec 权限：不允许操作，给结论句', () => {
    const g = protectedGate({ protected: true }, false)
    expect(g.allowed).toBe(false)
    expect(g.needForce).toBe(false)
    expect(g.conclusion).not.toBe('')
  })

  it('结论句只讲结论，不出现权限码/字段名/协议术语', () => {
    for (const hasExec of [true, false]) {
      const text = protectedGate({ protected: true }, hasExec).conclusion
      for (const term of ['docker:', 'force', 'confirm', 'payload', 'target']) {
        expect(text, `结论句泄露了内部术语「${term}」`).not.toContain(term)
      }
    }
  })
})
