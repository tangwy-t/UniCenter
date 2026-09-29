/**
 * 确认弹窗的形态/校验/强制开关测试。
 *
 * ── 为什么测的是纯函数而不是挂载组件 ────────────────────────────────
 * 与 log-viewer 同一约定：vitest 环境是 node，没有 jsdom / @vue/test-utils，组件不挂载；
 * 于是把弹窗的全部决策（四形态、逐字期望值、输入校验、强制开关出现条件）下沉到
 * `utils/confirm.ts` 的纯函数里，模板只做绑定。本文件最后用源码扫描钉住「模板确实
 * 绑定了这些决策」这一层，避免纯函数测绿而组件没接线。
 */
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { confirmForm, confirmInputValid, needsWordInput } from '../utils/confirm'

const CONFIRM_VUE = new URL('../components/action-confirm.vue', import.meta.url).pathname
const source = readFileSync(CONFIRM_VUE, 'utf8')

describe('四形态（普通确认 / DELETE / 目标名 / 文件名）', () => {
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
  it('save 第一段：不带 overwrite 不要求任何输入', () => {
    const form = confirmForm({ action: 'image:save', options: { filename: 'mysql.tar' } }, false)
    expect(form.kind).toBe('none')
    expect(form.expected).toBe('')
  })

  it('save 第二段：overwrite=true 才要求文件名', () => {
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
    confirmForm({ action: 'volume:remove', target: 'uploads', targetProtected: true }, true)
  ]

  it('不出现权限码/字段名/协议术语', () => {
    for (const form of forms) {
      const text = [
        form.label,
        form.targetText,
        form.conclusion,
        form.inputLabel,
        form.inputPlaceholder,
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
})
