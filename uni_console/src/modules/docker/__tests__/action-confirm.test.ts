/**
 * 确认弹窗的形态/校验/强制开关测试。
 *
 * ── 为什么测的是纯函数而不是挂载组件 ────────────────────────────────
 * 与 log-viewer 同一约定：vitest 环境是 node，没有 jsdom / @vue/test-utils，组件不挂载；
 * 于是把弹窗的全部决策（五形态、逐字期望值、输入档描述与校验、强制开关出现条件）
 * 下沉到 `utils/confirm.ts` 的纯函数里，模板只做绑定。本文件最后用源码扫描钉住
 * 「模板确实绑定了这些决策」这一层，避免纯函数测绿而组件没接线。
 */
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import {
  actionInputError,
  confirmForm,
  confirmInputValid,
  needsWordInput,
  validateActionInput
} from '../utils/confirm'

const CONFIRM_VUE = new URL('../components/action-confirm.vue', import.meta.url).pathname
const source = readFileSync(CONFIRM_VUE, 'utf8')

describe('五形态（普通确认 / DELETE / 目标名 / 文件名 / 输入）', () => {
  it('标准档：普通确认，无输入框，主按钮是删除', () => {
    const form = confirmForm({ action: 'container:remove', target: 'mysql' }, false)
    expect(form.kind).toBe('confirm')
    expect(form.needsInput).toBe(false)
    expect(form.expected).toBe('')
    expect(form.targetText).toBe('mysql')
    expect(form.buttonText).toBe('删除')
    expect(form.conclusion).not.toBe('')
    expect(confirmInputValid(form, '')).toBe(true)
  })

  it('DELETE 档：逐字输入固定文本，主按钮是清理', () => {
    const form = confirmForm({ action: 'image:prune', targetKind: '镜像' }, false)
    expect(form.kind).toBe('delete-word')
    expect(form.needsInput).toBe(true)
    expect(form.expected).toBe('DELETE')
    expect(form.inputPlaceholder).toBe('DELETE')
    expect(form.inputLabel).toContain('DELETE')
    expect(form.buttonText).toBe('清理')
    expect(form.conclusion).not.toBe('')
  })

  it('目标名档：项目级动作照抄项目名，主按钮沿用动作标签', () => {
    const form = confirmForm(
      { action: 'compose:down', target: 'uni-center', targetKind: '项目' },
      false
    )
    expect(form.kind).toBe('target-word')
    expect(form.expected).toBe('uni-center')
    expect(form.inputLabel).toBe('输入项目名称以确认')
    expect(form.buttonText).toBe('停止并移除项目')
  })

  it('目标名档：「项目/服务」形态取服务部分（含 scale 缩容到 0）', () => {
    const form = confirmForm(
      {
        action: 'compose.service:scale',
        target: 'uni-center/core',
        options: { n: 0 },
        targetKind: '服务'
      },
      false
    )
    expect(form.kind).toBe('target-word')
    expect(form.expected).toBe('core')
    expect(form.inputLabel).toBe('输入服务名称以确认')
  })

  it('目标名档：仅删容器同样照抄服务名', () => {
    const form = confirmForm(
      { action: 'compose.service:remove-containers', target: 'uni-center/core' },
      false
    )
    expect(form.kind).toBe('target-word')
    expect(form.expected).toBe('core')
  })

  it('文件名档：导出的覆盖重发照抄文件名，主按钮是覆盖导出', () => {
    const form = confirmForm(
      {
        action: 'image:save',
        target: 'mysql:8.0',
        options: { filename: 'mysql.tar', overwrite: true }
      },
      false
    )
    expect(form.kind).toBe('filename-word')
    expect(form.needsInput).toBe(true)
    expect(form.expected).toBe('mysql.tar')
    expect(form.buttonText).toBe('覆盖导出')
    expect(form.conclusion).not.toBe('')
  })

  it('无确认档：needsInput=false（不弹窗的动作即使被打开也不要求输入）', () => {
    const form = confirmForm({ action: 'container:start', target: 'mysql' }, false)
    expect(form.kind).toBe('none')
    expect(needsWordInput(form.kind)).toBe(false)
    expect(form.needsInput).toBe(false)
    expect(confirmInputValid(form, '随便填')).toBe(true)
  })
})

