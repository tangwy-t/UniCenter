<template>
  <el-dialog v-model="visible" :title="form.id ? '编辑字典类型' : '新增字典类型'" width="480px">
    <el-form :model="form" label-width="80px">
      <el-form-item label="名称">
        <el-input v-model="form.name" placeholder="请输入字典名称" />
      </el-form-item>
      <el-form-item label="编码">
        <el-input
          v-model="form.code"
          placeholder="小写字母开头的字母/数字/下划线"
          :disabled="!!form.id"
        />
      </el-form-item>
      <el-form-item label="状态">
        <el-radio-group v-model="form.status">
          <el-radio v-for="opt in statusOptions" :key="opt.value" :value="opt.value">
            {{ opt.label }}
          </el-radio>
        </el-radio-group>
      </el-form-item>
      <el-form-item label="备注">
        <el-input v-model="form.remark" type="textarea" placeholder="请输入备注" />
      </el-form-item>
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
  import { createDictType, updateDictType } from '../api'
  import { useDictStatus } from '../useDictStatus'

  const emit = defineEmits<{ (e: 'saved'): void }>()
  const { ensure: ensureStatus, options: statusOptions } = useDictStatus()

  const visible = ref(false)
  const form = reactive<Api.Dict.DictTypeForm & { id?: string }>({
    id: undefined,
    code: '',
    name: '',
    status: 1,
    remark: ''
  })

  async function open(row?: Api.Dict.DictType) {
    await ensureStatus()
    Object.assign(form, row ?? { id: undefined, code: '', name: '', status: 1, remark: '' })
    visible.value = true
  }

  async function onSave() {
    if (!form.name.trim() || !form.code.trim()) {
      ElMessage.warning('请填写名称和编码')
      return
    }
    if (!/^[a-z][a-z0-9_]*$/.test(form.code)) {
      ElMessage.warning('编码须为小写字母开头的字母/数字/下划线')
      return
    }
    if (form.id) {
      await updateDictType(form.id, form)
    } else {
      await createDictType(form)
    }
    ElMessage.success('已保存')
    visible.value = false
    emit('saved')
  }

  defineExpose({ open })
</script>
