<!-- 账号设置:基本资料 / 密码安全 双 Tab(保存后通知父级刷新总览) -->
<template>
  <section class="uc-card">
    <ElTabs v-model="activeTab" class="uc-tabs px-5 pt-3 pb-5">
      <ElTabPane name="basic">
        <template #label>
          <span class="uc-tab-label">
            <ArtSvgIcon icon="ri:user-3-line" />
            {{ '基本资料' }}
          </span>
        </template>

        <p class="mb-4 text-xs text-g-500">{{ '维护你的姓名与联系方式' }}</p>
        <ElForm
          ref="profileFormRef"
          :model="profileForm"
          :rules="profileRules"
          label-position="right"
          label-width="70px"
        >
          <ElFormItem label="姓名" prop="realName">
            <ElInput
              v-model="profileForm.realName"
              :placeholder="'请输入真实姓名'"
              :maxlength="64"
              clearable
            />
          </ElFormItem>
          <ElFormItem label="昵称" prop="nickname">
            <ElInput
              v-model="profileForm.nickname"
              :placeholder="'请输入昵称'"
              :maxlength="64"
              clearable
            />
          </ElFormItem>
          <ElFormItem label="性别">
            <ElRadioGroup v-model="profileForm.gender">
              <ElRadio v-for="opt in genderOptions" :key="opt.value" :value="opt.value">
                {{ opt.label }}
              </ElRadio>
            </ElRadioGroup>
          </ElFormItem>
          <ElFormItem label="邮箱" prop="email">
            <ElInput
              v-model="profileForm.email"
              type="email"
              :placeholder="'请输入邮箱'"
              :maxlength="128"
              clearable
            />
          </ElFormItem>
          <ElFormItem label="手机号" prop="phone">
            <ElInput
              v-model="profileForm.phone"
              :placeholder="'请输入手机号'"
              :maxlength="20"
              clearable
            />
          </ElFormItem>

          <div class="mt-1 flex justify-end gap-3">
            <ArtButtonTable
              icon="ri:refresh-line"
              iconClass="bg-g-300/55 text-g-700"
              title="还原"
              @click="onResetProfile"
            />
            <ArtButtonTable
              icon="ri:check-line"
              iconClass="bg-theme/12 text-theme"
              title="保存"
              :disabled="profileSaving"
              @click="onSaveProfile"
            />
          </div>
        </ElForm>
      </ElTabPane>

      <ElTabPane name="security">
        <template #label>
          <span class="uc-tab-label">
            <ArtSvgIcon icon="ri:shield-keyhole-line" />
            {{ '密码安全' }}
          </span>
        </template>

        <p class="mb-4 text-xs text-g-500">{{
          '修改成功后所有会话立即失效，请用新密码重新登录'
        }}</p>
        <ElForm
          ref="pwdFormRef"
          :model="pwdForm"
          :rules="pwdRules"
          label-position="right"
          label-width="110px"
        >
          <ElFormItem label="当前密码" prop="oldPassword">
            <ElInput
              v-model="pwdForm.oldPassword"
              type="password"
              show-password
              autocomplete="current-password"
              :placeholder="'请输入当前密码'"
              :maxlength="64"
            />
          </ElFormItem>
          <ElFormItem label="新密码" prop="newPassword">
            <ElInput
              v-model="pwdForm.newPassword"
              type="password"
              show-password
              autocomplete="new-password"
              :placeholder="'请输入新密码（6-64 位）'"
              :maxlength="64"
            />
            <p class="mt-1.5 text-xs text-g-500">{{ '6-64 位，建议使用字母、数字与符号的组合' }}</p>
          </ElFormItem>
          <ElFormItem label="确认新密码" prop="confirmPassword">
            <ElInput
              v-model="pwdForm.confirmPassword"
              type="password"
              show-password
              autocomplete="new-password"
              :placeholder="'请再次输入新密码'"
              :maxlength="64"
              @keyup.enter="onSubmitPassword"
            />
          </ElFormItem>

          <div class="mt-1 flex justify-end">
            <ArtButtonTable
              icon="ri:check-line"
              iconClass="bg-theme/12 text-theme"
              :title="pwdSaving ? '提交中…' : '确认修改'"
              :disabled="pwdSaving"
              @click="onSubmitPassword"
            />
          </div>
        </ElForm>
      </ElTabPane>
    </ElTabs>
  </section>
</template>

