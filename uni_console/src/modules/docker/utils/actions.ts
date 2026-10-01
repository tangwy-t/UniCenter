/**
 * 二期写动作的注册表，以及确认档/保护档的**前端镜像**（纯数据 + 纯函数）。
 *
 * ── 单一事实源在哪 ──────────────────────────────────────────────────
 * 跨语言无法 import，故这里逐条镜像两处事实（与 phase-gate 的核对注释同一套方法）：
 *   - action 清单 / options 必填 / confirm 判定：`uni_protocol/docker.go` 的
 *     `dockerActionSpecs` 与 `ExpectedDockerConfirm`；
 *   - 权限码 / 期次：`uni_core/internal/pkg/dockerpolicy` 的 `policies`（spec §4.3.1 的表）。
 * `__tests__/actions.test.ts` 用条数互补（4 只读 + 22 二期写 + 1 三期 + 3 四期 +
 * 1 五期 = 31）与逐条断言把「镜像漂移」变成红灯。
 *
 * ── 为什么注册表只收二期写动作 ──────────────────────────────────────
 * 一期四个只读动作在 `utils/cmd.ts` 的 `PHASE1_ACTIONS` 里；三期终端（会话制，确认档/
 * 保护档与写动作不是同一套）与四期配置编辑类动作不进本文件 —— 四期三条（write/
 * validate/patch）在 `utils/compose.ts` 的 `COMPOSE_ACTIONS` 里，确认档由
 * compose-editor 以 override 形式传给确认弹窗（协议 §4.3.1 的「强 = 照抄项目名」）。
 * phase-gate 守卫现在的口径是「模块里出现的 action 字面量必须都在协议白名单
 * （六前缀口径）内」，四期之后不再有「后期动作」清单；但**本注册表的条数断言
 * 仍然有效**（4a 起 22 条 —— container:create 随四支柱创建面归操作面二期），
 * 不要为了「表看起来完整」把三/四/五期动作加进来（会破坏五份清单互补的断言）。
 */
import { PermDockerDelete, PermDockerManage } from '@/enums/permission'

/** 动作的危险程度：normal 常规 / danger 危险 / destructive 毁灭（批量不可逆清理）。 */
export type ActionDanger = 'normal' | 'danger' | 'destructive'

/**
 * 确认档形态（供弹窗选形态）：
 *   - none：无需弹窗，点了直接发；
 *   - confirm：普通确认弹窗（标准档，无逐字输入）；
 *   - delete-word：逐字输入固定文本 DELETE；
 *   - target-word：逐字输入目标名（项目/服务级动作输入**服务名或项目名**）；
 *   - filename-word：逐字输入文件名（导出覆盖已有产物）。
 */
export type ConfirmKind = 'none' | 'confirm' | 'delete-word' | 'target-word' | 'filename-word'

/** 指令 options 的前端形态（只声明判定用得到的字段，其余原样透传）。 */
export interface DockerActionOptions {
  target?: string
  n?: number
  overwrite?: boolean
  filename?: string
  [key: string]: unknown
}

/** 一期四个只读动作之外的**二期写动作全集**（22 条，顺序 = spec §4.3.1 书写顺序）。 */
export const PHASE2_ACTIONS = [
  'container:create',
  'container:start',
  'container:stop',
  'container:restart',
  'container:remove',
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
  'compose.service:remove-containers'
] as const
export type Phase2Action = (typeof PHASE2_ACTIONS)[number]

export interface DockerActionEntry {
  action: Phase2Action
  /** 菜单/按钮上的中文标签（页面只说结论，不出现 action 名）。 */
  label: string
  icon: string
  /** 权限码，照 spec §4.3.1（docker:manage / docker:delete）。 */
  perm: string
  /** 期次：注册表只收二期。 */
  phase: 2
  danger: ActionDanger
  /** 是否需要目标（*:prune 无目标；image:load 的主参数是文件名而不是 target）。 */
  needsTarget: boolean
  /** 静态确认档；两个随 options 变化的例外（image:save / compose.service:scale）由 confirmKind 修正。 */
  confirm: ConfirmKind
  /** 目标的保护档是否拦它（agent 的 guard 覆盖面；镜像/网络/清理类无保护粒度）。 */
  guarded: boolean
  /** 弹窗里的结论句（不可恢复/影响面），页面上关于后果的唯一文案来源。 */
  conclusion: string
}

