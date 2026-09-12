/**
 * 字典 store：按 code 懒加载字典项并缓存
 */
import { defineStore } from 'pinia'
import { ref } from 'vue'
import { fetchDictByCode } from '@/api/dict'

export const useDictStore = defineStore('dictStore', () => {
  const items = ref<Record<string, Api.Dict.DictItem[]>>({})

  const load = async (code: string) => {
    if (items.value[code]) return items.value[code]
    const list = await fetchDictByCode(code)
    items.value[code] = list
    return list
  }

  const label = (code: string, value: string): string =>
    items.value[code]?.find((i) => i.value === value)?.label ?? value

  return { items, load, label }
})
