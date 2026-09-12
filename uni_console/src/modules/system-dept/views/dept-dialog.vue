<template>
  <el-dialog v-model="visible" :title="form.id ? '编辑部门' : '新增部门'" width="680px">
    <el-form :model="form" label-width="92px">
      <el-row :gutter="12">
        <el-col :span="24">
          <el-form-item label="上级部门">
            <el-tree-select
              v-model="form.parentId"
              :data="parentTree"
              :props="{ label: 'label', children: 'children' }"
              node-key="value"
              check-strictly
              clearable
              placeholder="顶级部门"
              style="width: 100%"
            />
          </el-form-item>
        </el-col>

        <el-col :span="12">
          <el-form-item label="部门名称">
            <el-input v-model="form.name" placeholder="请输入部门名称" />
          </el-form-item>
        </el-col>
        <el-col :span="12">
          <el-form-item label="负责人">
            <el-input v-model="form.leader" placeholder="请输入负责人" />
          </el-form-item>
        </el-col>

        <el-col :span="12">
          <el-form-item label="联系电话">
            <el-input v-model="form.phone" placeholder="请输入联系电话" />
          </el-form-item>
        </el-col>
        <el-col :span="12">
          <el-form-item label="邮箱">
            <el-input v-model="form.email" placeholder="请输入邮箱" />
          </el-form-item>
        </el-col>

        <el-col :span="12">
          <el-form-item label="排序">
            <el-input-number
              v-model="form.sort"
              :min="0"
              controls-position="right"
              style="width: 100%"
            />
          </el-form-item>
        </el-col>
        <el-col :span="12">
          <el-form-item label="状态">
            <el-radio-group v-model="form.status">
              <el-radio v-for="opt in statusOptions" :key="opt.value" :value="opt.value">
                {{ opt.label }}
              </el-radio>
            </el-radio-group>
          </el-form-item>
        </el-col>
      </el-row>
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
  import { computed, reactive, ref } from 'vue'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import { ElMessage } from 'element-plus'
  import { useDict } from '@/hooks/core/useDict'
  import { fetchDepts, createDept, updateDept } from '../api'

  const emit = defineEmits<{ (e: 'saved'): void }>()
  const { ensure: ensureStatus, options: statusOptions } = useDict('sys_dept_status', {
    numeric: true
  })

  const visible = ref(false)
  const depts = ref<Api.System.Dept[]>([])
  const form = reactive<Api.System.DeptForm>({
    id: undefined,
    parentId: undefined,
    name: '',
    sort: 0,
    leader: '',
    phone: '',
    email: '',
    status: 1
  })

  const parentTree = computed(() => {
    const toNode = (d: Api.System.Dept): any => ({
      value: d.id,
      label: d.name,
      children: d.children?.map(toNode) ?? []
    })
    // 顶部提供“顶级部门”虚拟节点(value 与服务端 parentId=0 对应)。
    return [{ value: '0', label: '顶级部门' }, ...depts.value.map(toNode)]
  })

  async function open(row?: Api.System.Dept, parentId?: string) {
    await ensureStatus()
    depts.value = await fetchDepts()
    Object.assign(
      form,
      row ?? {
        id: undefined,
        parentId: parentId ?? undefined,
        name: '',
        sort: 0,
        leader: '',
        phone: '',
        email: '',
        status: 1
      }
    )
    visible.value = true
  }

  async function onSave() {
    if (!form.name.trim()) {
      ElMessage.warning('请填写部门名称')
      return
    }
    const payload: Api.System.DeptForm = { ...form }
    // 上级部门为空/被清空 → 显式提交 parentId=0 移到顶级部门。
    // 不能省略该字段:服务端对缺省字段保持原上级不变,导致“移到顶级”无效。
    if (!payload.parentId) payload.parentId = 0
    if (form.id) {
      await updateDept(form.id, payload)
    } else {
      await createDept(payload)
    }
    ElMessage.success('已保存')
    visible.value = false
    emit('saved')
  }

  defineExpose({ open })
</script>