/** 注册表（22 条，逐条对照 spec §4.3.1 的「二」档）。 */
export const DOCKER_ACTION_REGISTRY: readonly DockerActionEntry[] = [
  {
    // 创建面（4a）：**没有 target**（动作对象是将要诞生的容器，语义都在 image/name ——
    // 协议 validateDockerContainerCreate 显式拒绝带 target）；危险度低（不删不停任何
    // 现存目标，与启停同级 docker:manage）；标准档确认（create 是「多出来一个东西」
    // 的决定，值得让用户在弹窗里再核对一次镜像名与容器名）。
    action: 'container:create',
    label: '创建容器',
    icon: 'ri:add-circle-line',
    perm: PermDockerManage,
    phase: 2,
    danger: 'normal',
    needsTarget: false,
    confirm: 'confirm',
    // 保护清单管的是**现存**容器/卷的停删重建；create 的对象还不存在，谈不上受保护。
    guarded: false,
    conclusion: ''
  },
  {
    action: 'container:start',
    label: '启动',
    icon: 'ri:play-circle-line',
    perm: PermDockerManage,
    phase: 2,
    danger: 'normal',
    needsTarget: true,
    confirm: 'none',
    guarded: true,
    conclusion: ''
  },
  {
    action: 'container:stop',
    label: '停止',
    icon: 'ri:pause-circle-line',
    perm: PermDockerManage,
    phase: 2,
    danger: 'normal',
    needsTarget: true,
    confirm: 'none',
    guarded: true,
    conclusion: ''
  },
  {
    action: 'container:restart',
    label: '重启',
    icon: 'ri:refresh-line',
    perm: PermDockerManage,
    phase: 2,
    danger: 'normal',
    needsTarget: true,
    confirm: 'none',
    guarded: true,
    conclusion: ''
  },
  {
    action: 'container:remove',
    label: '删除',
    icon: 'ri:delete-bin-5-line',
    perm: PermDockerDelete,
    phase: 2,
    danger: 'danger',
    needsTarget: true,
    confirm: 'confirm',
    guarded: true,
    conclusion: '此操作不可恢复。'
  },
  {
    action: 'image:remove',
    label: '删除',
    icon: 'ri:delete-bin-5-line',
    perm: PermDockerDelete,
    phase: 2,
    danger: 'danger',
    needsTarget: true,
    confirm: 'confirm',
    guarded: false,
    conclusion: '此操作不可恢复。'
  },
  {
    action: 'image:prune',
    label: '清理悬空镜像',
    icon: 'ri:brush-line',
    perm: PermDockerDelete,
    phase: 2,
    danger: 'destructive',
    needsTarget: false,
    confirm: 'delete-word',
    guarded: false,
    conclusion: '未被使用的悬空镜像会被清除。此操作不可恢复。'
  },
  {
    action: 'image:pull',
    label: '拉取镜像',
    icon: 'ri:download-2-line',
    perm: PermDockerManage,
    phase: 2,
    danger: 'normal',
    needsTarget: true,
    confirm: 'none',
    guarded: false,
    conclusion: ''
  },
  {
    action: 'image:tag',
    label: '打标签',
    icon: 'ri:price-tag-3-line',
    perm: PermDockerManage,
    phase: 2,
    danger: 'normal',
    needsTarget: true,
    confirm: 'none',
    guarded: false,
    conclusion: ''
  },
  {
    action: 'image:save',
    label: '导出',
    icon: 'ri:file-download-line',
    perm: PermDockerManage,
    phase: 2,
    danger: 'normal',
    needsTarget: true,
    confirm: 'filename-word',
    guarded: false,
    conclusion: '同名文件将被覆盖，原文件无法找回。'
  },
  {
    action: 'image:load',
    label: '载入镜像',
    icon: 'ri:file-upload-line',
    perm: PermDockerManage,
    phase: 2,
    danger: 'normal',
    needsTarget: false,
    confirm: 'none',
    guarded: false,
    conclusion: ''
  },
  {
    action: 'volume:remove',
    label: '删除',
    icon: 'ri:delete-bin-5-line',
    perm: PermDockerDelete,
    phase: 2,
    danger: 'danger',
    needsTarget: true,
    confirm: 'confirm',
    guarded: true,
    conclusion: '卷里的数据会一并丢失，此操作不可恢复。'
  },
  {
    action: 'volume:prune',
    label: '清理未使用卷',
    icon: 'ri:brush-line',
    perm: PermDockerDelete,
    phase: 2,
    danger: 'destructive',
    needsTarget: false,
    confirm: 'delete-word',
    guarded: false,
    conclusion: '未被任何容器使用的数据卷会被清除。此操作不可恢复。'
  },
  {
    action: 'network:remove',
    label: '删除',
    icon: 'ri:delete-bin-5-line',
    perm: PermDockerDelete,
    phase: 2,
    danger: 'danger',
    needsTarget: true,
    confirm: 'confirm',
    guarded: false,
    conclusion: '此操作不可恢复。'
  },
  {
    action: 'compose:up',
    label: '启动项目',
    icon: 'ri:play-circle-line',
    perm: PermDockerManage,
    phase: 2,
    danger: 'danger',
    needsTarget: true,
    confirm: 'target-word',
    guarded: true,
    conclusion: '项目下的容器会被重建，服务会短暂中断。'
  },
  {
    action: 'compose:stop',
    label: '停止项目',
    icon: 'ri:pause-circle-line',
    perm: PermDockerManage,
    phase: 2,
    danger: 'danger',
    needsTarget: true,
    confirm: 'target-word',
    guarded: true,
    conclusion: '项目下的所有容器都会停止。'
  },
  {
    action: 'compose:start',
    label: '启动项目容器',
    icon: 'ri:play-line',
    perm: PermDockerManage,
    phase: 2,
    danger: 'normal',
    needsTarget: true,
    confirm: 'none',
    guarded: true,
    conclusion: ''
  },
  {
    action: 'compose:restart',
    label: '重启项目',
    icon: 'ri:refresh-line',
    perm: PermDockerManage,
    phase: 2,
    danger: 'normal',
    needsTarget: true,
    confirm: 'none',
    guarded: true,
    conclusion: ''
  },
  {
    action: 'compose:pull',
    label: '拉取项目镜像',
    icon: 'ri:download-2-line',
    perm: PermDockerManage,
    phase: 2,
    danger: 'normal',
    needsTarget: true,
    confirm: 'none',
    guarded: true,
    conclusion: ''
  },
  {
    action: 'compose:down',
    label: '停止并移除项目',
    icon: 'ri:shut-down-line',
    perm: PermDockerDelete,
    phase: 2,
    danger: 'destructive',
    needsTarget: true,
    confirm: 'target-word',
    guarded: true,
    conclusion: '项目下的容器与网络会被移除。此操作不可恢复。'
  },
  {
    action: 'compose.service:scale',
    label: '调整实例数',
    icon: 'ri:arrow-up-down-line',
    perm: PermDockerManage,
    phase: 2,
    danger: 'danger',
    needsTarget: true,
    confirm: 'target-word',
    guarded: true,
    conclusion: '调小到 0 时，该服务的容器会被移除。'
  },
  {
    action: 'compose.service:remove-containers',
    label: '删除服务容器',
    icon: 'ri:delete-bin-5-line',
    perm: PermDockerDelete,
    phase: 2,
    danger: 'danger',
    needsTarget: true,
    confirm: 'target-word',
    guarded: true,
    conclusion: '该服务的容器会被移除；可用「启动项目」重新创建。'
  }
]