<script setup lang="ts">
  import { onMounted, reactive, ref, watch } from 'vue'
  import { ElMessage, ElMessageBox, type FormInstance, type FormRules } from 'element-plus'
  import { useUserStore } from '@/store/modules/user'
  import { useDict } from '@/hooks/core/useDict'
  import { fetchMyProfile, updateMyProfile, changeMyPassword } from '../../api'

  defineOptions({ name: 'UcAccountSettings' })

  const props = defineProps<{
    /** 已保存的资料快照(来自总览接口),作为表单初值与「还原」目标 */
    profile: { realName: string; nickname: string; email: string; phone: string; gender: string }
  }>()

  const emit = defineEmits<{ saved: [] }>()

  const userStore = useUserStore()
  const { ensure: ensureGender, options: genderOptions } = useDict('sys_user_gender')
  const activeTab = ref('basic')

  /* ── 基本资料 ───────────────────────────────────── */
  const profileFormRef = ref<FormInstance>()
  const profileSaving = ref(false)
  const profileForm = reactive<Api.Auth.UpdateProfileParams>({
    realName: '',
    nickname: '',
    email: '',
    phone: '',
    gender: ''
  })

  const profileRules = reactive<FormRules>({
    realName: [
      { required: true, message: '请输入姓名', trigger: 'blur' },
      { min: 2, max: 64, message: '长度 2-64 个字符', trigger: 'blur' }
    ],
    email: [{ type: 'email', message: '邮箱格式不正确', trigger: ['blur', 'change'] }],
    phone: [
      {
        pattern: /^[0-9+\-\s]{7,20}$/,
        message: '手机号格式不正确',
        trigger: ['blur', 'change']
      }
    ]
  })

  const assignProfileForm = () => {
    profileForm.realName = props.profile.realName || ''
    profileForm.nickname = props.profile.nickname || ''
    profileForm.email = props.profile.email || ''
    profileForm.phone = props.profile.phone || ''
    profileForm.gender = props.profile.gender || ''
  }

  // 总览加载完成/保存后刷新时同步表单初值
  watch(() => props.profile, assignProfileForm, { immediate: true })

  // 性别字典:渲染单选选项
  onMounted(() => {
    ensureGender()
  })

  const onResetProfile = async () => {
    try {
      await ElMessageBox.confirm('放弃未保存的修改？', '提示', {
        type: 'warning',
        confirmButtonText: '确定',
        cancelButtonText: '取消'
      })
      assignProfileForm()
      profileFormRef.value?.clearValidate()
    } catch {
      /* 取消还原,忽略 */
    }
  }

  const onSaveProfile = async () => {
    const valid = await profileFormRef.value?.validate().catch(() => false)
    if (!valid) return
    profileSaving.value = true
    try {
      await updateMyProfile({
        realName: profileForm.realName.trim(),
        nickname: (profileForm.nickname || '').trim(),
        email: profileForm.email.trim(),
        phone: profileForm.phone.trim(),
        gender: profileForm.gender || undefined
      })
      // 回源刷新:同步最新资料到 store(顶部头像菜单即时生效)
      const fresh = await fetchMyProfile()
      userStore.setUserInfo(fresh)
      assignProfileForm()
      ElMessage.success('资料已更新')
      emit('saved')
    } finally {
      profileSaving.value = false
    }
  }

  /* ── 修改密码 ───────────────────────────────────── */
  const pwdFormRef = ref<FormInstance>()
  const pwdSaving = ref(false)
  const pwdForm = reactive({ oldPassword: '', newPassword: '', confirmPassword: '' })

  const pwdRules = reactive<FormRules>({
    oldPassword: [{ required: true, message: '请输入当前密码', trigger: 'blur' }],
    newPassword: [
      { required: true, message: '请输入新密码', trigger: 'blur' },
      { min: 6, max: 64, message: '密码长度需在 6-64 位之间', trigger: 'blur' }
    ],
    confirmPassword: [
      { required: true, message: '请再次输入新密码', trigger: 'blur' },
      {
        validator: (_rule, value: string, callback) => {
          if (value !== pwdForm.newPassword) {
            callback(new Error('两次输入的密码不一致'))
          } else {
            callback()
          }
        },
        trigger: ['blur', 'change']
      }
    ]
  })

  // 新密码变化后,若确认框已填写则即时复校验一致性
  watch(
    () => pwdForm.newPassword,
    () => {
      if (pwdForm.confirmPassword)
        pwdFormRef.value?.validateField('confirmPassword').catch(() => undefined)
    }
  )

  const onSubmitPassword = async () => {
    const valid = await pwdFormRef.value?.validate().catch(() => false)
    if (!valid) return
    pwdSaving.value = true
    try {
      await changeMyPassword({ oldPassword: pwdForm.oldPassword, newPassword: pwdForm.newPassword })
      // 后端已吊销全部会话(含当前 token):提示后本地登出回登录页,
      // 不能再发任何带旧 token 的请求。
      await ElMessageBox.alert(
        '出于安全考虑，当前登录已失效，请使用新密码重新登录。',
        '密码修改成功',
        { type: 'success', confirmButtonText: 'OK' }
      ).catch(() => undefined)
      userStore.logOut()
    } finally {
      pwdSaving.value = false
    }
  }
</script>

<style scoped>
  .uc-tab-label {
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }

  .uc-tabs :deep(.el-tabs__header) {
    margin-bottom: 1.25rem;
  }

  .uc-tabs :deep(.el-tabs__item) {
    font-size: 13px;
  }
</style>
