<template>
  <el-select v-model="model" :placeholder="placeholder" clearable>
    <el-option v-for="i in list" :key="i.value" :label="i.label" :value="i.value" />
  </el-select>
</template>

<script setup lang="ts">
  import { onMounted, ref } from 'vue'
  import { useDictStore } from '@/store/modules/dict'

  const props = defineProps<{ code: string; placeholder?: string }>()
  const model = defineModel<string>()

  const dict = useDictStore()
  const list = ref<Api.Dict.DictItem[]>([])

  onMounted(async () => {
    list.value = await dict.load(props.code)
  })
</script>
