<template>
  <el-tag :type="tagType" effect="light">{{ text }}</el-tag>
</template>

<script setup lang="ts">
  import { computed, onMounted, ref } from 'vue'
  import { useDictStore } from '@/store/modules/dict'

  const props = defineProps<{ code: string; value: string; listClass?: string }>()

  const dict = useDictStore()
  const item = ref<Api.Dict.DictItem | null>(null)

  onMounted(async () => {
    const list = await dict.load(props.code)
    item.value = list.find((i) => i.value === props.value) ?? null
  })

  const text = computed(() => item.value?.label ?? props.value)
  const tagType = computed<'primary' | 'success' | 'info' | 'warning' | 'danger'>(() => {
    const cls = (item.value?.list_class || props.listClass || '') as string
    return ['primary', 'success', 'info', 'warning', 'danger'].includes(cls)
      ? (cls as 'primary' | 'success' | 'info' | 'warning' | 'danger')
      : 'info'
  })
</script>