/** 注册表索引（按 action 查找；未登记返回 undefined）。 */
export function lookupDockerAction(action: string): DockerActionEntry | undefined {
  return DOCKER_ACTION_REGISTRY.find((e) => e.action === action)
}

/** 该 action 是否属于二期写动作（注册表内）。 */
export function isPhase2Action(action: string): action is Phase2Action {
  return lookupDockerAction(action) !== undefined
}

/**
 * 目标是否受保护档约束（agent 的 guard 覆盖面）。
 *
 * 注册表内按条目标记；未登记动作保守返回 true（未知动作即使有保护语义也不放行）。
 */
export function actionGuarded(action: string): boolean {
  return lookupDockerAction(action)?.guarded ?? true
}

/**
 * 「项目/服务」形态 target 的拆分 —— **逐条照抄**协议 `SplitDockerProjectService`：
 * 按**第一个** `/` 切、两侧都非空且各自合法才成立（否则 null，调用方回退整串）。
 */
export function splitDockerProjectService(
  target: string
): { project: string; service: string } | null {
  const i = target.indexOf('/')
  if (i <= 0 || i === target.length - 1) return null
  const project = target.slice(0, i)
  const service = target.slice(i + 1)
  // 与协议的两条正则同形：项目名 / 容器名都是「首字符字母数字 + [A-Za-z0-9_.-]*」。
  const nameRe = /^[a-zA-Z0-9][a-zA-Z0-9_.-]*$/
  if (!nameRe.test(project) || !nameRe.test(service)) return null
  return { project, service }
}

