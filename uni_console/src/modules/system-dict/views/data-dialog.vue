<template>
  <el-dialog v-model="visible" :title="form.id ? '编辑字典数据' : '新增字典数据'" width="480px">
    <el-form :model="form" label-width="80px">
      <el-form-item label="标签"><el-input v-model="form.label" /></el-form-item>
      <el-form-item label="值"><el-input v-model="form.value" /></el-form-item>
      <el-form-item label="标签样式">
        <el-select v-model="form.listClass" clearable placeholder="默认">
          <el-option v-for="c in classes" :key="c.value" :label="c.label" :value="c.value" />
        </el-select>
      </el-form-item>
      <el-form-item label="排序"><el-input-number v-model="form.sort" :min="0" /></el-form-item>
      <el-form-item label="状态">
        <el-radio-group v-model="form.status">
          <el-radio v-for="opt in statusOptions" :key="opt.value" :value="opt.value">
            {{ opt.label }}
          </el-radio>
        </el-radio-group>
      </el-form-item>
      <el-form-item label="备注"><el-input v-model="form.remark" type="textarea" /></el-form-item>
    </el-form>
    <template #footer>
      <div class="art-dialog-footer">
        <ArtButtonTable
          icon="ri:close-line"
          iconClass="bg-g-300/55 text-g-700"
          title="取消"
          @click="visible = false"
        />
        <ArtButtonTable
          icon="ri:check-line"
          iconClass="bg-theme/12 text-theme"
          title="保存"
          @click="onSave"
        />
      </div>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
  import { reactive, ref } from 'vue'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import { ElMessage } from 'element-plus'
  import { createDictData, updateDictData } from '../api'
  import { useDictStatus } from '../useDictStatus'

  const props = defineProps<{ typeId?: string }>()
  const emit = defineEmits<{ (e: 'saved'): void }>()
  const { ensure: ensureStatus, options: statusOptions } = useDictStatus()

  const visible = ref(false)
  const classes = [
    { label: '主要 · primary', value: 'primary' },
    { label: '成功 · success', value: 'success' },
    { label: '信息 · info', value: 'info' },
    { label: '警告 · warning', value: 'warning' },
    { label: '危险 · danger', value: 'danger' }
  ] as const
  const form = reactive<Api.Dict.DictDataForm & { id?: string }>({
    id: undefined,
    label: '',
    value: '',
    listClass: '',
    isDefault: 0,
    sort: 0,
    status: 1,
    remark: ''
  })

  async function open(row?: Api.Dict.DictData) {
    await ensureStatus()
    Object.assign(
      form,
      row ?? {
        id: undefined,
        label: '',
        value: '',
        listClass: '',
        isDefault: 0,
        sort: 0,
        status: 1,
        remark: ''
      }
    )
    visible.value = true
  }

  async function onSave() {
    if (form.id) {
      await updateDictData(props.typeId!, form.id, form)
    } else {
      await createDictData(props.typeId!, form)
    }
    ElMessage.success('已保存')
    visible.value = false
    emit('saved')
  }

  defineExpose({ open })
</script>
