/**
 * 动作注册表与确认档/输入档前端镜像的核对测试。
 *
 * ── 与协议对齐的核对方式（跨语言无法 import）─────────────────────────
 * 事实源：`uni_protocol/docker.go` 的 `dockerActionSpecs` 与 `ExpectedDockerConfirm`；
 * 权限码：`uni_core/internal/pkg/dockerpolicy` 的 `policies`（spec §4.3.1）。
 *
 * ── 两层条数口径（别混）────────────────────────────────────────────
 * 「协议全集」= dockerActionSpecs 的六前缀动作 35 条（docker:events 走流订阅、
 * 不带六前缀，不在扫描口径）；「前端已接线」= 本模块各分类清单合计 **35 条** ——
 * P3 收编 image:scan 后**两层相等**（PENDING_WIRING 已清空：协议动作全部接线，
 * 此后协议再加动作而前端没跟，差额会重新出现在红灯里）。
 * 分类是**语义**的（读 / 写 / 会话流 / 配置编辑），不是发布期次：
 *   前端已接线 35 = 4 只读轮询（utils/cmd.ts）+ 25 写（本注册表）+ 1 终端 exec
 *   + 3 配置编辑 + 1 统计流 + 1 聚合日志流。
 * 以后协议加动作时，先改协议/策略，再改这里，最后两处测试与 phase-gate 一起对齐。
 */
import { describe, expect, it } from 'vitest'
import { COMPOSE_LOGS_ACTIONS, PHASE1_ACTIONS } from '../utils/cmd'
import {
  actionGuarded,
  confirmKind,
  DOCKER_ACTION_REGISTRY,
  expectedConfirm,
  isWriteAction,
  lookupDockerAction,
  needsConfirm,
  protectedGate,
  splitDockerProjectService,
  WRITE_ACTIONS
} from '../utils/actions'

/**
 * 终端（会话制交互）动作：字面量只在 pty-terminal 组件里出现，不进任何清单型源码
 *（此处单列是为了互补条数断言与「确实接线了」的正向断言，与 phase-gate 同源）。
 */
const INTERACTIVE_ACTIONS = ['container:exec']

/** 配置编辑动作（compose.file 三条）：清单本体在 utils/compose.ts 的 COMPOSE_ACTIONS。 */
const CONFIG_EDIT_ACTIONS = ['compose.file:write', 'compose.file:validate', 'compose.file:patch']

/** 统计流（会话制只读监控面）：字面量只在 container-stats 组件里出现。 */
const STATS_STREAM_ACTIONS = ['container:stats']

/** 协议全集（六前缀口径；docker:events 走流订阅不在该口径内，见文件头）。 */
const PROTOCOL_ACTION_COUNT = 35

/** 前端已接线（本模块全部分类清单合计；见文件头的分解）。 */
const WIRED_ACTION_COUNT = 35

/**
 * 协议有、前端尚未接线（P3 收编 image:scan 后为空）。
 * 清单本体保留而不是删常量：两层口径的「全集 = 已接线 + 待接线」等式仍然成立
 * （为空时即「收编完成」），协议下次加动作时这里重新出现差额就是红灯。
 */
const PENDING_WIRING: string[] = []