/**
 * 该 action 在给定 options 下要求的 confirm 值（空串 = 无需确认）。
 *
 * **逐条照抄**协议 `ExpectedDockerConfirm`（uni_protocol/docker.go 约 388-460 行），
 * 含三处例外：
 *   - `image:prune` / `volume:prune` → 固定文本 DELETE；
 *   - `compose:up/stop/down` 与 `compose.service:*` → 照抄目标名（「项目/服务」形态取
 *     服务名）；其中 `compose.service:scale` **n>0 不要确认**（扩到 N>0 可逆），
 *     n=0（或未给 n）才要服务名；
 *   - `image:save` **覆盖时强**：只有 overwrite=true 的重发才要求照抄文件名。
 *
 * 大小写敏感（协议的 CheckDockerConfirm 也是逐字比较）：模糊匹配会让「照抄一遍」
 * 退化成「随便填点东西」。
 */
export function expectedConfirm(action: string, options?: DockerActionOptions | null): string {
  const entry = lookupDockerAction(action)
  if (!entry) return ''
  switch (entry.confirm) {
    case 'delete-word':
      return 'DELETE'
    case 'target-word': {
      // 例外：scale 只在缩容到 0 时要确认（扩到 N>0 是常规可逆操作）。
      if (action === 'compose.service:scale' && options?.n != null && options.n > 0) return ''
      if (!options) return ''
      const target = options.target ?? ''
      const split = target === '' ? null : splitDockerProjectService(target)
      return split ? split.service : target
    }
    case 'filename-word':
      if (!options || options.overwrite !== true) return ''
      return options.filename ?? ''
    default:
      return ''
  }
}

/**
 * 弹窗形态（动态修正两个随 options 变化的例外，其余照注册表）。
 *
 * 与 expectedConfirm 的分工：expectedConfirm 给「该填什么」，confirmKind 给「用哪种
 * 弹窗/要不要弹」。判形态**不看 expectedConfirm 的结果**——目标为空时后者会返回空串，
 * 但那不代表「不用确认」。
 */
export function confirmKind(action: string, options?: DockerActionOptions | null): ConfirmKind {
  if (action === 'image:save') return options?.overwrite === true ? 'filename-word' : 'none'
  if (action === 'compose.service:scale') {
    return options?.n != null && options.n > 0 ? 'none' : 'target-word'
  }
  return lookupDockerAction(action)?.confirm ?? 'none'
}

/** 该动作（在给定 options 下）是否需要先弹确认。 */
export function needsConfirm(action: string, options?: DockerActionOptions | null): boolean {
  return confirmKind(action, options) !== 'none'
}

export interface ProtectedGateResult {
  /** 目标是否受保护（agent 算好的结论，前端不重复实现判断）。 */
  protected: boolean
  /** 当前账号此刻是否可以做（false = 按钮禁用）。 */
  allowed: boolean
  /** 弹窗里是否渲染「强制操作」开关（= 受保护且有 docker:exec 级权限）。 */
  needForce: boolean
  /** 结论句（页面直接显示；未受保护时为空串）。 */
  conclusion: string
}

/**
 * 受保护目标的额外要求（spec §10.1 / §4.3.1 的保护档）。
 *
 * 规则：目标受保护时，**先看有没有 docker:exec 权限** ——
 *   - 有：允许操作，但弹窗里必须出现「强制操作」开关（勾选后才带 `force`），
 *     且 confirm 档照旧要满足；
 *   - 没有：不允许操作（按钮禁用），给结论句。
 *
 * `force` 字段名不要拼错（协议 json tag 是 `force`）：拼错会让「强制」静默失效，
 * 而 agent 对受保护目标默认拒绝 —— 用户的体感是「勾了也没用」。
 */
export function protectedGate(
  target: { protected?: boolean } | null | undefined,
  hasExec: boolean
): ProtectedGateResult {
  if (target?.protected !== true) {
    return { protected: false, allowed: true, needForce: false, conclusion: '' }
  }
  if (hasExec) {
    return {
      protected: true,
      allowed: true,
      needForce: true,
      conclusion: '这是一个受保护的目标；需要勾选「强制操作」才能继续。'
    }
  }
  return {
    protected: true,
    allowed: false,
    needForce: false,
    conclusion: '这是一个受保护的目标，当前账号不能对它执行操作。'
  }
}