describe('image:save 两段 / scale n>0 不弹确认', () => {
  it('save 第一段：不带 overwrite 是输入档（收集文件名），无逐字期望', () => {
    const form = confirmForm({ action: 'image:save', options: { filename: 'mysql.tar' } }, false)
    expect(form.kind).toBe('input')
    expect(form.needsInput).toBe(true)
    expect(form.expected).toBe('')
    expect(form.inputLabel).toBe('文件名')
    expect(form.inputPlaceholder).toBe('例如 镜像名.tar')
    expect(form.inputHint).toContain('agent 下载目录')
  })

  it('save 第二段：overwrite=true 才要求照抄文件名', () => {
    const form = confirmForm(
      { action: 'image:save', options: { filename: 'mysql.tar', overwrite: true } },
      false
    )
    expect(form.kind).toBe('filename-word')
    expect(form.expected).toBe('mysql.tar')
  })

  it('scale n>0 是可逆常规操作：完全没有输入要求', () => {
    const form = confirmForm(
      { action: 'compose.service:scale', target: 'p/s', options: { n: 3 } },
      false
    )
    expect(form.kind).toBe('none')
    expect(form.needsInput).toBe(false)
  })
})

describe('输入档（收集新参数，无逐字确认值）', () => {
  it('打标签：label/placeholder 来自条目 input，expected 恒空，主按钮沿用动作标签', () => {
    const form = confirmForm({ action: 'image:tag', target: 'mysql:8.0' }, false)
    expect(form.kind).toBe('input')
    expect(form.needsInput).toBe(true)
    expect(form.inputLabel).toBe('新的镜像引用')
    expect(form.inputPlaceholder).toBe('例如 仓库/名称:标签')
    expect(form.inputHint).toBe('')
    expect(form.expected).toBe('')
    expect(form.buttonText).toBe('打标签')
    expect(form.targetText).toBe('mysql:8.0')
  })

  it('载入镜像：无目标（targetText 回退动作标签），hint 讲清文件须在 agent 下载目录', () => {
    const form = confirmForm({ action: 'image:load' }, false)
    expect(form.kind).toBe('input')
    expect(form.targetText).toBe('载入镜像')
    expect(form.inputLabel).toBe('文件名')
    expect(form.inputHint).toBe('只填文件名，文件需已放在该主机的 agent 下载目录。')
  })

  it('image:save 第一段的 hint：导出落点那句原样在（原 prompt 正文不丢口径）', () => {
    const form = confirmForm({ action: 'image:save', target: 'mysql:8.0' }, false)
    expect(form.kind).toBe('input')
    expect(form.inputHint).toBe('只填文件名，产物落在该主机的 agent 下载目录。')
  })

  it('打标签校验：非法引用/空串都不通过（禁提交）', () => {
    const form = confirmForm({ action: 'image:tag' }, false)
    expect(confirmInputValid(form, 'uni-center/core:v2')).toBe(true)
    expect(confirmInputValid(form, 'nginx')).toBe(true)
    expect(confirmInputValid(form, 'has space')).toBe(false)
    expect(confirmInputValid(form, '不合法!')).toBe(false)
    expect(confirmInputValid(form, '')).toBe(false)
    expect(confirmInputValid(form, '   ')).toBe(false)
  })

  it('文件名校验：只拦空值（沿用原 prompt 口径），任意非空文件名都放行', () => {
    const form = confirmForm({ action: 'image:save', target: 'mysql:8.0' }, false)
    expect(confirmInputValid(form, 'mysql.tar')).toBe(true)
    expect(confirmInputValid(form, 'my file.tar.gz')).toBe(true) // 格式不前端拦（服务端兜）
    expect(confirmInputValid(form, '')).toBe(false)
    expect(confirmInputValid(form, '  ')).toBe(false)
  })

  it('就地错误：非空但不合法时给提示句；空串不显（不该一开口就挨骂）', () => {
    const form = confirmForm({ action: 'image:tag' }, false)
    expect(actionInputError(form, '不合法!')).toContain('镜像引用')
    expect(actionInputError(form, 'has space')).not.toBe('')
    // 空值不显错：靠按钮禁用表达即可（与拉取对话框同一口径）。
    expect(actionInputError(form, '')).toBe('')
    expect(actionInputError(form, '   ')).toBe('')
    // 合法值没有错误句。
    expect(actionInputError(form, 'nginx')).toBe('')
  })

  it('必填错误句由 label 拼出（给校验函数兜底：不合法格式也会得到提示）', () => {
    const spec = { label: '文件名', placeholder: 'p' }
    expect(validateActionInput(spec, '')).toBe('「文件名」不能为空')
    expect(validateActionInput(spec, '  ')).toBe('「文件名」不能为空')
    expect(validateActionInput(spec, 'a.tar')).toBe(true)
    // 必填关掉时空串也放行（描述可以声明可选输入）。
    expect(validateActionInput({ ...spec, required: false }, '')).toBe(true)
  })

  it('非输入档（逐字/标准/无档）恒无就地错误：反馈形态就是禁用按钮本身', () => {
    expect(actionInputError(confirmForm({ action: 'image:prune' }, false), 'delete')).toBe('')
    expect(actionInputError(confirmForm({ action: 'container:remove' }, false), 'x')).toBe('')
    expect(actionInputError(confirmForm({ action: 'container:start' }, false), '')).toBe('')
  })

  it('声明 input 档却没有参数描述：保守不通过（数据缺口以禁用按钮暴露，不放行缺参数指令）', () => {
    const form = confirmForm(
      { action: 'compose.file:patch', override: { kind: 'input', label: 'x' } },
      false
    )
    expect(form.kind).toBe('input')
    expect(form.inputSpec).toBeUndefined()
    expect(confirmInputValid(form, '任何值')).toBe(false)
  })

  it('override 声明 input 描述：形态与校验走 override 给的（注册表外动作同一条路）', () => {
    const form = confirmForm(
      {
        action: 'compose.file:patch',
        override: {
          kind: 'input',
          label: '保存配置',
          input: { label: '备注', placeholder: '例如 发布前基线', required: true }
        }
      },
      false
    )
    expect(form.kind).toBe('input')
    expect(form.inputLabel).toBe('备注')
    expect(form.inputSpec?.label).toBe('备注')
    expect(confirmInputValid(form, '发布前基线')).toBe(true)
    expect(confirmInputValid(form, '')).toBe(false)
  })
})

