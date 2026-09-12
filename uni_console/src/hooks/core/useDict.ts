/**
 * useDict：通用字典消费组合式。
 *
 * 按字典 code 懒加载并在 store 缓存，提供：
 * - options  ：表单下拉/单选项（可选数值型 value）
 * - labelOf  ：value → label
 * - render   ：value → 带颜色(tag type)的 el-tag VNode（颜色取 list_class）
 *
 * 用于列表中把某字段按字典渲染成彩色标签，或表单中按字典生成选项。
 */
import { ref, computed, h } from 'vue'
import { ElTag } from 'element-plus'
import { useDictStore } from '@/store/modules/dict'

const TAG_TYPES = ['primary', 'success', 'info', 'warning', 'danger'] as const
type TagType = (typeof TAG_TYPES)[number]

interface UseDictOptions {
  /** value 转为数字（用于绑定 int8 状态字段，如 status 1/0） */
  numeric?: boolean
}

export function useDict(code: string, opts: UseDictOptions = {}) {
  const dictStore = useDictStore()
  const items = ref<Api.Dict.DictItem[]>([])

  async function ensure() {
    items.value = await dictStore.load(code)
  }

  const options = computed(() =>
    items.value.map((i) => ({ label: i.label, value: opts.numeric ? Number(i.value) : i.value }))
  )

  function labelOf(value: string | number | undefined | null): string {
    if (value === undefined || value === null) return ''
    const item = items.value.find((i) => i.value === String(value))
    return item?.label ?? String(value)
  }

  function toTagType(cls: string): TagType {
    return (TAG_TYPES as readonly string[]).includes(cls) ? (cls as TagType) : 'info'
  }

  /** value → 字典 list_class 对应的 el-tag type（无匹配时回退 info） */
  function tagType(value: string | number | undefined | null): TagType {
    if (value === undefined || value === null) return 'info'
    const item = items.value.find((i) => i.value === String(value))
    return toTagType(item?.list_class ?? '')
  }

  function render(value: string | number | undefined | null) {
    if (value === undefined || value === null) return ''
    const item = items.value.find((i) => i.value === String(value))
    return h(
      ElTag,
      { type: toTagType(item?.list_class ?? ''), effect: 'light' },
      () => item?.label ?? String(value)
    )
  }

  return { ensure, options, labelOf, render, tagType }
}
