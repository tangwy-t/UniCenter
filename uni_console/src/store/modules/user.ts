/**
 * 用户状态管理模块
 *
 * 提供用户相关的状态管理
 *
 * ## 主要功能
 *
 * - 用户登录状态管理
 * - 用户信息存储
 * - 访问令牌和刷新令牌管理
 * - 语言设置
 * - 搜索历史记录
 * - 锁屏状态和密码管理
 * - 登出清理逻辑
 *
 * ## 使用场景
 *
 * - 用户登录和认证
 * - 权限验证
 * - 个人信息展示
 * - 多语言切换
 * - 锁屏功能
 * - 搜索历史管理
 *
 * ## 持久化
 *
 * - 使用 localStorage 存储
 * - 存储键：sys-v{version}-user
 * - 登出时自动清理
 *
 * @module store/modules/user
 * @author Art Design Pro Team
 */
import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { router } from '@/router'
import { useSettingStore } from './setting'
import { useWorktabStore } from './worktab'
import { AppRouteRecord } from '@/types/router'
import { RoutesAlias } from '@/router/routesAlias'
import { resetRouterState, rememberPermissionFingerprint } from '@/router/guards/beforeEach'
import { useMenuStore } from './menu'
import { StorageConfig } from '@/utils/storage/storage-config'
import { fetchLogin, fetchGetUserInfo, fetchLogout } from '@/api/auth'
import { useSocketStore } from './socket'

/**
 * 用户状态管理
 * 管理用户登录状态、个人信息、语言设置、搜索历史、锁屏状态等
 */