describe('逐字校验（输入不匹配时主按钮禁用）', () => {
  const deleteForm = confirmForm({ action: 'image:prune' }, false)
  const targetForm = confirmForm({ action: 'compose:down', target: 'uni-center' }, false)
  const fileForm = confirmForm(
    { action: 'image:save', options: { filename: 'a.tar', overwrite: true } },
    false
  )

  it('DELETE 档：只有逐字一致才通过；大小写/空白都不算', () => {
    expect(confirmInputValid(deleteForm, 'DELETE')).toBe(true)
    expect(confirmInputValid(deleteForm, 'delete')).toBe(false)
    expect(confirmInputValid(deleteForm, 'DELETE ')).toBe(false)
    expect(confirmInputValid(deleteForm, '')).toBe(false)
  })

  it('目标名档：一字不差（大小写敏感）', () => {
    expect(confirmInputValid(targetForm, 'uni-center')).toBe(true)
    expect(confirmInputValid(targetForm, 'Uni-Center')).toBe(false)
    expect(confirmInputValid(targetForm, 'uni')).toBe(false)
  })

  it('文件名档：一字不差', () => {
    expect(confirmInputValid(fileForm, 'a.tar')).toBe(true)
    expect(confirmInputValid(fileForm, 'a.tar ')).toBe(false)
    expect(confirmInputValid(fileForm, 'A.TAR')).toBe(false)
  })

  it('标准档不受输入影响（没有输入框，主按钮始终可提交）', () => {
    const form = confirmForm({ action: 'network:remove', target: 'app-net' }, false)
    expect(confirmInputValid(form, '')).toBe(true)
    expect(confirmInputValid(form, 'x')).toBe(true)
  })
})

describe('强制开关（受保护目标 + docker:exec 级权限）', () => {
  it('未受保护：不渲染强制开关，即使有强制权限', () => {
    for (const hasExec of [true, false]) {
      const form = confirmForm({ action: 'container:remove', target: 'mysql' }, hasExec)
      expect(form.protected).toBe(false)
      expect(form.showForce).toBe(false)
      expect(form.protectedConclusion).toBe('')
    }
  })

  it('受保护 + 有强制权限：渲染开关与结论句', () => {
    const form = confirmForm(
      { action: 'container:remove', target: 'uni-center-core', targetProtected: true },
      true
    )
    expect(form.protected).toBe(true)
    expect(form.showForce).toBe(true)
    expect(form.protectedConclusion).not.toBe('')
  })

  it('受保护 + 无强制权限：不渲染开关，给不能操作的结论句', () => {
    const form = confirmForm(
      { action: 'container:remove', target: 'uni-center-core', targetProtected: true },
      false
    )
    expect(form.protected).toBe(true)
    expect(form.showForce).toBe(false)
    expect(form.protectedConclusion).not.toBe('')
  })
})

