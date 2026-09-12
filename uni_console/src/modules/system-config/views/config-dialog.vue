<template>
  <el-dialog v-model="visible" :title="form.id ? '编辑参数' : '新增参数'" width="520px">
    <el-form :model="form" label-width="90px">
      <el-form-item label="参数名称">
        <el-input v-model="form.name" placeholder="请输入参数名称" maxlength="64" />
      </el-form-item>
      <el-form-item label="参数键">
        <el-input
          v-model="form.configKey"
          placeholder="如 sys.user.initPassword"
          :disabled="!!form.id"
        />
      </el-form-item>
      <el-form-item label="参数值">
        <el-input v-model="form.configValue" placeholder="请输入参数值" />
      </el-form-item>
      <el-form-item label="类型">
        <el-select v-model="form.configType" style="width: 100%">
          <el-option v-for="t in typeOptions" :key="t.value" :label="t.label" :value="t.value" />
        </el-select>
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
  import { useDict } from '@/hooks/core/useDict'
  import { createConfig, updateConfig } from '../api'

  const emit = defineEmits<{ (e: 'saved'): void }>()
  const { ensure: ensureStatus, options: statusOptions } = useDict('sys_config_status', {
    numeric: true
  })

  const typeOptions = [
    { label: '字符串', value: 'S' },
    { label: '数值', value: 'N' },
    { label: '布尔', value: 'B' },
    { label: 'JSON', value: 'J' }
  ]

  const visible = ref(false)
  const form = reactive<Api.Config.Form>({
    id: undefined,
    name: '',
    configKey: '',
    configValue: '',
    configType: 'S',
    status: 1,
    remark: ''
  })

  async function open(row?: Api.Config.Config) {
    await ensureStatus()
    Object.assign(
      form,
      row ?? {
        id: undefined,
        name: '',
        configKey: '',
        configValue: '',
        configType: 'S',
        status: 1,
        remark: ''
      }
    )
    visible.value = true
  }

  async function onSave() {
    if (!form.name.trim() || !form.configKey?.trim() || !form.configValue.trim()) {
      ElMessage.warning('请填写参数名称、参数键和参数值')
      return
    }
    if (form.id) {
      await updateConfig(form.id, form)
    } else {
      await createConfig(form)
    }
    ElMessage.success('已保存')
    visible.value = false
    emit('saved')
  }

  defineExpose({ open })
</script>
