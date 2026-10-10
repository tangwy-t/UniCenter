// @vitest-environment jsdom
/**
 * ArtSearchBar「分组选项」追加能力的守卫（Wave 4a-2）。
 *
 * 两个方向：
 *  ④ 扁平用法不回归 —— options 不带 `group` 时仍渲染为**平铺 ElOption**
 *      （既有页面行为逐字不变）；这是「追加式」的红线。
 *  ⑤ 分组用法 —— options 带 `group` 时用 ElOptionGroup 包起来渲染，
 *     组序按首次出现、组内保序，组的 label = option.group。
 *
 * 手法：挂载真 ArtSearchBar，把 EP 的表单/下拉组件换成轻量替身
 * （保留 default 槽），从而在 DOM 里直接数 ElOptionGroup / ElOption 的替身。
 */
import { defineComponent, h, ref } from 'vue'
import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

vi.mock('@/hooks/core/useAppBreakpoints', () => ({
  useAppBreakpoints: () => ({ smaller: () => ref(false), greaterOrEqual: () => ref(false) })
}))

import ArtSearchBar from './index.vue'

/** 透明替身：渲染默认槽（让嵌套的选项替身露在 DOM 里）。 */
const slotStub = (name: string) =>
  defineComponent({
    name,
    setup:
      (_, { slots }) =>
      () =>
        h('div', { 'data-comp': name }, slots.default?.())
  })

/** ElOptionGroup 替身：把 label 透成可断言的属性。 */
const optionGroupStub = defineComponent({
  name: 'ElOptionGroup',
  props: { label: { type: String, default: '' } },
  setup:
    (p, { slots }) =>
    () =>
      h('div', { 'data-comp': 'ElOptionGroup', 'data-group': p.label }, slots.default?.())
})

/** ElOption 替身：把 label 渲染成可断言的文本。 */
const optionStub = defineComponent({
  name: 'ElOption',
  props: {
    label: { type: [String, Number], default: '' },
    value: { type: [String, Number, Boolean], default: '' }
  },
  setup: (p) => () => h('span', { 'data-option': String(p.label) })
})

const STUBS = {
  ElForm: slotStub('ElForm'),
  ElRow: slotStub('ElRow'),
  ElCol: slotStub('ElCol'),
  ElFormItem: slotStub('ElFormItem'),
  ElSelect: slotStub('ElSelect'),
  ElOption: optionStub,
  ElOptionGroup: optionGroupStub
}

function mountBar(options: Record<string, unknown>[]) {
  return mount(ArtSearchBar, {
    props: {
      items: [{ key: 'host', label: '主机', type: 'select', options }],
      modelValue: {}
    },
    global: { stubs: STUBS }
  })
}

describe('ArtSearchBar 扁平用法不回归（④）', () => {
  it('不带 group 的 options：平铺 ElOption，不出现 ElOptionGroup', () => {
    const w = mountBar([
      { label: 'bogon', value: 'h1' },
      { label: 'nas', value: 'h2' }
    ])
    expect(w.findAll('[data-comp="ElOptionGroup"]')).toHaveLength(0)
    const opts = w.findAll('[data-option]')
    expect(opts.map((o) => o.attributes('data-option'))).toEqual(['bogon', 'nas'])
  })

  it('空 options 同样不出现分组', () => {
    const w = mountBar([])
    expect(w.findAll('[data-comp="ElOptionGroup"]')).toHaveLength(0)
    expect(w.findAll('[data-option]')).toHaveLength(0)
  })
})

describe('ArtSearchBar 分组用法（⑤）', () => {
  it('带 group 的 options：每组装进一个 ElOptionGroup，选项总数不变', () => {
    const w = mountBar([
      { label: 'bogon', value: 'h1', group: '机房A' },
      { label: 'nas', value: 'h2', group: '机房B' },
      { label: 'edge', value: 'h3', group: '机房A' }
    ])
    const groups = w.findAll('[data-comp="ElOptionGroup"]')
    // 两个不同的组 → 两个 ElOptionGroup（组序按首次出现）
    expect(groups).toHaveLength(2)
    // 选项总数不变（分组只是包了一层），且组内保序、组间按首次出现
    expect(w.findAll('[data-option]').map((o) => o.attributes('data-option'))).toEqual([
      'bogon',
      'edge',
      'nas'
    ])
  })

  it('组的 label 来自 option.group（组序 = 首次出现顺序）', () => {
    const w = mountBar([
      { label: 'bogon', value: 'h1', group: '机房A' },
      { label: 'nas', value: 'h2', group: '未分组' }
    ])
    const groups = w.findAll('[data-comp="ElOptionGroup"]')
    expect(groups.map((g) => g.attributes('data-group'))).toEqual(['机房A', '未分组'])
  })
})
