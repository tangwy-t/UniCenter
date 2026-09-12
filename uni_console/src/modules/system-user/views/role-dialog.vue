<template>
  <el-dialog v-model="visible" :title="`分配角色 - ${user?.username ?? ''}`" width="440px">
    <el-form :model="form" label-width="70px">
      <el-form-item label="角色">
        <el-select v-model="form.roleIds" multiple placeholder="请选择角色" style="width: 100%">
          <el-option v-for="r in roles" :key="r.id" :label="r.name" :value="r.id" />
        </el-select>
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
  import { fetchAllRoles, assignUserRoles } from '../api'

  const emit = defineEmits<{ (e: 'saved'): void }>()

  const visible = ref(false)
  const user = ref<Api.System.User>()
  const roles = ref<Api.System.Role[]>([])
  const form = reactive<{ roleIds: string[] }>({ roleIds: [] })

  async function open(row: Api.System.User) {
    user.value = row
    roles.value = await fetchAllRoles()
    // 优先使用后端返回的 roleIds 精确回填；旧接口无 roleIds 时按名称兜底匹配
    form.roleIds = row.roleIds?.length
      ? [...row.roleIds]
      : roles.value.filter((r) => row.roleNames?.includes(r.name)).map((r) => r.id)
    visible.value = true
  }

  async function onSave() {
    await assignUserRoles(user.value!.id, form.roleIds)
    ElMessage.success('已保存')
    visible.value = false
    emit('saved')
  }

  defineExpose({ open })
</script>
