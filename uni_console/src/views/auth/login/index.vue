<!-- 登录页面 -->
<template>
  <!-- h-dvh：移动端地址栏收起/展开时高度跟随，避免 100vh 溢出；
       左右/底部安全区避让刘海屏横屏与底部手势条（非刘海设备 env() 为 0） -->
  <div
    class="flex w-full h-dvh pl-[env(safe-area-inset-left)] pr-[env(safe-area-inset-right)] pb-[env(safe-area-inset-bottom)]"
  >
    <LoginLeftView />

    <div class="auth-pane relative flex-1">
      <AuthTopBar />

      <div class="auth-right-wrap">
        <div class="form">
          <h3 class="title">{{ '欢迎回来' }}</h3>

          <ElForm
            ref="formRef"
            class="login-form"
            :model="formData"
            :rules="rules"
            @keyup.enter="handleSubmit"
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

            <div class="submit-row">
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
  @use '@styles/core/breakpoints.scss' as *;

  /* 表单列的三档版式（与 LoginLeftView 的品牌画幅同一条高度轴呼吸）
   *
   * 桌面（高 > short）：440×650 的两列构图，表单列在右半区居中
   * 宽矮（高 ≤ short）：650 的**定高**在矮视口里是病的根 —— `inset-0 + m-auto` 会把
   *   650 高的盒子居中到视口外（844×390 实测 top = -130px），标题被裁到屏幕上方，
   *   底部又空出一大截。这一档改由内容定高，并交给 .auth-pane 在纵向居中
   *   （只把定高拆掉还不够：内容一路贴顶、底部空出一整屏，同样是「节奏散」）。
   * 短横（高 ≤ short 且宽 ≤ compact 且 ≥ phone）：品牌列收成左栏（见 LoginLeftView），
   *   表单列在剩下的右栏居中，并让开顶栏那一行控件。
   * 手机横屏（高 ≤ phoneShort）：触屏触靶 —— 输入框 / 登录键 / 记住密码行一律 44px。 */
  .auth-pane {
    @include respond-height-at-most('short') {
      display: flex;
      align-items: center;
      justify-content: center;
    }

    @include respond-height-at-most('short') {
      @include respond-at-most('compact') {
        @include respond-at-least('phone') {
          // 顶栏（18px 顶距 + 32/44px 行高）之下再让 8px 呼吸，底部留 12px。
          // 写成**容器内边距**而不是表单列的 top/bottom：居中带随之内收，
          // 表单列在「顶栏之下、屏幕底之上」这条带里居中，两头都不贴边。
          padding-top: 58px;
          padding-bottom: 12px;
        }
      }
    }
  }

  .auth-right-wrap {
    /* 组件高度旋钮（el-ui 的 `.el-button--default` 用 !important 吃这个变量：
       桌面 36 / 触屏 40）。登录页要让按钮对齐输入框的 40，手机横屏再抬到 44 ——
       在本列内重设变量即可，不必再写一条 !important 去跟全局拼优先级。 */
    --el-component-custom-height: 40px;

    /* 桌面档节奏：模板里原有两条内联间距（标题→表单 25px、记住密码→登录 30px），
       收进样式表以便按高度档覆盖，桌面取值与原来一致。 */
    .login-form {
      margin-top: 25px;
    }

    .submit-row {
      margin-top: 30px;
    }

    @include respond-height-at-most('short') {
      position: relative; // 脱离绝对定位，改由 .auth-pane 的 flex 居中（不占 transform，入场滑入不受影响）
      inset: auto;
      margin: 0;
      height: auto;
      max-height: 100%;
      overflow-x: hidden;
      overflow-y: auto;

      .form {
        height: auto;
        padding: 8px 0;
      }

      .title {
        font-size: 30px;
        line-height: 1.25;
      }

      .login-form {
        margin-top: 18px;
      }

      .submit-row {
        margin-top: 20px;
      }

      @include respond-at-most('compact') {
        @include respond-at-least('phone') {
          // 品牌栏已占去左侧，表单列不再吃满整页；两侧各留 16px 兜底窄视口
          max-width: calc(100% - 32px);
        }
      }
    }

    @include respond-height-at-most('phoneShort') {
      --el-component-custom-height: 44px;

      .form {
        padding: 0;
      }

      .title {
        font-size: 24px;
      }

      .login-form {
        margin-top: 14px;
      }

      .custom-height {
        height: 44px;
      }

      :deep(.el-input__wrapper),
      .captcha-img {
        height: 44px;
      }

      :deep(.el-checkbox) {
        height: 44px;
      }

      :deep(.el-form-item) {
        margin-bottom: 12px;
      }
    }

    // 动效可关：入场滑入是装饰性的，用户声明「减少动态效果」时不播放
    @media (prefers-reduced-motion: reduce) {
      animation: none !important;
    }
  }

  :deep(.el-input__wrapper) {
    height: 40px;
  }
</style>