describe('文案只讲结论（D5）', () => {
  const forms = [
    confirmForm({ action: 'container:remove', target: 'mysql' }, false),
    confirmForm({ action: 'image:prune' }, false),
    confirmForm({ action: 'compose:down', target: 'uni-center' }, false),
    confirmForm({ action: 'image:save', options: { filename: 'a.tar', overwrite: true } }, false),
    confirmForm({ action: 'volume:remove', target: 'uploads', targetProtected: true }, true),
    // 输入档的两组新文案（标签/提示/就地错误句）一并进扫词面。
    confirmForm({ action: 'image:tag', target: 'mysql:8.0' }, false),
    confirmForm({ action: 'image:load' }, false)
  ]

  it('不出现权限码/字段名/协议术语', () => {
    for (const form of forms) {
      const text = [
        form.label,
        form.targetText,
        form.conclusion,
        form.inputLabel,
        form.inputPlaceholder,
        form.inputHint,
        form.buttonText,
        form.protectedConclusion
      ].join('\n')
      for (const term of [
        'docker:',
        'payload',
        'force',
        'confirm',
        'stale',
        'sha256',
        'alreadyExists'
      ]) {
        expect(text, `${form.kind} 形态泄露了内部术语「${term}」`).not.toContain(term)
      }
    }
  })

  it('输入档的必填错误句与格式错误句也不泄露内部术语', () => {
    const tag = confirmForm({ action: 'image:tag' }, false)
    for (const r of [
      validateActionInput({ label: '文件名', placeholder: 'p' }, ''),
      typeof tag.inputSpec?.validate === 'function' ? tag.inputSpec.validate('不合法!') : ''
    ]) {
      const text = r === true ? '' : String(r)
      for (const term of ['docker:', 'payload', 'action', 'options', 'dst', 'src']) {
        expect(text, `输入档错误句泄露了内部术语「${term}」`).not.toContain(term)
      }
    }
  })
})

describe('注册表之外的动作（四期配置编辑）用 override 显式给出确认描述', () => {
  // 四期三条不在二期写动作注册表里，标签/结论/确认档/逐字期望值都由调用方给出
  //（协议 §4.3.1：compose.file:write/patch 是「强 = 照抄项目名」）。
  const override = {
    kind: 'target-word' as const,
    label: '保存配置',
    conclusion: '原文件会先备份，再写入新内容。',
    expected: 'uni-center'
  }

  it('override 生效：强确认照抄项目名与调用方文案', () => {
    const form = confirmForm(
      { action: 'compose.file:patch', target: 'uni-center', targetKind: '项目', override },
      false
    )
    expect(form.kind).toBe('target-word')
    expect(form.needsInput).toBe(true)
    expect(form.expected).toBe('uni-center')
    expect(form.label).toBe('保存配置')
    expect(form.conclusion).toContain('备份')
    expect(form.buttonText).toBe('保存配置')
    expect(confirmInputValid(form, 'uni-center')).toBe(true)
    expect(confirmInputValid(form, 'Uni-Center')).toBe(false)
  })

  it('不给 override 时注册表查不到 → 不弹输入（调用方必须显式给出）', () => {
    const form = confirmForm({ action: 'compose.file:patch', target: 'uni-center' }, false)
    expect(form.kind).toBe('none')
    expect(form.needsInput).toBe(false)
  })
})

describe('组件模板确实绑定了这些决策（防纯函数测绿而组件没接线）', () => {
  it('输入框/强制开关/禁用逻辑都来自 form 与 canSubmit', () => {
    expect(source).toContain('v-if="form.needsInput"')
    expect(source).toContain('v-if="form.showForce"')
    expect(source).toContain(':disabled="!canSubmit"')
    expect(source).toContain('confirmInputValid(form.value, input.value)')
    expect(source).toContain('hasAuth(PermDockerExec)')
    expect(source).toContain('form.protected')
  })

  it('输入档的 hint/就地错误/值回传/两段式切档清空都接线在组件里', () => {
    // hint 与就地错误：模板绑定 form.inputHint / inputError（纯函数推导）。
    expect(source).toContain('v-if="form.inputHint"')
    expect(source).toContain('v-if="inputError"')
    expect(source).toContain('actionInputError(form.value, input.value)')
    // 提交载荷：输入档的值经 value 回传（裁剪），逐字档仍走 confirm 原样比对。
    expect(source).toContain("value: form.value.kind === 'input' ? input.value.trim() : undefined")
    // save 两段式在同一只弹窗里切档：切档必须清空输入（第二段要重新照抄）。
    expect(source).toContain('() => [form.value.kind, form.value.expected]')
    expect(source).toContain('needsWordInput(form.value.kind)')
  })
})