export const useUserStore = defineStore(
  'userStore',
  () => {
    // 登录状态
    const isLogin = ref(false)
    // 锁屏状态
    const isLock = ref(false)
    // 锁屏密码
    const lockPassword = ref('')
    // 用户信息
    const info = ref<Partial<Api.Auth.UserInfo>>({})
    // 搜索历史记录
    const searchHistory = ref<AppRouteRecord[]>([])
    // 访问令牌
    const accessToken = ref('')
    // 刷新令牌
    const refreshToken = ref('')

    // 计算属性：获取用户信息
    const getUserInfo = computed(() => info.value)
    // 计算属性：获取设置状态
    const getSettingState = computed(() => useSettingStore().$state)
    // 计算属性：获取工作台状态
    const getWorktabState = computed(() => useWorktabStore().$state)

    /**
     * 设置用户信息
     * @param newInfo 新的用户信息
     */
    const setUserInfo = (newInfo: Api.Auth.UserInfo) => {
      info.value = newInfo
    }

    /**
     * 设置登录状态
     * @param status 登录状态
     */
    const setLoginStatus = (status: boolean) => {
      isLogin.value = status
    }

    /**
     * 设置搜索历史
     * @param list 搜索历史列表
     */
    const setSearchHistory = (list: AppRouteRecord[]) => {
      searchHistory.value = list
    }

    /**
     * 设置锁屏状态
     * @param status 锁屏状态
     */
    const setLockStatus = (status: boolean) => {
      isLock.value = status
    }

    /**
     * 设置锁屏密码
     * @param password 锁屏密码
     */
    const setLockPassword = (password: string) => {
      lockPassword.value = password
    }

    /**
     * 设置令牌
     * @param newAccessToken 访问令牌
     * @param newRefreshToken 刷新令牌（可选）
     */
    const setToken = (newAccessToken: string, newRefreshToken?: string) => {
      accessToken.value = newAccessToken
      if (newRefreshToken) {
        refreshToken.value = newRefreshToken
      }
    }

    /**
     * 退出登录
     * 清空所有用户相关状态并跳转到登录页
     * 如果是同一账号重新登录，保留工作台标签页
     */
    const logOut = () => {
      // 断开 WebSocket 连接，清理状态
      useSocketStore().disconnect()
      // 保存当前用户 ID，用于下次登录时判断是否为同一用户。
      // 若 info 已为空(如 token 过期时 401 拦截器触发的登出,此时
      // user/info 可能从未加载或已被清空),无法确定身份,应清除残留值
      // 而非保留旧值,否则下次登录会误判为"同一用户"而不清标签页。
      const currentUserId = info.value.id
      if (currentUserId) {
        localStorage.setItem(StorageConfig.LAST_USER_ID_KEY, String(currentUserId))
      } else {
        localStorage.removeItem(StorageConfig.LAST_USER_ID_KEY)
      }

      // 安全修复（评审 #1）：登出时调用后端 /logout，吊销服务端 refresh
      // token。此前前端只清本地状态、从不调 /logout，导致被盗的 refresh
      // token 在受害者"登出"后仍长期有效、可无限续期。此处用当前 refresh
      // token 尽力而为地通知服务端（失败不阻断本地登出，服务端 token 会
      // 自然过期）。
      const rt = refreshToken.value
      if (rt) {
        fetchLogout().catch(() => {
          // 网络/服务端异常时静默降级：本地登出照常，服务端 refresh 过期兜底。
        })
      }

      // 清空用户信息
      info.value = {}
      // 注意：「记住密码」凭据在登出时**保留**，这样用户勾选过记住密码后，
      // 下次回到登录页仍会自动回填账号密码并默认勾选。凭据仅在用户取消
      // 勾选或登录失败时由登录页清洗（见 views/auth/login/index.vue）。
      // 重置登录状态
      isLogin.value = false
      // 重置锁屏状态
      isLock.value = false
      // 清空锁屏密码
      lockPassword.value = ''
      // 清空访问令牌
      accessToken.value = ''
      // 清空刷新令牌
      refreshToken.value = ''
      // 注意：不清空工作台标签页，等下次登录时根据用户判断
      // 移除iframe路由缓存
      sessionStorage.removeItem('iframeRoutes')
      // 清除“建议改密”软提醒关闭标记:下次登录再次评估
      sessionStorage.removeItem(StorageConfig.PASSWORD_REMINDER_DISMISSED_KEY)
      // 清空主页路径
      useMenuStore().setHomePath('')
      // 重置路由状态
      resetRouterState(500)
      // 跳转到登录页，携带当前路由作为 redirect 参数
      // 注意：登录页路径是 /auth/login，不能与 /login 硬编码比较，
      // 否则在登录页上触发登出时会把 redirect 指向自身，越叠越长导致登录后无法跳转。
      const currentRoute = router.currentRoute.value
      const redirect = currentRoute.path !== RoutesAlias.Login ? currentRoute.fullPath : undefined
      router.push({
        name: 'Login',
        query: redirect ? { redirect } : undefined
      })
    }

    /**
     * 检查并清理工作台标签页
     * 如果不是同一用户登录，清空工作台标签页
     * 应在登录成功后调用
     */
    const checkAndClearWorktabs = () => {
      const lastUserId = localStorage.getItem(StorageConfig.LAST_USER_ID_KEY)
      const currentUserId = info.value.id

      // 无法获取当前用户 ID，跳过检查
      if (!currentUserId) return

      // 首次登录或缓存已清除，保留现有标签页
      if (!lastUserId) {
        return
      }

      // 不同用户登录，清空工作台标签页
      if (String(currentUserId) !== lastUserId) {
        const worktabStore = useWorktabStore()
        worktabStore.opened = []
        worktabStore.keepAliveExclude = []
      }

      // 清除临时存储
      localStorage.removeItem(StorageConfig.LAST_USER_ID_KEY)
    }

    /**
     * 登录：获取令牌 → 拉取用户信息 → 复用工作台标签
     */
    const login = async (params: Api.Auth.LoginParams) => {
      const { accessToken, refreshToken } = await fetchLogin(params)
      setToken(accessToken, refreshToken)
      const userInfo = await fetchGetUserInfo()
      setUserInfo(userInfo)
      // 记录权限基线,供路由守卫判断运行时权限是否发生变化。
      rememberPermissionFingerprint(userInfo.permissions)
      isLogin.value = true
      checkAndClearWorktabs()
      // 登录成功后建立 WebSocket 连接（无 token 时 connect 内部会跳过）
      useSocketStore().connect()
    }

    return {
      isLogin,
      isLock,
      lockPassword,
      info,
      searchHistory,
      accessToken,
      refreshToken,
      getUserInfo,
      getSettingState,
      getWorktabState,
      setUserInfo,
      setLoginStatus,
      setSearchHistory,
      setLockStatus,
      setLockPassword,
      setToken,
      login,
      logOut,
      checkAndClearWorktabs
    }
  },
  {
    persist: {
      key: 'user',
      storage: localStorage
    }
  }
)
