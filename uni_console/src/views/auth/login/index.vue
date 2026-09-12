<!-- 登录页面 -->
<template>
  <div class="flex w-full h-screen">
    <LoginLeftView />

    <div class="relative flex-1">
      <AuthTopBar />

      <div class="auth-right-wrap">
        <div class="form">
          <h3 class="title">{{ '欢迎回来' }}</h3>
          <p class="sub-title">{{ '输入您的账号和密码登录' }}</p>

          <ElForm
            ref="formRef"
            :model="formData"
            :rules="rules"
            @keyup.enter="handleSubmit"
            style="margin-top: 25px"
          >
            <ElFormItem prop="username">
              <ElInput
                class="custom-height"
                :placeholder="'请输入账号'"
                v-model.trim="formData.username"
              />
            </ElFormItem>

            <ElFormItem prop="password">
              <ElInput
                class="custom-height"
                :placeholder="'请输入密码'"
                v-model.trim="formData.password"
                type="password"
                autocomplete="off"
                show-password
              />
            </ElFormItem>

            <ElFormItem v-if="captchaRequired" prop="captchaCode">
              <div class="flex items-center gap-2">
                <ElInput
                  class="custom-height flex-1"
                  placeholder="请输入验证码"
                  v-model.trim="formData.captchaCode"
                />
                <img
                  class="captcha-img"
                  :src="captchaImage"
                  alt="验证码"
                  title="点击刷新"
                  @click="loadCaptcha"
                />
              </div>
            </ElFormItem>

            <ElFormItem class="remember-row">
              <ElCheckbox v-model="remember">{{ '记住密码' }}</ElCheckbox>
            </ElFormItem>

            <div style="margin-top: 30px">
              <ElButton
                class="w-full custom-height"
                type="primary"
                @click="handleSubmit"
                :loading="loading"
              >
                {{ '登录' }}
              </ElButton>
            </div>
          </ElForm>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
  import { useUserStore } from '@/store/modules/user'
  import { ElMessage, ElNotification, type FormInstance, type FormRules } from 'element-plus'
  import { HttpError } from '@/utils/http/error'
  import { fetchCaptcha } from '@/api/auth'
  import AppConfig from '@/config'
  import { RoutesAlias } from '@/router/routesAlias'
  import {
    loadRememberedLogin,
    saveRememberedLogin,
    clearRememberedLogin
  } from '@/utils/auth/remember-login'

  defineOptions({ name: 'Login' })

  // 后端 apperror.CodeCaptchaRequired = 10004
  const CAPTCHA_REQUIRED_CODE = 10004

  const userStore = useUserStore()
  const router = useRouter()
  const route = useRoute()

  const formRef = ref<FormInstance>()
  const formData = reactive({ username: '', password: '', captchaCode: '' })

  // 记住密码：挂载时回填，登录成功后按勾选状态保存/清除
  const remember = ref(false)

  onMounted(() => {
    const saved = loadRememberedLogin()
    if (saved) {
      formData.username = saved.username
      formData.password = saved.password
      remember.value = true
    }
  })

  const captchaRequired = ref(false)
  const captchaKey = ref('')
  const captchaImage = ref('')

  const rules = computed<FormRules>(() => {
    const r: FormRules = {
      username: [{ required: true, message: '请输入账号', trigger: 'blur' }],
      password: [{ required: true, message: '请输入密码', trigger: 'blur' }]
    }
    if (captchaRequired.value) {
      r.captchaCode = [{ required: true, message: '请输入验证码', trigger: 'blur' }]
    }
    return r
  })

  const loading = ref(false)

  // 获取验证码
  const loadCaptcha = async () => {
    const res = await fetchCaptcha()
    captchaKey.value = res.captchaKey
    captchaImage.value = res.captchaImage
    formData.captchaCode = ''
  }

  // 计算登录成功后的跳转目标
  // 仅接受站内路径，且不跳回登录页，避免 redirect 被异常累积后登录无法跳转/死循环
  const getRedirectTarget = (): string => {
    const raw = route.query.redirect
    const r = Array.isArray(raw) ? String(raw[0] ?? '') : typeof raw === 'string' ? raw : ''

    if (!r || !r.startsWith('/') || r.startsWith('//')) {
      return '/'
    }
    // 回跳登录/注册等鉴权页时，落到首页，避免死循环
    if (
      r === RoutesAlias.Login ||
      r.startsWith(`${RoutesAlias.Login}?`) ||
      r.startsWith(`${RoutesAlias.Login}/`)
    ) {
      return '/'
    }
    return r
  }

  // 登录
  const handleSubmit = async () => {
    if (!formRef.value) return
    try {
      const valid = await formRef.value.validate()
      if (!valid) return

      loading.value = true
      await userStore.login({
        username: formData.username,
        password: formData.password,
        captchaKey: captchaRequired.value ? captchaKey.value : undefined,
        captchaCode: captchaRequired.value ? formData.captchaCode : undefined
      })

      // 登录成功后按勾选状态保存/清除记住的凭据
      if (remember.value) {
        saveRememberedLogin(formData.username, formData.password)
      } else {
        clearRememberedLogin()
      }

      showLoginSuccessNotice()

      router.push(getRedirectTarget())
    } catch (error) {
      if (error instanceof HttpError && error.bizCode === CAPTCHA_REQUIRED_CODE) {
        // 失败次数达到阈值，后端要求验证码
        captchaRequired.value = true
        await loadCaptcha()
        ElMessage.warning(error.message)
      } else if (error instanceof HttpError) {
        ElMessage.error(error.message)
        // 验证码错误/过期时刷新一张
        if (captchaRequired.value) await loadCaptcha()
      } else {
        console.error('[Login] error:', error)
      }
    } finally {
      loading.value = false
    }
  }

  // 登录成功提示
  const showLoginSuccessNotice = () => {
    const systemName = AppConfig.systemInfo.name
    setTimeout(() => {
      ElNotification({
        title: '登录成功',
        type: 'success',
        duration: 2500,
        zIndex: 10000,
        message: `欢迎回来, ${systemName}!`
      })
    }, 1000)
  }
</script>

<style scoped>
  @import './style.css';

  .captcha-img {
    height: 40px;
    width: auto;
    max-width: 120px;
    cursor: pointer;
    border: 1px solid var(--art-gray-300);
    border-radius: 4px;
  }

  /* 记住密码勾选框：收紧表单项间距，贴近密码框 */
  .remember-row {
    margin-bottom: 12px;
  }
</style>

<style lang="scss" scoped>
  :deep(.el-input__wrapper) {
    height: 40px;
  }
</style>
