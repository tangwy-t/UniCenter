<template>
  <el-dialog v-model="visible" :title="form.id ? '编辑用户' : '新增用户'" width="560px">
    <el-form ref="formRef" :model="form" :rules="rules" label-width="90px">
      <!-- 基本信息 -->
      <div class="form-section">基本信息</div>
      <el-form-item label="姓名" prop="realName">
        <el-input v-model="form.realName" placeholder="请输入真实姓名" maxlength="64" />
      </el-form-item>
      <el-form-item label="昵称" prop="nickname">
        <el-input v-model="form.nickname" placeholder="请输入昵称" maxlength="64" />
      </el-form-item>
      <el-form-item label="性别">
        <el-radio-group v-model="form.gender">
          <el-radio v-for="opt in genderOptions" :key="opt.value" :value="opt.value">
            {{ opt.label }}
          </el-radio>
        </el-radio-group>
      </el-form-item>
      <el-form-item label="手机号" prop="phone">
        <el-input v-model="form.phone" placeholder="请输入手机号" maxlength="11" />
      </el-form-item>
      <el-form-item label="邮箱" prop="email">
        <el-input v-model="form.email" placeholder="请输入邮箱" />
      </el-form-item>
      <el-form-item label="部门">
        <el-tree-select
          v-model="form.deptId"
          :data="deptTree"
          :props="{ label: 'label', children: 'children' }"
          node-key="value"
          check-strictly
          clearable
          filterable
          placeholder="请选择部门"
          style="width: 100%"
        />
      </el-form-item>

      <!-- 账号信息（仅新增） -->
      <template v-if="!form.id">
        <div class="form-section">账号信息</div>
        <el-form-item label="用户名" prop="username">
          <el-input
            v-model="form.username"
            placeholder="登录用户名（3-64位，字母或数字）"
            maxlength="64"
          />
        </el-form-item>
        <el-form-item label="密码" prop="password">
          <el-input
            v-model="form.password"
            type="password"
            show-password
            placeholder="登录密码（6-64位）"
            maxlength="64"
          />
        </el-form-item>
        <el-form-item label="角色">
          <el-select
            v-model="form.roleIds"
            multiple
            filterable
            placeholder="请选择角色"
            style="width: 100%"
          >
            <el-option v-for="r in roles" :key="r.id" :label="r.name" :value="r.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="状态">
          <el-radio-group v-model="form.status">
            <el-radio v-for="opt in statusOptions" :key="opt.value" :value="opt.value">
              {{ opt.label }}
            </el-radio>
          </el-radio-group>
        </el-form-item>
      </template>
      <div v-else class="role-hint">
        <ArtSvgIcon icon="ri:information-line" class="text-[14px]" />
        <span>用户角色请在列表「分配角色」中调整</span>
      </div>

      <!-- 其他 -->
      <div class="form-section">其他</div>
      <el-form-item label="备注">
        <el-input
          v-model="form.remark"
          type="textarea"
          :rows="3"
          placeholder="请输入备注"
          maxlength="500"
        />
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
  import { computed, reactive, ref } from 'vue'
  import type { FormInstance, FormRules } from 'element-plus'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtSvgIcon from '@/components/core/base/art-svg-icon/index.vue'
  import { ElMessage } from 'element-plus'
  import { useDict } from '@/hooks/core/useDict'
  import { fetchDepts, fetchAllRoles, createUser, updateUser } from '../api'

  const emit = defineEmits<{ (e: 'saved'): void }>()
  const { ensure: ensureStatus, options: statusOptions } = useDict('sys_normal_disable', {
    numeric: true
  })
  const { ensure: ensureGender, options: genderOptions } = useDict('sys_user_gender')

  const visible = ref(false)
  const formRef = ref<FormInstance>()
  const depts = ref<Api.System.Dept[]>([])
  const roles = ref<Api.System.Role[]>([])
  const form = reactive<Api.System.UserForm>({
    id: undefined,
    username: '',
    password: '',
    realName: '',
    nickname: '',
    gender: '',
    email: '',
    phone: '',
    deptId: undefined,
    roleIds: [],
    status: 1,
    remark: ''
  })

  const rules: FormRules = {
    username: [
      { required: true, message: '请输入用户名', trigger: 'blur' },
      { min: 3, max: 64, message: '长度在 3 到 64 个字符', trigger: 'blur' },
      { pattern: /^[A-Za-z0-9]+$/, message: '用户名只能包含字母和数字', trigger: 'blur' }
    ],
    password: [
      { required: true, message: '请输入密码', trigger: 'blur' },
      { min: 6, max: 64, message: '密码长度 6-64 位', trigger: 'blur' }
    ],
    realName: [{ required: true, message: '请输入姓名', trigger: 'blur' }],
    phone: [{ pattern: /^1[3-9]\d{9}$/, message: '请输入正确的手机号', trigger: 'blur' }],
    email: [{ type: 'email', message: '请输入正确的邮箱地址', trigger: ['blur', 'change'] }]
  }

  const deptTree = computed(() => {
    const toNode = (d: Api.System.Dept): any => ({
      value: d.id,
      label: d.name,
      children: d.children?.map(toNode) ?? []
    })
    return depts.value.map(toNode)
  })

  async function open(row?: Api.System.User) {
    await ensureStatus()
    await ensureGender()
    const [deptList, roleList] = await Promise.all([fetchDepts(), fetchAllRoles()])
    depts.value = deptList
    roles.value = roleList
    Object.assign(
      form,
      row ?? {
        id: undefined,
        username: '',
        password: '',
        realName: '',
        nickname: '',
        gender: '',
        email: '',
        phone: '',
        deptId: undefined,
        roleIds: [],
        status: 1,
        remark: ''
      }
    )
    visible.value = true
  }

  async function onSave() {
    if (!formRef.value) return
    const valid = await formRef.value.validate().catch(() => false)
    if (!valid) return

    // 空字符串一律省略（undefined）：后端 email/phone 是带校验的空指针字段，
    // JSON 里的 "" 会反序列化成"指向空串的指针"，触发 omitempty+email/min 校验失败(400)
    const strip = (v?: string) => (v?.trim() ? v.trim() : undefined)

    if (form.id) {
      await updateUser(form.id, {
        realName: form.realName,
        nickname: strip(form.nickname),
        gender: form.gender || undefined,
        email: strip(form.email),
        phone: strip(form.phone),
        deptId: form.deptId || undefined,
        remark: form.remark
      })
    } else {
      await createUser({
        ...form,
        realName: form.realName,
        nickname: strip(form.nickname),
        gender: form.gender || undefined,
        email: strip(form.email),
        phone: strip(form.phone),
        deptId: form.deptId || undefined,
        remark: form.remark
      })
    }
    ElMessage.success('已保存')
    visible.value = false
    emit('saved')
  }

  defineExpose({ open })
</script>

<style lang="scss" scoped>
  .form-section {
    display: flex;
    align-items: center;
    gap: 6px;
    margin: 2px 0 12px;
    font-size: 13px;
    font-weight: 600;
    color: var(--el-text-color-primary);

    &::before {
      content: '';
      width: 3px;
      height: 13px;
      border-radius: 2px;
      background: var(--el-color-primary);
    }
  }

  .role-hint {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 8px 10px;
    margin-bottom: 12px;
    border-radius: 6px;
    font-size: 12px;
    color: var(--el-color-primary);
    background: var(--el-color-primary-light-9);
  }
</style>