describe('写动作注册表', () => {
  it('恰好覆盖 25 条写动作，无重复、顺序与 WRITE_ACTIONS 一致', () => {
    expect(DOCKER_ACTION_REGISTRY).toHaveLength(25)
    const actions = DOCKER_ACTION_REGISTRY.map((e) => e.action)
    expect(new Set(actions).size).toBe(actions.length)
    expect(actions).toEqual([...WRITE_ACTIONS])
  })

  it('分类互补：已接线 35 = 4 只读 + 25 写 + 1 终端 + 3 配置编辑 + 1 统计流 + 1 聚合日志流，无交集', () => {
    const writes = DOCKER_ACTION_REGISTRY.map((e) => e.action)
    const all = [
      ...PHASE1_ACTIONS,
      ...writes,
      ...INTERACTIVE_ACTIONS,
      ...CONFIG_EDIT_ACTIONS,
      ...STATS_STREAM_ACTIONS,
      ...COMPOSE_LOGS_ACTIONS
    ]
    expect(new Set(all).size).toBe(all.length)
    expect(all).toHaveLength(WIRED_ACTION_COUNT)
    // 非写分类不该混进注册表（分类是语义的：会话流/配置编辑有各自的家）。
    for (const a of [...INTERACTIVE_ACTIONS, ...CONFIG_EDIT_ACTIONS, ...STATS_STREAM_ACTIONS]) {
      expect(isWriteAction(a), `非写动作 ${a} 不该进写动作注册表`).toBe(false)
    }
    for (const a of COMPOSE_LOGS_ACTIONS) {
      // compose:logs 是只读流动作：不进注册表（无确认档/保护档语义），也不进只读轮询清单。
      expect(isWriteAction(a), `只读流动作 ${a} 不该进写动作注册表`).toBe(false)
      expect([...PHASE1_ACTIONS], `${a} 不该进只读 runRead 清单`).not.toContain(a)
    }
  })

  it('两层口径：协议全集 35 = 已接线 35 + 待接线 0（image:build / image:push / image:scan 已收编）', () => {
    // 防的是「协议加了动作、前端清单没跟」与「把未接线动作算进已接线」两个方向的漂移。
    const wired = [
      ...PHASE1_ACTIONS,
      ...DOCKER_ACTION_REGISTRY.map((e) => e.action),
      ...INTERACTIVE_ACTIONS,
      ...CONFIG_EDIT_ACTIONS,
      ...STATS_STREAM_ACTIONS,
      ...COMPOSE_LOGS_ACTIONS
    ]
    for (const a of PENDING_WIRING) {
      expect(wired, `${a} 尚未接线，不该出现在任何前端清单`).not.toContain(a)
      expect(isWriteAction(a), `${a} 尚未收编注册表`).toBe(false)
    }
    expect(wired).toHaveLength(WIRED_ACTION_COUNT)
    expect(WIRED_ACTION_COUNT + PENDING_WIRING.length).toBe(PROTOCOL_ACTION_COUNT)
    // P2/P3 收编的正向断言：三条镜像侧动作已进注册表（此前是 PENDING_WIRING 的差额）。
    for (const a of ['image:build', 'image:push', 'image:scan']) {
      expect(isWriteAction(a), `${a} 应已收编写动作注册表`).toBe(true)
    }
  })

  it('每条都有标签/图标/权限码，且权限码只取 spec §4.3.1 的两档；期次字段已拆除', () => {
    for (const e of DOCKER_ACTION_REGISTRY) {
      expect(e.label, `${e.action} 缺中文标签`).not.toBe('')
      expect(e.label, `${e.action} 的标签不该泄露 action 名`).not.toContain(e.action)
      expect(e.icon, `${e.action} 缺图标`).toMatch(/^ri:/)
      expect(['docker:manage', 'docker:delete'], `${e.action} 的权限码`).toContain(e.perm)
      // 发布期次的 phase 字段已拆除：条目属于哪个分类由「收在哪份清单/注册表」表达，
      // 条目上再挂一份期次就是第二套事实源（漂移面）。
      expect('phase' in e, `${e.action} 不该再携带期次字段`).toBe(false)
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
    expect(isWriteAction('container:inspect')).toBe(false)
    expect(confirmKind('container:inspect')).toBe('none')
    expect(expectedConfirm('container:inspect')).toBe('')
    expect(actionGuarded('container:inspect')).toBe(true) // 未登记保守视为受保护档约束
  })

  it('P2 分发闭环两条：build 无 target、push 有 target；确认档 none（协议无 Confirm 要求、独立对话框即确认）', () => {
    // 逐条对照协议 dockerActionSpecs：build Required=[context,tag]（无 target，
    // 与 create 同型）；push Required=[target]（与 pull 同字段）。两档的权限码与
    // 超时在 core 策略表（docker:manage、30/15 分钟）—— 前端镜像的是权限码列。
    const build = lookupDockerAction('image:build')
    const push = lookupDockerAction('image:push')
    expect(build).toBeTruthy()
    expect(push).toBeTruthy()
    expect(build!.needsTarget, 'build 没有 target（语义在 context/tag 里）').toBe(false)
    expect(push!.needsTarget, 'push 的 target 是本地镜像引用').toBe(true)
    for (const e of [build!, push!]) {
      expect(e.perm, `${e.action} 权限码与 pull 同档（分发家族）`).toBe('docker:manage')
      expect(e.danger).toBe('normal')
      expect(e.confirm, `${e.action} 无确认档（对话框即确认）`).toBe('none')
      expect(e.guarded, `${e.action} 镜像无保护粒度`).toBe(false)
      // 输入面在独立对话框（构建表单/推送选择），不走 action-confirm 的输入档。
      expect(e.input).toBeUndefined()
      expect(expectedConfirm(e.action, {})).toBe('')
      expect(confirmKind(e.action, {})).toBe('none')
    }
  })

  it('P3 安全面一条：scan 有 target、manage 档、无确认（照 image:pull 形态；读语义但走长任务通道）', () => {
    // 逐条对照协议 dockerActionSpecs：image:scan Required=[target]（与 pull 同字段）；
    // 权限码 docker:manage（协议裁决：扫描要执行外部二进制并下载漏洞库，与 pull
    // 同档而非 inspect 档）。它读语义但住在写注册表 —— 理由是交互形态（分钟级
    // 长任务 + 任务中心可见 + 行内就近触发），不是读写之分（见注册表条目注释）。
    const scan = lookupDockerAction('image:scan')
    expect(scan).toBeTruthy()
    expect(scan!.needsTarget, 'scan 的 target 是镜像引用（与 pull 同字段）').toBe(true)
    expect(scan!.perm).toBe('docker:manage')
    expect(scan!.danger).toBe('normal')
    // 无破坏性动作：抄一遍 target 只会把确认训练成例行公事（协议无 Confirm 要求）。
    expect(scan!.confirm).toBe('none')
    expect(scan!.guarded, '镜像无保护粒度').toBe(false)
    expect(scan!.input).toBeUndefined()
    expect(expectedConfirm('image:scan', {})).toBe('')
    expect(confirmKind('image:scan', {})).toBe('none')
  })
})

describe('输入档（input 描述：弹窗内收集新参数）', () => {
  it('声明 input 的条目恰好三条：打标签（新引用）、导出（文件名）、载入（文件名）', () => {
    const withInput = DOCKER_ACTION_REGISTRY.filter((e) => e.input)
    expect(withInput.map((e) => e.action).sort()).toEqual(['image:load', 'image:save', 'image:tag'])
  })

  it('confirmKind 判为 input 的动作必须声明 input（形态与描述互相咬合，缺描述=数据缺口）', () => {
    for (const e of DOCKER_ACTION_REGISTRY) {
      const isInputKind = confirmKind(e.action, {}) === 'input'
      // image:save 的静态档是 filename-word，第一段经 confirmKind 修正为 input —— 在判式里。
      expect(Boolean(e.input), `${e.action} 的 input 声明与 input 形态不一致`).toBe(isInputKind)
    }
  })

  it('image:tag 的校验：镜像引用格式（与拉取对话框同一把协议尺）', () => {
    const spec = lookupDockerAction('image:tag')?.input
    expect(spec).toBeTruthy()
    expect(spec!.label).toBe('新的镜像引用')
    expect(spec!.placeholder).toBe('例如 仓库/名称:标签')
    expect(spec!.validate?.('uni-center/core:v2')).toBe(true)
    expect(spec!.validate?.('nginx')).toBe(true)
    // 不合法给提示句（弹窗就地显示），不是静默的 false。
    const bad = spec!.validate?.('bad ref!')
    expect(bad).not.toBe(true)
    expect(typeof bad).toBe('string')
    expect(bad).toContain('镜像引用')
  })

  it('image:save / image:load 的校验：沿用原 prompt 口径 —— 只拦空值，不前端拦格式', () => {
    for (const action of ['image:save', 'image:load']) {
      const spec = lookupDockerAction(action)?.input
      expect(spec).toBeTruthy()
      expect(spec!.validate, `${action} 不该校验格式（原 prompt 只拦空值）`).toBeUndefined()
      expect(spec!.required ?? true, `${action} 必填`).toBe(true)
    }
    // 原 prompt 的正文口径搬进 hint（「只填文件名」与产物/文件落点），一句不丢。
    expect(lookupDockerAction('image:save')?.input?.hint).toBe(
      '只填文件名，产物落在该主机的 agent 下载目录。'
    )
    expect(lookupDockerAction('image:load')?.input?.hint).toBe(
      '只填文件名，文件需已放在该主机的 agent 下载目录。'
    )
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
    // 输入档恒无逐字值：协议对这三条不要求 confirm（弹窗收集的是参数本身）。
    ['image:tag', { src: 'a:1', dst: 'a:2' }, ''],
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
    // 覆盖时强：image:save 只有 overwrite=true 的重发才要文件名（第一段是输入档，无逐字值）
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
    // 输入档：打标签/载入的参数必须在弹窗里收集（值拿不到就无从发送）。
    ['image:tag', { src: 'a:1' }, 'input'],
    ['image:load', {}, 'input'],
    // image:save 两段：第一段收集文件名（输入档），第二段照抄文件名（覆盖确认）。
    ['image:save', { filename: 'a.tar' }, 'input'],
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

  it('输入档的 expected 恒空，但弹窗必须开（值要先收集，与逐字档的空串语义不同）', () => {
    expect(confirmKind('image:tag', { src: 'a:1' })).toBe('input')
    expect(needsConfirm('image:tag', { src: 'a:1' })).toBe(true)
    expect(expectedConfirm('image:tag', { src: 'a:1' })).toBe('')
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
