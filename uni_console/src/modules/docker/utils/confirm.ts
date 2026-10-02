/**
 * 确认弹窗的形态推导（纯函数）：把「动作注册表 + 确认档 + 输入档 + 目标保护」折成
 * 渲染所需的一切。
 *
 * 组件（components/action-confirm.vue）只读这里的结果 —— 五形态的差异、逐字比对的
 * 期望值、输入档的描述与校验、强制开关的出现条件全部集中在这一个文件里，便于单测
 * 与文案核对（D5：只讲结论）。
 */
import {
  actionGuarded,
  confirmKind,
  expectedConfirm,
  lookupDockerAction,
  protectedGate,
  type ActionDanger,
  type ActionInputSpec,
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
   * 注册表之外的动作（配置编辑的三条 compose.file:*）显式给出确认描述：
   * 它们不在写动作注册表里（注册表只收写动作），标签/结论/确认档/逐字期望值
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
  /** 输入档的参数描述（kind='input' 时给出；缺省回退注册表条目的 input）。 */
  input?: ActionInputSpec
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
  /** 是否要输入框（三种 *-word 逐字档 + input 输入档）。 */
  needsInput: boolean
  inputLabel: string
  inputPlaceholder: string
  /** 输入框下的操作提示（输入档才有；逐字档的口径已写进 label/placeholder）。 */
  inputHint: string
  /** 输入档的参数描述（kind='input' 时存在）：校验走 validateActionInput。 */
  inputSpec?: ActionInputSpec
  /** 逐字比对的期望值（大小写敏感，一字不差；输入档恒空）。 */
  expected: string
  buttonText: string
  /** 是否显示保护标记（锁图标 + 受保护；不用 🔒 emoji —— 见 action-confirm 的注释）。 */
  protected: boolean
  /** 是否渲染「强制操作」开关：受保护 **且** 有 docker:exec 级权限。 */
  showForce: boolean
  /** 保护结论句（未受保护时为空串）。 */
  protectedConclusion: string
}

/** 是否需要逐字输入（三种 *-word 形态；输入档不是「照抄」，单判）。 */
export function needsWordInput(kind: ConfirmKind): boolean {
  return kind === 'delete-word' || kind === 'target-word' || kind === 'filename-word'
}

/**
 * 输入档校验：必填（缺省 true）拦空串/纯空白，格式交给描述的 validate。
 *
 * 入参先裁剪首尾空白 —— 空白首尾在提交时本来就要裁掉，不该左右格式判定。
 * 返回 true = 通过；返回 string = 不通过 + 就地显示的提示（与拉取对话框
 * 「校验错误就地显示」同一形态）。
 */
export function validateActionInput(spec: ActionInputSpec, value: string): true | string {
  const v = value.trim()
  if ((spec.required ?? true) && v === '') return `「${spec.label}」不能为空`
  return spec.validate ? spec.validate(v) : true
}

/**
 * 逐字/输入两档的提交闸门：强档必须一字不差（大小写敏感 —— 模糊匹配会让
 * 「照抄一遍」退化成「随便填点东西」）；输入档走 validateActionInput；
 * 标准档/无需输入形态始终通过。
 *
 * 输入档**没有描述**时保守不通过（fail-closed）：注册表数据缺口宁可按钮
 * 永远禁用（显性的接线红灯），也不放行一条缺参数的指令。
 */
export function confirmInputValid(
  form: Pick<ConfirmForm, 'kind' | 'expected' | 'inputSpec'>,
  input: string
): boolean {
  if (form.kind === 'input') {
    return form.inputSpec ? validateActionInput(form.inputSpec, input) === true : false
  }
  if (needsWordInput(form.kind)) return input === form.expected
  return true
}

/**
 * 输入档的就地错误提示（校验不通过时显示在输入框下）：通过时空串；
 * 空串不显错（空值靠按钮禁用表达就够了，不该一开口就挨骂 —— 与拉取对话框
 * 同一口径）；非输入档恒空（逐字档的反馈就是禁用按钮本身）。
 */
export function actionInputError(
  form: Pick<ConfirmForm, 'kind' | 'inputSpec'>,
  input: string
): string {
  if (form.kind !== 'input' || !form.inputSpec) return ''
  if (input.trim() === '') return ''
  const r = validateActionInput(form.inputSpec, input)
  return r === true ? '' : r
}

/**
 * 主按钮文案：按形态给动词，避免用户在「删除」二字上误以为所有动作都一样。
 * 标准档（confirm）沿用动作标签 —— 此前四条标准档恰好全是删除（label 即「删除」），
 * 4a 起 container:create 也是标准档，硬编码「删除」会让创建弹窗长出一个删除按钮；
 * 输入档同样沿用标签（打标签/导出/载入镜像 —— 收集参数的弹窗，按钮就是动作本身）。
 */
function buttonTextOf(kind: ConfirmKind, label: string): string {
  if (kind === 'delete-word') return '清理'
  if (kind === 'filename-word') return '覆盖导出'
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
  // 输入档的 label/placeholder/hint/校验全部来自参数描述（override 优先，缺省回退
  // 注册表条目）；逐字档的口径仍由 kind + targetKind 推导（见 inputLabelOf）。
  const inputSpec = kind === 'input' ? (override?.input ?? entry?.input) : undefined
  const needsInput = needsWordInput(kind) || kind === 'input'
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
    inputLabel: inputSpec ? inputSpec.label : inputLabelOf(kind, input.targetKind ?? ''),
    inputPlaceholder: inputSpec ? inputSpec.placeholder : inputPlaceholderOf(kind),
    inputHint: inputSpec?.hint ?? '',
    inputSpec,
    // 输入档的 expected 恒空（协议不要求逐字值；expectedConfirm 对 input 档返回空串）。
    expected: needsInput ? (override?.expected ?? expectedConfirm(input.action, options)) : '',
    buttonText: buttonTextOf(kind, label),
    protected: gate.protected,
    showForce: gate.needForce,
    protectedConclusion: gate.conclusion
  }
}
