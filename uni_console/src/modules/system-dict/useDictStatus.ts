/**
 * 状态字典：统一用 sys_dict_status 字典渲染“启用|禁用”状态。
 *
 * sys_dict_status 是“字典类型/字典数据”状态字段的唯一展示来源：
 * - 列表展示：render(status) 输出带颜色的标签（文字取 label，颜色取 list_class）
 * - 表单选项：options 输出启用/禁用的下拉/单选选项
 */
import { ref, computed, h } from 'vue'
import { ElTag } from 'element-plus'
import { useDictStore } from '@/store/modules/dict'

const STATUS_DICT_CODE = 'sys_dict_status'
const TAG_TYPES = ['primary', 'success', 'info', 'warning', 'danger'] as const
type TagType = (typeof TAG_TYPES)[number]

export function useDictStatus() {
  const dictStore = useDictStore()
  const items = ref<Api.Dict.DictItem[]>([])

  // 预加载 sys_dict_status 字典（store 有缓存，重复调用开销很小）
  async function ensure() {
    items.value = await dictStore.load(STATUS_DICT_CODE)
  }

  // 表单选项：value 转为数字，方便直接绑定 form.status（1/0）
  const options = computed(() =>
    items.value.map((i) => ({ label: i.label, value: Number(i.value) }))
  )

  function toTagType(cls: string): TagType {
    return (TAG_TYPES as readonly string[]).includes(cls) ? (cls as TagType) : 'info'
  }

  // 根据状态码（1/0）渲染标签；没有对应字典项时回落为“正常/停用”
  function render(status: number) {
    const item = items.value.find((i) => i.value === String(status))
    return h(
      ElTag,
      { type: toTagType(item?.list_class ?? ''), effect: 'light' },
      () => item?.label ?? (status === 1 ? '正常' : '停用')
    )
  }

  return { ensure, render, options }
}
