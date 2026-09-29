/**
 * 确认弹窗的形态推导（纯函数）：把「动作注册表 + 确认档 + 目标保护」折成渲染所需的一切。
 *
 * 组件（components/action-confirm.vue）只读这里的结果 —— 四形态的差异、逐字比对的期望值、
 * 强制开关的出现条件全部集中在这一个文件里，便于单测与文案核对（D5：只讲结论）。
 */
import {
  actionGuarded,
  confirmKind,
  expectedConfirm,
  lookupDockerAction,
  protectedGate,
  type ActionDanger,
  type ConfirmKind,
  type DockerActionOptions
} from './actions'

export interface ConfirmFormInput {
  action: string
  target?: string
  options?: DockerActionOptions | null
  /** 目标是否受保护（快照里的 protected 结论 —— agent 算好的，前端不重复判断）。 */
  targetProtected?: boolean
  /** 目标的名词（容器/镜像/数据卷/网络/项目/服务），用于逐字输入提示。 */
  targetKind?: string
  /**
   * 注册表之外的动作（四期配置编辑的三条）显式给出确认描述：
   * 它们不在二期写动作注册表里（注册表只收二期），标签/结论/确认档/逐字期望值
   * 由调用方（compose-editor）按协议 §4.3.1 的行给出。
   */
  override?: ConfirmOverride
}

/** 注册表之外动作的确认描述（强确认档 = 照抄项目名，协议 ExpectedDockerConfirm）。 */
export interface ConfirmOverride {
  kind: ConfirmKind
  label: string
  conclusion?: string
  danger?: ActionDanger
  /** 逐字期望值；缺省时回退注册表推导（override 场景必须给）。 */
  expected?: string
}

export interface ConfirmForm {
  kind: ConfirmKind
  label: string
  danger: ActionDanger
  /** 该动作是否受保护档约束（agent 的 guard 覆盖面）。 */
  guarded: boolean
  /** 卡片里的目标名（无目标动作用动作标签）。 */
  targetText: string
  /** 后果结论句（注册表给的唯一文案来源）。 */
  conclusion: string
  /** 是否要逐字输入（三种 *-word 形态）。 */
  needsInput: boolean
  inputLabel: string
  inputPlaceholder: string
  /** 逐字比对的期望值（大小写敏感，一字不差）。 */
  expected: string
  buttonText: string
  /** 是否显示 🔒（目标受保护）。 */
  protected: boolean
  /** 是否渲染「强制操作」开关：受保护 **且** 有 docker:exec 级权限。 */
  showForce: boolean
  /** 保护结论句（未受保护时为空串）。 */
  protectedConclusion: string
}

/** 是否需要逐字输入（三种 *-word 形态）。 */
export function needsWordInput(kind: ConfirmKind): boolean {
  return kind === 'delete-word' || kind === 'target-word' || kind === 'filename-word'
}

/**
 * 逐字确认是否满足：强档必须一字不差（大小写敏感 —— 模糊匹配会让「照抄一遍」
 * 退化成「随便填点东西」）；标准档/无需输入形态始终通过。
 */
export function confirmInputValid(
  form: Pick<ConfirmForm, 'kind' | 'expected'>,
  input: string
): boolean {
  if (!needsWordInput(form.kind)) return true
  return input === form.expected
}

/** 主按钮文案：按形态给动词，避免用户在「删除」二字上误以为所有动作都一样。 */
function buttonTextOf(kind: ConfirmKind, label: string): string {
  if (kind === 'delete-word') return '清理'
  if (kind === 'filename-word') return '覆盖导出'
  if (kind === 'confirm') return '删除'
  return label
}

function inputLabelOf(kind: ConfirmKind, targetKind: string): string {
  if (kind === 'delete-word') return '输入 DELETE 以确认'
  if (kind === 'filename-word') return '输入文件名以确认'
  if (kind === 'target-word') return `输入${targetKind}名称以确认`
  return ''
}

function inputPlaceholderOf(kind: ConfirmKind): string {
  if (kind === 'delete-word') return 'DELETE'
  if (kind === 'target-word') return '输入完整名称'
  if (kind === 'filename-word') return '例如 backup.tar'
  return ''
}

export function confirmForm(input: ConfirmFormInput, hasExec: boolean): ConfirmForm {
  const entry = lookupDockerAction(input.action)
  const override = input.override
  const kind = override?.kind ?? confirmKind(input.action, input.options)
  const needsInput = needsWordInput(kind)
  const gate = protectedGate({ protected: input.targetProtected }, hasExec)
  const label = override?.label ?? entry?.label ?? input.action
  // 强档的期望值来自 options.target（协议判定读的就是它），而组件的 target 是独立 prop：
  // 在这里把 prop 折进 options，调用方就不必两处都传。
  const options: DockerActionOptions = { ...(input.options ?? {}) }
  if (input.target !== undefined) options.target = input.target
  return {
    kind,
    label,
    danger: override?.danger ?? entry?.danger ?? 'normal',
    guarded: actionGuarded(input.action),
    targetText: input.target || label,
    conclusion: override?.conclusion ?? entry?.conclusion ?? '',
    needsInput,
    inputLabel: inputLabelOf(kind, input.targetKind ?? ''),
    inputPlaceholder: inputPlaceholderOf(kind),
    expected: needsInput ? (override?.expected ?? expectedConfirm(input.action, options)) : '',
    buttonText: buttonTextOf(kind, label),
    protected: gate.protected,
    showForce: gate.needForce,
    protectedConclusion: gate.conclusion
  }
}
